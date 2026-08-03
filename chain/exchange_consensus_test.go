package chain

import (
	"testing"

	"github.com/ITJHIT/onchain-orderbook/orderbook"

	"l1chain/consensus"
	"l1chain/core"
	"l1chain/exchange"
)

// This file exercises the exchange through the path a real node actually
// takes -- Chain.AddBlock and Chain.CandidateStateRoot -- rather than the
// standalone ApplyBlock helper in transition.go, which nothing in production
// calls. That distinction is not academic: applyBlockRewarded and
// CandidateStateRoot originally called the bare, position-less ApplyTx, so
// every order ever mined on a real chain got OrderID{Height:0, Index:0}.
// Nothing in a single-block test could see that, because a single block's
// first exchange transaction always looks like index 0 regardless of which
// height supplies it. It only shows up across blocks, which is what these
// tests are shaped around.

// mineExchangeBlock builds and mines a block extending parent, computing its
// StateRoot via CandidateStateRoot -- the exact call node.go's miner makes --
// so the test can never independently drift from what AddBlock re-derives.
func mineExchangeBlock(t *testing.T, c *Chain, parent core.Block, txs []core.Transaction) core.Block {
	t.Helper()
	root, gasUsed, err := c.CandidateStateRoot(txs, testMiner, acceptAll)
	if err != nil {
		t.Fatalf("CandidateStateRoot: %v", err)
	}
	// BaseFee (M9) is derived from parent's own header, not c.NextBaseFee()
	// (which reads the CHAIN's current head) -- parent here may not be the
	// chain's canonical head (e.g. a test building an alternate branch), so
	// this must be correct regardless of what AddBlock later does with it.
	baseFee := ComputeBaseFee(parent.Header.BaseFee, parent.Header.GasUsed, GasTarget(c.GasLimit()))
	h := core.Header{
		Height:     parent.Header.Height + 1,
		PrevHash:   parent.Hash(),
		Coinbase:   testMiner,
		Difficulty: testDiff,
		BaseFee:    baseFee,
		GasUsed:    gasUsed,
	}
	b := core.Block{Header: h, Txs: txs}
	b.Header.MerkleRoot = b.TxRoot()
	b.Header.StateRoot = root
	consensus.Mine(&b.Header, 0)
	return b
}

func exOrderTx(from core.Address, nonce uint64, side orderbook.Side, price, qty int64) core.Transaction {
	return core.Transaction{
		From: from, To: exchange.Address, Nonce: nonce, ChainID: DefaultChainID,
		Data: exchange.EncodePlace(side, price, qty), Signature: []byte{1},
	}
}

func exCancelTx(from core.Address, nonce uint64, id orderbook.OrderID) core.Transaction {
	return core.Transaction{
		From: from, To: exchange.Address, Nonce: nonce, ChainID: DefaultChainID,
		Data: exchange.EncodeCancel(id), Signature: []byte{1},
	}
}

// The regression itself: two accounts each place an order as the first
// transaction of their own block. Pre-fix, "first transaction of the block"
// meant index 0 regardless of height, so both orders received the identical
// id OrderID{0,0} -- indistinguishable to Cancel. Post-fix their heights
// differ, so their ids do too, and each account can cancel only its own.
func TestOrderIdentityDoesNotCollideAcrossBlocksOnTheRealChainPath(t *testing.T) {
	alloc := map[core.Address]uint64{addr(1): 1_000_000, addr(2): 1_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0}
	gb := g.ToBlock()
	c := NewChain(gb, alloc)

	b1 := mineExchangeBlock(t, c, gb, []core.Transaction{
		exOrderTx(addr(1), 0, orderbook.Buy, 100, 10),
	})
	if err := c.AddBlock(b1, acceptAll); err != nil {
		t.Fatalf("AddBlock 1: %v", err)
	}

	b2 := mineExchangeBlock(t, c, b1, []core.Transaction{
		exOrderTx(addr(2), 0, orderbook.Buy, 90, 20),
	})
	if err := c.AddBlock(b2, acceptAll); err != nil {
		t.Fatalf("AddBlock 2: %v", err)
	}

	idA := orderbook.OrderID{Height: 1, Index: 0}
	idB := orderbook.OrderID{Height: 2, Index: 0}
	if idA == idB {
		t.Fatal("test setup is not exercising two distinct ids")
	}

	// Account 2 cancelling with account 1's id must fail before a block
	// containing it could ever be mined: CandidateStateRoot is exactly what a
	// miner calls to build a candidate block, and a rejected transaction means
	// there is no valid root to mine over, so the attempt can never reach
	// AddBlock at all. If ids had collided (both {0,0} pre-fix), this call
	// would have succeeded, because the order it found by that id would have
	// been indistinguishable from account 1's.
	_, _, err := c.CandidateStateRoot([]core.Transaction{
		exCancelTx(addr(2), 1, idA),
	}, testMiner, acceptAll)
	if err == nil {
		t.Fatal("account 2 was able to build a valid candidate cancelling account 1's order")
	}

	// Account 1 cancelling its own, correctly-identified order succeeds.
	b3 := mineExchangeBlock(t, c, b2, []core.Transaction{
		exCancelTx(addr(1), 1, idA),
	})
	if err := c.AddBlock(b3, acceptAll); err != nil {
		t.Fatalf("account 1 could not cancel its own order: %v", err)
	}

	// Account 2's order must be completely untouched by any of the above.
	locked := exchange.LockedQuoteOf(c.State(), addr(2))
	if want := int64(90 * 20); locked != uint64(want) {
		t.Fatalf("account 2's lock is %d, want %d -- it should not have moved", locked, want)
	}
}

// The mining/validation determinism contract this all rests on: the root
// CandidateStateRoot computes for a candidate block containing exchange
// orders must be the exact root AddBlock re-derives and accepts. If height
// plumbing ever drifts between the two call sites again, this fails instead
// of silently minting an unmineable chain.
func TestCandidateStateRootAgreesWithAddBlockAcrossMultipleExchangeBlocks(t *testing.T) {
	alloc := map[core.Address]uint64{addr(1): 1_000_000, addr(2): 1_000_000, addr(3): 1_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0}
	gb := g.ToBlock()
	c := NewChain(gb, alloc)

	parent := gb
	for h, txs := range [][]core.Transaction{
		{exOrderTx(addr(1), 0, orderbook.Buy, 100, 5)},
		{exOrderTx(addr(2), 0, orderbook.Buy, 101, 3), exOrderTx(addr(3), 0, orderbook.Buy, 99, 7)},
	} {
		b := mineExchangeBlock(t, c, parent, txs)
		if err := c.AddBlock(b, acceptAll); err != nil {
			t.Fatalf("block %d: AddBlock rejected a root it computed itself: %v", h+1, err)
		}
		parent = b
	}
}
