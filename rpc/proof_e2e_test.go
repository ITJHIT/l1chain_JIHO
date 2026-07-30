package rpc_test

import (
	"net/http/httptest"
	"testing"

	"l1chain/chain"
	"l1chain/core"
	"l1chain/node"
	"l1chain/rpc"
	"l1chain/vm"
	"l1chain/wallet"
)

// counterCode: SLOAD slot0; +1; SSTORE slot0; STOP. Same tiny contract
// chain/contract_test.go uses, duplicated here (package rpc_test is a
// separate black-box package with no access to chain's unexported test
// helpers) so getStorageProof has a real, known storage slot to prove
// against rather than testing only the account-proof half of the API.
var counterCode = []byte{0x60, 0x00, 0x54, 0x60, 0x01, 0x01, 0x60, 0x00, 0x55, 0x00}

// TestGetAccountProofVerifiesAgainstTheReturnedStateRoot drives a real Node
// behind an httptest RPC server, fetches an account proof over the wire, and
// verifies it exactly the way a light client would: independently, against
// nothing but the stateRoot the response itself named -- not by trusting the
// node's plain getBalance.
func TestGetAccountProofVerifiesAgainstTheReturnedStateRoot(t *testing.T) {
	a, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey(A): %v", err)
	}
	miner, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey(miner): %v", err)
	}

	n, err := node.New(node.Config{
		MinerKey:     miner,
		Difficulty:   6,
		GenesisAlloc: map[core.Address]uint64{a.Address(): 1000},
	})
	if err != nil {
		t.Fatalf("node.New: %v", err)
	}
	defer n.Close()

	srv := httptest.NewServer(rpc.NewServer(n))
	defer srv.Close()
	client := rpc.NewClient(srv.URL)

	if _, err := n.MineBlock(); err != nil { // no txs; just get past genesis
		t.Fatalf("MineBlock: %v", err)
	}

	res, found, err := client.GetAccountProof(a.Address().Hex())
	if err != nil {
		t.Fatalf("GetAccountProof: %v", err)
	}
	if !found {
		t.Fatal("expected account A to be found")
	}
	if res.Proof.Account.Balance != 1000 {
		t.Fatalf("proof carries balance %d, want 1000", res.Proof.Account.Balance)
	}

	acct, ok, err := rpc.VerifyAccountProof(res.StateRoot, a.Address().Hex(), res.Proof)
	if err != nil {
		t.Fatalf("VerifyAccountProof: %v", err)
	}
	if !ok {
		t.Fatal("valid account proof did not verify")
	}
	if acct.Balance != 1000 {
		t.Fatalf("verified account balance = %d, want 1000", acct.Balance)
	}

	// Tamper with the claimed balance (simulating a lying node) and confirm
	// verification catches it.
	tampered := res.Proof
	tampered.Account.Balance = 999999
	if _, ok, err := rpc.VerifyAccountProof(res.StateRoot, a.Address().Hex(), tampered); err != nil {
		t.Fatalf("VerifyAccountProof(tampered): %v", err)
	} else if ok {
		t.Fatal("tampered account proof verified successfully")
	}

	// A stale/wrong state root must also be rejected.
	var wrongRoot core.Hash
	wrongRoot[0] = 0xFF
	if _, ok, err := rpc.VerifyAccountProof(wrongRoot.Hex(), a.Address().Hex(), res.Proof); err != nil {
		t.Fatalf("VerifyAccountProof(wrong root): %v", err)
	} else if ok {
		t.Fatal("proof verified against an unrelated state root")
	}

	// An account that was never touched has no proof to give.
	var never core.Address
	never[19] = 0xAB
	if _, found, err := client.GetAccountProof(never.Hex()); err != nil {
		t.Fatalf("GetAccountProof(never): %v", err)
	} else if found {
		t.Fatal("expected not found for an untouched address")
	}
}

// TestGetStorageProofVerifiesAContractSlot deploys and calls a tiny contract
// through real sendRawTx/MineBlock, then proves and verifies one of its
// storage slots over the wire -- the chained (account -> that account's own
// storage trie) proof, not just the account half.
func TestGetStorageProofVerifiesAContractSlot(t *testing.T) {
	sender, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey(sender): %v", err)
	}
	miner, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey(miner): %v", err)
	}

	n, err := node.New(node.Config{
		MinerKey:     miner,
		Difficulty:   6,
		GenesisAlloc: map[core.Address]uint64{sender.Address(): 10_000_000},
	})
	if err != nil {
		t.Fatalf("node.New: %v", err)
	}
	defer n.Close()

	srv := httptest.NewServer(rpc.NewServer(n))
	defer srv.Close()
	client := rpc.NewClient(srv.URL)

	deploy := core.Transaction{To: core.Address{}, Nonce: 0, GasLimit: 100000, ChainID: chain.DefaultChainID, Data: counterCode}
	sender.Sign(&deploy)
	if _, err := client.SendRawTx(deploy); err != nil {
		t.Fatalf("SendRawTx(deploy): %v", err)
	}
	if _, err := n.MineBlock(); err != nil {
		t.Fatalf("MineBlock(deploy): %v", err)
	}

	contract := vm.CreateAddress(sender.Address(), 0)
	call := core.Transaction{To: contract, Nonce: 1, GasLimit: 100000, ChainID: chain.DefaultChainID}
	sender.Sign(&call)
	if _, err := client.SendRawTx(call); err != nil {
		t.Fatalf("SendRawTx(call): %v", err)
	}
	if _, err := n.MineBlock(); err != nil {
		t.Fatalf("MineBlock(call): %v", err)
	}

	var slot0 core.Hash // slot 0
	res, found, err := client.GetStorageProof(contract.Hex(), slot0.Hex())
	if err != nil {
		t.Fatalf("GetStorageProof: %v", err)
	}
	if !found {
		t.Fatal("expected slot 0 to be found after one call")
	}

	var wantOne core.Hash
	wantOne[core.HashLen-1] = 1
	if res.Proof.SlotProof == nil {
		t.Fatal("expected a non-empty slot proof")
	}

	val, ok, err := rpc.VerifyStorageProof(res.StateRoot, contract.Hex(), slot0.Hex(), res.Proof)
	if err != nil {
		t.Fatalf("VerifyStorageProof: %v", err)
	}
	if !ok {
		t.Fatal("valid storage proof did not verify")
	}
	if val != wantOne {
		t.Fatalf("verified slot value = %x, want %x", val, wantOne)
	}

	// Tamper with the claimed value.
	tampered := res.Proof
	var wrongVal core.Hash
	wrongVal[0] = 0xEE
	tampered.Value = wrongVal.Hex()
	if _, ok, err := rpc.VerifyStorageProof(res.StateRoot, contract.Hex(), slot0.Hex(), tampered); err != nil {
		t.Fatalf("VerifyStorageProof(tampered): %v", err)
	} else if ok {
		t.Fatal("tampered storage proof verified successfully")
	}
}
