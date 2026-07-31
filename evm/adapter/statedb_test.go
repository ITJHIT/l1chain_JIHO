package adapter

import (
	"bytes"
	"testing"

	"l1chain/core"
	"l1chain/state"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/holiman/uint256"
)

var (
	addrA = common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	addrB = common.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
)

func u256(v uint64) *uint256.Int { return new(uint256.Int).SetUint64(v) }

func TestBalanceAddSubRoundTrip(t *testing.T) {
	s := New(state.NewMemStateDB())

	if got := s.GetBalance(addrA); !got.IsZero() {
		t.Fatalf("fresh balance = %s, want 0", got)
	}

	prev := s.AddBalance(addrA, u256(100), tracing.BalanceChangeUnspecified)
	if !prev.IsZero() {
		t.Fatalf("AddBalance returned previous = %s, want 0", &prev)
	}
	if got := s.GetBalance(addrA); got.Cmp(u256(100)) != 0 {
		t.Fatalf("balance after AddBalance(100) = %s, want 100", got)
	}

	prev = s.SubBalance(addrA, u256(40), tracing.BalanceChangeUnspecified)
	if prev.Cmp(u256(100)) != 0 {
		t.Fatalf("SubBalance returned previous = %s, want 100", &prev)
	}
	if got := s.GetBalance(addrA); got.Cmp(u256(60)) != 0 {
		t.Fatalf("balance after SubBalance(40) = %s, want 60", got)
	}
}

func TestBalanceOverflowLatchesErrAndLeavesBalanceUnchanged(t *testing.T) {
	s := New(state.NewMemStateDB())
	s.AddBalance(addrA, u256(5), tracing.BalanceChangeUnspecified)

	huge := new(uint256.Int).Sub(new(uint256.Int).Lsh(u256(1), 128), u256(1)) // 2^128 - 1, far past uint64
	s.AddBalance(addrA, huge, tracing.BalanceChangeUnspecified)

	if s.Err() == nil {
		t.Fatal("Err() = nil after an overflowing AddBalance, want a latched error")
	}
	if got := s.GetBalance(addrA); got.Cmp(u256(5)) != 0 {
		t.Fatalf("balance after overflowing AddBalance = %s, want unchanged 5", got)
	}
}

func TestBalanceUnderflowLatchesErrAndLeavesBalanceUnchanged(t *testing.T) {
	s := New(state.NewMemStateDB())
	s.AddBalance(addrA, u256(5), tracing.BalanceChangeUnspecified)

	s.SubBalance(addrA, u256(6), tracing.BalanceChangeUnspecified)

	if s.Err() == nil {
		t.Fatal("Err() = nil after an underflowing SubBalance, want a latched error")
	}
	if got := s.GetBalance(addrA); got.Cmp(u256(5)) != 0 {
		t.Fatalf("balance after underflowing SubBalance = %s, want unchanged 5", got)
	}
}

func TestNonceRoundTrip(t *testing.T) {
	s := New(state.NewMemStateDB())

	if got := s.GetNonce(addrA); got != 0 {
		t.Fatalf("fresh nonce = %d, want 0", got)
	}
	s.SetNonce(addrA, 7, tracing.NonceChangeUnspecified)
	if got := s.GetNonce(addrA); got != 7 {
		t.Fatalf("nonce after SetNonce(7) = %d, want 7", got)
	}
}

func TestCodeRoundTripUsesL1ChainHashConvention(t *testing.T) {
	s := New(state.NewMemStateDB())
	code := []byte{0x60, 0x01, 0x60, 0x02, 0x01} // PUSH1 1 PUSH1 2 ADD -- arbitrary real-looking bytecode

	if got := s.GetCodeHash(addrA); got != (common.Hash{}) {
		t.Fatalf("fresh codehash = %s, want zero (l1chain's no-code convention)", got)
	}

	prev := s.SetCode(addrA, code, tracing.CodeChangeUnspecified)
	if prev != nil {
		t.Fatalf("SetCode returned previous code = %x, want nil", prev)
	}
	if got := s.GetCode(addrA); !bytes.Equal(got, code) {
		t.Fatalf("GetCode = %x, want %x", got, code)
	}
	if got := s.GetCodeSize(addrA); got != len(code) {
		t.Fatalf("GetCodeSize = %d, want %d", got, len(code))
	}
	wantHash := common.Hash(core.SumHash(code))
	if got := s.GetCodeHash(addrA); got != wantHash {
		t.Fatalf("GetCodeHash = %s, want %s (l1chain's core.SumHash, not keccak256)", got, wantHash)
	}
}

func TestSnapshotRevertUndoesBalanceNonceAndCodeTogether(t *testing.T) {
	s := New(state.NewMemStateDB())
	s.AddBalance(addrA, u256(10), tracing.BalanceChangeUnspecified)
	s.SetNonce(addrA, 1, tracing.NonceChangeUnspecified)

	id := s.Snapshot()

	s.AddBalance(addrA, u256(90), tracing.BalanceChangeUnspecified)
	s.SetNonce(addrA, 2, tracing.NonceChangeUnspecified)
	s.SetCode(addrA, []byte{0xfe}, tracing.CodeChangeUnspecified)

	if got := s.GetBalance(addrA); got.Cmp(u256(100)) != 0 {
		t.Fatalf("balance before revert = %s, want 100", got)
	}

	s.RevertToSnapshot(id)

	if got := s.GetBalance(addrA); got.Cmp(u256(10)) != 0 {
		t.Fatalf("balance after revert = %s, want 10 (pre-snapshot value)", got)
	}
	if got := s.GetNonce(addrA); got != 1 {
		t.Fatalf("nonce after revert = %d, want 1 (pre-snapshot value)", got)
	}
	if got := s.GetCode(addrA); got != nil {
		t.Fatalf("code after revert = %x, want nil (pre-snapshot value)", got)
	}
	if got := s.GetCodeHash(addrA); got != (common.Hash{}) {
		t.Fatalf("codehash after revert = %s, want zero", got)
	}
}

func TestNestedSnapshotsRevertToEarlierDiscardsLaterOnes(t *testing.T) {
	s := New(state.NewMemStateDB())

	id1 := s.Snapshot()
	s.SetNonce(addrA, 1, tracing.NonceChangeUnspecified)
	_ = s.Snapshot() // id2, intentionally never reverted to directly
	s.SetNonce(addrA, 2, tracing.NonceChangeUnspecified)
	_ = s.Snapshot() // id3
	s.SetNonce(addrA, 3, tracing.NonceChangeUnspecified)

	if got := s.GetNonce(addrA); got != 3 {
		t.Fatalf("nonce before revert = %d, want 3", got)
	}

	// Reverting straight to id1 must undo id2's and id3's writes too, not
	// just the most recent one.
	s.RevertToSnapshot(id1)

	if got := s.GetNonce(addrA); got != 0 {
		t.Fatalf("nonce after reverting to id1 = %d, want 0 (state before any of the three writes)", got)
	}

	// The journal must be truncated, not just logically skipped: taking a
	// fresh snapshot right after revert should reproduce the same id,
	// proving the reverted entries were actually discarded.
	if got := s.Snapshot(); got != id1 {
		t.Fatalf("Snapshot() immediately after RevertToSnapshot(id1) = %d, want %d", got, id1)
	}
}

// TestAdapterWorksAgainstTheRealMPTStateDB proves the adapter isn't only
// correct against the simple in-memory reference implementation -- it
// exercises the exact same production factory (state.New()) chain/chain.go
// will eventually use, since mptStateDB's isEmpty-based deletion semantics
// (state/mpt.go) differ from memStateDB's and are the ones that actually
// matter once this is wired into consensus.
func TestAdapterWorksAgainstTheRealMPTStateDB(t *testing.T) {
	s := New(state.New())
	code := []byte{0x60, 0x00, 0x60, 0x00, 0xfd} // PUSH1 0 PUSH1 0 REVERT

	s.AddBalance(addrB, u256(1_000), tracing.BalanceChangeUnspecified)
	s.SetNonce(addrB, 5, tracing.NonceChangeUnspecified)
	s.SetCode(addrB, code, tracing.CodeChangeUnspecified)

	if got := s.GetBalance(addrB); got.Cmp(u256(1_000)) != 0 {
		t.Fatalf("balance = %s, want 1000", got)
	}
	if got := s.GetNonce(addrB); got != 5 {
		t.Fatalf("nonce = %d, want 5", got)
	}
	if got := s.GetCode(addrB); !bytes.Equal(got, code) {
		t.Fatalf("code = %x, want %x", got, code)
	}

	id := s.Snapshot()
	s.SubBalance(addrB, u256(1_000), tracing.BalanceChangeUnspecified)
	s.SetNonce(addrB, 0, tracing.NonceChangeUnspecified)
	s.SetCode(addrB, nil, tracing.CodeChangeUnspecified)
	if got := s.GetBalance(addrB); !got.IsZero() {
		t.Fatalf("balance after zeroing everything = %s, want 0", got)
	}

	s.RevertToSnapshot(id)
	if got := s.GetBalance(addrB); got.Cmp(u256(1_000)) != 0 {
		t.Fatalf("balance after revert = %s, want 1000 restored", got)
	}
	if got := s.GetNonce(addrB); got != 5 {
		t.Fatalf("nonce after revert = %d, want 5 restored", got)
	}
	if got := s.GetCode(addrB); !bytes.Equal(got, code) {
		t.Fatalf("code after revert = %x, want %x restored", got, code)
	}
}
