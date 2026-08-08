package chain

import (
	"testing"

	"l1chain/consensus"
	"l1chain/core"
)

// TestFeeSplitBurnsBaseCreditsTipToCoinbase is PR8's own required test (e)
// (non-negotiable per the M9 plan): a real block, applied through the real
// consensus path (CandidateStateRoot to build, AddBlock to validate),
// balance-conservation-proves the burn/tip split -- computed via
// chain.EffectiveGasPrice directly (the same function production code
// uses), not a second, independent re-derivation of the formula.
func TestFeeSplitBurnsBaseCreditsTipToCoinbase(t *testing.T) {
	sender := addr(1)
	coinbase := addr(9)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0, InitialBaseFee: 100}
	gb := g.ToBlock()
	c := NewChain(gb, alloc)

	// GasFeeCap and GasTipCap are deliberately distinct so burn and tip are
	// both nonzero and individually distinguishable in the assertions below.
	tx := core.Transaction{From: sender, To: core.Address{}, Nonce: 0, GasLimit: 40_000, ChainID: DefaultChainID, GasFeeCap: 150, GasTipCap: 20, Data: counterCode, Signature: []byte{1}}

	totalBefore := c.State().GetAccount(sender).Balance // the only funded account

	root, gasUsed, err := c.CandidateStateRoot([]core.Transaction{tx}, coinbase, acceptAll)
	if err != nil {
		t.Fatalf("CandidateStateRoot: %v", err)
	}
	h := core.Header{
		Height: 1, PrevHash: gb.Hash(), Coinbase: coinbase, Difficulty: testDiff,
		StateRoot: root, BaseFee: c.NextBaseFee(), GasUsed: gasUsed,
	}
	b := core.Block{Header: h, Txs: []core.Transaction{tx}}
	b.Header.MerkleRoot = b.TxRoot()
	consensus.Mine(&b.Header, 0)
	if err := c.AddBlock(b, acceptAll); err != nil {
		t.Fatalf("AddBlock: %v", err)
	}

	baseFee := b.Header.BaseFee
	effectivePrice, priorityFee, err := EffectiveGasPrice(baseFee, tx.GasFeeCap, tx.GasTipCap)
	if err != nil {
		t.Fatalf("EffectiveGasPrice: %v", err)
	}
	if priorityFee == 0 {
		t.Fatal("test setup: priorityFee must be nonzero to distinguish tip from burn")
	}
	burn := gasUsed * baseFee
	if burn == 0 {
		t.Fatal("test setup: burn must be nonzero to be worth proving")
	}

	wantSenderBalance := totalBefore - gasUsed*effectivePrice
	if got := c.State().GetAccount(sender).Balance; got != wantSenderBalance {
		t.Fatalf("sender balance = %d, want %d (debited exactly gasUsed*effectivePrice, i.e. the GasFeeCap*GasLimit reservation correctly refunded)", got, wantSenderBalance)
	}

	wantCoinbaseBalance := BlockReward + gasUsed*priorityFee
	if got := c.State().GetAccount(coinbase).Balance; got != wantCoinbaseBalance {
		t.Fatalf("coinbase balance = %d, want %d (BlockReward + tip)", got, wantCoinbaseBalance)
	}

	// Total supply (sender + coinbase are the only two accounts that ever
	// hold value in this test) after the block must equal before + the new
	// BlockReward mint - the burned portion -- proving the burn genuinely
	// leaves the system, credited to nobody, not merely "not credited to
	// the sender or coinbase specifically."
	totalAfter := c.State().GetAccount(sender).Balance + c.State().GetAccount(coinbase).Balance
	wantTotalAfter := totalBefore + BlockReward - burn
	if totalAfter != wantTotalAfter {
		t.Fatalf("total supply after = %d, want %d (before %d + BlockReward %d - burn %d)", totalAfter, wantTotalAfter, totalBefore, BlockReward, burn)
	}
}

// TestBlockBuilderPrefersHigherTipRespectsNonceOrder is PR8's own required
// proof that the new mechanism actually works: given two independent
// senders' fee-priced transactions, a tight gas budget that fits only one
// must select the higher-tip one; a wider budget that fits both must
// include both.
func TestBlockBuilderPrefersHigherTipRespectsNonceOrder(t *testing.T) {
	senderA := addr(1)
	senderB := addr(2)
	alloc := map[core.Address]uint64{senderA: 10_000_000, senderB: 10_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0} // BaseFee stays 0
	gb := g.ToBlock()
	c := NewChain(gb, alloc)
	c.SetGasLimit(40_000) // room for exactly one 40_000-limit tx

	txA := core.Transaction{From: senderA, To: core.Address{}, Nonce: 0, GasLimit: 40_000, ChainID: DefaultChainID, GasFeeCap: 10, GasTipCap: 1, Data: counterCode, Signature: []byte{1}}
	txB := core.Transaction{From: senderB, To: core.Address{}, Nonce: 0, GasLimit: 40_000, ChainID: DefaultChainID, GasFeeCap: 10, GasTipCap: 5, Data: counterCode, Signature: []byte{1}}

	got := c.BuildBlockTxs([]core.Transaction{txA, txB})
	if len(got) != 1 {
		t.Fatalf("BuildBlockTxs (only room for one) = %d txs, want 1", len(got))
	}
	if got[0].From != senderB {
		t.Fatalf("BuildBlockTxs picked sender %x, want the higher-tip sender %x", got[0].From, senderB)
	}

	c.SetGasLimit(100_000) // room for both now
	got = c.BuildBlockTxs([]core.Transaction{txA, txB})
	if len(got) != 2 {
		t.Fatalf("BuildBlockTxs (room for both) = %d txs, want 2", len(got))
	}
}

// TestBlockBuilderNonceOrderBlocksLaterFreePassTx proves the nonce-safety
// property directly: a sender's fee-priced transaction at nonce 0 that
// doesn't fit the gas budget must block their OWN later plain-transfer
// transaction at nonce 1 too -- including the nonce-1 transaction while
// skipping nonce 0 would make the resulting block fail AddBlock's own
// ErrBadNonce check.
func TestBlockBuilderNonceOrderBlocksLaterFreePassTx(t *testing.T) {
	sender := addr(1)
	recipient := addr(2)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0}
	gb := g.ToBlock()
	c := NewChain(gb, alloc)
	c.SetGasLimit(1) // too small for any fee-priced transaction to ever fit

	feePriced := core.Transaction{From: sender, To: core.Address{}, Nonce: 0, GasLimit: 40_000, ChainID: DefaultChainID, GasFeeCap: 10, GasTipCap: 1, Data: counterCode, Signature: []byte{1}}
	plain := core.Transaction{From: sender, To: recipient, Value: 100, Nonce: 1, ChainID: DefaultChainID, Signature: []byte{1}}

	got := c.BuildBlockTxs([]core.Transaction{feePriced, plain})
	if len(got) != 0 {
		t.Fatalf("BuildBlockTxs = %d txs, want 0 (nonce 0's fee-priced tx can't fit, so nonce 1's plain transfer must not be skipped ahead to)", len(got))
	}
}

// TestBlockBuilderFreePassUnboundedAndOrderPreserved proves the other half
// of the design: plain-transfer/exchange/attestation transactions are
// always included, in full, regardless of the gas limit (they are not
// fee-priced and were never bounded before this PR either).
func TestBlockBuilderFreePassUnboundedAndOrderPreserved(t *testing.T) {
	sender := addr(1)
	recipient := addr(2)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0}
	gb := g.ToBlock()
	c := NewChain(gb, alloc)
	c.SetGasLimit(1) // irrelevant to fee-exempt transactions

	var txs []core.Transaction
	for i := uint64(0); i < 5; i++ {
		txs = append(txs, core.Transaction{From: sender, To: recipient, Value: 1, Nonce: i, ChainID: DefaultChainID, Signature: []byte{1}})
	}

	got := c.BuildBlockTxs(txs)
	if len(got) != len(txs) {
		t.Fatalf("BuildBlockTxs = %d plain transfers, want all %d included regardless of GasLimit(1)", len(got), len(txs))
	}
	for i, tx := range got {
		if tx.Nonce != uint64(i) {
			t.Fatalf("BuildBlockTxs[%d].Nonce = %d, want %d (nonce order preserved)", i, tx.Nonce, i)
		}
	}
}
