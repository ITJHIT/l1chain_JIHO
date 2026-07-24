// Package wallet implements secp256k1 ECDSA key management, address derivation,
// and recoverable-signature signing/verification for M1. It builds on the core
// primitives and provides the real signature verifier (see Verify) that is
// injected into the chain engine's pluggable verifySig hook.
package wallet

import (
	"l1chain/core"

	secp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// Key wraps a secp256k1 private key used to sign transactions.
type Key struct {
	priv *secp.PrivateKey
}

// NewKey generates a new cryptographically secure secp256k1 key.
func NewKey() (Key, error) {
	p, err := secp.GeneratePrivateKey()
	if err != nil {
		return Key{}, err
	}
	return Key{priv: p}, nil
}

// KeyFromBytes reconstructs a Key from its 32-byte big-endian serialization
// (as produced by Bytes). It is the inverse of Bytes for persistence.
func KeyFromBytes(b []byte) Key {
	return Key{priv: secp.PrivKeyFromBytes(b)}
}

// Bytes returns the 32-byte big-endian serialization of the private key.
func (k Key) Bytes() []byte {
	return k.priv.Serialize()
}

// Address derives the account address from the public key.
//
// Convention (M1): hash the 64-byte uncompressed public key (X||Y, i.e. the
// SEC uncompressed encoding with its leading 0x04 tag byte stripped) with
// core.SumHash (SHA-256) and take the LAST 20 bytes of the digest. This is a
// project-specific convention for M1 and deliberately NOT Ethereum's
// keccak256(pubkey)[12:]; keccak-based address derivation arrives in M4.
func (k Key) Address() core.Address {
	return pubKeyAddress(k.priv.PubKey())
}

// Sign signs tx in place: it sets tx.From to the key's address and tx.Signature
// to a recoverable compact ECDSA signature over tx.SigningHash(). The compact
// signature embeds the recovery id so Verify can recover the signer's pubkey.
func (k Key) Sign(tx *core.Transaction) {
	tx.From = k.Address()
	digest := tx.SigningHash()
	// isCompressedKey=true so recovery reconstructs from the compressed pubkey;
	// address derivation below uses the full uncompressed X||Y regardless.
	tx.Signature = ecdsa.SignCompact(k.priv, digest[:], true)
}

// pubKeyAddress applies the M1 address-derivation convention to a public key:
// SumHash(X||Y) and take the last 20 bytes.
func pubKeyAddress(pub *secp.PublicKey) core.Address {
	uncompressed := pub.SerializeUncompressed() // 65 bytes: 0x04 || X(32) || Y(32)
	digest := core.SumHash(uncompressed[1:])    // hash the 64-byte X||Y
	var a core.Address
	copy(a[:], digest[core.HashLen-core.AddrLen:]) // last 20 bytes
	return a
}
