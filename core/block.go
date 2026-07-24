package core

import "encoding/binary"

// Header is the block header. StateRoot is carried from M1 so the field layout
// is stable when the KV state model is replaced by an MPT model in M4.
type Header struct {
	Height     uint64
	PrevHash   Hash
	MerkleRoot Hash
	StateRoot  Hash
	Coinbase   Address // miner credited with the block reward in canonical state
	Timestamp  int64
	Difficulty uint32 // required number of leading zero bits in the block hash
	Nonce      uint64 // PoW solution
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

// headerPreimage is the deterministic encoding of the header used for hashing.
func (h *Header) preimage() []byte {
	buf := make([]byte, 0, 8+HashLen*3+AddrLen+8+4+8)
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
	return buf
}

// Hash returns the block hash (hash of the header, including the PoW nonce).
func (h *Header) Hash() Hash { return SumHash(h.preimage()) }

// Hash returns the block's hash via its header.
func (b *Block) Hash() Hash { return b.Header.Hash() }
