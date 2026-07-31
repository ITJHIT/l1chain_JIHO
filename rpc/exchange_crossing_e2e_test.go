package rpc_test

import (
	"net/http/httptest"
	"testing"

	"github.com/ITJHIT/onchain-orderbook/orderbook"

	"l1chain/core"
	"l1chain/exchange"
	"l1chain/node"
	"l1chain/rpc"
	"l1chain/wallet"
)

// TestRealCrossingTradeOverRPC closes the gap rpc/exchange_e2e_test.go's own
// comment flags: genesis base-asset premine (GenesisBaseAlloc) funds a
// resting sell order on a real node -- the first time a crossing trade has
// ever run through node.New -> SendRawTx (RPC) -> MineBlock rather than a
// hand-built state.StateDB fed directly into ApplyBlockWithMode/CandidateStateRoot.
func TestRealCrossingTradeOverRPC(t *testing.T) {
	seller, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey(seller): %v", err)
	}
	buyer, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey(buyer): %v", err)
	}
	miner, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey(miner): %v", err)
	}

	n, err := node.New(node.Config{
		MinerKey:         miner,
		Difficulty:       6,
		GenesisAlloc:     map[core.Address]uint64{buyer.Address(): 100_000},
		GenesisBaseAlloc: map[core.Address]uint64{seller.Address(): 10},
	})
	if err != nil {
		t.Fatalf("node.New: %v", err)
	}
	defer n.Close()

	// Must be set before mining -- see chain.Chain.SetExchangeMode.
	n.SetExchangeMode(exchange.BatchAuction)

	srv := httptest.NewServer(rpc.NewServer(n))
	defer srv.Close()
	client := rpc.NewClient(srv.URL)

	sellTx := placeOrderTx(seller, 0, orderbook.Sell, 100, 10)
	if _, err := client.SendRawTx(sellTx); err != nil {
		t.Fatalf("SendRawTx(sell): %v", err)
	}
	buyTx := placeOrderTx(buyer, 0, orderbook.Buy, 100, 10)
	if _, err := client.SendRawTx(buyTx); err != nil {
		t.Fatalf("SendRawTx(buy): %v", err)
	}
	if _, err := n.MineBlock(); err != nil {
		t.Fatalf("MineBlock: %v", err)
	}

	// The positive path rpc/exchange_e2e_test.go's comment flags as
	// unexercised: a real clear, read back over RPC.
	auction, found, err := client.GetLastAuction()
	if err != nil {
		t.Fatalf("GetLastAuction: %v", err)
	}
	if !found {
		t.Fatal("GetLastAuction reports no clear after a crossing trade")
	}
	if auction.Price != 100 || auction.Volume != 10 || auction.Height != 1 {
		t.Fatalf("auction = %+v, want price=100 volume=10 height=1", auction)
	}

	sellerBal, err := client.GetExchangeBalance(seller.Address().Hex())
	if err != nil {
		t.Fatalf("GetExchangeBalance(seller): %v", err)
	}
	if sellerBal.Base != 0 || sellerBal.LockedBase != 0 {
		t.Fatalf("seller exchange balance = %+v, want base=0 lockedBase=0 (fully sold)", sellerBal)
	}
	sellerQuote, err := client.GetBalance(seller.Address().Hex())
	if err != nil {
		t.Fatalf("GetBalance(seller): %v", err)
	}
	if sellerQuote != 100*10 {
		t.Fatalf("seller native balance = %d, want %d (price*volume received on fill)", sellerQuote, 100*10)
	}

	buyerBal, err := client.GetExchangeBalance(buyer.Address().Hex())
	if err != nil {
		t.Fatalf("GetExchangeBalance(buyer): %v", err)
	}
	if buyerBal.Base != 10 || buyerBal.LockedBase != 0 {
		t.Fatalf("buyer exchange balance = %+v, want base=10 lockedBase=0 (fully filled)", buyerBal)
	}
	if buyerBal.LockedQuote != 0 {
		t.Fatalf("buyer locked quote = %d, want 0 (nothing left resting)", buyerBal.LockedQuote)
	}
	buyerQuote, err := client.GetBalance(buyer.Address().Hex())
	if err != nil {
		t.Fatalf("GetBalance(buyer): %v", err)
	}
	if buyerQuote != 100_000-100*10 {
		t.Fatalf("buyer native balance = %d, want %d (price*volume spent on fill)", buyerQuote, 100_000-100*10)
	}

	depth, err := client.GetOrderBookDepth()
	if err != nil {
		t.Fatalf("GetOrderBookDepth: %v", err)
	}
	if len(depth.Bids) != 0 || len(depth.Asks) != 0 {
		t.Fatalf("depth after full clear = %+v, want flat book", depth)
	}
}
