package pos

import (
	"encoding/binary"
	"math/big"

	"l1chain/core"
)

// ProposerSeed derives the selection seed for the block at height from the
// parent block's hash -- data every observer already has the instant the
// parent is known, with no extra consensus round required.
//
// NAMED LIMITATION (see the M8 plan and README's "still deferred" section):
// this is NOT unbiased, grinding-resistant randomness. The proposer of block
// N fully determines parentHash for block N+1's seed the moment they decide
// whether to publish their own block, so there is a weak, single-block-
// lookahead grinding option a real VRF/RANDAO beacon would close. Out of
// scope for M8 -- stated plainly, not hidden.
func ProposerSeed(parentHash core.Hash, height uint64) core.Hash {
	var h [8]byte
	binary.BigEndian.PutUint64(h[:], height)
	return core.HashConcat(parentHash[:], h[:])
}

// SelectProposer deterministically picks the proposer for a height from the
// EFFECTIVE (non-jailed) validator set and its total stake -- a pure
// function: identical inputs give identical output on every node, which is
// exactly the property the required cross-instance determinism test checks.
//
// Stake-weighted "roulette wheel" selection, made deterministic by
// substituting a hash for a PRNG: target = seed mod totalActiveStake, then
// walk cumulative stake ranges for the validator whose
// [cumulative, cumulative+stake) range contains target.
func SelectProposer(active []ValidatorInfo, totalActiveStake uint64, seed core.Hash) (ValidatorInfo, error) {
	if totalActiveStake == 0 || len(active) == 0 {
		return ValidatorInfo{}, ErrNoActiveValidators
	}
	target := new(big.Int).Mod(new(big.Int).SetBytes(seed[:]), new(big.Int).SetUint64(totalActiveStake))
	var cum uint64
	for _, v := range active {
		cum += v.Stake
		if target.Cmp(new(big.Int).SetUint64(cum)) < 0 {
			return v, nil
		}
	}
	// Defensive fallback, not expected to be reachable: target < totalActiveStake
	// always holds by construction of Mod above, and cum reaches
	// totalActiveStake exactly after the last validator, so the loop above
	// always returns before falling through -- kept as a safe default rather
	// than an unreachable panic.
	return active[len(active)-1], nil
}
