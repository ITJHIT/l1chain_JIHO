package adapter

import (
	"math/big"
	"testing"

	"l1chain/state"

	gethcore "github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/common"
	gethstate "github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/types/bal"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// fullStateDB is a TEST-ONLY wrapper satisfying the complete vm.StateDB
// interface early, by embedding this package's real, in-progress *StateDB
// (promoting every method already implemented -- SelfDestruct,
// IsNewContract, AddBalance, SubBalance, Snapshot/RevertToSnapshot,
// transient storage/access lists/Prepare (PR3), etc.) and stubbing only
// what PR4 hasn't built yet: GetStateAndCommittedState/GetState/SetState
// (contract storage itself -- not needed by anything wired through this
// wrapper so far) and logs/witness/Finalise/SetTxContext.
//
// This exists so tests in this package can drive a real vm.EVM through
// real, unexported opcodes now -- proving each new piece is wire-compatible
// with the actual opcode call patterns, rather than discovering a mismatch
// only once *StateDB reaches full conformance in PR4 (at which point this
// wrapper is deleted and these tests can drive *StateDB directly).
type fullStateDB struct {
	*StateDB
}

func (fullStateDB) GetStateAndCommittedState(common.Address, common.Hash) (common.Hash, common.Hash) {
	return common.Hash{}, common.Hash{}
}
func (fullStateDB) GetState(common.Address, common.Hash) common.Hash { return common.Hash{} }
func (fullStateDB) SetState(common.Address, common.Hash, common.Hash) common.Hash {
	return common.Hash{}
}
func (fullStateDB) AddLog(*types.Log)                              {}
func (fullStateDB) LogsForBurnAccounts() []*types.Log               { return nil }
func (fullStateDB) AddPreimage(common.Hash, []byte)                 {}
func (fullStateDB) Witness() *stateless.Witness                     { return nil }
func (fullStateDB) AccessEvents() *gethstate.AccessEvents            { return nil }
func (fullStateDB) Finalise(bool) *bal.ConstructionBlockAccessList { return nil }
func (fullStateDB) SetTxContext(common.Hash, int, uint32)           {}

var _ vm.StateDB = fullStateDB{}

func newTestEVM(sdb vm.StateDB, cfg *params.ChainConfig) *vm.EVM {
	blockCtx := vm.BlockContext{
		CanTransfer:      gethcore.CanTransfer,
		Transfer:         gethcore.Transfer,
		GetHash:          func(uint64) common.Hash { return common.Hash{} },
		Coinbase:         common.Address{},
		BlockNumber:      big.NewInt(1),
		Time:             1,
		Difficulty:       new(big.Int),
		GasLimit:         uint64(1) << 63,
		BaseFee:          new(big.Int),
		BlobBaseFee:      new(big.Int),
		CostPerStateByte: params.CostPerStateByte,
		// vm.NewEVM derives its own internal chainRules as
		// cfg.Rules(num, blockCtx.Random != nil, time) -- every post-Merge
		// fork field (IsShanghai/IsCancun/...) in params.ChainConfig.Rules
		// is gated behind isMerge (params/config.go: "IsCancun: isMerge &&
		// c.IsCancun(...)"), so CancunTime alone is NOT sufficient; without
		// a non-nil Random here, EIP-6780 (enable6780, wired at the Cancun
		// instruction set) never actually applies and SELFDESTRUCT silently
		// falls back to the old unconditional pre-Cancun opcode. Only the
		// non-nilness is load-bearing for isMerge; the value itself is only
		// consumed by the PREVRANDAO opcode, unused here.
		Random: &common.Hash{},
	}
	return vm.NewEVM(blockCtx, sdb, cfg, vm.Config{})
}

// rulesFor mirrors newTestEVM's own isMerge signal (see its Random field
// comment) for the SEPARATE cfg.Rules(...) call Prepare/ActivePrecompiles
// need -- vm.NewEVM's internal rules computation and this one must agree,
// or Prepare would warm a different precompile set than the one actually
// active in the interpreter.
func rulesFor(cfg *params.ChainConfig) params.Rules {
	return cfg.Rules(big.NewInt(1), true, 1)
}

// modernChainConfig mirrors evm.ModernChainConfig() (every fork through
// Cancun active at height/time 0) -- duplicated locally rather than
// importing l1chain/evm, since evm/adapter is meant to stay independently
// buildable of the sibling evm package it will eventually supersede.
func modernChainConfig() *params.ChainConfig {
	z := new(big.Int)
	t0 := uint64(0)
	return &params.ChainConfig{
		ChainID:                 big.NewInt(1),
		HomesteadBlock:          z,
		DAOForkBlock:            z,
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

func deployVia(t *testing.T, sdb vm.StateDB, cfg *params.ChainConfig, from common.Address, code []byte, value *uint256.Int, gas uint64) common.Address {
	t.Helper()
	rules := rulesFor(cfg)
	sdb.Prepare(rules, from, common.Address{}, nil, vm.ActivePrecompiles(rules), nil)
	e := newTestEVM(sdb, cfg)
	e.SetTxContext(vm.TxContext{Origin: from, GasPrice: new(uint256.Int)})
	_, addr, _, err := e.Create(from, code, vm.NewGasBudget(gas, 0), value)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return addr
}

// callVia returns the scalar gas used, in the same capability-grade (not
// consensus-grade) sense evm.Harness.Call already documents -- useful here
// specifically to compare relative costs (e.g. cold vs. warm SLOAD), not to
// assert an exact absolute number.
func callVia(t *testing.T, sdb vm.StateDB, cfg *params.ChainConfig, from, to common.Address, gas uint64) uint64 {
	t.Helper()
	rules := rulesFor(cfg)
	sdb.Prepare(rules, from, common.Address{}, &to, vm.ActivePrecompiles(rules), nil)
	e := newTestEVM(sdb, cfg)
	e.SetTxContext(vm.TxContext{Origin: from, GasPrice: new(uint256.Int)})
	budget := vm.NewGasBudget(gas, 0)
	_, result, err := e.Call(from, to, nil, budget, new(uint256.Int))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	return result.Used(budget)
}

// selfdestructBytecode is PUSH20 <beneficiary> SELFDESTRUCT -- 22 bytes.
func selfdestructBytecode(beneficiary common.Address) []byte {
	code := make([]byte, 0, 22)
	code = append(code, 0x73) // PUSH20
	code = append(code, beneficiary[:]...)
	code = append(code, 0xff) // SELFDESTRUCT
	return code
}

// deployingInitCode wraps runtime in the standard "CODECOPY the trailing
// bytes into memory, then RETURN them" constructor pattern, so runtime
// becomes the deployed contract's own code instead of executing at deploy
// time.
func deployingInitCode(runtime []byte) []byte {
	n := byte(len(runtime))
	offset := byte(12) // length of the prefix itself, below
	prefix := []byte{
		0x60, n, // PUSH1 n        (length, for CODECOPY -- pushed first = deepest)
		0x60, offset, // PUSH1 offset   (code offset, for CODECOPY)
		0x60, 0x00, // PUSH1 0        (dest offset, for CODECOPY -- pushed last = top)
		0x39,       // CODECOPY
		0x60, n,    // PUSH1 n        (size, for RETURN -- pushed first = deepest)
		0x60, 0x00, // PUSH1 0        (offset, for RETURN -- pushed last = top)
		0xf3, // RETURN
	}
	return append(prefix, runtime...)
}

const (
	sdDeployGas = uint64(3_000_000)
	sdCallGas   = uint64(1_000_000)
)

// TestSelfDestruct6780SameTxActuallyDestructs drives real bytecode through
// the real, unexported opSelfdestruct6780 opcode (core/vm/instructions.go):
// a constructor that self-destructs during its OWN creation transaction.
// IsNewContract(this) is true (CreateContract was called on this very
// StateDB instance moments earlier, by vm.EVM.Create itself), so EIP-6780's
// "new contract" branch applies -- the real opcode calls AddBalance/
// SubBalance/SelfDestruct, actually marking the contract destroyed.
func TestSelfDestruct6780SameTxActuallyDestructs(t *testing.T) {
	cfg := modernChainConfig()
	sdb := fullStateDB{New(state.NewMemStateDB())}
	deployer := common.HexToAddress("0xd000000000000000000000000000000000000d")
	beneficiary := common.HexToAddress("0xbe0000000000000000000000000000000000be")

	sdb.AddBalance(deployer, u256(1_000_000), 0)

	initCode := selfdestructBytecode(beneficiary) // constructor self-destructs immediately
	contractAddr := deployVia(t, sdb, cfg, deployer, initCode, u256(500), sdDeployGas)

	if !sdb.HasSelfDestructed(contractAddr) {
		t.Fatal("HasSelfDestructed = false, want true (constructor self-destructed a newly created contract)")
	}
	if got := sdb.GetBalance(contractAddr); !got.IsZero() {
		t.Fatalf("contract balance after same-tx self-destruct = %s, want 0", got)
	}
	if got := sdb.GetBalance(beneficiary); got.Cmp(u256(500)) != 0 {
		t.Fatalf("beneficiary balance after same-tx self-destruct = %s, want 500", got)
	}
}

// TestSelfDestruct6780PriorTxOnlyTransfersBalance drives real bytecode
// through the same real opcode, but for a contract deployed by an EARLIER,
// independent StateDB instance -- modeling a contract that already existed
// before this transaction's StateDB was constructed. IsNewContract(this) is
// false on the SECOND instance (it never called CreateContract for this
// address itself), so EIP-6780's "not new" branch applies: the real opcode
// only transfers the balance and never calls SelfDestruct -- the contract,
// its code, and its HasSelfDestructed flag must all survive.
func TestSelfDestruct6780PriorTxOnlyTransfersBalance(t *testing.T) {
	cfg := modernChainConfig()
	base := state.NewMemStateDB() // shared underlying persisted state
	deployer := common.HexToAddress("0xd000000000000000000000000000000000000d")
	beneficiary := common.HexToAddress("0xbe0000000000000000000000000000000000be")
	caller := common.HexToAddress("0xca000000000000000000000000000000000ca0")

	// First "transaction": deploy a contract whose RUNTIME code (not its
	// constructor) self-destructs when called.
	deploySDB := fullStateDB{New(base)}
	deploySDB.AddBalance(deployer, u256(1_000_000), 0)
	runtime := selfdestructBytecode(beneficiary)
	contractAddr := deployVia(t, deploySDB, cfg, deployer, deployingInitCode(runtime), new(uint256.Int), sdDeployGas)
	if got := deploySDB.GetCode(contractAddr); len(got) == 0 {
		t.Fatalf("deployed contract has no runtime code")
	}
	// Fund the now-deployed contract directly, simulating value it received
	// in some later, separate transaction before the self-destructing call.
	deploySDB.AddBalance(contractAddr, u256(700), 0)

	// A SECOND, independent StateDB instance over the SAME underlying
	// state -- this is the "prior tx" shape: it never saw contractAddr's
	// creation, so its own IsNewContract(contractAddr) is false by
	// construction, exactly like a fresh per-tx adapter instance would be
	// in production once a contract survives past the transaction that
	// created it.
	callSDB := fullStateDB{New(base)}
	if callSDB.IsNewContract(contractAddr) {
		t.Fatal("IsNewContract on a fresh StateDB instance = true, want false (this instance never created it)")
	}

	callVia(t, callSDB, cfg, caller, contractAddr, sdCallGas)

	if callSDB.HasSelfDestructed(contractAddr) {
		t.Fatal("HasSelfDestructed = true, want false (EIP-6780: not created this tx, so no actual destruct)")
	}
	if got := callSDB.GetCode(contractAddr); len(got) == 0 {
		t.Fatal("contract code is gone after a prior-tx self-destruct call, want it to survive (EIP-6780)")
	}
	if got := callSDB.GetBalance(contractAddr); !got.IsZero() {
		t.Fatalf("contract balance after prior-tx self-destruct = %s, want 0 (transferred out)", got)
	}
	if got := callSDB.GetBalance(beneficiary); got.Cmp(u256(700)) != 0 {
		t.Fatalf("beneficiary balance after prior-tx self-destruct = %s, want 700", got)
	}
}
