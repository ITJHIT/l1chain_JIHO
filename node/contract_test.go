package node

import (
	"math/big"
	"testing"

	"l1chain/chain"
	"l1chain/core"
	"l1chain/vm"
	"l1chain/wallet"
)

// counterCode increments the contract's own storage slot 0 on each call.
var counterCode = []byte{0x60, 0x00, 0x54, 0x60, 0x01, 0x01, 0x60, 0x00, 0x55, 0x00}

func slotHash(u uint64) core.Hash {
	var h core.Hash
	b := new(big.Int).SetUint64(u).Bytes()
	copy(h[core.HashLen-len(b):], b)
	return h
}

func signedContractTx(k wallet.Key, to core.Address, isCreate bool, nonce, gas uint64, data []byte) core.Transaction {
	tx := core.Transaction{Nonce: nonce, GasLimit: gas, ChainID: chain.DefaultChainID, Data: data}
	if !isCreate {
		tx.To = to
	}
	k.Sign(&tx)
	return tx
}

// TestNodeMinesContractBlocksAndReplays drives the whole contract path through
// the node: deploy + call are mined (each via MineBlock, whose deriveStateRoot
// runs the StackVM and whose AddBlock re-derives/validates the identical root),
// then the mined blocks are replayed onto a fresh chain to prove determinism.
func TestNodeMinesContractBlocksAndReplays(t *testing.T) {
	a := newKeyT(t)
	alloc := map[core.Address]uint64{a.Address(): 1_000_000}
	n, err := New(Config{
		MinerKey:         newKeyT(t),
		Difficulty:       testDifficulty,
		GenesisAlloc:     alloc,
		GenesisTimestamp: 1000, // fixed so a fresh replay chain builds an identical genesis
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()

	contract := vm.CreateAddress(a.Address(), 0)

	// Deploy.
	if err := n.SubmitTx(signedContractTx(a, core.Address{}, true, 0, 100000, counterCode)); err != nil {
		t.Fatalf("SubmitTx deploy: %v", err)
	}
	blk1, err := n.MineBlock()
	if err != nil {
		t.Fatalf("MineBlock deploy (StateRoot mining/validation agreement failed?): %v", err)
	}
	if code := n.chain.State().GetCode(contract); len(code) != len(counterCode) {
		t.Fatalf("deployed code len = %d, want %d", len(code), len(counterCode))
	}

	// Call.
	if err := n.SubmitTx(signedContractTx(a, contract, false, 1, 100000, nil)); err != nil {
		t.Fatalf("SubmitTx call: %v", err)
	}
	blk2, err := n.MineBlock()
	if err != nil {
		t.Fatalf("MineBlock call: %v", err)
	}
	if got := n.chain.State().GetStorage(contract, slotHash(0)); got != slotHash(1) {
		t.Fatalf("contract slot0 = %x, want 1 after call", got)
	}
	// Sender balance dropped by gas (create + call), no value transferred.
	wantBal := uint64(1_000_000 - vm.GasCreate - 5212)
	if got := n.Balance(a.Address()); got != wantBal {
		t.Fatalf("sender balance = %d, want %d", got, wantBal)
	}

	// Replay both mined blocks onto a fresh chain with the identical genesis.
	genesis, ok := n.GetBlockByHeight(0)
	if !ok {
		t.Fatal("missing genesis block")
	}
	cB := chain.NewChain(genesis, alloc)
	for _, b := range []core.Block{blk1, blk2} {
		if err := cB.AddBlock(b, wallet.Verify); err != nil {
			t.Fatalf("fresh-chain replay of mined block height %d rejected: %v", b.Header.Height, err)
		}
	}
	if got := cB.State().GetStorage(contract, slotHash(0)); got != slotHash(1) {
		t.Fatalf("replayed chain slot0 = %x, want 1", got)
	}
}
