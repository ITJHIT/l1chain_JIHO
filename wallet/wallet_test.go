package wallet

import (
	"testing"

	"l1chain/core"
)

func sampleTx() core.Transaction {
	return core.Transaction{
		To:       core.Address{9, 9, 9},
		Value:    100,
		Nonce:    0,
		GasLimit: 21000,
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	tx := sampleTx()
	k.Sign(&tx)
	if tx.From != k.Address() {
		t.Fatalf("Sign did not set From to key address")
	}
	if !Verify(tx) {
		t.Fatalf("Verify(signed tx) = false, want true")
	}
}

func TestVerifyRejectsTamperedValue(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	tx := sampleTx()
	k.Sign(&tx)
	// Mutate Value without re-signing: the signing hash changes so the recovered
	// address no longer matches From.
	tx.Value += 1
	if Verify(tx) {
		t.Fatalf("Verify(tampered value) = true, want false")
	}
}

func TestVerifyRejectsWrongSender(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	tx := sampleTx()
	k.Sign(&tx)
	// Point From at a different address; recovered address won't match.
	tx.From = core.Address{1, 2, 3, 4}
	if Verify(tx) {
		t.Fatalf("Verify(wrong sender) = true, want false")
	}
}

func TestVerifyRejectsEmptySignature(t *testing.T) {
	tx := sampleTx()
	if Verify(tx) {
		t.Fatalf("Verify(empty sig) = true, want false")
	}
	tx.Signature = []byte{1, 2, 3}
	if Verify(tx) {
		t.Fatalf("Verify(short sig) = true, want false")
	}
}

func TestKeyFromBytesReproducesAddressAndSig(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	k2 := KeyFromBytes(k.Bytes())
	if k2.Address() != k.Address() {
		t.Fatalf("KeyFromBytes address mismatch: %x != %x", k2.Address(), k.Address())
	}
	tx := sampleTx()
	k2.Sign(&tx)
	if !Verify(tx) {
		t.Fatalf("Verify(tx signed by restored key) = false, want true")
	}
	if tx.From != k.Address() {
		t.Fatalf("restored key produced different sender address")
	}
}
