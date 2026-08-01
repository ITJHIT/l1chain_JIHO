package pos

import (
	"errors"
	"testing"

	"l1chain/core"
)

func testAddr(b byte) core.Address {
	var a core.Address
	a[0] = b
	return a
}

func testPubKey(t *testing.T) []byte {
	t.Helper()
	k, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return k.PubKey()
}

func TestNewValidatorSetRejectsEmpty(t *testing.T) {
	if _, err := NewValidatorSet(nil); !errors.Is(err, ErrEmptyValidatorSet) {
		t.Fatalf("err = %v, want ErrEmptyValidatorSet", err)
	}
}

func TestNewValidatorSetRejectsZeroStake(t *testing.T) {
	vs := []ValidatorInfo{{Address: testAddr(1), BLSPubKey: testPubKey(t), Stake: 0}}
	if _, err := NewValidatorSet(vs); err == nil {
		t.Fatal("expected an error for zero stake, got nil")
	}
}

func TestNewValidatorSetRejectsDuplicateAddress(t *testing.T) {
	a := testAddr(1)
	vs := []ValidatorInfo{
		{Address: a, BLSPubKey: testPubKey(t), Stake: 10},
		{Address: a, BLSPubKey: testPubKey(t), Stake: 20},
	}
	if _, err := NewValidatorSet(vs); err == nil {
		t.Fatal("expected an error for a duplicate validator address, got nil")
	}
}

func TestNewValidatorSetRejectsDuplicatePubKey(t *testing.T) {
	pk := testPubKey(t)
	vs := []ValidatorInfo{
		{Address: testAddr(1), BLSPubKey: pk, Stake: 10},
		{Address: testAddr(2), BLSPubKey: pk, Stake: 20},
	}
	if _, err := NewValidatorSet(vs); err == nil {
		t.Fatal("expected an error for a duplicate BLS pubkey, got nil")
	}
}

func TestNewValidatorSetRejectsInvalidPubKey(t *testing.T) {
	vs := []ValidatorInfo{{Address: testAddr(1), BLSPubKey: []byte("not a real compressed G1 point"), Stake: 10}}
	if _, err := NewValidatorSet(vs); err == nil {
		t.Fatal("expected an error for an invalid BLS pubkey, got nil")
	}
}

func TestNewValidatorSetTotalStake(t *testing.T) {
	vs := []ValidatorInfo{
		{Address: testAddr(1), BLSPubKey: testPubKey(t), Stake: 10},
		{Address: testAddr(2), BLSPubKey: testPubKey(t), Stake: 20},
		{Address: testAddr(3), BLSPubKey: testPubKey(t), Stake: 70},
	}
	set, err := NewValidatorSet(vs)
	if err != nil {
		t.Fatalf("NewValidatorSet: %v", err)
	}
	if got := set.TotalStake(); got != 100 {
		t.Fatalf("TotalStake = %d, want 100", got)
	}
	if got := set.Len(); got != 3 {
		t.Fatalf("Len = %d, want 3", got)
	}
}

func TestValidatorSetByAddress(t *testing.T) {
	a1, a2 := testAddr(1), testAddr(2)
	vs := []ValidatorInfo{
		{Address: a1, BLSPubKey: testPubKey(t), Stake: 10},
		{Address: a2, BLSPubKey: testPubKey(t), Stake: 20},
	}
	set, err := NewValidatorSet(vs)
	if err != nil {
		t.Fatalf("NewValidatorSet: %v", err)
	}
	if got, ok := set.ByAddress(a1); !ok || got.Stake != 10 {
		t.Fatalf("ByAddress(a1) = %+v, %v, want stake=10, true", got, ok)
	}
	if _, ok := set.ByAddress(testAddr(9)); ok {
		t.Fatal("ByAddress found a validator that was never registered")
	}
}

func TestValidatorSetEffectiveStakeExcludesJailed(t *testing.T) {
	a1, a2, a3 := testAddr(1), testAddr(2), testAddr(3)
	vs := []ValidatorInfo{
		{Address: a1, BLSPubKey: testPubKey(t), Stake: 10},
		{Address: a2, BLSPubKey: testPubKey(t), Stake: 20},
		{Address: a3, BLSPubKey: testPubKey(t), Stake: 70},
	}
	set, err := NewValidatorSet(vs)
	if err != nil {
		t.Fatalf("NewValidatorSet: %v", err)
	}

	active, total := set.EffectiveStake(nil)
	if len(active) != 3 || total != 100 {
		t.Fatalf("EffectiveStake(nil) = %d validators, total %d; want 3, 100", len(active), total)
	}

	jailed := map[core.Address]bool{a2: true}
	active, total = set.EffectiveStake(jailed)
	if len(active) != 2 || total != 80 {
		t.Fatalf("EffectiveStake(jailed a2) = %d validators, total %d; want 2, 80", len(active), total)
	}
	for _, v := range active {
		if v.Address == a2 {
			t.Fatal("jailed validator a2 still present in EffectiveStake's active list")
		}
	}
	// Relative order of the SURVIVING validators must be preserved (a1
	// before a3), since SelectProposer's cumulative-stake walk depends on it.
	if active[0].Address != a1 || active[1].Address != a3 {
		t.Fatalf("EffectiveStake did not preserve relative order: got %x, %x", active[0].Address, active[1].Address)
	}
}
