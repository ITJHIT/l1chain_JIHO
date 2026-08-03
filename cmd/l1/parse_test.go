package main

import (
	"bytes"
	"encoding/hex"
	"testing"

	"l1chain/pos"
)

func TestParseValidatorsEmpty(t *testing.T) {
	got, err := parseValidators("")
	if err != nil {
		t.Fatalf(`parseValidators(""): %v`, err)
	}
	if got != nil {
		t.Fatalf(`parseValidators("") = %v, want nil`, got)
	}
}

func TestParseValidatorsRoundTrip(t *testing.T) {
	k, err := pos.NewKey()
	if err != nil {
		t.Fatalf("pos.NewKey: %v", err)
	}
	addrHex := "0100000000000000000000000000000000000000"
	pubHex := hex.EncodeToString(k.PubKey())
	spec := addrHex + ":" + pubHex + ":100," // trailing comma/whitespace must be tolerated, mirroring parseAlloc

	got, err := parseValidators(spec)
	if err != nil {
		t.Fatalf("parseValidators: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("parseValidators returned %d entries, want 1", len(got))
	}
	if got[0].Stake != 100 {
		t.Fatalf("Stake = %d, want 100", got[0].Stake)
	}
	if !bytes.Equal(got[0].BLSPubKey, k.PubKey()) {
		t.Fatal("BLSPubKey round-trip mismatch")
	}
}

func TestParseValidatorsMultipleEntries(t *testing.T) {
	k1, err := pos.NewKey()
	if err != nil {
		t.Fatalf("pos.NewKey 1: %v", err)
	}
	k2, err := pos.NewKey()
	if err != nil {
		t.Fatalf("pos.NewKey 2: %v", err)
	}
	spec := "0100000000000000000000000000000000000000:" + hex.EncodeToString(k1.PubKey()) + ":10," +
		"0200000000000000000000000000000000000000:" + hex.EncodeToString(k2.PubKey()) + ":90"

	got, err := parseValidators(spec)
	if err != nil {
		t.Fatalf("parseValidators: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("parseValidators returned %d entries, want 2", len(got))
	}
	if got[0].Stake+got[1].Stake != 100 {
		t.Fatalf("combined stake = %d, want 100", got[0].Stake+got[1].Stake)
	}
}

func TestParseValidatorsRejectsMalformed(t *testing.T) {
	if _, err := parseValidators("not-enough-fields"); err == nil {
		t.Fatal("expected an error for a malformed entry (missing fields)")
	}
	if _, err := parseValidators("0100000000000000000000000000000000000000:nothex:100"); err == nil {
		t.Fatal("expected an error for a non-hex BLS pubkey")
	}
	if _, err := parseValidators("0100000000000000000000000000000000000000:aabb:notanumber"); err == nil {
		t.Fatal("expected an error for a non-numeric stake")
	}
	if _, err := parseValidators("nothex:aabb:100"); err == nil {
		t.Fatal("expected an error for a non-hex address")
	}
}
