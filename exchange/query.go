package exchange

import (
	"github.com/ITJHIT/onchain-orderbook/orderbook"

	"l1chain/core"
	"l1chain/state"
)

// Level is one aggregated price point, the shape an explorer's depth chart or
// an RPC caller's order-book widget wants: total resting quantity at a price,
// not the individual orders making it up.
type Level struct {
	Price orderbook.Price
	Qty   orderbook.Qty
}

// OrderView is one resting order, addressed rather than aggregated -- what an
// explorer's order-by-order table wants, as opposed to Depth's price-level view.
type OrderView struct {
	Height  uint64
	Index   uint32
	Account core.Address
	Side    orderbook.Side
	Price   orderbook.Price
	Qty     orderbook.Qty
}

// snapshot is the shared read path for Snapshot/Depth: load the book read-only
// and hand back its resting orders in canonical order (bids best-first, then
// asks best-first). The mode passed to Load does not matter for a read -- it
// only ever affects how NEW orders would be matched, not what is already on
// disk -- so Continuous is used as an arbitrary placeholder.
func snapshot(st state.StateDB) ([]orderbook.Order, error) {
	e, err := Load(st, Continuous)
	if err != nil {
		return nil, err
	}
	return e.Book.Snapshot(), nil
}

// Snapshot returns every resting order.
func Snapshot(st state.StateDB) ([]OrderView, error) {
	orders, err := snapshot(st)
	if err != nil {
		return nil, err
	}
	out := make([]OrderView, len(orders))
	for i, o := range orders {
		out[i] = OrderView{
			Height: o.ID.Height, Index: o.ID.Index, Account: toAddress(o.Account),
			Side: o.Side, Price: o.Price, Qty: o.Qty,
		}
	}
	return out, nil
}

// Depth aggregates resting orders into price levels, bids best-first then asks
// best-first.
//
// This does not need a new export on orderbook.Book to compute: Snapshot's
// contract is that it returns orders grouped by level in canonical order, bids
// section then asks section, so a single linear pass grouping consecutive
// (side, price) runs reconstructs the levels exactly -- no second query, no
// dependency on anything not already public.
func Depth(st state.StateDB) (bids, asks []Level, err error) {
	orders, err := snapshot(st)
	if err != nil {
		return nil, nil, err
	}
	for _, o := range orders {
		levels := &bids
		if o.Side == orderbook.Sell {
			levels = &asks
		}
		if n := len(*levels); n > 0 && (*levels)[n-1].Price == o.Price {
			(*levels)[n-1].Qty += o.Qty
			continue
		}
		*levels = append(*levels, Level{Price: o.Price, Qty: o.Qty})
	}
	return bids, asks, nil
}

// BestBidAsk returns the top of book. hasBid/hasAsk are false when that side is
// empty.
func BestBidAsk(st state.StateDB) (bid orderbook.Price, bidQty orderbook.Qty, hasBid bool,
	ask orderbook.Price, askQty orderbook.Qty, hasAsk bool, err error) {
	bids, asks, err := Depth(st)
	if err != nil {
		return 0, 0, false, 0, 0, false, err
	}
	if len(bids) > 0 {
		bid, bidQty, hasBid = bids[0].Price, bids[0].Qty, true
	}
	if len(asks) > 0 {
		ask, askQty, hasAsk = asks[0].Price, asks[0].Qty, true
	}
	return
}
