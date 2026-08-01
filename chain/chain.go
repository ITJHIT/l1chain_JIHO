package chain

import (
	"errors"

	"l1chain/consensus"
	"l1chain/core"
	"l1chain/exchange"
	"l1chain/pos"
	"l1chain/state"
)

// Chain validation errors.
var (
	ErrUnknownParent  = errors.New("chain: unknown parent block")
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
	// ErrPoSRequiresValidators is returned by SetConsensusMode when switching
	// to PoS without a non-empty validator set -- a PoS chain with no
	// validators could never select a proposer, so this is rejected
	// explicitly at configuration time rather than surfacing later as
	// pos.ErrNoActiveValidators on the first mined block.
	ErrPoSRequiresValidators = errors.New("chain: PoS consensus mode requires a non-empty validator set")
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

	genesisHash      core.Hash
	genesisAlloc     map[core.Address]uint64
	genesisBaseAlloc map[core.Address]uint64

	head        core.Hash
	headState   state.StateDB
	heightIndex map[uint64]core.Hash // canonical height -> block hash
	chainID     uint64               // replay-protection domain enforced on every tx

	// exchangeMode governs every block's exchange transactions, mining and
	// validation alike -- the zero value is exchange.Continuous, so a chain
	// that never calls SetExchangeMode behaves exactly as it did before this
	// field existed. Like chainID, it is meant to be set once, immediately
	// after NewChain, and never touched again: deriveState replays the WHOLE
	// chain from genesis using whatever exchangeMode is current at replay time,
	// so changing it after blocks already exist would make historical blocks
	// re-derive under a mode different from the one they were actually mined
	// and validated under.
	exchangeMode exchange.Mode

	// consensusMode selects PoW (the zero value, unchanged since M1) or PoS
	// (M8, additive) for this chain's entire lifetime. Like chainID/
	// exchangeMode, it is meant to be set once, immediately after
	// NewChain(WithAlloc), and never touched again: deriveState replays the
	// WHOLE chain from genesis under whatever mode is current at replay
	// time, so changing it after blocks exist would make historical blocks
	// re-derive under a mode different from the one they were actually
	// produced and validated under.
	//
	// NOT YET consulted by AddBlock/CandidateStateRoot as of this PR -- PoW
	// behavior is completely unaffected until the wiring PR that follows
	// this one (see the M8 plan's PR5).
	consensusMode consensus.Mode
	// validatorSet is nil for a PoW chain; for a PoS chain it is the
	// genesis-fixed validator registry SetConsensusMode was given (see
	// pos.ValidatorSet's own doc comment for why M8 scopes this as
	// immutable, genesis-only config -- no live deposit/exit path exists).
	validatorSet *pos.ValidatorSet
}

// SetExchangeMode sets the matching mode every block's exchange transactions
// are applied under. See the exchangeMode field comment for why this should be
// called once, before mining begins, and not changed afterward.
func (c *Chain) SetExchangeMode(m exchange.Mode) { c.exchangeMode = m }

// ExchangeMode returns the currently configured exchange matching mode.
func (c *Chain) ExchangeMode() exchange.Mode { return c.exchangeMode }

// SetConsensusMode selects PoW or PoS for this chain's entire lifetime. See
// the consensusMode field comment for why this must be called once, before
// mining begins, and never changed afterward. Rejects PoS with an empty
// validator set (see ErrPoSRequiresValidators). Not yet consulted by
// AddBlock/CandidateStateRoot as of this PR.
func (c *Chain) SetConsensusMode(mode consensus.Mode, vs *pos.ValidatorSet) error {
	if mode == consensus.PoS && (vs == nil || vs.Len() == 0) {
		return ErrPoSRequiresValidators
	}
	c.consensusMode = mode
	c.validatorSet = vs
	return nil
}

// ConsensusMode returns the chain's configured consensus mode (PoW, the zero
// value, unless SetConsensusMode was called).
func (c *Chain) ConsensusMode() consensus.Mode { return c.consensusMode }

// ValidatorSet returns the chain's validator set, or nil for a PoW chain.
func (c *Chain) ValidatorSet() *pos.ValidatorSet { return c.validatorSet }

// NewChain creates a chain seeded with the genesis block. The optional alloc is
// the genesis allocation (same map given to ApplyGenesis); it is required for
// the engine to reproduce genesis balances when replaying state. Callers that
// used a funded genesis MUST pass the same alloc so State() and StateRoot
// validation are correct.
//
// NewChain never carries a base-asset allocation (see Genesis.BaseAlloc) --
// callers that funded genesis with one MUST use NewChainWithAlloc instead, or
// every replay (mining and validation alike) will fund a state that disagrees
// with the genesis block's own baked StateRoot.
func NewChain(genesis core.Block, alloc ...map[core.Address]uint64) *Chain {
	var a map[core.Address]uint64
	if len(alloc) > 0 {
		a = alloc[0]
	}
	return NewChainWithAlloc(genesis, a, nil)
}

// NewChainWithAlloc is NewChain with an explicit base-asset genesis allocation
// (credited via exchange.CreditBase, same as Genesis.BaseAlloc/ApplyGenesis)
// alongside the existing native/quote alloc. A setter-after-construction
// approach (the SetExchangeMode/SetChainID pattern) does not work here:
// NewChain funds headState via fundGenesis() before returning, so a setter
// would run too late -- the genesis block's already-baked StateRoot would
// already disagree with headState's root by the time it ran.
func NewChainWithAlloc(genesis core.Block, alloc, baseAlloc map[core.Address]uint64) *Chain {
	c := &Chain{
		blocks:      make(map[core.Hash]core.Block),
		td:          make(map[core.Hash]uint64),
		heightIndex: make(map[uint64]core.Hash),
		chainID:     DefaultChainID,
	}
	c.genesisAlloc = alloc
	c.genesisBaseAlloc = baseAlloc

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
	st := state.New()
	for addr, bal := range c.genesisAlloc {
		acct := st.GetAccount(addr)
		acct.Balance += bal
		st.SetAccount(addr, acct)
	}
	for addr, amt := range c.genesisBaseAlloc {
		exchange.CreditBase(st, addr, amt)
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
//
// Transactions go through applyTxsAt with b's real height and the given
// exchange mode -- the ONLY path a real block takes on the way into canonical
// state. A plain per-tx ApplyTx call here would silently forward (0, 0) for
// every transaction in every block ever mined, which does not fail a single
// test in isolation but collides the identity of every order ever placed on
// the chain -- Cancel could no longer tell one user's resting order from
// another's. (It did, for one commit's worth of this repository's history.)
func applyBlockRewarded(st state.StateDB, b core.Block, verifySig func(core.Transaction) bool, mode exchange.Mode) error {
	if err := applyTxsAt(st, b.Txs, verifySig, b.Header.Height, mode); err != nil {
		return err
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
		if err := applyBlockRewarded(st, path[i], verifySig, c.exchangeMode); err != nil {
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
	// The candidate block does not exist yet, so its height is derived rather
	// than read from a header: one past the current head's. AddBlock enforces
	// exactly this relationship (ErrBadHeight), so a miner using any other value
	// here would compute a root AddBlock could never actually validate.
	height := c.blocks[c.head].Header.Height + 1
	if err := applyTxsAt(st, txs, verifySig, height, c.exchangeMode); err != nil {
		return core.Hash{}, err
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
	if err := applyBlockRewarded(st, b, verifySig, c.exchangeMode); err != nil {
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
