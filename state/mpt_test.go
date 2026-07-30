package state

import (
	"testing"

	"l1chain/core"
)

func hashKey(b byte) core.Hash {
	var h core.Hash
	h[0] = b
	return h
}

func TestMPTStateRootEmptyIsZeroStable(t *testing.T) {
	s := New()
	r1 := s.StateRoot()
	s.SetAccount(addr(1), Account{Balance: 0, Nonce: 0})
	if s.StateRoot() != r1 {
		t.Fatal("empty accounts must not affect the state root")
	}
}

func TestMPTStateRootChangesWithBalance(t *testing.T) {
	s := New()
	r0 := s.StateRoot()
	s.SetAccount(addr(1), Account{Balance: 100})
	if s.StateRoot() == r0 {
		t.Fatal("state root must change after a balance update")
	}
}

func TestMPTStateRootDeterministicRegardlessOfInsertOrder(t *testing.T) {
	s1 := New()
	s1.SetAccount(addr(1), Account{Balance: 10})
	s1.SetAccount(addr(2), Account{Balance: 20})
	s1.SetAccount(addr(3), Account{Balance: 30})

	s2 := New()
	s2.SetAccount(addr(3), Account{Balance: 30})
	s2.SetAccount(addr(1), Account{Balance: 10})
	s2.SetAccount(addr(2), Account{Balance: 20})

	if s1.StateRoot() != s2.StateRoot() {
		t.Fatal("state root must be independent of insertion order")
	}
}

func TestMPTGetUnknownAccountIsZero(t *testing.T) {
	s := New()
	if got := s.GetAccount(addr(9)); got.Balance != 0 || got.Nonce != 0 {
		t.Fatalf("unknown account must be zero, got %+v", got)
	}
}

func TestSetStoragePopulatesAccountStorageRoot(t *testing.T) {
	s := New()
	if got := s.GetAccount(addr(1)).StorageRoot; !got.IsZero() {
		t.Fatalf("fresh account should have a zero StorageRoot, got %x", got)
	}
	s.SetStorage(addr(1), hashKey(1), hashKey(42))
	if got := s.GetAccount(addr(1)).StorageRoot; got.IsZero() {
		t.Fatal("StorageRoot should be non-zero after the first SetStorage")
	}
	if got := s.GetStorage(addr(1), hashKey(1)); got != hashKey(42) {
		t.Fatalf("got %x want %x", got, hashKey(42))
	}
}

func TestSetStorageZeroOnAbsentKeyIsNoOp(t *testing.T) {
	s := New()
	s.SetStorage(addr(1), hashKey(1), hashKey(9)) // give the account a storage trie
	root1 := s.StateRoot()
	s.SetStorage(addr(1), hashKey(2), core.Hash{}) // key 2 was never set
	if s.StateRoot() != root1 {
		t.Fatal("clearing an already-absent storage key changed the root")
	}
}

func TestClearingAllStorageReturnsStorageRootToZero(t *testing.T) {
	s := New()
	s.SetStorage(addr(1), hashKey(1), hashKey(9))
	s.SetStorage(addr(1), hashKey(1), core.Hash{}) // clear it back out
	if got := s.GetAccount(addr(1)).StorageRoot; !got.IsZero() {
		t.Fatalf("StorageRoot should return to zero once all storage is cleared, got %x", got)
	}
	if got := s.GetStorage(addr(1), hashKey(1)); !got.IsZero() {
		t.Fatalf("cleared key should read as zero, got %x", got)
	}
}

func TestSetCodeChangesStateRoot(t *testing.T) {
	s := New()
	s.SetAccount(addr(1), Account{Balance: 1})
	rootBefore := s.StateRoot()
	s.SetCode(addr(1), []byte{0x60, 0x01})
	rootAfterFirst := s.StateRoot()
	if rootAfterFirst == rootBefore {
		t.Fatal("deploying code must change the state root")
	}
	// Redeploying DIFFERENT bytecode at the same address must also change
	// the root -- this is the correctness property that makes CodeHash
	// meaningful: a producer cannot swap bytecode at an address without the
	// state root reflecting it.
	s.SetCode(addr(1), []byte{0x60, 0x02})
	if s.StateRoot() == rootAfterFirst {
		t.Fatal("redeploying different code at the same address must change the state root")
	}
	got := s.GetCode(addr(1))
	if len(got) != 2 || got[0] != 0x60 || got[1] != 0x02 {
		t.Fatalf("got code %x, want 6002", got)
	}
}

func TestStorageIndependentAcrossAccounts(t *testing.T) {
	s := New()
	s.SetStorage(addr(1), hashKey(1), hashKey(100))
	rootAfterAcct1 := s.StateRoot()
	acct1RootAfter1 := s.GetAccount(addr(1)).StorageRoot

	s.SetStorage(addr(2), hashKey(1), hashKey(200)) // same key, different account
	if s.GetAccount(addr(1)).StorageRoot != acct1RootAfter1 {
		t.Fatal("mutating account 2's storage changed account 1's StorageRoot")
	}
	if v := s.GetStorage(addr(1), hashKey(1)); v != hashKey(100) {
		t.Fatalf("account 1's slot changed: got %x want %x", v, hashKey(100))
	}
	if v := s.GetStorage(addr(2), hashKey(1)); v != hashKey(200) {
		t.Fatalf("account 2's slot wrong: got %x want %x", v, hashKey(200))
	}
	// Touching account 2 SHOULD change the world root, since its account
	// leaf (StorageRoot) changed.
	if s.StateRoot() == rootAfterAcct1 {
		t.Fatal("mutating account 2's storage did not change the world state root")
	}
}

// TestMPTAndMemStateDBAreEachInternallyConsistent runs the same operation
// sequence against both implementations and checks each is self-consistent
// (zero-default reads, overwrite semantics, delete idempotency) -- NOT that
// they produce equal root *values*, which they are not required to (they are
// different algorithms). memStateDB remains a useful independent reference
// for exactly this kind of conformance check.
func TestMPTAndMemStateDBAreEachInternallyConsistent(t *testing.T) {
	for _, db := range []StateDB{New(), NewMemStateDB()} {
		if got := db.GetAccount(addr(1)); got.Balance != 0 {
			t.Fatalf("%T: fresh account should read zero balance", db)
		}
		db.SetAccount(addr(1), Account{Balance: 5})
		if got := db.GetAccount(addr(1)); got.Balance != 5 {
			t.Fatalf("%T: expected balance 5, got %d", db, got.Balance)
		}
		db.SetAccount(addr(1), Account{Balance: 7})
		if got := db.GetAccount(addr(1)); got.Balance != 7 {
			t.Fatalf("%T: overwrite failed, got %d", db, got.Balance)
		}
		if got := db.GetStorage(addr(1), hashKey(1)); !got.IsZero() {
			t.Fatalf("%T: unset storage key should read zero", db)
		}
		db.SetStorage(addr(1), hashKey(1), hashKey(9))
		db.SetStorage(addr(1), hashKey(1), core.Hash{}) // clear it right back out
		if got := db.GetStorage(addr(1), hashKey(1)); !got.IsZero() {
			t.Fatalf("%T: cleared storage key should read zero, got %x", db, got)
		}
	}
}
