package chain

import (
	"testing"

	"github.com/ITJHIT/onchain-orderbook/orderbook"

	"l1chain/consensus"
	"l1chain/core"
	"l1chain/exchange"
	"l1chain/state"
)

// This file demonstrates the actual argument for a batch auction, on the real
// state transition, not just inside the vendored library: under continuous
// matching an order's position in the block decides who gets filled, which is
// exactly the option a block producer can sell (reorder transactions, or
// insert its own, ahead of a victim's). A uniform-price batch auction removes
// that option instead of policing it.
//
// Both blocks below run the identical three transactions -- one seller resting
// supply, two buyers oversubscribing it -- with only the two buyers swapped.
// Continuous mode must disagree with itself about who gets filled depending on
// that swap. Batch mode must not.
//
// It calls ApplyBlockWithMode directly rather than going through Chain.AddBlock:
// a real crossing trade needs the seller to hold the base asset, and genesis
// currently only funds the native/quote side (exchange.CreditBase is
// documented as a genesis-or-test helper precisely because there is no minting
// transaction yet). Going through the full Chain would replay from a genesis
// that never saw the credit and reject the sell for lack of funds before the
// interesting part of this test ever ran. applyTxsAt -- what ApplyBlockWithMode
// calls -- is the exact function Chain.AddBlock's applyBlockRewarded and
// Chain.CandidateStateRoot both call internally, so this is still the real
// production code path, just without the genesis-replay ceremony this
// particular property does not need.

func mevState(t *testing.T, seller, buyer1, buyer2 core.Address) state.StateDB {
	t.Helper()
	st := state.NewMemStateDB()
	st.SetAccount(seller, state.Account{Balance: 0})
	st.SetAccount(buyer1, state.Account{Balance: 10_000})
	st.SetAccount(buyer2, state.Account{Balance: 10_000})
	exchange.CreditBase(st, seller, 10) // exactly enough for ONE buyer, not both
	return st
}

// raceBlock returns [seller rests 10 @ 100, buyer A bids 10 @ 100, buyer B bids
// 10 @ 100] with the two buyers in the given order. Demand is double the
// available supply either way.
func raceBlock(seller, a, b core.Address) []core.Transaction {
	return []core.Transaction{
		orderTxFor(seller, 0, orderbook.Sell, 100, 10),
		orderTxFor(a, 0, orderbook.Buy, 100, 10),
		orderTxFor(b, 0, orderbook.Buy, 100, 10),
	}
}

func orderTxFor(from core.Address, nonce uint64, side orderbook.Side, price, qty int64) core.Transaction {
	return core.Transaction{
		From: from, To: exchange.Address, Nonce: nonce, ChainID: DefaultChainID,
		Data: exchange.EncodePlace(side, price, qty), Signature: []byte{1},
	}
}

func TestContinuousMatchingIsPositionSensitiveOnTheRealChain(t *testing.T) {
	seller, buyer1, buyer2 := addr(1), addr(2), addr(3)

	stAB := mevState(t, seller, buyer1, buyer2)
	if err := ApplyBlockWithMode(stAB, core.Block{Header: core.Header{Height: 1, Coinbase: addr(9)},
		Txs: raceBlock(seller, buyer1, buyer2)}, addr(9), acceptAll, exchange.Continuous); err != nil {
		t.Fatalf("block [buyer1, buyer2]: %v", err)
	}
	filled1AB, _ := exchange.BaseOf(stAB, buyer1)
	filled2AB, _ := exchange.BaseOf(stAB, buyer2)

	stBA := mevState(t, seller, buyer1, buyer2)
	if err := ApplyBlockWithMode(stBA, core.Block{Header: core.Header{Height: 1, Coinbase: addr(9)},
		Txs: raceBlock(seller, buyer2, buyer1)}, addr(9), acceptAll, exchange.Continuous); err != nil {
		t.Fatalf("block [buyer2, buyer1]: %v", err)
	}
	filled1BA, _ := exchange.BaseOf(stBA, buyer1)
	filled2BA, _ := exchange.BaseOf(stBA, buyer2)

	// Whoever is EARLIER in the block takes the entire scarce supply; the
	// other gets nothing. Swapping the order swaps who that is.
	if filled1AB != 10 || filled2AB != 0 {
		t.Fatalf("[buyer1, buyer2] order: buyer1=%d buyer2=%d, want 10/0", filled1AB, filled2AB)
	}
	if filled1BA != 0 || filled2BA != 10 {
		t.Fatalf("[buyer2, buyer1] order: buyer1=%d buyer2=%d, want 0/10", filled1BA, filled2BA)
	}
}

func TestBatchAuctionIsPositionIndifferentOnTheRealChain(t *testing.T) {
	seller, buyer1, buyer2 := addr(1), addr(2), addr(3)

	stAB := mevState(t, seller, buyer1, buyer2)
	if err := ApplyBlockWithMode(stAB, core.Block{Header: core.Header{Height: 1, Coinbase: addr(9)},
		Txs: raceBlock(seller, buyer1, buyer2)}, addr(9), acceptAll, exchange.BatchAuction); err != nil {
		t.Fatalf("block [buyer1, buyer2]: %v", err)
	}
	filled1AB, _ := exchange.BaseOf(stAB, buyer1)
	filled2AB, _ := exchange.BaseOf(stAB, buyer2)

	stBA := mevState(t, seller, buyer1, buyer2)
	if err := ApplyBlockWithMode(stBA, core.Block{Header: core.Header{Height: 1, Coinbase: addr(9)},
		Txs: raceBlock(seller, buyer2, buyer1)}, addr(9), acceptAll, exchange.BatchAuction); err != nil {
		t.Fatalf("block [buyer2, buyer1]: %v", err)
	}
	filled1BA, _ := exchange.BaseOf(stBA, buyer1)
	filled2BA, _ := exchange.BaseOf(stBA, buyer2)

	// Equal-sized competing demand against scarce supply splits evenly, and
	// splits the SAME way regardless of which buyer's transaction the block
	// happened to list first.
	if filled1AB != 5 || filled2AB != 5 {
		t.Fatalf("[buyer1, buyer2] order: buyer1=%d buyer2=%d, want 5/5", filled1AB, filled2AB)
	}
	if filled1BA != 5 || filled2BA != 5 {
		t.Fatalf("[buyer2, buyer1] order: buyer1=%d buyer2=%d, want 5/5", filled1BA, filled2BA)
	}
	if filled1AB != filled1BA || filled2AB != filled2BA {
		t.Fatalf("reordering the block changed the outcome: [1,2]=%d/%d vs [2,1]=%d/%d",
			filled1AB, filled2AB, filled1BA, filled2BA)
	}
}

// The chain-level configuration surface this all hangs off: SetExchangeMode
// actually changes what a block does, through the real Chain (genesis-funded,
// buy-only so no base-asset gap applies), not just through the lower-level
// ApplyBlockWithMode helper the two tests above use.
func TestChainExchangeModeAffectsRealBlocks(t *testing.T) {
	alloc := map[core.Address]uint64{addr(2): 10_000, addr(3): 10_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0}
	gb := g.ToBlock()

	c := NewChain(gb, alloc)
	if c.ExchangeMode() != exchange.Continuous {
		t.Fatalf("default exchange mode = %v, want Continuous (zero value)", c.ExchangeMode())
	}
	c.SetExchangeMode(exchange.BatchAuction)
	if c.ExchangeMode() != exchange.BatchAuction {
		t.Fatal("SetExchangeMode did not take effect")
	}

	// Two buyers bidding for the same price on a chain with nothing resting:
	// nothing crosses in either mode, but the block must still be mineable and
	// both orders must still be resting afterward -- proves the batch path is
	// wired all the way through AddBlock, not just reachable in isolation.
	txs := []core.Transaction{
		orderTxFor(addr(2), 0, orderbook.Buy, 100, 5),
		orderTxFor(addr(3), 0, orderbook.Buy, 100, 5),
	}
	root, gasUsed, err := c.CandidateStateRoot(txs, addr(9), acceptAll)
	if err != nil {
		t.Fatalf("CandidateStateRoot: %v", err)
	}
	baseFee := ComputeBaseFee(gb.Header.BaseFee, gb.Header.GasUsed, GasTarget(c.GasLimit()))
	h := core.Header{Height: 1, PrevHash: gb.Hash(), Coinbase: addr(9), Difficulty: testDiff, BaseFee: baseFee, GasUsed: gasUsed}
	b := core.Block{Header: h, Txs: txs}
	b.Header.MerkleRoot = b.TxRoot()
	b.Header.StateRoot = root
	consensus.Mine(&b.Header, 0)

	if err := c.AddBlock(b, acceptAll); err != nil {
		t.Fatalf("AddBlock: %v", err)
	}
	if lq := exchange.LockedQuoteOf(c.State(), addr(2)); lq != 500 {
		t.Fatalf("buyer 2 locked quote = %d, want 500 (order still resting)", lq)
	}
	if lq := exchange.LockedQuoteOf(c.State(), addr(3)); lq != 500 {
		t.Fatalf("buyer 3 locked quote = %d, want 500 (order still resting)", lq)
	}
}
