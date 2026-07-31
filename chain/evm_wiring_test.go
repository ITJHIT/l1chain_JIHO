package chain

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"l1chain/core"
	"l1chain/evm"
	"l1chain/state"

	"github.com/ethereum/go-ethereum/accounts/abi"
)

// TestEVMAdapterCandidateStateRootAgreesWithFreshChainAddBlock is PR6's own
// highest-signal proof, exactly as the plan specifies: chainA mines a real
// EVM-adapter workload block by block via CandidateStateRoot+PoW (the exact
// path node.go's own miner takes); chainB is an INDEPENDENTLY constructed
// *Chain from the same genesis that NEVER calls CandidateStateRoot itself
// -- it only ever sees chainA's already-mined, already-PoW-stamped blocks
// through AddBlock, exactly like a real second node syncing over the
// network. Both must accept every block and agree on StateRoot() after
// each one.
//
// This is stronger than mineExchangeBlock's own existing single-chain
// tests (CandidateStateRoot and AddBlock's re-derivation agreeing WITHIN
// one *Chain instance): two independently constructed *state.New() MPTs,
// two independently constructed evm/adapter.StateDB wrappers, reaching the
// identical root is the only real proof that a second node validating this
// EVM workload over the network would actually accept it.
func TestEVMAdapterCandidateStateRootAgreesWithFreshChainAddBlock(t *testing.T) {
	sender := addr(1)
	beneficiary := addr(2)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0}
	gb := g.ToBlock()

	chainA := NewChain(gb, alloc) // mines
	chainB := NewChain(gb, alloc) // only ever validates via AddBlock

	parsedABI, err := abi.JSON(strings.NewReader(evm.L1TokenMetaData.ABI))
	if err != nil {
		t.Fatalf("parse L1Token ABI: %v", err)
	}
	erc20Code, err := hex.DecodeString(strings.TrimPrefix(evm.L1TokenMetaData.Bin, "0x"))
	if err != nil {
		t.Fatalf("decode L1Token bytecode: %v", err)
	}
	ctorArgs, err := parsedABI.Constructor.Inputs.Pack(big.NewInt(1_000_000))
	if err != nil {
		t.Fatalf("pack constructor args: %v", err)
	}
	erc20Code = append(erc20Code, ctorArgs...)

	const (
		nonceERC20 = iota
		nonceInner
		nonceOuter
		nonceCallOuter
		nonceSelfdestruct
	)
	erc20Addr := evmCreateAddr(sender, nonceERC20)
	innerAddr := evmCreateAddr(sender, nonceInner)
	outerAddr := evmCreateAddr(sender, nonceOuter)
	selfdestructedAddr := evmCreateAddr(sender, nonceSelfdestruct)

	blocks := [][]core.Transaction{
		// Real OZ-ERC20 deployment through evm.DeployAddress.
		{
			{From: sender, To: evm.DeployAddress, Nonce: nonceERC20, GasLimit: 3_000_000, ChainID: DefaultChainID, Data: erc20Code, Signature: []byte{1}},
		},
		// A nested CALL whose callee genuinely reverts (real Snapshot/
		// RevertToSnapshot exercise): "inner" always reverts, "outer"
		// calls inner, swallows the revert, and writes its own storage.
		{
			{From: sender, To: evm.DeployAddress, Nonce: nonceInner, GasLimit: 1_000_000, ChainID: DefaultChainID, Data: evmInitCode(evmRevertRuntime), Signature: []byte{1}},
			{From: sender, To: evm.DeployAddress, Nonce: nonceOuter, GasLimit: 1_000_000, ChainID: DefaultChainID, Data: evmInitCode(evmCallThenStoreRuntime(innerAddr)), Signature: []byte{1}},
		},
		{
			{From: sender, To: outerAddr, Nonce: nonceCallOuter, GasLimit: 1_000_000, ChainID: DefaultChainID, Signature: []byte{1}},
		},
		// A constructor that self-destructs DURING its own creation
		// transaction -- EIP-6780's "new contract" branch. Passed
		// directly as init code, not wrapped in evmInitCode's
		// CODECOPY+RETURN pattern (see mpt_determinism_test.go's own
		// identical note).
		{
			{From: sender, To: evm.DeployAddress, Nonce: nonceSelfdestruct, GasLimit: 1_000_000, ChainID: DefaultChainID, Data: evmSelfdestructRuntime(beneficiary), Signature: []byte{1}},
		},
	}

	parent := gb
	for i, txs := range blocks {
		b := mineExchangeBlock(t, chainA, parent, txs) // generic despite the name: just mines+PoW-stamps a block of txs
		if err := chainA.AddBlock(b, acceptAll); err != nil {
			t.Fatalf("block %d: chainA rejected its own mined block: %v", i+1, err)
		}
		if err := chainB.AddBlock(b, acceptAll); err != nil {
			t.Fatalf("block %d: chainB (independent instance, never called CandidateStateRoot) rejected chainA's block: %v", i+1, err)
		}
		if got, want := chainB.State().StateRoot(), chainA.State().StateRoot(); got != want {
			t.Fatalf("block %d: chainB's re-derived root %x != chainA's mined root %x", i+1, got, want)
		}
		parent = b
	}

	if chainA.State().StateRoot().IsZero() {
		t.Fatal("expected a non-zero state root after real EVM transaction activity")
	}

	// Sanity: the workload actually did what it claims, on BOTH
	// independent chains (agreeing roots alone would not catch two chains
	// that both silently no-op'd the same way).
	for _, c := range []*Chain{chainA, chainB} {
		st := c.State()
		if got := len(st.GetCode(erc20Addr)); got == 0 {
			t.Fatal("real OZ-ERC20 deployment left no code at the derived address")
		}
		if got := st.GetStorage(outerAddr, slotHash(0)); got != slotHash(1) {
			t.Fatalf("outer contract slot0 = %x, want 1 (its own write must survive inner's revert)", got)
		}
		if got := st.GetAccount(selfdestructedAddr); got != (state.Account{}) {
			t.Fatalf("self-destructed account = %+v, want the zero Account (Finalise should have deleted it)", got)
		}
	}
}
