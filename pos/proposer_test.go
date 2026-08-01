package pos

import (
	"encoding/binary"
	"errors"
	"testing"

	"l1chain/core"
)

func hashFromUint64(v uint64) core.Hash {
	var h core.Hash
	binary.BigEndian.PutUint64(h[len(h)-8:], v)
	return h
}

func TestProposerSeedDeterministic(t *testing.T) {
	parent := core.SumHash([]byte("some block"))
	s1 := ProposerSeed(parent, 42)
	s2 := ProposerSeed(parent, 42)
	if s1 != s2 {
		t.Fatal("ProposerSeed is not deterministic for identical inputs")
	}
}

func TestProposerSeedDiffersByHeightAndParent(t *testing.T) {
	parentA := core.SumHash([]byte("branch A"))
	parentB := core.SumHash([]byte("branch B"))
	if ProposerSeed(parentA, 1) == ProposerSeed(parentA, 2) {
		t.Fatal("ProposerSeed did not change when only height changed")
	}
	if ProposerSeed(parentA, 1) == ProposerSeed(parentB, 1) {
		t.Fatal("ProposerSeed did not change when only parentHash changed")
	}
}

func TestSelectProposerErrorsOnNoActiveValidators(t *testing.T) {
	if _, err := SelectProposer(nil, 0, core.Hash{}); !errors.Is(err, ErrNoActiveValidators) {
		t.Fatalf("err = %v, want ErrNoActiveValidators", err)
	}
	v := ValidatorInfo{Address: testAddr(1), Stake: 10}
	if _, err := SelectProposer([]ValidatorInfo{v}, 0, core.Hash{}); !errors.Is(err, ErrNoActiveValidators) {
		t.Fatalf("err = %v, want ErrNoActiveValidators (zero total stake)", err)
	}
}

// TestSelectProposerWeightedByStakeRanges proves the cumulative-stake walk
// lands exactly where the math says it should, at both interior points and
// range boundaries -- not a statistical/probabilistic check, matching this
// repo's own "relational, precisely-derived" test ethos.
func TestSelectProposerWeightedByStakeRanges(t *testing.T) {
	v0 := ValidatorInfo{Address: testAddr(1), Stake: 10} // range [0,10)
	v1 := ValidatorInfo{Address: testAddr(2), Stake: 20} // range [10,30)
	v2 := ValidatorInfo{Address: testAddr(3), Stake: 70} // range [30,100)
	active := []ValidatorInfo{v0, v1, v2}
	const total = 100

	cases := []struct {
		target uint64
		want   core.Address
	}{
		{0, v0.Address},
		{9, v0.Address},
		{10, v1.Address},
		{29, v1.Address},
		{30, v2.Address},
		{99, v2.Address},
	}
	for _, c := range cases {
		got, err := SelectProposer(active, total, hashFromUint64(c.target))
		if err != nil {
			t.Fatalf("target=%d: %v", c.target, err)
		}
		if got.Address != c.want {
			t.Fatalf("target=%d: proposer = %x, want %x", c.target, got.Address, c.want)
		}
	}
}

func TestSelectProposerDeterministicAcrossRepeatedCalls(t *testing.T) {
	v0 := ValidatorInfo{Address: testAddr(1), Stake: 10}
	v1 := ValidatorInfo{Address: testAddr(2), Stake: 90}
	active := []ValidatorInfo{v0, v1}
	seed := core.SumHash([]byte("height 5"))
	first, err := SelectProposer(active, 100, seed)
	if err != nil {
		t.Fatalf("SelectProposer: %v", err)
	}
	for i := 0; i < 10; i++ {
		got, err := SelectProposer(active, 100, seed)
		if err != nil {
			t.Fatalf("SelectProposer[%d]: %v", i, err)
		}
		if got.Address != first.Address {
			t.Fatalf("SelectProposer[%d] = %x, want %x (same every call for identical inputs)", i, got.Address, first.Address)
		}
	}
}
