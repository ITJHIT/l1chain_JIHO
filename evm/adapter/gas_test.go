package adapter

import (
	"testing"

	"l1chain/state"

	"github.com/ethereum/go-ethereum/common"
)

// TestColdVsWarmSLOADGasDiffers is the load-bearing proof this package's
// access-list bookkeeping (AddSlotToAccessList/SlotInAccessList) is
// gas-correct, not just bookkeeping-correct: it drives real bytecode
// through the real, unexported gasSLoadEIP2929 dynamic-gas function
// (core/vm/operations_acl.go) via SLOAD, and asserts the SECOND access to
// the same slot costs meaningfully less than the first -- a real, measured
// gas delta, not an assertion on internal map state.
func TestColdVsWarmSLOADGasDiffers(t *testing.T) {
	cfg := modernChainConfig()
	base := state.NewMemStateDB()
	deployer := common.HexToAddress("0xd000000000000000000000000000000000000d")

	oneSload := []byte{
		0x60, 0x00, // PUSH1 0 (slot)
		0x54, // SLOAD
		0x50, // POP
		0x00, // STOP
	}
	twoSload := []byte{
		0x60, 0x00, // PUSH1 0 (slot)
		0x54,       // SLOAD (cold)
		0x50,       // POP
		0x60, 0x00, // PUSH1 0 (same slot)
		0x54, // SLOAD (warm)
		0x50, // POP
		0x00, // STOP
	}

	// Both calls below are zero-value, so CanTransfer is never consulted --
	// deployer needs no balance.
	sdbA := fullStateDB{New(base)}
	addrOne := common.HexToAddress("0x1111111111111111111111111111111111111a")
	sdbA.SetCode(addrOne, oneSload, 0)
	gasOne := callVia(t, sdbA, cfg, deployer, addrOne, sdCallGas)

	sdbB := fullStateDB{New(base)}
	addrTwo := common.HexToAddress("0x1111111111111111111111111111111111111b")
	sdbB.SetCode(addrTwo, twoSload, 0)
	gasTwo := callVia(t, sdbB, cfg, deployer, addrTwo, sdCallGas)

	if gasOne == 0 || gasTwo == 0 {
		t.Fatalf("gasOne=%d gasTwo=%d, want both non-zero", gasOne, gasTwo)
	}
	delta := gasTwo - gasOne
	// The second SLOAD of the same slot must cost the warm price (100),
	// plus a few gas of opcode overhead for the extra PUSH1/SLOAD/POP -- far
	// less than paying the cold price (2100) a second time.
	if delta == 0 || delta >= 1000 {
		t.Fatalf("second-SLOAD delta = %d gas (gasOne=%d, gasTwo=%d), want a small warm-priced delta (~100), not a second cold charge (~2100)", delta, gasOne, gasTwo)
	}
	if gasOne < 2000 {
		t.Fatalf("gasOne = %d, want at least ~2100 (a single cold SLOAD)", gasOne)
	}
}
