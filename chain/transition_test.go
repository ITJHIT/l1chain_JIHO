package chain

import (
	"errors"
	"testing"

	"l1chain/core"
	"l1chain/state"
)

func acceptAll(tx core.Transaction) bool { return true }
func rejectAll(tx core.Transaction) bool { return false }

func fundedState(bals map[core.Address]uint64) state.StateDB {
	st := state.NewMemStateDB()
	for a, b := range bals {
		st.SetAccount(a, state.Account{Balance: b})
	}
	return st
}

func TestApplyTxHappyPath(t *testing.T) {
	st := fundedState(map[core.Address]uint64{addr(1): 1000})
	tx := core.Transaction{From: addr(1), To: addr(2), Value: 300, Nonce: 0, Signature: []byte{1}}

	if err := ApplyTx(st, tx, acceptAll); err != nil {
		t.Fatalf("ApplyTx: %v", err)
	}
	if got := st.GetAccount(addr(1)); got.Balance != 700 || got.Nonce != 1 {
		t.Fatalf("sender = %+v, want balance 700 nonce 1", got)
	}
	if got := st.GetAccount(addr(2)).Balance; got != 300 {
		t.Fatalf("recipient balance = %d, want 300", got)
	}
}

func TestApplyTxRejectsBadNonce(t *testing.T) {
	st := fundedState(map[core.Address]uint64{addr(1): 1000})
	tx := core.Transaction{From: addr(1), To: addr(2), Value: 10, Nonce: 5, Signature: []byte{1}}
	if err := ApplyTx(st, tx, acceptAll); !errors.Is(err, ErrBadNonce) {
		t.Fatalf("err = %v, want ErrBadNonce", err)
	}
	if st.GetAccount(addr(1)).Balance != 1000 {
		t.Fatalf("state mutated on bad nonce")
	}
}

func TestApplyTxRejectsInsufficientBalance(t *testing.T) {
	st := fundedState(map[core.Address]uint64{addr(1): 50})
	tx := core.Transaction{From: addr(1), To: addr(2), Value: 100, Nonce: 0, Signature: []byte{1}}
	if err := ApplyTx(st, tx, acceptAll); !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("err = %v, want ErrInsufficientBalance", err)
	}
	if st.GetAccount(addr(2)).Balance != 0 {
		t.Fatalf("recipient credited on insufficient balance")
	}
}

func TestApplyTxRejectsBadSignature(t *testing.T) {
	st := fundedState(map[core.Address]uint64{addr(1): 1000})
	tx := core.Transaction{From: addr(1), To: addr(2), Value: 10, Nonce: 0, Signature: []byte{1}}
	if err := ApplyTx(st, tx, rejectAll); !errors.Is(err, ErrBadSig) {
		t.Fatalf("err = %v, want ErrBadSig", err)
	}
	if st.GetAccount(addr(1)).Balance != 1000 {
		t.Fatalf("state mutated on bad signature")
	}
}

func TestApplyBlockAtomicity(t *testing.T) {
	st := fundedState(map[core.Address]uint64{addr(1): 1000})
	good := core.Transaction{From: addr(1), To: addr(2), Value: 100, Nonce: 0, Signature: []byte{1}}
	bad := core.Transaction{From: addr(1), To: addr(3), Value: 100, Nonce: 99, Signature: []byte{1}} // bad nonce

	b := core.Block{Txs: []core.Transaction{good, bad}}
	err := ApplyBlock(st, b, addr(9), acceptAll)
	if !errors.Is(err, ErrBadNonce) {
		t.Fatalf("err = %v, want ErrBadNonce", err)
	}
	// Committed state must be untouched: no debit, no credit, no coinbase.
	if got := st.GetAccount(addr(1)).Balance; got != 1000 {
		t.Fatalf("sender balance = %d, want 1000 (atomicity broken)", got)
	}
	if got := st.GetAccount(addr(2)).Balance; got != 0 {
		t.Fatalf("recipient credited despite failed block (atomicity broken)")
	}
	if got := st.GetAccount(addr(9)).Balance; got != 0 {
		t.Fatalf("miner credited despite failed block (atomicity broken)")
	}
}

func TestApplyBlockCoinbase(t *testing.T) {
	st := fundedState(map[core.Address]uint64{addr(1): 1000})
	tx := core.Transaction{From: addr(1), To: addr(2), Value: 100, Nonce: 0, Signature: []byte{1}}
	b := core.Block{Txs: []core.Transaction{tx}}

	if err := ApplyBlock(st, b, addr(9), acceptAll); err != nil {
		t.Fatalf("ApplyBlock: %v", err)
	}
	if got := st.GetAccount(addr(9)).Balance; got != BlockReward {
		t.Fatalf("miner balance = %d, want %d", got, BlockReward)
	}
	if got := st.GetAccount(addr(1)).Balance; got != 900 {
		t.Fatalf("sender balance = %d, want 900", got)
	}
	if got := st.GetAccount(addr(2)).Balance; got != 100 {
		t.Fatalf("recipient balance = %d, want 100", got)
	}
}
