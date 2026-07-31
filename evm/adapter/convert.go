// Package adapter bridges l1chain's own production state.StateDB (a real
// SHA-256 MPT, see state/mpt.go -- an 8-method interface) to go-ethereum's
// vm.StateDB (34+ methods: snapshots, refunds, logs, self-destruct,
// transient storage, access lists, Prepare/Finalise/SetTxContext), so a
// real embedded go-ethereum EVM can execute directly against l1chain's own
// canonical state -- rather than the fully separate, disconnected
// keccak/MPT sandbox evm.Harness has used since M4.
//
// Built in isolation first (this package has zero dependents yet): every
// piece here is proven standalone before chain/transition.go ever routes a
// real transaction through it.
package adapter

import (
	"l1chain/core"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// toCoreAddress converts go-ethereum's common.Address to l1chain's own
// core.Address -- both plain [20]byte arrays, so this is always a direct
// byte copy, never a hash or any other derivation.
func toCoreAddress(a common.Address) core.Address {
	var out core.Address
	copy(out[:], a[:])
	return out
}

// toCommonHash converts l1chain's core.Hash to go-ethereum's common.Hash --
// both plain [32]byte arrays.
func toCommonHash(h core.Hash) common.Hash {
	var out common.Hash
	copy(out[:], h[:])
	return out
}

// toCoreHash converts go-ethereum's common.Hash to l1chain's own core.Hash.
func toCoreHash(h common.Hash) core.Hash {
	var out core.Hash
	copy(out[:], h[:])
	return out
}

// toUint256 widens an l1chain uint64 balance to go-ethereum's 256-bit
// balance type. Always exact -- every uint64 fits in a uint256.
func toUint256(v uint64) *uint256.Int {
	return new(uint256.Int).SetUint64(v)
}
