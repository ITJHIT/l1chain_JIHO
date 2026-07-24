package state

import (
	"testing"

	"l1chain/core"
)

func addr(b byte) core.Address {
	var a core.Address
	a[0] = b
	return a
}

func TestStateRootEmptyIsZeroStable(t *testing.T) {
	s := NewMemStateDB()
	r1 := s.StateRoot()
	// Setting then zeroing an account must not change the empty root.
	s.SetAccount(addr(1), Account{Balance: 0, Nonce: 0})
	if s.StateRoot() != r1 {
		t.Fatal("empty accounts must not affect the state root")
	}
}

func TestStateRootChangesWithBalance(t *testing.T) {
	s := NewMemStateDB()
	r0 := s.StateRoot()
	s.SetAccount(addr(1), Account{Balance: 100})
	if s.StateRoot() == r0 {
		t.Fatal("state root must change after a balance update")
	}
}

func TestStateRootDeterministicRegardlessOfInsertOrder(t *testing.T) {
	s1 := NewMemStateDB()
	s1.SetAccount(addr(1), Account{Balance: 10})
	s1.SetAccount(addr(2), Account{Balance: 20})

	s2 := NewMemStateDB()
	s2.SetAccount(addr(2), Account{Balance: 20})
	s2.SetAccount(addr(1), Account{Balance: 10})

	if s1.StateRoot() != s2.StateRoot() {
		t.Fatal("state root must be independent of insertion order")
	}
}

func TestGetUnknownAccountIsZero(t *testing.T) {
	s := NewMemStateDB()
	if got := s.GetAccount(addr(9)); got.Balance != 0 || got.Nonce != 0 {
		t.Fatalf("unknown account must be zero, got %+v", got)
	}
}
