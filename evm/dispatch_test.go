package evm

import (
	"bytes"
	"testing"

	"l1chain/core"
	"l1chain/exchange"
	"l1chain/vm"
)

// deployer/recipient in this package's other test files (erc20_test.go)
// are go-ethereum common.Address values, not l1chain's own core.Address --
// DeriveContractAddress works in l1chain's own address space, so this file
// needs its own core.Address-typed test addresses.
var (
	dispatchFrom      = core.Address{0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa}
	dispatchFromOther = core.Address{0xbb, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb}
)

func TestTagUntagCodeRoundTrip(t *testing.T) {
	runtime := []byte{0x60, 0x01, 0x60, 0x02, 0x01} // PUSH1 1 PUSH1 2 ADD

	tagged := TagCode(runtime)
	if !IsTaggedCode(tagged) {
		t.Fatal("IsTaggedCode(TagCode(runtime)) = false, want true")
	}
	if got := UntagCode(tagged); !bytes.Equal(got, runtime) {
		t.Fatalf("UntagCode(TagCode(runtime)) = %x, want %x", got, runtime)
	}
}

func TestIsTaggedCodeFalseForM3Bytecode(t *testing.T) {
	m3Code := ERC20CreationCode() // the hand-assembled M3 fixture, a real, sizeable, non-empty program
	if len(m3Code) == 0 {
		t.Fatal("ERC20CreationCode() returned empty, test fixture broken")
	}
	if IsTaggedCode(m3Code) {
		t.Fatal("IsTaggedCode(M3 bytecode) = true, want false -- M3 bytecode must never be mistaken for a tagged EVM contract")
	}
	if got := UntagCode(m3Code); !bytes.Equal(got, m3Code) {
		t.Fatal("UntagCode on untagged code must return it unchanged")
	}
}

func TestIsTaggedCodeFalseForShortOrEmptyCode(t *testing.T) {
	for _, code := range [][]byte{nil, {}, {0xFE}, {0xFE, 'L', '1'}} {
		if IsTaggedCode(code) {
			t.Fatalf("IsTaggedCode(%x) = true, want false (shorter than the full magic)", code)
		}
	}
}

func TestDeriveContractAddressDeterministic(t *testing.T) {
	a1 := DeriveContractAddress(dispatchFrom, 0)
	a2 := DeriveContractAddress(dispatchFrom, 0)
	if a1 != a2 {
		t.Fatalf("DeriveContractAddress is not deterministic: %x != %x", a1, a2)
	}
	if a3 := DeriveContractAddress(dispatchFrom, 1); a3 == a1 {
		t.Fatal("DeriveContractAddress(dispatchFrom, 0) == DeriveContractAddress(dispatchFrom, 1), want distinct addresses per nonce")
	}
	if a4 := DeriveContractAddress(dispatchFromOther, 0); a4 == a1 {
		t.Fatal("DeriveContractAddress(dispatchFrom, 0) == DeriveContractAddress(dispatchFromOther, 0), want distinct addresses per sender")
	}
}

// TestDeriveContractAddressNeverCollidesWithM3 proves the domain separation
// is real, not just a hopeful naming choice: for a range of (from, nonce)
// pairs, evm.DeriveContractAddress and M3's own vm.CreateAddress (identical
// SumHash(from||nonce) shape, minus the prefix) must never agree.
func TestDeriveContractAddressNeverCollidesWithM3(t *testing.T) {
	addrs := []core.Address{dispatchFrom, dispatchFromOther}
	for _, from := range addrs {
		for nonce := uint64(0); nonce < 5; nonce++ {
			evmAddr := DeriveContractAddress(from, nonce)
			m3Addr := vm.CreateAddress(from, nonce)
			if evmAddr == m3Addr {
				t.Fatalf("DeriveContractAddress(%x, %d) collided with vm.CreateAddress: %x", from, nonce, evmAddr)
			}
		}
	}
}

func TestDeployAddressDistinctFromExchangeAddress(t *testing.T) {
	if DeployAddress == exchange.Address {
		t.Fatal("evm.DeployAddress == exchange.Address, want two distinct reserved addresses")
	}
	if DeployAddress == (core.Address{}) {
		t.Fatal("evm.DeployAddress is the zero address, which M3 already reserves for its own creation convention")
	}
}
