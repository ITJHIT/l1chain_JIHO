// Package pos implements the M8 Proof-of-Stake consensus mode: validator
// identity, stake-weighted proposer selection, and BLS-aggregated checkpoint
// attestations, layered ADDITIVELY alongside the existing PoW consensus mode
// (see consensus/pow.go) -- selected once per chain via Genesis.ConsensusMode,
// never mixed within one chain's lifetime.
//
// This file (bls.go) is the lowest layer: a thin wrapper over blst's min-pk
// convention (public keys in G1, 48-byte compressed; signatures in G2,
// 96-byte compressed) -- the same convention Ethereum's own beacon chain
// uses, chosen specifically for FastAggregateVerify: many validators'
// signatures over the SAME message collapse into one aggregate signature and
// one pairing check. That aggregation property is the entire reason this
// milestone signs with BLS instead of reusing the ECDSA wallet.Key already
// used for transaction signing.
package pos

import (
	"crypto/rand"
	"errors"
	"fmt"

	blst "github.com/supranational/blst/bindings/go"
)

// pubKeySize/sigSize are blst's min-pk compressed encoding lengths: a G1
// point (public key) compresses to 48 bytes, a G2 point (signature) to 96.
const (
	pubKeySize = 48
	sigSize    = 96
)

// SignatureSize re-exports sigSize for callers that need a cheap,
// STRUCTURAL-ONLY shape check without access to a validator set to do real
// verification -- e.g. p2p's gossip-layer topic validator (see
// p2p/gossip.go's blockTopicValidator), which has no chain/validator-set
// reference and can therefore only check "is ProposerSig even the right
// length for a real BLS signature," never "is it valid."
const SignatureSize = sigSize

// Key wraps a BLS12-381 secret key used to sign validator attestations and
// proposed block headers.
type Key struct {
	sk *blst.SecretKey
}

// NewKey generates a new cryptographically secure BLS12-381 key, mirroring
// wallet.NewKey's shape. blst.KeyGen requires at least 32 bytes of IKM
// (input keying material); this draws them from crypto/rand.
func NewKey() (Key, error) {
	var ikm [32]byte
	if _, err := rand.Read(ikm[:]); err != nil {
		return Key{}, err
	}
	sk := blst.KeyGen(ikm[:])
	if sk == nil {
		return Key{}, errors.New("pos: blst.KeyGen failed")
	}
	return Key{sk: sk}, nil
}

// KeyFromBytes reconstructs a Key from its 32-byte big-endian scalar
// serialization (as produced by Bytes), mirroring wallet.KeyFromBytes.
func KeyFromBytes(b []byte) (Key, error) {
	sk := new(blst.SecretKey).Deserialize(b)
	if sk == nil {
		return Key{}, errors.New("pos: invalid BLS secret key bytes")
	}
	return Key{sk: sk}, nil
}

// Bytes returns the 32-byte big-endian serialization of the secret key.
func (k Key) Bytes() []byte { return k.sk.Serialize() }

// PubKey returns the 48-byte compressed G1 public key.
func (k Key) PubKey() []byte {
	return new(blst.P1Affine).From(k.sk).Compress()
}

// Sign returns a 96-byte compressed G2 signature over msg, domain-separated
// by dst (see DST).
func (k Key) Sign(msg, dst []byte) []byte {
	return new(blst.P2Affine).Sign(k.sk, msg, dst).Compress()
}

// DST builds this project's BLS domain-separation tag: the standard min-pk
// ciphersuite string blst's own tests use ("BLS_SIG_BLS12381G2_XMD:SHA-256_
// SSWU_RO_...") with an appended chain-ID suffix -- the IETF BLS-signature
// draft's own sanctioned way to customize a DST per application -- so a
// signature produced for chain A's validator set cannot be replayed as valid
// on chain B. This plays the same replay-domain-separation role
// core.Transaction.ChainID already plays for ECDSA tx signatures (see
// core/transaction.go's own preimage doc comment).
func DST(chainID uint64) []byte {
	return []byte(fmt.Sprintf("BLS_SIG_BLS12381G2_XMD:SHA-256_SSWU_RO_L1CHAIN_POS_CHAIN%d_", chainID))
}

// Verify reports whether sig is a valid signature by pubKey over msg under
// dst. pubKey/sig must be the compressed byte encodings PubKey/Sign produce.
// Both the public key's and the signature's own group membership are
// validated internally (sigGroupcheck=true, pkValidate=true) -- pubKey/sig
// here always originate from network input (an attestation tx or a block
// header), never from this process's own key material, so neither can be
// trusted un-checked.
func Verify(pubKey, msg, sig, dst []byte) bool {
	if len(pubKey) != pubKeySize || len(sig) != sigSize {
		return false
	}
	pk := new(blst.P1Affine).Uncompress(pubKey)
	if pk == nil {
		return false
	}
	s := new(blst.P2Affine).Uncompress(sig)
	if s == nil {
		return false
	}
	return s.Verify(true, pk, true, msg, dst)
}

// ValidatePubKey reports whether pubKey decodes to a valid, correctly
// subgroup-checked G1 point. Intended to be called ONCE per validator, at
// registration time (see ValidatorSet's own constructor) -- VerifyAggregate/
// FastAggregateVerify deliberately skip per-call pubkey validation for
// performance, which is standard BLS practice: validate a validator's key
// when it joins the set, not on every attestation it later signs.
func ValidatePubKey(pubKey []byte) bool {
	if len(pubKey) != pubKeySize {
		return false
	}
	pk := new(blst.P1Affine).Uncompress(pubKey)
	return pk != nil && pk.KeyValidate()
}

// Aggregate combines multiple compressed signatures (over the SAME message,
// by convention -- see VerifyAggregate) into one compressed aggregate
// signature. Returns an error if sigs is empty or any entry fails to decode
// or fails its own subgroup check.
func Aggregate(sigs [][]byte) ([]byte, error) {
	if len(sigs) == 0 {
		return nil, errors.New("pos: Aggregate called with no signatures")
	}
	agg := new(blst.P2Aggregate)
	if !agg.AggregateCompressed(sigs, true) {
		return nil, errors.New("pos: failed to aggregate signatures (invalid point or not in G2)")
	}
	return agg.ToAffine().Compress(), nil
}

// VerifyAggregate reports whether aggSig (as produced by Aggregate) is a
// valid aggregate of signatures by EVERY key in pubKeys, all over the SAME
// msg under dst -- this is FastAggregateVerify's precondition, and the
// reason checkpoint attestations (attestation.go) are always "N validators
// attest to the SAME target hash," never mixed messages.
func VerifyAggregate(pubKeys [][]byte, msg, aggSig, dst []byte) bool {
	if len(pubKeys) == 0 || len(aggSig) != sigSize {
		return false
	}
	pks := make([]*blst.P1Affine, len(pubKeys))
	for i, b := range pubKeys {
		if len(b) != pubKeySize {
			return false
		}
		pk := new(blst.P1Affine).Uncompress(b)
		if pk == nil {
			return false
		}
		pks[i] = pk
	}
	sig := new(blst.P2Affine).Uncompress(aggSig)
	if sig == nil {
		return false
	}
	return sig.FastAggregateVerify(true, pks, msg, dst)
}
