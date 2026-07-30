package state

import (
	"encoding/binary"

	"l1chain/core"
	"l1chain/trie"
)

// mptStateDB is the production StateDB: a two-level Merkle Patricia Trie --
// a world trie of accounts keyed by address, and, per account, its own
// storage trie keyed by storage word, whose root lives in that account's
// StorageRoot field (mirroring real Ethereum's design, and finally giving
// that field -- dead in memStateDB -- a real purpose).
type mptStateDB struct {
	nodes map[core.Hash][]byte // every node, both trie levels together
	code  map[core.Hash][]byte // codeHash -> raw bytecode, content-addressed
	root  core.Hash            // current world-trie root
}

// New returns an empty StateDB backed by the real MPT. This is the
// production factory; NewMemStateDB's flat-hash implementation remains as an
// independent reference implementation (see state/statedb.go), not used in
// production.
func New() StateDB {
	return &mptStateDB{
		nodes: make(map[core.Hash][]byte),
		code:  make(map[core.Hash][]byte),
	}
}

// accountEncLen: Balance(8) + Nonce(8) + CodeHash(32) + StorageRoot(32).
const accountEncLen = 8 + 8 + core.HashLen + core.HashLen

func encodeAccount(a Account) []byte {
	out := make([]byte, accountEncLen)
	binary.BigEndian.PutUint64(out[0:8], a.Balance)
	binary.BigEndian.PutUint64(out[8:16], a.Nonce)
	copy(out[16:16+core.HashLen], a.CodeHash[:])
	copy(out[16+core.HashLen:16+2*core.HashLen], a.StorageRoot[:])
	return out
}

func decodeAccount(b []byte) Account {
	if len(b) < accountEncLen {
		return Account{}
	}
	var a Account
	a.Balance = binary.BigEndian.Uint64(b[0:8])
	a.Nonce = binary.BigEndian.Uint64(b[8:16])
	copy(a.CodeHash[:], b[16:16+core.HashLen])
	copy(a.StorageRoot[:], b[16+core.HashLen:16+2*core.HashLen])
	return a
}

// isEmpty reports whether a contributes nothing to the state root -- the MPT
// equivalent of memStateDB's hasState check, but collapsed to a pure
// function of the Account struct's own four fields now that CodeHash and
// StorageRoot are populated for real instead of always being dead zeros.
func (a Account) isEmpty() bool {
	return a.Balance == 0 && a.Nonce == 0 && a.CodeHash.IsZero() && a.StorageRoot.IsZero()
}

func (s *mptStateDB) GetAccount(addr core.Address) Account {
	val, found := trie.Get(s.nodes, s.root, addr[:])
	if !found {
		return Account{}
	}
	return decodeAccount(val)
}

func (s *mptStateDB) SetAccount(addr core.Address, acct Account) {
	if acct.isEmpty() {
		// Deleting rather than storing an all-zero encoding is what keeps an
		// untouched address and an explicitly-zeroed one producing the exact
		// same root (see state/mpt_test.go's empty-is-zero-stable case) --
		// exactly mirroring memStateDB.hasState's exclusion, now expressed as
		// a genuine trie deletion rather than a root-fold skip.
		s.root = trie.Delete(s.nodes, s.root, addr[:])
		return
	}
	s.root = trie.Put(s.nodes, s.root, addr[:], encodeAccount(acct))
}

func (s *mptStateDB) GetCode(addr core.Address) []byte {
	acct := s.GetAccount(addr)
	if acct.CodeHash.IsZero() {
		return nil
	}
	c := s.code[acct.CodeHash]
	if len(c) == 0 {
		return nil
	}
	out := make([]byte, len(c))
	copy(out, c)
	return out
}

// SetCode wires CodeHash into the account leaf for real -- unlike
// memStateDB, where CodeHash is a dead field and only a side map of raw
// bytecode affects the root. Without this, a block producer could redeploy
// different bytecode at the same address across two chains without changing
// StateRoot, which would be a real regression against memStateDB's existing
// behavior (its flat fold does hash code content into the root today).
func (s *mptStateDB) SetCode(addr core.Address, code []byte) {
	acct := s.GetAccount(addr)
	if len(code) == 0 {
		acct.CodeHash = core.Hash{}
	} else {
		h := core.SumHash(code)
		acct.CodeHash = h
		if _, exists := s.code[h]; !exists {
			cp := make([]byte, len(code))
			copy(cp, code)
			s.code[h] = cp
		}
	}
	s.SetAccount(addr, acct)
}

func (s *mptStateDB) GetStorage(addr core.Address, key core.Hash) core.Hash {
	acct := s.GetAccount(addr)
	if acct.StorageRoot.IsZero() {
		return core.Hash{}
	}
	val, found := trie.Get(s.nodes, acct.StorageRoot, key[:])
	if !found {
		return core.Hash{}
	}
	var h core.Hash
	copy(h[:], val)
	return h
}

// SetStorage mutates addr's own storage trie, then re-sets the account so
// the new StorageRoot propagates into the world trie -- the same shape as
// real Ethereum's SSTORE. An address with no storage trie yet reads
// acct.StorageRoot as core.Hash{}, which trie.Put/trie.Delete already treat
// as "empty trie," so the first SetStorage on a fresh address needs no
// special-casing here.
func (s *mptStateDB) SetStorage(addr core.Address, key, val core.Hash) {
	acct := s.GetAccount(addr)
	if val.IsZero() {
		acct.StorageRoot = trie.Delete(s.nodes, acct.StorageRoot, key[:])
	} else {
		acct.StorageRoot = trie.Put(s.nodes, acct.StorageRoot, key[:], val[:])
	}
	s.SetAccount(addr, acct)
}

// StateRoot/Commit: mutation is eager (every Set* call above already updated
// s.root), so both just return the current root -- matching memStateDB's
// existing "Commit is a no-op, returns the current root" contract exactly,
// so callers that already treat the two as interchangeable see no change in
// behavior.
func (s *mptStateDB) StateRoot() core.Hash { return s.root }
func (s *mptStateDB) Commit() core.Hash    { return s.root }
