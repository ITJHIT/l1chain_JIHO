package chain

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/ITJHIT/onchain-orderbook/orderbook"

	"l1chain/core"
	"l1chain/evm"
	"l1chain/exchange"
	"l1chain/state"
	"l1chain/vm"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// TestMPTDeterministicUnderRealisticMixedWorkload is a pre-flight check for
// state.New() (the MPT), run BEFORE anything in chain.go is switched over to
// it. Nothing here touches Chain -- it cannot yet: fundGenesis/deriveState
// are hardcoded to state.NewMemStateDB() until that switch happens. Instead
// this reproduces, by hand, exactly what Chain.CandidateStateRoot and
// Chain.AddBlock's applyBlockRewarded both do internally: fund genesis, then
// for each block apply every tx via ApplyTxAt in order and credit
// BlockReward -- against two INDEPENDENTLY constructed state.New()
// instances processing the identical block sequence.
//
// Two sources of real nondeterminism risk are deliberately exercised, not
// just asserted away: genesis funding iterates a Go map (whose iteration
// order is randomized on purpose), and the workload mixes a plain transfer,
// a contract deploy + call (touches CodeHash and the contract's own storage
// trie), and an exchange order at a real block height/index (touches the
// exchange address's namespaced storage) -- so both trie levels (world +
// per-account storage) are exercised, the same way
// TestContractBlockDeterministicReplay and
// TestCandidateStateRootAgreesWithAddBlockAcrossMultipleExchangeBlocks
// already do for the pre-MPT root.
func TestMPTDeterministicUnderRealisticMixedWorkload(t *testing.T) {
	// addr(N) and exAddr(N) both just set byte[0] = N and zero the rest, so
	// they collide for equal N -- every address below uses a distinct N
	// across BOTH helpers (found the hard way: an earlier version reused 1
	// for both sender and trader1, making them the same account and
	// producing an unrelated-looking ErrBadNonce failure).
	sender := addr(1)
	recipient := addr(2)
	trader1, trader2 := exAddr(3), exAddr(4)
	miner := addr(9)
	alloc := map[core.Address]uint64{
		sender:  10_000_000,
		trader1: 1_000_000,
		trader2: 1_000_000,
	}
	contract := vm.CreateAddress(sender, 1) // sender's nonce is 1 at deploy (see block 1)

	replicate := func() state.StateDB {
		st := state.New()
		for a, bal := range alloc { // Go map iteration order: randomized on purpose
			acct := st.GetAccount(a)
			acct.Balance += bal
			st.SetAccount(a, acct)
		}
		exchange.CreditBase(st, trader1, 10_000)

		apply := func(height uint64, txs ...core.Transaction) {
			for i, tx := range txs {
				if err := ApplyTxAt(st, tx, acceptAll, height, uint32(i)); err != nil {
					t.Fatalf("height %d tx %d: %v", height, i, err)
				}
			}
			m := st.GetAccount(miner)
			m.Balance += BlockReward
			st.SetAccount(miner, m)
		}

		// Block 1: a plain transfer, then a contract deployment.
		apply(1,
			core.Transaction{From: sender, To: recipient, Value: 100, Nonce: 0, ChainID: DefaultChainID, Signature: []byte{1}},
			core.Transaction{From: sender, To: core.Address{}, Nonce: 1, GasLimit: 100000, ChainID: DefaultChainID, Data: counterCode, Signature: []byte{1}},
		)

		// Block 2: call the deployed contract (mutates its own storage
		// trie), and a resting sell order on the exchange (mutates the
		// exchange address's namespaced storage) at a real height/index --
		// exactly the position an order's identity is derived from.
		apply(2,
			core.Transaction{From: sender, To: contract, Nonce: 2, GasLimit: 100000, ChainID: DefaultChainID, Signature: []byte{1}},
			core.Transaction{From: trader1, To: exchange.Address, Nonce: 0, Data: exchange.EncodePlace(orderbook.Sell, 100, 40), Signature: []byte{1}},
		)

		// Block 3: a crossing buy against the resting sell.
		apply(3,
			core.Transaction{From: trader2, To: exchange.Address, Nonce: 0, Data: exchange.EncodePlace(orderbook.Buy, 120, 25), Signature: []byte{1}},
		)

		return st
	}

	stA := replicate()
	stB := replicate()

	if stA.StateRoot() != stB.StateRoot() {
		t.Fatalf("two independent replicas of the identical block sequence diverged: %x vs %x", stA.StateRoot(), stB.StateRoot())
	}
	if stA.StateRoot().IsZero() {
		t.Fatal("expected a non-zero state root after real transaction activity")
	}

	// Sanity: the workload actually did what it claims, on both replicas
	// (agreeing roots alone would not catch two replicas that both silently
	// no-op'd the same way).
	for _, st := range []state.StateDB{stA, stB} {
		if got := st.GetStorage(contract, slotHash(0)); got != slotHash(1) {
			t.Fatalf("contract slot0 = %x, want 1 after one call", got)
		}
		sellerBase, sellerLocked := exchange.BaseOf(st, trader1)
		if sellerBase != 10_000-25 || sellerLocked != 15 {
			t.Fatalf("seller base=%d locked=%d, want base=%d locked=15", sellerBase, sellerLocked, 10_000-25)
		}
	}
}

// evmCreateAddr mirrors what vm.EVM.Create itself derives internally
// (crypto.CreateAddress(caller, nonce), go-ethereum's own real, keccak-based
// formula -- confirmed unavoidable and harmless to use as-is: see the PR
// that removed this repo's own attempted SHA-256 scheme, which turned out
// to have no way to actually take effect). Needed here purely so this test
// can address a SECOND transaction at a contract deployed by an earlier
// one in the same block sequence.
func evmCreateAddr(from core.Address, nonce uint64) core.Address {
	var f common.Address
	copy(f[:], from[:])
	a := crypto.CreateAddress(f, nonce)
	var out core.Address
	copy(out[:], a[:])
	return out
}

// evmInitCode wraps runtime in the standard "CODECOPY the trailing bytes
// into memory, then RETURN them" constructor pattern (the same shape
// evm/adapter's own selfdestruct_test.go uses), so runtime becomes the
// deployed contract's own code instead of executing at deploy time.
func evmInitCode(runtime []byte) []byte {
	n := byte(len(runtime))
	const offset = 12 // length of the prefix itself, below
	prefix := []byte{
		0x60, n, // PUSH1 n        (length, for CODECOPY)
		0x60, offset, // PUSH1 offset   (code offset, for CODECOPY)
		0x60, 0x00, // PUSH1 0        (dest offset, for CODECOPY)
		0x39,       // CODECOPY
		0x60, n,    // PUSH1 n        (size, for RETURN)
		0x60, 0x00, // PUSH1 0        (offset, for RETURN)
		0xf3, // RETURN
	}
	return append(prefix, runtime...)
}

// evmRevertRuntime always reverts: PUSH1 0 PUSH1 0 REVERT.
var evmRevertRuntime = []byte{0x60, 0x00, 0x60, 0x00, 0xfd}

// evmCallThenStoreRuntime CALLs inner and discards its success/failure flag
// -- a real mid-call revert that's CONTAINED here, never propagated to the
// outer call -- then SSTOREs slot 0 = 1 to prove the outer call's own state
// change survives despite it. Exercises evm/adapter's Snapshot/
// RevertToSnapshot for real: vm.EVM.Call snapshots before dispatching to
// inner and reverts to it when inner's REVERT opcode fires.
func evmCallThenStoreRuntime(inner core.Address) []byte {
	code := []byte{
		0x60, 0x00, // PUSH1 0  retSize
		0x60, 0x00, // PUSH1 0  retOffset
		0x60, 0x00, // PUSH1 0  argsSize
		0x60, 0x00, // PUSH1 0  argsOffset
		0x60, 0x00, // PUSH1 0  value
		0x73, // PUSH20 inner
	}
	code = append(code, inner[:]...)
	code = append(code,
		0x5a,       // GAS
		0xf1,       // CALL
		0x50,       // POP (discard success flag)
		0x60, 0x01, // PUSH1 1  (SSTORE value)
		0x60, 0x00, // PUSH1 0  (SSTORE key)
		0x55, // SSTORE
		0x00, // STOP
	)
	return code
}

// evmSelfdestructRuntime is PUSH20 <beneficiary> SELFDESTRUCT.
func evmSelfdestructRuntime(beneficiary core.Address) []byte {
	code := make([]byte, 0, 22)
	code = append(code, 0x73)
	code = append(code, beneficiary[:]...)
	code = append(code, 0xff)
	return code
}

// TestMPTDeterministicWithEVMWorkload extends
// TestMPTDeterministicUnderRealisticMixedWorkload's exact shape (two
// independently constructed state.New() instances processing an identical
// block sequence, roots compared) to cover the real embedded EVM path
// wired in this same change: a real M6 OZ-ERC20 deployment through
// evm.DeployAddress, a nested CALL whose callee genuinely reverts
// (exercising Snapshot/RevertToSnapshot for real, not just in isolation),
// and a same-transaction SELFDESTRUCT (EIP-6780's "new contract" branch),
// interleaved with a plain transfer and an M3 contract call so both the
// old and new dispatch paths run side by side in the same blocks.
func TestMPTDeterministicWithEVMWorkload(t *testing.T) {
	sender := addr(1)
	recipient := addr(2)
	beneficiary := addr(3)
	alloc := map[core.Address]uint64{sender: 10_000_000}

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

	// Nonces, and therefore every deployed address (both M3's own
	// SumHash(From||Nonce) and go-ethereum's crypto.CreateAddress), are
	// fixed ahead of time so this test can reference them when asserting,
	// without depending on either replica's own internal bookkeeping. Every
	// transaction below is from the SAME sender, so these must be exactly
	// sequential (0, 1, 2, ...) in the order the transactions are applied,
	// not just distinct.
	const (
		nonceERC20 = iota // block 1, tx 0
		nonceM3Deploy     // block 1, tx 1
		nonceInner        // block 2, tx 0
		nonceOuter        // block 2, tx 1
		nonceCallOuter    // block 3, tx 0
		nonceCallM3       // block 3, tx 1
		nonceTransfer     // block 3, tx 2
		nonceSelfdestruct // block 4, tx 0
	)
	erc20Addr := evmCreateAddr(sender, nonceERC20)
	m3ContractAddr := vm.CreateAddress(sender, nonceM3Deploy)
	innerAddr := evmCreateAddr(sender, nonceInner)
	outerAddr := evmCreateAddr(sender, nonceOuter)
	selfdestructedAddr := evmCreateAddr(sender, nonceSelfdestruct)

	replicate := func() state.StateDB {
		st := state.New()
		acct := st.GetAccount(sender)
		acct.Balance += alloc[sender]
		st.SetAccount(sender, acct)

		apply := func(height uint64, txs ...core.Transaction) {
			for i, tx := range txs {
				if err := ApplyTxAt(st, tx, acceptAll, height, uint32(i)); err != nil {
					t.Fatalf("height %d tx %d: %v", height, i, err)
				}
			}
		}

		// Block 1: real OZ-ERC20 deployed through evm.DeployAddress,
		// alongside an M3 contract deployment (To == zero address) --
		// both dispatch paths in the same block.
		apply(1,
			core.Transaction{From: sender, To: evm.DeployAddress, Nonce: nonceERC20, GasLimit: 3_000_000, ChainID: DefaultChainID, Data: erc20Code, Signature: []byte{1}},
			core.Transaction{From: sender, To: core.Address{}, Nonce: nonceM3Deploy, GasLimit: 100000, ChainID: DefaultChainID, Data: counterCode, Signature: []byte{1}},
		)

		// Block 2: deploy the "inner" (always reverts) and "outer" (calls
		// inner, swallows the revert, writes its own storage) EVM
		// contracts.
		apply(2,
			core.Transaction{From: sender, To: evm.DeployAddress, Nonce: nonceInner, GasLimit: 1_000_000, ChainID: DefaultChainID, Data: evmInitCode(evmRevertRuntime), Signature: []byte{1}},
			core.Transaction{From: sender, To: evm.DeployAddress, Nonce: nonceOuter, GasLimit: 1_000_000, ChainID: DefaultChainID, Data: evmInitCode(evmCallThenStoreRuntime(innerAddr)), Signature: []byte{1}},
		)

		// Block 3: call the outer contract (real nested-call-with-revert),
		// call the M3 contract (unrelated dispatch path, same block), and
		// a plain transfer.
		apply(3,
			core.Transaction{From: sender, To: outerAddr, Nonce: nonceCallOuter, GasLimit: 1_000_000, ChainID: DefaultChainID, Signature: []byte{1}},
			core.Transaction{From: sender, To: m3ContractAddr, Nonce: nonceCallM3, GasLimit: 100000, ChainID: DefaultChainID, Signature: []byte{1}},
			core.Transaction{From: sender, To: recipient, Value: 100, Nonce: nonceTransfer, ChainID: DefaultChainID, Signature: []byte{1}},
		)

		// Block 4: a constructor that self-destructs DURING its own
		// creation transaction -- EIP-6780's "new contract" branch. Passed
		// directly as init code (NOT wrapped in evmInitCode's
		// CODECOPY+RETURN pattern): the constructor itself IS the
		// self-destruct, executed at deploy time, not code that could
		// later be called and self-destruct on some future, separate
		// transaction (that would exercise EIP-6780's OTHER branch
		// instead, already covered by evm/adapter's own
		// TestSelfDestruct6780PriorTxOnlyTransfersBalance).
		apply(4,
			core.Transaction{From: sender, To: evm.DeployAddress, Nonce: nonceSelfdestruct, GasLimit: 1_000_000, ChainID: DefaultChainID, Data: evmSelfdestructRuntime(beneficiary), Signature: []byte{1}},
		)

		return st
	}

	stA := replicate()
	stB := replicate()

	if stA.StateRoot() != stB.StateRoot() {
		t.Fatalf("two independent replicas of the identical EVM+M3 block sequence diverged: %x vs %x", stA.StateRoot(), stB.StateRoot())
	}
	if stA.StateRoot().IsZero() {
		t.Fatal("expected a non-zero state root after real transaction activity")
	}

	for _, st := range []state.StateDB{stA, stB} {
		if got := len(st.GetCode(erc20Addr)); got == 0 {
			t.Fatal("real OZ-ERC20 deployment left no code at the derived address")
		}
		if got := st.GetStorage(outerAddr, slotHash(0)); got != slotHash(1) {
			t.Fatalf("outer contract slot0 = %x, want 1 (its own write must survive inner's revert)", got)
		}
		if got := st.GetAccount(innerAddr).Balance; got != 0 {
			t.Fatalf("inner contract balance = %d, want 0 (its constructor deploy carried no value)", got)
		}
		if got := len(st.GetCode(m3ContractAddr)); got == 0 {
			t.Fatal("M3 contract deployment left no code -- the old dispatch path must be unaffected")
		}
		// The same-tx self-destruct must have actually been deleted by
		// Finalise (applyEVMTx's own wiring), not just flagged: a
		// SelfDestruct call alone only sets a bookkeeping flag
		// (evm/adapter.StateDB.SelfDestruct's own doc comment).
		if got := st.GetAccount(selfdestructedAddr); got != (state.Account{}) {
			t.Fatalf("self-destructed account = %+v, want the zero Account (Finalise should have deleted it)", got)
		}
		if got := len(st.GetCode(selfdestructedAddr)); got != 0 {
			t.Fatalf("self-destructed contract still has %d bytes of code, want none", got)
		}
	}
}
