package state

import (
	"l1chain/core"
	"l1chain/trie"
)

// AccountProof lets a caller who only trusts a block's StateRoot (e.g. a
// light client) verify one account's balance/nonce/codeHash/storageRoot
// without holding the whole trie.
type AccountProof struct {
	Account Account
	Proof   trie.Proof
}

// GenerateAccountProof returns a proof that addr's account (as returned by
// GetAccount) is present under stateRoot. found is false if the address has
// no account (an empty/never-touched address) -- there is nothing to prove
// in that case; see the trie package's proof.go doc for why non-existence
// proofs are out of scope here.
func GenerateAccountProof(nodes map[core.Hash][]byte, stateRoot core.Hash, addr core.Address) (AccountProof, bool) {
	proof, value, found := trie.GenerateProof(nodes, stateRoot, addr[:])
	if !found {
		return AccountProof{}, false
	}
	return AccountProof{Account: decodeAccount(value), Proof: proof}, true
}

// VerifyAccountProof checks that ap.Account really is addr's account under
// stateRoot, re-deriving the claimed value's encoding itself rather than
// trusting ap.Account's fields directly -- a caller cannot make this pass by
// handing back a proof for a different value than the Account it claims.
func VerifyAccountProof(stateRoot core.Hash, addr core.Address, ap AccountProof) bool {
	return trie.VerifyProof(stateRoot, addr[:], encodeAccount(ap.Account), ap.Proof)
}

// StorageProof is a chained, two-stage proof: the account itself is proven
// present in the world trie under stateRoot (stage 1), and the storage slot
// is proven present in that account's own storage trie, rooted at the
// StorageRoot the stage-1 proof just verified (stage 2) -- never at a
// caller-supplied root, which is what makes the chaining meaningful rather
// than two unrelated proofs.
type StorageProof struct {
	AccountProof AccountProof
	SlotProof    trie.Proof
	Value        core.Hash
}

// GenerateStorageProof proves that addr's storage at key holds value under
// stateRoot. found is false if either the account or the slot doesn't exist.
func GenerateStorageProof(nodes map[core.Hash][]byte, stateRoot core.Hash, addr core.Address, key core.Hash) (StorageProof, bool) {
	ap, found := GenerateAccountProof(nodes, stateRoot, addr)
	if !found || ap.Account.StorageRoot.IsZero() {
		return StorageProof{}, false
	}
	slotProof, value, found := trie.GenerateProof(nodes, ap.Account.StorageRoot, key[:])
	if !found {
		return StorageProof{}, false
	}
	var v core.Hash
	copy(v[:], value)
	return StorageProof{AccountProof: ap, SlotProof: slotProof, Value: v}, true
}

// VerifyStorageProof re-derives both stages independently: it verifies the
// account proof against stateRoot exactly as VerifyAccountProof does, then
// verifies the slot proof against that *verified* account's own
// StorageRoot -- never against a root taken from anywhere else.
func VerifyStorageProof(stateRoot core.Hash, addr core.Address, key core.Hash, sp StorageProof) bool {
	if !VerifyAccountProof(stateRoot, addr, sp.AccountProof) {
		return false
	}
	return trie.VerifyProof(sp.AccountProof.Account.StorageRoot, key[:], sp.Value[:], sp.SlotProof)
}
