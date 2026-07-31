package adapter

import (
	"testing"

	"l1chain/state"

	"github.com/ethereum/go-ethereum/core/tracing"
)

func TestRefundAddSubRoundTripAndSnapshotRevert(t *testing.T) {
	s := New(state.NewMemStateDB())

	if got := s.GetRefund(); got != 0 {
		t.Fatalf("fresh refund = %d, want 0", got)
	}
	s.AddRefund(100)
	if got := s.GetRefund(); got != 100 {
		t.Fatalf("refund after AddRefund(100) = %d, want 100", got)
	}
	id := s.Snapshot()
	s.AddRefund(50)
	s.SubRefund(30)
	if got := s.GetRefund(); got != 120 {
		t.Fatalf("refund after +50-30 = %d, want 120", got)
	}
	s.RevertToSnapshot(id)
	if got := s.GetRefund(); got != 100 {
		t.Fatalf("refund after revert = %d, want 100 (pre-snapshot value)", got)
	}
}

func TestSubRefundPanicsBelowZero(t *testing.T) {
	s := New(state.NewMemStateDB())
	s.AddRefund(10)

	defer func() {
		if recover() == nil {
			t.Fatal("SubRefund(11) with refund=10 did not panic, want a panic (mirroring go-ethereum's own SubRefund)")
		}
	}()
	s.SubRefund(11)
}

func TestExistFalseForUntouchedNeverPersistedAddress(t *testing.T) {
	s := New(state.NewMemStateDB())
	if s.Exist(addrA) {
		t.Fatal("Exist = true for an address never touched or persisted, want false")
	}
}

func TestCreateAccountMakesExistTrueEvenThoughStillAllZero(t *testing.T) {
	s := New(state.NewMemStateDB())
	s.CreateAccount(addrA)

	if !s.Exist(addrA) {
		t.Fatal("Exist = false immediately after CreateAccount, want true")
	}
	if !s.Empty(addrA) {
		t.Fatal("Empty = false immediately after CreateAccount with no balance/nonce/code, want true")
	}
	// l1chain's own base state has no way to represent "exists but all
	// zero" -- state/mpt.go's SetAccount deletes an all-zero account
	// outright -- so base itself must still show nothing persisted here;
	// Exist's "true" above comes entirely from this adapter's own touched
	// bookkeeping, not from base.
	if s.base.GetAccount(toCoreAddress(addrA)) != (state.Account{}) {
		t.Fatal("base has a persisted account after a bare CreateAccount with no balance/nonce/code, want none")
	}
}

func TestExistTrueForAddressPersistedInBaseButNeverTouchedThisInstance(t *testing.T) {
	base := state.NewMemStateDB()
	base.SetAccount(toCoreAddress(addrA), state.Account{Balance: 5})

	// A fresh adapter instance over the same base, which never itself
	// called CreateAccount/Touch/any mutator on addrA.
	s := New(base)
	if !s.Exist(addrA) {
		t.Fatal("Exist = false for an address with real persisted balance, want true")
	}
	if s.Empty(addrA) {
		t.Fatal("Empty = true for an address with non-zero balance, want false")
	}
}

func TestTouchAloneMakesExistTrueWithoutMutatingAnything(t *testing.T) {
	s := New(state.NewMemStateDB())
	s.Touch(addrA)

	if !s.Exist(addrA) {
		t.Fatal("Exist = false after Touch, want true")
	}
	if got := s.GetBalance(addrA); !got.IsZero() {
		t.Fatalf("balance after a bare Touch = %s, want 0 (Touch must not mutate)", got)
	}
}

func TestCreateContractSetsIsNewContractOnlyForThisInstance(t *testing.T) {
	s := New(state.NewMemStateDB())
	if s.IsNewContract(addrA) {
		t.Fatal("IsNewContract = true before CreateContract, want false")
	}
	s.CreateContract(addrA)
	if !s.IsNewContract(addrA) {
		t.Fatal("IsNewContract = false after CreateContract, want true")
	}
	if !s.Exist(addrA) {
		t.Fatal("Exist = false after CreateContract, want true")
	}
	// addrB never had CreateContract called on it at all.
	if s.IsNewContract(addrB) {
		t.Fatal("IsNewContract = true for an untouched address, want false")
	}
}

func TestSelfDestructFlagRoundTripAndSnapshotRevert(t *testing.T) {
	s := New(state.NewMemStateDB())
	if s.HasSelfDestructed(addrA) {
		t.Fatal("HasSelfDestructed = true before SelfDestruct, want false")
	}

	id := s.Snapshot()
	s.SelfDestruct(addrA)
	if !s.HasSelfDestructed(addrA) {
		t.Fatal("HasSelfDestructed = false after SelfDestruct, want true")
	}
	if !s.Exist(addrA) {
		t.Fatal("Exist = false for a self-destructed address within the same lifetime, want true")
	}

	s.RevertToSnapshot(id)
	if s.HasSelfDestructed(addrA) {
		t.Fatal("HasSelfDestructed = true after revert, want false")
	}
}

func TestSnapshotRevertUndoesTouchedAndNewContractToo(t *testing.T) {
	s := New(state.NewMemStateDB())
	id := s.Snapshot()

	s.CreateContract(addrA)
	s.SetNonce(addrA, 1, tracing.NonceChangeUnspecified)

	if !s.Exist(addrA) || !s.IsNewContract(addrA) {
		t.Fatal("addrA should exist and be a new contract before revert")
	}

	s.RevertToSnapshot(id)

	if s.Exist(addrA) {
		t.Fatal("Exist = true after reverting past CreateContract, want false")
	}
	if s.IsNewContract(addrA) {
		t.Fatal("IsNewContract = true after reverting past CreateContract, want false")
	}
	if got := s.GetNonce(addrA); got != 0 {
		t.Fatalf("nonce after revert = %d, want 0", got)
	}
}
