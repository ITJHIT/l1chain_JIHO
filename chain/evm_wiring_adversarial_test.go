package chain

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"l1chain/core"
	"l1chain/evm"

	"github.com/ethereum/go-ethereum/accounts/abi"
)

// This file extends M4's (evm/adversarial_test.go) and M6's
// (evm/oz_adversarial_test.go) adversarial coverage -- both of which only
// ever drive the standalone evm/runtime.go Harness, disconnected from real
// chain state -- to the actual production consensus path wired in M7:
// chain.ApplyTxAt / Chain.AddBlock / Chain.CandidateStateRoot via
// evm_wiring.go's applyEVMTx. Same four invariant categories M4 established
// (out-of-gas, revert integrity, reentrancy bound, deterministic root), now
// proven at the level a real node actually executes at, not just in
// isolation.

// evmSstoreThenRevertRuntime writes slot0=1, THEN reverts: PUSH1 1 PUSH1 0
// SSTORE PUSH1 0 PUSH1 0 REVERT. Proves a reverted call's storage write does
// not survive.
var evmSstoreThenRevertRuntime = []byte{
	0x60, 0x01, // PUSH1 1
	0x60, 0x00, // PUSH1 0
	0x55,       // SSTORE (slot0 = 1)
	0x60, 0x00, // PUSH1 0  retSize
	0x60, 0x00, // PUSH1 0  retOffset
	0xfd, // REVERT
}

// evmSelfReentrantRuntime CALLs its own address forwarding all remaining
// gas, with no base case: PUSH1 0 x5 ADDRESS GAS CALL POP STOP. Mirrors
// evm/adversarial_test.go's own TestAdvEVM06RecursionBoundedByDepthAndGas
// runtime exactly, now driven through the wired chain path instead of the
// standalone Harness.
var evmSelfReentrantRuntime = []byte{
	0x60, 0x00, // PUSH1 0  retSize
	0x60, 0x00, // PUSH1 0  retOffset
	0x60, 0x00, // PUSH1 0  argsSize
	0x60, 0x00, // PUSH1 0  argsOffset
	0x60, 0x00, // PUSH1 0  value
	0x30,       // ADDRESS
	0x5a,       // GAS
	0xf1,       // CALL
	0x50,       // POP (discard success flag)
	0x00,       // STOP
}

// buildERC20DeployCode returns the real solc-compiled L1Token (contracts/
// L1Token.sol, frozen in evm/oz_erc20_fixture.go) creation bytecode with a
// packed constructor arg -- reused here purely as a genuinely large,
// realistic contract for the out-of-gas deploy case (M4's own case-1 used
// the same real-ERC20-under-tiny-gas shape).
func buildERC20DeployCode(t *testing.T) []byte {
	t.Helper()
	parsedABI, err := abi.JSON(strings.NewReader(evm.L1TokenMetaData.ABI))
	if err != nil {
		t.Fatalf("parse L1Token ABI: %v", err)
	}
	code, err := hex.DecodeString(strings.TrimPrefix(evm.L1TokenMetaData.Bin, "0x"))
	if err != nil {
		t.Fatalf("decode L1Token bytecode: %v", err)
	}
	ctorArgs, err := parsedABI.Constructor.Inputs.Pack(big.NewInt(1_000_000))
	if err != nil {
		t.Fatalf("pack constructor args: %v", err)
	}
	return append(code, ctorArgs...)
}

// ---------------------------------------------------------------------------
// Case 1: Out-of-gas deploy through the wired chain path.
// A real, sizeable contract (the solc-compiled L1Token) deployed with a gas
// limit far too small to complete construction. Must not be a block-
// validation error (only the up-front affordability check can reject a tx
// outright -- applyEVMTx's own doc comment); must leave no partial code at
// the derived address; must still consume exactly one nonce and burn the
// full gas reservation (no refund on OOG).
// ---------------------------------------------------------------------------
func TestAdvEVMWiring01OutOfGasDeployLeavesNoCodeAndConsumesFullGas(t *testing.T) {
	sender := addr(1)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0}
	gb := g.ToBlock()
	c := NewChain(gb, alloc)

	const tinyGas = 1_000 // real ERC20 construction needs vastly more
	deployTx := core.Transaction{From: sender, To: evm.DeployAddress, Nonce: 0, GasLimit: tinyGas, ChainID: DefaultChainID, Data: buildERC20DeployCode(t), Signature: []byte{1}}
	deployedAddr := evmCreateAddr(sender, 0)

	b := mineExchangeBlock(t, c, gb, []core.Transaction{deployTx})
	if err := c.AddBlock(b, acceptAll); err != nil {
		t.Fatalf("AddBlock: an out-of-gas EVM deploy must not be a block-validation error: %v", err)
	}

	st := c.State()
	if got := len(st.GetCode(deployedAddr)); got != 0 {
		t.Fatalf("OOG deploy left %d bytes of code at the derived address, want 0 (no partial contract)", got)
	}
	if got := st.GetAccount(sender).Nonce; got != 1 {
		t.Fatalf("sender nonce = %d, want 1 (an OOG deploy still consumes exactly one nonce)", got)
	}
	if got := st.GetAccount(sender).Balance; got != 10_000_000-tinyGas {
		t.Fatalf("sender balance = %d, want %d (OOG burns the full gas reservation, no refund)", got, 10_000_000-tinyGas)
	}
}

// ---------------------------------------------------------------------------
// Case 2: Reverted call through the wired chain path.
// A contract that writes storage then unconditionally REVERTs. The block
// must still be accepted; the storage write must NOT survive (real
// Snapshot/RevertToSnapshot exercised through production wiring, not just
// evm/adapter's own isolated tests); gas is still charged (bounded by the
// call's own gas limit, not free) and exactly one nonce is still consumed.
// ---------------------------------------------------------------------------
func TestAdvEVMWiring02RevertedCallLeavesStorageUnchanged(t *testing.T) {
	sender := addr(1)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0}
	gb := g.ToBlock()
	c := NewChain(gb, alloc)

	deployTx := core.Transaction{From: sender, To: evm.DeployAddress, Nonce: 0, GasLimit: 200_000, ChainID: DefaultChainID, Data: evmInitCode(evmSstoreThenRevertRuntime), Signature: []byte{1}}
	contractAddr := evmCreateAddr(sender, 0)
	b1 := mineExchangeBlock(t, c, gb, []core.Transaction{deployTx})
	if err := c.AddBlock(b1, acceptAll); err != nil {
		t.Fatalf("AddBlock deploy: %v", err)
	}
	if got := len(c.State().GetCode(contractAddr)); got == 0 {
		t.Fatal("setup: deploy left no code")
	}

	const callGas = 100_000
	balanceBeforeCall := c.State().GetAccount(sender).Balance
	callTx := core.Transaction{From: sender, To: contractAddr, Nonce: 1, GasLimit: callGas, ChainID: DefaultChainID, Signature: []byte{1}}
	b2 := mineExchangeBlock(t, c, b1, []core.Transaction{callTx})
	if err := c.AddBlock(b2, acceptAll); err != nil {
		t.Fatalf("AddBlock reverted call: a REVERTed EVM call must not be a block-validation error: %v", err)
	}

	st := c.State()
	if got := st.GetStorage(contractAddr, slotHash(0)); !got.IsZero() {
		t.Fatalf("slot0 = %x after a reverted SSTORE-then-REVERT call, want the zero hash (the write must not survive the revert)", got)
	}
	if got := st.GetAccount(sender).Nonce; got != 2 {
		t.Fatalf("sender nonce = %d, want 2 (deploy + reverted call, each consuming exactly one nonce)", got)
	}
	// A REVERT (unlike OOG) only burns gas actually consumed up to the
	// REVERT opcode, then refunds the rest -- so the bound checked here is
	// "some gas was charged, never more than the call's own budget," not an
	// exact figure (M4's own report documents getting this exact distinction
	// wrong once for a similar reentrancy case and correcting it).
	if got := st.GetAccount(sender).Balance; got >= balanceBeforeCall || got < balanceBeforeCall-callGas {
		t.Fatalf("sender balance = %d, want strictly less than %d and at least %d (gas charged, bounded by the call's own limit)", got, balanceBeforeCall, balanceBeforeCall-callGas)
	}
}

// ---------------------------------------------------------------------------
// Case 3: Reentrancy bound through the wired chain path.
// A contract that CALLs its own address forwarding all remaining gas, with
// no base case, called with a tight gas budget. The property under test is
// bounded, non-hanging, non-panicking termination (the 1024 call-depth cap
// and EIP-150's 63/64 gas-forwarding rule apply exactly as they do in the
// standalone Harness) -- reaching the assertions below at all is itself
// part of the proof.
// ---------------------------------------------------------------------------
func TestAdvEVMWiring03ReentrancyBoundedNoHangNoPanic(t *testing.T) {
	sender := addr(1)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0}
	gb := g.ToBlock()
	c := NewChain(gb, alloc)

	deployTx := core.Transaction{From: sender, To: evm.DeployAddress, Nonce: 0, GasLimit: 200_000, ChainID: DefaultChainID, Data: evmInitCode(evmSelfReentrantRuntime), Signature: []byte{1}}
	contractAddr := evmCreateAddr(sender, 0)
	b1 := mineExchangeBlock(t, c, gb, []core.Transaction{deployTx})
	if err := c.AddBlock(b1, acceptAll); err != nil {
		t.Fatalf("AddBlock deploy: %v", err)
	}
	afterDeployBalance := c.State().GetAccount(sender).Balance

	const callGas = 200_000
	callTx := core.Transaction{From: sender, To: contractAddr, Nonce: 1, GasLimit: callGas, ChainID: DefaultChainID, Signature: []byte{1}}
	b2 := mineExchangeBlock(t, c, b1, []core.Transaction{callTx})
	if err := c.AddBlock(b2, acceptAll); err != nil {
		t.Fatalf("AddBlock self-reentrant call: %v", err)
	}

	st := c.State()
	if got := st.GetAccount(sender).Nonce; got != 2 {
		t.Fatalf("sender nonce = %d, want 2 (deploy + self-reentrant call)", got)
	}
	if got := st.GetAccount(sender).Balance; got > afterDeployBalance || got < afterDeployBalance-callGas {
		t.Fatalf("sender balance = %d, want between %d and %d (call gas bounded by its own budget)", got, afterDeployBalance-callGas, afterDeployBalance)
	}
}

// ---------------------------------------------------------------------------
// Case 4: Determinism of the whole adversarial sequence across two
// INDEPENDENT chain instances -- cases 1-3 combined into one multi-block
// sequence, mined on chainA via CandidateStateRoot+PoW (the exact path
// node.go's own miner takes), validated on chainB purely via AddBlock
// (chainB never calls CandidateStateRoot itself), exactly mirroring
// TestEVMAdapterCandidateStateRootAgreesWithFreshChainAddBlock's own shape.
// Proves the OOG/revert/reentrancy invariants above hold identically for a
// second node independently re-executing the same blocks, not just
// self-consistently within one chain instance.
// ---------------------------------------------------------------------------
func TestAdvEVMWiring04AdversarialSequenceDeterministicAcrossIndependentChains(t *testing.T) {
	sender := addr(1)
	recipient := addr(2)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0}
	gb := g.ToBlock()

	chainA := NewChain(gb, alloc) // mines
	chainB := NewChain(gb, alloc) // only ever validates via AddBlock

	const (
		nonceOOGDeploy = iota
		nonceRevertDeploy
		nonceReentrantDeploy
		nonceRevertCall
		nonceReentrantCall
		nonceTransfer
	)
	oogAddr := evmCreateAddr(sender, nonceOOGDeploy)
	revertAddr := evmCreateAddr(sender, nonceRevertDeploy)
	reentrantAddr := evmCreateAddr(sender, nonceReentrantDeploy)

	blocks := [][]core.Transaction{
		// Block 1: OOG deploy of a real, sizeable contract under a tiny gas limit.
		{
			{From: sender, To: evm.DeployAddress, Nonce: nonceOOGDeploy, GasLimit: 1_000, ChainID: DefaultChainID, Data: buildERC20DeployCode(t), Signature: []byte{1}},
		},
		// Block 2: deploy the revert-on-call and self-reentrant contracts.
		{
			{From: sender, To: evm.DeployAddress, Nonce: nonceRevertDeploy, GasLimit: 200_000, ChainID: DefaultChainID, Data: evmInitCode(evmSstoreThenRevertRuntime), Signature: []byte{1}},
			{From: sender, To: evm.DeployAddress, Nonce: nonceReentrantDeploy, GasLimit: 200_000, ChainID: DefaultChainID, Data: evmInitCode(evmSelfReentrantRuntime), Signature: []byte{1}},
		},
		// Block 3: exercise both under real chain execution, interleaved with
		// an ordinary plain transfer (the pre-existing dispatch path,
		// unaffected in the same sequence).
		{
			{From: sender, To: revertAddr, Nonce: nonceRevertCall, GasLimit: 100_000, ChainID: DefaultChainID, Signature: []byte{1}},
			{From: sender, To: reentrantAddr, Nonce: nonceReentrantCall, GasLimit: 200_000, ChainID: DefaultChainID, Signature: []byte{1}},
			{From: sender, To: recipient, Value: 100, Nonce: nonceTransfer, ChainID: DefaultChainID, Signature: []byte{1}},
		},
	}

	parent := gb
	for i, txs := range blocks {
		b := mineExchangeBlock(t, chainA, parent, txs)
		if err := chainA.AddBlock(b, acceptAll); err != nil {
			t.Fatalf("block %d: chainA rejected its own mined block: %v", i+1, err)
		}
		if err := chainB.AddBlock(b, acceptAll); err != nil {
			t.Fatalf("block %d: chainB (independent instance) rejected chainA's block: %v", i+1, err)
		}
		if got, want := chainB.State().StateRoot(), chainA.State().StateRoot(); got != want {
			t.Fatalf("block %d: chainB's re-derived root %x != chainA's mined root %x", i+1, got, want)
		}
		parent = b
	}

	for _, c := range []*Chain{chainA, chainB} {
		st := c.State()
		if got := len(st.GetCode(oogAddr)); got != 0 {
			t.Fatalf("OOG deploy left %d bytes of code, want 0", got)
		}
		if got := st.GetStorage(revertAddr, slotHash(0)); !got.IsZero() {
			t.Fatalf("revert contract slot0 = %x, want zero (revert must not persist)", got)
		}
		if got := len(st.GetCode(reentrantAddr)); got == 0 {
			t.Fatal("self-reentrant contract deployment left no code")
		}
		if got := st.GetAccount(sender).Nonce; got != nonceTransfer+1 {
			t.Fatalf("sender nonce = %d, want %d (every tx in the sequence consumes exactly one nonce)", got, nonceTransfer+1)
		}
		if got := st.GetAccount(recipient).Balance; got != 100 {
			t.Fatalf("recipient balance = %d, want 100 (ordinary transfer path unaffected by the adversarial sequence)", got)
		}
	}
}
