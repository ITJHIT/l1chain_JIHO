// Package core holds chain-agnostic primitives: hashes, blocks, transactions,
// and the merkle tree. It has no dependency on state, consensus, p2p, or vm so
// those layers can plug in behind interfaces (see the state and vm packages).
package core

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashLen is the byte length of a Hash (32 bytes, SHA-256).
//
// NOTE (M4): EVM equivalence requires keccak256 for contract/state hashing.
// M1 uses SHA-256 for block/merkle identity; the hashing function is isolated
// here so M4 can introduce keccak256 for the EVM/MPT paths without touching
// block plumbing.
const HashLen = 32

// Hash is a fixed-size 32-byte digest.
type Hash [HashLen]byte

// Hex returns the lowercase hex encoding of the hash.
func (h Hash) Hex() string { return hex.EncodeToString(h[:]) }

// IsZero reports whether the hash is the zero value.
func (h Hash) IsZero() bool {
	for _, b := range h {
		if b != 0 {
			return false
		}
	}
	return true
}

// SumHash returns the SHA-256 digest of data as a Hash.
func SumHash(data []byte) Hash {
	return Hash(sha256.Sum256(data))
}

// HashConcat hashes the concatenation of the provided byte slices in order.
func HashConcat(parts ...[]byte) Hash {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	var out Hash
	copy(out[:], h.Sum(nil))
	return out
}
