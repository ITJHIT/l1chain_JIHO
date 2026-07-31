package evm

import (
	"encoding/binary"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// Harness is a thin, deterministic wrapper around go-ethereum's EVM and state.
// It builds a real keccak/MPT-backed state (rawdb.NewMemoryDatabase -> triedb ->
// state.NewDatabase -> state.New) and exposes Deploy/Call helpers that drive
// vm.EVM.Create / vm.EVM.Call.
//
// go-ethereum v1.17.4 APIs used:
//   - state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
//     where NewDatabaseForTesting = NewDatabase(triedb.NewDatabase(
//     rawdb.NewMemoryDatabase(), nil), NewCodeDB(...)) — a genuine
//     Merkle-Patricia-Trie over an in-memory keccak-hashed key/value store.
//   - vm.NewEVM(blockCtx, statedb, chainConfig, vm.Config{}) + evm.SetTxContext(txCtx)
//   - evm.Create(caller, code, vm.NewGasBudget(gas,0), value)
//   - evm.Call(caller, addr, input, vm.NewGasBudget(gas,0), value)
//
// This build of go-ethereum carries the EIP-8037 split-gas model, so gas is a
// vm.GasBudget{regular,state}. We deliberately collapse both lanes here by
// funding the whole budget as regular gas via vm.NewGasBudget(gas, 0); state
// charges spill into regular gas per GasBudget.CanAfford. The scalar gasUsed
// returned by Deploy/Call is therefore capability-grade (proves execution and
// relative cost) — NOT consensus-grade metering. Honouring the split-gas lanes
// for block-hash-exact accounting is a long-term goal, not required for M4.
type Harness struct {
	State       *state.StateDB
	ChainConfig *params.ChainConfig
	BlockNumber *big.Int
	Time        uint64
	GasPrice    *uint256.Int

	// txCounter derives a distinct synthetic tx hash for each Deploy/Call, so
	// h.State.SetTxContext (see newTxContext) tags any LOG emitted with a
	// real, non-zero TxHash instead of state.StateDB's zero-value default.
	// This was previously unset entirely -- unobservable while the only
	// fixture (erc20_fixture.go) was hand-assembled bytecode that never
	// emits LOG0-4, but real solc output (oz_erc20_fixture.go) always emits
	// Transfer on mint/transfer.
	txCounter uint64
}

// NewHarness constructs a Harness over a fresh, empty real state.
func NewHarness() (*Harness, error) {
	sdb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		return nil, err
	}
	return &Harness{
		State:       sdb,
		ChainConfig: ModernChainConfig(),
		BlockNumber: big.NewInt(1),
		Time:        1,
		GasPrice:    new(uint256.Int),
	}, nil
}

// ModernChainConfig returns a chain config with every fork through Cancun
// activated at height/time 0 so PUSH0, SHA3, and the full modern opcode set are
// available to solc-style bytecode regardless of its compilation target.
func ModernChainConfig() *params.ChainConfig {
	z := new(big.Int)
	t0 := uint64(0)
	return &params.ChainConfig{
		ChainID:                 big.NewInt(1),
		HomesteadBlock:          z,
		DAOForkBlock:            z,
		DAOForkSupport:          false,
		EIP150Block:             z,
		EIP155Block:             z,
		EIP158Block:             z,
		ByzantiumBlock:          z,
		ConstantinopleBlock:     z,
		PetersburgBlock:         z,
		IstanbulBlock:           z,
		MuirGlacierBlock:        z,
		BerlinBlock:             z,
		LondonBlock:             z,
		TerminalTotalDifficulty: big.NewInt(0),
		ShanghaiTime:            &t0,
		CancunTime:              &t0,
	}
}

// Fund credits an account with wei, creating it if necessary.
func (h *Harness) Fund(addr common.Address, wei *uint256.Int) {
	if !h.State.Exist(addr) {
		h.State.CreateAccount(addr)
	}
	h.State.AddBalance(addr, wei, tracing.BalanceChangeUnspecified)
}

func (h *Harness) newEVM(origin common.Address) *vm.EVM {
	blockCtx := vm.BlockContext{
		CanTransfer:      core.CanTransfer,
		Transfer:         core.Transfer,
		GetHash:          func(uint64) common.Hash { return common.Hash{} },
		Coinbase:         common.Address{},
		BlockNumber:      new(big.Int).Set(h.BlockNumber),
		Time:             h.Time,
		Difficulty:       new(big.Int),
		GasLimit:         uint64(1) << 63,
		BaseFee:          new(big.Int),
		BlobBaseFee:      new(big.Int),
		CostPerStateByte: params.CostPerStateByte,
		// vm.NewEVM derives its own internal chainRules as
		// h.ChainConfig.Rules(num, blockCtx.Random != nil, time) -- every
		// post-Merge fork field params.ChainConfig.Rules computes
		// (IsShanghai/IsCancun/...) is gated behind isMerge
		// (params/config.go: "IsCancun: isMerge && c.IsCancun(...)"), not
		// derived from CancunTime/ShanghaiTime alone. Without a non-nil
		// Random here, ModernChainConfig's own doc comment ("every fork
		// through Cancun activated... so PUSH0 ... are available") was
		// FALSE as actually wired: the jump-table switch in vm.NewEVM fell
		// through past every isMerge-gated case straight to the pre-Shanghai
		// London instruction set. Found while adding evm/adapter's real
		// opSelfdestruct6780 (EIP-6780, Cancun-only) coverage -- that test
		// hit the exact same gap and traced it to this line. Silently
		// harmless so far only because solc 0.8.24 defaults to targeting
		// evmVersion "paris" (confirmed via contracts/artifacts/build-info),
		// which needs nothing London can't already execute -- see
		// TestModernChainConfigReachesShanghaiAndCancun below for the
		// regression proof this doesn't silently regress again.
		Random: &common.Hash{},
	}
	e := vm.NewEVM(blockCtx, h.State, h.ChainConfig, vm.Config{})
	e.SetTxContext(vm.TxContext{Origin: origin, GasPrice: h.GasPrice})
	return e
}

// rules mirrors newEVM's own isMerge signal (see its Random field comment)
// for the separate ChainConfig.Rules(...) call Prepare/ActivePrecompiles
// need -- vm.NewEVM's internal rules computation and this one must agree,
// or Prepare would warm a different precompile set than the one actually
// active in the interpreter.
func (h *Harness) rules() params.Rules {
	return h.ChainConfig.Rules(h.BlockNumber, true, h.Time)
}

// newTxContext derives a distinct synthetic tx hash from h.txCounter and
// tags h.State with it via SetTxContext, so any LOG opcode this call
// executes (core/vm/instructions.go's makeLog -> StateDB.AddLog) records a
// real, non-zero TxHash rather than the zero-value default that resulted
// from SetTxContext never being called at all before this. index/
// blockAccessIndex are both 0: Harness has no notion of "multiple txs in one
// block" to assign a real position within, so 0 is the only meaningful value
// -- what matters for the fix is TxHash, not TxIndex.
func (h *Harness) newTxContext() common.Hash {
	h.txCounter++
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], h.txCounter)
	txHash := crypto.Keccak256Hash(buf[:])
	h.State.SetTxContext(txHash, 0, 0)
	return txHash
}

// Deploy runs contract-creation bytecode from `from` and returns the deployed
// contract address plus scalar gas consumed.
func (h *Harness) Deploy(from common.Address, code []byte, gas uint64) (common.Address, uint64, error) {
	h.newTxContext()
	rules := h.rules()
	h.State.Prepare(rules, from, common.Address{}, nil, vm.ActivePrecompiles(rules), nil)
	e := h.newEVM(from)
	budget := vm.NewGasBudget(gas, 0)
	_, addr, result, err := e.Create(from, code, budget, new(uint256.Int))
	return addr, result.Used(budget), err
}

// Call executes a message call into `to` with the given ABI-encoded input and
// returns the raw return data plus scalar gas consumed.
func (h *Harness) Call(from, to common.Address, input []byte, gas uint64) ([]byte, uint64, error) {
	h.newTxContext()
	rules := h.rules()
	h.State.Prepare(rules, from, common.Address{}, &to, vm.ActivePrecompiles(rules), nil)
	e := h.newEVM(from)
	budget := vm.NewGasBudget(gas, 0)
	ret, result, err := e.Call(from, to, input, budget, new(uint256.Int))
	return ret, result.Used(budget), err
}

// Logs returns every log emitted by this Harness's State so far (across all
// Deploy/Call invocations), each tagged with the real per-call TxHash
// newTxContext derived -- see state.StateDB.Logs.
func (h *Harness) Logs() []*types.Log {
	return h.State.Logs()
}

// Code returns the runtime bytecode stored at addr.
func (h *Harness) Code(addr common.Address) []byte {
	return h.State.GetCode(addr)
}

// StateRoot finalises pending state and returns the MPT root hash. Useful for
// asserting determinism across independent runs.
func (h *Harness) StateRoot() common.Hash {
	return h.State.IntermediateRoot(true)
}
