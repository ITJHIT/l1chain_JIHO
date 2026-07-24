package chain

// Chain-level adversarial red-team cases for the M3 VM-through-chain path.
//
// These complement vm/adversarial_test.go by driving malicious contract
// bytecode through the FULL state-transition + block-validation pipeline
// (ApplyTx -> applyContractTx -> StackVM, then AddBlock's StateRoot
// re-derivation). They assert node-level safety: an out-of-gas contract still
// advances the sender nonce and consumes gas while reverting storage, and the
// VM is deterministic across independent mining and validation derivations.
//
// No production code is modified; only tests are added. They reuse the
// in-package helpers addr, testDiff, buildBlock, acceptAll, newContractChain.

import (
	"testing"

	"l1chain/core"
	"l1chain/vm"
)

// callSeq emits the 7-arg CALL to `to` with no value/args/ret, then `after`.
func callSeq(to core.Address, after ...byte) []byte {
	code := []byte{
		0x60, 0x00, // retSize
		0x60, 0x00, // retOffset
		0x60, 0x00, // argsSize
		0x60, 0x00, // argsOffset
		0x60, 0x00, // value
		0x73, // PUSH20 addr
	}
	code = append(code, to[:]...)
	code = append(code, 0x60, 0x00) // gas
	code = append(code, 0xf1)       // CALL
	code = append(code, after...)
	return code
}

func rawTx(to core.Address, nonce, gas uint64, data []byte) core.Transaction {
	return core.Transaction{From: addr(1), To: to, Nonce: nonce, GasLimit: gas, Data: data, Signature: []byte{1}}
}

// TestAdvChain05OOGCallAdvancesNonceConsumesGasRevertsStorage proves node-level
// out-of-gas semantics for a MID-EXECUTION OOG (after a successful SSTORE):
// storage is reverted, but the sender nonce advances and the full gas budget is
// consumed — a malicious contract cannot avoid paying or stall the nonce.
func TestAdvChain05OOGCallAdvancesNonceConsumesGasRevertsStorage(t *testing.T) {
	c, gb, alloc := newContractChain()

	// Contract: slot0 = 7 (affordable) then slot1 = 9 (runs the meter dry).
	oogCode := []byte{
		0x60, 0x07, 0x60, 0x00, 0x55, // SSTORE slot0 = 7
		0x60, 0x09, 0x60, 0x01, 0x55, // SSTORE slot1 = 9 -> OOG here
		0x00,
	}
	contract := vm.CreateAddress(addr(1), 0)

	// Block 1: deploy.
	b1 := buildBlock(gb, nil, []core.Transaction{rawTx(core.Address{}, 0, 100000, oogCode)}, testDiff, 1, alloc)
	if err := c.AddBlock(b1, acceptAll); err != nil {
		t.Fatalf("AddBlock deploy: %v", err)
	}
	balAfterDeploy := uint64(10_000_000 - vm.GasCreate)
	if got := c.State().GetAccount(addr(1)).Balance; got != balAfterDeploy {
		t.Fatalf("balance after deploy = %d, want %d", got, balAfterDeploy)
	}

	// Block 2: OOG call with just enough gas for the first SSTORE, not the second.
	const callGas = 6000
	b2 := buildBlock(b1, []core.Block{b1}, []core.Transaction{rawTx(contract, 1, callGas, nil)}, testDiff, 2, alloc)
	if err := c.AddBlock(b2, acceptAll); err != nil {
		t.Fatalf("AddBlock oog call: %v", err)
	}

	// Storage reverted: neither slot committed.
	if got := c.State().GetStorage(contract, slotHash(0)); !got.IsZero() {
		t.Fatalf("slot0 = %x, want zero (mid-exec OOG must revert the first SSTORE)", got)
	}
	if got := c.State().GetStorage(contract, slotHash(1)); !got.IsZero() {
		t.Fatalf("slot1 = %x, want zero", got)
	}
	// Nonce advanced despite the failure.
	if got := c.State().GetAccount(addr(1)).Nonce; got != 2 {
		t.Fatalf("nonce = %d, want 2 (OOG call must still advance nonce)", got)
	}
	// Full call gas consumed (no refund on OOG).
	wantBal := balAfterDeploy - callGas
	if got := c.State().GetAccount(addr(1)).Balance; got != wantBal {
		t.Fatalf("balance after OOG call = %d, want %d (full %d gas consumed)", got, wantBal, callGas)
	}
}

// TestAdvChain06AdversarialContractMiningEqualsValidation proves determinism of
// a non-trivial contract (arithmetic + SSTORE + inter-contract CALL) across the
// mining-side StateRoot derivation (buildBlock) and the validation-side
// re-derivation (AddBlock) on a FRESH chain with the same genesis. Any VM
// nondeterminism would make chain B reject a block with ErrBadStateRoot.
func TestAdvChain06AdversarialContractMiningEqualsValidation(t *testing.T) {
	cA, gb, alloc := newContractChain()

	callee := vm.CreateAddress(addr(1), 0) // counter, deployed at nonce 0
	caller := vm.CreateAddress(addr(1), 1) // mixed contract, deployed at nonce 1

	// mixed: PUSH1 5; PUSH1 3; ADD; PUSH1 0; SSTORE (slot0=8); CALL callee; POP; STOP.
	mixedCode := append([]byte{0x60, 0x05, 0x60, 0x03, 0x01, 0x60, 0x00, 0x55}, callSeq(callee, 0x50, 0x00)...)

	b1 := buildBlock(gb, nil, []core.Transaction{rawTx(core.Address{}, 0, 100000, counterCode)}, testDiff, 1, alloc)
	b2 := buildBlock(b1, []core.Block{b1}, []core.Transaction{rawTx(core.Address{}, 1, 100000, mixedCode)}, testDiff, 2, alloc)
	b3 := buildBlock(b2, []core.Block{b1, b2}, []core.Transaction{rawTx(caller, 2, 100000, nil)}, testDiff, 3, alloc)

	for _, b := range []core.Block{b1, b2, b3} {
		if err := cA.AddBlock(b, acceptAll); err != nil {
			t.Fatalf("chain A AddBlock height %d: %v", b.Header.Height, err)
		}
	}

	// Expected post-state: caller slot0 == 8 (arithmetic), callee slot0 == 1 (CALL).
	if got := cA.State().GetStorage(caller, slotHash(0)); got != slotHash(8) {
		t.Fatalf("caller slot0 = %x, want 8", got)
	}
	if got := cA.State().GetStorage(callee, slotHash(0)); got != slotHash(1) {
		t.Fatalf("callee slot0 = %x, want 1", got)
	}

	// Fresh chain B, same genesis, replays the identical mined blocks. If the VM
	// were nondeterministic the re-derived StateRoot would differ -> rejection.
	cB := NewChain(gb, alloc)
	for _, b := range []core.Block{b1, b2, b3} {
		if err := cB.AddBlock(b, acceptAll); err != nil {
			t.Fatalf("chain B replay height %d rejected: %v (mining/validation VM disagreement)", b.Header.Height, err)
		}
	}
	if cA.State().StateRoot() != cB.State().StateRoot() {
		t.Fatalf("chain A and B StateRoots diverged (nondeterministic VM)")
	}
}
