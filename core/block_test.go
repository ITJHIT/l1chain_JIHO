package core

import (
	"encoding/binary"
	"testing"
)

func TestTxHashChangesWithSignature(t *testing.T) {
	base := Transaction{Value: 5, Nonce: 1}
	signing := base.SigningHash()
	base.Signature = []byte{0xde, 0xad}
	if base.SigningHash() != signing {
		t.Fatal("SigningHash must exclude the signature")
	}
	if base.Hash() == signing {
		t.Fatal("full Hash must include the signature")
	}
}

func TestTxHashFieldSensitivity(t *testing.T) {
	a := Transaction{Value: 5, Nonce: 1}
	b := Transaction{Value: 6, Nonce: 1}
	if a.SigningHash() == b.SigningHash() {
		t.Fatal("different values must produce different signing hashes")
	}
}

// TestTxHashFieldSensitivityGasFeeCapGasTipCap proves the M9 fee fields are
// genuinely signed over -- a tampered GasFeeCap or GasTipCap must change
// SigningHash, exactly like GasLimit already does, so neither can be altered
// post-signature without invalidating the signature.
func TestTxHashFieldSensitivityGasFeeCapGasTipCap(t *testing.T) {
	base := Transaction{Value: 5, Nonce: 1, GasLimit: 100, GasFeeCap: 10, GasTipCap: 2}
	signing := base.SigningHash()

	feeCapChanged := base
	feeCapChanged.GasFeeCap = 11
	if feeCapChanged.SigningHash() == signing {
		t.Fatal("SigningHash must change when GasFeeCap changes")
	}

	tipCapChanged := base
	tipCapChanged.GasTipCap = 3
	if tipCapChanged.SigningHash() == signing {
		t.Fatal("SigningHash must change when GasTipCap changes")
	}
}

// TestTxSigningHashIncludesGasFeeCapAndGasTipCap independently re-derives the
// M9 10-field encoding by hand (mirrors
// TestEmptyProposerSigLeavesHashFormulaUnchanged's own re-derivation style
// for Header/ProposerSig) rather than merely asserting by inspection that the
// new fields were added somewhere in the preimage.
func TestTxSigningHashIncludesGasFeeCapAndGasTipCap(t *testing.T) {
	tx := Transaction{
		From:      Address{1},
		To:        Address{2},
		Value:     100,
		Nonce:     3,
		GasLimit:  50000,
		ChainID:   1337,
		GasFeeCap: 20,
		GasTipCap: 5,
		Data:      []byte("payload"),
	}
	var buf []byte
	buf = append(buf, tx.From[:]...)
	buf = append(buf, tx.To[:]...)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], tx.Value)
	buf = append(buf, n[:]...)
	binary.BigEndian.PutUint64(n[:], tx.Nonce)
	buf = append(buf, n[:]...)
	binary.BigEndian.PutUint64(n[:], tx.GasLimit)
	buf = append(buf, n[:]...)
	binary.BigEndian.PutUint64(n[:], tx.ChainID)
	buf = append(buf, n[:]...)
	binary.BigEndian.PutUint64(n[:], tx.GasFeeCap)
	buf = append(buf, n[:]...)
	binary.BigEndian.PutUint64(n[:], tx.GasTipCap)
	buf = append(buf, n[:]...)
	buf = append(buf, tx.Data...)

	want := SumHash(buf)
	if got := tx.SigningHash(); got != want {
		t.Fatalf("SigningHash() = %x, want %x (independently re-derived 10-field encoding)", got, want)
	}

	tx.Signature = []byte{0xaa, 0xbb}
	wantWithSig := SumHash(append(append([]byte{}, buf...), tx.Signature...))
	if got := tx.Hash(); got != wantWithSig {
		t.Fatalf("Hash() = %x, want %x", got, wantWithSig)
	}
}

func TestBlockTxRootMatchesMerkle(t *testing.T) {
	b := Block{Txs: []Transaction{{Value: 1}, {Value: 2}}}
	leaves := []Hash{b.Txs[0].Hash(), b.Txs[1].Hash()}
	if b.TxRoot() != MerkleRoot(leaves) {
		t.Fatal("block TxRoot must equal merkle root of tx hashes")
	}
}

func TestBlockHashDependsOnNonce(t *testing.T) {
	h := Header{Height: 1, Difficulty: 4, Nonce: 0}
	first := h.Hash()
	h.Nonce = 1
	if h.Hash() == first {
		t.Fatal("block hash must change with the PoW nonce")
	}
}

// TestHeaderSigningHashExcludesProposerSig mirrors
// TestTxHashChangesWithSignature's exact shape for Header/ProposerSig (M8):
// a PoS proposer signs SigningHash(), so the signature itself must never be
// part of what it signs over, and the full Hash() must still change once
// ProposerSig is set.
func TestHeaderSigningHashExcludesProposerSig(t *testing.T) {
	h := Header{Height: 1, Difficulty: 0, Coinbase: Address{1}}
	signing := h.SigningHash()
	h.ProposerSig = []byte{0xde, 0xad, 0xbe, 0xef}
	if h.SigningHash() != signing {
		t.Fatal("SigningHash must exclude ProposerSig")
	}
	if h.Hash() == signing {
		t.Fatal("full Hash must include ProposerSig")
	}
}

// TestEmptyProposerSigLeavesHashFormulaUnchanged proves the M8 field
// addition is a genuine no-op for every PoW block (empty ProposerSig,
// forever -- see ProposerSig's own doc comment): Hash() must equal
// SumHash of the exact same 8-field encoding preimage() always produced
// before ProposerSig existed, independently re-derived here rather than
// merely asserted by inspection.
func TestEmptyProposerSigLeavesHashFormulaUnchanged(t *testing.T) {
	h := Header{
		Height:     7,
		PrevHash:   SumHash([]byte("prev")),
		MerkleRoot: SumHash([]byte("merkle")),
		StateRoot:  SumHash([]byte("state")),
		Coinbase:   Address{9},
		Timestamp:  1234,
		Difficulty: 6,
		Nonce:      42,
	}
	var buf []byte
	var n8 [8]byte
	binary.BigEndian.PutUint64(n8[:], h.Height)
	buf = append(buf, n8[:]...)
	buf = append(buf, h.PrevHash[:]...)
	buf = append(buf, h.MerkleRoot[:]...)
	buf = append(buf, h.StateRoot[:]...)
	buf = append(buf, h.Coinbase[:]...)
	binary.BigEndian.PutUint64(n8[:], uint64(h.Timestamp))
	buf = append(buf, n8[:]...)
	var n4 [4]byte
	binary.BigEndian.PutUint32(n4[:], h.Difficulty)
	buf = append(buf, n4[:]...)
	binary.BigEndian.PutUint64(n8[:], h.Nonce)
	buf = append(buf, n8[:]...)

	want := SumHash(buf)
	if got := h.Hash(); got != want {
		t.Fatalf("Hash() with empty ProposerSig = %x, want %x (the pre-M8 8-field formula, independently re-derived)", got, want)
	}
	if got := h.SigningHash(); got != want {
		t.Fatalf("SigningHash() with empty ProposerSig = %x, want %x", got, want)
	}
}
