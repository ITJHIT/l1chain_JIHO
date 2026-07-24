package chain

import (
	"errors"
	"testing"

	"l1chain/consensus"
	"l1chain/core"
	"l1chain/state"
)

// testMiner is the coinbase address credited with BlockReward for every block
// the test helpers build (genesis excluded).
var testMiner = addr(9)

// replayBranch reproduces the state after applying the txs of the given branch
// (genesis excluded from txs) plus each block's coinbase reward on top of alloc.
func replayBranch(alloc map[core.Address]uint64, branch []core.Block) state.StateDB {
	st := state.NewMemStateDB()
	for a, b := range alloc {
		acct := st.GetAccount(a)
		acct.Balance += b
		st.SetAccount(a, acct)
	}
	for _, blk := range branch {
		for i := range blk.Txs {
			_ = ApplyTx(st, blk.Txs[i], acceptAll)
		}
		m := st.GetAccount(blk.Header.Coinbase)
		m.Balance += BlockReward
		st.SetAccount(blk.Header.Coinbase, m)
	}
	return st
}

// buildBlock mines a valid block extending parent, given the prior blocks of the
// same branch (used to compute the parent state) and this block's txs. The block
// is mined for testMiner, whose coinbase reward is folded into the state root.
func buildBlock(parent core.Block, prior []core.Block, txs []core.Transaction, diff uint32, ts int64, alloc map[core.Address]uint64) core.Block {
	st := replayBranch(alloc, prior)
	for i := range txs {
		_ = ApplyTx(st, txs[i], acceptAll)
	}
	m := st.GetAccount(testMiner)
	m.Balance += BlockReward
	st.SetAccount(testMiner, m)
	h := core.Header{
		Height:     parent.Header.Height + 1,
		PrevHash:   parent.Hash(),
		Coinbase:   testMiner,
		Difficulty: diff,
		Timestamp:  ts,
	}
	b := core.Block{Header: h, Txs: txs}
	b.Header.MerkleRoot = b.TxRoot()
	b.Header.StateRoot = st.StateRoot()
	consensus.Mine(&b.Header, 0)
	return b
}

func headHash(c *Chain) core.Hash {
	h := c.Head()
	return h.Hash()
}

func tx(from, to core.Address, value, nonce uint64) core.Transaction {
	return core.Transaction{From: from, To: to, Value: value, Nonce: nonce, ChainID: DefaultChainID, Signature: []byte{1}}
}

const testDiff = 6

func newTestChain() (*Chain, core.Block, map[core.Address]uint64) {
	alloc := map[core.Address]uint64{addr(1): 1000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0}
	gb := g.ToBlock()
	return NewChain(gb, alloc), gb, alloc
}

func TestChainRejectsUnknownParent(t *testing.T) {
	c, _, alloc := newTestChain()
	orphan := core.Block{Header: core.Header{Height: 1, PrevHash: core.SumHash([]byte("nope")), Difficulty: testDiff}}
	orphan.Header.MerkleRoot = orphan.TxRoot()
	orphan.Header.StateRoot = replayBranch(alloc, nil).StateRoot()
	consensus.Mine(&orphan.Header, 0)
	if err := c.AddBlock(orphan, acceptAll); !errors.Is(err, ErrUnknownParent) {
		t.Fatalf("err = %v, want ErrUnknownParent", err)
	}
}

func TestChainRejectsBadPoW(t *testing.T) {
	c, gb, alloc := newTestChain()
	b := buildBlock(gb, nil, []core.Transaction{tx(addr(1), addr(2), 100, 0)}, testDiff, 1, alloc)
	// Demand an impossible difficulty without re-mining.
	b.Header.Difficulty = 240
	if err := c.AddBlock(b, acceptAll); !errors.Is(err, ErrBadPoW) {
		t.Fatalf("err = %v, want ErrBadPoW", err)
	}
}

func TestChainRejectsBadStateRoot(t *testing.T) {
	c, gb, alloc := newTestChain()
	b := buildBlock(gb, nil, []core.Transaction{tx(addr(1), addr(2), 100, 0)}, testDiff, 1, alloc)
	b.Header.StateRoot = core.SumHash([]byte("wrong"))
	consensus.Mine(&b.Header, 0) // re-mine so PoW passes; state root now wrong
	if err := c.AddBlock(b, acceptAll); !errors.Is(err, ErrBadStateRoot) {
		t.Fatalf("err = %v, want ErrBadStateRoot", err)
	}
}

func TestChainRejectsBadMerkleRoot(t *testing.T) {
	c, gb, alloc := newTestChain()
	b := buildBlock(gb, nil, []core.Transaction{tx(addr(1), addr(2), 100, 0)}, testDiff, 1, alloc)
	b.Header.MerkleRoot = core.SumHash([]byte("wrong"))
	consensus.Mine(&b.Header, 0)
	if err := c.AddBlock(b, acceptAll); !errors.Is(err, ErrBadTxRoot) {
		t.Fatalf("err = %v, want ErrBadTxRoot", err)
	}
}

func TestChainHappyExtendAndState(t *testing.T) {
	c, gb, alloc := newTestChain()
	a1 := buildBlock(gb, nil, []core.Transaction{tx(addr(1), addr(2), 100, 0)}, testDiff, 1, alloc)
	if err := c.AddBlock(a1, acceptAll); err != nil {
		t.Fatalf("AddBlock a1: %v", err)
	}
	if headHash(c) != a1.Hash() {
		t.Fatalf("head not a1 after extend")
	}
	if got := c.State().GetAccount(addr(2)).Balance; got != 100 {
		t.Fatalf("addr(2) balance = %d, want 100", got)
	}
	// Coinbase reward is now part of canonical state: the miner gains BlockReward.
	if got := c.State().GetAccount(testMiner).Balance; got != BlockReward {
		t.Fatalf("miner balance = %d, want %d (one block reward)", got, BlockReward)
	}
	if b, ok := c.GetByHeight(1); !ok || b.Hash() != a1.Hash() {
		t.Fatalf("GetByHeight(1) != a1")
	}
}

func TestChainReorg(t *testing.T) {
	c, gb, alloc := newTestChain()

	// Branch A: two blocks moving 100 then 100 from addr(1) -> addr(2).
	a1 := buildBlock(gb, nil, []core.Transaction{tx(addr(1), addr(2), 100, 0)}, testDiff, 1, alloc)
	a2 := buildBlock(a1, []core.Block{a1}, []core.Transaction{tx(addr(1), addr(2), 100, 1)}, testDiff, 2, alloc)
	if err := c.AddBlock(a1, acceptAll); err != nil {
		t.Fatalf("AddBlock a1: %v", err)
	}
	if err := c.AddBlock(a2, acceptAll); err != nil {
		t.Fatalf("AddBlock a2: %v", err)
	}
	if headHash(c) != a2.Hash() {
		t.Fatalf("head should be a2")
	}

	// Branch B: three blocks moving to addr(3). Uses different timestamps so
	// the block hashes differ from branch A.
	b1 := buildBlock(gb, nil, []core.Transaction{tx(addr(1), addr(3), 300, 0)}, testDiff, 11, alloc)
	b2 := buildBlock(b1, []core.Block{b1}, []core.Transaction{tx(addr(1), addr(3), 50, 1)}, testDiff, 12, alloc)
	b3 := buildBlock(b2, []core.Block{b1, b2}, []core.Transaction{tx(addr(1), addr(3), 25, 2)}, testDiff, 13, alloc)

	// After b1, b2 the branch ties A's cumulative difficulty -> NO reorg.
	if err := c.AddBlock(b1, acceptAll); err != nil {
		t.Fatalf("AddBlock b1: %v", err)
	}
	if err := c.AddBlock(b2, acceptAll); err != nil {
		t.Fatalf("AddBlock b2: %v", err)
	}
	if headHash(c) != a2.Hash() {
		t.Fatalf("equal-weight branch caused a reorg; head should still be a2")
	}

	// b3 overtakes -> reorg to branch B.
	if err := c.AddBlock(b3, acceptAll); err != nil {
		t.Fatalf("AddBlock b3: %v", err)
	}
	if headHash(c) != b3.Hash() {
		t.Fatalf("head should be b3 after reorg")
	}
	if b, ok := c.GetByHeight(1); !ok || b.Hash() != b1.Hash() {
		t.Fatalf("canonical height 1 should follow branch B (b1)")
	}
	if b, ok := c.GetByHeight(3); !ok || b.Hash() != b3.Hash() {
		t.Fatalf("canonical height 3 should be b3")
	}
	// Canonical state reflects branch B replay: addr(1)=1000-375=625, addr(3)=375, addr(2)=0.
	if got := c.State().GetAccount(addr(3)).Balance; got != 375 {
		t.Fatalf("addr(3) balance = %d, want 375", got)
	}
	if got := c.State().GetAccount(addr(1)).Balance; got != 625 {
		t.Fatalf("addr(1) balance = %d, want 625", got)
	}
	if got := c.State().GetAccount(addr(2)).Balance; got != 0 {
		t.Fatalf("addr(2) balance = %d, want 0 (branch A abandoned)", got)
	}
	// Branch B has three blocks, so the miner earned three block rewards after
	// the reorg switched canonical state to branch B.
	if got := c.State().GetAccount(testMiner).Balance; got != 3*BlockReward {
		t.Fatalf("miner balance = %d, want %d (three branch-B rewards)", got, 3*BlockReward)
	}
	if c.TotalDifficulty(b3.Hash()) <= c.TotalDifficulty(a2.Hash()) {
		t.Fatalf("b3 cumulative difficulty should exceed a2")
	}

	// A lighter side block off genesis must NOT reorg.
	light := buildBlock(gb, nil, []core.Transaction{tx(addr(1), addr(4), 1, 0)}, testDiff, 21, alloc)
	if err := c.AddBlock(light, acceptAll); err != nil {
		t.Fatalf("AddBlock light: %v", err)
	}
	if headHash(c) != b3.Hash() {
		t.Fatalf("lighter branch must not reorg; head should stay b3")
	}
}
