package exchange

import (
	"encoding/binary"

	obchain "github.com/ITJHIT/onchain-orderbook/chain"
	"github.com/ITJHIT/onchain-orderbook/orderbook"

	"l1chain/core"
	"l1chain/state"
)

// BatchSession accumulates one block's worth of exchange activity so it can
// clear at one uniform price instead of matching continuously.
//
// Apply cannot Load-and-Save per transaction the way Apply does, because a
// batch auction's entire point is that no single transaction in the block
// settles on its own -- every placement has to stay unmatched until every
// other placement in the same block has also been staged, or a transaction's
// position in the block would matter again through the side door of "did it
// get staged before or after the auction already cleared."
//
// Cancels are the one thing that still applies immediately, in every mode:
// a cancel is not a bid on anything, so there is no position for a block
// producer to sell by choosing when it lands.
type BatchSession struct {
	engine  *obchain.Engine
	staged  []orderbook.Order
	touched bool
}

// NewBatchSession loads the book once for the whole block. senders should
// list every address that will place or cancel this block -- the same role
// Apply's single `tx.From` argument plays for one transaction -- so their
// balances are visible before the first Apply call needs them.
func NewBatchSession(st state.StateDB, senders ...core.Address) (*BatchSession, error) {
	e, err := Load(st, BatchAuction, senders...)
	if err != nil {
		return nil, err
	}
	return &BatchSession{engine: e}, nil
}

// Apply decodes and applies one exchange transaction within the session: a
// cancel takes effect now, a placement locks its funds now and is queued for
// the auction Finish runs once, at the end of the block.
func (s *BatchSession) Apply(height uint64, index uint32, from core.Address, data []byte) error {
	op, err := Decode(from, data)
	if err != nil {
		return err
	}
	s.touched = true

	if op.Kind == obchain.TxCancel {
		res := s.engine.ApplyPositioned(height, index, op)
		if !res.Accepted {
			if res.Err != nil {
				return res.Err
			}
			return ErrRejected
		}
		return nil
	}

	res, o := s.engine.StagePlace(height, index, op)
	if !res.Accepted || o == nil {
		if res.Err != nil {
			return res.Err
		}
		return ErrRejected
	}
	s.staged = append(s.staged, *o)
	return nil
}

// Finish clears whatever was staged, settles the resulting fills, and writes
// the book and every touched balance back to st. Safe to call on a session
// that never saw an exchange transaction: it is a genuine no-op then, not an
// error, because a caller that creates a session up front for every block
// (rather than only when it already knows one is needed) should not have to
// special-case the empty case itself.
//
// When the auction actually clears, the price, volume and the height it
// cleared at are also written to durable storage under nsLastAuction --
// otherwise the ONLY place this result would ever have existed is the return
// value handed to whoever mined or validated that one block, gone the moment
// they moved on. LastAuction lets anyone (an RPC caller, an explorer) ask "what
// did this market clear at" without having replayed the block that answered it.
func (s *BatchSession) Finish(st state.StateDB, height uint64) (obchain.AuctionSummary, error) {
	if !s.touched {
		return obchain.AuctionSummary{}, nil
	}
	summary, err := s.engine.ClearBatch(s.staged)
	if err != nil {
		return obchain.AuctionSummary{}, err
	}
	if err := Save(st, s.engine); err != nil {
		return obchain.AuctionSummary{}, err
	}
	if summary.Cleared {
		writeLastAuction(st, summary.Price, summary.Volume, height)
	}
	return summary, nil
}

func writeLastAuction(st state.StateDB, price orderbook.Price, volume orderbook.Qty, height uint64) {
	var a, b core.Hash
	binary.BigEndian.PutUint64(a[0:8], uint64(price))
	binary.BigEndian.PutUint64(a[8:16], uint64(volume))
	binary.BigEndian.PutUint64(b[24:32], height)
	st.SetStorage(Address, indexKey(nsLastAuction, 0), a)
	st.SetStorage(Address, indexKey(nsLastAuction, 1), b)
}

// LastAuction returns the most recent price and volume a batch auction
// cleared at, and the height it cleared at. ok is false if no auction has ever
// cleared on this chain (a fresh chain, or one that has only ever run
// Continuous mode).
func LastAuction(st state.StateDB) (price orderbook.Price, volume orderbook.Qty, height uint64, ok bool) {
	a := st.GetStorage(Address, indexKey(nsLastAuction, 0))
	b := st.GetStorage(Address, indexKey(nsLastAuction, 1))
	if a.IsZero() && b.IsZero() {
		return 0, 0, 0, false
	}
	price = orderbook.Price(binary.BigEndian.Uint64(a[0:8]))
	volume = orderbook.Qty(binary.BigEndian.Uint64(a[8:16]))
	height = binary.BigEndian.Uint64(b[24:32])
	return price, volume, height, true
}
