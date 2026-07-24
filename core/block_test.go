package core

import "testing"

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
