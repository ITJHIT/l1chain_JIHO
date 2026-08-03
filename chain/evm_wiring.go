package chain

import (
	"math/big"

	"l1chain/core"
	"l1chain/evm"
	"l1chain/evm/adapter"
	"l1chain/state"

	"github.com/ethereum/go-ethereum/common"
	gethcore "github.com/ethereum/go-ethereum/core"
	gethvm "github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// isEVMTx reports whether tx must be routed through the real embedded EVM
// (evm/adapter) instead of M3's StackVM: either a deployment (To ==
// evm.DeployAddress) or a call to an address already holding EVM-tagged
// code. evm.IsTaggedCode is checked directly against st -- never through
// the adapter, which transparently strips the tag for its own callers
// (evm/adapter.StateDB.GetCode's own doc comment) -- because this check
// itself decides whether to construct an adapter at all.
//
// Must run BEFORE isContractTx: a deployed EVM contract "has code" by
// isContractTx's own definition too (len(st.GetCode(tx.To)) > 0), and
// would otherwise be misrouted to the StackVM.
func isEVMTx(st state.StateDB, tx core.Transaction) bool {
	if tx.To == evm.DeployAddress {
		return true
	}
	return evm.IsTaggedCode(st.GetCode(tx.To))
}

func toGethAddress(a core.Address) common.Address {
	var out common.Address
	copy(out[:], a[:])
	return out
}

func toGethValue(v uint64) *uint256.Int {
	return new(uint256.Int).SetUint64(v)
}

// applyEVMTx runs an EVM deployment or call through evm/adapter, reusing
// applyContractTx's exact gas-reserve/refund pattern (see its own doc
// comment) against the adapter's scalar GasUsed: gas is reserved up front,
// persisting regardless of execution outcome; a REVERTED or out-of-gas
// execution is not itself a block-validation error here any more than it
// is for M3 (the embedded EVM already reverts its own snapshot on failure,
// matching vm.StackVM.Execute's own "VM reverts its own writes" contract)
// -- only the up-front affordability check can reject the transaction
// outright.
//
// The sender's OWN nonce is set to preNonce+1 (an absolute assignment, not
// a relative bump) after Create/Call returns -- see the comment at that
// point below for why an absolute set is required here, unlike
// applyContractTx's own unconditional relative "bump immediately, before
// running anything" pattern, which does NOT transfer here unchanged.
//
// KNOWN LIMITATION: block.timestamp/block.coinbase/block.difficulty are
// fixed placeholders, not real header fields -- only block.number
// (height) is real. Threading genuine header context through would touch
// Chain.AddBlock/CandidateStateRoot's own call sites, deliberately kept
// out of this already-large wiring change; a contract that reads those
// opcodes won't get meaningful values yet. block.basefee (the BASEFEE
// opcode, 0x48) IS real as of M9 -- see BlockContext.BaseFee below, set
// from this same function's own baseFee parameter, the identical value
// Chain.AddBlock independently re-derives and validates for this block.
//
// baseFee (M9) replaces the old flat GasPrice constant: gas is reserved at
// the sender's own GasFeeCap (worst case), but only the effective price
// (baseFee + capped priority fee) is actually charged for gas consumed,
// mirroring applyContractTx's own M9 fee-accounting exactly (see its doc
// comment for the burn/tip/refund split).
func applyEVMTx(st state.StateDB, tx core.Transaction, from state.Account, height uint64, baseFee uint64) (uint64, error) {
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

	// preNonce is the sender's nonce as of immediately before Create/Call
	// runs. The final nonce is set to preNonce+1 by absolute assignment
	// (not `acct.Nonce++`) -- see the comment below, at the assignment
	// itself, for why relative increment double-counts.
	preNonce := from.Nonce

	// Reserve gas up front; persists regardless of the execution outcome,
	// same as applyContractTx. The nonce bump is NOT here -- see below.
	from.Balance -= gasReserve
	st.SetAccount(tx.From, from)

	sdb := adapter.New(st)
	cfg := evm.ModernChainConfig()
	// isMerge=true is load-bearing: without it, every post-Merge fork field
	// params.ChainConfig.Rules computes (IsShanghai/IsCancun/...) stays
	// false regardless of ShanghaiTime/CancunTime, exactly the bug fixed in
	// evm.Harness (evm/runtime.go) -- EIP-6780 self-destruct, transient
	// storage, and PUSH0 would silently fall back to pre-Shanghai semantics
	// here too if this were ever dropped.
	rules := cfg.Rules(new(big.Int).SetUint64(height), true, 1)

	fromAddr := toGethAddress(tx.From)
	value := toGethValue(tx.Value)
	isDeploy := tx.To == evm.DeployAddress

	var toAddr *common.Address
	if !isDeploy {
		a := toGethAddress(tx.To)
		toAddr = &a
	}
	sdb.Prepare(rules, fromAddr, common.Address{}, toAddr, gethvm.ActivePrecompiles(rules), nil)

	e := gethvm.NewEVM(gethvm.BlockContext{
		CanTransfer:      gethcore.CanTransfer,
		Transfer:         gethcore.Transfer,
		GetHash:          func(uint64) common.Hash { return common.Hash{} },
		Coinbase:         common.Address{},
		BlockNumber:      new(big.Int).SetUint64(height),
		Time:             1,
		Difficulty:       new(big.Int),
		GasLimit:         uint64(1) << 63,
		BaseFee:          new(big.Int).SetUint64(baseFee),
		BlobBaseFee:      new(big.Int),
		CostPerStateByte: params.CostPerStateByte,
		// Same isMerge signal as Rules above, via vm.NewEVM's own internal
		// chainRules := chainConfig.Rules(num, blockCtx.Random != nil, time)
		// -- see evm/adapter's own selfdestruct_test.go for the full trace
		// of why a nil Random silently disables EIP-6780 and friends.
		Random: &common.Hash{},
	}, sdb, cfg, gethvm.Config{})
	e.SetTxContext(gethvm.TxContext{Origin: fromAddr, GasPrice: new(uint256.Int)})

	// preCall is taken, and Create/Call run, BEFORE the sender's own final
	// nonce is set. This is load-bearing for deployment specifically:
	// vm.EVM.Create derives the new contract's address as
	// crypto.CreateAddress(caller, evm.StateDB.GetNonce(caller)) --
	// reading the caller's CURRENT (pre-bump) nonce through this same
	// adapter at the moment it runs.
	//
	// CORRECTION (found via CI diagnostic, not assumption): vm.EVM.Create
	// (core/vm/evm.go's create(), line ~511) DOES bump the caller's own
	// nonce internally -- unconditionally, right after validation passes,
	// via evm.StateDB.SetNonce(caller, nonce+1,
	// tracing.NonceChangeContractCreator) -- BEFORE the init code even
	// runs. This mirrors go-ethereum's own core/state_transition.go
	// exactly: TransitionDb bumps the sender's nonce explicitly itself
	// ONLY on the Call branch (`st.state.SetNonce(msg.From, ...)`
	// immediately before `st.evm.Call`), and deliberately skips that bump
	// on the Create branch, since st.evm.Create already does it. So Create
	// touches the caller's nonce; Call does not.
	//
	// Bumping the sender's nonce a second time, unconditionally, after
	// Create returns -- this code's original shape -- double-counted it:
	// create() took it 0->1 internally, then a second `acct.Nonce++` here
	// took it 1->2, for a single deploy transaction. Root-caused from a
	// real CI failure (TestMPTDeterministicWithEVMWorkload /
	// TestEVMAdapterCandidateStateRootAgreesWithFreshChainAddBlock both
	// observed sender.Nonce=2 after one deploy tx, while step-by-step
	// diagnostic logging showed create()'s own internal bump already
	// landing on exactly 1 before this function's own bump ran).
	//
	// Fix: below, the final nonce is SET to preNonce+1 by absolute
	// assignment, not relatively incremented -- correct whether or not
	// Create already bumped it (Call never does; Create always does, and
	// that bump is itself undone by RevertToSnapshot(preCall) on the
	// sdb.Err() overflow path below, so an absolute set is required there
	// too, not just for the ordinary success path).
	preCall := sdb.Snapshot()
	budget := gethvm.NewGasBudget(tx.GasLimit, 0)
	var gasUsed uint64
	if isDeploy {
		_, _, result, _ := e.Create(fromAddr, tx.Data, budget, value)
		gasUsed = result.Used(budget)
	} else {
		_, result, _ := e.Call(fromAddr, *toAddr, tx.Data, budget, value)
		gasUsed = result.Used(budget)
	}

	// evm/adapter.StateDB.Err's own doc comment: a balance overflowing
	// l1chain's uint64 ceiling doesn't itself raise an opcode-level EVM
	// error (AddBalance just silently declines the write and returns the
	// unchanged value), so the embedded interpreter would NOT have reverted
	// on its own here -- this must be handled explicitly, exactly like an
	// out-of-gas revert: discard every EVM-side change from this call
	// (gasUsed is forced to the full limit below, so no refund is issued
	// either); the up-front gas reservation and the nonce bump still to
	// come both persist regardless.
	if sdb.Err() != nil {
		sdb.RevertToSnapshot(preCall)
		gasUsed = tx.GasLimit
	}

	// Finalise deletes any address that ended this call self-destructed or
	// (EIP-161, active from genesis in this chain config -- rules.IsEIP158)
	// empty -- without this, a same-tx SELFDESTRUCT would mark the account
	// but never actually remove it from the trie, since SelfDestruct itself
	// only sets a flag (evm/adapter.StateDB.SelfDestruct's own doc
	// comment); Finalise is the only thing that acts on it. Safe to call
	// even after the Err()-triggered revert above: RevertToSnapshot(preCall)
	// already rolled back every touched/selfDestructed entry this call
	// made, so Finalise here is then an empty no-op.
	sdb.Finalise(rules.IsEIP158)

	// Set the sender's nonce to preNonce+1 now -- after Create/Call has
	// already derived any contract address it needed to (see the comment
	// above). An ABSOLUTE assignment, not a relative `acct.Nonce++`: Create
	// already moved the nonce to preNonce+1 internally (see above), and
	// that move survives here unless sdb.Err() triggered a
	// RevertToSnapshot(preCall), which undoes it back to preNonce -- either
	// way, setting to preNonce+1 directly lands on the correct value
	// without caring which path was taken. Call never touches the nonce at
	// all, so the same assignment is what performs its bump. Every path
	// (success, EVM-level revert, sdb.Err() overflow) consumes exactly one
	// nonce, matching applyContractTx's own "reverted or out-of-gas still
	// consumes the nonce" rule.
	// Re-reads fresh from st (not the stale `from` captured above) for the
	// same reason applyContractTx does: execution may have changed this
	// same account's balance (e.g. it received value back from a nested
	// call), and this must not clobber that with a stale copy.
	refund := gasReserve - gasUsed*effectivePrice
	acct := st.GetAccount(tx.From)
	acct.Nonce = preNonce + 1
	acct.Balance += refund
	st.SetAccount(tx.From, acct)
	return gasUsed, nil
}
