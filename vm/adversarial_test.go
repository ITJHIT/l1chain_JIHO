package vm

// Adversarial red-team suite for the M3 StackVM.
//
// Every case drives malicious or malformed bytecode through the real
// StackVM.Execute path (the same entrypoint block validation uses) and asserts
// the four safety invariants the M3 VM must uphold under attack:
//
//   1. no crash        — a panic here fails the test; a hostile contract must
//                        never take the node process down.
//   2. gas-bounded     — unbounded work (infinite loops, deep recursion) must
//                        terminate via ErrOutOfGas with GasUsed == GasLimit.
//   3. no corruption   — a reverting/OOG frame commits nothing, and one
//                        contract can never read or clobber another's storage.
//   4. deterministic   — identical input against identical state yields an
//                        identical StateRoot on every run.
//
// These tests add NO production changes and only ADD test coverage; they reuse
// the in-package helpers from stackvm_test.go (execCode, withRet, retSuffix,
// hashOfUint, callerToB, counter).

import (
	"bytes"
	"errors"
	"testing"

	"l1chain/core"
	"l1chain/state"
)

// deployTwo builds a fresh StateDB with a funded sender and two contracts
// (codeA at addrA, codeB at addrB), then returns it so a message can be run and
// its storage/state root inspected.
func deployTwo(codeA, codeB []byte) (state.StateDB, core.Address, core.Address, core.Address) {
	st := state.NewMemStateDB()
	from := core.Address{0x01}
	addrA := core.Address{0x0a}
	addrB := core.Address{0x0b}
	st.SetAccount(from, state.Account{Balance: 1_000_000_000})
	st.SetCode(addrA, codeA)
	st.SetCode(addrB, codeB)
	return st, from, addrA, addrB
}

// ---------------------------------------------------------------------------
// Case 1: infinite loop -> terminates via out-of-gas, not a hang.
// ---------------------------------------------------------------------------

func TestAdvVM01InfiniteLoopOutOfGas(t *testing.T) {
	// SSTORE slot0=7, then JUMPDEST; PUSH1 5; JUMP  -> spins forever.
	// The pre-loop SSTORE lets us prove the OOG revert wipes an already-applied
	// storage write.
	//   [0]0x60 0x07  PUSH1 7
	//   [2]0x60 0x00  PUSH1 0
	//   [4]0x55       SSTORE            (slot0 = 7)
	//   [5]0x5b       JUMPDEST          <- jump target
	//   [6]0x60 0x05  PUSH1 5
	//   [8]0x56       JUMP
	code := []byte{0x60, 0x07, 0x60, 0x00, 0x55, 0x5b, 0x60, 0x05, 0x56}
	const gasLimit = 1_000_000
	r, st, to := execCode(code, nil, gasLimit)

	if r.Success {
		t.Fatal("infinite loop must not succeed")
	}
	if !errors.Is(r.Err, ErrOutOfGas) {
		t.Fatalf("err = %v, want ErrOutOfGas (loop must terminate via gas, not hang)", r.Err)
	}
	if r.GasUsed != gasLimit {
		t.Fatalf("GasUsed = %d, want GasLimit %d on OOG", r.GasUsed, gasLimit)
	}
	if got := st.GetStorage(to, hashOfUint(0)); !got.IsZero() {
		t.Fatalf("slot0 = %x, want zero (OOG must revert the pre-loop SSTORE)", got)
	}
}

// ---------------------------------------------------------------------------
// Case 2: stack underflow and stack overflow -> guarded errors, no panic.
// ---------------------------------------------------------------------------

func TestAdvVM02StackUnderflowAndOverflow(t *testing.T) {
	// Underflow: ADD / SUB / MUL / SSTORE / JUMP with an empty stack.
	underflow := [][]byte{
		{0x01},             // ADD
		{0x03},             // SUB
		{0x02},             // MUL
		{0x55},             // SSTORE (needs 2 words)
		{0x56},             // JUMP (needs 1 word)
		{0x60, 0x00, 0x55}, // PUSH1 0; SSTORE -> only one word, value pop underflows
	}
	for i, code := range underflow {
		r, _, _ := execCode(code, nil, 1_000_000)
		if r.Success || !errors.Is(r.Err, ErrStackUnderflow) {
			t.Fatalf("underflow[%d] %x: success=%v err=%v, want ErrStackUnderflow", i, code, r.Success, r.Err)
		}
	}

	// Overflow: 1025 straight-line PUSH1 ops exceed MaxStack (1024).
	over := make([]byte, 0, (MaxStack+1)*2)
	for i := 0; i < MaxStack+1; i++ {
		over = append(over, 0x60, 0x01) // PUSH1 1
	}
	r, _, _ := execCode(over, nil, 1_000_000)
	if r.Success || !errors.Is(r.Err, ErrStackOverflow) {
		t.Fatalf("overflow: success=%v err=%v, want ErrStackOverflow", r.Success, r.Err)
	}
}

// ---------------------------------------------------------------------------
// Case 3: invalid JUMP destinations -> revert with ErrInvalidJump, no panic.
// ---------------------------------------------------------------------------

func TestAdvVM03InvalidJumpDestinations(t *testing.T) {
	cases := []struct {
		name string
		code []byte
	}{
		{
			// Jump to a non-JUMPDEST opcode (offset 3 is STOP).
			name: "non-jumpdest",
			code: []byte{0x60, 0x03, 0x56, 0x00},
		},
		{
			// Jump far out of bounds (offset 255 >> len(code)).
			name: "out-of-bounds",
			code: []byte{0x60, 0xff, 0x56},
		},
		{
			// Jump into PUSH data: offset 5 holds 0x5b but it is the immediate
			// of PUSH2 at offset 4, NOT a real JUMPDEST.
			//   [0]PUSH1 5 [2]JUMP [3]STOP [4]PUSH2 [5]0x5b [6]0x00
			name: "into-push-data",
			code: []byte{0x60, 0x05, 0x56, 0x00, 0x61, 0x5b, 0x00},
		},
		{
			// Conditional jump (JUMPI) taken to a bad destination.
			//   PUSH1 1 (cond) PUSH1 9 (dest) JUMPI STOP
			name: "jumpi-bad-dest",
			code: []byte{0x60, 0x01, 0x60, 0x09, 0x57, 0x00},
		},
	}
	for _, c := range cases {
		r, _, _ := execCode(c.code, nil, 1_000_000)
		if r.Success || !errors.Is(r.Err, ErrInvalidJump) {
			t.Fatalf("%s %x: success=%v err=%v, want ErrInvalidJump", c.name, c.code, r.Success, r.Err)
		}
	}
}

// ---------------------------------------------------------------------------
// Case 4: storage isolation -> a contract only ever sees its OWN storage.
// ---------------------------------------------------------------------------

func TestAdvVM04StorageIsolation(t *testing.T) {
	// A writes slot0 = 0x2a. B reads slot0 and RETURNs it.
	codeA := []byte{0x60, 0x2a, 0x60, 0x00, 0x55, 0x00}     // PUSH1 42; PUSH1 0; SSTORE; STOP
	codeB := withRet(0x60, 0x00, 0x54)                      // PUSH1 0; SLOAD; RETURN(slot0)
	st, from, addrA, addrB := deployTwo(codeA, codeB)

	// Run A -> A.slot0 becomes 42.
	rA := StackVM{}.Execute(st, Message{From: from, To: &addrA, GasLimit: 1_000_000})
	if !rA.Success {
		t.Fatalf("A failed: %v", rA.Err)
	}
	if got := st.GetStorage(addrA, hashOfUint(0)); got != hashOfUint(42) {
		t.Fatalf("A slot0 = %x, want 42", got)
	}

	// Run B -> it must read its OWN (empty) slot0, i.e. 0, not A's 42.
	rB := StackVM{}.Execute(st, Message{From: from, To: &addrB, GasLimit: 1_000_000})
	if !rB.Success {
		t.Fatalf("B failed: %v", rB.Err)
	}
	if !bytes.Equal(rB.ReturnData, make([]byte, 32)) {
		t.Fatalf("B read slot0 = %x, want 32 zero bytes (cross-contract storage leak!)", rB.ReturnData)
	}
	// B must not have created/altered A's storage either.
	if got := st.GetStorage(addrA, hashOfUint(0)); got != hashOfUint(42) {
		t.Fatalf("A slot0 = %x after B ran, want still 42 (cross-contract corruption!)", got)
	}
	if got := st.GetStorage(addrB, hashOfUint(0)); !got.IsZero() {
		t.Fatalf("B slot0 = %x, want zero (B never wrote it)", got)
	}
}

// ---------------------------------------------------------------------------
// Case 5 (VM half): OOG after a committed-in-frame SSTORE reverts that write.
// (The chain-level half — nonce advances, gas consumed — is in chain/adversarial_test.go.)
// ---------------------------------------------------------------------------

func TestAdvVM05OutOfGasMidExecutionRevertsSStore(t *testing.T) {
	// First SSTORE (slot0=7) is affordable; the second (slot1=9) runs the meter
	// dry. The whole message must revert -> BOTH slots unchanged.
	code := []byte{
		0x60, 0x07, 0x60, 0x00, 0x55, // slot0 = 7   (~5006 gas)
		0x60, 0x09, 0x60, 0x01, 0x55, // slot1 = 9   (needs ~5006 more)
		0x00,
	}
	const gasLimit = 6000
	r, st, to := execCode(code, nil, gasLimit)
	if r.Success || !errors.Is(r.Err, ErrOutOfGas) {
		t.Fatalf("success=%v err=%v, want ErrOutOfGas mid-execution", r.Success, r.Err)
	}
	if r.GasUsed != gasLimit {
		t.Fatalf("GasUsed = %d, want %d", r.GasUsed, gasLimit)
	}
	if got := st.GetStorage(to, hashOfUint(0)); !got.IsZero() {
		t.Fatalf("slot0 = %x, want zero (the first SSTORE must be reverted by the later OOG)", got)
	}
	if got := st.GetStorage(to, hashOfUint(1)); !got.IsZero() {
		t.Fatalf("slot1 = %x, want zero", got)
	}
}

// ---------------------------------------------------------------------------
// Case 6 (VM half): identical execution -> identical StateRoot (determinism).
// The mining==validation half lives in chain/adversarial_test.go.
// ---------------------------------------------------------------------------

func TestAdvVM06DeterministicRepeatedExecution(t *testing.T) {
	// A non-trivial contract exercising arithmetic + SSTORE + CALL:
	//   PUSH1 5; PUSH1 3; ADD; PUSH1 0; SSTORE  (slot0 = 8)
	//   CALL counter@B; POP; STOP               (B.slot0 += 1)
	addrB := core.Address{0x0b}
	mixedPrefix := []byte{0x60, 0x05, 0x60, 0x03, 0x01, 0x60, 0x00, 0x55}
	codeA := append(append([]byte{}, mixedPrefix...), callerToB(addrB, 0x50, 0x00)...)
	codeB := counter

	run := func() core.Hash {
		st, from, addrA, _ := deployTwo(codeA, codeB)
		r := StackVM{}.Execute(st, Message{From: from, To: &addrA, GasLimit: 10_000_000})
		if !r.Success {
			t.Fatalf("mixed contract failed: %v", r.Err)
		}
		if got := st.GetStorage(addrA, hashOfUint(0)); got != hashOfUint(8) {
			t.Fatalf("A slot0 = %x, want 8 (arithmetic 5+3)", got)
		}
		if got := st.GetStorage(addrB, hashOfUint(0)); got != hashOfUint(1) {
			t.Fatalf("B slot0 = %x, want 1 (CALL ran the counter)", got)
		}
		return st.StateRoot()
	}

	root1 := run()
	root2 := run()
	if root1 != root2 {
		t.Fatalf("nondeterministic StateRoot: %x != %x", root1, root2)
	}
}

// ---------------------------------------------------------------------------
// Case 7: reentrancy via CALL -> bounded by call depth and shared gas.
// ---------------------------------------------------------------------------

func TestAdvVM07ReentrancyBoundedByDepthAndGas(t *testing.T) {
	addrA := core.Address{0x0a}
	addrB := core.Address{0x0b}
	// A calls B; B calls back into A; mutually recursive with no base case.
	codeA := append(callerToB(addrB, 0x50), 0x00) // CALL B; POP; STOP
	codeB := append(callerToB(addrA, 0x50), 0x00) // CALL A; POP; STOP

	// (a) With generous gas the recursion terminates at the call-depth limit
	//     (deeper CALLs push 0 without executing) and unwinds successfully — it
	//     does NOT recurse forever and does NOT panic.
	{
		st, from, a, _ := deployTwo(codeA, codeB)
		_ = a
		r := StackVM{}.Execute(st, Message{From: from, To: &addrA, GasLimit: 500_000_000})
		if !r.Success {
			t.Fatalf("depth-bounded reentrancy should succeed, got err=%v", r.Err)
		}
	}

	// (b) With a tight gas budget the shared meter bounds the recursion: it OOGs
	//     with GasUsed == GasLimit rather than hanging or panicking.
	{
		st, from, _, _ := deployTwo(codeA, codeB)
		const gasLimit = 100_000
		r := StackVM{}.Execute(st, Message{From: from, To: &addrA, GasLimit: gasLimit})
		if r.Success || !errors.Is(r.Err, ErrOutOfGas) {
			t.Fatalf("gas-starved reentrancy: success=%v err=%v, want ErrOutOfGas", r.Success, r.Err)
		}
		if r.GasUsed != gasLimit {
			t.Fatalf("GasUsed = %d, want GasLimit %d", r.GasUsed, gasLimit)
		}
	}

	// (c) Determinism: the depth-bounded reentrancy yields an identical StateRoot
	//     across independent runs.
	rootOf := func() core.Hash {
		st, from, _, _ := deployTwo(codeA, codeB)
		StackVM{}.Execute(st, Message{From: from, To: &addrA, GasLimit: 500_000_000})
		return st.StateRoot()
	}
	if r1, r2 := rootOf(), rootOf(); r1 != r2 {
		t.Fatalf("reentrancy nondeterministic: %x != %x", r1, r2)
	}
}

// ---------------------------------------------------------------------------
// Case 8: truncated PUSH immediate and out-of-range CALLDATALOAD -> zero-padded.
// ---------------------------------------------------------------------------

func TestAdvVM08TruncatedPushAndCalldataBounds(t *testing.T) {
	// (a) PUSH32 with only one immediate byte before end-of-code. The VM must
	//     zero-pad the missing 31 bytes and finish without an out-of-range panic.
	{
		code := []byte{0x7f, 0x01} // PUSH32 <1 byte, truncated>
		r, _, _ := execCode(code, nil, 1_000_000)
		if !r.Success {
			t.Fatalf("truncated PUSH32 must not fail: %v", r.Err)
		}
	}

	// (b) CALLDATALOAD entirely past the end of calldata -> zero word.
	{
		code := withRet(0x60, 0xff, 0x35) // PUSH1 255; CALLDATALOAD; RETURN
		r, _, _ := execCode(code, nil, 1_000_000)
		if !r.Success {
			t.Fatalf("out-of-range CALLDATALOAD must not fail: %v", r.Err)
		}
		if !bytes.Equal(r.ReturnData, make([]byte, 32)) {
			t.Fatalf("CALLDATALOAD past end = %x, want 32 zero bytes", r.ReturnData)
		}
	}

	// (c) CALLDATALOAD straddling the end -> the in-range bytes then zero-padding.
	{
		code := withRet(0x60, 0x00, 0x35) // PUSH1 0; CALLDATALOAD; RETURN(word@0)
		calldata := []byte{0xaa, 0xbb, 0xcc, 0xdd}
		r, _, _ := execCode(code, calldata, 1_000_000)
		if !r.Success {
			t.Fatalf("straddling CALLDATALOAD failed: %v", r.Err)
		}
		want := make([]byte, 32)
		copy(want, calldata) // 0xaabbccdd then zero padding
		if !bytes.Equal(r.ReturnData, want) {
			t.Fatalf("CALLDATALOAD@0 = %x, want %x (in-range bytes then zero pad)", r.ReturnData, want)
		}
	}
}
