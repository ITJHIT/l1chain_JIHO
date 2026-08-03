package core

import "encoding/binary"

// Header is the block header. StateRoot is carried from M1 so the field layout
// is stable when the KV state model is replaced by an MPT model in M4.
type Header struct {
	Height     uint64
	PrevHash   Hash
	MerkleRoot Hash
	StateRoot  Hash
	Coinbase   Address // miner (PoW) or proposer (PoS) credited with the block reward in canonical state
	Timestamp  int64
	Difficulty uint32 // required number of leading zero bits in the block hash; always 0 for a PoS block
	Nonce      uint64 // PoW solution; always 0 for a PoS block

	// BaseFee is this block's protocol-computed fee-market base fee (M9, see
	// chain.ComputeBaseFee) -- deterministically derived from the parent
	// header's own BaseFee/GasUsed, verified independently by every
	// validator, never chosen freely by the miner/proposer. GasUsed is the
	// total gas actually consumed by this block's fee-priced (contract/EVM)
	// transactions; plain-transfer/exchange/attestation transactions are not
	// fee-priced and do not contribute to it (see chain/transition.go's own
	// scope-boundary doc comment). Both apply identically under PoW and PoS
	// -- the fee market is a state-transition concern, not a consensus-mode
	// concern.
	BaseFee uint64
	GasUsed uint64

	// ProposerSig is a PoS validator's BLS signature (see pos.Key.Sign) by
	// Coinbase over SigningHash() -- empty for every PoW block, forever (M8
	// adds PoS as a genesis-selected mode ADDITIVE to PoW, never replacing
	// it; see Genesis.ConsensusMode). Because preimage(withSig=true) simply
	// appends this field, an empty ProposerSig makes preimage(true) ==
	// preimage(false) byte-for-byte, so Hash() is unchanged in VALUE from
	// its pre-M8 definition for every PoW block that ever existed -- adding
	// this field changes the hash FORMULA, not any existing PoW block's
	// actual hash.
	ProposerSig []byte
}

// Block is a header plus its ordered transactions.
type Block struct {
	Header Header
	Txs    []Transaction
}

// TxRoot computes the merkle root of the block's transactions.
func (b *Block) TxRoot() Hash {
	leaves := make([]Hash, len(b.Txs))
	for i := range b.Txs {
		leaves[i] = b.Txs[i].Hash()
	}
	return MerkleRoot(leaves)
}

// preimage is the deterministic encoding of the header used for hashing, up
// to (but not including, unless withSig) ProposerSig -- mirroring
// core.Transaction.preimage(withSig bool)'s exact shape and rationale: a PoS
// proposer signs SigningHash() = preimage(false), so the signature itself is
// never part of what it signs over.
func (h *Header) preimage(withSig bool) []byte {
	buf := make([]byte, 0, 8+HashLen*3+AddrLen+8+4+8+8+8+len(h.ProposerSig))
	var n8 [8]byte
	binary.BigEndian.PutUint64(n8[:], h.Height)
	buf = append(buf, n8[:]...)
	buf = append(buf, h.PrevHash[:]...)
	buf = append(buf, h.MerkleRoot[:]...)
	buf = append(buf, h.StateRoot[:]...)
	buf = append(buf, h.Coinbase[:]...)
	binary.BigEndian.PutUint64(n8[:], uint64(h.Timestamp))
	buf = append(buf, n8[:]...)
	var n4 [4]byte
	binary.BigEndian.PutUint32(n4[:], h.Difficulty)
	buf = append(buf, n4[:]...)
	binary.BigEndian.PutUint64(n8[:], h.Nonce)
	buf = append(buf, n8[:]...)
	binary.BigEndian.PutUint64(n8[:], h.BaseFee)
	buf = append(buf, n8[:]...)
	binary.BigEndian.PutUint64(n8[:], h.GasUsed)
	buf = append(buf, n8[:]...)
	if withSig {
		buf = append(buf, h.ProposerSig...)
	}
	return buf
}

// SigningHash is the digest a PoS proposer signs (excludes ProposerSig),
// mirroring core.Transaction.SigningHash's exact role for tx signatures.
// Unused by PoW blocks (consensus.Mine never calls this -- it mines directly
// against Hash(), exactly as before M8; see consensus/pow.go).
func (h *Header) SigningHash() Hash { return SumHash(h.preimage(false)) }

// Hash returns the block hash (hash of the header, including the PoW nonce
// and, for a PoS block, ProposerSig). Empty for every PoW block, so
// preimage(true) == preimage(false) byte-for-byte there -- see ProposerSig's
// own doc comment for why this leaves every PoW block's hash VALUE unchanged.
func (h *Header) Hash() Hash { return SumHash(h.preimage(true)) }

// Hash returns the block's hash via its header.
func (b *Block) Hash() Hash { return b.Header.Hash() }
