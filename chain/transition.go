package chain

import (
	"errors"

	"l1chain/core"
	"l1chain/state"
	"l1chain/vm"
)

// Sentinel errors returned by the state-transition function.
var (
	// ErrBadSig is returned when verifySig rejects the transaction signature.
	ErrBadSig = errors.New("chain: invalid transaction signature")
	// ErrBadNonce is returned when tx.Nonce != sender account nonce.
	ErrBadNonce = errors.New("chain: bad transaction nonce")
	// ErrInsufficientBalance is returned when the sender cannot cover Value.
	ErrInsufficientBalance = errors.New("chain: insufficient balance")
	// ErrCantAffordGas is returned when the sender cannot cover the up-front gas
	// reservation (GasLimit * GasPrice) for a contract transaction.
	ErrCantAffordGas = errors.New("chain: insufficient balance for gas")
)

// GasPrice is the native-coin price per unit of gas for contract transactions
// (M3). It is a fixed constant so gas accounting is deterministic across nodes.
// A failed/out-of-gas contract tx still consumes gas and advances the sender
// nonce (EVM-ish semantics) but reverts all storage writes.
const GasPrice uint64 = 1


// BlockReward is the coinbase reward credited to a block's miner.
const BlockReward uint64 = 50

// AcceptNonEmptySig is the default, placeholder signature verifier: it accepts
// any transaction carrying a non-empty signature. The real secp256k1 verifier
// is injected by a later slice.
func AcceptNonEmptySig(tx core.Transaction) bool { return len(tx.Signature) > 0 }

// ApplyTx applies a single transaction against st, mutating it in place. It
// verifies the signature and enforces exact-nonce ordering. Empty-Data value
// transfers to a codeless, non-zero recipient take the plain-transfer path
// (debit sender, credit recipient, bump nonce). Every other shape — contract
// creation (To == zero), a call carrying Data, or a call to an account with
// code — is routed through the StackVM via applyContractTx.
func ApplyTx(st state.StateDB, tx core.Transaction, verifySig func(core.Transaction) bool) error {
	if verifySig == nil || !verifySig(tx) {
		return ErrBadSig
	}
	from := st.GetAccount(tx.From)
	if tx.Nonce != from.Nonce {
		return ErrBadNonce
	}

	if isContractTx(st, tx) {
		return applyContractTx(st, tx, from)
	}

	if from.Balance < tx.Value {
		return ErrInsufficientBalance
	}

	// Self-transfer: balance nets to zero, but the nonce still advances.
	if tx.From == tx.To {
		from.Nonce++
		st.SetAccount(tx.From, from)
		return nil
	}

	to := st.GetAccount(tx.To)
	from.Balance -= tx.Value
	from.Nonce++
	to.Balance += tx.Value
	st.SetAccount(tx.From, from)
	st.SetAccount(tx.To, to)
	return nil
}

// isContractTx reports whether tx must be routed through the VM rather than the
// plain-transfer path: contract creation (To == zero Address), any tx carrying
// call Data, or a call to an account that has deployed code.
func isContractTx(st state.StateDB, tx core.Transaction) bool {
	var zero core.Address
	if tx.To == zero {
		return true // contract creation
	}
	if len(tx.Data) > 0 {
		return true // call with calldata
	}
	return len(st.GetCode(tx.To)) > 0 // call to a contract account
}

// applyContractTx runs a contract creation or call through the StackVM. Gas is
// reserved up front (GasLimit * GasPrice) and, together with the nonce bump,
// always persists — even when execution reverts or runs out of gas. Unused gas
// is refunded. On success the VM has already committed value transfer and
// storage writes to st; on failure those writes are reverted by the VM, leaving
// only the gas charge and nonce increment. The tx is only rejected outright
// (nonce NOT advanced) when the sender cannot afford the up-front gas.
func applyContractTx(st state.StateDB, tx core.Transaction, from state.Account) error {
	gasReserve := tx.GasLimit * GasPrice
	if GasPrice != 0 && tx.GasLimit != 0 && gasReserve/GasPrice != tx.GasLimit {
		return ErrCantAffordGas // multiplication overflow: unaffordable by definition
	}
	if from.Balance < gasReserve {
		return ErrCantAffordGas
	}

	// Reserve gas and advance nonce up front; both persist regardless of the
	// execution outcome.
	from.Balance -= gasReserve
	from.Nonce++
	st.SetAccount(tx.From, from)

	var to *core.Address
	var zero core.Address
	if tx.To != zero {
		dst := tx.To
		to = &dst
	}
	receipt := vm.StackVM{}.Execute(st, vm.Message{
		From:     tx.From,
		To:       to,
		Value:    tx.Value,
		GasLimit: tx.GasLimit,
		Nonce:    tx.Nonce,
		Data:     tx.Data,
	})

	// Refund unused gas (GasUsed == GasLimit on out-of-gas, so refund is 0).
	refund := (tx.GasLimit - receipt.GasUsed) * GasPrice
	acct := st.GetAccount(tx.From)
	acct.Balance += refund
	st.SetAccount(tx.From, acct)
	return nil
}

// ApplyBlock applies every transaction in b in order, then credits BlockReward
// to miner (coinbase). Application is atomic: if any transaction fails, st is
// left unchanged and the tx error is returned. Transactions are staged on an
// overlay and only flushed to st once the whole block succeeds.
func ApplyBlock(st state.StateDB, b core.Block, miner core.Address, verifySig func(core.Transaction) bool) error {
	ov := newOverlay(st)
	for i := range b.Txs {
		if err := ApplyTx(ov, b.Txs[i], verifySig); err != nil {
			return err
		}
	}
	m := ov.GetAccount(miner)
	m.Balance += BlockReward
	ov.SetAccount(miner, m)

	ov.flush()
	return nil
}

// overlayState is a copy-on-write staging layer over a base StateDB. Reads fall
// through to the base until an address is written; flush commits the staged
// writes to the base. It lets ApplyBlock validate a whole block before mutating
// committed state.
type overlayState struct {
	base    state.StateDB
	dirty   map[core.Address]state.Account
	code    map[core.Address][]byte
	storage map[core.Address]map[core.Hash]core.Hash
}

func newOverlay(base state.StateDB) *overlayState {
	return &overlayState{
		base:    base,
		dirty:   make(map[core.Address]state.Account),
		code:    make(map[core.Address][]byte),
		storage: make(map[core.Address]map[core.Hash]core.Hash),
	}
}

func (o *overlayState) GetAccount(addr core.Address) state.Account {
	if acct, ok := o.dirty[addr]; ok {
		return acct
	}
	return o.base.GetAccount(addr)
}

func (o *overlayState) SetAccount(addr core.Address, acct state.Account) {
	o.dirty[addr] = acct
}

func (o *overlayState) GetCode(addr core.Address) []byte {
	if c, ok := o.code[addr]; ok {
		return c
	}
	return o.base.GetCode(addr)
}

func (o *overlayState) SetCode(addr core.Address, code []byte) {
	o.code[addr] = code
}

func (o *overlayState) GetStorage(addr core.Address, key core.Hash) core.Hash {
	if m, ok := o.storage[addr]; ok {
		if v, ok2 := m[key]; ok2 {
			return v
		}
	}
	return o.base.GetStorage(addr, key)
}

func (o *overlayState) SetStorage(addr core.Address, key, val core.Hash) {
	m := o.storage[addr]
	if m == nil {
		m = make(map[core.Hash]core.Hash)
		o.storage[addr] = m
	}
	m[key] = val
}

// StateRoot/Commit delegate to the base. They are not consulted between the
// staging and flush of a block, so the un-flushed overlay never reports a root.
func (o *overlayState) StateRoot() core.Hash { return o.base.StateRoot() }
func (o *overlayState) Commit() core.Hash    { return o.base.Commit() }

func (o *overlayState) flush() {
	for addr, acct := range o.dirty {
		o.base.SetAccount(addr, acct)
	}
	for addr, code := range o.code {
		o.base.SetCode(addr, code)
	}
	for addr, m := range o.storage {
		for key, val := range m {
			o.base.SetStorage(addr, key, val)
		}
	}
}
