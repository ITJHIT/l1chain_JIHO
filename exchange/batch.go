package exchange

import (
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
func (s *BatchSession) Finish(st state.StateDB) (obchain.AuctionSummary, error) {
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
	return summary, nil
}
