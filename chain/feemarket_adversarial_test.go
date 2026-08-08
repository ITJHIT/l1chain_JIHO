package chain

import (
	"errors"
	"testing"

	"l1chain/core"
)

// This file is PR10's own redteam/adversarial coverage for M9 (EIP-1559-style
// fee market): three cases not already exercised by a required wiring-PR
// test. The other categories the M9 plan names are already proven by
// required tests written in PR1/PR5/PR6/PR7/PR8:
//   - base fee determinism across independent chains: TestBaseFeeAgreesAcrossIndependentChains (PR5)
//   - fee-cap/tip-cap validity:                       TestTxWithFeeCapBelowBaseFeeRejected, TestTxWithTipExceedingFeeCapRejected (PR5)
//   - elasticity direction (rise/fall):                TestBaseFeeRisesAfterFullBlockFallsAfterEmptyBlock (PR6)
//   - a single over-limit transaction:                 TestBlockExceedingGasLimitRejected (PR6)
//   - GasUsed honesty:                                 TestBadGasUsedRejected (PR6)
//   - the BASEFEE opcode:                              TestEVMBaseFeeOpcodeReflectsRealBlockBaseFee (PR7)
//   - burn/tip balance conservation:                    TestFeeSplitBurnsBaseCreditsTipToCoinbase (PR8)
//   - block builder tip-ordering + nonce safety:        TestBlockBuilderPrefersHigherTipRespectsNonceOrder, TestBlockBuilderNonceOrderBlocksLaterFreePassTx (PR8)
//
// artifacts/m9-fee-market-redteam-report.json cites all of these by name
// rather than duplicating them here.

// TestAdvFeeMarket01ExactBaseFeeBoundaryIncludable proves the boundary case
// of EffectiveGasPrice's own feeCap<baseFee check: a transaction whose
// GasFeeCap equals the block's BaseFee EXACTLY (zero headroom, zero possible
// priority fee) is still includable -- the check is a strict less-than, not
// less-than-or-equal -- proven through the real wired chain path
// (AddBlock), not just the pure function in isolation (already covered by
// chain/feemarket_test.go's own TestEffectiveGasPriceFeeCapEqualsBaseFee
// MeansZeroTip).
func TestAdvFeeMarket01ExactBaseFeeBoundaryIncludable(t *testing.T) {
	sender := addr(1)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0, InitialBaseFee: 100}
	gb := g.ToBlock()
	c := NewChain(gb, alloc)

	baseFee := c.NextBaseFee() // the real value block 1 will carry, derived from genesis
	tx := core.Transaction{From: sender, To: core.Address{}, Nonce: 0, GasLimit: 40_000, ChainID: DefaultChainID, GasFeeCap: baseFee, GasTipCap: 0, Data: counterCode, Signature: []byte{1}}

	b := mineExchangeBlock(t, c, gb, []core.Transaction{tx})
	if err := c.AddBlock(b, acceptAll); err != nil {
		t.Fatalf("AddBlock (GasFeeCap == BaseFee exactly) = %v, want nil (zero headroom is still includable, not rejected)", err)
	}
	if got := c.State().GetAccount(sender).Nonce; got != 1 {
		t.Fatalf("sender nonce = %d, want 1 (the transaction was actually applied)", got)
	}
}

// TestAdvFeeMarket02BlockGasLimitEnforcedOnSumNotPerTx proves the block gas
// cap is checked against the SUM of actual gas consumed by every fee-priced
// transaction in the block, not any single transaction's own usage: two
// deploys, each individually well within the configured limit, are rejected
// together because their combined real cost exceeds it -- a materially
// different scenario from TestBlockExceedingGasLimitRejected's own single
// oversized transaction.
func TestAdvFeeMarket02BlockGasLimitEnforcedOnSumNotPerTx(t *testing.T) {
	sender := addr(1)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0}
	gb := g.ToBlock()
	c := NewChain(gb, alloc)
	c.SetGasLimit(40_000) // each deploy (32_000 = vm.GasCreate) fits alone; two together (64_000) don't

	tx1 := core.Transaction{From: sender, To: core.Address{}, Nonce: 0, GasLimit: 40_000, ChainID: DefaultChainID, GasFeeCap: 1, GasTipCap: 1, Data: counterCode, Signature: []byte{1}}
	tx2 := core.Transaction{From: sender, To: core.Address{}, Nonce: 1, GasLimit: 40_000, ChainID: DefaultChainID, GasFeeCap: 1, GasTipCap: 1, Data: counterCode, Signature: []byte{1}}

	b := mineExchangeBlock(t, c, gb, []core.Transaction{tx1, tx2})
	if err := c.AddBlock(b, acceptAll); !errors.Is(err, ErrBlockGasLimitExceeded) {
		t.Fatalf("AddBlock (two 32_000-gas deploys, neither alone exceeding the 40_000 limit, summing to 64_000) = %v, want ErrBlockGasLimitExceeded", err)
	}
}

// TestAdvFeeMarket03BlockBuilderExcludesPricedOutSenderKeepsOthers extends
// the block builder's own required tests (both 2-sender, neither exercising
// the "priced out" deactivation branch -- one uses BaseFee 0 so no
// transaction can ever be priced out, the other tests the separate
// "doesn't fit the gas budget" branch) to a 3-sender scenario that
// specifically exercises GasFeeCap < BaseFee exclusion: proves a priced-out
// sender's exclusion doesn't corrupt or block selection for OTHER,
// independent senders.
func TestAdvFeeMarket03BlockBuilderExcludesPricedOutSenderKeepsOthers(t *testing.T) {
	senderLow := addr(1)
	senderMid := addr(2)
	senderHigh := addr(3)
	alloc := map[core.Address]uint64{senderLow: 10_000_000, senderMid: 10_000_000, senderHigh: 10_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0, InitialBaseFee: 50}
	gb := g.ToBlock()
	c := NewChain(gb, alloc)

	baseFee := c.NextBaseFee()
	if baseFee == 0 {
		t.Fatal("test setup: baseFee must be nonzero to meaningfully price a transaction out")
	}

	txLow := core.Transaction{From: senderLow, To: core.Address{}, Nonce: 0, GasLimit: 40_000, ChainID: DefaultChainID, GasFeeCap: baseFee - 1, GasTipCap: 0, Data: counterCode, Signature: []byte{1}} // priced out
	txMid := core.Transaction{From: senderMid, To: core.Address{}, Nonce: 0, GasLimit: 40_000, ChainID: DefaultChainID, GasFeeCap: baseFee + 10, GasTipCap: 2, Data: counterCode, Signature: []byte{1}}
	txHigh := core.Transaction{From: senderHigh, To: core.Address{}, Nonce: 0, GasLimit: 40_000, ChainID: DefaultChainID, GasFeeCap: baseFee + 10, GasTipCap: 5, Data: counterCode, Signature: []byte{1}}

	got := c.BuildBlockTxs([]core.Transaction{txLow, txMid, txHigh})
	if len(got) != 2 {
		t.Fatalf("BuildBlockTxs = %d txs, want 2 (senderLow priced out, senderMid and senderHigh both includable)", len(got))
	}
	included := map[core.Address]bool{}
	for _, tx := range got {
		included[tx.From] = true
	}
	if included[senderLow] {
		t.Fatal("senderLow's priced-out transaction (GasFeeCap < BaseFee) was included")
	}
	if !included[senderMid] || !included[senderHigh] {
		t.Fatal("senderMid and senderHigh's valid transactions must both be included despite senderLow being priced out")
	}
}
