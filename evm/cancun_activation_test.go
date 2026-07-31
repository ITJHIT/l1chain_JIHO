package evm

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/holiman/uint256"
	"testing"
)

// TestModernChainConfigReachesShanghaiAndCancun is a regression test for a
// real bug found while adding evm/adapter's EIP-6780 self-destruct
// coverage: Harness.newEVM never signaled isMerge (vm.BlockContext.Random
// was left nil), so despite ModernChainConfig's own doc comment claiming
// "every fork through Cancun activated... PUSH0 ... are available",
// go-ethereum's params.ChainConfig.Rules gates EVERY post-Merge fork field
// behind isMerge -- the jump-table switch in vm.NewEVM fell through past
// Shanghai/Cancun straight to the pre-Shanghai London instruction set. This
// was silently harmless for M4-M6 only because solc 0.8.24 defaults to
// targeting evmVersion "paris" (confirmed via contracts/artifacts/build-info
// -- Paris introduced no new opcodes over London), so nothing ever actually
// exercised the gap. Fixed in newEVM/rules; this test proves it stays
// fixed by driving two opcodes that did NOT exist before Shanghai/Cancun
// respectively through a real Harness.
func TestModernChainConfigReachesShanghaiAndCancun(t *testing.T) {
	h, err := NewHarness()
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	h.Fund(deployer, uint256.NewInt(1_000_000_000_000_000_000))

	// Shanghai (EIP-3855): PUSH0 must execute, not ErrInvalidOpCode.
	push0Code := []byte{0x5f, 0x5f, 0xf3} // PUSH0 PUSH0 RETURN
	if _, _, err := h.Deploy(deployer, push0Code, deployGas); err != nil {
		t.Fatalf("deploy of PUSH0-only init code failed (Shanghai not active?): %v", err)
	}

	// Cancun (EIP-1153): a real TSTORE/TLOAD round trip, not just "the
	// opcode didn't error" -- proves transient storage genuinely reaches
	// the StateDB and back through a real embedded EVM execution.
	target := common.HexToAddress("0x7777777777777777777777777777777777777c")
	transientRoundTrip := []byte{
		0x60, 0x2a, // PUSH1 42        (value)
		0x60, 0x00, // PUSH1 0         (key)
		0x5d,       // TSTORE          transient[0] = 42
		0x60, 0x00, // PUSH1 0         (key)
		0x5c,       // TLOAD           push transient[0]
		0x60, 0x00, // PUSH1 0         (memory offset)
		0x52,       // MSTORE          mem[0:32] = 42
		0x60, 0x20, // PUSH1 32        (size)
		0x60, 0x00, // PUSH1 0         (offset)
		0xf3, // RETURN mem[0:32]
	}
	h.State.SetCode(target, transientRoundTrip, tracing.CodeChangeUnspecified)

	ret, _, err := h.Call(deployer, target, nil, callGas)
	if err != nil {
		t.Fatalf("call exercising TSTORE/TLOAD failed (Cancun not active?): %v", err)
	}
	if len(ret) != 32 || ret[31] != 42 {
		t.Fatalf("transient storage round-trip returned %x, want a 32-byte word ending in 0x2a (42)", ret)
	}
}
