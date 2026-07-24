package evm

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// This file is the M4 red-team / adversarial suite for the go-ethereum-embedded
// EVM harness (evm/runtime.go) and the ERC-20 fixture (evm/erc20_fixture.go).
//
// It drives malicious and malformed contracts / calldata through the REAL
// Harness.Deploy / Harness.Call path — the same entrypoints M4 exercises — and
// asserts the four safety invariants under attack:
//   (1) no harness crash / panic,
//   (2) gas-bounded termination (OOG error, never a hang),
//   (3) reverts / OOG frames leave state unchanged (balances + totalSupply),
//   (4) determinism (identical balances AND identical MPT state root across
//       independent fresh states).
//
// No production evm/ code is modified; this file only ADDS test coverage and
// reuses the in-package helpers (deployToken, balanceOf, deployer, recipient,
// deployGas, callGas, Encode*/Decode*).

var attacker = common.HexToAddress("0x3333333333333333333333333333333333333333")

// totalSupply reads the ERC-20 totalSupply() view.
func totalSupply(t *testing.T, h *Harness, token common.Address) *big.Int {
	t.Helper()
	ret, _, err := h.Call(deployer, token, EncodeTotalSupply(), callGas)
	if err != nil {
		t.Fatalf("totalSupply: %v", err)
	}
	return DecodeUint256(ret)
}

// wrapRuntime wraps a runtime byte-slice (len < 256) in a minimal, fixed-length
// (12-byte) constructor that CODECOPYs the runtime and RETURNs it, yielding
// deployable creation bytecode.
func wrapRuntime(runtime []byte) []byte {
	n := byte(len(runtime))
	prefix := []byte{
		0x60, n, // PUSH1 len
		0x60, 0x0c, // PUSH1 12  (offset of runtime within this code)
		0x60, 0x00, // PUSH1 0   (dest mem)
		0x39,       // CODECOPY
		0x60, n,    // PUSH1 len
		0x60, 0x00, // PUSH1 0
		0xf3, // RETURN
	}
	return append(prefix, runtime...)
}

// mustNotPanic runs fn and fails the test (instead of crashing the process) if
// fn panics — turning "harness crash" into explicit, recorded evidence.
func mustNotPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("HARNESS CRASH in %s: panic=%v", name, r)
		}
	}()
	fn()
}

// ---------------------------------------------------------------------------
// Case 1: out-of-gas deploy AND out-of-gas call.
// ---------------------------------------------------------------------------

func TestAdvEVM01OutOfGasDeployAndCall(t *testing.T) {
	// (a) OOG deploy: deploy the ERC-20 with an intentionally tiny gas budget.
	h, err := NewHarness()
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	h.Fund(deployer, uint256.NewInt(1_000_000_000_000_000_000))

	var addr common.Address
	var derr error
	mustNotPanic(t, "OOG-deploy", func() {
		addr, _, derr = h.Deploy(deployer, ERC20CreationCode(), 50)
	})
	if derr == nil {
		t.Fatalf("OOG deploy unexpectedly succeeded (addr=%s)", addr.Hex())
	}
	if code := h.Code(addr); len(code) != 0 {
		t.Fatalf("OOG deploy left partial runtime code (%d bytes) at %s", len(code), addr.Hex())
	}
	t.Logf("OOG deploy: err=%v; no partial contract at %s", derr, addr.Hex())

	// (b) OOG call: deploy normally, then call transfer with a tiny gas budget.
	h2, token := deployToken(t)
	dBefore := balanceOf(t, h2, token, deployer)
	rBefore := balanceOf(t, h2, token, recipient)
	tsBefore := totalSupply(t, h2, token)

	var cerr error
	mustNotPanic(t, "OOG-call", func() {
		_, _, cerr = h2.Call(deployer, token, EncodeTransfer(recipient, big.NewInt(1)), 50)
	})
	if cerr == nil {
		t.Fatal("OOG transfer call unexpectedly succeeded")
	}
	if got := balanceOf(t, h2, token, deployer); got.Cmp(dBefore) != 0 {
		t.Fatalf("deployer balance changed after OOG call: %s != %s", got, dBefore)
	}
	if got := balanceOf(t, h2, token, recipient); got.Cmp(rBefore) != 0 {
		t.Fatalf("recipient balance changed after OOG call: %s != %s", got, rBefore)
	}
	if got := totalSupply(t, h2, token); got.Cmp(tsBefore) != 0 {
		t.Fatalf("totalSupply changed after OOG call: %s != %s", got, tsBefore)
	}
	t.Logf("OOG call: err=%v; balances + totalSupply unchanged", cerr)
}

// ---------------------------------------------------------------------------
// Case 2: transfer-to-self and transfer-of-zero preserve invariants.
// ---------------------------------------------------------------------------

func TestAdvEVM02SelfTransferAndZeroTransfer(t *testing.T) {
	h, token := deployToken(t)
	start := balanceOf(t, h, token, deployer)
	tsStart := totalSupply(t, h, token)

	// (a) transfer to self: balance must be net-zero unchanged.
	ret, _, err := h.Call(deployer, token, EncodeTransfer(deployer, big.NewInt(1000)), callGas)
	if err != nil {
		t.Fatalf("self-transfer: %v", err)
	}
	if DecodeUint256(ret).Sign() == 0 {
		t.Fatal("self-transfer returned false")
	}
	if got := balanceOf(t, h, token, deployer); got.Cmp(start) != 0 {
		t.Fatalf("self-transfer corrupted balance: %s != %s", got, start)
	}

	// (b) transfer of zero to a fresh recipient: both balances net-correct.
	ret, _, err = h.Call(deployer, token, EncodeTransfer(recipient, big.NewInt(0)), callGas)
	if err != nil {
		t.Fatalf("zero-transfer: %v", err)
	}
	if DecodeUint256(ret).Sign() == 0 {
		t.Fatal("zero-transfer returned false")
	}
	if got := balanceOf(t, h, token, deployer); got.Cmp(start) != 0 {
		t.Fatalf("zero-transfer changed deployer balance: %s != %s", got, start)
	}
	if got := balanceOf(t, h, token, recipient); got.Sign() != 0 {
		t.Fatalf("zero-transfer credited recipient: %s", got)
	}
	if got := totalSupply(t, h, token); got.Cmp(tsStart) != 0 {
		t.Fatalf("totalSupply drifted: %s != %s", got, tsStart)
	}
	t.Logf("self/zero transfer ok: balance=%s totalSupply=%s", start, tsStart)
}

// ---------------------------------------------------------------------------
// Case 3: overspend reverts -> balances AND totalSupply invariant preserved.
// ---------------------------------------------------------------------------

func TestAdvEVM03OverspendRevertPreservesSupply(t *testing.T) {
	h, token := deployToken(t)
	dBefore := balanceOf(t, h, token, deployer)
	rBefore := balanceOf(t, h, token, recipient)
	tsBefore := totalSupply(t, h, token)

	tooMuch := new(big.Int).Add(ERC20InitialSupply, big.NewInt(1))
	ret, _, err := h.Call(deployer, token, EncodeTransfer(recipient, tooMuch), callGas)
	if err == nil && DecodeUint256(ret).Sign() != 0 {
		t.Fatal("over-balance transfer unexpectedly succeeded")
	}

	if got := balanceOf(t, h, token, deployer); got.Cmp(dBefore) != 0 {
		t.Fatalf("deployer balance changed on revert: %s != %s", got, dBefore)
	}
	if got := balanceOf(t, h, token, recipient); got.Cmp(rBefore) != 0 {
		t.Fatalf("recipient balance changed on revert: %s != %s", got, rBefore)
	}
	tsAfter := totalSupply(t, h, token)
	if tsAfter.Cmp(tsBefore) != 0 {
		t.Fatalf("totalSupply changed on revert: %s != %s", tsAfter, tsBefore)
	}
	if tsAfter.Cmp(ERC20InitialSupply) != 0 {
		t.Fatalf("totalSupply invariant broken: %s != initial %s", tsAfter, ERC20InitialSupply)
	}
	t.Logf("overspend reverted (err=%v); balances + totalSupply(%s) invariant held", err, tsAfter)
}

// ---------------------------------------------------------------------------
// Case 4: malicious infinite-loop contract is gas-bounded, no hang, no panic.
// ---------------------------------------------------------------------------

func TestAdvEVM04InfiniteLoopGasBounded(t *testing.T) {
	h, err := NewHarness()
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	h.Fund(deployer, uint256.NewInt(1_000_000_000_000_000_000))

	// runtime: JUMPDEST; PUSH1 0; JUMP  -> unconditional infinite loop.
	loop := []byte{0x5b, 0x60, 0x00, 0x56}
	var loopAddr common.Address
	mustNotPanic(t, "loop-deploy", func() {
		loopAddr, _, err = h.Deploy(deployer, wrapRuntime(loop), deployGas)
	})
	if err != nil {
		t.Fatalf("deploy loop contract: %v", err)
	}
	if code := h.Code(loopAddr); len(code) != len(loop) {
		t.Fatalf("loop runtime len=%d, want %d", len(code), len(loop))
	}

	const budget = uint64(500_000)
	var gasUsed uint64
	var cerr error
	mustNotPanic(t, "loop-call", func() {
		_, gasUsed, cerr = h.Call(deployer, loopAddr, nil, budget)
	})
	if cerr == nil {
		t.Fatal("infinite-loop call unexpectedly returned success")
	}
	// Gas-bounded: the loop must consume (essentially) the whole budget rather
	// than hang. Allow a small slack for the final failing op.
	if gasUsed < budget-100 {
		t.Fatalf("infinite loop did not consume its gas budget: used=%d budget=%d", gasUsed, budget)
	}
	t.Logf("infinite loop bounded by gas: err=%v gasUsed=%d/%d (no hang, no panic)", cerr, gasUsed, budget)
}

// ---------------------------------------------------------------------------
// Case 5: call to non-existent / empty-code address returns cleanly.
// ---------------------------------------------------------------------------

func TestAdvEVM05CallEmptyCodeAddress(t *testing.T) {
	h, err := NewHarness()
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	h.Fund(deployer, uint256.NewInt(1_000_000_000_000_000_000))

	empty := common.HexToAddress("0x00000000000000000000000000000000deadbeef")
	if code := h.Code(empty); len(code) != 0 {
		t.Fatalf("precondition: address unexpectedly has code (%d bytes)", len(code))
	}

	var ret []byte
	var cerr error
	mustNotPanic(t, "empty-code-call", func() {
		ret, _, cerr = h.Call(deployer, empty, []byte{0x01, 0x02, 0x03, 0x04}, callGas)
	})
	// EVM semantics: a call into an address with no code is a no-op success
	// returning empty data.
	if cerr != nil {
		t.Fatalf("call to empty-code address errored: %v", cerr)
	}
	if len(ret) != 0 {
		t.Fatalf("call to empty-code address returned %d bytes, want 0", len(ret))
	}
	t.Logf("empty-code call clean: err=nil retLen=0 (no panic)")
}

// ---------------------------------------------------------------------------
// Case 6: reentrancy / self-recursion bounded by call depth (1024) + gas.
// ---------------------------------------------------------------------------

func TestAdvEVM06RecursionBoundedByDepthAndGas(t *testing.T) {
	newAttackerHarness := func(t *testing.T) (*Harness, common.Address) {
		t.Helper()
		h, err := NewHarness()
		if err != nil {
			t.Fatalf("NewHarness: %v", err)
		}
		h.Fund(deployer, uint256.NewInt(1_000_000_000_000_000_000))
		// runtime: CALL back into own ADDRESS forwarding all gas, then POP;STOP.
		// With no base case this reenters until the 1024 call-depth limit makes
		// the inner CALL fail (returns 0 without executing), unwinding to STOP.
		//   PUSH1 0 x5 (retSize,retOffset,argsSize,argsOffset,value)
		//   ADDRESS GAS CALL POP STOP
		reenter := []byte{
			0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00,
			0x30, 0x5a, 0xf1, 0x50, 0x00,
		}
		addr, _, err := h.Deploy(deployer, wrapRuntime(reenter), deployGas)
		if err != nil {
			t.Fatalf("deploy reentrant contract: %v", err)
		}
		return h, addr
	}

	// (a) generous gas: recursion terminates at the depth limit -> success.
	hA, addrA := newAttackerHarness(t)
	var errA error
	mustNotPanic(t, "recursion-generous", func() {
		_, _, errA = hA.Call(deployer, addrA, nil, 50_000_000)
	})
	if errA != nil {
		t.Fatalf("depth-bounded recursion did not unwind to success: %v", errA)
	}
	rootA := hA.StateRoot()
	t.Logf("(a) generous gas: recursion unwound at depth limit -> success")

	// (b) tight gas: even a tight budget terminates deterministically without a
	// hang or panic. Self-CALL reentrancy is bounded by TWO independent limits:
	// the 1024 call-depth cap AND the EIP-150 63/64 gas-forwarding rule, so each
	// inner CALL eventually receives too little gas to recurse further, returns
	// 0, and the stack unwinds. The adversary can therefore never hang the
	// harness. We assert bounded termination (gasUsed <= budget), not a specific
	// error, since a well-formed unwind legitimately returns success.
	hB, addrB := newAttackerHarness(t)
	const tight = uint64(200_000)
	var gasUsedB uint64
	var errB error
	mustNotPanic(t, "recursion-tight", func() {
		_, gasUsedB, errB = hB.Call(deployer, addrB, nil, tight)
	})
	if gasUsedB > tight {
		t.Fatalf("gas used %d exceeds budget %d", gasUsedB, tight)
	}
	t.Logf("(b) tight gas: bounded termination (depth+63/64 rule) err=%v gasUsed=%d/%d", errB, gasUsedB, tight)

	// (c) determinism: repeat the generous run on a fresh state -> same root.
	hC, addrC := newAttackerHarness(t)
	mustNotPanic(t, "recursion-determinism", func() {
		if _, _, err := hC.Call(deployer, addrC, nil, 50_000_000); err != nil {
			t.Fatalf("recursion determinism run: %v", err)
		}
	})
	if rootA != hC.StateRoot() {
		t.Fatalf("non-deterministic recursion state root: %s != %s", rootA.Hex(), hC.StateRoot().Hex())
	}
	t.Logf("(c) deterministic recursion root=%s", rootA.Hex())
}

// ---------------------------------------------------------------------------
// Case 7: determinism under an adversarial (revert-containing) call sequence.
// ---------------------------------------------------------------------------

func runAdversarialSequence(t *testing.T) (dBal, rBal, aBal *big.Int, root common.Hash) {
	t.Helper()
	h, token := deployToken(t)

	// A sequence mixing successful transfers with a guaranteed revert.
	steps := []struct {
		to     common.Address
		amount *big.Int
	}{
		{recipient, big.NewInt(100)},                                   // ok
		{attacker, big.NewInt(250)},                                    // ok
		{recipient, new(big.Int).Add(ERC20InitialSupply, big.NewInt(1))}, // REVERT (overspend)
		{recipient, big.NewInt(50)},                                    // ok
		{deployer, big.NewInt(999)},                                    // self-transfer (net-zero)
	}
	for _, s := range steps {
		// Ignore per-step error: the reverting step is expected to fail, and
		// EVM revert semantics must leave that step's state untouched.
		_, _, _ = h.Call(deployer, token, EncodeTransfer(s.to, s.amount), callGas)
	}
	return balanceOf(t, h, token, deployer),
		balanceOf(t, h, token, recipient),
		balanceOf(t, h, token, attacker),
		h.StateRoot()
}

func TestAdvEVM07DeterministicAdversarialSequence(t *testing.T) {
	d1, r1, a1, root1 := runAdversarialSequence(t)
	d2, r2, a2, root2 := runAdversarialSequence(t)

	if d1.Cmp(d2) != 0 || r1.Cmp(r2) != 0 || a1.Cmp(a2) != 0 {
		t.Fatalf("non-deterministic balances: run1(d=%s,r=%s,a=%s) run2(d=%s,r=%s,a=%s)",
			d1, r1, a1, d2, r2, a2)
	}
	if root1 != root2 {
		t.Fatalf("non-deterministic MPT state root: %s != %s", root1.Hex(), root2.Hex())
	}

	// Sanity: recipient got 100+50=150, attacker got 250, deployer paid 400.
	wantR := big.NewInt(150)
	wantA := big.NewInt(250)
	wantD := new(big.Int).Sub(ERC20InitialSupply, big.NewInt(400))
	if r1.Cmp(wantR) != 0 || a1.Cmp(wantA) != 0 || d1.Cmp(wantD) != 0 {
		t.Fatalf("adversarial sequence net wrong: d=%s(want %s) r=%s(want %s) a=%s(want %s)",
			d1, wantD, r1, wantR, a1, wantA)
	}
	t.Logf("deterministic adversarial sequence: d=%s r=%s a=%s root=%s", d1, r1, a1, root1.Hex())
}

// ---------------------------------------------------------------------------
// Case 8: malformed / short calldata handled per ABI, no harness panic.
// ---------------------------------------------------------------------------

func TestAdvEVM08MalformedCalldata(t *testing.T) {
	h, token := deployToken(t)
	dBefore := balanceOf(t, h, token, deployer)
	tsBefore := totalSupply(t, h, token)

	// (a) empty calldata: dispatcher reads a zero selector -> no match -> revert.
	var e1 error
	mustNotPanic(t, "empty-calldata", func() {
		_, _, e1 = h.Call(deployer, token, nil, callGas)
	})
	if e1 == nil {
		t.Fatal("empty calldata unexpectedly did not revert")
	}
	t.Logf("(a) empty calldata reverted: err=%v", e1)

	// (b) truncated selector (2 bytes): still no valid match -> revert, no panic.
	var e2 error
	mustNotPanic(t, "short-selector", func() {
		_, _, e2 = h.Call(deployer, token, []byte{0xa9, 0x05}, callGas)
	})
	if e2 == nil {
		t.Fatal("2-byte calldata unexpectedly did not revert")
	}
	t.Logf("(b) 2-byte selector reverted: err=%v", e2)

	// (c) balanceOf with a truncated (10-byte) address arg: CALLDATALOAD
	// zero-pads beyond calldata end -> reads a well-formed (zero-padded) slot,
	// returns cleanly, no slice-out-of-range panic.
	shortBal := append(SelBalanceOf[:], make([]byte, 10)...)
	var ret3 []byte
	var e3 error
	mustNotPanic(t, "short-balanceOf", func() {
		ret3, _, e3 = h.Call(deployer, token, shortBal, callGas)
	})
	if e3 != nil {
		t.Fatalf("truncated balanceOf errored: %v", e3)
	}
	if DecodeUint256(ret3).Sign() != 0 {
		t.Fatalf("truncated balanceOf on zero-padded address returned non-zero: %s", DecodeUint256(ret3))
	}
	t.Logf("(c) truncated balanceOf returned 0 (zero-padded arg), no panic")

	// (d) transfer with only the selector (no to/amount): both args zero-pad to
	// zero -> transfers 0 to address(0), a net-zero no-op; state unchanged.
	var e4 error
	mustNotPanic(t, "short-transfer", func() {
		_, _, e4 = h.Call(deployer, token, SelTransfer[:], callGas)
	})
	// Zero-value transfer succeeds (amount 0 <= balance); assert no corruption.
	if e4 != nil {
		t.Logf("(d) selector-only transfer errored (also acceptable): %v", e4)
	}
	if got := balanceOf(t, h, token, deployer); got.Cmp(dBefore) != 0 {
		t.Fatalf("malformed transfer changed deployer balance: %s != %s", got, dBefore)
	}
	if got := totalSupply(t, h, token); got.Cmp(tsBefore) != 0 {
		t.Fatalf("malformed calldata changed totalSupply: %s != %s", got, tsBefore)
	}
	t.Logf("(d) selector-only transfer: balances + totalSupply unchanged (no panic)")
}
