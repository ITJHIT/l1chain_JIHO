package core

import "encoding/binary"

// Transaction is a value transfer (M1) or a contract call/creation (M3+).
// Signature is filled by the ECDSA(secp256k1) signing slice; SigningHash
// covers every field except the signature so the signature is verifiable.
type Transaction struct {
	From      Address
	To        Address // zero Address reserved for contract creation in M3+
	Value     uint64
	Nonce     uint64
	GasLimit  uint64
	ChainID   uint64 // replay-domain separator; the signature covers it (see preimage)
	GasFeeCap uint64 // M9: hard ceiling, sender never pays more than this per gas unit
	GasTipCap uint64 // M9: priority fee offered to the block producer, per gas unit
	Data      []byte
	Signature []byte
}

// preimage returns the deterministic byte encoding of the tx up to (but not
// including) the given signature bytes.
//
// Byte layout (all integers big-endian, fixed width):
//
//	From(20) || To(20) || Value(u64) || Nonce(u64) || GasLimit(u64) ||
//	ChainID(u64) || GasFeeCap(u64) || GasTipCap(u64) || Data [ || Signature ]
//
// ChainID sits at a FIXED position right after GasLimit and before Data, so the
// signature (taken over SigningHash = preimage(withSig=false)) commits to the
// chain id: a signature produced for chain A cannot be replayed on chain B
// because B's transactions hash a different ChainID and thus a different digest.
// GasFeeCap/GasTipCap (M9) sit right after ChainID, signed over the same way --
// a sender commits to its own fee ceiling and priority offer; neither can be
// tampered with post-signature, exactly like GasLimit already couldn't be.
func (t *Transaction) preimage(withSig bool) []byte {
	buf := make([]byte, 0, 20+20+8+8+8+8+8+8+len(t.Data)+len(t.Signature))
	buf = append(buf, t.From[:]...)
	buf = append(buf, t.To[:]...)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], t.Value)
	buf = append(buf, n[:]...)
	binary.BigEndian.PutUint64(n[:], t.Nonce)
	buf = append(buf, n[:]...)
	binary.BigEndian.PutUint64(n[:], t.GasLimit)
	buf = append(buf, n[:]...)
	binary.BigEndian.PutUint64(n[:], t.ChainID)
	buf = append(buf, n[:]...)
	binary.BigEndian.PutUint64(n[:], t.GasFeeCap)
	buf = append(buf, n[:]...)
	binary.BigEndian.PutUint64(n[:], t.GasTipCap)
	buf = append(buf, n[:]...)
	buf = append(buf, t.Data...)
	if withSig {
		buf = append(buf, t.Signature...)
	}
	return buf
}
// SigningHash is the digest a signer signs (excludes Signature).
func (t *Transaction) SigningHash() Hash { return SumHash(t.preimage(false)) }

// Hash is the full transaction identity (includes Signature).
func (t *Transaction) Hash() Hash { return SumHash(t.preimage(true)) }
