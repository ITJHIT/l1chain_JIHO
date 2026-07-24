package chain

import (
	"math/big"
	"testing"

	"l1chain/core"
	"l1chain/vm"
)

// counterCode: SLOAD slot0; +1; SSTORE slot0; STOP. Increments its own slot 0
// on every call. Gas: 3+200+3+3+3+5000 = 5212.
var counterCode = []byte{0x60, 0x00, 0x54, 0x60, 0x01, 0x01, 0x60, 0x00, 0x55, 0x00}

const counterCallGas = 5212

func slotHash(u uint64) core.Hash {
	var h core.Hash
	b := new(big.Int).SetUint64(u).Bytes()
	copy(h[core.HashLen-len(b):], b)
	return h
}

func newContractChain() (*Chain, core.Block, map[core.Address]uint64) {
	alloc := map[core.Address]uint64{addr(1): 10_000_000}
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0}
	gb := g.ToBlock()
	return NewChain(gb, alloc), gb, alloc
}

func deployTx(nonce uint64) core.Transaction {
	return core.Transaction{From: addr(1), To: core.Address{}, Nonce: nonce, GasLimit: 100000, Data: counterCode, Signature: []byte{1}}
}

func callTx(to core.Address, nonce, gas uint64) core.Transaction {
	return core.Transaction{From: addr(1), To: to, Nonce: nonce, GasLimit: gas, Signature: []byte{1}}
}

// TestContractDeployCallAndOOG exercises the full VM-through-chain path: deploy a
// counter contract, mine it, call it (slot increments, gas deducted), then send
// an out-of-gas call (storage unchanged, nonce still advances, gas consumed).
func TestContractDeployCallAndOOG(t *testing.T) {
	c, gb, alloc := newContractChain()
	contract := vm.CreateAddress(addr(1), 0)

	// Block 1: deploy.
	b1 := buildBlock(gb, nil, []core.Transaction{deployTx(0)}, testDiff, 1, alloc)
	if err := c.AddBlock(b1, acceptAll); err != nil {
		t.Fatalf("AddBlock deploy: %v", err)
	}
	if code := c.State().GetCode(contract); len(code) != len(counterCode) {
		t.Fatalf("deployed code len = %d, want %d", len(code), len(counterCode))
	}
	wantBal := uint64(10_000_000 - vm.GasCreate)
	if got := c.State().GetAccount(addr(1)).Balance; got != wantBal {
		t.Fatalf("after deploy sender balance = %d, want %d (gas %d)", got, wantBal, vm.GasCreate)
	}

	// Block 2: call — slot0 goes 0 -> 1, gas deducted.
	b2 := buildBlock(b1, []core.Block{b1}, []core.Transaction{callTx(contract, 1, 100000)}, testDiff, 2, alloc)
	if err := c.AddBlock(b2, acceptAll); err != nil {
		t.Fatalf("AddBlock call: %v", err)
	}
	if got := c.State().GetStorage(contract, slotHash(0)); got != slotHash(1) {
		t.Fatalf("contract slot0 = %x, want 1 after call", got)
	}
	wantBal -= counterCallGas
	if got := c.State().GetAccount(addr(1)).Balance; got != wantBal {
		t.Fatalf("after call sender balance = %d, want %d (gas %d)", got, wantBal, counterCallGas)
	}
	if got := c.State().GetAccount(addr(1)).Nonce; got != 2 {
		t.Fatalf("sender nonce = %d, want 2", got)
	}

	// Block 3: out-of-gas call — storage unchanged, nonce advances, gas consumed.
	b3 := buildBlock(b2, []core.Block{b1, b2}, []core.Transaction{callTx(contract, 2, 100)}, testDiff, 3, alloc)
	if err := c.AddBlock(b3, acceptAll); err != nil {
		t.Fatalf("AddBlock oog call: %v", err)
	}
	if got := c.State().GetStorage(contract, slotHash(0)); got != slotHash(1) {
		t.Fatalf("contract slot0 = %x, want STILL 1 after OOG call", got)
	}
	wantBal -= 100 // full GasLimit consumed on OOG
	if got := c.State().GetAccount(addr(1)).Balance; got != wantBal {
		t.Fatalf("after OOG sender balance = %d, want %d", got, wantBal)
	}
	if got := c.State().GetAccount(addr(1)).Nonce; got != 3 {
		t.Fatalf("sender nonce after OOG = %d, want 3 (must advance)", got)
	}
}

// TestContractBlockDeterministicReplay proves the mining/validation VM agreement
// seam: blocks whose StateRoot was produced by the mining-side derivation are
// accepted verbatim by AddBlock on a FRESH chain with the same genesis. If the
// VM were nondeterministic, the re-derived StateRoot would differ and AddBlock
// would reject with ErrBadStateRoot.
func TestContractBlockDeterministicReplay(t *testing.T) {
	cA, gb, alloc := newContractChain()
	contract := vm.CreateAddress(addr(1), 0)

	b1 := buildBlock(gb, nil, []core.Transaction{deployTx(0)}, testDiff, 1, alloc)
	b2 := buildBlock(b1, []core.Block{b1}, []core.Transaction{callTx(contract, 1, 100000)}, testDiff, 2, alloc)
	b3 := buildBlock(b2, []core.Block{b1, b2}, []core.Transaction{callTx(contract, 2, 100000)}, testDiff, 3, alloc)

	for _, b := range []core.Block{b1, b2, b3} {
		if err := cA.AddBlock(b, acceptAll); err != nil {
			t.Fatalf("chain A AddBlock height %d: %v", b.Header.Height, err)
		}
	}

	// Fresh chain B, identical genesis, replays the SAME mined blocks.
	cB := NewChain(gb, alloc)
	for _, b := range []core.Block{b1, b2, b3} {
		if err := cB.AddBlock(b, acceptAll); err != nil {
			t.Fatalf("chain B replay AddBlock height %d rejected: %v (mining/validation VM disagreement)", b.Header.Height, err)
		}
	}

	// Both chains must agree on the resulting contract storage and state root.
	if got := cB.State().GetStorage(contract, slotHash(0)); got != slotHash(2) {
		t.Fatalf("chain B slot0 = %x, want 2 (two calls)", got)
	}
	if cA.State().StateRoot() != cB.State().StateRoot() {
		t.Fatalf("chain A and B state roots diverged")
	}
}
