package chain

import (
	"testing"

	"github.com/ITJHIT/onchain-orderbook/orderbook"

	"l1chain/core"
	"l1chain/exchange"
	"l1chain/state"
)

// End-to-end: an order is an ordinary signed transaction, applied by the same
// state transition that applies transfers and contract calls, and the resulting
// book is committed by the same state root that commits balances.

func exAddr(n byte) core.Address {
	var a core.Address
	a[0] = n
	return a
}

// fresh returns a state where three accounts hold native coin (the quote asset)
// and base holdings on the exchange.
func exState(t *testing.T) state.StateDB {
	t.Helper()
	st := state.NewMemStateDB()
	for i := byte(1); i <= 3; i++ {
		st.SetAccount(exAddr(i), state.Account{Balance: 1_000_000})
		exchange.CreditBase(st, exAddr(i), 10_000)
	}
	return st
}

func orderTx(from core.Address, nonce uint64, side orderbook.Side, price, qty int64) core.Transaction {
	return core.Transaction{
		From:      from,
		To:        exchange.Address,
		Nonce:     nonce,
		Data:      exchange.EncodePlace(side, price, qty),
		Signature: []byte{1}, // AcceptNonEmptySig
	}
}

func exBlock(height uint64, txs ...core.Transaction) core.Block {
	return core.Block{
		Header: core.Header{Height: height, Coinbase: exAddr(9)},
		Txs:    txs,
	}
}

func TestAnOrderIsAnOrdinaryTransaction(t *testing.T) {
	st := exState(t)

	// A resting sell, then a buy that crosses it.
	err := ApplyBlock(st, exBlock(1,
		orderTx(exAddr(1), 0, orderbook.Sell, 100, 40),
		orderTx(exAddr(2), 0, orderbook.Buy, 120, 25),
	), exAddr(9), AcceptNonEmptySig)
	if err != nil {
		t.Fatalf("block rejected: %v", err)
	}

	// The seller gave up 25 base and was paid at its own resting price of 100.
	sellerBase, sellerLocked := exchange.BaseOf(st, exAddr(1))
	if sellerBase != 10_000-25 {
		t.Fatalf("seller holds %d base, want %d", sellerBase, 10_000-25)
	}
	if sellerLocked != 15 { // 40 placed, 25 filled
		t.Fatalf("seller has %d base locked, want 15 still resting", sellerLocked)
	}
	if got := st.GetAccount(exAddr(1)).Balance; got != 1_000_000+2500 {
		t.Fatalf("seller native balance %d, want %d", got, 1_000_000+2500)
	}

	// The buyer was willing to pay 120 but paid 100; the over-lock must be gone.
	buyerBase, _ := exchange.BaseOf(st, exAddr(2))
	if buyerBase != 10_000+25 {
		t.Fatalf("buyer holds %d base, want %d", buyerBase, 10_000+25)
	}
	if got := st.GetAccount(exAddr(2)).Balance; got != 1_000_000-2500 {
		t.Fatalf("buyer native balance %d, want %d (25 @ 100, not @ 120)", got, 1_000_000-2500)
	}
	if lq := exchange.LockedQuoteOf(st, exAddr(2)); lq != 0 {
		t.Fatalf("buyer still has %d quote locked after a fully filled order", lq)
	}

	// The nonce advanced on both, so neither order can be replayed.
	if n := st.GetAccount(exAddr(1)).Nonce; n != 1 {
		t.Fatalf("seller nonce %d, want 1", n)
	}
}

// The book has to be inside the chain's commitment, not beside it. If placing an
// order does not move the state root, validators are not agreeing about the book
// at all -- they just happen to compute it the same way.
func TestTheBookIsCommittedByTheChainStateRoot(t *testing.T) {
	st := exState(t)
	before := st.StateRoot()

	if err := ApplyBlock(st, exBlock(1, orderTx(exAddr(1), 0, orderbook.Sell, 100, 10)),
		exAddr(9), AcceptNonEmptySig); err != nil {
		t.Fatalf("block rejected: %v", err)
	}
	after := st.StateRoot()
	if before == after {
		t.Fatal("resting an order did not change the state root; the book is not committed")
	}

	// Cancelling it must return the root to a state that reflects an empty book.
	if err := ApplyBlock(st, exBlock(2, core.Transaction{
		From: exAddr(1), To: exchange.Address, Nonce: 1,
		Data:      exchange.EncodeCancel(orderbook.OrderID{Height: 1, Index: 0}),
		Signature: []byte{1},
	}), exAddr(9), AcceptNonEmptySig); err != nil {
		t.Fatalf("cancel rejected: %v", err)
	}
	if _, locked := exchange.BaseOf(st, exAddr(1)); locked != 0 {
		t.Fatalf("cancel left %d base locked", locked)
	}
}

// Order identity comes from position in the chain. Two nodes that apply the same
// blocks must therefore agree on the root without exchanging anything else.
func TestTwoNodesApplyingTheSameBlocksAgree(t *testing.T) {
	blocks := []core.Block{
		exBlock(1,
			orderTx(exAddr(1), 0, orderbook.Sell, 101, 50),
			orderTx(exAddr(2), 0, orderbook.Sell, 100, 30),
			orderTx(exAddr(3), 0, orderbook.Buy, 102, 60),
		),
		exBlock(2,
			orderTx(exAddr(1), 1, orderbook.Buy, 99, 20),
			orderTx(exAddr(3), 1, orderbook.Sell, 98, 25),
		),
	}

	var first core.Hash
	for node := 0; node < 6; node++ {
		st := exState(t)
		for _, b := range blocks {
			if err := ApplyBlock(st, b, exAddr(9), AcceptNonEmptySig); err != nil {
				t.Fatalf("node %d rejected block %d: %v", node, b.Header.Height, err)
			}
		}
		root := st.StateRoot()
		if node == 0 {
			first = root
			continue
		}
		if root != first {
			t.Fatalf("node %d root %x != node 0 root %x", node, root, first)
		}
	}
}

// A block is atomic. An order that cannot be funded must take the whole block
// down rather than half-applying it, the same as any other failing transaction.
func TestAnUnfundableOrderFailsTheBlockAtomically(t *testing.T) {
	st := exState(t)
	before := st.StateRoot()

	err := ApplyBlock(st, exBlock(1,
		orderTx(exAddr(1), 0, orderbook.Sell, 100, 10),
		orderTx(exAddr(2), 0, orderbook.Sell, 100, 99_999), // far more base than held
	), exAddr(9), AcceptNonEmptySig)
	if err == nil {
		t.Fatal("block containing an unfundable order was accepted")
	}
	if st.StateRoot() != before {
		t.Fatal("a failed block mutated state; ApplyBlock is supposed to be atomic")
	}
	if _, locked := exchange.BaseOf(st, exAddr(1)); locked != 0 {
		t.Fatalf("the first order's lock survived a failed block: %d", locked)
	}
}

func TestMalformedCalldataIsRejected(t *testing.T) {
	st := exState(t)
	for name, data := range map[string][]byte{
		"empty":        {},
		"unknown op":   {0x7F},
		"short place":  {exchange.OpPlace, 0x00, 0x01},
		"short cancel": {exchange.OpCancel, 0x00},
	} {
		err := ApplyBlock(st, exBlock(1, core.Transaction{
			From: exAddr(1), To: exchange.Address, Nonce: 0,
			Data: data, Signature: []byte{1},
		}), exAddr(9), AcceptNonEmptySig)
		if err == nil {
			t.Fatalf("%s calldata was accepted", name)
		}
	}
}

// Non-exchange transactions must be untouched by the routing.
func TestPlainTransfersStillWork(t *testing.T) {
	st := exState(t)
	if err := ApplyBlock(st, exBlock(1, core.Transaction{
		From: exAddr(1), To: exAddr(2), Value: 500, Nonce: 0, Signature: []byte{1},
	}), exAddr(9), AcceptNonEmptySig); err != nil {
		t.Fatalf("plain transfer rejected: %v", err)
	}
	if got := st.GetAccount(exAddr(2)).Balance; got != 1_000_500 {
		t.Fatalf("recipient balance %d, want 1000500", got)
	}
}
