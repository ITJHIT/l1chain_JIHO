package store

import (
	"bytes"
	"testing"

	"l1chain/core"
	"l1chain/wallet"
)

// TestCodecRoundTripPreservesHashAndSignatures proves DecodeBlock(EncodeBlock(b))
// reproduces the exact block: same Block.Hash(), same transactions, and byte-for-
// byte identical Signature (and Data) slices.
func TestCodecRoundTripPreservesHashAndSignatures(t *testing.T) {
	sender, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	recipient, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}

	tx0 := core.Transaction{
		From:     sender.Address(),
		To:       recipient.Address(),
		Value:    42,
		Nonce:    0,
		GasLimit: 21000,
		Data:     []byte("hello-store"),
	}
	sender.Sign(&tx0)
	if !wallet.Verify(tx0) {
		t.Fatalf("tx0 failed Verify before encoding")
	}

	tx1 := core.Transaction{
		From:  sender.Address(),
		To:    recipient.Address(),
		Value: 7,
		Nonce: 1,
	}
	sender.Sign(&tx1)

	b := core.Block{
		Header: core.Header{
			Height:     5,
			PrevHash:   core.Hash{1, 2, 3},
			MerkleRoot: core.Hash{9},
			StateRoot:  core.Hash{8},
			Timestamp:  1234,
			Difficulty: 6,
			Nonce:      99,
		},
		Txs: []core.Transaction{tx0, tx1},
	}

	enc, err := EncodeBlock(b)
	if err != nil {
		t.Fatalf("EncodeBlock: %v", err)
	}
	got, err := DecodeBlock(enc)
	if err != nil {
		t.Fatalf("DecodeBlock: %v", err)
	}

	if got.Hash() != b.Hash() {
		t.Fatalf("Block.Hash() mismatch after round-trip: got %s want %s", got.Hash().Hex(), b.Hash().Hex())
	}
	if len(got.Txs) != len(b.Txs) {
		t.Fatalf("tx count mismatch: got %d want %d", len(got.Txs), len(b.Txs))
	}
	for i := range b.Txs {
		if got.Txs[i].Hash() != b.Txs[i].Hash() {
			t.Fatalf("tx[%d] hash mismatch after round-trip", i)
		}
		if !bytes.Equal(got.Txs[i].Signature, b.Txs[i].Signature) {
			t.Fatalf("tx[%d] Signature not preserved", i)
		}
		if !bytes.Equal(got.Txs[i].Data, b.Txs[i].Data) {
			t.Fatalf("tx[%d] Data not preserved", i)
		}
		if !wallet.Verify(got.Txs[i]) {
			t.Fatalf("tx[%d] failed Verify after round-trip", i)
		}
	}
}
