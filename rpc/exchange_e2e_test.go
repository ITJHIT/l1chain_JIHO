package rpc_test

import (
	"net/http/httptest"
	"testing"

	"github.com/ITJHIT/onchain-orderbook/orderbook"

	"l1chain/chain"
	"l1chain/core"
	"l1chain/exchange"
	"l1chain/node"
	"l1chain/rpc"
	"l1chain/wallet"
)

func placeOrderTx(from wallet.Key, nonce uint64, side orderbook.Side, price, qty int64) core.Transaction {
	tx := core.Transaction{
		To: exchange.Address, Nonce: nonce, ChainID: chain.DefaultChainID,
		Data: exchange.EncodePlace(side, price, qty),
	}
	from.Sign(&tx)
	return tx
}

// A resting order, placed and paid for through the ordinary sendRawTx path,
// is visible over RPC exactly the way it is visible to the state transition
// that created it -- same depth, same snapshot, same balances.
func TestOrderBookIsVisibleOverRPC(t *testing.T) {
	buyer, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey(buyer): %v", err)
	}
	miner, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey(miner): %v", err)
	}

	n, err := node.New(node.Config{
		MinerKey:     miner,
		Difficulty:   6,
		GenesisAlloc: map[core.Address]uint64{buyer.Address(): 100_000},
	})
	if err != nil {
		t.Fatalf("node.New: %v", err)
	}
	defer n.Close()

	srv := httptest.NewServer(rpc.NewServer(n))
	defer srv.Close()
	client := rpc.NewClient(srv.URL)

	// Before anything is placed, the book and every balance read as empty.
	depth, err := client.GetOrderBookDepth()
	if err != nil {
		t.Fatalf("GetOrderBookDepth (empty): %v", err)
	}
	if len(depth.Bids) != 0 || len(depth.Asks) != 0 {
		t.Fatalf("empty chain reports depth: %+v", depth)
	}

	tx := placeOrderTx(buyer, 0, orderbook.Buy, 150, 20)
	if _, err := client.SendRawTx(tx); err != nil {
		t.Fatalf("SendRawTx: %v", err)
	}
	if _, err := n.MineBlock(); err != nil {
		t.Fatalf("MineBlock: %v", err)
	}

	depth, err = client.GetOrderBookDepth()
	if err != nil {
		t.Fatalf("GetOrderBookDepth: %v", err)
	}
	if len(depth.Bids) != 1 || depth.Bids[0].Price != 150 || depth.Bids[0].Qty != 20 {
		t.Fatalf("depth = %+v, want one bid level 150/20", depth)
	}
	if len(depth.Asks) != 0 {
		t.Fatalf("phantom ask depth: %+v", depth.Asks)
	}

	book, err := client.GetOrderBook()
	if err != nil {
		t.Fatalf("GetOrderBook: %v", err)
	}
	if len(book) != 1 {
		t.Fatalf("book = %+v, want one resting order", book)
	}
	if book[0].Account != buyer.Address().Hex() || book[0].Side != "buy" ||
		book[0].Price != 150 || book[0].Qty != 20 {
		t.Fatalf("order view = %+v", book[0])
	}
	// height,index encode WHERE in the chain the order came from -- the
	// mined block was height 1, and it was the first (only) transaction.
	if book[0].Height != 1 || book[0].Index != 0 {
		t.Fatalf("order identity = height=%d index=%d, want 1/0", book[0].Height, book[0].Index)
	}

	bal, err := client.GetExchangeBalance(buyer.Address().Hex())
	if err != nil {
		t.Fatalf("GetExchangeBalance: %v", err)
	}
	if bal.LockedQuote != 150*20 {
		t.Fatalf("locked quote = %d, want %d", bal.LockedQuote, 150*20)
	}

	// No batch has ever run on this chain (default mode is Continuous).
	if _, found, err := client.GetLastAuction(); err != nil {
		t.Fatalf("GetLastAuction: %v", err)
	} else if found {
		t.Fatal("GetLastAuction reports a clear on a Continuous-mode chain")
	}
}

// Stated boundary rather than a silently-skipped case: getLastAuction's
// POSITIVE path -- an actual clear, read back over RPC -- is not exercised
// here. A real crossing trade needs base-asset holdings on the sell side, and
// genesis currently only funds the native/quote side (see exchange.go's
// CreditBase doc comment: it is a genesis-or-test helper because there is no
// minting transaction yet, and node.New's replay-from-genesis path -- which
// MineBlock goes through on every block -- cannot see a balance credited
// out-of-band after the chain was constructed). The mechanism itself IS
// verified, directly, in exchange/query_test.go
// (TestLastAuctionPersistsAcrossTheBlockThatClearedIt), which drives the exact
// same BatchSession.Finish -> exchange.LastAuction path this RPC method reads
// from; what is not covered is the last few lines of plumbing between that
// and an HTTP response, which is unbranching pass-through code. Genesis
// base-asset premine (parallel to the existing native Alloc, and threaded
// through the store's durable round-trip the same way) would close this gap
// and is real, separately-scoped work.
