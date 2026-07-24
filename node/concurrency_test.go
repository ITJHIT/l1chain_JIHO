package node

import (
	"sync"
	"testing"

	"l1chain/core"
)

// TestConcurrentMineAndReads exercises the exact interleaving that crashed the
// daemon before the Node mutex was added: a miner goroutine writes to the shared
// chain canonical state (a Go map) via MineBlock while reader goroutines call
// Balance/Nonce/GetBlockByHeight/Head concurrently. Without n.mu this races on
// the state map ("fatal error: concurrent map read and map write"); with the
// RWMutex it is clean under `go test -race ./node/`.
func TestConcurrentMineAndReads(t *testing.T) {
	miner := newKeyT(t)
	funded := newKeyT(t)

	n, err := New(Config{
		MinerKey:     miner,
		Difficulty:   testDifficulty, // tiny difficulty so mining is fast
		GenesisAlloc: map[core.Address]uint64{funded.Address(): 1_000_000},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()

	const blocks = 20
	const readers = 8

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Reader goroutines hammer the read paths until mining finishes.
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = n.Balance(miner.Address())
				_ = n.Balance(funded.Address())
				_ = n.Nonce(funded.Address())
				head := n.Head()
				_, _ = n.GetBlockByHeight(head.Header.Height)
				_ = n.MempoolLen()
			}
		}()
	}

	// Miner goroutine writes shared state concurrently with the readers.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for i := 0; i < blocks; i++ {
			if _, err := n.MineBlock(); err != nil {
				t.Errorf("MineBlock %d: %v", i, err)
				return
			}
		}
	}()

	wg.Wait()

	if got := n.Head().Header.Height; got != blocks {
		t.Fatalf("head height = %d, want %d", got, blocks)
	}
}
