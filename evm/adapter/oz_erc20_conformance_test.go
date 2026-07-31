package adapter

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"l1chain/evm"
	"l1chain/state"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

const (
	ozDeployGas = uint64(3_000_000)
	ozCallGas   = uint64(1_000_000)
)

var (
	ozDeployer  = common.HexToAddress("0x0d000000000000000000000000000000000d00")
	ozRecipient = common.HexToAddress("0x0d0000000000000000000000000000000000ec")
)

// callViaData is callVia's sibling, returning the raw call output instead
// of gas used -- needed here to read back ABI-encoded balanceOf() results.
func callViaData(t *testing.T, sdb vm.StateDB, cfg *params.ChainConfig, from, to common.Address, input []byte, gas uint64) []byte {
	t.Helper()
	rules := rulesFor(cfg)
	sdb.Prepare(rules, from, common.Address{}, &to, vm.ActivePrecompiles(rules), nil)
	e := newTestEVM(sdb, cfg)
	e.SetTxContext(vm.TxContext{Origin: from, GasPrice: new(uint256.Int)})
	ret, _, err := e.Call(from, to, input, vm.NewGasBudget(gas, 0), new(uint256.Int))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	return ret
}

func abiBalanceOf(t *testing.T, parsedABI abi.ABI, addr common.Address, call func([]byte) []byte) *big.Int {
	t.Helper()
	in, err := parsedABI.Pack("balanceOf", addr)
	if err != nil {
		t.Fatalf("pack balanceOf: %v", err)
	}
	vals, err := parsedABI.Unpack("balanceOf", call(in))
	if err != nil {
		t.Fatalf("unpack balanceOf: %v", err)
	}
	return vals[0].(*big.Int)
}

// TestOZERC20ProducesIdenticalObservableResultsViaAdapterAndHarness is PR4's
// own load-bearing proof, exactly as the plan specifies: the real
// solc-compiled OZ ERC20 fixture (evm.L1TokenMetaData, M6) deployed/
// transferred through this package's *StateDB adapter must produce the SAME
// ABI-observable results -- balances, and the real Transfer event's
// signature -- as the identical sequence run through evm.Harness's own real
// go-ethereum *state.StateDB. This is the proof that *StateDB is a genuine,
// swappable alternative backend for the embedded EVM, not merely
// internally self-consistent in isolation.
//
// ERC20 balances live in CONTRACT STORAGE (a Solidity mapping, via SSTORE/
// SLOAD), never in native account balance -- so this exercises GetState/
// SetState specifically, not AddBalance/SubBalance, and both backends can
// represent a 32-byte storage word losslessly regardless of their very
// different native-balance precision (uint256 vs. l1chain's uint64).
func TestOZERC20ProducesIdenticalObservableResultsViaAdapterAndHarness(t *testing.T) {
	parsedABI, err := abi.JSON(strings.NewReader(evm.L1TokenMetaData.ABI))
	if err != nil {
		t.Fatalf("parse L1Token ABI: %v", err)
	}
	creationCode, err := hex.DecodeString(strings.TrimPrefix(evm.L1TokenMetaData.Bin, "0x"))
	if err != nil {
		t.Fatalf("decode L1Token bytecode: %v", err)
	}
	initialSupply := big.NewInt(1_000_000)
	ctorArgs, err := parsedABI.Constructor.Inputs.Pack(initialSupply)
	if err != nil {
		t.Fatalf("pack constructor args: %v", err)
	}
	creationCode = append(creationCode, ctorArgs...)

	transferInput, err := parsedABI.Pack("transfer", ozRecipient, big.NewInt(1000))
	if err != nil {
		t.Fatalf("pack transfer: %v", err)
	}
	transferSig := parsedABI.Events["Transfer"].ID

	// --- Path 1: evm.Harness, the real go-ethereum *state.StateDB M6
	// already proved this fixture against (evm/oz_adversarial_test.go).
	h, err := evm.NewHarness()
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	h.Fund(ozDeployer, uint256.NewInt(1_000_000_000_000_000_000))
	harnessAddr, _, err := h.Deploy(ozDeployer, creationCode, ozDeployGas)
	if err != nil {
		t.Fatalf("Harness Deploy: %v", err)
	}
	if _, _, err := h.Call(ozDeployer, harnessAddr, transferInput, ozCallGas); err != nil {
		t.Fatalf("Harness transfer: %v", err)
	}
	harnessCall := func(in []byte) []byte {
		ret, _, err := h.Call(ozDeployer, harnessAddr, in, ozCallGas)
		if err != nil {
			t.Fatalf("Harness call: %v", err)
		}
		return ret
	}
	harnessDeployerBal := abiBalanceOf(t, parsedABI, ozDeployer, harnessCall)
	harnessRecipientBal := abiBalanceOf(t, parsedABI, ozRecipient, harnessCall)
	var harnessTransferSig common.Hash
	for _, lg := range h.Logs() {
		if lg.Address == harnessAddr && len(lg.Topics) > 0 && lg.Topics[0] == transferSig {
			harnessTransferSig = lg.Topics[0]
		}
	}

	// --- Path 2: this package's own *StateDB adapter, identical sequence.
	cfg := evm.ModernChainConfig()
	sdb := New(state.NewMemStateDB())
	sdb.AddBalance(ozDeployer, u256(1_000_000_000_000_000_000), 0)
	adapterAddr := deployVia(t, sdb, cfg, ozDeployer, creationCode, new(uint256.Int), ozDeployGas)
	callViaData(t, sdb, cfg, ozDeployer, adapterAddr, transferInput, ozCallGas)
	adapterCall := func(in []byte) []byte {
		return callViaData(t, sdb, cfg, ozDeployer, adapterAddr, in, ozCallGas)
	}
	adapterDeployerBal := abiBalanceOf(t, parsedABI, ozDeployer, adapterCall)
	adapterRecipientBal := abiBalanceOf(t, parsedABI, ozRecipient, adapterCall)
	var adapterTransferSig common.Hash
	for _, lg := range sdb.Logs() {
		if lg.Address == adapterAddr && len(lg.Topics) > 0 && lg.Topics[0] == transferSig {
			adapterTransferSig = lg.Topics[0]
		}
	}

	// vm.EVM.Create's own address derivation (crypto.CreateAddress(caller,
	// nonce)) is a pure function of the caller's nonce, unrelated to which
	// StateDB backs it -- so both paths deriving the identical address is
	// an expected, real cross-check, not a coincidence.
	if harnessAddr != adapterAddr {
		t.Fatalf("deployed address differs: harness=%s adapter=%s", harnessAddr, adapterAddr)
	}
	if harnessDeployerBal.Cmp(adapterDeployerBal) != 0 {
		t.Fatalf("deployer balanceOf differs: harness=%s adapter=%s", harnessDeployerBal, adapterDeployerBal)
	}
	if harnessRecipientBal.Cmp(adapterRecipientBal) != 0 {
		t.Fatalf("recipient balanceOf differs: harness=%s adapter=%s", harnessRecipientBal, adapterRecipientBal)
	}
	if adapterRecipientBal.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("adapter recipient balanceOf = %s, want 1000", adapterRecipientBal)
	}
	if harnessTransferSig == (common.Hash{}) || adapterTransferSig == (common.Hash{}) {
		t.Fatalf("Transfer event missing on one side: harness=%s adapter=%s", harnessTransferSig, adapterTransferSig)
	}
	if harnessTransferSig != adapterTransferSig {
		t.Fatalf("Transfer event signature differs: harness=%s adapter=%s", harnessTransferSig, adapterTransferSig)
	}
}
