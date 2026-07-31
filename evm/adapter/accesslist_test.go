package adapter

import (
	"testing"

	"l1chain/state"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

func TestAddAddressToAccessListRoundTrip(t *testing.T) {
	s := New(state.NewMemStateDB())
	if s.AddressInAccessList(addrA) {
		t.Fatal("AddressInAccessList = true before AddAddressToAccessList, want false")
	}
	s.AddAddressToAccessList(addrA)
	if !s.AddressInAccessList(addrA) {
		t.Fatal("AddressInAccessList = false after AddAddressToAccessList, want true")
	}
	if s.AddressInAccessList(addrB) {
		t.Fatal("AddressInAccessList = true for an untouched address, want false")
	}
}

func TestAddSlotToAccessListAlsoWarmsTheAddress(t *testing.T) {
	s := New(state.NewMemStateDB())
	slot := common.HexToHash("0x01")

	addrOk, slotOk := s.SlotInAccessList(addrA, slot)
	if addrOk || slotOk {
		t.Fatalf("SlotInAccessList before any warming = (%v, %v), want (false, false)", addrOk, slotOk)
	}

	s.AddSlotToAccessList(addrA, slot)

	addrOk, slotOk = s.SlotInAccessList(addrA, slot)
	if !addrOk || !slotOk {
		t.Fatalf("SlotInAccessList after AddSlotToAccessList = (%v, %v), want (true, true)", addrOk, slotOk)
	}
	if !s.AddressInAccessList(addrA) {
		t.Fatal("AddressInAccessList = false after AddSlotToAccessList, want true (a warm slot implies a warm address)")
	}

	// A different slot on the same address must NOT be warm.
	otherSlot := common.HexToHash("0x02")
	addrOk, slotOk = s.SlotInAccessList(addrA, otherSlot)
	if !addrOk {
		t.Fatal("address should still read warm for a different slot")
	}
	if slotOk {
		t.Fatal("a different, never-warmed slot on the same address reads warm, want false")
	}
}

func TestSnapshotRevertUndoesAccessListChanges(t *testing.T) {
	s := New(state.NewMemStateDB())
	slot := common.HexToHash("0x01")

	id := s.Snapshot()
	s.AddSlotToAccessList(addrA, slot)
	if !s.AddressInAccessList(addrA) {
		t.Fatal("address should be warm before revert")
	}

	s.RevertToSnapshot(id)

	if s.AddressInAccessList(addrA) {
		t.Fatal("AddressInAccessList = true after revert, want false")
	}
	addrOk, slotOk := s.SlotInAccessList(addrA, slot)
	if addrOk || slotOk {
		t.Fatalf("SlotInAccessList after revert = (%v, %v), want (false, false)", addrOk, slotOk)
	}
}

func TestPrepareWarmsSenderDestPrecompilesAndShanghaiCoinbase(t *testing.T) {
	s := New(state.NewMemStateDB())
	sender := addrA
	dest := addrB
	coinbase := common.HexToAddress("0xc0ffee00000000000000000000000000000000")
	precompile := common.HexToAddress("0x0000000000000000000000000000000000000001")

	rules := params.Rules{IsEIP2929: true, IsShanghai: true}
	s.Prepare(rules, sender, coinbase, &dest, []common.Address{precompile}, nil)

	for name, addr := range map[string]common.Address{"sender": sender, "dest": dest, "precompile": precompile, "coinbase": coinbase} {
		if !s.AddressInAccessList(addr) {
			t.Fatalf("%s not warm after Prepare (IsShanghai=true), want warm", name)
		}
	}
}

func TestPrepareDoesNotWarmCoinbasePreShanghai(t *testing.T) {
	s := New(state.NewMemStateDB())
	coinbase := common.HexToAddress("0xc0ffee00000000000000000000000000000000")

	rules := params.Rules{IsEIP2929: true, IsShanghai: false}
	s.Prepare(rules, addrA, coinbase, nil, nil, nil)

	if s.AddressInAccessList(coinbase) {
		t.Fatal("coinbase warm after Prepare with IsShanghai=false, want cold (EIP-3651 not active)")
	}
	if !s.AddressInAccessList(addrA) {
		t.Fatal("sender should still be warm regardless of IsShanghai")
	}
}

func TestPrepareResetsAnyPriorAccessList(t *testing.T) {
	s := New(state.NewMemStateDB())
	rules := params.Rules{IsEIP2929: true}
	s.AddAddressToAccessList(addrA)

	s.Prepare(rules, addrB, common.Address{}, nil, nil, nil)

	if s.AddressInAccessList(addrA) {
		t.Fatal("addrA still warm after a fresh Prepare, want the prior access list wiped")
	}
	if !s.AddressInAccessList(addrB) {
		t.Fatal("addrB (the new sender) should be warm after Prepare")
	}
}

func TestTransientStateRoundTrip(t *testing.T) {
	s := New(state.NewMemStateDB())
	slot := common.HexToHash("0x01")
	val := common.HexToHash("0x2a")

	if got := s.GetTransientState(addrA, slot); got != (common.Hash{}) {
		t.Fatalf("fresh transient state = %s, want zero", got)
	}
	s.SetTransientState(addrA, slot, val)
	if got := s.GetTransientState(addrA, slot); got != val {
		t.Fatalf("transient state after Set = %s, want %s", got, val)
	}
}

func TestSnapshotRevertUndoesTransientState(t *testing.T) {
	s := New(state.NewMemStateDB())
	slot := common.HexToHash("0x01")

	id := s.Snapshot()
	s.SetTransientState(addrA, slot, common.HexToHash("0x2a"))
	s.RevertToSnapshot(id)

	if got := s.GetTransientState(addrA, slot); got != (common.Hash{}) {
		t.Fatalf("transient state after revert = %s, want zero", got)
	}
}

func TestPrepareClearsTransientState(t *testing.T) {
	s := New(state.NewMemStateDB())
	slot := common.HexToHash("0x01")
	s.SetTransientState(addrA, slot, common.HexToHash("0x2a"))

	s.Prepare(params.Rules{}, addrA, common.Address{}, nil, nil, nil)

	if got := s.GetTransientState(addrA, slot); got != (common.Hash{}) {
		t.Fatalf("transient state after Prepare = %s, want zero (Prepare must clear it, EIP-1153)", got)
	}
}
