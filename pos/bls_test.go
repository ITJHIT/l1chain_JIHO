package pos

import (
	"bytes"
	"testing"
)

func TestBLSSignVerifyRoundTrip(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	dst := DST(1)
	msg := []byte("l1chain m8 checkpoint")
	sig := k.Sign(msg, dst)
	if !Verify(k.PubKey(), msg, sig, dst) {
		t.Fatal("valid signature failed to verify")
	}
}

func TestBLSVerifyRejectsTamperedMessage(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	dst := DST(1)
	sig := k.Sign([]byte("original"), dst)
	if Verify(k.PubKey(), []byte("tampered"), sig, dst) {
		t.Fatal("signature verified against a different message than it signed")
	}
}

func TestBLSVerifyRejectsWrongKey(t *testing.T) {
	k1, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey k1: %v", err)
	}
	k2, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey k2: %v", err)
	}
	dst := DST(1)
	msg := []byte("l1chain m8 checkpoint")
	sig := k1.Sign(msg, dst)
	if Verify(k2.PubKey(), msg, sig, dst) {
		t.Fatal("signature verified under the wrong public key")
	}
}

// TestBLSDSTDomainSeparatesChains proves DST(chainID) genuinely prevents
// cross-chain replay: a signature produced under chain 1's DST must NOT
// verify under chain 2's DST, mirroring core.Transaction.ChainID's own
// replay-domain-separation role for ECDSA tx signatures.
func TestBLSDSTDomainSeparatesChains(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	msg := []byte("l1chain m8 checkpoint")
	sigChainA := k.Sign(msg, DST(1))
	if Verify(k.PubKey(), msg, sigChainA, DST(2)) {
		t.Fatal("a signature produced under chain 1's DST verified under chain 2's DST -- cross-chain replay would be possible")
	}
	if !Verify(k.PubKey(), msg, sigChainA, DST(1)) {
		t.Fatal("sanity: the same signature must still verify under its OWN chain's DST")
	}
}

func TestBLSKeyBytesRoundTrip(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	k2, err := KeyFromBytes(k.Bytes())
	if err != nil {
		t.Fatalf("KeyFromBytes: %v", err)
	}
	if !bytes.Equal(k.PubKey(), k2.PubKey()) {
		t.Fatal("KeyFromBytes(k.Bytes()) produced a different public key than k")
	}
}

func TestBLSAggregateVerify(t *testing.T) {
	const n = 5
	pubKeys := make([][]byte, n)
	sigs := make([][]byte, n)
	dst := DST(1)
	msg := []byte("checkpoint height=32")
	for i := 0; i < n; i++ {
		k, err := NewKey()
		if err != nil {
			t.Fatalf("NewKey[%d]: %v", i, err)
		}
		pubKeys[i] = k.PubKey()
		sigs[i] = k.Sign(msg, dst)
	}
	agg, err := Aggregate(sigs)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if !VerifyAggregate(pubKeys, msg, agg, dst) {
		t.Fatal("valid aggregate signature failed to verify against all signers' pubkeys")
	}
}

// TestBLSAggregateVerifyRejectsMissingSigner proves VerifyAggregate genuinely
// requires every listed pubkey to have contributed -- an aggregate built from
// n-1 signers must not verify against all n pubkeys.
func TestBLSAggregateVerifyRejectsMissingSigner(t *testing.T) {
	const n = 3
	pubKeys := make([][]byte, n)
	sigs := make([][]byte, 0, n-1)
	dst := DST(1)
	msg := []byte("checkpoint height=32")
	for i := 0; i < n; i++ {
		k, err := NewKey()
		if err != nil {
			t.Fatalf("NewKey[%d]: %v", i, err)
		}
		pubKeys[i] = k.PubKey()
		if i != n-1 { // deliberately omit the last signer's signature
			sigs = append(sigs, k.Sign(msg, dst))
		}
	}
	agg, err := Aggregate(sigs)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if VerifyAggregate(pubKeys, msg, agg, dst) {
		t.Fatal("aggregate signature from n-1 signers verified against n pubkeys")
	}
}

func TestBLSAggregateEmptyRejected(t *testing.T) {
	if _, err := Aggregate(nil); err == nil {
		t.Fatal("Aggregate(nil) should return an error, not a zero-value signature")
	}
}

func TestBLSValidatePubKey(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	if !ValidatePubKey(k.PubKey()) {
		t.Fatal("a real, freshly generated public key failed ValidatePubKey")
	}
	if ValidatePubKey([]byte("not a real compressed G1 point")) {
		t.Fatal("garbage bytes passed ValidatePubKey")
	}
}
