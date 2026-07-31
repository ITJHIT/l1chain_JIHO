package evm

import (
	"bytes"
	"encoding/binary"

	"l1chain/core"
)

// DeployAddress is the reserved account a transaction sends to in order to
// deploy a real EVM contract (evm/adapter), instead of M3's own
// StackVM-based creation convention (To == the zero address). Distinct from
// exchange.Address (0xEC0B...01) by construction -- both are reserved,
// dispatch-routing addresses, but this repo has no central registry of
// them, so each one is simply chosen to be visibly distinctive on sight.
// 0x45 0x56 0x4D spells "EVM" in ASCII.
var DeployAddress = core.Address{
	0x45, 0x56, 0x4D, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
}

// codeMagic tags deployed EVM runtime code so a later CALL can recognize it
// and route to evm/adapter instead of M3's StackVM. 0xFE is EVM's own
// INVALID opcode; M3 (vm.StackVM) only implements a fixed, specific opcode
// subset that never includes 0xFE, so tagged code can never be mistaken for
// M3 bytecode, nor vice versa, by coincidence -- not just by convention.
var codeMagic = []byte{0xFE, 'L', '1', 'E', 'V', 'M', '1', 0xFE}

// TagCode prepends codeMagic to runtime, producing what actually gets
// stored as an EVM contract's on-chain code.
func TagCode(runtime []byte) []byte {
	out := make([]byte, 0, len(codeMagic)+len(runtime))
	out = append(out, codeMagic...)
	out = append(out, runtime...)
	return out
}

// IsTaggedCode reports whether code begins with codeMagic, i.e. was
// deployed through DeployAddress rather than M3's creation path.
func IsTaggedCode(code []byte) bool {
	return len(code) >= len(codeMagic) && bytes.Equal(code[:len(codeMagic)], codeMagic)
}

// UntagCode strips codeMagic, returning the real EVM runtime bytes a CALL
// should execute. Returns code unchanged if it isn't tagged.
func UntagCode(code []byte) []byte {
	if !IsTaggedCode(code) {
		return code
	}
	return code[len(codeMagic):]
}

// evmCreatePreimagePrefix domain-separates DeriveContractAddress from
// vm.CreateAddress's own SumHash(From||Nonce) scheme (vm/stackvm.go) --
// prepending a fixed string that never appears in M3's own preimage makes
// the two address spaces unable to collide by construction, not merely by
// being statistically unlikely to.
const evmCreatePreimagePrefix = "l1chain-evm-create"

// DeriveContractAddress computes the address a real EVM contract deployed
// by from at nonce nonce will live at: the last 20 bytes of
// SumHash(evmCreatePreimagePrefix || from || nonce), mirroring
// vm.CreateAddress's own big-endian-nonce, last-20-bytes convention exactly
// except for the domain-separating prefix.
func DeriveContractAddress(from core.Address, nonce uint64) core.Address {
	buf := make([]byte, 0, len(evmCreatePreimagePrefix)+core.AddrLen+8)
	buf = append(buf, evmCreatePreimagePrefix...)
	buf = append(buf, from[:]...)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], nonce)
	buf = append(buf, n[:]...)
	h := core.SumHash(buf)
	var addr core.Address
	copy(addr[:], h[core.HashLen-core.AddrLen:])
	return addr
}
