package chain

import (
	"errors"
	"testing"

	"l1chain/consensus"
	"l1chain/core"
	"l1chain/pos"
)

// newTestChain builds a minimal funded chain, matching the inline pattern
// every other file in this package uses (there is no shared newChain helper
// in package chain -- redteam's own newChain is a different package).
func newTestChain(t *testing.T, alloc map[core.Address]uint64) *Chain {
	t.Helper()
	g := Genesis{Alloc: alloc, Difficulty: testDiff, Timestamp: 0}
	return NewChain(g.ToBlock(), alloc)
}

func testValidatorSet(t *testing.T) *pos.ValidatorSet {
	t.Helper()
	k, err := pos.NewKey()
	if err != nil {
		t.Fatalf("pos.NewKey: %v", err)
	}
	vs, err := pos.NewValidatorSet([]pos.ValidatorInfo{
		{Address: addr(1), BLSPubKey: k.PubKey(), Stake: 100},
	})
	if err != nil {
		t.Fatalf("pos.NewValidatorSet: %v", err)
	}
	return vs
}

func TestChainDefaultConsensusModeIsPoW(t *testing.T) {
	c := newTestChain(t, map[core.Address]uint64{addr(1): 1000})
	if got := c.ConsensusMode(); got != consensus.PoW {
		t.Fatalf("ConsensusMode() = %v, want PoW (a Chain that never calls SetConsensusMode)", got)
	}
	if c.ValidatorSet() != nil {
		t.Fatal("ValidatorSet() must be nil for a PoW chain")
	}
}

func TestSetConsensusModeRejectsPoSWithoutValidators(t *testing.T) {
	c := newTestChain(t, map[core.Address]uint64{addr(1): 1000})
	if err := c.SetConsensusMode(consensus.PoS, nil); !errors.Is(err, ErrPoSRequiresValidators) {
		t.Fatalf("err = %v, want ErrPoSRequiresValidators (nil validator set)", err)
	}
	empty, err := pos.NewValidatorSet(nil)
	if err == nil {
		// pos.NewValidatorSet itself already rejects empty input, so this
		// path only matters if that ever changes; assert the chain-level
		// guard would still catch it.
		if err := c.SetConsensusMode(consensus.PoS, empty); !errors.Is(err, ErrPoSRequiresValidators) {
			t.Fatalf("err = %v, want ErrPoSRequiresValidators (empty validator set)", err)
		}
	}
	if got := c.ConsensusMode(); got != consensus.PoW {
		t.Fatalf("ConsensusMode() after a rejected SetConsensusMode call = %v, want unchanged PoW", got)
	}
}

func TestSetConsensusModePoSWithValidators(t *testing.T) {
	c := newTestChain(t, map[core.Address]uint64{addr(1): 1000})
	vs := testValidatorSet(t)
	if err := c.SetConsensusMode(consensus.PoS, vs); err != nil {
		t.Fatalf("SetConsensusMode: %v", err)
	}
	if got := c.ConsensusMode(); got != consensus.PoS {
		t.Fatalf("ConsensusMode() = %v, want PoS", got)
	}
	if got := c.ValidatorSet(); got != vs {
		t.Fatal("ValidatorSet() did not return the exact set SetConsensusMode was given")
	}
}
