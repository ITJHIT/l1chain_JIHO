package evm

import (
	"bytes"
	"testing"

	"l1chain/core"
	"l1chain/exchange"
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

func TestDeployAddressDistinctFromExchangeAddress(t *testing.T) {
	if DeployAddress == exchange.Address {
		t.Fatal("evm.DeployAddress == exchange.Address, want two distinct reserved addresses")
	}
	if DeployAddress == (core.Address{}) {
		t.Fatal("evm.DeployAddress is the zero address, which M3 already reserves for its own creation convention")
	}
}
