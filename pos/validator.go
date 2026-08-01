package pos

import (
	"fmt"

	"l1chain/core"
)

// ValidatorInfo is one validator's genesis-fixed identity and stake. Address
// is an ordinary secp256k1 address (the same key type wallet.Key already
// produces) -- reused as Header.Coinbase for a proposed block, playing the
// same role a PoW miner's address already plays. BLSPubKey is the separate
// consensus-signing key (see bls.go's own doc comment for why PoS uses a
// second key type alongside the existing account key).
type ValidatorInfo struct {
	Address   core.Address
	BLSPubKey []byte // 48-byte compressed G1 point
	Stake     uint64
}

// ValidatorSet is the genesis-fixed set of validators and their stakes. M8
// scopes staking as immutable, genesis-only configuration -- see the M8
// plan's "no live deposit tx" decision -- so a ValidatorSet, once
// constructed, never changes; only which of its members are jailed changes
// (tracked separately, see EffectiveStake).
type ValidatorSet struct {
	validators []ValidatorInfo
	totalStake uint64
}

// NewValidatorSet validates and constructs a ValidatorSet: rejects zero
// stake, a duplicate address or BLS pubkey, an invalid BLS pubkey (checked
// ONCE here, at registration -- see ValidatePubKey's own doc comment for why
// aggregate verification deliberately skips per-call pubkey validation), and
// total-stake overflow.
func NewValidatorSet(vs []ValidatorInfo) (*ValidatorSet, error) {
	if len(vs) == 0 {
		return nil, ErrEmptyValidatorSet
	}
	seenAddr := make(map[core.Address]bool, len(vs))
	seenPub := make(map[string]bool, len(vs))
	var total uint64
	for _, v := range vs {
		if v.Stake == 0 {
			return nil, fmt.Errorf("pos: validator %x has zero stake", v.Address)
		}
		if seenAddr[v.Address] {
			return nil, fmt.Errorf("pos: duplicate validator address %x", v.Address)
		}
		pubKey := string(v.BLSPubKey)
		if seenPub[pubKey] {
			return nil, fmt.Errorf("pos: duplicate validator BLS pubkey for %x", v.Address)
		}
		if !ValidatePubKey(v.BLSPubKey) {
			return nil, fmt.Errorf("pos: validator %x has an invalid BLS pubkey", v.Address)
		}
		seenAddr[v.Address] = true
		seenPub[pubKey] = true
		newTotal := total + v.Stake
		if newTotal < total {
			return nil, ErrTotalStakeOverflow
		}
		total = newTotal
	}
	out := make([]ValidatorInfo, len(vs))
	copy(out, vs)
	return &ValidatorSet{validators: out, totalStake: total}, nil
}

// TotalStake returns the sum of every validator's stake, ignoring jailing.
func (vs *ValidatorSet) TotalStake() uint64 { return vs.totalStake }

// Len returns the number of validators in the set, ignoring jailing.
func (vs *ValidatorSet) Len() int { return len(vs.validators) }

// ByAddress looks up a validator by its account address.
func (vs *ValidatorSet) ByAddress(a core.Address) (ValidatorInfo, bool) {
	for _, v := range vs.validators {
		if v.Address == a {
			return v, true
		}
	}
	return ValidatorInfo{}, false
}

// EffectiveStake returns the validators NOT present (as true) in jailed, in
// the SAME relative order the set was constructed in, plus their combined
// stake. Order matters: SelectProposer's cumulative-stake walk must be
// byte-for-byte deterministic across independent nodes, so both the
// validator order and the jailed-exclusion must be identical everywhere --
// jailed itself is derived identically on every node as a side effect of
// Chain.AddBlock's own equivocation detection, never a local/independent
// judgment call.
func (vs *ValidatorSet) EffectiveStake(jailed map[core.Address]bool) (active []ValidatorInfo, total uint64) {
	active = make([]ValidatorInfo, 0, len(vs.validators))
	for _, v := range vs.validators {
		if jailed[v.Address] {
			continue
		}
		active = append(active, v)
		total += v.Stake
	}
	return active, total
}
