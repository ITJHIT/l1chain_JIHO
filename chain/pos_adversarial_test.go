package chain

import (
	"errors"
	"testing"

	"l1chain/consensus"
	"l1chain/core"
	"l1chain/pos"
)

// This file is PR10's own redteam/adversarial coverage for M8 (PoS): the ONE
// case not already exercised by a required wiring-PR test -- a FORGED
// signature (correct identity claimed, but the signature itself does not
// actually come from that identity), as opposed to a merely WRONG identity
// claim. The other categories the M8 plan names are already proven by
// required tests in pos_wiring_test.go:
//   - wrong-proposer block:            TestPoSWrongProposerBlockRejected (PR5)
//   - double-propose:                  TestPoSDoubleProposeDetectedAndJailed (PR7)
//   - double-attest:                   TestPoSDoubleAttestDetectedAndJailed (PR7)
//   - finality-conflicting heavier branch: TestPoSFinalitySafetyRejectsConflictingHeavierBranch (PR6)
//
// artifacts/m8-pos-redteam-report.json cites all six by name rather than
// duplicating them here.

// TestAdvPoS01ForgedProposerSignatureRejected proves AddBlock (PoS mode)
// rejects a block whose Coinbase correctly names the SELECTED proposer, but
// whose ProposerSig was produced by a DIFFERENT key (or is not a real
// signature at all) -- a strictly different failure mode than
// TestPoSWrongProposerBlockRejected, which uses a genuine signature from the
// wrong identity.
func TestAdvPoS01ForgedProposerSignatureRejected(t *testing.T) {
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
		t.Fatalf("SelectProposer: %v", err)
	}

	root, err := chainA.CandidateStateRoot(nil, selected.Address, acceptAll)
	if err != nil {
		t.Fatalf("CandidateStateRoot: %v", err)
	}
	h := core.Header{Height: 1, PrevHash: gb.Hash(), Coinbase: selected.Address, Timestamp: 1, StateRoot: root}

	// (a) Signed with a DIFFERENT validator's real key: the Coinbase claim
	// (identity) is correct, but the signature does not actually come from
	// that identity.
	var forger pos.Key
	for a, k := range keys {
		if a != selected.Address {
			forger = k
			break
		}
	}
	forged := core.Block{Header: h}
	forged.Header.MerkleRoot = forged.TxRoot()
	signingHash := forged.Header.SigningHash()
	forged.Header.ProposerSig = forger.Sign(signingHash[:], pos.DST(chainA.ChainID()))

	if err := chainA.AddBlock(forged, acceptAll); !errors.Is(err, ErrBadProposerSig) {
		t.Fatalf("chainA.AddBlock (forged proposer sig) = %v, want ErrBadProposerSig", err)
	}
	if err := chainB.AddBlock(forged, acceptAll); !errors.Is(err, ErrBadProposerSig) {
		t.Fatalf("chainB.AddBlock (forged proposer sig) = %v, want ErrBadProposerSig", err)
	}

	// (b) Fixed-length garbage: not even a real signature. Its own distinct
	// ProposerSig bytes give this block a different hash than (a)'s, so this
	// is a genuinely separate AddBlock call, not a duplicate.
	garbage := core.Block{Header: h}
	garbage.Header.MerkleRoot = garbage.TxRoot()
	garbage.Header.ProposerSig = make([]byte, pos.SignatureSize)
	if err := chainA.AddBlock(garbage, acceptAll); !errors.Is(err, ErrBadProposerSig) {
		t.Fatalf("chainA.AddBlock (garbage proposer sig) = %v, want ErrBadProposerSig", err)
	}

	headA := chainA.Head()
	if headA.Hash() != gb.Hash() {
		t.Fatal("chainA head advanced on a forged/garbage-signature block")
	}
}

// TestAdvPoS02ForgedAttestationSignatureRejected proves AddBlock (PoS mode)
// rejects a block carrying an attestation tx whose From correctly names a
// registered validator, but whose embedded BLS signature was produced by a
// DIFFERENT validator's key -- the attestation analog of case 1 above.
func TestAdvPoS02ForgedAttestationSignatureRejected(t *testing.T) {
	sender := addr(1)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	vs, keys := posTestValidators(t)

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

	claimedValidator := addr(10)
	forgerKey := keys[addr(11)] // a DIFFERENT validator's real key
	forgedSig := forgerKey.Sign(checkpointHash[:], pos.DST(c.ChainID()))
	tx := core.Transaction{
		From:      claimedValidator,
		To:        pos.AttestAddress,
		Nonce:     0,
		ChainID:   c.ChainID(),
		Data:      pos.EncodeAttest(pos.CheckpointInterval, checkpointHash, forgedSig),
		Signature: []byte{1},
	}
	b := proposePoSBlock(t, c, vs, keys, parent, []core.Transaction{tx})

	if err := c.AddBlock(b, acceptAll); !errors.Is(err, ErrBadAttestation) {
		t.Fatalf("AddBlock (forged attestation sig) = %v, want ErrBadAttestation", err)
	}
	if got := c.Head().Header.Height; got != pos.CheckpointInterval {
		t.Fatalf("head advanced past the checkpoint on a block with a forged attestation: height=%d, want %d", got, pos.CheckpointInterval)
	}
}
