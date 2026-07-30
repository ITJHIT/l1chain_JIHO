package store

import (
	"path/filepath"
	"testing"

	"l1chain/chain"
	"l1chain/consensus"
	"l1chain/core"
	"l1chain/state"
	"l1chain/wallet"
)

// testDiff is a tiny PoW difficulty so tests mine quickly.
const testDiff = 6

// storeMiner is the coinbase address credited with BlockReward per built block.
var storeMiner = core.Address{9}

// openTemp opens a fresh store in a per-test temp dir.
func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// dummyBlock builds a self-consistent (hash-stable) block for storage tests; it
// is not required to satisfy chain validation.
func dummyBlock(height uint64, prev core.Hash, nonce uint64) core.Block {
	b := core.Block{Header: core.Header{Height: height, PrevHash: prev, Nonce: nonce}}
	b.Header.MerkleRoot = b.TxRoot()
	return b
}

func TestPutGetBlockRoundTrip(t *testing.T) {
	s := openTemp(t)
	b := dummyBlock(3, core.Hash{4, 5}, 77)

	if err := s.PutBlock(b); err != nil {
		t.Fatalf("PutBlock: %v", err)
	}
	got, found, err := s.GetBlock(b.Hash())
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if !found {
		t.Fatalf("GetBlock: block not found after PutBlock")
	}
	if got.Hash() != b.Hash() {
		t.Fatalf("GetBlock hash mismatch: got %s want %s", got.Hash().Hex(), b.Hash().Hex())
	}

	// Unknown hash -> found=false, no error.
	_, found, err = s.GetBlock(core.Hash{0xde, 0xad})
	if err != nil {
		t.Fatalf("GetBlock(unknown): %v", err)
	}
	if found {
		t.Fatalf("GetBlock(unknown) reported found=true")
	}
}

func TestCanonicalAndHeadRoundTrip(t *testing.T) {
	s := openTemp(t)

	// Canonical: unknown height -> found=false.
	if _, found, err := s.GetCanonical(1); err != nil || found {
		t.Fatalf("GetCanonical(unknown): found=%v err=%v", found, err)
	}
	h1 := core.Hash{1}
	if err := s.SetCanonical(1, h1); err != nil {
		t.Fatalf("SetCanonical: %v", err)
	}
	got, found, err := s.GetCanonical(1)
	if err != nil || !found || got != h1 {
		t.Fatalf("GetCanonical: got=%s found=%v err=%v", got.Hex(), found, err)
	}

	// Head: unknown -> found=false.
	if _, found, err := s.GetHead(); err != nil || found {
		t.Fatalf("GetHead(unset): found=%v err=%v", found, err)
	}
	hh := core.Hash{2, 2, 2}
	if err := s.SetHead(hh); err != nil {
		t.Fatalf("SetHead: %v", err)
	}
	got, found, err = s.GetHead()
	if err != nil || !found || got != hh {
		t.Fatalf("GetHead: got=%s found=%v err=%v", got.Hex(), found, err)
	}
}

// buildChain constructs genesis + `nBlocks` mined blocks, the first of which
// carries a real wallet-signed transfer from `sender` to `recipient`. It returns
// the chain and the alloc used to fund genesis.
func buildChain(t *testing.T, sender, recipient wallet.Key, nBlocks int) (*chain.Chain, map[core.Address]uint64) {
	t.Helper()

	alloc := map[core.Address]uint64{sender.Address(): 1000}
	gb := chain.Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0}.ToBlock()
	c := chain.NewChain(gb, alloc)

	// Running state mirrors chain derivation (alloc + applied txs) so each block's
	// StateRoot is correct.
	running := state.New()
	for a, bal := range alloc {
		acct := running.GetAccount(a)
		acct.Balance += bal
		running.SetAccount(a, acct)
	}

	parent := gb
	for i := 0; i < nBlocks; i++ {
		var txs []core.Transaction
		if i == 0 {
			tx := core.Transaction{From: sender.Address(), To: recipient.Address(), Value: 250, Nonce: 0, ChainID: chain.DefaultChainID}
			sender.Sign(&tx)
			txs = []core.Transaction{tx}
		}
		for j := range txs {
			if err := chain.ApplyTx(running, txs[j], wallet.Verify); err != nil {
				t.Fatalf("ApplyTx building block %d: %v", i+1, err)
			}
		}
		// Credit the coinbase reward so `running` mirrors canonical derivation.
		m := running.GetAccount(storeMiner)
		m.Balance += chain.BlockReward
		running.SetAccount(storeMiner, m)
		b := core.Block{
			Header: core.Header{
				Height:     parent.Header.Height + 1,
				PrevHash:   parent.Hash(),
				Coinbase:   storeMiner,
				Difficulty: testDiff,
				Timestamp:  int64(i + 1),
			},
			Txs: txs,
		}
		b.Header.MerkleRoot = b.TxRoot()
		b.Header.StateRoot = running.StateRoot()
		if _, found := consensus.Mine(&b.Header, 0); !found {
			t.Fatalf("Mine failed for block %d", i+1)
		}
		if err := c.AddBlock(b, wallet.Verify); err != nil {
			t.Fatalf("AddBlock %d: %v", i+1, err)
		}
		parent = b
	}
	return c, alloc
}

// TestRestartDurability builds a chain, persists it, closes and re-opens the DB,
// reloads the chain and asserts head, height, canonical hashes and balances all
// match the original.
func TestRestartDurability(t *testing.T) {
	sender, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey sender: %v", err)
	}
	recipient, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey recipient: %v", err)
	}

	orig, alloc := buildChain(t, sender, recipient, 2)

	dbPath := filepath.Join(t.TempDir(), "durable.db")
	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open s1: %v", err)
	}
	if err := Save(s1, orig); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s1.PutGenesisAlloc(alloc); err != nil {
		t.Fatalf("PutGenesisAlloc: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close s1: %v", err)
	}

	// Reopen from disk and reload.
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open s2: %v", err)
	}
	defer s2.Close()

	reloaded, ok, err := Load(s2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !ok {
		t.Fatalf("Load reported no head after Save")
	}

	// Head hash and height.
	origHead := orig.Head()
	reHead := reloaded.Head()
	if reHead.Hash() != origHead.Hash() {
		t.Fatalf("head hash mismatch: got %s want %s", reHead.Hash().Hex(), origHead.Hash().Hex())
	}
	if reHead.Header.Height != origHead.Header.Height {
		t.Fatalf("head height mismatch: got %d want %d", reHead.Header.Height, origHead.Header.Height)
	}

	// Canonical hashes per height.
	for h := uint64(0); h <= orig.Head().Header.Height; h++ {
		ob, ok1 := orig.GetByHeight(h)
		rb, ok2 := reloaded.GetByHeight(h)
		if !ok1 || !ok2 {
			t.Fatalf("GetByHeight(%d): orig=%v reloaded=%v", h, ok1, ok2)
		}
		if ob.Hash() != rb.Hash() {
			t.Fatalf("canonical hash mismatch at height %d: got %s want %s", h, rb.Hash().Hex(), ob.Hash().Hex())
		}
	}

	// Balances (state root + specific accounts).
	if reloaded.State().StateRoot() != orig.State().StateRoot() {
		t.Fatalf("state root mismatch after reload")
	}
	for _, a := range []core.Address{sender.Address(), recipient.Address()} {
		want := orig.State().GetAccount(a).Balance
		got := reloaded.State().GetAccount(a).Balance
		if got != want {
			t.Fatalf("balance mismatch for %s: got %d want %d", a.Hex(), got, want)
		}
	}
	if reloaded.State().GetAccount(recipient.Address()).Balance != 250 {
		t.Fatalf("recipient balance = %d, want 250", reloaded.State().GetAccount(recipient.Address()).Balance)
	}
	// Miner rewards are part of canonical state and survive a reload: two mined
	// blocks credit the coinbase 2 * BlockReward.
	if got := reloaded.State().GetAccount(storeMiner).Balance; got != 2*chain.BlockReward {
		t.Fatalf("reloaded miner balance = %d, want %d", got, 2*chain.BlockReward)
	}

	// Load on an empty store returns (nil,false,nil).
	empty := openTemp(t)
	c, ok, err := Load(empty)
	if err != nil || ok || c != nil {
		t.Fatalf("Load(empty): c=%v ok=%v err=%v", c, ok, err)
	}
}
