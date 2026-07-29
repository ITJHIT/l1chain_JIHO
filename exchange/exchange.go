// Package exchange puts a limit order book inside the chain's state transition.
//
// The book itself lives in github.com/ITJHIT/onchain-orderbook, which is written
// to be deterministic and is tested there against eight independent engines, a
// replay from genesis, and a demonstration of the map-iteration hazard it avoids.
// This package is the seam: it makes that engine reachable from an ordinary
// signed transaction, and stores its state where the chain's root already
// commits to it.
//
// Design, and why it is this shape:
//
//   - **Orders are ordinary transactions.** A placement is a normal
//     core.Transaction with To == Address and the order encoded in Data. Nothing
//     about signing, nonce ordering, the mempool or gossip changes, and the book
//     inherits replay protection and exact-nonce sequencing for free. This is
//     what a precompile or a Cosmos module is: a reserved address the state
//     transition routes to instead of the VM.
//   - **Order identity comes from position in the chain**, (height, index within
//     block), never from a local counter. Two validators therefore assign the
//     same identifiers without coordinating, and a replay from genesis
//     reproduces them exactly.
//   - **Book state lives in the exchange account's storage**, so StateRoot folds
//     it automatically -- it already sorts storage keys. Committing the book by
//     inventing a second root would have meant a second thing that can disagree.
//   - **The native coin is the quote asset.** Balances that already exist are
//     reused rather than shadowed, so a trade moves real chain balance, not a
//     parallel ledger that has to be reconciled.
//
// Honest limitation: Load/Save reads and rewrites the whole book per exchange
// transaction, which is O(resting orders) rather than O(touched orders). It is
// correct and deterministic, which is what this milestone is about; a production
// implementation would key each order into its own storage slot and touch only
// what it changes.
package exchange

import (
	"encoding/binary"
	"errors"
	"math"

	obchain "github.com/ITJHIT/onchain-orderbook/chain"
	"github.com/ITJHIT/onchain-orderbook/orderbook"

	"l1chain/core"
	"l1chain/state"
)

// Address is the reserved account the exchange lives at. Transactions sent here
// are routed to the book instead of the VM.
var Address = core.Address{
	0xEC, 0x0B, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
}

// Op codes carried in the first byte of Transaction.Data.
const (
	OpPlace  byte = 1
	OpCancel byte = 2
)

const (
	sideBuy  byte = 0
	sideSell byte = 1
)

var (
	ErrBadCalldata   = errors.New("exchange: malformed calldata")
	ErrUnknownOp     = errors.New("exchange: unknown opcode")
	ErrRejected      = errors.New("exchange: order rejected")
	ErrBalanceRange  = errors.New("exchange: balance outside signed range")
	ErrNotConfigured = errors.New("exchange: base asset not credited")
)

// EncodePlace builds the calldata for a limit order.
//
//	op(1) || side(1) || price(8, big-endian) || qty(8, big-endian)
func EncodePlace(side orderbook.Side, price orderbook.Price, qty orderbook.Qty) []byte {
	out := make([]byte, 18)
	out[0] = OpPlace
	if side == orderbook.Sell {
		out[1] = sideSell
	}
	binary.BigEndian.PutUint64(out[2:10], uint64(price))
	binary.BigEndian.PutUint64(out[10:18], uint64(qty))
	return out
}

// EncodeCancel builds the calldata to cancel a resting order.
//
//	op(1) || height(8) || index(4)
func EncodeCancel(id orderbook.OrderID) []byte {
	out := make([]byte, 13)
	out[0] = OpCancel
	binary.BigEndian.PutUint64(out[1:9], id.Height)
	binary.BigEndian.PutUint32(out[9:13], id.Index)
	return out
}

// Decode turns calldata into an engine transaction for `from`.
func Decode(from core.Address, data []byte) (obchain.Tx, error) {
	if len(data) == 0 {
		return obchain.Tx{}, ErrBadCalldata
	}
	var acct orderbook.AccountID
	copy(acct[:], from[:])

	switch data[0] {
	case OpPlace:
		if len(data) != 18 {
			return obchain.Tx{}, ErrBadCalldata
		}
		side := orderbook.Buy
		if data[1] == sideSell {
			side = orderbook.Sell
		} else if data[1] != sideBuy {
			return obchain.Tx{}, ErrBadCalldata
		}
		return obchain.Tx{
			Kind:    obchain.TxPlace,
			Account: acct,
			Side:    side,
			Price:   orderbook.Price(binary.BigEndian.Uint64(data[2:10])),
			Qty:     orderbook.Qty(binary.BigEndian.Uint64(data[10:18])),
		}, nil
	case OpCancel:
		if len(data) != 13 {
			return obchain.Tx{}, ErrBadCalldata
		}
		return obchain.Tx{
			Kind:    obchain.TxCancel,
			Account: acct,
			Cancel: orderbook.OrderID{
				Height: binary.BigEndian.Uint64(data[1:9]),
				Index:  binary.BigEndian.Uint32(data[9:13]),
			},
		}, nil
	default:
		return obchain.Tx{}, ErrUnknownOp
	}
}

// IsExchangeTx reports whether a transaction is addressed to the book.
func IsExchangeTx(tx core.Transaction) bool { return tx.To == Address }

// ---------------------------------------------------------------------------
// Storage layout
//
// Every key is a 32-byte word tagged in byte 0 so the namespaces cannot collide:
//
//	ns 0x01  holdings   key[12:32]=address   val: base(8) lockedBase(8) lockedQuote(8) pad(8)
//	ns 0x02  book size  key[24:32]=0         val: count(8) in the last 8 bytes
//	ns 0x03  order head key[24:32]=i         val: account(20) height(8) index(4)
//	ns 0x04  order tail key[24:32]=i         val: side(1) pad(7) price(8) qty(8) pad(8)
//
// Quote balances are deliberately absent: they are the native coin, read and
// written through Account.Balance.
// ---------------------------------------------------------------------------

const (
	nsHoldings byte = 0x01
	nsBookSize byte = 0x02
	nsOrderA   byte = 0x03
	nsOrderB   byte = 0x04
)

func holdingsKey(addr core.Address) core.Hash {
	var k core.Hash
	k[0] = nsHoldings
	copy(k[12:32], addr[:])
	return k
}

func indexKey(ns byte, i uint64) core.Hash {
	var k core.Hash
	k[0] = ns
	binary.BigEndian.PutUint64(k[24:32], i)
	return k
}

func toAccount(a core.Address) orderbook.AccountID {
	var id orderbook.AccountID
	copy(id[:], a[:])
	return id
}

func toAddress(id orderbook.AccountID) core.Address {
	var a core.Address
	copy(a[:], id[:])
	return a
}

func i64(u uint64) (int64, error) {
	if u > math.MaxInt64 {
		return 0, ErrBalanceRange
	}
	return int64(u), nil
}

// Load reconstructs the engine from chain state.
//
// `extra` names addresses that must be present even if they have nothing resting
// -- the sender of the current transaction, which needs its balance visible to
// the funds check. Storage is not enumerable through StateDB, so the account set
// is (addresses on the book) plus (addresses named here); anyone else has no
// resting order and no lock, and reading them would change nothing.
func Load(st state.StateDB, mode obchain.Mode, extra ...core.Address) (*obchain.Engine, error) {
	e := obchain.NewEngine(mode)

	sizeWord := st.GetStorage(Address, indexKey(nsBookSize, 0))
	count := binary.BigEndian.Uint64(sizeWord[24:32])
	orders := make([]orderbook.Order, 0, count)
	addrs := make([]core.Address, 0, count+uint64(len(extra)))
	seen := make(map[core.Address]bool)

	for i := uint64(0); i < count; i++ {
		a := st.GetStorage(Address, indexKey(nsOrderA, i))
		b := st.GetStorage(Address, indexKey(nsOrderB, i))
		var o orderbook.Order
		copy(o.Account[:], a[0:20])
		o.ID.Height = binary.BigEndian.Uint64(a[20:28])
		o.ID.Index = binary.BigEndian.Uint32(a[28:32])
		if b[0] == sideSell {
			o.Side = orderbook.Sell
		}
		o.Price = orderbook.Price(binary.BigEndian.Uint64(b[8:16]))
		o.Qty = orderbook.Qty(binary.BigEndian.Uint64(b[16:24]))
		orders = append(orders, o)

		addr := toAddress(o.Account)
		if !seen[addr] {
			seen[addr] = true
			addrs = append(addrs, addr)
		}
	}
	for _, a := range extra {
		if !seen[a] {
			seen[a] = true
			addrs = append(addrs, a)
		}
	}

	// Credit balances before replaying the book, so that re-resting the orders
	// re-establishes exactly the locks they held.
	for _, addr := range addrs {
		h := st.GetStorage(Address, holdingsKey(addr))
		base, err := i64(binary.BigEndian.Uint64(h[0:8]))
		if err != nil {
			return nil, err
		}
		quote, err := i64(st.GetAccount(addr).Balance)
		if err != nil {
			return nil, err
		}
		e.State.Credit(toAccount(addr), base, quote)
	}

	// Replaying through Place re-derives every lock from the orders themselves,
	// so locks are never stored and so can never disagree with the book.
	if err := e.Restore(orders); err != nil {
		return nil, err
	}
	return e, nil
}

// Save writes the engine back to chain state.
func Save(st state.StateDB, e *obchain.Engine) error {
	prevWord := st.GetStorage(Address, indexKey(nsBookSize, 0))
	prev := binary.BigEndian.Uint64(prevWord[24:32])

	snapshot := e.Book.Snapshot()
	for i, o := range snapshot {
		var a, b core.Hash
		copy(a[0:20], o.Account[:])
		binary.BigEndian.PutUint64(a[20:28], o.ID.Height)
		binary.BigEndian.PutUint32(a[28:32], o.ID.Index)
		if o.Side == orderbook.Sell {
			b[0] = sideSell
		}
		binary.BigEndian.PutUint64(b[8:16], uint64(o.Price))
		binary.BigEndian.PutUint64(b[16:24], uint64(o.Qty))
		st.SetStorage(Address, indexKey(nsOrderA, uint64(i)), a)
		st.SetStorage(Address, indexKey(nsOrderB, uint64(i)), b)
	}
	// Clear slots the book shrank out of. Leaving them would not affect the book
	// -- nothing reads past the count -- but it would leave them in the state
	// root, so two nodes that reached the same book by different paths would
	// commit to different roots.
	for i := uint64(len(snapshot)); i < prev; i++ {
		st.SetStorage(Address, indexKey(nsOrderA, i), core.Hash{})
		st.SetStorage(Address, indexKey(nsOrderB, i), core.Hash{})
	}

	var size core.Hash
	binary.BigEndian.PutUint64(size[24:32], uint64(len(snapshot)))
	st.SetStorage(Address, indexKey(nsBookSize, 0), size)

	for id, bal := range e.State.Snapshot() {
		addr := toAddress(id)
		if bal.Quote < 0 {
			return ErrBalanceRange
		}
		acct := st.GetAccount(addr)
		acct.Balance = uint64(bal.Quote)
		st.SetAccount(addr, acct)

		var h core.Hash
		if bal.Base < 0 || bal.LockedBase < 0 || bal.LockedQuote < 0 {
			return ErrBalanceRange
		}
		binary.BigEndian.PutUint64(h[0:8], uint64(bal.Base))
		binary.BigEndian.PutUint64(h[8:16], uint64(bal.LockedBase))
		binary.BigEndian.PutUint64(h[16:24], uint64(bal.LockedQuote))
		st.SetStorage(Address, holdingsKey(addr), h)
	}
	return nil
}

// CreditBase gives an address a base-asset balance. Genesis and tests use it;
// a real deployment would mint on a bridge deposit.
func CreditBase(st state.StateDB, addr core.Address, amount uint64) {
	k := holdingsKey(addr)
	h := st.GetStorage(Address, k)
	binary.BigEndian.PutUint64(h[0:8], binary.BigEndian.Uint64(h[0:8])+amount)
	st.SetStorage(Address, k, h)
}

// BaseOf returns an address's base holdings and the part locked in resting orders.
func BaseOf(st state.StateDB, addr core.Address) (total, locked uint64) {
	h := st.GetStorage(Address, holdingsKey(addr))
	return binary.BigEndian.Uint64(h[0:8]), binary.BigEndian.Uint64(h[8:16])
}

// LockedQuoteOf returns the native-coin balance an address has committed to
// resting buy orders.
func LockedQuoteOf(st state.StateDB, addr core.Address) uint64 {
	h := st.GetStorage(Address, holdingsKey(addr))
	return binary.BigEndian.Uint64(h[16:24])
}

// Apply routes one exchange transaction: decode, load, apply at this position in
// the chain, store back.
//
// A rejection is returned as an error so the caller decides the policy. The
// chain's ApplyTx treats it the way it treats any failed transaction, which
// keeps "what happens to a bad order" a property of the chain rather than a
// second, quieter rule living here.
func Apply(st state.StateDB, tx core.Transaction, height uint64, index uint32) error {
	op, err := Decode(tx.From, tx.Data)
	if err != nil {
		return err
	}
	e, err := Load(st, obchain.Continuous, tx.From)
	if err != nil {
		return err
	}
	res := e.ApplyPositioned(height, index, op)
	if !res.Accepted {
		if res.Err != nil {
			return res.Err
		}
		return ErrRejected
	}
	return Save(st, e)
}
