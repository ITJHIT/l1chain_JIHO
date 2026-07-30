package exchange

import (
	"errors"
	"testing"

	"github.com/ITJHIT/onchain-orderbook/orderbook"

	"l1chain/core"
	"l1chain/state"
)

func qAddr(b byte) core.Address {
	var a core.Address
	a[0] = b
	return a
}

func qOrderTx(from core.Address, nonce uint64, side orderbook.Side, price, qty int64) core.Transaction {
	return core.Transaction{From: from, To: Address, Nonce: nonce, Data: EncodePlace(side, price, qty), Signature: []byte{1}}
}

// applyForQueryTest exercises exactly the routing chain.ApplyTxAt would (sig
// already accepted by construction here, nonce check, then Apply) without
// importing the chain package, which itself imports exchange -- importing it
// back would be a cycle.
func applyForQueryTest(st state.StateDB, height uint64, txs []core.Transaction) error {
	for i, tx := range txs {
		acct := st.GetAccount(tx.From)
		if tx.Nonce != acct.Nonce {
			return errBadTestNonce
		}
		acct.Nonce++
		st.SetAccount(tx.From, acct)
		if err := Apply(st, tx, height, uint32(i)); err != nil {
			return err
		}
	}
	return nil
}

var errBadTestNonce = errors.New("bad nonce")

func TestDepthIsEmptyOnAFreshExchange(t *testing.T) {
	st := state.NewMemStateDB()
	bids, asks, err := Depth(st)
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if len(bids) != 0 || len(asks) != 0 {
		t.Fatalf("fresh exchange has depth: bids=%v asks=%v", bids, asks)
	}
	_, _, hasBid, _, _, hasAsk, err := BestBidAsk(st)
	if err != nil {
		t.Fatalf("BestBidAsk: %v", err)
	}
	if hasBid || hasAsk {
		t.Fatal("fresh exchange reports a top of book")
	}
}

// Multiple orders at the same price must aggregate into one level; orders at
// different prices must stay separate, sorted best-first per side.
func TestDepthAggregatesOrdersAtTheSamePriceAndKeepsSidesSorted(t *testing.T) {
	st := state.NewMemStateDB()
	for _, a := range []core.Address{qAddr(1), qAddr(2), qAddr(3)} {
		st.SetAccount(a, state.Account{Balance: 100_000})
	}
	CreditBase(st, qAddr(4), 1000)
	CreditBase(st, qAddr(5), 1000)

	txs := []core.Transaction{
		qOrderTx(qAddr(1), 0, orderbook.Buy, 100, 5),
		qOrderTx(qAddr(2), 0, orderbook.Buy, 100, 3), // same price as above: must aggregate to 8
		qOrderTx(qAddr(3), 0, orderbook.Buy, 99, 10), // worse price: separate level, second
		qOrderTx(qAddr(4), 0, orderbook.Sell, 105, 4),
		qOrderTx(qAddr(5), 0, orderbook.Sell, 106, 6),
	}
	if err := applyForQueryTest(st, 1, txs); err != nil {
		t.Fatalf("apply: %v", err)
	}

	bids, asks, err := Depth(st)
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if len(bids) != 2 {
		t.Fatalf("bids = %+v, want 2 levels", bids)
	}
	if bids[0].Price != 100 || bids[0].Qty != 8 {
		t.Fatalf("best bid level = %+v, want price=100 qty=8", bids[0])
	}
	if bids[1].Price != 99 || bids[1].Qty != 10 {
		t.Fatalf("second bid level = %+v, want price=99 qty=10", bids[1])
	}
	if len(asks) != 2 || asks[0].Price != 105 || asks[1].Price != 106 {
		t.Fatalf("asks = %+v, want [105, 106] best-first", asks)
	}

	bid, bidQty, hasBid, ask, askQty, hasAsk, err := BestBidAsk(st)
	if err != nil {
		t.Fatalf("BestBidAsk: %v", err)
	}
	if !hasBid || bid != 100 || bidQty != 8 {
		t.Fatalf("best bid = %d/%d hasBid=%v, want 100/8/true", bid, bidQty, hasBid)
	}
	if !hasAsk || ask != 105 || askQty != 4 {
		t.Fatalf("best ask = %d/%d hasAsk=%v, want 105/4/true", ask, askQty, hasAsk)
	}
}

func TestSnapshotReturnsIndividualOrdersWithOwners(t *testing.T) {
	st := state.NewMemStateDB()
	st.SetAccount(qAddr(1), state.Account{Balance: 100_000})

	if err := applyForQueryTest(st, 3, []core.Transaction{
		qOrderTx(qAddr(1), 0, orderbook.Buy, 50, 7),
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	views, err := Snapshot(st)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("got %d orders, want 1", len(views))
	}
	v := views[0]
	if v.Account != qAddr(1) || v.Price != 50 || v.Qty != 7 || v.Height != 3 || v.Index != 0 {
		t.Fatalf("order view = %+v", v)
	}
}

func TestLastAuctionIsUnsetUntilABatchClears(t *testing.T) {
	st := state.NewMemStateDB()
	if _, _, _, ok := LastAuction(st); ok {
		t.Fatal("fresh exchange reports a last auction")
	}
}

func TestLastAuctionPersistsAcrossTheBlockThatClearedIt(t *testing.T) {
	seller, buyer1, buyer2 := qAddr(1), qAddr(2), qAddr(3)
	st := state.NewMemStateDB()
	st.SetAccount(buyer1, state.Account{Balance: 10_000})
	st.SetAccount(buyer2, state.Account{Balance: 10_000})
	CreditBase(st, seller, 10)

	session, err := NewBatchSession(st, seller, buyer1, buyer2)
	if err != nil {
		t.Fatalf("NewBatchSession: %v", err)
	}
	for i, tx := range []core.Transaction{
		qOrderTx(seller, 0, orderbook.Sell, 100, 10),
		qOrderTx(buyer1, 0, orderbook.Buy, 100, 10),
		qOrderTx(buyer2, 0, orderbook.Buy, 100, 10),
	} {
		if err := session.Apply(42, uint32(i), tx.From, tx.Data); err != nil {
			t.Fatalf("stage %d: %v", i, err)
		}
	}
	if _, err := session.Finish(st, 42); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	price, volume, height, ok := LastAuction(st)
	if !ok {
		t.Fatal("LastAuction reports no auction after one cleared")
	}
	if price != 100 || volume != 10 || height != 42 {
		t.Fatalf("LastAuction = price=%d volume=%d height=%d, want 100/10/42", price, volume, height)
	}
}

func TestLastAuctionDoesNotUpdateWhenNothingCrosses(t *testing.T) {
	st := state.NewMemStateDB()
	st.SetAccount(qAddr(1), state.Account{Balance: 10_000})
	CreditBase(st, qAddr(2), 10)

	session, err := NewBatchSession(st, qAddr(1), qAddr(2))
	if err != nil {
		t.Fatalf("NewBatchSession: %v", err)
	}
	// A buy far below the resting ask -- funded, staged, but nothing crosses.
	if err := session.Apply(5, 0, qAddr(1), EncodePlace(orderbook.Buy, 1, 1)); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := session.Finish(st, 5); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if _, _, _, ok := LastAuction(st); ok {
		t.Fatal("LastAuction reports a clear when nothing crossed")
	}
}
