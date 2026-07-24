package store

import (
	"encoding/binary"
	"errors"

	bolt "go.etcd.io/bbolt"

	"l1chain/core"
)

// Bucket names and fixed meta keys.
var (
	bucketBlocks  = []byte("blocks")  // block hash -> encoded block
	bucketHeights = []byte("heights") // canonical height (big-endian u64) -> block hash
	bucketMeta    = []byte("meta")    // misc metadata

	metaHead  = []byte("head")          // -> head block hash
	metaAlloc = []byte("genesis_alloc") // -> encoded genesis allocation map
)

// Store is a BoltDB-backed durable block/chain store.
type Store struct {
	db *bolt.DB
}

// Open opens (creating if necessary) the BoltDB file at path and ensures the
// required buckets exist.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketBlocks, bucketHeights, bucketMeta} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database file.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// PutBlock stores b under its hash in the blocks bucket.
func (s *Store) PutBlock(b core.Block) error {
	enc, err := EncodeBlock(b)
	if err != nil {
		return err
	}
	h := b.Hash()
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketBlocks).Put(h[:], enc)
	})
}

// GetBlock returns the block stored under h. found is false (with a nil error)
// when no block exists for that hash.
func (s *Store) GetBlock(h core.Hash) (core.Block, bool, error) {
	var (
		b     core.Block
		found bool
	)
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketBlocks).Get(h[:])
		if v == nil {
			return nil
		}
		dec, err := DecodeBlock(v)
		if err != nil {
			return err
		}
		b = dec
		found = true
		return nil
	})
	if err != nil {
		return core.Block{}, false, err
	}
	return b, found, nil
}

// heightKey encodes a height as an 8-byte big-endian key (sortable).
func heightKey(height uint64) []byte {
	var k [8]byte
	binary.BigEndian.PutUint64(k[:], height)
	return k[:]
}

// SetCanonical records that h is the canonical block at the given height.
func (s *Store) SetCanonical(height uint64, h core.Hash) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketHeights).Put(heightKey(height), h[:])
	})
}

// GetCanonical returns the canonical block hash at height. found is false (nil
// error) when no canonical hash is recorded for that height.
func (s *Store) GetCanonical(height uint64) (core.Hash, bool, error) {
	var (
		h     core.Hash
		found bool
	)
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketHeights).Get(heightKey(height))
		if v == nil {
			return nil
		}
		if len(v) != core.HashLen {
			return errors.New("store: corrupt canonical hash length")
		}
		copy(h[:], v)
		found = true
		return nil
	})
	if err != nil {
		return core.Hash{}, false, err
	}
	return h, found, nil
}

// SetHead records the current head block hash.
func (s *Store) SetHead(h core.Hash) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketMeta).Put(metaHead, h[:])
	})
}

// GetHead returns the stored head block hash. found is false (nil error) when
// no head has been recorded yet.
func (s *Store) GetHead() (core.Hash, bool, error) {
	var (
		h     core.Hash
		found bool
	)
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketMeta).Get(metaHead)
		if v == nil {
			return nil
		}
		if len(v) != core.HashLen {
			return errors.New("store: corrupt head hash length")
		}
		copy(h[:], v)
		found = true
		return nil
	})
	if err != nil {
		return core.Hash{}, false, err
	}
	return h, found, nil
}

// PutGenesisAlloc persists the genesis allocation map. The chain engine keeps
// this allocation private and re-derives balances from it, so it cannot be
// recovered from stored blocks alone (a funded genesis carries no transactions).
// The caller that constructed the chain embeds the same alloc here so Load can
// faithfully reproduce State() balances after a restart. A nil/empty alloc is
// valid and records an empty allocation.
func (s *Store) PutGenesisAlloc(alloc map[core.Address]uint64) error {
	enc, err := encodeAlloc(alloc)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketMeta).Put(metaAlloc, enc)
	})
}

// GetGenesisAlloc returns the persisted genesis allocation. found is false (nil
// error) when no allocation was recorded; callers should then treat the genesis
// as unfunded (empty alloc).
func (s *Store) GetGenesisAlloc() (map[core.Address]uint64, bool, error) {
	var (
		alloc map[core.Address]uint64
		found bool
	)
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketMeta).Get(metaAlloc)
		if v == nil {
			return nil
		}
		dec, err := decodeAlloc(v)
		if err != nil {
			return err
		}
		alloc = dec
		found = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return alloc, found, nil
}
