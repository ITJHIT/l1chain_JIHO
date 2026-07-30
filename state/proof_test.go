package state

import (
	"testing"
)

func TestVerifyAccountProofRoundTrip(t *testing.T) {
	s := New().(*mptStateDB)
	s.SetAccount(addr(1), Account{Balance: 100, Nonce: 3})
	s.SetAccount(addr(2), Account{Balance: 5})

	ap, found := GenerateAccountProof(s.nodes, s.root, addr(1))
	if !found {
		t.Fatal("expected account 1 to be found")
	}
	if ap.Account.Balance != 100 || ap.Account.Nonce != 3 {
		t.Fatalf("proof carries the wrong account: %+v", ap.Account)
	}
	if !VerifyAccountProof(s.root, addr(1), ap) {
		t.Fatal("valid account proof rejected")
	}
}

func TestVerifyAccountProofRejectsWrongAccountValue(t *testing.T) {
	s := New().(*mptStateDB)
	s.SetAccount(addr(1), Account{Balance: 100})
	ap, found := GenerateAccountProof(s.nodes, s.root, addr(1))
	if !found {
		t.Fatal("expected found")
	}
	ap.Account.Balance = 999 // tamper with the claimed value, not the proof itself
	if VerifyAccountProof(s.root, addr(1), ap) {
		t.Fatal("proof verified against a tampered claimed account value")
	}
}

func TestGenerateAccountProofMissingAccount(t *testing.T) {
	s := New().(*mptStateDB)
	s.SetAccount(addr(1), Account{Balance: 1}) // some unrelated state to exercise a real trie
	if _, found := GenerateAccountProof(s.nodes, s.root, addr(9)); found {
		t.Fatal("expected not found for an account that was never set")
	}
}

func TestVerifyStorageProofChainedRoundTrip(t *testing.T) {
	s := New().(*mptStateDB)
	s.SetAccount(addr(1), Account{Balance: 1}) // give the account some non-storage state too
	s.SetStorage(addr(1), hashKey(1), hashKey(42))
	s.SetStorage(addr(1), hashKey(2), hashKey(43))

	sp, found := GenerateStorageProof(s.nodes, s.root, addr(1), hashKey(1))
	if !found {
		t.Fatal("expected slot 1 to be found")
	}
	if sp.Value != hashKey(42) {
		t.Fatalf("got value %x want %x", sp.Value, hashKey(42))
	}
	if !VerifyStorageProof(s.root, addr(1), hashKey(1), sp) {
		t.Fatal("valid storage proof rejected")
	}
}

func TestVerifyStorageProofRejectsWrongValue(t *testing.T) {
	s := New().(*mptStateDB)
	s.SetStorage(addr(1), hashKey(1), hashKey(42))
	sp, found := GenerateStorageProof(s.nodes, s.root, addr(1), hashKey(1))
	if !found {
		t.Fatal("expected found")
	}
	sp.Value = hashKey(99)
	if VerifyStorageProof(s.root, addr(1), hashKey(1), sp) {
		t.Fatal("storage proof verified against a tampered claimed value")
	}
}

func TestVerifyStorageProofRejectsAccountForgedIntoADifferentStorageRoot(t *testing.T) {
	s := New().(*mptStateDB)
	s.SetStorage(addr(1), hashKey(1), hashKey(42))
	s.SetStorage(addr(2), hashKey(1), hashKey(77))

	sp1, found := GenerateStorageProof(s.nodes, s.root, addr(1), hashKey(1))
	if !found {
		t.Fatal("expected found")
	}
	sp2, found := GenerateStorageProof(s.nodes, s.root, addr(2), hashKey(1))
	if !found {
		t.Fatal("expected found")
	}
	// Splice account 2's (verified, correct) account proof onto account 1's
	// slot proof/value -- the chained verification must catch that the slot
	// proof was never actually rooted at account 2's StorageRoot.
	forged := StorageProof{AccountProof: sp2.AccountProof, SlotProof: sp1.SlotProof, Value: sp1.Value}
	if VerifyStorageProof(s.root, addr(2), hashKey(1), forged) {
		t.Fatal("chained storage proof accepted a slot proof rooted at a different account's storage trie")
	}
}

func TestGenerateStorageProofMissingSlot(t *testing.T) {
	s := New().(*mptStateDB)
	s.SetStorage(addr(1), hashKey(1), hashKey(42))
	if _, found := GenerateStorageProof(s.nodes, s.root, addr(1), hashKey(9)); found {
		t.Fatal("expected not found for a storage key that was never set")
	}
}

func TestGenerateStorageProofAccountWithNoStorage(t *testing.T) {
	s := New().(*mptStateDB)
	s.SetAccount(addr(1), Account{Balance: 1}) // has an account, but no storage trie
	if _, found := GenerateStorageProof(s.nodes, s.root, addr(1), hashKey(1)); found {
		t.Fatal("expected not found for an account with no storage trie")
	}
}
