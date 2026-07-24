package node

import (
	"path/filepath"
	"testing"

	"l1chain/chain"
	"l1chain/core"
	"l1chain/wallet"
)

const testDifficulty = 6

func newKeyT(t *testing.T) wallet.Key {
	t.Helper()
	k, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return k
}

// signedTransfer builds and signs a transfer from -> to.
func signedTransfer(from wallet.Key, to core.Address, value, nonce uint64) core.Transaction {
	tx := core.Transaction{To: to, Value: value, Nonce: nonce, GasLimit: 21000}
	from.Sign(&tx)
	return tx
}

func TestGenesisFunding(t *testing.T) {
	a := newKeyT(t)
	n, err := New(Config{
		MinerKey:     newKeyT(t),
		Difficulty:   testDifficulty,
		GenesisAlloc: map[core.Address]uint64{a.Address(): 1000},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()

	if got := n.Balance(a.Address()); got != 1000 {
		t.Fatalf("genesis balance = %d, want 1000", got)
	}
	if h := n.Head().Header.Height; h != 0 {
		t.Fatalf("genesis head height = %d, want 0", h)
	}
}

func TestSubmitTxAcceptAndReject(t *testing.T) {
	a := newKeyT(t)
	b := newKeyT(t)
	n, err := New(Config{
		MinerKey:     newKeyT(t),
		Difficulty:   testDifficulty,
		GenesisAlloc: map[core.Address]uint64{a.Address(): 1000},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()

	// Valid signed tx is accepted.
	tx := signedTransfer(a, b.Address(), 100, 0)
	if err := n.SubmitTx(tx); err != nil {
		t.Fatalf("SubmitTx(valid) = %v, want nil", err)
	}
	if n.MempoolLen() != 1 {
		t.Fatalf("mempool len = %d, want 1", n.MempoolLen())
	}

	// Tampered tx (value changed after signing) must be rejected.
	tampered := tx
	tampered.Value = 999
	tampered.Nonce = 1
	if err := n.SubmitTx(tampered); err == nil {
		t.Fatalf("SubmitTx(tampered) = nil, want rejection")
	}

	// Unsigned tx must be rejected.
	unsigned := core.Transaction{From: a.Address(), To: b.Address(), Value: 1, Nonce: 1}
	if err := n.SubmitTx(unsigned); err == nil {
		t.Fatalf("SubmitTx(unsigned) = nil, want rejection")
	}

	// Overspend must be rejected.
	over := signedTransfer(a, b.Address(), 100000, 1)
	if err := n.SubmitTx(over); err == nil {
		t.Fatalf("SubmitTx(overspend) = nil, want rejection")
	}
}

func TestMineBlockAdvancesHeadAndBalances(t *testing.T) {
	a := newKeyT(t)
	b := newKeyT(t)
	n, err := New(Config{
		MinerKey:     newKeyT(t),
		Difficulty:   testDifficulty,
		GenesisAlloc: map[core.Address]uint64{a.Address(): 1000},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()

	if err := n.SubmitTx(signedTransfer(a, b.Address(), 250, 0)); err != nil {
		t.Fatalf("SubmitTx: %v", err)
	}

	blk, err := n.MineBlock()
	if err != nil {
		t.Fatalf("MineBlock: %v", err)
	}
	if blk.Header.Height != 1 {
		t.Fatalf("mined height = %d, want 1", blk.Header.Height)
	}
	head := n.Head()
	if head.Hash() != blk.Hash() {
		t.Fatalf("head not advanced to mined block")
	}
	if n.MempoolLen() != 0 {
		t.Fatalf("mempool not drained: %d", n.MempoolLen())
	}
	if got := n.Balance(a.Address()); got != 750 {
		t.Fatalf("A balance = %d, want 750", got)
	}
	if got := n.Balance(b.Address()); got != 250 {
		t.Fatalf("B balance = %d, want 250", got)
	}
	// A transfer block credits the recipient AND the miner's coinbase reward,
	// while the sender is debited; totals stay consistent.
	if got := n.Balance(n.MinerAddress()); got != chain.BlockReward {
		t.Fatalf("miner balance = %d, want %d (one block reward)", got, chain.BlockReward)
	}

	// The tx is retrievable by hash from the canonical chain.
	txHash := blk.Txs[0].Hash()
	if _, ok := n.GetTxByHash(txHash); !ok {
		t.Fatalf("GetTxByHash did not find mined tx")
	}
	if _, ok := n.GetBlockByHeight(1); !ok {
		t.Fatalf("GetBlockByHeight(1) not found")
	}
}

func TestPersistenceAcrossReload(t *testing.T) {
	a := newKeyT(t)
	b := newKeyT(t)
	miner := newKeyT(t)
	dbPath := filepath.Join(t.TempDir(), "chain.db")
	alloc := map[core.Address]uint64{a.Address(): 1000}

	// First instance: mine a transfer, then close.
	n1, err := New(Config{DBPath: dbPath, MinerKey: miner, Difficulty: testDifficulty, GenesisAlloc: alloc})
	if err != nil {
		t.Fatalf("New(1): %v", err)
	}
	if err := n1.SubmitTx(signedTransfer(a, b.Address(), 300, 0)); err != nil {
		t.Fatalf("SubmitTx: %v", err)
	}
	blk, err := n1.MineBlock()
	if err != nil {
		t.Fatalf("MineBlock: %v", err)
	}
	wantHead := blk.Hash()
	if err := n1.Close(); err != nil {
		t.Fatalf("Close(1): %v", err)
	}

	// Second instance: reload from the same DB; state must match.
	n2, err := New(Config{DBPath: dbPath, MinerKey: miner, Difficulty: testDifficulty, GenesisAlloc: alloc})
	if err != nil {
		t.Fatalf("New(2 reload): %v", err)
	}
	defer n2.Close()

	h2 := n2.Head()
	if h2.Hash() != wantHead {
		t.Fatalf("reloaded head = %s, want %s", h2.Hash().Hex(), wantHead.Hex())
	}
	if got := n2.Balance(a.Address()); got != 700 {
		t.Fatalf("reloaded A balance = %d, want 700", got)
	}
	if got := n2.Balance(b.Address()); got != 300 {
		t.Fatalf("reloaded B balance = %d, want 300", got)
	}
	if h := n2.Head().Header.Height; h != 1 {
		t.Fatalf("reloaded head height = %d, want 1", h)
	}
}

// TestMineBlockGrowsMinerBalanceByReward verifies the coinbase reward is now
// part of canonical state: each mined block increases the miner's balance by
// exactly BlockReward, even for empty (no-tx) blocks.
func TestMineBlockGrowsMinerBalanceByReward(t *testing.T) {
	n, err := New(Config{
		MinerKey:     newKeyT(t),
		Difficulty:   testDifficulty,
		GenesisAlloc: map[core.Address]uint64{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()

	miner := n.MinerAddress()
	if got := n.Balance(miner); got != 0 {
		t.Fatalf("pre-mine miner balance = %d, want 0", got)
	}
	for i := uint64(1); i <= 3; i++ {
		if _, err := n.MineBlock(); err != nil {
			t.Fatalf("MineBlock %d: %v", i, err)
		}
		if got, want := n.Balance(miner), i*chain.BlockReward; got != want {
			t.Fatalf("after %d blocks miner balance = %d, want %d", i, got, want)
		}
	}
}
