// Package consensus implements Proof-of-Work: a leading-zero-bits target,
// mining, verification, and Bitcoin-style difficulty retargeting toward a
// target block interval (~10s, per the approved plan).
package consensus

import (
	"l1chain/core"
)

// MaxDifficulty caps the leading-zero-bit target at the hash bit width.
const MaxDifficulty = core.HashLen * 8

// leadingZeroBits counts the number of leading zero bits in h.
func leadingZeroBits(h core.Hash) uint32 {
	var count uint32
	for _, b := range h {
		if b == 0 {
			count += 8
			continue
		}
		for mask := byte(0x80); mask != 0; mask >>= 1 {
			if b&mask != 0 {
				return count
			}
			count++
		}
	}
	return count
}

// MeetsTarget reports whether the hash satisfies the required difficulty.
func MeetsTarget(h core.Hash, difficulty uint32) bool {
	return leadingZeroBits(h) >= difficulty
}

// Mine searches for a nonce so the header hash meets its Difficulty. It mutates
// header.Nonce and returns the winning hash. maxIters == 0 means unbounded.
// found reports whether a solution was located within maxIters.
func Mine(header *core.Header, maxIters uint64) (hash core.Hash, found bool) {
	for i := uint64(0); maxIters == 0 || i < maxIters; i++ {
		header.Nonce = i
		hash = header.Hash()
		if MeetsTarget(hash, header.Difficulty) {
			return hash, true
		}
	}
	return hash, false
}

// RetargetInterval is the number of blocks between difficulty adjustments.
const RetargetInterval = 10

// TargetIntervalSeconds is the desired seconds per block (~10s).
const TargetIntervalSeconds = 10

// NextDifficulty adjusts difficulty based on the actual time taken to produce
// the last RetargetInterval blocks versus the expected time. It moves by at
// most one bit per retarget to avoid oscillation, and never below 1.
func NextDifficulty(current uint32, actualSeconds, expectedSeconds int64) uint32 {
	if expectedSeconds <= 0 {
		return current
	}
	switch {
	case actualSeconds < expectedSeconds/2:
		if current < MaxDifficulty {
			return current + 1 // blocks too fast -> harder
		}
	case actualSeconds > expectedSeconds*2:
		if current > 1 {
			return current - 1 // blocks too slow -> easier
		}
	}
	return current
}
