// Package store provides durable, BoltDB-backed persistence for the chain: an
// encoding for blocks and a Store keyed by block hash / canonical height, plus
// bridge helpers that Save a chain.Chain and Load it back after a restart.
//
// It depends only on core, chain, state, consensus, wallet and go.etcd.io/bbolt.
// No existing package's public API is modified.
package store

import (
	"bytes"
	"encoding/gob"

	"l1chain/core"
)

// EncodeBlock serializes b into a deterministic byte slice. It uses encoding/gob
// over the block's exported fields (Header + Txs, including each tx's Data and
// Signature byte slices), so DecodeBlock(EncodeBlock(b)) reproduces b exactly —
// crucially preserving core.Block.Hash() and every transaction's Hash().
func EncodeBlock(b core.Block) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&b); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DecodeBlock is the inverse of EncodeBlock.
func DecodeBlock(data []byte) (core.Block, error) {
	var b core.Block
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&b); err != nil {
		return core.Block{}, err
	}
	return b, nil
}

// encodeAlloc serializes a genesis allocation map for the meta bucket.
func encodeAlloc(alloc map[core.Address]uint64) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(alloc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeAlloc is the inverse of encodeAlloc.
func decodeAlloc(data []byte) (map[core.Address]uint64, error) {
	alloc := make(map[core.Address]uint64)
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&alloc); err != nil {
		return nil, err
	}
	return alloc, nil
}
