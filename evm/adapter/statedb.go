package adapter

import (
	"fmt"

	"l1chain/core"
	"l1chain/state"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
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

	refund uint64

	// touched, selfDestructed, and newContracts are per-instance,
	// never-persisted-to-base bookkeeping, journaled like every other
	// mutation for correct Snapshot/RevertToSnapshot behavior. l1chain's own
	// base state can only represent "non-empty and persisted" or "absent"
	// (state/mpt.go's SetAccount deletes any all-zero account outright), so
	// on its own it has no way to express EIP-161's "exists but is
	// currently empty" -- e.g. immediately after CreateAccount, before any
	// balance/code lands. This mirrors go-ethereum's own in-memory
	// stateObjects map, which plays exactly this role until Finalise (PR4)
	// decides what actually gets persisted.
	touched        map[core.Address]bool
	selfDestructed map[core.Address]bool
	newContracts   map[core.Address]bool

	// warmAddrs/warmSlots (EIP-2929/2930 access lists) and transient
	// (EIP-1153 transient storage) are reset wholesale by Prepare, exactly
	// once per transaction -- mirroring go-ethereum's own StateDB.Prepare,
	// which replaces s.accessList/s.transientStorage outright rather than
	// incrementally merging into whatever was there before. Individual
	// AddAddressToAccessList/AddSlotToAccessList/SetTransientState calls
	// AFTER that reset are journaled as usual.
	warmAddrs map[core.Address]bool
	warmSlots map[addrSlot]bool
	transient map[addrSlot]core.Hash
}

// addrSlot is the composite (address, storage key) key access-list slot
// tracking and transient storage share.
type addrSlot struct {
	addr core.Address
	slot core.Hash
}

// touch marks addr as existing for the lifetime of this StateDB instance
// (or until reverted). A no-op if addr is already touched -- matching
// go-ethereum's own getOrNewStateObject, which only journals account
// creation once per address.
func (s *StateDB) touch(addr core.Address) {
	if s.touched[addr] {
		return
	}
	if s.touched == nil {
		s.touched = make(map[core.Address]bool)
	}
	s.touched[addr] = true
	s.journal.entries = append(s.journal.entries, func(s *StateDB) {
		delete(s.touched, addr)
	})
}

// basePersisted reports whether addr has any real, already-persisted state
// in base. l1chain's own trie only ever stores non-empty accounts
// (state/mpt.go's SetAccount deletes an all-zero one outright), so any of
// these four fields being non-zero is equivalent to "the trie would find
// something for this address."
func basePersisted(acct state.Account) bool {
	return acct.Balance != 0 || acct.Nonce != 0 || !acct.CodeHash.IsZero() || !acct.StorageRoot.IsZero()
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

// SetNonce always touches addr, even when nonce is unchanged -- matching
// go-ethereum's own SetNonce, which always calls getOrNewStateObject
// regardless of whether the value actually changes.
func (s *StateDB) SetNonce(addr common.Address, nonce uint64, _ tracing.NonceChangeReason) {
	a := toCoreAddress(addr)
	s.touch(a)
	acct := s.base.GetAccount(a)
	prev := acct.Nonce
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

// AddBalance always touches addr, even for a zero amount -- matching
// go-ethereum's own AddBalance (always calls getOrNewStateObject) and the
// real vm.EVM.Call's own comment that even a zero-value transfer must run
// "to ensure the state clearing mechanism is applied."
func (s *StateDB) AddBalance(addr common.Address, amount *uint256.Int, _ tracing.BalanceChangeReason) uint256.Int {
	a := toCoreAddress(addr)
	s.touch(a)
	cur := s.base.GetAccount(a).Balance
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

// SubBalance always touches addr, for the same reason AddBalance does.
func (s *StateDB) SubBalance(addr common.Address, amount *uint256.Int, _ tracing.BalanceChangeReason) uint256.Int {
	a := toCoreAddress(addr)
	s.touch(a)
	cur := s.base.GetAccount(a).Balance
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
	s.touch(a)
	prev := s.base.GetCode(a)
	s.base.SetCode(a, code)
	s.journal.entries = append(s.journal.entries, func(s *StateDB) {
		s.base.SetCode(a, prev)
	})
	return prev
}

// --- Refund -------------------------------------------------------------

func (s *StateDB) AddRefund(gas uint64) {
	prev := s.refund
	s.refund += gas
	s.journal.entries = append(s.journal.entries, func(s *StateDB) {
		s.refund = prev
	})
}

// SubRefund panics if gas exceeds the current refund counter -- mirroring
// go-ethereum's own SubRefund exactly (core/state/statedb.go). This is a
// genuine invariant, not a defensive check to soften: the embedded
// interpreter's own opcode gas accounting guarantees SubRefund is never
// called with more than what AddRefund has already accumulated, so hitting
// this indicates a real internal bug that should fail loudly, not be
// papered over into a silent clamp.
func (s *StateDB) SubRefund(gas uint64) {
	prev := s.refund
	if gas > s.refund {
		panic(fmt.Sprintf("evm/adapter: refund counter below zero (gas: %d > refund: %d)", gas, s.refund))
	}
	s.refund -= gas
	s.journal.entries = append(s.journal.entries, func(s *StateDB) {
		s.refund = prev
	})
}

func (s *StateDB) GetRefund() uint64 { return s.refund }

// --- Existence, emptiness, self-destruct, contract lifecycle ------------

// Exist reports whether addr exists: either this StateDB instance has
// touched it (CreateAccount/CreateContract/Touch/any mutator/SelfDestruct),
// or it already has real persisted state in base. Per vm.StateDB's own
// contract, this must also return true for an address self-destructed
// earlier in the same lifetime -- true by construction here, since
// SelfDestruct always touches first.
func (s *StateDB) Exist(addr common.Address) bool {
	a := toCoreAddress(addr)
	return s.touched[a] || basePersisted(s.base.GetAccount(a))
}

// Touch accesses addr without returning anything, materializing it exactly
// like a mutator would (see touch's doc comment).
func (s *StateDB) Touch(addr common.Address) {
	s.touch(toCoreAddress(addr))
}

// Empty implements EIP-161: balance = nonce = code = 0. Deliberately
// narrower than basePersisted (which also considers StorageRoot) --
// matching real Ethereum's own EIP-161 spec, which does not consider
// storage either. This means an account with leftover storage but zero
// balance/nonce/code (a real, narrow edge case -- e.g. momentarily
// mid-self-destruct) is EIP-161-empty here even though l1chain's own trie
// would still be keeping it alive via that storage; worth re-checking once
// Finalise (PR4) decides what actually gets deleted.
func (s *StateDB) Empty(addr common.Address) bool {
	acct := s.base.GetAccount(toCoreAddress(addr))
	return acct.Balance == 0 && acct.Nonce == 0 && acct.CodeHash.IsZero()
}

// CreateAccount marks addr as existing. Real go-ethereum's own CreateAccount
// resets nonce/balance/code -- its own doc comment warns it "assumes the
// account did not previously exist... will silently overwrite it" if
// called otherwise -- but go-ethereum's real CALL/CREATE callers
// (core/vm/evm.go) only ever invoke CreateAccount when !Exist(addr) is
// already true, so there is never pre-existing balance/nonce/code to lose
// in practice. Mirrored here as a pure "mark touched," matching what the
// only real callers actually need, not the full reset semantics their doc
// comment defends against but never actually exercises.
func (s *StateDB) CreateAccount(addr common.Address) {
	s.touch(toCoreAddress(addr))
}

// CreateContract flags addr as having been deployed during this StateDB
// instance's lifetime -- the flag IsNewContract reads, and the one
// EIP-6780's real opSelfdestruct6780 opcode branches on to decide whether a
// same-tx SELFDESTRUCT actually destroys the contract or only transfers
// its balance.
func (s *StateDB) CreateContract(addr common.Address) {
	a := toCoreAddress(addr)
	s.touch(a)
	if s.newContracts[a] {
		return
	}
	if s.newContracts == nil {
		s.newContracts = make(map[core.Address]bool)
	}
	s.newContracts[a] = true
	s.journal.entries = append(s.journal.entries, func(s *StateDB) {
		delete(s.newContracts, a)
	})
}

// IsNewContract reports whether addr was deployed via CreateContract during
// this StateDB instance's lifetime (see CreateContract).
func (s *StateDB) IsNewContract(addr common.Address) bool {
	return s.newContracts[toCoreAddress(addr)]
}

// SelfDestruct marks addr as self-destructed. Mirroring go-ethereum's own
// SelfDestruct exactly, this does NOT itself move any balance or delete
// anything -- opSelfdestruct6780 (core/vm/instructions.go) makes the
// AddBalance/SubBalance calls itself before calling this, and real deletion
// happens later, during Finalise (PR4), only for addresses that are both
// self-destructed and IsNewContract (EIP-6780).
func (s *StateDB) SelfDestruct(addr common.Address) {
	a := toCoreAddress(addr)
	s.touch(a)
	if s.selfDestructed[a] {
		return
	}
	if s.selfDestructed == nil {
		s.selfDestructed = make(map[core.Address]bool)
	}
	s.selfDestructed[a] = true
	s.journal.entries = append(s.journal.entries, func(s *StateDB) {
		delete(s.selfDestructed, a)
	})
}

// --- Access lists (EIP-2929/2930) and transient storage (EIP-1153) ------

// addWarmAddr journals warming addr, if it wasn't already warm. Returns
// whether it actually changed anything -- AddSlotToAccessList needs this to
// decide whether the address itself also needs its own journal entry,
// mirroring go-ethereum's own AddAddressToAccessList/AddSlotToAccessList.
func (s *StateDB) addWarmAddr(a core.Address) bool {
	if s.warmAddrs[a] {
		return false
	}
	if s.warmAddrs == nil {
		s.warmAddrs = make(map[core.Address]bool)
	}
	s.warmAddrs[a] = true
	s.journal.entries = append(s.journal.entries, func(s *StateDB) {
		delete(s.warmAddrs, a)
	})
	return true
}

func (s *StateDB) AddAddressToAccessList(addr common.Address) {
	s.addWarmAddr(toCoreAddress(addr))
}

func (s *StateDB) AddSlotToAccessList(addr common.Address, slot common.Hash) {
	a := toCoreAddress(addr)
	s.addWarmAddr(a)
	key := addrSlot{addr: a, slot: toCoreHash(slot)}
	if s.warmSlots[key] {
		return
	}
	if s.warmSlots == nil {
		s.warmSlots = make(map[addrSlot]bool)
	}
	s.warmSlots[key] = true
	s.journal.entries = append(s.journal.entries, func(s *StateDB) {
		delete(s.warmSlots, key)
	})
}

func (s *StateDB) AddressInAccessList(addr common.Address) bool {
	return s.warmAddrs[toCoreAddress(addr)]
}

func (s *StateDB) SlotInAccessList(addr common.Address, slot common.Hash) (addressOk bool, slotOk bool) {
	a := toCoreAddress(addr)
	return s.warmAddrs[a], s.warmSlots[addrSlot{addr: a, slot: toCoreHash(slot)}]
}

// Prepare resets and re-warms the access list and clears transient storage
// for a new transaction -- mirroring go-ethereum's own Prepare exactly,
// including that this reset itself is NOT journaled (Prepare always runs
// before the first Snapshot of a transaction, so there is nothing before it
// a revert could ever need to restore).
func (s *StateDB) Prepare(rules params.Rules, sender, coinbase common.Address, dst *common.Address, precompiles []common.Address, list types.AccessList) {
	if rules.IsEIP2929 {
		s.warmAddrs = make(map[core.Address]bool)
		s.warmSlots = make(map[addrSlot]bool)

		s.warmAddrs[toCoreAddress(sender)] = true
		if dst != nil {
			s.warmAddrs[toCoreAddress(*dst)] = true
		}
		for _, addr := range precompiles {
			s.warmAddrs[toCoreAddress(addr)] = true
		}
		for _, el := range list {
			a := toCoreAddress(el.Address)
			s.warmAddrs[a] = true
			for _, key := range el.StorageKeys {
				s.warmSlots[addrSlot{addr: a, slot: toCoreHash(key)}] = true
			}
		}
		if rules.IsShanghai { // EIP-3651: warm coinbase
			s.warmAddrs[toCoreAddress(coinbase)] = true
		}
	}
	s.transient = make(map[addrSlot]core.Hash)
}

func (s *StateDB) GetTransientState(addr common.Address, key common.Hash) common.Hash {
	return toCommonHash(s.transient[addrSlot{addr: toCoreAddress(addr), slot: toCoreHash(key)}])
}

// SetTransientState no-ops on a value that wouldn't actually change,
// mirroring go-ethereum's own SetTransientState exactly (unlike
// SetNonce/AddBalance/SubBalance, which always touch even on a no-op --
// transient storage carries no existence/EIP-161 semantics, so there is
// nothing else a no-op write needs to trigger here).
func (s *StateDB) SetTransientState(addr common.Address, key, value common.Hash) {
	as := addrSlot{addr: toCoreAddress(addr), slot: toCoreHash(key)}
	v := toCoreHash(value)
	prev := s.transient[as]
	if prev == v {
		return
	}
	if s.transient == nil {
		s.transient = make(map[addrSlot]core.Hash)
	}
	s.transient[as] = v
	s.journal.entries = append(s.journal.entries, func(s *StateDB) {
		s.transient[as] = prev
	})
}

// HasSelfDestructed reports whether addr was marked via SelfDestruct during
// this StateDB instance's lifetime.
func (s *StateDB) HasSelfDestructed(addr common.Address) bool {
	return s.selfDestructed[toCoreAddress(addr)]
}
