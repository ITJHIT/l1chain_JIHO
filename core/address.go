package core

import "encoding/hex"

// AddrLen is the byte length of an Address (20 bytes, Ethereum-style).
const AddrLen = 20

// Address is a 20-byte account identifier.
type Address [AddrLen]byte

// Hex returns the lowercase hex encoding of the address.
func (a Address) Hex() string { return hex.EncodeToString(a[:]) }

// Less reports a stable byte-lexicographic ordering, used for deterministic
// iteration (e.g. state root computation).
func (a Address) Less(b Address) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
