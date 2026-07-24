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
	Data      []byte
	Signature []byte
}

// preimage returns the deterministic byte encoding of the tx up to (but not
// including) the given signature bytes.
func (t *Transaction) preimage(withSig bool) []byte {
	buf := make([]byte, 0, 20+20+8+8+8+len(t.Data)+len(t.Signature))
	buf = append(buf, t.From[:]...)
	buf = append(buf, t.To[:]...)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], t.Value)
	buf = append(buf, n[:]...)
	binary.BigEndian.PutUint64(n[:], t.Nonce)
	buf = append(buf, n[:]...)
	binary.BigEndian.PutUint64(n[:], t.GasLimit)
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
