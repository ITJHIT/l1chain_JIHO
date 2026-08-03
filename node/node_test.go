package node

import (
	"errors"
	"path/filepath"
	"testing"

	"l1chain/chain"
	"l1chain/consensus"
	"l1chain/core"
	"l1chain/pos"
	"l1chain/wallet"
)

const testDifficulty = 6

// addr builds a deterministic non-zero address from a single byte, mirroring
// the same small helper other packages' own test files already define
// locally (chain, redteam, etc.).
func addr(b byte) core.Address {
	var a core.Address
	a[0] = b
	return a
}

func newKeyT(t *testing.T) wallet.Key {
	t.Helper()
	k, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return k
}

// signedTransfer builds and signs a transfer from -> to.
func signedTransfer(from wallet.Key, to core.Address, value, nonce uint64) core.Transaction {
	tx := core.Transaction{To: to, Value: value, Nonce: nonce, GasLimit: 21000, ChainID: chain.DefaultChainID}
	from.Sign(&tx)
	return tx
}

func TestGenesisFunding(t *testing.T) {
	a := newKeyT(t)
	n, err := New(Config{
		MinerKey:     newKeyT(t),
		Difficulty:   testDifficulty,
		GenesisAlloc: map[core.Address]uint64{a.Address(): 1000},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()

	if got := n.Balance(a.Address()); got != 1000 {
		t.Fatalf("genesis balance = %d, want 1000", got)
	}
	if h := n.Head().Header.Height; h != 0 {
		t.Fatalf("genesis head height = %d, want 0", h)
	}
}

func TestSubmitTxAcceptAndReject(t *testing.T) {
	a := newKeyT(t)
	b := newKeyT(t)
	n, err := New(Config{
		MinerKey:     newKeyT(t),
		Difficulty:   testDifficulty,
		GenesisAlloc: map[core.Address]uint64{a.Address(): 1000},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()

	// Valid signed tx is accepted.
	tx := signedTransfer(a, b.Address(), 100, 0)
	if err := n.SubmitTx(tx); err != nil {
		t.Fatalf("SubmitTx(valid) = %v, want nil", err)
	}
	if n.MempoolLen() != 1 {
		t.Fatalf("mempool len = %d, want 1", n.MempoolLen())
	}

	// Tampered tx (value changed after signing) must be rejected.
	tampered := tx
	tampered.Value = 999
	tampered.Nonce = 1
	if err := n.SubmitTx(tampered); err == nil {
		t.Fatalf("SubmitTx(tampered) = nil, want rejection")
	}

	// Unsigned tx must be rejected.
	unsigned := core.Transaction{From: a.Address(), To: b.Address(), Value: 1, Nonce: 1}
	if err := n.SubmitTx(unsigned); err == nil {
		t.Fatalf("SubmitTx(unsigned) = nil, want rejection")
	}

	// Overspend must be rejected.
	over := signedTransfer(a, b.Address(), 100000, 1)
	if err := n.SubmitTx(over); err == nil {
		t.Fatalf("SubmitTx(overspend) = nil, want rejection")
	}
}

func TestMineBlockAdvancesHeadAndBalances(t *testing.T) {
	a := newKeyT(t)
	b := newKeyT(t)
	n, err := New(Config{
		MinerKey:     newKeyT(t),
		Difficulty:   testDifficulty,
		GenesisAlloc: map[core.Address]uint64{a.Address(): 1000},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()

	if err := n.SubmitTx(signedTransfer(a, b.Address(), 250, 0)); err != nil {
		t.Fatalf("SubmitTx: %v", err)
	}

	blk, err := n.MineBlock()
	if err != nil {
		t.Fatalf("MineBlock: %v", err)
	}
	if blk.Header.Height != 1 {
		t.Fatalf("mined height = %d, want 1", blk.Header.Height)
	}
	head := n.Head()
	if head.Hash() != blk.Hash() {
		t.Fatalf("head not advanced to mined block")
	}
	if n.MempoolLen() != 0 {
		t.Fatalf("mempool not drained: %d", n.MempoolLen())
	}
	if got := n.Balance(a.Address()); got != 750 {
		t.Fatalf("A balance = %d, want 750", got)
	}
	if got := n.Balance(b.Address()); got != 250 {
		t.Fatalf("B balance = %d, want 250", got)
	}
	// A transfer block credits the recipient AND the miner's coinbase reward,
	// while the sender is debited; totals stay consistent.
	if got := n.Balance(n.MinerAddress()); got != chain.BlockReward {
		t.Fatalf("miner balance = %d, want %d (one block reward)", got, chain.BlockReward)
	}

	// The tx is retrievable by hash from the canonical chain.
	txHash := blk.Txs[0].Hash()
	if _, ok := n.GetTxByHash(txHash); !ok {
		t.Fatalf("GetTxByHash did not find mined tx")
	}
	if _, ok := n.GetBlockByHeight(1); !ok {
		t.Fatalf("GetBlockByHeight(1) not found")
	}
}

func TestPersistenceAcrossReload(t *testing.T) {
	a := newKeyT(t)
	b := newKeyT(t)
	miner := newKeyT(t)
	dbPath := filepath.Join(t.TempDir(), "chain.db")
	alloc := map[core.Address]uint64{a.Address(): 1000}

	// First instance: mine a transfer, then close.
	n1, err := New(Config{DBPath: dbPath, MinerKey: miner, Difficulty: testDifficulty, GenesisAlloc: alloc})
	if err != nil {
		t.Fatalf("New(1): %v", err)
	}
	if err := n1.SubmitTx(signedTransfer(a, b.Address(), 300, 0)); err != nil {
		t.Fatalf("SubmitTx: %v", err)
	}
	blk, err := n1.MineBlock()
	if err != nil {
		t.Fatalf("MineBlock: %v", err)
	}
	wantHead := blk.Hash()
	if err := n1.Close(); err != nil {
		t.Fatalf("Close(1): %v", err)
	}

	// Second instance: reload from the same DB; state must match.
	n2, err := New(Config{DBPath: dbPath, MinerKey: miner, Difficulty: testDifficulty, GenesisAlloc: alloc})
	if err != nil {
		t.Fatalf("New(2 reload): %v", err)
	}
	defer n2.Close()

	h2 := n2.Head()
	if h2.Hash() != wantHead {
		t.Fatalf("reloaded head = %s, want %s", h2.Hash().Hex(), wantHead.Hex())
	}
	if got := n2.Balance(a.Address()); got != 700 {
		t.Fatalf("reloaded A balance = %d, want 700", got)
	}
	if got := n2.Balance(b.Address()); got != 300 {
		t.Fatalf("reloaded B balance = %d, want 300", got)
	}
	if h := n2.Head().Header.Height; h != 1 {
		t.Fatalf("reloaded head height = %d, want 1", h)
	}
}

// TestMineBlockGrowsMinerBalanceByReward verifies the coinbase reward is now
// part of canonical state: each mined block increases the miner's balance by
// exactly BlockReward, even for empty (no-tx) blocks.
func TestMineBlockGrowsMinerBalanceByReward(t *testing.T) {
	n, err := New(Config{
		MinerKey:     newKeyT(t),
		Difficulty:   testDifficulty,
		GenesisAlloc: map[core.Address]uint64{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()

	miner := n.MinerAddress()
	if got := n.Balance(miner); got != 0 {
		t.Fatalf("pre-mine miner balance = %d, want 0", got)
	}
	for i := uint64(1); i <= 3; i++ {
		if _, err := n.MineBlock(); err != nil {
			t.Fatalf("MineBlock %d: %v", i, err)
		}
		if got, want := n.Balance(miner), i*chain.BlockReward; got != want {
			t.Fatalf("after %d blocks miner balance = %d, want %d", i, got, want)
		}
	}
}

// TestReplayAcrossChainIDRejected proves chain-id replay protection end to end.
// A transfer signed for the default chain id is:
//   - REJECTED at admission by a node configured for a foreign chain id
//     (node.ErrBadChainID) and never enters its mempool;
//   - REJECTED at the consensus boundary by that foreign node when delivered
//     inside a mined block (chain.ErrBadChainID via AcceptExternalBlock);
//   - ACCEPTED, mined, and credited by a node configured for the matching id.
// Because the signature commits to ChainID (core.Transaction.preimage), the tx
// cannot be re-signed for the other domain without a different key.
func TestReplayAcrossChainIDRejected(t *testing.T) {
	const foreignChainID = 4242

	a := newKeyT(t)
	b := newKeyT(t)
	alloc := map[core.Address]uint64{a.Address(): 1000}

	// Both nodes share an identical genesis (chain id is NOT part of genesis), so
	// a block mined on the matching node links cleanly onto the foreign node.
	nodeA, err := New(Config{
		MinerKey:         newKeyT(t),
		Difficulty:       testDifficulty,
		GenesisAlloc:     alloc,
		GenesisTimestamp: 1000,
		ChainID:          chain.DefaultChainID,
	})
	if err != nil {
		t.Fatalf("New A: %v", err)
	}
	defer nodeA.Close()

	nodeB, err := New(Config{
		MinerKey:         newKeyT(t),
		Difficulty:       testDifficulty,
		GenesisAlloc:     alloc,
		GenesisTimestamp: 1000,
		ChainID:          foreignChainID,
	})
	if err != nil {
		t.Fatalf("New B: %v", err)
	}
	defer nodeB.Close()

	// Signed for the default chain id (signedTransfer sets ChainID = DefaultChainID).
	tx := signedTransfer(a, b.Address(), 100, 0)

	// Admission-level replay protection on the foreign node.
	if err := nodeB.SubmitTx(tx); !errors.Is(err, ErrBadChainID) {
		t.Fatalf("nodeB.SubmitTx = %v, want ErrBadChainID", err)
	}
	if nodeB.MempoolLen() != 0 {
		t.Fatalf("foreign-chain tx entered node B mempool")
	}

	// Matching node accepts and mines the same tx.
	if err := nodeA.SubmitTx(tx); err != nil {
		t.Fatalf("nodeA.SubmitTx = %v, want nil", err)
	}
	blk, err := nodeA.MineBlock()
	if err != nil {
		t.Fatalf("nodeA.MineBlock: %v", err)
	}
	if got := nodeA.Balance(b.Address()); got != 100 {
		t.Fatalf("recipient balance on node A = %d, want 100", got)
	}

	// Consensus-level replay protection: the foreign node rejects the *block*
	// carrying the foreign-chain tx through the exact AddBlock validation path.
	if err := nodeB.AcceptExternalBlock(blk); !errors.Is(err, chain.ErrBadChainID) {
		t.Fatalf("nodeB.AcceptExternalBlock = %v, want chain.ErrBadChainID", err)
	}
	if got := nodeB.Head().Header.Height; got != 0 {
		t.Fatalf("node B head advanced to %d on a foreign-chain block", got)
	}
}

// testValidatorInfo returns a fresh validator registration and the real BLS
// key behind it, so callers that need to actually propose/sign PoS blocks
// (not just pass config validation) can configure Config.ValidatorBLSKey.
func testValidatorInfo(t *testing.T, addr core.Address, stake uint64) (pos.ValidatorInfo, pos.Key) {
	t.Helper()
	k, err := pos.NewKey()
	if err != nil {
		t.Fatalf("pos.NewKey: %v", err)
	}
	return pos.ValidatorInfo{Address: addr, BLSPubKey: k.PubKey(), Stake: stake}, k
}

func TestNewDefaultsToPoWConsensusMode(t *testing.T) {
	a := newKeyT(t)
	n, err := New(Config{
		MinerKey:     newKeyT(t),
		Difficulty:   testDifficulty,
		GenesisAlloc: map[core.Address]uint64{a.Address(): 1000},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()
	if got := n.ConsensusMode(); got != consensus.PoW {
		t.Fatalf("ConsensusMode() = %v, want PoW (Config.ConsensusMode left unset)", got)
	}
}

func TestNewWithPoSConsensusModeAndValidators(t *testing.T) {
	a := newKeyT(t)
	info, _ := testValidatorInfo(t, addr(1), 100)
	validators := []pos.ValidatorInfo{info}
	n, err := New(Config{
		MinerKey:      newKeyT(t),
		Difficulty:    testDifficulty,
		GenesisAlloc:  map[core.Address]uint64{a.Address(): 1000},
		ConsensusMode: consensus.PoS,
		Validators:    validators,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()
	if got := n.ConsensusMode(); got != consensus.PoS {
		t.Fatalf("ConsensusMode() = %v, want PoS", got)
	}
}

func TestNewRejectsPoSWithoutValidators(t *testing.T) {
	a := newKeyT(t)
	_, err := New(Config{
		MinerKey:      newKeyT(t),
		Difficulty:    testDifficulty,
		GenesisAlloc:  map[core.Address]uint64{a.Address(): 1000},
		ConsensusMode: consensus.PoS,
		// Validators deliberately omitted.
	})
	if err == nil {
		t.Fatal("New must reject ConsensusMode: PoS with no validators")
	}
}

// TestReloadRejectsPoSConsensusMode proves the store-persistence gap fails
// LOUD, not silent: consensus mode is not yet persisted (M8), so reloading a
// chain from an existing store while asking for PoS must be refused outright
// rather than silently starting a PoW node the caller believes is PoS.
func TestReloadRejectsPoSConsensusMode(t *testing.T) {
	a := newKeyT(t)
	miner := newKeyT(t)
	dbPath := filepath.Join(t.TempDir(), "chain.db")
	alloc := map[core.Address]uint64{a.Address(): 1000}

	n1, err := New(Config{DBPath: dbPath, MinerKey: miner, Difficulty: testDifficulty, GenesisAlloc: alloc})
	if err != nil {
		t.Fatalf("New(1): %v", err)
	}
	if err := n1.Close(); err != nil {
		t.Fatalf("Close(1): %v", err)
	}

	info, _ := testValidatorInfo(t, addr(1), 100)
	validators := []pos.ValidatorInfo{info}
	_, err = New(Config{
		DBPath:        dbPath,
		MinerKey:      miner,
		Difficulty:    testDifficulty,
		GenesisAlloc:  alloc,
		ConsensusMode: consensus.PoS,
		Validators:    validators,
	})
	if !errors.Is(err, ErrConsensusModeNotPersisted) {
		t.Fatalf("New(reload, PoS) = %v, want ErrConsensusModeNotPersisted", err)
	}
}

func TestProposeBlockRejectsOnPoWChain(t *testing.T) {
	a := newKeyT(t)
	n, err := New(Config{
		MinerKey:     newKeyT(t),
		Difficulty:   testDifficulty,
		GenesisAlloc: map[core.Address]uint64{a.Address(): 1000},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()
	if _, err := n.ProposeBlock(); !errors.Is(err, ErrNotPoSMode) {
		t.Fatalf("ProposeBlock on a PoW chain = %v, want ErrNotPoSMode", err)
	}
}

// TestProposeBlockBuildsAndCommitsWhenSelected drives the real, full
// production path (Node.ProposeBlock, not the lower-level chain.AddBlock
// directly) end to end: a single-validator PoS chain where this node's own
// MinerKey address IS that validator (so it is selected for every height,
// avoiding the need to simulate multiple nodes/turns), with a real
// ValidatorBLSKey configured to sign with.
func TestProposeBlockBuildsAndCommitsWhenSelected(t *testing.T) {
	miner := newKeyT(t)
	a := newKeyT(t)
	info, blsKey := testValidatorInfo(t, miner.Address(), 100)
	n, err := New(Config{
		MinerKey:        miner,
		GenesisAlloc:    map[core.Address]uint64{a.Address(): 1000},
		ConsensusMode:   consensus.PoS,
		Validators:      []pos.ValidatorInfo{info},
		ValidatorBLSKey: blsKey,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()

	if err := n.SubmitTx(signedTransfer(a, miner.Address(), 100, 0)); err != nil {
		t.Fatalf("SubmitTx: %v", err)
	}

	blk, err := n.ProposeBlock()
	if err != nil {
		t.Fatalf("ProposeBlock: %v", err)
	}
	if got := blk.Header.Height; got != 1 {
		t.Fatalf("proposed block height = %d, want 1", got)
	}
	if got := blk.Header.Coinbase; got != miner.Address() {
		t.Fatalf("proposed block coinbase = %x, want %x", got, miner.Address())
	}
	if len(blk.Header.ProposerSig) == 0 {
		t.Fatal("proposed block carries no ProposerSig")
	}
	if got := n.Head().Header.Height; got != 1 {
		t.Fatalf("node head height = %d, want 1 (ProposeBlock must commit via AddBlock)", got)
	}
	if got := n.Balance(miner.Address()); got != 100+chain.BlockReward {
		t.Fatalf("miner balance = %d, want %d (the included transfer plus the block reward, since the miner is also this block's coinbase)", got, 100+chain.BlockReward)
	}
}

// TestProposeBlockReturnsNotMyTurnWhenNotSelected configures a node whose own
// MinerKey address is NOT in the PoS validator set at all, so it can never be
// selected -- ProposeBlock must return ErrNotMyTurn, not attempt to build or
// sign anything.
func TestProposeBlockReturnsNotMyTurnWhenNotSelected(t *testing.T) {
	miner := newKeyT(t) // NOT a validator
	a := newKeyT(t)
	info, _ := testValidatorInfo(t, addr(1), 100) // a different address entirely
	n, err := New(Config{
		MinerKey:      miner,
		GenesisAlloc:  map[core.Address]uint64{a.Address(): 1000},
		ConsensusMode: consensus.PoS,
		Validators:    []pos.ValidatorInfo{info},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()

	if _, err := n.ProposeBlock(); !errors.Is(err, ErrNotMyTurn) {
		t.Fatalf("ProposeBlock (not a validator) = %v, want ErrNotMyTurn", err)
	}
	if got := n.Head().Header.Height; got != 0 {
		t.Fatalf("node head height = %d, want 0 (no block should have been produced)", got)
	}
}

// TestProposeBlockRejectsWithNoValidatorKey configures a node whose MinerKey
// IS the sole validator (so it WOULD be selected), but never sets
// ValidatorBLSKey -- ProposeBlock must fail cleanly with ErrNoValidatorKey
// rather than panic signing with a nil key.
func TestProposeBlockRejectsWithNoValidatorKey(t *testing.T) {
	miner := newKeyT(t)
	a := newKeyT(t)
	info, _ := testValidatorInfo(t, miner.Address(), 100)
	n, err := New(Config{
		MinerKey:      miner,
		GenesisAlloc:  map[core.Address]uint64{a.Address(): 1000},
		ConsensusMode: consensus.PoS,
		Validators:    []pos.ValidatorInfo{info},
		// ValidatorBLSKey deliberately left zero.
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()

	if _, err := n.ProposeBlock(); !errors.Is(err, ErrNoValidatorKey) {
		t.Fatalf("ProposeBlock (no validator key) = %v, want ErrNoValidatorKey", err)
	}
}

// TestMaybeAttestSubmitsAtCheckpointHeightAndDedups drives a single-validator
// PoS node (always selected, so ProposeBlock alone can build a real chain)
// to a real checkpoint height, then proves MaybeAttest builds and submits a
// real attestation transaction exactly once -- a second call before the
// first is ever mined must not resubmit a duplicate.
func TestMaybeAttestSubmitsAtCheckpointHeightAndDedups(t *testing.T) {
	a := newKeyT(t)
	miner := newKeyT(t)
	info, blsKey := testValidatorInfo(t, miner.Address(), 100)
	n, err := New(Config{
		MinerKey:        miner,
		GenesisAlloc:    map[core.Address]uint64{a.Address(): 1000},
		ConsensusMode:   consensus.PoS,
		Validators:      []pos.ValidatorInfo{info},
		ValidatorBLSKey: blsKey,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()

	for h := 0; h < pos.CheckpointInterval; h++ {
		if _, err := n.ProposeBlock(); err != nil {
			t.Fatalf("ProposeBlock (building height %d): %v", h+1, err)
		}
	}
	if got := n.Head().Header.Height; got != uint64(pos.CheckpointInterval) {
		t.Fatalf("head height = %d, want %d", got, pos.CheckpointInterval)
	}

	if err := n.MaybeAttest(); err != nil {
		t.Fatalf("MaybeAttest: %v", err)
	}
	if got := n.MempoolLen(); got != 1 {
		t.Fatalf("mempool length after MaybeAttest = %d, want 1 (the attestation tx)", got)
	}

	// Calling again before the attestation is ever mined must not resubmit
	// a duplicate (see lastAttestedHeight's own doc comment).
	if err := n.MaybeAttest(); err != nil {
		t.Fatalf("MaybeAttest (second call): %v", err)
	}
	if got := n.MempoolLen(); got != 1 {
		t.Fatalf("mempool length after second MaybeAttest = %d, want still 1 (no duplicate submission)", got)
	}
}

// TestMaybeAttestNoOpBeforeCheckpointHeight proves MaybeAttest does nothing
// (no error, no mempool growth) before the chain has actually reached a
// checkpoint height.
func TestMaybeAttestNoOpBeforeCheckpointHeight(t *testing.T) {
	a := newKeyT(t)
	miner := newKeyT(t)
	info, blsKey := testValidatorInfo(t, miner.Address(), 100)
	n, err := New(Config{
		MinerKey:        miner,
		GenesisAlloc:    map[core.Address]uint64{a.Address(): 1000},
		ConsensusMode:   consensus.PoS,
		Validators:      []pos.ValidatorInfo{info},
		ValidatorBLSKey: blsKey,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()

	if _, err := n.ProposeBlock(); err != nil {
		t.Fatalf("ProposeBlock: %v", err)
	}
	if err := n.MaybeAttest(); err != nil {
		t.Fatalf("MaybeAttest: %v", err)
	}
	if got := n.MempoolLen(); got != 0 {
		t.Fatalf("mempool length = %d, want 0 (height 1 is not a checkpoint)", got)
	}
}
