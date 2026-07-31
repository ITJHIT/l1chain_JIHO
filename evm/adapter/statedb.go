package adapter

import (
	"fmt"

	"l1chain/state"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/holiman/uint256"
)

// StateDB wraps an l1chain state.StateDB (state.New()'s real MPT in
// production, state.NewMemStateDB() in isolated tests) with journaled
// snapshot/revert support and (as this package grows across PR1-PR4)
// go-ethereum's full vm.StateDB method surface.
//
// base is mutated eagerly -- matching mptStateDB's own "eager mutation, no
// internal batching" convention (state/mpt.go's doc comment). Staging and
// rollback are the journal's job (journal.go), never base's.
type StateDB struct {
	base    state.StateDB
	journal journal

	// err latches the first unrecoverable error encountered (currently:
	// a balance overflowing l1chain's uint64 ceiling -- see setBalance).
	// Future wiring (PR6) treats this exactly like an out-of-gas revert:
	// once set, the tx's EVM-side changes are discarded, but up-front
	// reserved gas/nonce still applies, matching this codebase's existing
	// convention for other rejected-tx classes. Never a block-validation
	// error -- it's a pure function of on-chain state, so every honest
	// node reaches the same outcome from the same inputs.
	err error
}

// New wraps base with journaled snapshot/revert support.
func New(base state.StateDB) *StateDB {
	return &StateDB{base: base}
}

// Err returns the first unrecoverable error latched during this StateDB's
// lifetime, if any -- see the err field's doc comment.
func (s *StateDB) Err() error { return s.err }

// Snapshot/RevertToSnapshot implement vm.StateDB's numbered-checkpoint
// contract; see journal.go.
func (s *StateDB) Snapshot() int           { return s.journal.snapshot() }
func (s *StateDB) RevertToSnapshot(id int) { s.journal.revert(s, id) }

// --- Nonce -------------------------------------------------------------

func (s *StateDB) GetNonce(addr common.Address) uint64 {
	return s.base.GetAccount(toCoreAddress(addr)).Nonce
}

func (s *StateDB) SetNonce(addr common.Address, nonce uint64, _ tracing.NonceChangeReason) {
	a := toCoreAddress(addr)
	acct := s.base.GetAccount(a)
	prev := acct.Nonce
	if prev == nonce {
		return
	}
	acct.Nonce = nonce
	s.base.SetAccount(a, acct)
	s.journal.entries = append(s.journal.entries, func(s *StateDB) {
		acct := s.base.GetAccount(a)
		acct.Nonce = prev
		s.base.SetAccount(a, acct)
	})
}

// --- Balance -------------------------------------------------------------

func (s *StateDB) GetBalance(addr common.Address) *uint256.Int {
	return toUint256(s.base.GetAccount(toCoreAddress(addr)).Balance)
}

// setBalance is the shared undo-recording primitive for AddBalance/
// SubBalance: it writes newBalance, records an undo back to the previous
// value, and returns the previous value -- what vm.StateDB requires both
// callers to return.
func (s *StateDB) setBalance(addr common.Address, newBalance uint64) uint256.Int {
	a := toCoreAddress(addr)
	acct := s.base.GetAccount(a)
	prev := acct.Balance
	acct.Balance = newBalance
	s.base.SetAccount(a, acct)
	s.journal.entries = append(s.journal.entries, func(s *StateDB) {
		acct := s.base.GetAccount(a)
		acct.Balance = prev
		s.base.SetAccount(a, acct)
	})
	return *toUint256(prev)
}

func (s *StateDB) AddBalance(addr common.Address, amount *uint256.Int, _ tracing.BalanceChangeReason) uint256.Int {
	if amount.IsZero() {
		return *s.GetBalance(addr)
	}
	cur := s.base.GetAccount(toCoreAddress(addr)).Balance
	sum := new(uint256.Int).Add(toUint256(cur), amount)
	next, overflow := sum.Uint64WithOverflow()
	if overflow {
		if s.err == nil {
			s.err = fmt.Errorf("evm/adapter: balance of %s overflows uint64 adding %s to %d", addr, amount, cur)
		}
		return *toUint256(cur)
	}
	return s.setBalance(addr, next)
}

func (s *StateDB) SubBalance(addr common.Address, amount *uint256.Int, _ tracing.BalanceChangeReason) uint256.Int {
	if amount.IsZero() {
		return *s.GetBalance(addr)
	}
	cur := s.base.GetAccount(toCoreAddress(addr)).Balance
	// Sub on a *uint256.Int wraps modulo 2^256 (real EVM SUB semantics) when
	// amount > cur, so the result no longer fits in uint64 -- the same
	// Uint64WithOverflow() check below catches both true overflow and
	// underflow with no separate comparison needed. go-ethereum's own real
	// StateDB doesn't guard against underflow here either: CanTransfer /
	// opcode gas accounting is what actually keeps an unaffordable
	// SubBalance from being reached, mirrored rather than duplicated here.
	diff := new(uint256.Int).Sub(toUint256(cur), amount)
	next, overflow := diff.Uint64WithOverflow()
	if overflow {
		if s.err == nil {
			s.err = fmt.Errorf("evm/adapter: balance of %s underflows subtracting %s from %d", addr, amount, cur)
		}
		return *toUint256(cur)
	}
	return s.setBalance(addr, next)
}

// --- Code -------------------------------------------------------------

func (s *StateDB) GetCode(addr common.Address) []byte {
	return s.base.GetCode(toCoreAddress(addr))
}

func (s *StateDB) GetCodeSize(addr common.Address) int {
	return len(s.base.GetCode(toCoreAddress(addr)))
}

// GetCodeHash uses l1chain's own convention -- the all-zero Hash for an
// account with no code (state/mpt.go's SetCode, len(code)==0 branch) --
// never go-ethereum's keccak256(nil) EmptyCodeHash constant. l1chain's trie
// is SHA-256 throughout with zero keccak usage anywhere; matching its own
// existing zero-means-absent convention here keeps this adapter consistent
// with every other codehash check already in this codebase, rather than
// introducing a second, keccak-flavored "empty" sentinel value.
func (s *StateDB) GetCodeHash(addr common.Address) common.Hash {
	return toCommonHash(s.base.GetAccount(toCoreAddress(addr)).CodeHash)
}

// SetCode sets addr's runtime code and returns the previous code, per
// vm.StateDB's contract. Reverting replays base.SetCode with the previous
// bytes, which correctly recomputes the previous CodeHash too (SetCode
// derives CodeHash from code content) -- no separate CodeHash undo needed.
func (s *StateDB) SetCode(addr common.Address, code []byte, _ tracing.CodeChangeReason) []byte {
	a := toCoreAddress(addr)
	prev := s.base.GetCode(a)
	s.base.SetCode(a, code)
	s.journal.entries = append(s.journal.entries, func(s *StateDB) {
		s.base.SetCode(a, prev)
	})
	return prev
}
