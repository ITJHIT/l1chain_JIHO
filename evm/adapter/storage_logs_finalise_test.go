package adapter

import (
	"testing"

	"l1chain/state"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

func TestGetSetStateRoundTrip(t *testing.T) {
	s := New(state.NewMemStateDB())
	slot := common.HexToHash("0x01")
	val := common.HexToHash("0x2a")

	if got := s.GetState(addrA, slot); got != (common.Hash{}) {
		t.Fatalf("fresh state = %s, want zero", got)
	}
	prev := s.SetState(addrA, slot, val)
	if prev != (common.Hash{}) {
		t.Fatalf("SetState returned previous = %s, want zero", prev)
	}
	if got := s.GetState(addrA, slot); got != val {
		t.Fatalf("state after Set = %s, want %s", got, val)
	}
	if !s.Exist(addrA) {
		t.Fatal("Exist = false after SetState, want true (SetState must touch)")
	}
}

func TestGetStateAndCommittedStateTracksPreTransactionValue(t *testing.T) {
	s := New(state.NewMemStateDB())
	slot := common.HexToHash("0x01")
	original := common.HexToHash("0x01")
	updated := common.HexToHash("0x02")

	// Simulate a slot that already had a value before this transaction
	// started, then call Prepare to mark "start of transaction" the same
	// way real callers (deployVia/callVia) always do.
	s.SetState(addrA, slot, original)
	s.Prepare(params.Rules{}, addrA, common.Address{}, nil, nil, nil)

	cur, committed := s.GetStateAndCommittedState(addrA, slot)
	if cur != original || committed != original {
		t.Fatalf("immediately after Prepare, (current, committed) = (%s, %s), want both %s", cur, committed, original)
	}

	s.SetState(addrA, slot, updated)
	cur, committed = s.GetStateAndCommittedState(addrA, slot)
	if cur != updated {
		t.Fatalf("current after update = %s, want %s", cur, updated)
	}
	if committed != original {
		t.Fatalf("committed after update = %s, want unchanged %s (committed must reflect pre-transaction value)", committed, original)
	}

	// A second Prepare (next transaction) must re-baseline committed to
	// whatever is now persisted.
	s.Prepare(params.Rules{}, addrA, common.Address{}, nil, nil, nil)
	_, committed = s.GetStateAndCommittedState(addrA, slot)
	if committed != updated {
		t.Fatalf("committed after a fresh Prepare = %s, want %s (this transaction's own baseline)", committed, updated)
	}
}

func TestSnapshotRevertUndoesSetState(t *testing.T) {
	s := New(state.NewMemStateDB())
	slot := common.HexToHash("0x01")

	id := s.Snapshot()
	s.SetState(addrA, slot, common.HexToHash("0x2a"))
	s.RevertToSnapshot(id)

	if got := s.GetState(addrA, slot); got != (common.Hash{}) {
		t.Fatalf("state after revert = %s, want zero", got)
	}
}

func TestAddLogTagsAndAccumulates(t *testing.T) {
	s := New(state.NewMemStateDB())
	thash := common.HexToHash("0xaa")
	s.SetTxContext(thash, 3, 0)

	s.AddLog(&types.Log{Address: addrA})
	s.AddLog(&types.Log{Address: addrB})

	logs := s.Logs()
	if len(logs) != 2 {
		t.Fatalf("len(Logs()) = %d, want 2", len(logs))
	}
	for i, lg := range logs {
		if lg.TxHash != thash {
			t.Fatalf("logs[%d].TxHash = %s, want %s", i, lg.TxHash, thash)
		}
		if lg.TxIndex != 3 {
			t.Fatalf("logs[%d].TxIndex = %d, want 3", i, lg.TxIndex)
		}
		if int(lg.Index) != i {
			t.Fatalf("logs[%d].Index = %d, want %d", i, lg.Index, i)
		}
	}
}

func TestSnapshotRevertUndoesAddLog(t *testing.T) {
	s := New(state.NewMemStateDB())
	s.AddLog(&types.Log{Address: addrA})

	id := s.Snapshot()
	s.AddLog(&types.Log{Address: addrB})
	if len(s.Logs()) != 2 {
		t.Fatalf("len(Logs()) before revert = %d, want 2", len(s.Logs()))
	}

	s.RevertToSnapshot(id)

	logs := s.Logs()
	if len(logs) != 1 {
		t.Fatalf("len(Logs()) after revert = %d, want 1 (the reverted call's log must not survive)", len(logs))
	}
	if logs[0].Address != addrA {
		t.Fatalf("surviving log address = %s, want %s", logs[0].Address, addrA)
	}
}

func TestFinaliseDeletesSelfDestructedAccounts(t *testing.T) {
	s := New(state.NewMemStateDB())
	s.AddBalance(addrA, u256(100), tracing.BalanceChangeUnspecified)
	s.CreateContract(addrA) // IsNewContract(addrA) = true
	s.SelfDestruct(addrA)

	s.Finalise(true)

	if s.Exist(addrA) {
		t.Fatal("Exist = true after Finalise on a self-destructed account, want it deleted")
	}
	if got := s.GetBalance(addrA); !got.IsZero() {
		t.Fatalf("balance after Finalise-deletion = %s, want 0", got)
	}
}

func TestFinaliseDeletesEmptyTouchedAccountsWhenRequested(t *testing.T) {
	s := New(state.NewMemStateDB())
	s.Touch(addrA) // e.g. a zero-value CALL to a never-funded address

	s.Finalise(true)

	if s.Exist(addrA) {
		t.Fatal("Exist = true after Finalise(deleteEmptyObjects=true) on an empty touched account, want it deleted")
	}
}

func TestFinaliseKeepsEmptyAccountsWhenNotRequested(t *testing.T) {
	s := New(state.NewMemStateDB())
	s.Touch(addrA)

	s.Finalise(false)

	if !s.Exist(addrA) {
		t.Fatal("Exist = false after Finalise(deleteEmptyObjects=false), want the touched-but-empty account to survive")
	}
}

func TestFinaliseLeavesNonEmptyUnselfdestructedAccountsAlone(t *testing.T) {
	s := New(state.NewMemStateDB())
	s.AddBalance(addrA, u256(1), tracing.BalanceChangeUnspecified)

	s.Finalise(true)

	if !s.Exist(addrA) {
		t.Fatal("Exist = false after Finalise on a funded, non-destructed account, want it to survive")
	}
	if got := s.GetBalance(addrA); got.Cmp(u256(1)) != 0 {
		t.Fatalf("balance after Finalise = %s, want 1 (unaffected)", got)
	}
}
