package chain

import (
	"testing"

	"github.com/ITJHIT/onchain-orderbook/orderbook"

	"l1chain/core"
	"l1chain/exchange"
	"l1chain/state"
	"l1chain/vm"
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
