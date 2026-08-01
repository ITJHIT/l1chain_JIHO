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

// attestTx builds a checkpoint-attestation transaction from a validator,
// mirroring pos.EncodeAttest's own doc comment: the BLS signature covers
// targetHash under this chain's DST, exactly what pos/attestation_test.go's
// own round-trip test signs. Uses a placeholder ECDSA signature ([]byte{1})
// since every test in this file drives blocks through acceptAll, matching
// this package's existing tx()/exOrderTx() helper convention.
func attestTx(chainID uint64, from core.Address, nonce uint64, blsKey pos.Key, targetHeight uint64, targetHash core.Hash) core.Transaction {
	sig := blsKey.Sign(targetHash[:], pos.DST(chainID))
	return core.Transaction{
		From:      from,
		To:        pos.AttestAddress,
		Nonce:     nonce,
		ChainID:   chainID,
		Data:      pos.EncodeAttest(targetHeight, targetHash, sig),
		Signature: []byte{1},
	}
}

// TestPoSFinalitySafetyRejectsConflictingHeavierBranch is PR6's own required
// safety test (non-negotiable per the M8 plan): two independent chains
// independently reach an identical finalized checkpoint via >=2/3-stake
// attestations; a block extending genesis directly -- forking BEFORE the
// finalized checkpoint -- is rejected on BOTH instances, despite being a
// completely validly-proposer-signed PoS block in every other respect.
// Because the finality gate runs before any other validation (see
// respectsFinality's own doc comment), a conflicting branch can never even
// begin to accumulate weight, let alone become heavier than the canonical
// one -- a stronger safety property than merely losing a td comparison at
// reorg time.
func TestPoSFinalitySafetyRejectsConflictingHeavierBranch(t *testing.T) {
	sender := addr(1)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	vs, keys := posTestValidators(t) // stakes 10/20/70, total 100

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

	// Drive both chains to a real checkpoint height with ordinary blocks --
	// a validator can only attest to a block it has actually seen, so the
	// checkpoint block's own attestations must ride in a LATER block,
	// referencing its real, already-known hash (never its own -- a block
	// cannot reference a hash that is not yet determined while its own Txs,
	// which feed MerkleRoot, are still being decided).
	parent := gb
	var checkpointBlock core.Block
	for h := uint64(1); h <= pos.CheckpointInterval; h++ {
		b := proposePoSBlock(t, chainA, vs, keys, parent, nil)
		if err := chainA.AddBlock(b, acceptAll); err != nil {
			t.Fatalf("chainA block %d: %v", h, err)
		}
		if err := chainB.AddBlock(b, acceptAll); err != nil {
			t.Fatalf("chainB block %d: %v", h, err)
		}
		checkpointBlock = b
		parent = b
	}
	checkpointHash := checkpointBlock.Hash()

	// v2+v3 (stake 20+70=90) attest to the real checkpoint hash -- crosses
	// 2/3 of the total 100 stake.
	var attestTxs []core.Transaction
	for _, a := range []core.Address{addr(11), addr(12)} {
		attestTxs = append(attestTxs, attestTx(chainA.ChainID(), a, 0, keys[a], pos.CheckpointInterval, checkpointHash))
	}
	bAttest := proposePoSBlock(t, chainA, vs, keys, parent, attestTxs)
	if err := chainA.AddBlock(bAttest, acceptAll); err != nil {
		t.Fatalf("chainA attestation block: %v", err)
	}
	if err := chainB.AddBlock(bAttest, acceptAll); err != nil {
		t.Fatalf("chainB attestation block: %v", err)
	}

	if got, want := chainA.FinalizedHeight(), uint64(pos.CheckpointInterval); got != want {
		t.Fatalf("chainA.FinalizedHeight() = %d, want %d", got, want)
	}
	if got := chainA.FinalizedHash(); got != checkpointHash {
		t.Fatalf("chainA.FinalizedHash() = %x, want %x", got, checkpointHash)
	}
	if chainB.FinalizedHeight() != chainA.FinalizedHeight() || chainB.FinalizedHash() != chainA.FinalizedHash() {
		t.Fatal("chainB's finality state disagrees with chainA's -- determinism broken")
	}

	// A distinct block extending genesis directly (height 1, but carrying a
	// transfer so its StateRoot/MerkleRoot -- and therefore hash -- differ
	// from the real height-1 block already in both chains, avoiding
	// ErrDuplicateBlock): forks BEFORE the finalized checkpoint. Note
	// proposePoSBlock's own CandidateStateRoot call computes this StateRoot
	// against chainA's CURRENT (far-advanced) head rather than genesis, so
	// it would not actually match what AddBlock's own from-genesis
	// re-derivation expects -- but that never matters here, because
	// respectsFinality rejects rogue before state-root validation is ever
	// reached (see AddBlock's own ordering).
	rogueTxs := []core.Transaction{{From: sender, To: addr(99), Value: 1, Nonce: 0, ChainID: chainA.ChainID(), Signature: []byte{1}}}
	rogue := proposePoSBlock(t, chainA, vs, keys, gb, rogueTxs)
	if err := chainA.AddBlock(rogue, acceptAll); !errors.Is(err, ErrConflictsWithFinalized) {
		t.Fatalf("chainA.AddBlock (conflicts with finality) = %v, want ErrConflictsWithFinalized", err)
	}
	if err := chainB.AddBlock(rogue, acceptAll); !errors.Is(err, ErrConflictsWithFinalized) {
		t.Fatalf("chainB.AddBlock (conflicts with finality) = %v, want ErrConflictsWithFinalized", err)
	}
}

// TestPoSBelowTwoThirdsStakeDoesNotFinalize is PR6's negative control
// (matching this repo's own "relational, not hardcoded" test ethos): stake
// attesting to just under 2/3 leaves finality untouched; the one additional
// attestation that crosses the threshold is what actually flips it -- proven
// as a before/after pair, not a bare assertion of a magic number.
func TestPoSBelowTwoThirdsStakeDoesNotFinalize(t *testing.T) {
	sender := addr(1)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	vs, keys := posTestValidators(t) // stakes: addr(10)=10, addr(11)=20, addr(12)=70, total 100

	g := Genesis{Alloc: alloc, Difficulty: 0, Timestamp: 0}
	gb := g.ToBlock()
	c := NewChain(gb, alloc)
	if err := c.SetConsensusMode(consensus.PoS, vs); err != nil {
		t.Fatalf("SetConsensusMode: %v", err)
	}

	parent := gb
	var checkpointBlock core.Block
	for h := uint64(1); h <= pos.CheckpointInterval; h++ {
		b := proposePoSBlock(t, c, vs, keys, parent, nil)
		if err := c.AddBlock(b, acceptAll); err != nil {
			t.Fatalf("block %d: %v", h, err)
		}
		checkpointBlock = b
		parent = b
	}
	checkpointHash := checkpointBlock.Hash()

	// addr(10) alone (stake 10/100 = 10%) is far short of 2/3.
	tx1 := attestTx(c.ChainID(), addr(10), 0, keys[addr(10)], pos.CheckpointInterval, checkpointHash)
	b1 := proposePoSBlock(t, c, vs, keys, parent, []core.Transaction{tx1})
	if err := c.AddBlock(b1, acceptAll); err != nil {
		t.Fatalf("attest block 1 (addr(10) only, 10%% stake): %v", err)
	}
	if !c.FinalizedHash().IsZero() {
		t.Fatalf("finalized after only 10%% stake attested: height=%d hash=%x", c.FinalizedHeight(), c.FinalizedHash())
	}

	// addr(12) (stake 70) added on top: 10+70=80/100 = 80%, crosses 2/3.
	tx2 := attestTx(c.ChainID(), addr(12), 0, keys[addr(12)], pos.CheckpointInterval, checkpointHash)
	b2 := proposePoSBlock(t, c, vs, keys, b1, []core.Transaction{tx2})
	if err := c.AddBlock(b2, acceptAll); err != nil {
		t.Fatalf("attest block 2 (addr(12) added, 80%% stake): %v", err)
	}
	if got := c.FinalizedHash(); got != checkpointHash {
		t.Fatalf("FinalizedHash() after crossing 2/3 = %x, want %x", got, checkpointHash)
	}
	if got := c.FinalizedHeight(); got != pos.CheckpointInterval {
		t.Fatalf("FinalizedHeight() after crossing 2/3 = %d, want %d", got, pos.CheckpointInterval)
	}
}
