package chain

import (
	"sort"

	"l1chain/core"
	"l1chain/exchange"
	"l1chain/pos"
	"l1chain/state"
)

// isFeePriced reports whether tx is routed through one of the fee-market-
// priced paths (StackVM contract or embedded EVM) rather than one of the
// fee-exempt shapes (plain transfer, exchange order, PoS attestation) --
// mirrors chain/transition.go's applyTxAtSession dispatch order exactly.
// Exchange/attestation must be excluded BEFORE consulting isContractTx: an
// exchange order carries non-empty Data (its encoded order), which would
// otherwise satisfy isContractTx's own "any Data-carrying tx" catch-all.
func isFeePriced(st state.StateDB, tx core.Transaction) bool {
	if exchange.IsExchangeTx(tx) || pos.IsAttestationTx(tx) {
		return false
	}
	return isEVMTx(st, tx) || isContractTx(st, tx)
}

// BuildBlockTxs selects which pending mempool transactions to include in the
// next block. Every plain-transfer/exchange/attestation transaction is
// included unconditionally (unbounded, exactly as every block before this
// method existed -- a named, pre-existing limitation this method does not
// change). Fee-priced (contract/EVM) transactions are greedily selected by
// highest effective priority fee at the chain's current NextBaseFee(),
// bounded by GasLimit(), always respecting each sender's own nonce order: a
// sender's queue is only ever advanced from the front, never skipped into,
// so a fee-priced transaction that is priced out (GasFeeCap below BaseFee)
// or doesn't fit the remaining gas budget blocks every LATER transaction
// from that same sender in this block -- including any free-pass ones after
// it -- exactly like a real transaction pool must (skipping ahead would
// make the resulting block fail AddBlock's own ErrBadNonce check).
//
// The gas budget is enforced against each candidate's DECLARED GasLimit (a
// conservative upper bound on what it might consume), not its actual usage,
// which is only known after execution -- the same simplification real block
// builders (including go-ethereum's own transaction pool) use.
func (c *Chain) BuildBlockTxs(mempool []core.Transaction) []core.Transaction {
	st := c.State()
	baseFee := c.NextBaseFee()

	bySender := make(map[core.Address][]core.Transaction)
	for _, tx := range mempool {
		bySender[tx.From] = append(bySender[tx.From], tx)
	}
	senders := make([]core.Address, 0, len(bySender))
	for addr, txs := range bySender {
		sort.Slice(txs, func(i, j int) bool { return txs[i].Nonce < txs[j].Nonce })
		senders = append(senders, addr)
	}
	// Deterministic evaluation order -- Go map iteration is randomized, and
	// while BuildBlockTxs's own choices aren't re-validated by AddBlock (a
	// validator only checks the RESULTING block, not how it was built),
	// non-determinism here would make this method's own tests flaky.
	sort.Slice(senders, func(i, j int) bool { return senders[i].Hex() < senders[j].Hex() })

	queuePos := make(map[core.Address]int, len(senders))
	active := make(map[core.Address]bool, len(senders))
	for _, addr := range senders {
		active[addr] = true
	}

	var included []core.Transaction
	var gasBudget uint64

	for len(active) > 0 {
		// Drain free-pass (non-fee-priced) transactions from the front of
		// every active sender's queue -- unconditional, no ordering/budget
		// constraint.
		for _, addr := range senders {
			if !active[addr] {
				continue
			}
			queue := bySender[addr]
			i := queuePos[addr]
			for i < len(queue) && !isFeePriced(st, queue[i]) {
				included = append(included, queue[i])
				i++
			}
			queuePos[addr] = i
			if i >= len(queue) {
				active[addr] = false
			}
		}

		// Collect one candidate per still-active sender: their current
		// front, now guaranteed fee-priced.
		type candidate struct {
			addr        core.Address
			tx          core.Transaction
			priorityFee uint64
		}
		var candidates []candidate
		for _, addr := range senders {
			if !active[addr] {
				continue
			}
			tx := bySender[addr][queuePos[addr]]
			if tx.GasFeeCap < baseFee {
				// Not includable at the current price -- nonce order
				// forbids skipping to a later, cheaper transaction from
				// this same sender, so they're done for this block.
				active[addr] = false
				continue
			}
			_, priorityFee, _ := EffectiveGasPrice(baseFee, tx.GasFeeCap, tx.GasTipCap)
			candidates = append(candidates, candidate{addr, tx, priorityFee})
		}
		if len(candidates) == 0 {
			continue
		}

		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].priorityFee != candidates[j].priorityFee {
				return candidates[i].priorityFee > candidates[j].priorityFee
			}
			return candidates[i].addr.Hex() < candidates[j].addr.Hex() // deterministic tie-break
		})
		winner := candidates[0]

		if gasBudget+winner.tx.GasLimit > c.gasLimit {
			// Doesn't fit -- skipping to a smaller transaction from this
			// sender would violate nonce order, so they're done too.
			active[winner.addr] = false
			continue
		}

		included = append(included, winner.tx)
		gasBudget += winner.tx.GasLimit
		queuePos[winner.addr]++
		if queuePos[winner.addr] >= len(bySender[winner.addr]) {
			active[winner.addr] = false
		}
	}

	return included
}
