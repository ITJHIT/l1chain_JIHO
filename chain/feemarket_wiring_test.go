package chain

import (
	"errors"
	"testing"

	"l1chain/consensus"
	"l1chain/core"
	"l1chain/vm"
)

// feeTx builds a StackVM contract call/deploy transaction with a GasFeeCap
// (150) comfortably above anything BaseFee reaches in this file's own tests
// (genesis InitialBaseFee 100, drifting by at most 1/8 per block) and no tip
// -- these tests care about BaseFee agreement/rejection, not tip accounting
// (see chain/contract_test.go's deployTx/callTx for the GasFeeCap:1
// convention used elsewhere to preserve exact pre-M9 costs). gasLimit is the
// PER-TRANSACTION ceiling (unrelated to Chain.gasLimit, the block-level cap
// set via SetGasLimit) -- callers pass just enough headroom over the real
// StackVM cost (32_000 for a deploy, 5212 per counterCode call) that the
// reservation (gasLimit * 150) stays affordable against a 10_000_000 alloc
// even with a dozen transactions across several blocks.
func feeTx(from, to core.Address, nonce, gasLimit uint64, data []byte) core.Transaction {
	return core.Transaction{From: from, To: to, Nonce: nonce, GasLimit: gasLimit, ChainID: DefaultChainID, GasFeeCap: 150, GasTipCap: 0, Data: data, Signature: []byte{1}}
}

// TestBaseFeeAgreesAcrossIndependentChains is PR5's own required determinism
// test (non-negotiable per the M9 plan, mirrors M8's own required-test
// discipline): two independent *Chain instances, one mining via
// CandidateStateRoot+PoW (the exact path node.go's own miner takes), one
// only ever validating via AddBlock, across several blocks of deliberately
// varying fullness relative to GasTarget. Both must independently re-derive
// the IDENTICAL BaseFee at every height and agree on StateRoot.
func TestBaseFeeAgreesAcrossIndependentChains(t *testing.T) {
	sender := addr(1)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0, InitialBaseFee: 100}
	gb := g.ToBlock()

	chainA := NewChain(gb, alloc) // mines
	chainA.SetGasLimit(100_000)   // target = 50_000
	chainB := NewChain(gb, alloc) // only ever validates via AddBlock
	chainB.SetGasLimit(100_000)

	contract := vm.CreateAddress(sender, 0)

	// Each block's OWN Header.BaseFee reflects its PARENT's fullness, not its
	// own (exactly like real EIP-1559: BaseFee is a function of the parent
	// header) -- so the effect of block 3 being unusually full only becomes
	// observable in block 4's BaseFee, and block 4's own emptiness only
	// becomes observable in block 5's.
	//
	// Block 1: deploy (32_000 gas, below the 50_000 target).
	// Block 2: one call (5212 gas, below target).
	// Block 3: ten calls (52_120 gas, ABOVE target) -> block 4's BaseFee rises.
	// Block 4: empty (0 gas) -> block 5's BaseFee falls.
	// Block 5: empty, purely so its own BaseFee (reflecting block 4) can be read.
	blocks := [][]core.Transaction{
		{feeTx(sender, core.Address{}, 0, 40_000, counterCode)},
		{feeTx(sender, contract, 1, 10_000, nil)},
		{
			feeTx(sender, contract, 2, 10_000, nil), feeTx(sender, contract, 3, 10_000, nil),
			feeTx(sender, contract, 4, 10_000, nil), feeTx(sender, contract, 5, 10_000, nil),
			feeTx(sender, contract, 6, 10_000, nil), feeTx(sender, contract, 7, 10_000, nil),
			feeTx(sender, contract, 8, 10_000, nil), feeTx(sender, contract, 9, 10_000, nil),
			feeTx(sender, contract, 10, 10_000, nil), feeTx(sender, contract, 11, 10_000, nil),
		},
		{},
		{},
	}

	parent := gb
	var baseFees []uint64
	for i, txs := range blocks {
		b := mineExchangeBlock(t, chainA, parent, txs)
		if err := chainA.AddBlock(b, acceptAll); err != nil {
			t.Fatalf("block %d: chainA rejected its own mined block: %v", i+1, err)
		}
		if err := chainB.AddBlock(b, acceptAll); err != nil {
			t.Fatalf("block %d: chainB (independent instance) rejected chainA's block: %v", i+1, err)
		}
		blkA, ok := chainA.GetByHeight(b.Header.Height)
		if !ok {
			t.Fatalf("block %d: missing from chainA by height", i+1)
		}
		blkB, ok := chainB.GetByHeight(b.Header.Height)
		if !ok {
			t.Fatalf("block %d: missing from chainB by height", i+1)
		}
		if blkA.Header.BaseFee != blkB.Header.BaseFee {
			t.Fatalf("block %d: chainA BaseFee %d != chainB BaseFee %d", i+1, blkA.Header.BaseFee, blkB.Header.BaseFee)
		}
		if chainA.State().StateRoot() != chainB.State().StateRoot() {
			t.Fatalf("block %d: StateRoot diverged between independent chains", i+1)
		}
		baseFees = append(baseFees, blkA.Header.BaseFee)
		parent = b
	}

	// The elasticity mechanism must have actually responded to demand, not
	// just agreed on some arbitrary constant: block 4's BaseFee (reflecting
	// block 3's above-target fullness) must exceed block 3's own BaseFee;
	// block 5's BaseFee (reflecting block 4's emptiness) must fall below
	// block 4's own.
	if baseFees[3] <= baseFees[2] {
		t.Fatalf("BaseFee after an above-target block (%d) did not rise above the prior block's (%d)", baseFees[3], baseFees[2])
	}
	if baseFees[4] >= baseFees[3] {
		t.Fatalf("BaseFee after an empty block (%d) did not fall below the prior block's (%d)", baseFees[4], baseFees[3])
	}
}

// TestTxWithFeeCapBelowBaseFeeRejected proves a transaction whose GasFeeCap
// cannot cover the chain's current BaseFee is rejected with
// ErrFeeCapBelowBaseFee -- surfaced at CandidateStateRoot, the point a real
// miner/block-builder would first encounter it, before ever reaching
// AddBlock.
func TestTxWithFeeCapBelowBaseFeeRejected(t *testing.T) {
	sender := addr(1)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0, InitialBaseFee: 100}
	gb := g.ToBlock()
	c := NewChain(gb, alloc)

	tx := core.Transaction{From: sender, To: core.Address{}, Nonce: 0, GasLimit: 100_000, ChainID: DefaultChainID, GasFeeCap: 50, GasTipCap: 0, Data: counterCode, Signature: []byte{1}}
	_, _, err := c.CandidateStateRoot([]core.Transaction{tx}, addr(9), acceptAll)
	if !errors.Is(err, ErrFeeCapBelowBaseFee) {
		t.Fatalf("CandidateStateRoot (feeCap 50 < baseFee 100) = %v, want ErrFeeCapBelowBaseFee", err)
	}
}

// TestTxWithTipExceedingFeeCapRejected proves a transaction whose GasTipCap
// exceeds its own GasFeeCap -- an internally inconsistent transaction -- is
// rejected with ErrTipExceedsFeeCap, independent of BaseFee.
func TestTxWithTipExceedingFeeCapRejected(t *testing.T) {
	sender := addr(1)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0}
	gb := g.ToBlock()
	c := NewChain(gb, alloc)

	tx := core.Transaction{From: sender, To: core.Address{}, Nonce: 0, GasLimit: 100_000, ChainID: DefaultChainID, GasFeeCap: 5, GasTipCap: 10, Data: counterCode, Signature: []byte{1}}
	_, _, err := c.CandidateStateRoot([]core.Transaction{tx}, addr(9), acceptAll)
	if !errors.Is(err, ErrTipExceedsFeeCap) {
		t.Fatalf("CandidateStateRoot (tipCap 10 > feeCap 5) = %v, want ErrTipExceedsFeeCap", err)
	}
}

// TestBaseFeeRisesAfterFullBlockFallsAfterEmptyBlock is PR6's own required
// test (non-negotiable per the M9 plan): the core "this is actually a
// market" property, isolated on a single chain (PR5's own
// TestBaseFeeAgreesAcrossIndependentChains already proves cross-chain
// agreement; this test's own job is just the directional response to
// demand). Each block's OWN Header.BaseFee reflects its PARENT's fullness,
// not its own -- see TestBaseFeeAgreesAcrossIndependentChains's own doc
// comment for why the effect of an above-target block only becomes
// observable in the NEXT block's BaseFee.
func TestBaseFeeRisesAfterFullBlockFallsAfterEmptyBlock(t *testing.T) {
	sender := addr(1)
	alloc := map[core.Address]uint64{sender: 10_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0, InitialBaseFee: 100}
	gb := g.ToBlock()
	c := NewChain(gb, alloc)
	c.SetGasLimit(100_000) // target = 50_000

	contract := vm.CreateAddress(sender, 0)

	// Setup: deploy (32_000 gas, below target) -- not asserted on directly.
	deployBlk := mineExchangeBlock(t, c, gb, []core.Transaction{feeTx(sender, core.Address{}, 0, 40_000, counterCode)})
	if err := c.AddBlock(deployBlk, acceptAll); err != nil {
		t.Fatalf("AddBlock deploy: %v", err)
	}

	// Ten calls: 52_120 gas, ABOVE the 50_000 target.
	var calls []core.Transaction
	for i := uint64(0); i < 10; i++ {
		calls = append(calls, feeTx(sender, contract, i+1, 10_000, nil))
	}
	fullBlk := mineExchangeBlock(t, c, deployBlk, calls)
	if err := c.AddBlock(fullBlk, acceptAll); err != nil {
		t.Fatalf("AddBlock full block: %v", err)
	}

	emptyBlk1 := mineExchangeBlock(t, c, fullBlk, nil)
	if err := c.AddBlock(emptyBlk1, acceptAll); err != nil {
		t.Fatalf("AddBlock empty block 1: %v", err)
	}
	riseBaseFee := emptyBlk1.Header.BaseFee // reflects fullBlk's above-target usage

	emptyBlk2 := mineExchangeBlock(t, c, emptyBlk1, nil)
	if err := c.AddBlock(emptyBlk2, acceptAll); err != nil {
		t.Fatalf("AddBlock empty block 2: %v", err)
	}
	fallBaseFee := emptyBlk2.Header.BaseFee // reflects emptyBlk1's zero usage

	if riseBaseFee <= fullBlk.Header.BaseFee {
		t.Fatalf("BaseFee after an above-target block (%d) did not rise above that block's own BaseFee (%d)", riseBaseFee, fullBlk.Header.BaseFee)
	}
	if fallBaseFee >= riseBaseFee {
		t.Fatalf("BaseFee after an empty block (%d) did not fall below the prior block's (%d)", fallBaseFee, riseBaseFee)
	}
}

// TestBlockExceedingGasLimitRejected is PR6's other required test: a block
// whose actual gas consumption exceeds the chain's configured block gas cap
// is rejected with ErrBlockGasLimitExceeded, even though its declared
// Header.GasUsed correctly matches that (too-high) actual consumption --
// isolating the gas-limit check from the separate GasUsed-honesty check
// (see TestBadGasUsedRejected below).
func TestBlockExceedingGasLimitRejected(t *testing.T) {
	c, gb, _ := newContractChain() // alloc 10_000_000
	c.SetGasLimit(10_000)          // well below a single deploy's real cost (32_000 = vm.GasCreate)

	b := mineExchangeBlock(t, c, gb, []core.Transaction{deployTx(0)})
	if err := c.AddBlock(b, acceptAll); !errors.Is(err, ErrBlockGasLimitExceeded) {
		t.Fatalf("AddBlock (32_000 actual gas > 10_000 limit) = %v, want ErrBlockGasLimitExceeded", err)
	}
}

// TestBadGasUsedRejected proves a block whose declared Header.GasUsed does
// not match the actual gas consumed by its transactions is rejected with
// ErrBadGasUsed -- independent of whether the (wrong) claimed value would
// itself have been under the gas limit. The header is tampered AFTER mining
// and re-mined so its PoW is genuinely valid for the tampered content
// (AddBlock must catch this by re-deriving GasUsed itself, not by noticing
// a stale/invalid PoW).
func TestBadGasUsedRejected(t *testing.T) {
	c, gb, _ := newContractChain()

	b := mineExchangeBlock(t, c, gb, []core.Transaction{deployTx(0)})
	b.Header.GasUsed = 0 // tamper: claim 0 despite the deploy really consuming 32_000
	consensus.Mine(&b.Header, 0)

	if err := c.AddBlock(b, acceptAll); !errors.Is(err, ErrBadGasUsed) {
		t.Fatalf("AddBlock (tampered GasUsed) = %v, want ErrBadGasUsed", err)
	}
}
