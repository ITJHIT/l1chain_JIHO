package evm

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// TestHarnessTagsEmittedLogsWithARealTxHash is the small, synthetic
// (no-solc-needed) proof for the Harness.SetTxContext fix: before it,
// AddLog's TxHash was always the zero value because SetTxContext was never
// called at all. Runtime bytecode here is minimal hand-assembled LOG0+STOP
// -- PUSH1 0(size) PUSH1 0(offset) LOG0 STOP -- deliberately not routed
// through the real solc fixture, so this test exercises exactly the fix in
// isolation.
func TestHarnessTagsEmittedLogsWithARealTxHash(t *testing.T) {
	h, err := NewHarness()
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	h.Fund(deployer, uint256.NewInt(1_000_000_000_000_000_000))

	logOnce := []byte{0x60, 0x00, 0x60, 0x00, 0xa0, 0x00} // PUSH1 0; PUSH1 0; LOG0; STOP
	addr, _, err := h.Deploy(deployer, wrapRuntime(logOnce), deployGas)
	if err != nil {
		t.Fatalf("deploy log-emitting contract: %v", err)
	}
	if code := h.Code(addr); len(code) != len(logOnce) {
		t.Fatalf("runtime len=%d, want %d", len(code), len(logOnce))
	}
	// Deploy runs constructor (CODECOPY/RETURN) code, never the runtime
	// itself, so no log should exist yet.
	if got := len(h.Logs()); got != 0 {
		t.Fatalf("logs after Deploy = %d, want 0 (constructor never runs the runtime)", got)
	}

	if _, _, err := h.Call(deployer, addr, nil, callGas); err != nil {
		t.Fatalf("call log-emitting contract: %v", err)
	}
	logs := h.Logs()
	if len(logs) != 1 {
		t.Fatalf("logs after first Call = %d, want 1", len(logs))
	}
	firstHash := logs[0].TxHash
	if firstHash == (common.Hash{}) {
		t.Fatal("emitted log's TxHash is the zero value -- SetTxContext fix regressed")
	}

	// A second call must tag its log with a genuinely DIFFERENT tx hash --
	// proves the fix derives a real per-call value, not a fixed non-zero
	// constant that happens to pass the check above.
	if _, _, err := h.Call(deployer, addr, nil, callGas); err != nil {
		t.Fatalf("call log-emitting contract (2nd): %v", err)
	}
	logs = h.Logs()
	if len(logs) != 2 {
		t.Fatalf("logs after second Call = %d, want 2", len(logs))
	}
	secondHash := logs[1].TxHash
	if secondHash == (common.Hash{}) {
		t.Fatal("second emitted log's TxHash is the zero value")
	}
	if secondHash == firstHash {
		t.Fatalf("both calls' logs share the same TxHash (%s) -- expected a distinct hash per call", firstHash.Hex())
	}
}
