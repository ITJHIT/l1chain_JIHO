package chain

import (
	"log"
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
// The sender's OWN nonce bump is delayed until after Create/Call returns
// -- see the comment at that point below for why; applyContractTx's own
// "bump immediately, before running anything" order does NOT transfer
// here unchanged.
//
// KNOWN LIMITATION: block.timestamp/block.coinbase/block.difficulty are
// fixed placeholders, not real header fields -- only block.number
// (height) is real. Threading genuine header context through would touch
// Chain.AddBlock/CandidateStateRoot's own call sites, deliberately kept
// out of this already-large wiring change; a contract that reads those
// opcodes won't get meaningful values yet.
func applyEVMTx(st state.StateDB, tx core.Transaction, from state.Account, height uint64) error {
	gasReserve := tx.GasLimit * GasPrice
	if GasPrice != 0 && tx.GasLimit != 0 && gasReserve/GasPrice != tx.GasLimit {
		return ErrCantAffordGas // multiplication overflow: unaffordable by definition
	}
	if from.Balance < gasReserve {
		return ErrCantAffordGas
	}

	// Reserve gas up front; persists regardless of the execution outcome,
	// same as applyContractTx. The nonce bump is NOT here -- see below.
	from.Balance -= gasReserve
	st.SetAccount(tx.From, from)
	log.Printf("DIAG-WIRE step=after-gas-reserve sender.Nonce(via st)=%d", st.GetAccount(tx.From).Nonce)

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
		BaseFee:          new(big.Int),
		BlobBaseFee:      new(big.Int),
		CostPerStateByte: params.CostPerStateByte,
		// Same isMerge signal as Rules above, via vm.NewEVM's own internal
		// chainRules := chainConfig.Rules(num, blockCtx.Random != nil, time)
		// -- see evm/adapter's own selfdestruct_test.go for the full trace
		// of why a nil Random silently disables EIP-6780 and friends.
		Random: &common.Hash{},
	}, sdb, cfg, gethvm.Config{})
	e.SetTxContext(gethvm.TxContext{Origin: fromAddr, GasPrice: new(uint256.Int)})

	// preCall is taken, and Create/Call run, BEFORE the sender's own nonce
	// is bumped. This is load-bearing for deployment specifically:
	// vm.EVM.Create derives the new contract's address as
	// crypto.CreateAddress(caller, evm.StateDB.GetNonce(caller)) --
	// reading the caller's CURRENT nonce through this same adapter at the
	// moment it runs. Create never touches the CALLER's own nonce itself
	// (confirmed directly against go-ethereum's source: the only SetNonce
	// call in the whole create() path targets the newly created contract's
	// own nonce, per EIP-161's "new contracts start at 1", not the
	// deployer's). Bumping the sender's nonce before this point would
	// derive every contract one nonce too high, breaking the well-established
	// "a fresh account's first-ever deployment (nonce 0) lands at
	// CreateAddress(sender, 0)" convention -- caught by hand-deriving
	// expected addresses in this package's own cross-path determinism test
	// before it was ever wired into chain/transition.go for real.
	log.Printf("DIAG-WIRE step=before-call sdb.GetNonce(fromAddr)=%d st.GetAccount(tx.From).Nonce=%d", sdb.GetNonce(fromAddr), st.GetAccount(tx.From).Nonce)
	preCall := sdb.Snapshot()
	budget := gethvm.NewGasBudget(tx.GasLimit, 0)
	var gasUsed uint64
	if isDeploy {
		_, deployedAddr, result, err := e.Create(fromAddr, tx.Data, budget, value)
		gasUsed = result.Used(budget)
		log.Printf("DIAG-WIRE step=after-create deployedAddr=%s fromAddr=%s equal=%v err=%v", deployedAddr, fromAddr, deployedAddr == fromAddr, err)
	} else {
		_, result, err := e.Call(fromAddr, *toAddr, tx.Data, budget, value)
		gasUsed = result.Used(budget)
		log.Printf("DIAG-WIRE step=after-call err=%v", err)
	}
	log.Printf("DIAG-WIRE step=after-call sdb.GetNonce(fromAddr)=%d st.GetAccount(tx.From).Nonce=%d sdb.Err()=%v", sdb.GetNonce(fromAddr), st.GetAccount(tx.From).Nonce, sdb.Err())

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
	log.Printf("DIAG-WIRE step=after-finalise st.GetAccount(tx.From).Nonce=%d", st.GetAccount(tx.From).Nonce)

	// Bump the sender's nonce now -- after Create/Call has already derived
	// any contract address it needed to (see the comment above), but still
	// unconditional, exactly like applyContractTx's own up-front bump: a
	// reverted or out-of-gas EVM execution still consumes the nonce.
	// Re-reads fresh from st (not the stale `from` captured above) for the
	// same reason applyContractTx does: execution may have changed this
	// same account's balance (e.g. it received value back from a nested
	// call), and this must not clobber that with a stale copy.
	refund := (tx.GasLimit - gasUsed) * GasPrice
	acct := st.GetAccount(tx.From)
	acct.Nonce++
	acct.Balance += refund
	st.SetAccount(tx.From, acct)
	return nil
}
