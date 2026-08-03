package chain

import (
	"errors"

	"l1chain/core"
	"l1chain/exchange"
	"l1chain/pos"
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
	// reservation (GasLimit * GasFeeCap) for a contract/EVM transaction.
	ErrCantAffordGas = errors.New("chain: insufficient balance for gas")
)

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
	return ApplyTxAt(st, tx, verifySig, 0, 0)
}

// ApplyTxAt is ApplyTx with the transaction's position in the chain.
//
// The exchange needs it: an order's identity is (block height, index within
// block), which is the only source of order IDs that two validators agree on
// without coordinating and that a replay from genesis reproduces exactly. A
// counter kept in the exchange would diverge the moment two nodes processed a
// different number of rejected transactions.
//
// ApplyTx forwards with (0, 0), which is correct for every non-exchange
// transaction because none of them read the position.
//
// This standalone entrypoint has no chain/genesis to derive a fee-market
// BaseFee from, so it prices any contract/EVM transaction at BaseFee=0 (pure
// tip, no burn) -- documented, not silently arbitrary. A caller that wants
// real fee-market pricing goes through Chain.AddBlock/CandidateStateRoot
// instead, which always have a real parent header to derive BaseFee from.
func ApplyTxAt(st state.StateDB, tx core.Transaction, verifySig func(core.Transaction) bool, height uint64, index uint32) error {
	_, _, err := applyTxAtSession(st, tx, verifySig, height, index, nil, 0)
	return err
}

// applyTxAtSession is ApplyTxAt with an optional BatchSession.
//
// This is the ONE place that decides how an exchange transaction is handled --
// deliberately, after this package shipped with two call sites that each
// dispatched to the state transition independently and only one of them ever
// got the exchange routing fixed when it needed to change (see the commit that
// added ApplyTxAt in the first place: it fixed transition.go's ApplyBlock,
// which turned out to have no production callers, while chain.go's actual
// consensus path kept calling the old bare ApplyTx for another commit's worth
// of history). Every caller in this package, continuous or batch, production
// or test, now goes through this one function.
//
// session == nil means Continuous semantics: an exchange transaction settles
// immediately via exchange.Apply, exactly as it always has. session != nil
// means a BatchAuction block is in progress: an exchange transaction is staged
// into that session instead, and nothing about it settles until the caller
// runs session.Finish once, after every transaction in the block has been
// through this function.
//
// baseFee (M9) is this block's fee-market base fee, consulted ONLY by the
// contract/EVM branches below (applyContractTx/applyEVMTx) -- the two
// transaction shapes that already had a gas concept before M9. Plain
// transfers, exchange orders, and PoS attestations remain exactly as
// fee-exempt as they always were; baseFee is simply unused on those paths.
// Returns (gasUsed, tip, err): gasUsed/tip are always 0 on the fee-exempt
// paths and on any error; tip is the priority-fee portion Chain.AddBlock/
// CandidateStateRoot credit to the block's Coinbase, on top of BlockReward.
func applyTxAtSession(st state.StateDB, tx core.Transaction, verifySig func(core.Transaction) bool, height uint64, index uint32, session *exchange.BatchSession, baseFee uint64) (uint64, uint64, error) {
	if verifySig == nil || !verifySig(tx) {
		return 0, 0, ErrBadSig
	}
	from := st.GetAccount(tx.From)
	if tx.Nonce != from.Nonce {
		return 0, 0, ErrBadNonce
	}

	// The exchange is a reserved address the state transition routes to instead
	// of the VM -- the same shape as a precompile. It is checked before
	// isContractTx because an order carries calldata and would otherwise be sent
	// to the StackVM, which has no idea what an order is.
	if exchange.IsExchangeTx(tx) {
		if from.Balance < tx.Value {
			return 0, 0, ErrInsufficientBalance
		}
		// The nonce advances before the order runs, exactly as it does for a
		// contract call: a rejected order must still consume its nonce, or the
		// sender can replay it and the mempool cannot make progress past it.
		from.Nonce++
		st.SetAccount(tx.From, from)
		if session != nil {
			return 0, 0, session.Apply(height, index, tx.From, tx.Data)
		}
		return 0, 0, exchange.Apply(st, tx, height, index)
	}

	// Real embedded EVM contracts (evm/adapter) are routed here, before
	// isContractTx, for the same reason the exchange check above runs
	// first: a deployed EVM contract "has code" by isContractTx's own
	// definition too, and would otherwise be misrouted to the StackVM. See
	// isEVMTx's own doc comment (chain/evm_wiring.go).
	if isEVMTx(st, tx) {
		gasUsed, err := applyEVMTx(st, tx, from, height, baseFee)
		if err != nil {
			return 0, 0, err
		}
		// EffectiveGasPrice's inputs are identical to what applyEVMTx already
		// validated, so this re-derivation cannot itself fail -- it exists
		// only to recover the priorityFee split without threading a third
		// return value through applyEVMTx's own VM-execution plumbing.
		_, priorityFee, _ := EffectiveGasPrice(baseFee, tx.GasFeeCap, tx.GasTipCap)
		return gasUsed, gasUsed * priorityFee, nil
	}

	// PoS checkpoint attestations (M8) are a reserved address the state
	// transition routes to instead of the VM -- the same shape as the
	// exchange/EVM checks above. Checked before isContractTx for the same
	// reason: an attestation tx's calldata (its BLS signature plus target
	// height/hash, see pos.EncodeAttest) is non-empty, so isContractTx would
	// otherwise misroute it to the StackVM, which has no idea what an
	// attestation is. This is a pure nonce-bumping no-op at THIS layer --
	// no value movement, no VM -- because BLS verification and stake
	// tallying are whole-BLOCK concerns Chain.AddBlock handles one layer up
	// (see chain.go's verifyAttestations/recordAttestations), not a per-tx
	// state-transition concern. Fee-exempt, same as every other consensus-
	// infrastructure (not economic) transaction shape -- see this function's
	// own doc comment.
	if pos.IsAttestationTx(tx) {
		from.Nonce++
		st.SetAccount(tx.From, from)
		return 0, 0, nil
	}

	if isContractTx(st, tx) {
		gasUsed, err := applyContractTx(st, tx, from, baseFee)
		if err != nil {
			return 0, 0, err
		}
		_, priorityFee, _ := EffectiveGasPrice(baseFee, tx.GasFeeCap, tx.GasTipCap)
		return gasUsed, gasUsed * priorityFee, nil
	}

	if from.Balance < tx.Value {
		return 0, 0, ErrInsufficientBalance
	}

	// Self-transfer: balance nets to zero, but the nonce still advances.
	if tx.From == tx.To {
		from.Nonce++
		st.SetAccount(tx.From, from)
		return 0, 0, nil
	}

	to := st.GetAccount(tx.To)
	from.Balance -= tx.Value
	from.Nonce++
	to.Balance += tx.Value
	st.SetAccount(tx.From, from)
	st.SetAccount(tx.To, to)
	return 0, 0, nil
}

// applyTxsAt applies every tx in txs against st in order, at the given block
// height, under the given exchange mode.
//
// This is the single point Chain.AddBlock's re-derivation (applyBlockRewarded)
// and Chain.CandidateStateRoot both go through for their transaction loop --
// on purpose, so mining and validation can never again independently drift the
// way the bare-ApplyTx bug let them. In BatchAuction mode a session is opened
// once, before the loop, and every exchange transaction in the block is staged
// into it rather than settled on the spot; Finish runs once, after the loop,
// clearing everything that was staged at a single price. In Continuous mode
// (mode's zero value, so an unconfigured Chain behaves exactly as it always
// has) there is no session and nothing changes from before this function
// existed.
//
// baseFee (M9) is threaded through to every applyTxAtSession call. Returns
// the total gas used and total priority fee (tip) across every fee-priced
// (contract/EVM) transaction in txs -- Chain.AddBlock validates the former
// against Header.GasUsed/GasLimit; both AddBlock and CandidateStateRoot
// credit the latter to the block's Coinbase, on top of BlockReward.
func applyTxsAt(st state.StateDB, txs []core.Transaction, verifySig func(core.Transaction) bool, height uint64, mode exchange.Mode, baseFee uint64) (uint64, uint64, error) {
	var session *exchange.BatchSession
	if mode == exchange.BatchAuction {
		var senders []core.Address
		for i := range txs {
			if exchange.IsExchangeTx(txs[i]) {
				senders = append(senders, txs[i].From)
			}
		}
		if len(senders) > 0 {
			s, err := exchange.NewBatchSession(st, senders...)
			if err != nil {
				return 0, 0, err
			}
			session = s
		}
	}

	var totalGasUsed, totalTip uint64
	for i := range txs {
		gasUsed, tip, err := applyTxAtSession(st, txs[i], verifySig, height, uint32(i), session, baseFee)
		if err != nil {
			return 0, 0, err
		}
		totalGasUsed += gasUsed
		totalTip += tip
	}

	if session != nil {
		if _, err := session.Finish(st, height); err != nil {
			return 0, 0, err
		}
	}
	return totalGasUsed, totalTip, nil
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
// reserved up front at the sender's own GasFeeCap (M9's worst-case ceiling,
// replacing the old flat GasPrice constant) and, together with the nonce
// bump, always persists — even when execution reverts or runs out of gas.
// Only the effective price (baseFee + capped priority fee, never more than
// GasFeeCap -- see EffectiveGasPrice) is actually charged for gas consumed;
// the rest is refunded, exactly like the pre-M9 refund but at the real
// per-transaction price instead of a flat constant. The base-fee portion of
// what's charged is credited to nobody (burned, same mechanism M9 formalizes
// everywhere); the priority-fee portion is returned to the caller
// (applyTxAtSession) as tip, credited to Coinbase one layer up. On success the
// VM has already committed value transfer and storage writes to st; on
// failure those writes are reverted by the VM, leaving only the gas charge
// and nonce increment. The tx is only rejected outright (nonce NOT advanced)
// when the sender cannot afford the up-front gas, or when its fee fields are
// invalid (GasTipCap > GasFeeCap) or cannot cover this block's BaseFee.
func applyContractTx(st state.StateDB, tx core.Transaction, from state.Account, baseFee uint64) (uint64, error) {
	if err := ValidateFeeCapTip(tx.GasFeeCap, tx.GasTipCap); err != nil {
		return 0, err
	}
	gasReserve := tx.GasLimit * tx.GasFeeCap
	if tx.GasFeeCap != 0 && tx.GasLimit != 0 && gasReserve/tx.GasFeeCap != tx.GasLimit {
		return 0, ErrCantAffordGas // multiplication overflow: unaffordable by definition
	}
	if from.Balance < gasReserve {
		return 0, ErrCantAffordGas
	}
	effectivePrice, _, err := EffectiveGasPrice(baseFee, tx.GasFeeCap, tx.GasTipCap)
	if err != nil {
		return 0, err // ErrFeeCapBelowBaseFee
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

	// Refund whatever wasn't actually owed at the effective price (unused gas,
	// GasUsed == GasLimit on out-of-gas so that portion refunds 0; plus any
	// headroom between GasFeeCap and the real effective price). Never
	// underflows: effectivePrice <= GasFeeCap always (EffectiveGasPrice caps
	// priority fee at the feeCap-baseFee headroom), and receipt.GasUsed <=
	// tx.GasLimit always, so receipt.GasUsed*effectivePrice <= gasReserve.
	refund := gasReserve - receipt.GasUsed*effectivePrice
	acct := st.GetAccount(tx.From)
	acct.Balance += refund
	st.SetAccount(tx.From, acct)
	return receipt.GasUsed, nil
}

// ApplyBlock applies every transaction in b in order, then credits BlockReward
// to miner (coinbase). Application is atomic: if any transaction fails, st is
// left unchanged and the tx error is returned. Transactions are staged on an
// overlay and only flushed to st once the whole block succeeds.
//
// Exchange orders are matched continuously. Use ApplyBlockWithMode for
// BatchAuction.
func ApplyBlock(st state.StateDB, b core.Block, miner core.Address, verifySig func(core.Transaction) bool) error {
	return ApplyBlockWithMode(st, b, miner, verifySig, exchange.Continuous)
}

// ApplyBlockWithMode is ApplyBlock under an explicit exchange mode. Chain uses
// the equivalent path internally (applyBlockRewarded) with its own configured
// mode; this is the same machinery for a caller that wants block application
// without the rest of the consensus/mining apparatus -- a standalone
// simulation, or a test that wants to compare two orderings of one block
// without needing two different chains to do it.
func ApplyBlockWithMode(st state.StateDB, b core.Block, miner core.Address, verifySig func(core.Transaction) bool, mode exchange.Mode) error {
	ov := newOverlay(st)
	// b.Header.BaseFee is whatever the caller set on the block passed in
	// (zero-value 0 if unset, same as every other Header field this
	// standalone entrypoint doesn't independently validate -- see this
	// function's own doc comment: it's a simulation helper, not the real
	// consensus path, which is Chain.AddBlock).
	_, tip, err := applyTxsAt(ov, b.Txs, verifySig, b.Header.Height, mode, b.Header.BaseFee)
	if err != nil {
		return err
	}
	m := ov.GetAccount(miner)
	m.Balance += BlockReward + tip
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
