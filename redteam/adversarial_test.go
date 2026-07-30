package redteam

import (
	"errors"
	"math"
	"testing"

	"l1chain/chain"
	"l1chain/consensus"
	"l1chain/core"
	"l1chain/state"
	"l1chain/wallet"
)

// advDiff is a low PoW difficulty so mining stays fast in tests.
const advDiff uint32 = 6

// testMiner is the coinbase address credited BlockReward for built blocks.
var testMiner = addr(9)

// acceptAll is a permissive signature verifier used when a test isolates a
// non-signature invariant (balance atomicity, PoW, roots, reorg).
func acceptAll(core.Transaction) bool { return true }

// addr builds a deterministic non-zero address from a single byte.
func addr(b byte) core.Address {
	var a core.Address
	a[0] = b
	return a
}

// tx builds an unsigned (but non-empty-signature) value transfer, used with
// acceptAll for invariants that do not depend on real signatures.
func tx(from, to core.Address, value, nonce uint64) core.Transaction {
	return core.Transaction{From: from, To: to, Value: value, Nonce: nonce, ChainID: chain.DefaultChainID, Signature: []byte{1}}
}

// signedTx signs a value transfer with k, setting From and a real signature.
func signedTx(k wallet.Key, to core.Address, value, nonce uint64) core.Transaction {
	t := core.Transaction{To: to, Value: value, Nonce: nonce, ChainID: chain.DefaultChainID}
	k.Sign(&t)
	return t
}

// newChain builds a funded genesis chain at advDiff.
func newChain(alloc map[core.Address]uint64) (*chain.Chain, core.Block) {
	g := chain.Genesis{Alloc: alloc, Difficulty: advDiff, Timestamp: 0}
	gb := g.ToBlock()
	return chain.NewChain(gb, alloc), gb
}

// headHash returns the canonical head block's hash (Hash is a pointer method,
// so the value returned by Head() must be bound to a variable first).
func headHash(c *chain.Chain) core.Hash {
	h := c.Head()
	return h.Hash()
}

// replayBranch reproduces the state after applying every tx of branch (genesis
// excluded) plus each block's coinbase reward on top of alloc. It mirrors the
// engine's tx-plus-reward replay so built blocks carry a self-consistent root.
func replayBranch(alloc map[core.Address]uint64, branch []core.Block) state.StateDB {
	st := state.New()
	for a, bal := range alloc {
		acct := st.GetAccount(a)
		acct.Balance += bal
		st.SetAccount(a, acct)
	}
	for _, blk := range branch {
		for i := range blk.Txs {
			_ = chain.ApplyTx(st, blk.Txs[i], acceptAll)
		}
		m := st.GetAccount(blk.Header.Coinbase)
		m.Balance += chain.BlockReward
		st.SetAccount(blk.Header.Coinbase, m)
	}
	return st
}

// buildBlock mines a valid block extending parent, using prior (same-branch)
// blocks to compute the parent state. The block's MerkleRoot is always correct;
// its StateRoot reflects a best-effort replay (irrelevant for blocks the engine
// rejects before the state-root check, e.g. double-spends).
func buildBlock(parent core.Block, prior []core.Block, txs []core.Transaction, diff uint32, ts int64, alloc map[core.Address]uint64) core.Block {
	st := replayBranch(alloc, prior)
	for i := range txs {
		_ = chain.ApplyTx(st, txs[i], acceptAll)
	}
	m := st.GetAccount(testMiner)
	m.Balance += chain.BlockReward
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

// ---------------------------------------------------------------------------
// Case 1: Double-spend within one block.
// A sender with balance B submits two txs each spending > B/2 (sum > B) in the
// same block. AddBlock and ApplyBlock must reject; state must stay unchanged
// (atomicity, no negative/overflow balance).
// ---------------------------------------------------------------------------
func TestAdv01DoubleSpendWithinBlock(t *testing.T) {
	alloc := map[core.Address]uint64{addr(1): 1000}
	c, gb := newChain(alloc)

	t1 := tx(addr(1), addr(2), 600, 0) // > B/2
	t2 := tx(addr(1), addr(3), 600, 1) // > B/2, sum 1200 > B
	b := buildBlock(gb, nil, []core.Transaction{t1, t2}, advDiff, 1, alloc)

	if err := c.AddBlock(b, acceptAll); !errors.Is(err, chain.ErrInsufficientBalance) {
		t.Fatalf("AddBlock double-spend: err = %v, want ErrInsufficientBalance", err)
	}
	// Atomicity: the block was not adopted and canonical state is untouched.
	if headHash(c) != gb.Hash() {
		t.Fatalf("head advanced on a rejected double-spend block")
	}
	if got := c.State().GetAccount(addr(1)).Balance; got != 1000 {
		t.Fatalf("sender balance = %d, want 1000 (unchanged)", got)
	}
	if got := c.State().GetAccount(addr(2)).Balance; got != 0 {
		t.Fatalf("addr(2) balance = %d, want 0 (no partial spend)", got)
	}

	// Direct ApplyBlock atomicity: base state must be left fully unchanged.
	st := replayBranch(alloc, nil)
	before := st.GetAccount(addr(1)).Balance
	if err := chain.ApplyBlock(st, b, testMiner, acceptAll); !errors.Is(err, chain.ErrInsufficientBalance) {
		t.Fatalf("ApplyBlock double-spend: err = %v, want ErrInsufficientBalance", err)
	}
	if got := st.GetAccount(addr(1)).Balance; got != before {
		t.Fatalf("ApplyBlock leaked a partial debit: sender = %d, want %d", got, before)
	}
	if got := st.GetAccount(addr(2)).Balance; got != 0 {
		t.Fatalf("ApplyBlock leaked a partial credit: addr(2) = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Case 2: Replay / nonce reuse.
// A validly signed tx applied twice (same nonce) -> second application rejected
// with ErrBadNonce; balance is debited exactly once.
// ---------------------------------------------------------------------------
func TestAdv02ReplayNonceReuse(t *testing.T) {
	k, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	st := state.NewMemStateDB()
	acct := st.GetAccount(k.Address())
	acct.Balance = 1000
	st.SetAccount(k.Address(), acct)

	t1 := signedTx(k, addr(2), 100, 0)
	if !wallet.Verify(t1) {
		t.Fatalf("setup: signed tx must verify")
	}
	if err := chain.ApplyTx(st, t1, wallet.Verify); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// Replay the identical, already-applied tx.
	if err := chain.ApplyTx(st, t1, wallet.Verify); !errors.Is(err, chain.ErrBadNonce) {
		t.Fatalf("replay: err = %v, want ErrBadNonce", err)
	}
	if got := st.GetAccount(k.Address()).Balance; got != 900 {
		t.Fatalf("sender balance = %d, want 900 (debited once)", got)
	}
	if got := st.GetAccount(addr(2)).Balance; got != 100 {
		t.Fatalf("recipient balance = %d, want 100 (credited once)", got)
	}
	if got := st.GetAccount(k.Address()).Nonce; got != 1 {
		t.Fatalf("sender nonce = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// Case 3: Signature forgery / wrong signer.
// A tx signed by key X but claiming From = address(Y) -> wallet.Verify false and
// the block is rejected (ErrBadSig).
// ---------------------------------------------------------------------------
func TestAdv03SignatureForgeryWrongSigner(t *testing.T) {
	kx, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey x: %v", err)
	}
	ky, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey y: %v", err)
	}
	forged := signedTx(kx, addr(2), 100, 0) // signed by X, From = X
	forged.From = ky.Address()              // now claims to be Y

	if wallet.Verify(forged) {
		t.Fatalf("forged tx (signer X, From Y) must NOT verify")
	}

	alloc := map[core.Address]uint64{ky.Address(): 1000}
	c, gb := newChain(alloc)
	b := buildBlock(gb, nil, []core.Transaction{forged}, advDiff, 1, alloc)
	if err := c.AddBlock(b, wallet.Verify); !errors.Is(err, chain.ErrBadSig) {
		t.Fatalf("AddBlock forged tx: err = %v, want ErrBadSig", err)
	}
	if headHash(c) != gb.Hash() {
		t.Fatalf("head advanced on a block with a forged tx")
	}
}

// ---------------------------------------------------------------------------
// Case 4: Invalid Proof-of-Work.
// A well-formed block whose hash does not meet its declared Difficulty ->
// AddBlock rejects with ErrBadPoW.
// ---------------------------------------------------------------------------
func TestAdv04InvalidPoW(t *testing.T) {
	alloc := map[core.Address]uint64{addr(1): 1000}
	c, gb := newChain(alloc)
	b := buildBlock(gb, nil, []core.Transaction{tx(addr(1), addr(2), 100, 0)}, advDiff, 1, alloc)
	// Demand an impossible difficulty WITHOUT re-mining, so the hash cannot meet it.
	b.Header.Difficulty = 240
	if err := c.AddBlock(b, acceptAll); !errors.Is(err, chain.ErrBadPoW) {
		t.Fatalf("AddBlock invalid PoW: err = %v, want ErrBadPoW", err)
	}
}

// ---------------------------------------------------------------------------
// Case 5: Tampered StateRoot / MerkleRoot.
// Correct txs but a wrong Header.StateRoot -> ErrBadStateRoot; wrong
// Header.MerkleRoot -> ErrBadTxRoot. Blocks are re-mined so PoW passes and the
// root check is genuinely what rejects them.
// ---------------------------------------------------------------------------
func TestAdv05TamperedRoots(t *testing.T) {
	alloc := map[core.Address]uint64{addr(1): 1000}

	// Tampered StateRoot.
	c, gb := newChain(alloc)
	b := buildBlock(gb, nil, []core.Transaction{tx(addr(1), addr(2), 100, 0)}, advDiff, 1, alloc)
	b.Header.StateRoot = core.SumHash([]byte("evil-state-root"))
	consensus.Mine(&b.Header, 0)
	if err := c.AddBlock(b, acceptAll); !errors.Is(err, chain.ErrBadStateRoot) {
		t.Fatalf("AddBlock tampered StateRoot: err = %v, want ErrBadStateRoot", err)
	}

	// Tampered MerkleRoot (independent chain).
	c2, gb2 := newChain(alloc)
	b2 := buildBlock(gb2, nil, []core.Transaction{tx(addr(1), addr(2), 100, 0)}, advDiff, 1, alloc)
	b2.Header.MerkleRoot = core.SumHash([]byte("evil-merkle-root"))
	consensus.Mine(&b2.Header, 0)
	if err := c2.AddBlock(b2, acceptAll); !errors.Is(err, chain.ErrBadTxRoot) {
		t.Fatalf("AddBlock tampered MerkleRoot: err = %v, want ErrBadTxRoot", err)
	}
}

// ---------------------------------------------------------------------------
// Case 6: Deep reorg correctness / no cross-branch state leakage.
// Head branch A (2 blocks -> addr(2)) vs heavier branch B (3 blocks -> addr(3)).
// After the canonical switch, a tx effect from abandoned branch A must NOT be
// reflected in State().
// ---------------------------------------------------------------------------
func TestAdv06DeepReorgNoStateLeakage(t *testing.T) {
	alloc := map[core.Address]uint64{addr(1): 1000}
	c, gb := newChain(alloc)

	// Branch A: move 100 then 100 to addr(2).
	a1 := buildBlock(gb, nil, []core.Transaction{tx(addr(1), addr(2), 100, 0)}, advDiff, 1, alloc)
	a2 := buildBlock(a1, []core.Block{a1}, []core.Transaction{tx(addr(1), addr(2), 100, 1)}, advDiff, 2, alloc)
	for _, b := range []core.Block{a1, a2} {
		if err := c.AddBlock(b, acceptAll); err != nil {
			t.Fatalf("AddBlock branch A: %v", err)
		}
	}
	if headHash(c) != a2.Hash() {
		t.Fatalf("head should be a2 before reorg")
	}
	if got := c.State().GetAccount(addr(2)).Balance; got != 200 {
		t.Fatalf("addr(2) balance = %d, want 200 on branch A", got)
	}

	// Branch B: heavier (3 blocks) moving to addr(3). Distinct timestamps.
	b1 := buildBlock(gb, nil, []core.Transaction{tx(addr(1), addr(3), 300, 0)}, advDiff, 11, alloc)
	b2 := buildBlock(b1, []core.Block{b1}, []core.Transaction{tx(addr(1), addr(3), 50, 1)}, advDiff, 12, alloc)
	b3 := buildBlock(b2, []core.Block{b1, b2}, []core.Transaction{tx(addr(1), addr(3), 25, 2)}, advDiff, 13, alloc)
	for _, b := range []core.Block{b1, b2, b3} {
		if err := c.AddBlock(b, acceptAll); err != nil {
			t.Fatalf("AddBlock branch B: %v", err)
		}
	}
	if headHash(c) != b3.Hash() {
		t.Fatalf("head should be b3 after reorg to heavier branch B")
	}
	// No state leakage: branch A's credit to addr(2) must be gone.
	if got := c.State().GetAccount(addr(2)).Balance; got != 0 {
		t.Fatalf("STATE LEAK: addr(2) = %d after reorg, want 0 (branch A abandoned)", got)
	}
	if got := c.State().GetAccount(addr(3)).Balance; got != 375 {
		t.Fatalf("addr(3) = %d, want 375 (branch B)", got)
	}
	if got := c.State().GetAccount(addr(1)).Balance; got != 625 {
		t.Fatalf("addr(1) = %d, want 625 (1000-375 on branch B)", got)
	}
	if got := c.State().GetAccount(testMiner).Balance; got != 3*chain.BlockReward {
		t.Fatalf("miner = %d, want %d (three branch-B rewards)", got, 3*chain.BlockReward)
	}
	if c.TotalDifficulty(b3.Hash()) <= c.TotalDifficulty(a2.Hash()) {
		t.Fatalf("b3 cumulative difficulty must exceed a2")
	}
	// The abandoned branch's tip is still stored but is NOT canonical.
	if _, ok := c.GetByHash(a2.Hash()); !ok {
		t.Fatalf("abandoned block a2 should still be retrievable by hash")
	}
	if b, ok := c.GetByHeight(2); !ok || b.Hash() != b2.Hash() {
		t.Fatalf("canonical height 2 should follow branch B (b2)")
	}
}

// ---------------------------------------------------------------------------
// Case 7: Balance underflow / overflow guards.
// Value > balance and Value near math.MaxUint64 -> rejected with
// ErrInsufficientBalance, with no wraparound and no nonce advance.
// ---------------------------------------------------------------------------
func TestAdv07BalanceUnderflowOverflowGuards(t *testing.T) {
	st := state.NewMemStateDB()
	acct := st.GetAccount(addr(1))
	acct.Balance = 1000
	st.SetAccount(addr(1), acct)

	// Value greater than balance.
	if err := chain.ApplyTx(st, tx(addr(1), addr(2), 5000, 0), acceptAll); !errors.Is(err, chain.ErrInsufficientBalance) {
		t.Fatalf("over-balance transfer: err = %v, want ErrInsufficientBalance", err)
	}
	// Value near math.MaxUint64 (would wrap on an unchecked subtraction/addition).
	if err := chain.ApplyTx(st, tx(addr(1), addr(2), math.MaxUint64, 0), acceptAll); !errors.Is(err, chain.ErrInsufficientBalance) {
		t.Fatalf("MaxUint64 transfer: err = %v, want ErrInsufficientBalance", err)
	}
	// No wraparound and no state mutation from the rejected txs.
	if got := st.GetAccount(addr(1)).Balance; got != 1000 {
		t.Fatalf("sender balance = %d, want 1000 (no underflow)", got)
	}
	if got := st.GetAccount(addr(2)).Balance; got != 0 {
		t.Fatalf("recipient balance = %d, want 0 (no overflow wraparound)", got)
	}
	if got := st.GetAccount(addr(1)).Nonce; got != 0 {
		t.Fatalf("sender nonce = %d, want 0 (rejected txs must not advance nonce)", got)
	}
}

// ---------------------------------------------------------------------------
// Case 8: Coinbase tampering.
// A block whose claimed StateRoot credits the miner MORE than BlockReward -> the
// engine re-derives the correct (single-reward) state and rejects with
// ErrBadStateRoot.
// ---------------------------------------------------------------------------
func TestAdv08CoinbaseTampering(t *testing.T) {
	alloc := map[core.Address]uint64{addr(1): 1000}
	c, gb := newChain(alloc)
	txs := []core.Transaction{tx(addr(1), addr(2), 100, 0)}
	b := buildBlock(gb, nil, txs, advDiff, 1, alloc)

	// Forge a StateRoot in which the miner is credited an EXTRA BlockReward on
	// top of the legitimate one.
	st := replayBranch(alloc, nil)
	for i := range txs {
		_ = chain.ApplyTx(st, txs[i], acceptAll)
	}
	m := st.GetAccount(testMiner)
	m.Balance += 2 * chain.BlockReward // legitimate replay already added one; claim two
	st.SetAccount(testMiner, m)
	b.Header.StateRoot = st.StateRoot()
	consensus.Mine(&b.Header, 0)

	if err := c.AddBlock(b, acceptAll); !errors.Is(err, chain.ErrBadStateRoot) {
		t.Fatalf("AddBlock inflated coinbase: err = %v, want ErrBadStateRoot", err)
	}
	if headHash(c) != gb.Hash() {
		t.Fatalf("head advanced on a block with an inflated coinbase reward")
	}
}
