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
	// ErrWrongProposer is returned by AddBlock (PoS mode) when a block's
	// Coinbase is not the validator pos.SelectProposer deterministically
	// picked for that height -- the PoS analog of ErrBadPoW.
	ErrWrongProposer = errors.New("chain: block proposer is not the selected validator for this height")
	// ErrBadProposerSig is returned by AddBlock (PoS mode) when a block's
	// ProposerSig does not verify against the selected proposer's BLS
	// public key over the header's own SigningHash.
	ErrBadProposerSig = errors.New("chain: invalid proposer BLS signature")
	// ErrBadAttestation is returned by AddBlock (PoS mode) when a checkpoint
	// attestation transaction is malformed, targets a height that is not a
	// checkpoint (not a multiple of pos.CheckpointInterval), targets a
	// height at or beyond the block that carries it (a validator cannot
	// attest to a block that does not exist yet), comes from a sender that
	// is not a registered validator, or carries a BLS signature that does
	// not verify.
	ErrBadAttestation = errors.New("chain: invalid checkpoint attestation")
	// ErrConflictsWithFinalized is returned by AddBlock (PoS mode) when a
	// block's ancestry does not pass through the currently finalized
	// checkpoint -- the concrete safety property PoS finality buys: once
	// >=2/3 of stake has attested to a checkpoint, no competing branch that
	// conflicts with it can ever become canonical again, regardless of how
	// much raw weight it accumulates (see respectsFinality).
	ErrConflictsWithFinalized = errors.New("chain: block conflicts with an already-finalized checkpoint")
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
	consensusMode consensus.Mode
	// validatorSet is nil for a PoW chain; for a PoS chain it is the
	// genesis-fixed validator registry SetConsensusMode was given (see
	// pos.ValidatorSet's own doc comment for why M8 scopes this as
	// immutable, genesis-only config -- no live deposit/exit path exists).
	validatorSet *pos.ValidatorSet

	// attestedRound/checkpointStake/finalizedHash/finalizedHeight track PoS
	// checkpoint finality (M8, PoS mode only). All four are computed as
	// AddBlock side effects and are PERMANENT once set -- never rolled back
	// by a later reorg. That permanence is exactly what makes finality
	// actually irreversible across competing branches: the real safety
	// property PoS buys (see respectsFinality/recordAttestations).
	//
	// attestedRound maps a checkpoint round (pos.CheckpointRound(height)) to
	// the one target hash each validator has cast a vote for in that round.
	// A validator's SECOND, conflicting vote for an already-voted round is
	// simply not tallied into checkpointStake -- this dedup-at-record-time
	// is the entire safety argument for why two conflicting targets can
	// never both cross 2/3 of stake (a validator contributes to at most one
	// target's tally per round, full stop). Detecting and JAILING the
	// equivocating validator is PR7's addition on top of this.
	attestedRound   map[uint64]map[core.Address]core.Hash
	checkpointStake map[core.Hash]uint64
	finalizedHash   core.Hash
	finalizedHeight uint64

	// jailed is the set of validators excluded from future proposer
	// selection (EffectiveValidatorSet) and future attestation tallying
	// (recordAttestations) after a detected equivocation -- double-propose
	// (proposedAtHeight) or double-attest (attestedRound's own conflicting-
	// vote check). PERMANENT once set, like the finality fields above:
	// jailing is a consequence of on-chain evidence (two conflicting
	// signatures), not a judgment call that could later be reversed by a
	// different branch winning out. No economic penalty (stake burn/
	// seizure) -- see the M8 plan's own scope decision: stake is immutable
	// genesis config, not a mutable balance, so there is nothing to seize.
	jailed map[core.Address]bool
	// proposedAtHeight tracks, for a given PARENT hash (a (PrevHash,
	// Height) slot -- Height is redundant given PrevHash, since AddBlock's
	// own height check pins it to parent.Height+1), which hash each
	// validator has already proposed. A validator producing a SECOND,
	// DIFFERENT block for the SAME parent is equivocation (double-propose).
	// Producing legitimately different blocks on two genuinely different
	// parents is an ordinary fork (the same thing a PoW chain already
	// tolerates) and must never be conflated with equivocation.
	proposedAtHeight map[core.Hash]map[core.Address]core.Hash
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
// validator set (see ErrPoSRequiresValidators).
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

// FinalizedHeight returns the height of the most recently finalized PoS
// checkpoint, or 0 if none has finalized yet (indistinguishable from "0 is
// finalized" -- callers that need to tell the two apart should check
// FinalizedHash().IsZero() too, exactly like genesis's own hash conventions
// elsewhere in this codebase).
func (c *Chain) FinalizedHeight() uint64 { return c.finalizedHeight }

// FinalizedHash returns the hash of the most recently finalized PoS
// checkpoint, or the zero Hash if none has finalized yet.
func (c *Chain) FinalizedHash() core.Hash { return c.finalizedHash }

// Jailed reports whether addr has been excluded from future PoS proposer
// selection and attestation tallying due to a detected equivocation
// (double-propose or double-attest). Always false for a PoW chain.
func (c *Chain) Jailed(addr core.Address) bool { return c.jailed[addr] }

// EffectiveValidatorSet returns the chain's currently active (non-jailed)
// validators and their combined stake -- the SAME view AddBlock's own
// proposer-selection check uses, so a caller building a candidate block
// (see node.Node.ProposeBlock) always agrees with what AddBlock will
// independently re-derive when validating it. Panics if called on a
// non-PoS chain (nil validatorSet); callers must check ConsensusMode first,
// exactly like every other PoS-only accessor in this package.
func (c *Chain) EffectiveValidatorSet() ([]pos.ValidatorInfo, uint64) {
	return c.validatorSet.EffectiveStake(c.jailed)
}

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
		blocks:           make(map[core.Hash]core.Block),
		td:               make(map[core.Hash]uint64),
		heightIndex:      make(map[uint64]core.Hash),
		chainID:          DefaultChainID,
		attestedRound:    make(map[uint64]map[core.Address]core.Hash),
		checkpointStake:  make(map[core.Hash]uint64),
		jailed:           make(map[core.Address]bool),
		proposedAtHeight: make(map[core.Hash]map[core.Address]core.Hash),
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

// respectsFinality rejects any block whose ancestry does not pass through
// the currently finalized PoS checkpoint at the finalized height -- the
// concrete implementation of "no competing branch that conflicts with an
// already-finalized checkpoint can ever become canonical again." A no-op for
// PoW chains and before anything has ever finalized.
//
// This runs BEFORE the proposer/weight checks in AddBlock, so a conflicting
// block is rejected at the earliest possible point: it can never accumulate
// even a single block of weight, let alone become heavier than the
// canonical branch. That is a stronger safety property than merely losing a
// td comparison at reorg time.
func (c *Chain) respectsFinality(b core.Block) error {
	if c.consensusMode != consensus.PoS || c.finalizedHash.IsZero() {
		return nil
	}
	h := b.Header.PrevHash
	for {
		blk, ok := c.blocks[h]
		if !ok {
			return ErrUnknownParent
		}
		if blk.Header.Height == c.finalizedHeight {
			if h != c.finalizedHash {
				return ErrConflictsWithFinalized
			}
			return nil
		}
		if blk.Header.Height < c.finalizedHeight {
			return ErrConflictsWithFinalized
		}
		h = blk.Header.PrevHash
	}
}

// verifyAttestations checks every checkpoint-attestation transaction in b
// (PoS mode only): well-formed calldata, a checkpoint-aligned target height
// that is not in b's own future (a validator cannot attest to a block that
// does not exist yet -- b's own attestations, if any, necessarily reference
// an EARLIER block, never b itself, since b's hash is not yet determined
// while its own Txs are still being decided), a sender that is a registered
// validator, and a BLS signature that verifies. A bad attestation rejects
// the WHOLE block, the same severity a bad ECDSA signature already gets via
// applyTxAtSession's own ErrBadSig.
func (c *Chain) verifyAttestations(b core.Block) error {
	for i := range b.Txs {
		tx := b.Txs[i]
		if !pos.IsAttestationTx(tx) {
			continue
		}
		targetHeight, targetHash, sig, err := pos.DecodeAttest(tx.Data)
		if err != nil {
			return ErrBadAttestation
		}
		if targetHeight%pos.CheckpointInterval != 0 {
			return ErrBadAttestation
		}
		if targetHeight >= b.Header.Height {
			return ErrBadAttestation
		}
		validator, ok := c.validatorSet.ByAddress(tx.From)
		if !ok {
			return ErrBadAttestation
		}
		if !pos.Verify(validator.BLSPubKey, targetHash[:], sig, pos.DST(c.chainID)) {
			return ErrBadAttestation
		}
	}
	return nil
}

// recordAttestations tallies every (already-verified, by verifyAttestations
// before b was ever committed) attestation tx in b into c.checkpointStake,
// and advances c.finalizedHash/finalizedHeight once a target crosses 2/3 of
// the EFFECTIVE (non-jailed) validator set's total stake. Called AFTER b is
// committed, unconditionally of whether b becomes the new canonical head --
// attestations are tallied permanently and globally, independent of
// canonicalness, which is what makes finality survive a later reorg away
// from the branch that carried them.
//
// A validator's SECOND, CONFLICTING vote for an already-voted round is
// equivocation (double-attest): it is never tallied, and it jails the
// validator (see the jailed field's own doc comment) -- from that point on,
// EffectiveValidatorSet excludes them, so a later vote from them (even a
// first-seen-this-round one) is also skipped.
func (c *Chain) recordAttestations(b core.Block) {
	for i := range b.Txs {
		tx := b.Txs[i]
		if !pos.IsAttestationTx(tx) {
			continue
		}
		targetHeight, targetHash, _, err := pos.DecodeAttest(tx.Data)
		if err != nil {
			continue // unreachable: verifyAttestations already rejected malformed data
		}
		validator, ok := c.validatorSet.ByAddress(tx.From)
		if !ok {
			continue // unreachable: verifyAttestations already rejected unknown senders
		}
		round := pos.CheckpointRound(targetHeight)
		if c.attestedRound[round] == nil {
			c.attestedRound[round] = make(map[core.Address]core.Hash)
		}
		if existing, voted := c.attestedRound[round][tx.From]; voted {
			if existing != targetHash {
				c.jailed[tx.From] = true
			}
			continue
		}
		if c.jailed[tx.From] {
			continue // a jailed validator's vote never counts, even a first-seen-this-round one
		}
		c.attestedRound[round][tx.From] = targetHash
		c.checkpointStake[targetHash] += validator.Stake

		_, effectiveTotal := c.EffectiveValidatorSet()
		if targetHeight > c.finalizedHeight && c.checkpointStake[targetHash]*3 >= effectiveTotal*2 {
			c.finalizedHash = targetHash
			c.finalizedHeight = targetHeight
		}
	}
}

// detectEquivocation checks whether b.Header.Coinbase has already proposed a
// DIFFERENT block for this same parent (see proposedAtHeight's own doc
// comment for why "same parent," not merely "same height") -- if so, jails
// the validator. Called AFTER b is committed, mirroring recordAttestations'
// own "permanent regardless of canonicalness" timing: equivocation must be
// remembered even if this specific block never becomes canonical.
func (c *Chain) detectEquivocation(b core.Block, hash core.Hash) {
	if c.proposedAtHeight[b.Header.PrevHash] == nil {
		c.proposedAtHeight[b.Header.PrevHash] = make(map[core.Address]core.Hash)
	}
	if existing, proposed := c.proposedAtHeight[b.Header.PrevHash][b.Header.Coinbase]; proposed {
		if existing != hash {
			c.jailed[b.Header.Coinbase] = true
		}
		return
	}
	c.proposedAtHeight[b.Header.PrevHash][b.Header.Coinbase] = hash
}

// AddBlock validates and stores b, performing a reorg if b's branch becomes the
// heaviest. Validation covers parent linkage, height, finality (PoS mode),
// PoW (or, in PoS mode, proposer-selection + BLS signature + attestations),
// tx merkle root, and re-derived state root.
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
	if err := c.respectsFinality(b); err != nil {
		return err
	}

	// weight is this block's contribution to cumulative "difficulty" (c.td),
	// the sole input to the reorg decision below. PoW blocks contribute their
	// real Difficulty, exactly as before M8. PoS blocks always carry
	// Difficulty == 0 (see core.Header's own doc comment -- it is genuinely
	// unused in PoS mode, not repurposed), so reusing it here would freeze
	// c.td at 0 forever and the reorg check below would never advance the
	// canonical head past genesis. Instead every accepted PoS block
	// contributes weight 1 -- a simple longest-valid-chain rule, which is
	// the correct fork-choice for a chain where pos.SelectProposer already
	// guarantees at most one legitimately-signed block per height; the only
	// way a real fork arises is a validator equivocating (double-proposing),
	// which PR7 detects and jails rather than resolving via block weight.
	var weight uint64
	if c.consensusMode == consensus.PoS {
		active, total := c.EffectiveValidatorSet()
		selected, err := pos.SelectProposer(active, total, pos.ProposerSeed(b.Header.PrevHash, b.Header.Height))
		if err != nil {
			return err
		}
		if b.Header.Coinbase != selected.Address {
			return ErrWrongProposer
		}
		signingHash := b.Header.SigningHash()
		if !pos.Verify(selected.BLSPubKey, signingHash[:], b.Header.ProposerSig, pos.DST(c.chainID)) {
			return ErrBadProposerSig
		}
		if err := c.verifyAttestations(b); err != nil {
			return err
		}
		weight = 1
	} else {
		if !consensus.MeetsTarget(hash, b.Header.Difficulty) {
			return ErrBadPoW
		}
		weight = uint64(b.Header.Difficulty)
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

	// Commit the block and its cumulative weight.
	c.blocks[hash] = b
	c.td[hash] = c.td[b.Header.PrevHash] + weight
	if c.consensusMode == consensus.PoS {
		// Both tallied/detected permanently, independent of which branch
		// ends up canonical below -- see recordAttestations/
		// detectEquivocation's own doc comments for why that permanence is
		// what makes finality and jailing survive a reorg.
		c.recordAttestations(b)
		c.detectEquivocation(b, hash)
	}

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
