package wallet

import (
	"l1chain/core"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// Verify is the real secp256k1 signature verifier for M1. It recovers the
// signer's public key from tx.Signature over tx.SigningHash(), derives the
// address with the same convention as Key.Address, and returns true IFF the
// recovered address equals tx.From and the signature parses cleanly.
//
// Empty or malformed signatures return false. Pass Verify as the verifySig
// argument to chain.ApplyTx / chain.ApplyBlock / Chain.AddBlock to enforce
// genuine ownership of the sending account.
func Verify(tx core.Transaction) bool {
	// SignCompact produces a 65-byte signature (1 recovery byte + r + s).
	// Anything shorter cannot be a valid recoverable signature.
	if len(tx.Signature) != 65 {
		return false
	}
	digest := tx.SigningHash()
	pub, _, err := ecdsa.RecoverCompact(tx.Signature, digest[:])
	if err != nil {
		return false
	}
	return pubKeyAddress(pub) == tx.From
}
