package vm

import (
	"errors"
	"math/big"
	"testing"

	"l1chain/core"
	"l1chain/state"
)

// retSuffix stores the top stack word to memory[0] and RETURNs 32 bytes.
//
//	PUSH1 0x00 MSTORE PUSH1 0x20 PUSH1 0x00 RETURN
var retSuffix = []byte{0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3}

func withRet(prefix ...byte) []byte { return append(append([]byte{}, prefix...), retSuffix...) }

func hashOfUint(u uint64) core.Hash {
	var h core.Hash
	b := new(big.Int).SetUint64(u).Bytes()
	copy(h[core.HashLen-len(b):], b)
	return h
}

// execCode deploys code at a fixed contract address, funds the sender, and runs
// a call with the given calldata and gas. It returns the receipt and the state
// (so storage effects can be inspected).
func execCode(code, calldata []byte, gas uint64) (Receipt, state.StateDB, core.Address) {
	st := state.NewMemStateDB()
	from := core.Address{0x01}
	to := core.Address{0x0c}
	st.SetAccount(from, state.Account{Balance: 1_000_000})
	st.SetCode(to, code)
	r := StackVM{}.Execute(st, Message{From: from, To: &to, GasLimit: gas, Data: calldata})
	return r, st, to
}

func retUint(t *testing.T, r Receipt) uint64 {
	t.Helper()
	if !r.Success {
		t.Fatalf("execution failed: %v", r.Err)
	}
	return new(big.Int).SetBytes(r.ReturnData).Uint64()
}

func TestArithmeticAndComparisonOpcodes(t *testing.T) {
	cases := []struct {
		name string
		code []byte
		want uint64
	}{
		{"ADD", withRet(0x60, 0x03, 0x60, 0x04, 0x01), 7},
		{"MUL", withRet(0x60, 0x03, 0x60, 0x04, 0x02), 12},
		{"SUB", withRet(0x60, 0x04, 0x60, 0x0a, 0x03), 6},   // 10-4
		{"DIV", withRet(0x60, 0x03, 0x60, 0x0c, 0x04), 4},   // 12/3
		{"DIV0", withRet(0x60, 0x00, 0x60, 0x0c, 0x04), 0},  // 12/0 -> 0
		{"LT", withRet(0x60, 0x05, 0x60, 0x03, 0x10), 1},    // 3<5
		{"GT", withRet(0x60, 0x05, 0x60, 0x03, 0x11), 0},    // 3>5
		{"EQ", withRet(0x60, 0x07, 0x60, 0x07, 0x14), 1},    // 7==7
		{"ISZERO", withRet(0x60, 0x00, 0x15), 1},            // iszero(0)
		{"DUP1", withRet(0x60, 0x05, 0x80, 0x01), 10},       // 5 dup add
		{"SWAP1", withRet(0x60, 0x14, 0x60, 0x04, 0x90, 0x03), 16}, // swap(20,4);20-4
		{"MSTORE_MLOAD", withRet(0x60, 0x2a, 0x60, 0x00, 0x52, 0x60, 0x00, 0x51), 42},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _, _ := execCode(c.code, nil, 100000)
			if got := retUint(t, r); got != c.want {
				t.Fatalf("%s = %d, want %d", c.name, got, c.want)
			}
		})
	}
}

func TestWord256OverflowWraps(t *testing.T) {
	// 0 - 1 wraps to 2^256-1 (all 0xFF).
	r, _, _ := execCode(withRet(0x60, 0x01, 0x60, 0x00, 0x03), nil, 100000)
	if !r.Success {
		t.Fatalf("failed: %v", r.Err)
	}
	want := new(big.Int).Sub(tt256, big.NewInt(1))
	if got := new(big.Int).SetBytes(r.ReturnData); got.Cmp(want) != 0 {
		t.Fatalf("wrap = %x, want %x", got, want)
	}
}

func TestJumpAndJumpi(t *testing.T) {
	// JUMP over a PUSH1 7 to a JUMPDEST that returns 42.
	jump := withRet(0x60, 0x06, 0x56, 0x60, 0x07, 0x00, 0x5b, 0x60, 0x2a)
	if r, _, _ := execCode(jump, nil, 100000); retUint(t, r) != 42 {
		t.Fatalf("JUMP did not branch to JUMPDEST")
	}
	// JUMPI taken (cond=1) to a JUMPDEST that returns 42.
	jmpi := withRet(0x60, 0x01, 0x60, 0x08, 0x57, 0x60, 0x07, 0x00, 0x5b, 0x60, 0x2a)
	if r, _, _ := execCode(jmpi, nil, 100000); retUint(t, r) != 42 {
		t.Fatalf("JUMPI(1) did not branch")
	}
	// Invalid jump destination reverts.
	bad := []byte{0x60, 0x03, 0x56, 0x00} // JUMP to pc 3 which is not a JUMPDEST
	if r, _, _ := execCode(bad, nil, 100000); r.Success || !errors.Is(r.Err, ErrInvalidJump) {
		t.Fatalf("invalid jump: success=%v err=%v", r.Success, r.Err)
	}
}

func TestCalldataOpcodes(t *testing.T) {
	// CALLDATASIZE
	r, _, _ := execCode(withRet(0x36), []byte{1, 2, 3, 4, 5}, 100000)
	if retUint(t, r) != 5 {
		t.Fatalf("CALLDATASIZE = %d, want 5", retUint(t, r))
	}
	// CALLDATALOAD at offset 0 (word big-endian): last byte 0x2a -> 42.
	data := make([]byte, 32)
	data[31] = 0x2a
	r2, _, _ := execCode(withRet(0x60, 0x00, 0x35), data, 100000)
	if retUint(t, r2) != 42 {
		t.Fatalf("CALLDATALOAD = %d, want 42", retUint(t, r2))
	}
}

func TestCallValue(t *testing.T) {
	st := state.NewMemStateDB()
	from := core.Address{0x01}
	to := core.Address{0x0c}
	st.SetAccount(from, state.Account{Balance: 1000})
	st.SetCode(to, withRet(0x34)) // CALLVALUE
	r := StackVM{}.Execute(st, Message{From: from, To: &to, Value: 250, GasLimit: 100000})
	if retUint(t, r) != 250 {
		t.Fatalf("CALLVALUE = %d, want 250", retUint(t, r))
	}
	if got := st.GetAccount(to).Balance; got != 250 {
		t.Fatalf("callee balance = %d, want 250 (value transferred)", got)
	}
}

func TestStorageRoundTrip(t *testing.T) {
	// SSTORE slot5 = 42; SLOAD slot5; return.
	code := withRet(0x60, 0x2a, 0x60, 0x05, 0x55, 0x60, 0x05, 0x54)
	r, st, to := execCode(code, nil, 100000)
	if retUint(t, r) != 42 {
		t.Fatalf("SLOAD after SSTORE = %d, want 42", retUint(t, r))
	}
	if got := st.GetStorage(to, hashOfUint(5)); got != hashOfUint(42) {
		t.Fatalf("committed storage slot5 = %x, want 42", got)
	}
}

func TestGasAccountingPerOpcode(t *testing.T) {
	// PUSH1 PUSH1 ADD STOP = 3+3+3+0.
	if r, _, _ := execCode([]byte{0x60, 0x01, 0x60, 0x02, 0x01, 0x00}, nil, 100000); r.GasUsed != 9 {
		t.Fatalf("ADD program gas = %d, want 9", r.GasUsed)
	}
	// PUSH1 PUSH1 SSTORE STOP = 3+3+5000+0.
	if r, _, _ := execCode([]byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x00}, nil, 100000); r.GasUsed != 5006 {
		t.Fatalf("SSTORE program gas = %d, want 5006", r.GasUsed)
	}
}

func TestOutOfGasRevertsStorage(t *testing.T) {
	// First SSTORE (slot0=7) succeeds; second SSTORE OOGs. Nothing must commit.
	code := []byte{
		0x60, 0x07, 0x60, 0x00, 0x55, // slot0 = 7  (uses 5006)
		0x60, 0x09, 0x60, 0x01, 0x55, // slot1 = 9  (needs another 5006)
		0x00,
	}
	r, st, to := execCode(code, nil, 6000)
	if r.Success {
		t.Fatalf("expected OOG failure")
	}
	if !errors.Is(r.Err, ErrOutOfGas) {
		t.Fatalf("err = %v, want ErrOutOfGas", r.Err)
	}
	if r.GasUsed != 6000 {
		t.Fatalf("OOG GasUsed = %d, want gasLimit 6000", r.GasUsed)
	}
	if got := st.GetStorage(to, hashOfUint(0)); !got.IsZero() {
		t.Fatalf("slot0 = %x, want unchanged (OOG must revert already-applied writes)", got)
	}
	if got := st.GetStorage(to, hashOfUint(1)); !got.IsZero() {
		t.Fatalf("slot1 = %x, want unchanged", got)
	}
}

func TestStackUnderflowGuarded(t *testing.T) {
	r, _, _ := execCode([]byte{0x01}, nil, 100000) // ADD with empty stack
	if r.Success || !errors.Is(r.Err, ErrStackUnderflow) {
		t.Fatalf("underflow: success=%v err=%v", r.Success, r.Err)
	}
}

func TestStackOverflowGuarded(t *testing.T) {
	// Infinite loop pushing a value each iteration until the stack overflows.
	//   JUMPDEST PUSH1 1 PUSH1 0 JUMP
	code := []byte{0x5b, 0x60, 0x01, 0x60, 0x00, 0x56}
	r, _, _ := execCode(code, nil, 10_000_000)
	if r.Success || !errors.Is(r.Err, ErrStackOverflow) {
		t.Fatalf("overflow: success=%v err=%v", r.Success, r.Err)
	}
}

// callerToB builds bytecode that CALLs contract b (no value/data) and then
// executes `after` (e.g. POP or SSTORE of the call result).
func callerToB(b core.Address, after ...byte) []byte {
	code := []byte{
		0x60, 0x00, // retSize
		0x60, 0x00, // retOffset
		0x60, 0x00, // argsSize
		0x60, 0x00, // argsOffset
		0x60, 0x00, // value
		0x73,       // PUSH20 addr
	}
	code = append(code, b[:]...)
	code = append(code, 0x60, 0x00) // gas
	code = append(code, 0xf1)       // CALL
	code = append(code, after...)
	return code
}

// counter increments the contract's own slot0 on each execution.
var counter = []byte{0x60, 0x00, 0x54, 0x60, 0x01, 0x01, 0x60, 0x00, 0x55, 0x00}

func TestCallIntoAnotherContract(t *testing.T) {
	st := state.NewMemStateDB()
	from := core.Address{0x01}
	addrA := core.Address{0x0a}
	addrB := core.Address{0x0b}
	st.SetAccount(from, state.Account{Balance: 1_000_000})
	st.SetCode(addrB, counter)
	st.SetCode(addrA, append(callerToB(addrB, 0x50), 0x00)) // CALL B; POP; STOP

	r := StackVM{}.Execute(st, Message{From: from, To: &addrA, GasLimit: 100000})
	if !r.Success {
		t.Fatalf("CALL failed: %v", r.Err)
	}
	if got := st.GetStorage(addrB, hashOfUint(0)); got != hashOfUint(1) {
		t.Fatalf("callee slot0 = %x, want 1 (CALL must run callee code and commit)", got)
	}
}

func TestCallDepthLimit(t *testing.T) {
	st := state.NewMemStateDB()
	addrA := core.Address{0x0a}
	addrB := core.Address{0x0b}
	st.SetCode(addrB, []byte{0x00}) // STOP
	// Caller stores the CALL result (1 success / 0 failure) into slot0.
	code := append(callerToB(addrB, 0x60, 0x00, 0x55), 0x00) // CALL; PUSH1 0; SSTORE; STOP

	// At a shallow depth the CALL succeeds -> slot0 == 1.
	sc0 := newScope(st)
	if _, err := (StackVM{}).run(&gasMeter{limit: 100000}, sc0, 0, code, addrA, 0, nil); err != nil {
		t.Fatalf("depth 0 run: %v", err)
	}
	if got := sc0.getStorage(addrA, core.Hash{}); got != hashOfUint(1) {
		t.Fatalf("depth 0 call result = %x, want 1", got)
	}

	// At the depth limit the CALL must fail (push 0) without executing the callee.
	scMax := newScope(st)
	if _, err := (StackVM{}).run(&gasMeter{limit: 100000}, scMax, MaxCallDepth, code, addrA, 0, nil); err != nil {
		t.Fatalf("depth-limit run: %v", err)
	}
	if got := scMax.getStorage(addrA, core.Hash{}); !got.IsZero() {
		t.Fatalf("call at depth limit = %x, want 0 (depth exceeded)", got)
	}
}

func TestContractCreation(t *testing.T) {
	st := state.NewMemStateDB()
	from := core.Address{0x01}
	st.SetAccount(from, state.Account{Balance: 1_000_000})
	r := StackVM{}.Execute(st, Message{From: from, To: nil, Nonce: 7, GasLimit: 100000, Data: counter})
	if !r.Success {
		t.Fatalf("creation failed: %v", r.Err)
	}
	want := CreateAddress(from, 7)
	if r.ContractAddress == nil || *r.ContractAddress != want {
		t.Fatalf("ContractAddress = %v, want %v", r.ContractAddress, want)
	}
	if r.GasUsed < GasCreate {
		t.Fatalf("creation gas = %d, want >= %d", r.GasUsed, GasCreate)
	}
	if got := st.GetCode(want); len(got) != len(counter) {
		t.Fatalf("stored code len = %d, want %d", len(got), len(counter))
	}
}

func TestContractCreationOutOfGas(t *testing.T) {
	st := state.NewMemStateDB()
	from := core.Address{0x01}
	st.SetAccount(from, state.Account{Balance: 1_000_000})
	r := StackVM{}.Execute(st, Message{From: from, To: nil, Nonce: 1, GasLimit: GasCreate - 1, Data: counter})
	if r.Success || !errors.Is(r.Err, ErrOutOfGas) {
		t.Fatalf("creation OOG: success=%v err=%v", r.Success, r.Err)
	}
	if got := st.GetCode(CreateAddress(from, 1)); len(got) != 0 {
		t.Fatalf("code stored despite OOG creation")
	}
}
