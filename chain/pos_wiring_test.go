package chain

import (
	"errors"
	"testing"

	"l1chain/consensus"
	"l1chain/core"
	"l1chain/pos"
)

// posTestValidators builds a 3-validator PoS validator set with distinct
// stakes (10/20/70) so pos.SelectProposer's cumulative-stake walk is
// genuinely exercised across a block sequence, plus a map from address to
// that validator's real BLS signing key (needed to actually propose/sign
// blocks in these tests).
func posTestValidators(t *testing.T) (*pos.ValidatorSet, map[core.Address]pos.Key) {
	t.Helper()
	addrs := []core.Address{addr(10), addr(11), addr(12)}
	stakes := []uint64{10, 20, 70}
	keys := make(map[core.Address]pos.Key, 3)
	infos := make([]pos.ValidatorInfo, 3)
	for i, a := range addrs {
		k, err := pos.NewKey()
		if err != nil {
			t.Fatalf("pos.NewKey[%d]: %v", i, err)
		}
		keys[a] = k
		infos[i] = pos.ValidatorInfo{Address: a, BLSPubKey: k.PubKey(), Stake: stakes[i]}
	}
	vs, err := pos.NewValidatorSet(infos)
	if err != nil {
		t.Fatalf("pos.NewValidatorSet: %v", err)
	}
	return vs, keys
}

// proposePoSBlock builds, signs (with the real selected validator's BLS
// key), and returns a valid PoS block extending parent -- the PoS analog of
// exchange_consensus_test.go's mineExchangeBlock: instead of consensus.Mine
// searching for a nonce, it computes the SAME pos.SelectProposer result
// AddBlock will independently re-derive, and signs with that validator's
// real key.
func proposePoSBlock(t *testing.T, c *Chain, vs *pos.ValidatorSet, keys map[core.Address]pos.Key, parent core.Block, txs []core.Transaction) core.Block {
	t.Helper()
	height := parent.Header.Height + 1
	active, total := vs.EffectiveStake(nil)
	seed := pos.ProposerSeed(parent.Hash(), height)
	selected, err := pos.SelectProposer(active, total, seed)
	if err != nil {
		t.Fatalf("pos.SelectProposer: %v", err)
	}
	root, err := c.CandidateStateRoot(txs, selected.Address, acceptAll)
	if err != nil {
		t.Fatalf("CandidateStateRoot: %v", err)
	}
	h := core.Header{
		Height:    height,
		PrevHash:  parent.Hash(),
		Coinbase:  selected.Address,
		Timestamp: int64(height),
		StateRoot: root,
	}
	b := core.Block{Header: h, Txs: txs}
	b.Header.MerkleRoot = b.TxRoot()
	signingHash := b.Header.SigningHash()
	key, ok := keys[selected.Address]
	if !ok {
		t.Fatalf("no test key registered for selected proposer %x", selected.Address)
	}
	b.Header.ProposerSig = key.Sign(signingHash[:], pos.DST(c.ChainID()))
	return b
}

// TestPoSProposerSelectionAndStateRootAgreeAcrossIndependentChains is PR5's
// own required determinism test (non-negotiable per the M8 plan, mirroring
// M7 PR6's own precedent exactly): two independent *Chain instances built
// from an identical PoS genesis and validator set; chainA proposes a real
// block sequence through the actual SelectProposer + BLS-sign +
// CandidateStateRoot + AddBlock path; chainB only ever validates via
// AddBlock, never calling CandidateStateRoot or SelectProposer to build a
// block itself. Both must agree on StateRoot after every block, and an
// independent re-derivation of SelectProposer on chainB's own side must
// agree with the proposer chainA actually used -- proving determinism, not
// just "both chains happened to accept the block I built."
func TestPoSProposerSelectionAndStateRootAgreeAcrossIndependentChains(t *testing.T) {
	sender := addr(1)
	recipient := addr(2)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	vs, keys := posTestValidators(t)

	g := Genesis{Alloc: alloc, Difficulty: 0, Timestamp: 0}
	gb := g.ToBlock()

	chainA := NewChain(gb, alloc)
	if err := chainA.SetConsensusMode(consensus.PoS, vs); err != nil {
		t.Fatalf("chainA.SetConsensusMode: %v", err)
	}
	chainB := NewChain(gb, alloc)
	if err := chainB.SetConsensusMode(consensus.PoS, vs); err != nil {
		t.Fatalf("chainB.SetConsensusMode: %v", err)
	}

	blocks := [][]core.Transaction{
		nil,
		{{From: sender, To: recipient, Value: 100, Nonce: 0, ChainID: DefaultChainID, Signature: []byte{1}}},
		nil,
		{{From: sender, To: recipient, Value: 50, Nonce: 1, ChainID: DefaultChainID, Signature: []byte{1}}},
		nil,
	}

	parent := gb
	for i, txs := range blocks {
		b := proposePoSBlock(t, chainA, vs, keys, parent, txs)
		if err := chainA.AddBlock(b, acceptAll); err != nil {
			t.Fatalf("block %d: chainA rejected its own proposed block: %v", i+1, err)
		}
		if err := chainB.AddBlock(b, acceptAll); err != nil {
			t.Fatalf("block %d: chainB (independent instance) rejected chainA's block: %v", i+1, err)
		}
		if got, want := chainB.State().StateRoot(), chainA.State().StateRoot(); got != want {
			t.Fatalf("block %d: chainB's root %x != chainA's root %x", i+1, got, want)
		}

		// Independent re-derivation on chainB's own validator set/view --
		// SelectProposer is a pure function, so this must match the proposer
		// chainA actually used, proving determinism rather than lucky agreement.
		activeB, totalB := chainB.ValidatorSet().EffectiveStake(nil)
		gotSelected, err := pos.SelectProposer(activeB, totalB, pos.ProposerSeed(parent.Hash(), b.Header.Height))
		if err != nil {
			t.Fatalf("block %d: chainB SelectProposer: %v", i+1, err)
		}
		if gotSelected.Address != b.Header.Coinbase {
			t.Fatalf("block %d: chainB independently selected proposer %x, chainA's block used %x", i+1, gotSelected.Address, b.Header.Coinbase)
		}

		parent = b
	}

	if chainA.State().StateRoot().IsZero() {
		t.Fatal("expected a non-zero state root after real PoS transaction activity")
	}
	if got := chainA.State().GetAccount(recipient).Balance; got != 150 {
		t.Fatalf("recipient balance = %d, want 150 (both transfers applied)", got)
	}
}

// TestPoSWrongProposerBlockRejected proves AddBlock (PoS mode) rejects a
// block whose Coinbase/ProposerSig come from a validator OTHER than the one
// pos.SelectProposer deterministically picked for that height -- identically
// on two independent chain instances, so this is a shared consensus rule,
// not an artifact of one chain's own bookkeeping.
func TestPoSWrongProposerBlockRejected(t *testing.T) {
	sender := addr(1)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	vs, keys := posTestValidators(t)

	g := Genesis{Alloc: alloc, Difficulty: 0, Timestamp: 0}
	gb := g.ToBlock()

	chainA := NewChain(gb, alloc)
	if err := chainA.SetConsensusMode(consensus.PoS, vs); err != nil {
		t.Fatalf("chainA.SetConsensusMode: %v", err)
	}
	chainB := NewChain(gb, alloc)
	if err := chainB.SetConsensusMode(consensus.PoS, vs); err != nil {
		t.Fatalf("chainB.SetConsensusMode: %v", err)
	}

	active, total := vs.EffectiveStake(nil)
	selected, err := pos.SelectProposer(active, total, pos.ProposerSeed(gb.Hash(), 1))
	if err != nil {
		t.Fatalf("pos.SelectProposer: %v", err)
	}
	// Find any OTHER validator -- guaranteed to exist, posTestValidators
	// always registers 3.
	var wrong pos.ValidatorInfo
	for a := range keys {
		if a != selected.Address {
			info, ok := vs.ByAddress(a)
			if !ok {
				t.Fatalf("test setup: %x missing from validator set", a)
			}
			wrong = info
			break
		}
	}

	root, err := chainA.CandidateStateRoot(nil, wrong.Address, acceptAll)
	if err != nil {
		t.Fatalf("CandidateStateRoot: %v", err)
	}
	h := core.Header{Height: 1, PrevHash: gb.Hash(), Coinbase: wrong.Address, Timestamp: 1, StateRoot: root}
	b := core.Block{Header: h}
	b.Header.MerkleRoot = b.TxRoot()
	signingHash := b.Header.SigningHash()
	b.Header.ProposerSig = keys[wrong.Address].Sign(signingHash[:], pos.DST(chainA.ChainID()))

	if err := chainA.AddBlock(b, acceptAll); !errors.Is(err, ErrWrongProposer) {
		t.Fatalf("chainA.AddBlock (wrong proposer) = %v, want ErrWrongProposer", err)
	}
	if err := chainB.AddBlock(b, acceptAll); !errors.Is(err, ErrWrongProposer) {
		t.Fatalf("chainB.AddBlock (wrong proposer) = %v, want ErrWrongProposer", err)
	}
	headA := chainA.Head()
	if headA.Hash() != gb.Hash() {
		t.Fatal("chainA head advanced on a wrong-proposer block")
	}
	headB := chainB.Head()
	if headB.Hash() != gb.Hash() {
		t.Fatal("chainB head advanced on a wrong-proposer block")
	}
}
