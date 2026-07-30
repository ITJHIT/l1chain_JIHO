package chain

import (
	"testing"

	"l1chain/core"
	"l1chain/state"
)

func addr(b byte) core.Address {
	var a core.Address
	a[0] = b
	return a
}

func TestApplyGenesisFundsAllocAndStateRoot(t *testing.T) {
	g := Genesis{
		Alloc:      map[core.Address]uint64{addr(1): 1000, addr(2): 500},
		Difficulty: 6,
		Timestamp:  1000,
	}

	st := state.New()
	blk := ApplyGenesis(st, g)

	if got := st.GetAccount(addr(1)).Balance; got != 1000 {
		t.Fatalf("addr(1) balance = %d, want 1000", got)
	}
	if got := st.GetAccount(addr(2)).Balance; got != 500 {
		t.Fatalf("addr(2) balance = %d, want 500", got)
	}
	if blk.Header.Height != 0 {
		t.Fatalf("genesis height = %d, want 0", blk.Header.Height)
	}
	if !blk.Header.PrevHash.IsZero() {
		t.Fatalf("genesis PrevHash should be zero")
	}
	if blk.Header.StateRoot != st.StateRoot() {
		t.Fatalf("genesis StateRoot != funded state root")
	}

	// Independently funded state must yield the same root (determinism).
	ref := state.New()
	ref.SetAccount(addr(1), state.Account{Balance: 1000})
	ref.SetAccount(addr(2), state.Account{Balance: 500})
	if blk.Header.StateRoot != ref.StateRoot() {
		t.Fatalf("genesis StateRoot mismatch vs reference funding")
	}

	// ToBlock must match ApplyGenesis on a fresh db.
	if g.ToBlock().Header.StateRoot != blk.Header.StateRoot {
		t.Fatalf("ToBlock StateRoot != ApplyGenesis StateRoot")
	}
}
