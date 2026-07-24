package wallet

import (
	"testing"

	"l1chain/chain"
	"l1chain/consensus"
	"l1chain/core"
	"l1chain/state"
)

// integDiff is a tiny PoW difficulty so tests mine quickly.
const integDiff = 6

// fundedFromAlloc returns a fresh state funded with the genesis allocation.
func fundedFromAlloc(alloc map[core.Address]uint64) state.StateDB {
	st := state.NewMemStateDB()
	for a, bal := range alloc {
		acct := st.GetAccount(a)
		acct.Balance += bal
		st.SetAccount(a, acct)
	}
	return st
}

// mineBlock builds a valid block extending parent that applies txs, computing
// the state root from the alloc-funded base plus the coinbase reward and mining
// real PoW. The block is mined for core.Address{7}.
func mineBlock(t *testing.T, parent core.Block, alloc map[core.Address]uint64, txs []core.Transaction) core.Block {
	t.Helper()
	coinbase := core.Address{7}
	st := fundedFromAlloc(alloc)
	for i := range txs {
		if err := chain.ApplyTx(st, txs[i], Verify); err != nil {
			t.Fatalf("ApplyTx while building block: %v", err)
		}
	}
	m := st.GetAccount(coinbase)
	m.Balance += chain.BlockReward
	st.SetAccount(coinbase, m)
	b := core.Block{
		Header: core.Header{
			Height:     parent.Header.Height + 1,
			PrevHash:   parent.Hash(),
			Coinbase:   coinbase,
			Difficulty: integDiff,
			Timestamp:  1,
		},
		Txs: txs,
	}
	b.Header.MerkleRoot = b.TxRoot()
	b.Header.StateRoot = st.StateRoot()
	consensus.Mine(&b.Header, 0)
	return b
}

func TestChainAcceptsRealSignedTransfer(t *testing.T) {
	walletA, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey A: %v", err)
	}
	walletB, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey B: %v", err)
	}

	alloc := map[core.Address]uint64{walletA.Address(): 1000}
	genesis := chain.Genesis{Alloc: alloc, Difficulty: integDiff, Timestamp: 0}
	gb := genesis.ToBlock()
	c := chain.NewChain(gb, alloc)

	// walletA -> walletB transfer, signed by the real key.
	txn := core.Transaction{From: walletA.Address(), To: walletB.Address(), Value: 250, Nonce: 0, ChainID: chain.DefaultChainID}
	walletA.Sign(&txn)
	if !Verify(txn) {
		t.Fatalf("signed transfer failed Verify before chain application")
	}

	// ApplyBlock path accepts it against a funded state.
	stDirect := fundedFromAlloc(alloc)
	blk := mineBlock(t, gb, alloc, []core.Transaction{txn})
	if err := chain.ApplyBlock(stDirect, blk, core.Address{7}, Verify); err != nil {
		t.Fatalf("ApplyBlock rejected valid signed tx: %v", err)
	}
	if got := stDirect.GetAccount(walletB.Address()).Balance; got != 250 {
		t.Fatalf("walletB balance after ApplyBlock = %d, want 250", got)
	}

	// AddBlock path (full chain engine) accepts it.
	if err := c.AddBlock(blk, Verify); err != nil {
		t.Fatalf("AddBlock rejected valid signed tx: %v", err)
	}
	head := c.Head()
	if head.Hash() != blk.Hash() {
		t.Fatalf("head not advanced to signed block")
	}
	if got := c.State().GetAccount(walletB.Address()).Balance; got != 250 {
		t.Fatalf("walletB balance on chain = %d, want 250", got)
	}
}

func TestChainRejectsTamperedSignedTransfer(t *testing.T) {
	walletA, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey A: %v", err)
	}
	walletB, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey B: %v", err)
	}

	alloc := map[core.Address]uint64{walletA.Address(): 1000}
	genesis := chain.Genesis{Alloc: alloc, Difficulty: integDiff, Timestamp: 0}
	gb := genesis.ToBlock()
	c := chain.NewChain(gb, alloc)

	txn := core.Transaction{From: walletA.Address(), To: walletB.Address(), Value: 250, Nonce: 0, ChainID: chain.DefaultChainID}
	walletA.Sign(&txn)
	// Tamper the value after signing; the signature no longer matches.
	txn.Value = 900

	// Mine a block carrying the tampered tx. Its state root is derived assuming
	// the tx applies, but the chain re-verifies with wallet.Verify and rejects.
	b := core.Block{
		Header: core.Header{
			Height:     gb.Header.Height + 1,
			PrevHash:   gb.Hash(),
			Difficulty: integDiff,
			Timestamp:  1,
		},
		Txs: []core.Transaction{txn},
	}
	b.Header.MerkleRoot = b.TxRoot()
	// Compute a plausible state root as if it applied (won't matter; sig check fails first).
	st := fundedFromAlloc(alloc)
	acctA := st.GetAccount(walletA.Address())
	acctA.Balance -= txn.Value
	acctA.Nonce++
	st.SetAccount(walletA.Address(), acctA)
	acctB := st.GetAccount(walletB.Address())
	acctB.Balance += txn.Value
	st.SetAccount(walletB.Address(), acctB)
	b.Header.StateRoot = st.StateRoot()
	consensus.Mine(&b.Header, 0)

	if err := c.AddBlock(b, Verify); err == nil {
		t.Fatalf("AddBlock accepted tampered signed tx, want rejection")
	}
	head := c.Head()
	if head.Hash() != gb.Hash() {
		t.Fatalf("head advanced on rejected tampered block")
	}

	// The direct ApplyBlock path rejects it too.
	if err := chain.ApplyBlock(fundedFromAlloc(alloc), b, core.Address{7}, Verify); err == nil {
		t.Fatalf("ApplyBlock accepted tampered signed tx, want rejection")
	}
}
