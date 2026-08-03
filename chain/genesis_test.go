package chain

import (
	"testing"

	"l1chain/core"
	"l1chain/exchange"
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

// TestNewChainWithAllocMatchesGenesisStateRoot is the load-bearing check for
// the base-asset alloc plumbing: NewChainWithAlloc's fundGenesis() replay
// (the same path every future mining/validation deriveState call reuses) must
// agree, byte for byte, with the state ApplyGenesis already baked into the
// genesis block's own StateRoot. A mismatch here would mean genesis and every
// subsequent block would fail ErrBadStateRoot from height 0 onward -- the same
// shadow-computation-diverges-from-production failure class the M5 MPT
// migration already had to guard against.
func TestNewChainWithAllocMatchesGenesisStateRoot(t *testing.T) {
	alloc := map[core.Address]uint64{addr(1): 1000}
	baseAlloc := map[core.Address]uint64{addr(2): 500}
	g := Genesis{Alloc: alloc, BaseAlloc: baseAlloc, Difficulty: 6, Timestamp: 1000}
	genesis := g.ToBlock()

	c := NewChainWithAlloc(genesis, alloc, baseAlloc)

	if c.State().StateRoot() != genesis.Header.StateRoot {
		t.Fatalf("Chain.State().StateRoot() = %x, want genesis StateRoot %x",
			c.State().StateRoot(), genesis.Header.StateRoot)
	}
	head := c.Head()
	if head.Hash() != genesis.Hash() {
		t.Fatalf("Chain.Head() != genesis block")
	}
	if total, _ := exchange.BaseOf(c.State(), addr(2)); total != 500 {
		t.Fatalf("Chain.State() base balance for addr(2) = %d, want 500", total)
	}

	// NewChain (no base alloc) must be unaffected -- confirms the new
	// constructor didn't change existing zero-base-alloc behavior.
	plainGenesis := Genesis{Alloc: alloc, Difficulty: 6, Timestamp: 1000}.ToBlock()
	plain := NewChain(plainGenesis, alloc)
	if plain.State().StateRoot() != plainGenesis.Header.StateRoot {
		t.Fatalf("NewChain (no base alloc) StateRoot mismatch")
	}
}

// TestGenesisInitialBaseFeeSeedsHeaderBaseFee proves InitialBaseFee (M9)
// actually reaches the genesis header -- there is no parent block to derive
// BaseFee from (chain.ComputeBaseFee needs one), so genesis must seed it
// directly, exactly like Difficulty already does.
func TestGenesisInitialBaseFeeSeedsHeaderBaseFee(t *testing.T) {
	g := Genesis{
		Alloc:          map[core.Address]uint64{addr(1): 1000},
		Difficulty:     6,
		Timestamp:      1000,
		InitialBaseFee: 42,
	}
	blk := g.ToBlock()
	if blk.Header.BaseFee != 42 {
		t.Fatalf("genesis BaseFee = %d, want 42", blk.Header.BaseFee)
	}
	if blk.Header.GasUsed != 0 {
		t.Fatalf("genesis GasUsed = %d, want 0 (no transactions)", blk.Header.GasUsed)
	}
}

// TestGenesisZeroInitialBaseFeeIsExplicitZero proves the zero value is a real,
// unconfigured-genesis default (no accidental fallback), matching every other
// Genesis field's own zero-value behavior (Difficulty/Timestamp default to 0
// exactly the same way).
func TestGenesisZeroInitialBaseFeeIsExplicitZero(t *testing.T) {
	g := Genesis{Alloc: map[core.Address]uint64{addr(1): 1000}}
	if got := g.ToBlock().Header.BaseFee; got != 0 {
		t.Fatalf("genesis BaseFee with unset InitialBaseFee = %d, want 0", got)
	}
}

// TestNewChainDefaultsToDefaultGasLimit mirrors chainID's own default-value
// precedent (NewChainWithAlloc initializes chainID: DefaultChainID) for the
// M9 gasLimit field.
func TestNewChainDefaultsToDefaultGasLimit(t *testing.T) {
	alloc := map[core.Address]uint64{addr(1): 1000}
	g := Genesis{Alloc: alloc, Difficulty: 6, Timestamp: 1000}
	c := NewChain(g.ToBlock(), alloc)
	if got := c.GasLimit(); got != DefaultGasLimit {
		t.Fatalf("GasLimit() = %d, want DefaultGasLimit (%d)", got, DefaultGasLimit)
	}
}

// TestSetGasLimitOverridesDefault mirrors SetChainID's own override
// precedent: set once, immediately after NewChain, same as node.New does for
// Config.ChainID.
func TestSetGasLimitOverridesDefault(t *testing.T) {
	alloc := map[core.Address]uint64{addr(1): 1000}
	g := Genesis{Alloc: alloc, Difficulty: 6, Timestamp: 1000}
	c := NewChain(g.ToBlock(), alloc)
	c.SetGasLimit(100_000)
	if got := c.GasLimit(); got != 100_000 {
		t.Fatalf("GasLimit() after SetGasLimit(100_000) = %d, want 100_000", got)
	}
}

func TestApplyGenesisFundsBaseAllocAndStateRoot(t *testing.T) {
	g := Genesis{
		Alloc:      map[core.Address]uint64{addr(1): 1000},
		BaseAlloc:  map[core.Address]uint64{addr(2): 250, addr(3): 750},
		Difficulty: 6,
		Timestamp:  1000,
	}

	st := state.New()
	blk := ApplyGenesis(st, g)

	if total, locked := exchange.BaseOf(st, addr(2)); total != 250 || locked != 0 {
		t.Fatalf("addr(2) base = (%d,%d), want (250,0)", total, locked)
	}
	if total, locked := exchange.BaseOf(st, addr(3)); total != 750 || locked != 0 {
		t.Fatalf("addr(3) base = (%d,%d), want (750,0)", total, locked)
	}
	if blk.Header.StateRoot != st.StateRoot() {
		t.Fatalf("genesis StateRoot != funded state root")
	}

	// Independently funded state (native alloc via SetAccount, base alloc via
	// exchange.CreditBase directly, exactly how BaseAlloc's existing five test
	// call sites already fund it) must yield the same root -- determinism, and
	// proof that ApplyGenesis's BaseAlloc loop does exactly what CreditBase does.
	ref := state.New()
	ref.SetAccount(addr(1), state.Account{Balance: 1000})
	exchange.CreditBase(ref, addr(2), 250)
	exchange.CreditBase(ref, addr(3), 750)
	if blk.Header.StateRoot != ref.StateRoot() {
		t.Fatalf("genesis StateRoot mismatch vs reference BaseAlloc funding")
	}

	// ToBlock must match ApplyGenesis on a fresh db.
	if g.ToBlock().Header.StateRoot != blk.Header.StateRoot {
		t.Fatalf("ToBlock StateRoot != ApplyGenesis StateRoot")
	}
}
