// Package state defines the StateDB abstraction over accounts and their
// balances/nonces, plus an in-memory key-value implementation for M1.
//
// The interface is intentionally storage-agnostic so the implementation can be
// swapped from the simple KV/hash model used in M1 to a Merkle-Patricia-Trie
// backed model required for EVM equivalence in M4, without changing block
// validation, RPC, or the VM entrypoint.
//
// M3 extends the interface with contract code and per-account key/value storage
// (both 32-byte words). StateRoot folds accounts, code, and storage
// deterministically (addresses sorted, and each account's storage keys sorted)
// so the root stays reproducible across mining and validation on every node.
package state

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"sort"

	"l1chain/core"
)

// Account is the mutable state of an address.
type Account struct {
	Balance uint64
	Nonce   uint64
	// CodeHash and StorageRoot are unused in M1 (no contracts yet) but are
	// carried so the header/account layout is stable through M3/M4.
	CodeHash    core.Hash
	StorageRoot core.Hash
}

// StateDB is the account-state interface consumed by consensus, RPC and the VM.
// M1 provides a KV implementation; M4 will provide an MPT implementation.
type StateDB interface {
	GetAccount(addr core.Address) Account
	SetAccount(addr core.Address, acct Account)

	// GetCode/SetCode manage contract runtime bytecode (M3). Accounts without
	// deployed code return a nil/empty slice.
	GetCode(addr core.Address) []byte
	SetCode(addr core.Address, code []byte)

	// GetStorage/SetStorage manage a contract's 32-byte word key/value store
	// (M3). Unknown keys read as the zero Hash; writing the zero Hash clears the
	// slot so it does not perturb the state root.
	GetStorage(addr core.Address, key core.Hash) core.Hash
	SetStorage(addr core.Address, key, val core.Hash)

	StateRoot() core.Hash
	Commit() core.Hash
}

// memStateDB is an in-memory KV StateDB. StateRoot is a deterministic hash over
// the sorted (address, account, code, storage) set — a placeholder for the MPT
// root that keeps blocks verifiable in M1–M3.
type memStateDB struct {
	accounts map[core.Address]Account
	code     map[core.Address][]byte
	storage  map[core.Address]map[core.Hash]core.Hash
}

// NewMemStateDB returns an empty in-memory StateDB.
func NewMemStateDB() StateDB {
	return &memStateDB{
		accounts: make(map[core.Address]Account),
		code:     make(map[core.Address][]byte),
		storage:  make(map[core.Address]map[core.Hash]core.Hash),
	}
}

func (s *memStateDB) GetAccount(addr core.Address) Account {
	return s.accounts[addr] // zero value for unknown accounts
}

func (s *memStateDB) SetAccount(addr core.Address, acct Account) {
	s.accounts[addr] = acct
}

func (s *memStateDB) GetCode(addr core.Address) []byte {
	c := s.code[addr]
	if len(c) == 0 {
		return nil
	}
	out := make([]byte, len(c))
	copy(out, c)
	return out
}

func (s *memStateDB) SetCode(addr core.Address, code []byte) {
	if len(code) == 0 {
		delete(s.code, addr)
		return
	}
	cp := make([]byte, len(code))
	copy(cp, code)
	s.code[addr] = cp
}

func (s *memStateDB) GetStorage(addr core.Address, key core.Hash) core.Hash {
	m := s.storage[addr]
	if m == nil {
		return core.Hash{}
	}
	return m[key] // zero Hash for unknown keys
}

func (s *memStateDB) SetStorage(addr core.Address, key, val core.Hash) {
	if val.IsZero() {
		if m := s.storage[addr]; m != nil {
			delete(m, key)
			if len(m) == 0 {
				delete(s.storage, addr)
			}
		}
		return
	}
	m := s.storage[addr]
	if m == nil {
		m = make(map[core.Hash]core.Hash)
		s.storage[addr] = m
	}
	m[key] = val
}

// hasState reports whether addr contributes anything to the state root: a
// non-empty account, deployed code, or non-empty storage.
func (s *memStateDB) hasState(addr core.Address) bool {
	acct := s.accounts[addr]
	if acct.Balance != 0 || acct.Nonce != 0 || !acct.CodeHash.IsZero() {
		return true
	}
	if len(s.code[addr]) != 0 {
		return true
	}
	return len(s.storage[addr]) != 0
}

func (s *memStateDB) StateRoot() core.Hash {
	seen := make(map[core.Address]struct{})
	for a := range s.accounts {
		if s.hasState(a) {
			seen[a] = struct{}{}
		}
	}
	for a, c := range s.code {
		if len(c) != 0 {
			seen[a] = struct{}{}
		}
	}
	for a, m := range s.storage {
		if len(m) != 0 {
			seen[a] = struct{}{}
		}
	}

	addrs := make([]core.Address, 0, len(seen))
	for a := range seen {
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool { return addrs[i].Less(addrs[j]) })

	h := sha256.New()
	var buf [8]byte
	for _, a := range addrs {
		acct := s.accounts[a]
		h.Write(a[:])
		binary.BigEndian.PutUint64(buf[:], acct.Balance)
		h.Write(buf[:])
		binary.BigEndian.PutUint64(buf[:], acct.Nonce)
		h.Write(buf[:])
		h.Write(acct.CodeHash[:])
		h.Write(acct.StorageRoot[:])

		// Fold code deterministically: length prefix + bytes.
		code := s.code[a]
		binary.BigEndian.PutUint64(buf[:], uint64(len(code)))
		h.Write(buf[:])
		h.Write(code)

		// Fold storage deterministically: sorted keys, length-prefixed.
		m := s.storage[a]
		keys := make([]core.Hash, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i][:], keys[j][:]) < 0 })
		binary.BigEndian.PutUint64(buf[:], uint64(len(keys)))
		h.Write(buf[:])
		for _, k := range keys {
			v := m[k]
			h.Write(k[:])
			h.Write(v[:])
		}
	}
	var root core.Hash
	copy(root[:], h.Sum(nil))
	return root
}

// Commit is a no-op for the in-memory implementation (nothing to flush) and
// returns the current root. A persistent implementation flushes here.
func (s *memStateDB) Commit() core.Hash { return s.StateRoot() }
