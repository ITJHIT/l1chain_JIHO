package chain

import (
	"errors"

	"l1chain/consensus"
	"l1chain/core"
	"l1chain/state"
)

// Chain validation errors.
var (
	ErrUnknownParent = errors.New("chain: unknown parent block")
	ErrDuplicateBlock = errors.New("chain: duplicate block")
	ErrBadHeight      = errors.New("chain: height not parent+1")
	ErrBadPoW         = errors.New("chain: block hash does not meet difficulty")
	ErrBadTxRoot      = errors.New("chain: merkle root mismatch")
	ErrBadStateRoot   = errors.New("chain: state root mismatch")
	// ErrBadChainID is returned when a transaction's ChainID does not match the
	// chain's configured id. This is the replay-protection rule enforced at the
	// consensus boundary: it is applied identically by the mining path
	// (CandidateStateRoot) and the validation path (AddBlock), so a tx signed for
	// one chain can never be included in or accepted onto another.
	ErrBadChainID = errors.New("chain: transaction chain id mismatch")
)

// DefaultChainID is the replay-protection domain used by genesis and every
// standard flow (CLI, node config, browser signer) unless explicitly overridden
// via node.Config.ChainID / Chain.SetChainID.
const DefaultChainID uint64 = 1337

// Chain is an in-memory block store implementing longest-chain (heaviest
// cumulative difficulty) consensus with reorg. It keeps every valid block it has
// seen keyed by hash, the cumulative difficulty of each block, the current head,
// and the canonical (head) state plus a height index over the canonical branch.
//
// The engine re-derives state by replaying each block from genesis: every
// block's transactions followed by the fixed BlockReward credited to that
// block's Header.Coinbase (the genesis block at height 0 is not rewarded). Thus
// canonical State() reflects genesis alloc + all txs + all block rewards, and
// StateRoot validation covers the coinbase credit.
type Chain struct {
	blocks map[core.Hash]core.Block
	td     map[core.Hash]uint64 // cumulative difficulty per block hash

	genesisHash  core.Hash
	genesisAlloc map[core.Address]uint64

	head        core.Hash
	headState   state.StateDB
	heightIndex map[uint64]core.Hash // canonical height -> block hash
	chainID     uint64 // replay-protection domain enforced on every tx
}

// NewChain creates a chain seeded with the genesis block. The optional alloc is
// the genesis allocation (same map given to ApplyGenesis); it is required for
// the engine to reproduce genesis balances when replaying state. Callers that
// used a funded genesis MUST pass the same alloc so State() and StateRoot
// validation are correct.
func NewChain(genesis core.Block, alloc ...map[core.Address]uint64) *Chain {
	c := &Chain{
		blocks:      make(map[core.Hash]core.Block),
		td:          make(map[core.Hash]uint64),
		heightIndex: make(map[uint64]core.Hash),
		chainID:     DefaultChainID,
	}
	if len(alloc) > 0 {
		c.genesisAlloc = alloc[0]
	}

	gh := genesis.Hash()
	c.genesisHash = gh
	c.blocks[gh] = genesis
	c.td[gh] = uint64(genesis.Header.Difficulty)
	c.head = gh
	c.headState = c.fundGenesis()
	c.rebuildIndex()
	return c
}

// fundGenesis returns a fresh StateDB funded with the genesis allocation.
func (c *Chain) fundGenesis() state.StateDB {
	st := state.NewMemStateDB()
	for addr, bal := range c.genesisAlloc {
		acct := st.GetAccount(addr)
		acct.Balance += bal
		st.SetAccount(addr, acct)
	}
	return st
}

// SetChainID overrides the replay-protection domain enforced on every tx. It is
// set by node.New from Config.ChainID so a node and its chain agree on the id.
func (c *Chain) SetChainID(id uint64) { c.chainID = id }

// ChainID returns the chain's configured replay-protection domain.
func (c *Chain) ChainID() uint64 { return c.chainID }

// assertChainID is the single authoritative replay-protection rule. Both the
// mining path (CandidateStateRoot) and the validation path (AddBlock) call it on
// the block/candidate transactions, so mining and validation accept exactly the
// same set of txs. It rejects any tx whose ChainID differs from c.chainID; the
// signature already commits to ChainID (see core.Transaction.preimage), so a tx
// cannot both verify and claim a different id than its signer intended.
func (c *Chain) assertChainID(txs []core.Transaction) error {
	for i := range txs {
		if txs[i].ChainID != c.chainID {
			return ErrBadChainID
		}
	}
	return nil
}

// chainTo returns the blocks from genesis to hash (inclusive, genesis first) by
// following PrevHash links.
func (c *Chain) chainTo(hash core.Hash) ([]core.Block, bool) {
	var rev []core.Block
	h := hash
	for {
		b, ok := c.blocks[h]
		if !ok {
			return nil, false
		}
		rev = append(rev, b)
		if h == c.genesisHash {
			break
		}
		h = b.Header.PrevHash
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, true
}

// applyBlockRewarded applies every transaction in b to st in order, then credits
// BlockReward to b.Header.Coinbase (the miner). The genesis block (height 0) is
// never rewarded. It mutates st in place and returns the first tx error.
func applyBlockRewarded(st state.StateDB, b core.Block, verifySig func(core.Transaction) bool) error {
	for j := range b.Txs {
		if err := ApplyTx(st, b.Txs[j], verifySig); err != nil {
			return err
		}
	}
	if b.Header.Height != 0 {
		acct := st.GetAccount(b.Header.Coinbase)
		acct.Balance += BlockReward
		st.SetAccount(b.Header.Coinbase, acct)
	}
	return nil
}

// deriveState replays transactions and block rewards from genesis up to (and
// including) hash and returns the resulting state. Canonical state therefore
// reflects genesis alloc + all txs + all block (coinbase) rewards.
func (c *Chain) deriveState(hash core.Hash, verifySig func(core.Transaction) bool) (state.StateDB, error) {
	path, ok := c.chainTo(hash)
	if !ok {
		return nil, ErrUnknownParent
	}
	st := c.fundGenesis()
	for i := 1; i < len(path); i++ { // skip genesis (no txs, no reward)
		if err := applyBlockRewarded(st, path[i], verifySig); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// CandidateStateRoot derives the state root that results from applying txs on
// top of the current canonical head plus a BlockReward credit to coinbase,
// WITHOUT mutating canonical state. It replays from genesis into a fresh state
// (exactly like AddBlock's re-derivation) so the returned root is precisely
// what AddBlock will re-derive and validate for a block carrying these txs.
// This is the mining/validation determinism seam: miners call this to fill
// Header.StateRoot; validators re-run the identical path in AddBlock.
func (c *Chain) CandidateStateRoot(txs []core.Transaction, coinbase core.Address, verifySig func(core.Transaction) bool) (core.Hash, error) {
	st, err := c.deriveState(c.head, verifySig)
	if err != nil {
		return core.Hash{}, err
	}
	if err := c.assertChainID(txs); err != nil {
		return core.Hash{}, err
	}
	for i := range txs {
		if err := ApplyTx(st, txs[i], verifySig); err != nil {
			return core.Hash{}, err
		}
	}
	m := st.GetAccount(coinbase)
	m.Balance += BlockReward
	st.SetAccount(coinbase, m)
	return st.StateRoot(), nil
}

// AddBlock validates and stores b, performing a reorg if b's branch becomes the
// heaviest. Validation covers parent linkage, height, PoW, tx merkle root, and
// re-derived state root.
func (c *Chain) AddBlock(b core.Block, verifySig func(core.Transaction) bool) error {
	hash := b.Hash()
	if _, exists := c.blocks[hash]; exists {
		return ErrDuplicateBlock
	}
	parent, ok := c.blocks[b.Header.PrevHash]
	if !ok {
		return ErrUnknownParent
	}
	if b.Header.Height != parent.Header.Height+1 {
		return ErrBadHeight
	}
	if !consensus.MeetsTarget(hash, b.Header.Difficulty) {
		return ErrBadPoW
	}
	if b.TxRoot() != b.Header.MerkleRoot {
		return ErrBadTxRoot
	}
	if err := c.assertChainID(b.Txs); err != nil {
		return err
	}

	// Re-derive state: parent state + this block's transactions + coinbase reward.
	st, err := c.deriveState(b.Header.PrevHash, verifySig)
	if err != nil {
		return err
	}
	if err := applyBlockRewarded(st, b, verifySig); err != nil {
		return err
	}
	if st.StateRoot() != b.Header.StateRoot {
		return ErrBadStateRoot
	}

	// Commit the block and its cumulative difficulty.
	c.blocks[hash] = b
	c.td[hash] = c.td[b.Header.PrevHash] + uint64(b.Header.Difficulty)

	// Reorg: adopt the strictly heaviest branch as the canonical head.
	if c.td[hash] > c.td[c.head] {
		c.head = hash
		c.headState = st
		c.rebuildIndex()
	}
	return nil
}

// rebuildIndex recomputes the canonical height -> hash index for the current head.
func (c *Chain) rebuildIndex() {
	c.heightIndex = make(map[uint64]core.Hash)
	path, ok := c.chainTo(c.head)
	if !ok {
		return
	}
	for _, b := range path {
		c.heightIndex[b.Header.Height] = b.Hash()
	}
}

// Head returns the current canonical head block.
func (c *Chain) Head() core.Block { return c.blocks[c.head] }

// GetByHash returns any known block by its hash.
func (c *Chain) GetByHash(h core.Hash) (core.Block, bool) {
	b, ok := c.blocks[h]
	return b, ok
}

// GetByHeight returns the canonical block at the given height.
func (c *Chain) GetByHeight(height uint64) (core.Block, bool) {
	h, ok := c.heightIndex[height]
	if !ok {
		return core.Block{}, false
	}
	b, ok := c.blocks[h]
	return b, ok
}

// State returns the canonical head state.
func (c *Chain) State() state.StateDB { return c.headState }

// TotalDifficulty returns the cumulative difficulty of the block with the given
// hash (0 if unknown).
func (c *Chain) TotalDifficulty(h core.Hash) uint64 { return c.td[h] }
