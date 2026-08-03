package chain

import (
	"errors"
	"testing"
)

func TestGasTarget(t *testing.T) {
	if got := GasTarget(8_000_000); got != 4_000_000 {
		t.Fatalf("GasTarget(8_000_000) = %d, want 4_000_000", got)
	}
	if got := GasTarget(1); got != 0 {
		t.Fatalf("GasTarget(1) = %d, want 0 (integer division)", got)
	}
}

func TestComputeBaseFeeUnchangedAtExactTarget(t *testing.T) {
	if got := ComputeBaseFee(100, 500, 500); got != 100 {
		t.Fatalf("ComputeBaseFee at exact target = %d, want 100 (unchanged)", got)
	}
}

func TestComputeBaseFeeRisesAboveTarget(t *testing.T) {
	// Fully at the 2x elasticity cap (parentGasUsed == 2*target): the classic
	// EIP-1559 worst case, expected to rise by the full 1/8 (12.5%).
	got := ComputeBaseFee(800, 1000, 500)
	want := uint64(800 + 800*500/500/BaseFeeMaxChangeDenominator) // +100 = 900
	if got != want {
		t.Fatalf("ComputeBaseFee above target = %d, want %d", got, want)
	}
	if got <= 800 {
		t.Fatal("BaseFee did not rise after above-target usage")
	}
}

func TestComputeBaseFeeFallsBelowTarget(t *testing.T) {
	// Fully empty block (parentGasUsed == 0): expected to fall by the full 1/8.
	got := ComputeBaseFee(800, 0, 500)
	want := uint64(800 - 800*500/500/BaseFeeMaxChangeDenominator) // -100 = 700
	if got != want {
		t.Fatalf("ComputeBaseFee below target = %d, want %d", got, want)
	}
	if got >= 800 {
		t.Fatal("BaseFee did not fall after below-target usage")
	}
}

func TestComputeBaseFeeRiseGuaranteesAtLeastOneUnit(t *testing.T) {
	// A tiny above-target delta that would round to 0 under plain integer
	// division must still move the fee by at least 1 -- otherwise the
	// correction could stall forever at a low BaseFee.
	got := ComputeBaseFee(1, 501, 500) // delta=1, 1*1/500/8 == 0 before the floor
	if got <= 1 {
		t.Fatalf("ComputeBaseFee = %d, want > 1 (guaranteed minimum rise)", got)
	}
}

func TestComputeBaseFeeNeverReachesZeroFromNonzeroStart(t *testing.T) {
	// Even the steepest possible below-target delta (a fully empty block) at
	// a low starting BaseFee never reaches 0 -- an inherent consequence of
	// dividing by BaseFeeMaxChangeDenominator (8), the same property real
	// Ethereum's own formula has, see ComputeBaseFee's own doc comment.
	got := ComputeBaseFee(1, 0, 500)
	if got != 1 {
		t.Fatalf("ComputeBaseFee(1, 0, 500) = %d, want 1 (never reaches 0)", got)
	}
	got = ComputeBaseFee(7, 0, 500)
	if got == 0 {
		t.Fatalf("ComputeBaseFee(7, 0, 500) = %d, want > 0", got)
	}
}

func TestComputeBaseFeeZeroTargetOrBaseFeeIsNoOp(t *testing.T) {
	if got := ComputeBaseFee(100, 50, 0); got != 100 {
		t.Fatalf("ComputeBaseFee with zero target = %d, want 100 (unchanged, no divide-by-zero)", got)
	}
	if got := ComputeBaseFee(0, 50, 500); got != 0 {
		t.Fatalf("ComputeBaseFee with zero parent BaseFee = %d, want 0", got)
	}
}

func TestComputeBaseFeeMovesAtMostOneEighth(t *testing.T) {
	// Even at the maximum possible single-block delta (empty block, i.e.
	// gasUsedDelta == gasTarget), the fall is capped at exactly 1/8 -- proves
	// the "at most 12.5% per block" bound directly, not just by example.
	baseFee := uint64(1600)
	target := uint64(1000)
	got := ComputeBaseFee(baseFee, 0, target)
	maxDrop := baseFee / BaseFeeMaxChangeDenominator
	if got < baseFee-maxDrop {
		t.Fatalf("ComputeBaseFee dropped more than 1/8: got %d, floor of allowed range %d", got, baseFee-maxDrop)
	}
}

func TestValidateFeeCapTip(t *testing.T) {
	if err := ValidateFeeCapTip(100, 50); err != nil {
		t.Fatalf("ValidateFeeCapTip(100, 50) = %v, want nil", err)
	}
	if err := ValidateFeeCapTip(100, 100); err != nil {
		t.Fatalf("ValidateFeeCapTip(100, 100) = %v, want nil (equal is fine)", err)
	}
	if err := ValidateFeeCapTip(50, 100); !errors.Is(err, ErrTipExceedsFeeCap) {
		t.Fatalf("ValidateFeeCapTip(50, 100) = %v, want ErrTipExceedsFeeCap", err)
	}
}

func TestEffectiveGasPriceCapsAtHeadroom(t *testing.T) {
	// tipCap (30) exceeds the headroom between feeCap (110) and baseFee (100):
	// priorityFee must be capped at the 10 headroom, not the full 30 tip.
	price, priority, err := EffectiveGasPrice(100, 110, 30)
	if err != nil {
		t.Fatalf("EffectiveGasPrice: %v", err)
	}
	if priority != 10 {
		t.Fatalf("priorityFee = %d, want 10 (capped at headroom)", priority)
	}
	if price != 110 {
		t.Fatalf("effectivePrice = %d, want 110 (baseFee + capped priority == feeCap)", price)
	}
}

func TestEffectiveGasPriceTipFullyPaidWhenHeadroomAllows(t *testing.T) {
	// tipCap (5) fits comfortably under the headroom (feeCap 200 - baseFee
	// 100 == 100): the full tip is paid, not clamped.
	price, priority, err := EffectiveGasPrice(100, 200, 5)
	if err != nil {
		t.Fatalf("EffectiveGasPrice: %v", err)
	}
	if priority != 5 {
		t.Fatalf("priorityFee = %d, want 5 (full tip, unclamped)", priority)
	}
	if price != 105 {
		t.Fatalf("effectivePrice = %d, want 105", price)
	}
}

func TestEffectiveGasPriceRejectsFeeCapBelowBaseFee(t *testing.T) {
	_, _, err := EffectiveGasPrice(100, 99, 0)
	if !errors.Is(err, ErrFeeCapBelowBaseFee) {
		t.Fatalf("EffectiveGasPrice(baseFee=100, feeCap=99) = %v, want ErrFeeCapBelowBaseFee", err)
	}
}

func TestEffectiveGasPriceFeeCapEqualsBaseFeeMeansZeroTip(t *testing.T) {
	// No headroom at all: the transaction is includable (feeCap >= baseFee),
	// but pays no priority fee no matter how high its tipCap is.
	price, priority, err := EffectiveGasPrice(100, 100, 50)
	if err != nil {
		t.Fatalf("EffectiveGasPrice: %v", err)
	}
	if priority != 0 {
		t.Fatalf("priorityFee = %d, want 0 (no headroom)", priority)
	}
	if price != 100 {
		t.Fatalf("effectivePrice = %d, want 100 (== baseFee)", price)
	}
}
