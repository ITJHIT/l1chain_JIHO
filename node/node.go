// Package node ties the chain engine, wallet, and store together into a single
// runnable full node: it owns the canonical chain, an optional durable store, a
// transaction mempool, and a miner identity. It is the integration surface the
// JSON-RPC server (package rpc) and the CLI (cmd/l1) drive.
//
// The node deliberately reuses the existing packages without changing their
// public APIs. In particular, MineBlock derives a block's StateRoot exactly the
// way chain.AddBlock re-derives and validates it: parent state plus the block's
// transactions plus the fixed BlockReward credited to the miner (Header.Coinbase).
// The coinbase reward is part of canonical chain state, so it is folded into the
// header StateRoot and committed by the block hash. This keeps AddBlock's
// validation intact.
package node

import (
	"errors"
	"sync"
	"time"

	"l1chain/chain"
	"l1chain/consensus"
	"l1chain/core"
	"l1chain/store"
	"l1chain/wallet"
)

// Errors surfaced by the node's transaction admission and mining paths.
var (
	// ErrBadSignature is returned by SubmitTx when wallet.Verify rejects the tx.
	ErrBadSignature = errors.New("node: invalid transaction signature")
	// ErrBadNonce is returned by SubmitTx when the tx nonce does not match the
	// sender's next expected nonce (accounting for already-pooled txs).
	ErrBadNonce = errors.New("node: bad transaction nonce")
	// ErrInsufficientBalance is returned by SubmitTx when the sender cannot
	// cover the tx value (accounting for already-pooled txs).
	ErrInsufficientBalance = errors.New("node: insufficient balance")
	// ErrNoStore is returned when a persistence operation is requested but the
	// node was configured in-memory (no DBPath).
	ErrNoStore = errors.New("node: no store configured")
	// ErrMempoolFull is returned by SubmitTx when the mempool already holds the
	// configured maximum number of pending transactions. The policy is a
	// deterministic reject-when-full: the NEW tx is rejected and existing pooled
	// txs are left untouched so per-sender nonce sequencing is never broken.
	ErrMempoolFull = errors.New("node: mempool full")
	// ErrBadChainID is returned by SubmitTx when a tx's ChainID does not match
	// the node's configured chain id. It is the mempool-admission mirror of the
	// authoritative consensus rule chain.ErrBadChainID (enforced in AddBlock /
	// CandidateStateRoot); rejecting here keeps replayed foreign-chain txs out of
	// the mempool so they can never poison mining.
	ErrBadChainID = errors.New("node: transaction chain id mismatch")
)

// Config parameterizes a Node.
type Config struct {
	// DBPath, when non-empty, enables durable persistence via package store.
	// When empty the node runs fully in-memory.
	DBPath string
	// MinerKey is the identity that produces blocks. Its address is used as the
	// coinbase miner. A zero Key is acceptable for read-only/in-memory usage but
	// MineBlock needs a real key to be meaningful.
	MinerKey wallet.Key
	// Difficulty is the PoW difficulty (leading zero bits) for genesis and every
	// mined block.
	Difficulty uint32
	// GenesisAlloc is the funded genesis allocation used when a fresh chain is
	// created. Ignored when an existing chain is loaded from the store.
	GenesisAlloc map[core.Address]uint64
	// GenesisTimestamp, when non-zero, fixes the genesis block timestamp so
	// independently constructed nodes (e.g. a multi-node network) produce an
	// identical genesis block/hash. When zero the current time is used.
	GenesisTimestamp int64
	// MaxMempool caps the number of pending transactions held in the mempool.
	// Values <= 0 resolve to DefaultMaxMempool. SubmitTx rejects new txs with
	// ErrMempoolFull once the cap is reached (existing pooled txs are kept).
	MaxMempool int
	// ChainID is the replay-protection domain. Values <= 0 (i.e. 0) resolve to
	// DefaultChainID. It is threaded into the chain so genesis and every flow
	// agree, and SubmitTx rejects txs carrying a different ChainID with
	// ErrBadChainID.
	ChainID uint64
}

// DefaultMaxMempool is the mempool size cap used when Config.MaxMempool <= 0.
// It bounds memory growth / mempool-flood DoS while leaving ample headroom for
// normal pending-tx batches.
const DefaultMaxMempool = 4096

// DefaultChainID is the replay-protection domain used when Config.ChainID == 0.
// It re-exports chain.DefaultChainID so node callers need not import chain.
const DefaultChainID = chain.DefaultChainID

// Node is a single full node: chain + optional store + mempool + miner.
type Node struct {
	// mu guards the mutable shared state below (chain canonical state map,
	// mempool, store) against concurrent access. SubmitTx/MineBlock take the
	// write lock; Head/Balance/Nonce/GetBlockByHeight/GetTxByHash/MempoolLen
	// take the read lock. deriveStateRoot's mutate-then-restore of the shared
	// canonical state runs entirely under the write lock held by MineBlock.
	mu         sync.RWMutex
	chain      *chain.Chain
	store      *store.Store
	mempool    []core.Transaction
	maxMempool int
	miner      wallet.Key
	Difficulty uint32
	chainID    uint64

	alloc map[core.Address]uint64
}

// New constructs a Node from cfg. If a store head already exists at DBPath the
// chain is loaded from it; otherwise a funded genesis is built (matching
// chain/genesis.go) and, when persistent, saved together with its allocation.
func New(cfg Config) (*Node, error) {
	maxMempool := cfg.MaxMempool
	if maxMempool <= 0 {
		maxMempool = DefaultMaxMempool
	}
	chainID := cfg.ChainID
	if chainID == 0 {
		chainID = DefaultChainID
	}
	n := &Node{
		miner:      cfg.MinerKey,
		Difficulty: cfg.Difficulty,
		chainID:    chainID,
		alloc:      cfg.GenesisAlloc,
		maxMempool: maxMempool,
	}

	if cfg.DBPath != "" {
		s, err := store.Open(cfg.DBPath)
		if err != nil {
			return nil, err
		}
		n.store = s

		c, ok, err := store.Load(s)
		if err != nil {
			_ = s.Close()
			return nil, err
		}
		if ok {
			n.chain = c
			n.chain.SetChainID(chainID)
			// Recover the genesis alloc so re-saves stay faithful.
			if alloc, have, err := s.GetGenesisAlloc(); err == nil && have {
				n.alloc = alloc
			}
			return n, nil
		}
	}

	// Fresh chain: build a funded genesis exactly like chain/genesis.go.
	genesisTS := cfg.GenesisTimestamp
	if genesisTS == 0 {
		genesisTS = time.Now().Unix()
	}
	g := chain.Genesis{
		Alloc:      cfg.GenesisAlloc,
		Difficulty: cfg.Difficulty,
		Timestamp:  genesisTS,
	}
	genesis := g.ToBlock()
	n.chain = chain.NewChain(genesis, cfg.GenesisAlloc)
	n.chain.SetChainID(chainID)

	if n.store != nil {
		if err := n.store.PutGenesisAlloc(cfg.GenesisAlloc); err != nil {
			_ = n.store.Close()
			return nil, err
		}
		if err := store.Save(n.store, n.chain); err != nil {
			_ = n.store.Close()
			return nil, err
		}
	}
	return n, nil
}

// Close releases the underlying store, if any.
func (n *Node) Close() error {
	if n.store != nil {
		return n.store.Close()
	}
	return nil
}

// MinerAddress returns the node's coinbase/miner address.
func (n *Node) MinerAddress() core.Address { return n.miner.Address() }

// Head returns the canonical head block.
func (n *Node) Head() core.Block {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.chain.Head()
}

// Balance returns the balance of addr in the canonical head state.
func (n *Node) Balance(addr core.Address) uint64 {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.chain.State().GetAccount(addr).Balance
}

// Nonce returns the current account nonce of addr in the canonical head state.
func (n *Node) Nonce(addr core.Address) uint64 {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.chain.State().GetAccount(addr).Nonce
}

// MempoolLen returns the number of pending transactions.
func (n *Node) MempoolLen() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return len(n.mempool)
}

// MempoolCap returns the resolved maximum number of pending transactions the
// mempool will hold. SubmitTx rejects new txs with ErrMempoolFull at this size.
func (n *Node) MempoolCap() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.maxMempool
}

// projectSender returns the effective next nonce and spendable balance for addr,
// starting from head state and folding in already-pooled transactions from addr.
// This lets a sender queue several sequential transactions before mining.
func (n *Node) projectSender(addr core.Address) (nonce, balance uint64) {
	acct := n.chain.State().GetAccount(addr)
	nonce = acct.Nonce
	balance = acct.Balance
	for i := range n.mempool {
		tx := n.mempool[i]
		if tx.From != addr {
			continue
		}
		nonce++
		if tx.From != tx.To && balance >= tx.Value {
			balance -= tx.Value
		}
	}
	return nonce, balance
}

// SubmitTx validates tx and, if acceptable, appends it to the mempool. It
// rejects unsigned/forged txs, wrong nonces, and unaffordable transfers,
// projecting already-pooled txs from the same sender. Once the mempool holds
// MempoolCap() txs it rejects further txs with ErrMempoolFull rather than
// evicting pooled txs, which would break per-sender nonce sequencing.
func (n *Node) SubmitTx(tx core.Transaction) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !wallet.Verify(tx) {
		return ErrBadSignature
	}
	if tx.ChainID != n.chainID {
		return ErrBadChainID
	}
	nextNonce, spendable := n.projectSender(tx.From)
	if tx.Nonce != nextNonce {
		return ErrBadNonce
	}
	if tx.From != tx.To && spendable < tx.Value {
		return ErrInsufficientBalance
	}
	if len(n.mempool) >= n.maxMempool {
		return ErrMempoolFull
	}
	n.mempool = append(n.mempool, tx)
	return nil
}

// deriveStateRoot computes the state root that results from applying txs on top
// of the current head plus a BlockReward credit to coinbase, WITHOUT mutating
// canonical state. It delegates to chain.CandidateStateRoot, which replays from
// genesis into a fresh state exactly the way chain.AddBlock re-derives and
// validates — so a mined block's StateRoot (including any contract-tx storage
// and gas effects) matches on re-validation. This is essential now that the
// StackVM writes storage: a mutate-then-restore over live head state cannot
// safely roll back contract storage, whereas a fresh replay never touches it.
func (n *Node) deriveStateRoot(txs []core.Transaction, coinbase core.Address) (core.Hash, error) {
	return n.chain.CandidateStateRoot(txs, coinbase, wallet.Verify)
}

// MineBlock builds a block over Head() from the mempool, solves PoW at the
// node's Difficulty, and appends it via chain.AddBlock. On success the included
// transactions are dropped from the mempool and (when persistent) the chain is
// re-saved. Returns the mined block.
func (n *Node) MineBlock() (core.Block, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	head := n.chain.Head()
	coinbase := n.miner.Address()
	txs := make([]core.Transaction, len(n.mempool))
	copy(txs, n.mempool)

	stateRoot, err := n.deriveStateRoot(txs, coinbase)
	if err != nil {
		return core.Block{}, err
	}

	blk := core.Block{
		Header: core.Header{
			Height:     head.Header.Height + 1,
			PrevHash:   head.Hash(),
			Coinbase:   coinbase,
			Timestamp:  time.Now().Unix(),
			Difficulty: n.Difficulty,
			StateRoot:  stateRoot,
		},
		Txs: txs,
	}
	blk.Header.MerkleRoot = blk.TxRoot()

	if _, found := consensus.Mine(&blk.Header, 0); !found {
		return core.Block{}, errors.New("node: mining failed to find a nonce")
	}

	if err := n.chain.AddBlock(blk, wallet.Verify); err != nil {
		return core.Block{}, err
	}

	// Drop the included txs (by hash) from the mempool.
	included := make(map[core.Hash]struct{}, len(txs))
	for i := range txs {
		included[txs[i].Hash()] = struct{}{}
	}
	kept := n.mempool[:0]
	for i := range n.mempool {
		if _, ok := included[n.mempool[i].Hash()]; !ok {
			kept = append(kept, n.mempool[i])
		}
	}
	n.mempool = kept

	if n.store != nil {
		if err := store.Save(n.store, n.chain); err != nil {
			return core.Block{}, err
		}
	}
	return blk, nil
}

// GetBlockByHeight returns the canonical block at height.
func (n *Node) GetBlockByHeight(height uint64) (core.Block, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.chain.GetByHeight(height)
}

// GetTxByHash scans the canonical chain from genesis to head for a transaction
// with the given hash and returns it when found.
func (n *Node) GetTxByHash(h core.Hash) (core.Transaction, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	head := n.chain.Head()
	for height := uint64(0); height <= head.Header.Height; height++ {
		b, ok := n.chain.GetByHeight(height)
		if !ok {
			continue
		}
		for i := range b.Txs {
			if b.Txs[i].Hash() == h {
				return b.Txs[i], true
			}
		}
	}
	return core.Transaction{}, false
}

// AcceptExternalBlock validates and adds a block received from a peer over the
// network. It takes the write lock and drives the exact same validated
// chain.AddBlock path (PoW, tx merkle root, re-derived state root, linkage)
// used by MineBlock, so a peer can never inject an invalid block. Duplicate /
// already-known blocks return nil so gossip re-delivery is idempotent. Callers
// that receive ErrUnknownParent should sync the missing ancestors and retry.
func (n *Node) AcceptExternalBlock(b core.Block) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.chain.AddBlock(b, wallet.Verify); err != nil {
		if errors.Is(err, chain.ErrDuplicateBlock) {
			return nil
		}
		return err
	}
	if n.store != nil {
		if err := store.Save(n.store, n.chain); err != nil {
			return err
		}
	}
	return nil
}

// CanonicalBlocksFrom returns the canonical blocks from height up to and
// including the current head, in ascending height order, for chain sync. It
// takes the read lock. Heights with no canonical block are skipped (there
// should be none in a contiguous canonical branch).
func (n *Node) CanonicalBlocksFrom(height uint64) []core.Block {
	n.mu.RLock()
	defer n.mu.RUnlock()
	head := n.chain.Head()
	var out []core.Block
	for h := height; h <= head.Header.Height; h++ {
		b, ok := n.chain.GetByHeight(h)
		if !ok {
			continue
		}
		out = append(out, b)
	}
	return out
}
