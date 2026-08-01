package pos

import "errors"

var (
	// ErrEmptyValidatorSet is returned by NewValidatorSet for an empty input.
	ErrEmptyValidatorSet = errors.New("pos: validator set must not be empty")
	// ErrTotalStakeOverflow is returned by NewValidatorSet if summing every
	// validator's stake would overflow uint64.
	ErrTotalStakeOverflow = errors.New("pos: total stake overflows uint64")
	// ErrNoActiveValidators is returned by SelectProposer when every
	// validator is jailed (or the set is empty) -- the chain halts block
	// production safely rather than panicking or selecting a jailed validator.
	ErrNoActiveValidators = errors.New("pos: no active (non-jailed) validators to select a proposer from")
	// ErrMalformedAttestation is returned by DecodeAttest for input that is
	// not exactly op(1) || targetHeight(8) || targetHash(32) || blsSig(96),
	// or whose op byte is not OpAttest.
	ErrMalformedAttestation = errors.New("pos: malformed attestation payload")
)
