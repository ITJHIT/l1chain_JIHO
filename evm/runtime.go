package evm

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
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
	}
	e := vm.NewEVM(blockCtx, h.State, h.ChainConfig, vm.Config{})
	e.SetTxContext(vm.TxContext{Origin: origin, GasPrice: h.GasPrice})
	return e
}

func (h *Harness) rules() params.Rules {
	return h.ChainConfig.Rules(h.BlockNumber, false, h.Time)
}

// Deploy runs contract-creation bytecode from `from` and returns the deployed
// contract address plus scalar gas consumed.
func (h *Harness) Deploy(from common.Address, code []byte, gas uint64) (common.Address, uint64, error) {
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
	rules := h.rules()
	h.State.Prepare(rules, from, common.Address{}, &to, vm.ActivePrecompiles(rules), nil)
	e := h.newEVM(from)
	budget := vm.NewGasBudget(gas, 0)
	ret, result, err := e.Call(from, to, input, budget, new(uint256.Int))
	return ret, result.Used(budget), err
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
