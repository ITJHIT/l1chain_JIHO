package chain

import "errors"

// BaseFeeMaxChangeDenominator bounds how much BaseFee can move in a single
// block: at most 1/8 (12.5%) up or down, the same denominator real EIP-1559
// uses. ElasticityMultiplier is the ratio between a block's hard gas cap
// (Chain.gasLimit) and its equilibrium target (GasTarget) -- a block can go up
// to 2x target in one block before BaseFee starts correcting it back down,
// same as real EIP-1559.
const (
	BaseFeeMaxChangeDenominator = 8
	ElasticityMultiplier        = 2
)

// ErrFeeCapBelowBaseFee is returned when a transaction's GasFeeCap cannot
// cover the block's current BaseFee -- the transaction is not includable at
// this price, regardless of GasTipCap.
var ErrFeeCapBelowBaseFee = errors.New("chain: gas fee cap below base fee")

// ErrTipExceedsFeeCap is returned when a transaction's GasTipCap is greater
// than its own GasFeeCap -- an internally inconsistent transaction (the
// sender would be offering a priority fee it never agreed to actually pay,
// since GasFeeCap is the hard ceiling on total price per gas unit).
var ErrTipExceedsFeeCap = errors.New("chain: gas tip cap exceeds gas fee cap")

// GasTarget is the equilibrium gas usage BaseFee adjusts toward: half of the
// block's hard gas cap, same as real EIP-1559's ElasticityMultiplier of 2.
func GasTarget(gasLimit uint64) uint64 {
	return gasLimit / ElasticityMultiplier
}

// ComputeBaseFee derives a block's BaseFee from its parent's BaseFee and
// actual gas usage against the (fixed, genesis-configured) gas target -- the
// real EIP-1559 recurrence, unmodified: unchanged at exactly target, moves by
// up to 1/8 per block proportional to how far usage was from target,
// guaranteed to move by at least 1 whenever usage is above target (integer
// division would otherwise silently round small deltas to 0 and stall the
// correction upward). The falling side has no such forced minimum -- matching
// real EIP-1559 exactly -- and as a direct consequence of dividing by
// BaseFeeMaxChangeDenominator (8), delta is always < parentBaseFee, so this
// naturally never reaches 0 from a nonzero start; the same is true of real
// Ethereum's own formula, for the same reason. The `parentBaseFee - delta`
// subtraction below still cannot underflow even if that invariant were ever
// violated by a future constant change, since delta is bounded defensively.
func ComputeBaseFee(parentBaseFee, parentGasUsed, parentGasTarget uint64) uint64 {
	if parentGasTarget == 0 || parentBaseFee == 0 {
		return parentBaseFee
	}
	switch {
	case parentGasUsed == parentGasTarget:
		return parentBaseFee
	case parentGasUsed > parentGasTarget:
		gasUsedDelta := parentGasUsed - parentGasTarget
		delta := parentBaseFee * gasUsedDelta / parentGasTarget / BaseFeeMaxChangeDenominator
		if delta < 1 {
			delta = 1
		}
		return parentBaseFee + delta
	default:
		gasUsedDelta := parentGasTarget - parentGasUsed
		delta := parentBaseFee * gasUsedDelta / parentGasTarget / BaseFeeMaxChangeDenominator
		if delta >= parentBaseFee {
			delta = parentBaseFee - 1 // defensive underflow guard; see doc comment
		}
		return parentBaseFee - delta
	}
}

// ValidateFeeCapTip checks the one static, context-free invariant a
// transaction's fee fields must satisfy on their own, independent of any
// block's BaseFee: the tip offered can never exceed the hard ceiling the
// sender agreed to pay in total.
func ValidateFeeCapTip(feeCap, tipCap uint64) error {
	if tipCap > feeCap {
		return ErrTipExceedsFeeCap
	}
	return nil
}

// EffectiveGasPrice computes what a transaction actually pays per gas unit at
// a given block BaseFee, and how much of that is priority fee (the portion
// that reaches the block producer rather than being burned): priorityFee is
// capped at whatever headroom is left between BaseFee and the sender's own
// FeeCap, exactly like real EIP-1559's min(tipCap, feeCap-baseFee) rule.
// Returns ErrFeeCapBelowBaseFee if the transaction cannot afford this block's
// BaseFee at all.
func EffectiveGasPrice(baseFee, feeCap, tipCap uint64) (effectivePrice, priorityFee uint64, err error) {
	if feeCap < baseFee {
		return 0, 0, ErrFeeCapBelowBaseFee
	}
	headroom := feeCap - baseFee
	priorityFee = tipCap
	if priorityFee > headroom {
		priorityFee = headroom
	}
	return baseFee + priorityFee, priorityFee, nil
}
