package pos

import (
	"bytes"
	"errors"
	"testing"

	"l1chain/core"
)

func TestEncodeDecodeAttestRoundTrip(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	targetHeight := uint64(320)
	targetHash := core.SumHash([]byte("checkpoint block"))
	sig := k.Sign(targetHash[:], DST(1))

	data := EncodeAttest(targetHeight, targetHash, sig)
	gotHeight, gotHash, gotSig, err := DecodeAttest(data)
	if err != nil {
		t.Fatalf("DecodeAttest: %v", err)
	}
	if gotHeight != targetHeight {
		t.Fatalf("targetHeight = %d, want %d", gotHeight, targetHeight)
	}
	if gotHash != targetHash {
		t.Fatalf("targetHash = %x, want %x", gotHash, targetHash)
	}
	if !bytes.Equal(gotSig, sig) {
		t.Fatal("decoded signature does not match the encoded one")
	}
	// The decoded fields must still verify as a real BLS signature -- proves
	// EncodeAttest/DecodeAttest round-trips the signature bytes exactly, not
	// just something that happens to compare equal.
	if !Verify(k.PubKey(), gotHash[:], gotSig, DST(1)) {
		t.Fatal("round-tripped attestation signature failed to verify")
	}
}

func TestDecodeAttestRejectsWrongLength(t *testing.T) {
	if _, _, _, err := DecodeAttest([]byte{OpAttest, 0x01}); !errors.Is(err, ErrMalformedAttestation) {
		t.Fatalf("err = %v, want ErrMalformedAttestation", err)
	}
}

func TestDecodeAttestRejectsWrongOpByte(t *testing.T) {
	data := EncodeAttest(1, core.Hash{}, make([]byte, sigSize))
	data[0] = 0xFF // corrupt the op byte
	if _, _, _, err := DecodeAttest(data); !errors.Is(err, ErrMalformedAttestation) {
		t.Fatalf("err = %v, want ErrMalformedAttestation", err)
	}
}

func TestIsAttestationTx(t *testing.T) {
	tx := core.Transaction{To: AttestAddress}
	if !IsAttestationTx(tx) {
		t.Fatal("a transaction addressed to AttestAddress was not recognized as an attestation")
	}
	other := core.Transaction{To: testAddr(1)}
	if IsAttestationTx(other) {
		t.Fatal("a transaction addressed elsewhere was recognized as an attestation")
	}
}

func TestCheckpointRound(t *testing.T) {
	cases := []struct {
		height uint64
		want   uint64
	}{
		{0, 0},
		{31, 0},
		{32, 1},
		{63, 1},
		{64, 2},
	}
	for _, c := range cases {
		if got := CheckpointRound(c.height); got != c.want {
			t.Fatalf("CheckpointRound(%d) = %d, want %d", c.height, got, c.want)
		}
	}
}
