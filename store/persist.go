package store

import (
	"fmt"

	"l1chain/chain"
	"l1chain/wallet"
)

// Save persists the canonical branch of c: it writes the head hash, then walks
// canonical heights 0..Head().Height, storing each canonical block (keyed by its
// hash) and the height->hash canonical index entry.
//
// Save does NOT persist the genesis allocation, which the chain keeps private.
// Callers that constructed the chain with a funded genesis MUST persist the same
// alloc via Store.PutGenesisAlloc so Load can reproduce State() balances.
func Save(s *Store, c *chain.Chain) error {
	head := c.Head()
	headHeight := head.Header.Height

	for h := uint64(0); h <= headHeight; h++ {
		b, ok := c.GetByHeight(h)
		if !ok {
			return fmt.Errorf("store: missing canonical block at height %d", h)
		}
		if err := s.PutBlock(b); err != nil {
			return err
		}
		if err := s.SetCanonical(h, b.Hash()); err != nil {
			return err
		}
	}
	return s.SetHead(head.Hash())
}

// Load reconstructs a chain.Chain from the store. It reloads the genesis block
// (canonical height 0) via chain.NewChain — supplying the persisted genesis
// allocation when present — then re-applies canonical blocks 1..N in height
// order via Chain.AddBlock using wallet.Verify as the signature verifier, so the
// reconstructed chain (head, height index, and derived state) matches the
// original. It returns (nil, false, nil) when the store has no head recorded.
func Load(s *Store) (*chain.Chain, bool, error) {
	headHash, ok, err := s.GetHead()
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}

	genHash, ok, err := s.GetCanonical(0)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, fmt.Errorf("store: head recorded but no genesis at height 0")
	}
	genesis, ok, err := s.GetBlock(genHash)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, fmt.Errorf("store: genesis block %s missing from blocks bucket", genHash.Hex())
	}

	alloc, haveAlloc, err := s.GetGenesisAlloc()
	if err != nil {
		return nil, false, err
	}

	var c *chain.Chain
	if haveAlloc {
		c = chain.NewChain(genesis, alloc)
	} else {
		c = chain.NewChain(genesis)
	}

	headBlock, ok, err := s.GetBlock(headHash)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, fmt.Errorf("store: head block %s missing from blocks bucket", headHash.Hex())
	}
	headHeight := headBlock.Header.Height

	for h := uint64(1); h <= headHeight; h++ {
		canonHash, ok, err := s.GetCanonical(h)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, fmt.Errorf("store: missing canonical hash at height %d", h)
		}
		b, ok, err := s.GetBlock(canonHash)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, fmt.Errorf("store: canonical block %s at height %d missing", canonHash.Hex(), h)
		}
		if err := c.AddBlock(b, wallet.Verify); err != nil {
			return nil, false, fmt.Errorf("store: replaying block at height %d: %w", h, err)
		}
	}

	finalHead := c.Head()
	if finalHead.Hash() != headHash {
		return nil, false, fmt.Errorf("store: reconstructed head %s != stored head %s", finalHead.Hash().Hex(), headHash.Hex())
	}
	return c, true, nil
}
