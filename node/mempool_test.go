package node

import (
	"errors"
	"testing"

	"l1chain/core"
)

// TestMempoolCapRejectsWhenFull verifies the deterministic reject-when-full
// policy: valid txs are accepted up to the cap, the next valid tx is rejected
// with ErrMempoolFull, MineBlock drains the pool, and submission resumes.
func TestMempoolCapRejectsWhenFull(t *testing.T) {
	a := newKeyT(t)
	b := newKeyT(t)
	n, err := New(Config{
		MinerKey:     newKeyT(t),
		Difficulty:   testDifficulty,
		GenesisAlloc: map[core.Address]uint64{a.Address(): 1_000_000},
		MaxMempool:   2,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()

	if got := n.MempoolCap(); got != 2 {
		t.Fatalf("MempoolCap() = %d, want 2", got)
	}

	// Fill the mempool to the cap with valid, increasing-nonce transfers.
	for i := uint64(0); i < 2; i++ {
		if err := n.SubmitTx(signedTransfer(a, b.Address(), 100, i)); err != nil {
			t.Fatalf("SubmitTx(nonce=%d) = %v, want nil", i, err)
		}
	}
	if got := n.MempoolLen(); got != 2 {
		t.Fatalf("mempool len = %d, want 2", got)
	}

	// The cap+1'th valid tx must be rejected with ErrMempoolFull.
	overflow := signedTransfer(a, b.Address(), 100, 2)
	if err := n.SubmitTx(overflow); !errors.Is(err, ErrMempoolFull) {
		t.Fatalf("SubmitTx(overflow) = %v, want ErrMempoolFull", err)
	}
	if got := n.MempoolLen(); got != 2 {
		t.Fatalf("mempool len after rejection = %d, want 2 (no pooled txs dropped)", got)
	}

	// Mining drains the mempool.
	if _, err := n.MineBlock(); err != nil {
		t.Fatalf("MineBlock: %v", err)
	}
	if got := n.MempoolLen(); got != 0 {
		t.Fatalf("mempool len after mine = %d, want 0", got)
	}

	// A further valid tx (next nonce after the mined ones) succeeds.
	if err := n.SubmitTx(signedTransfer(a, b.Address(), 100, 2)); err != nil {
		t.Fatalf("SubmitTx after drain = %v, want nil", err)
	}
	if got := n.MempoolLen(); got != 1 {
		t.Fatalf("mempool len after resume = %d, want 1", got)
	}
}

// TestMempoolDefaultCapWhenUnset verifies Config.MaxMempool <= 0 resolves to
// DefaultMaxMempool.
func TestMempoolDefaultCapWhenUnset(t *testing.T) {
	n, err := New(Config{
		MinerKey:   newKeyT(t),
		Difficulty: testDifficulty,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()

	if got := n.MempoolCap(); got != DefaultMaxMempool {
		t.Fatalf("MempoolCap() = %d, want DefaultMaxMempool (%d)", got, DefaultMaxMempool)
	}
	if DefaultMaxMempool <= 0 {
		t.Fatalf("DefaultMaxMempool = %d, want > 0", DefaultMaxMempool)
	}
}
