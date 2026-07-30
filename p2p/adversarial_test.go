package p2p

// Adversarial P2P red-team suite (M2). Each test drives a malicious or broken
// peer against an honest node over REAL libp2p hosts + GossipSub (the same
// stack as p2p_test.go) and asserts the honest node REJECTS the attack while
// staying alive, un-corrupted, and non-diverged. It also proves liveness: after
// each attack the honest node still accepts a subsequent VALID block/tx.
//
// This file adds tests only; it does not modify any production code and does
// not weaken validation. Every network block still flows through
// node.AcceptExternalBlock -> chain.AddBlock.

import (
	"context"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"l1chain/chain"
	"l1chain/consensus"
	"l1chain/core"
	"l1chain/node"
	"l1chain/store"
	"l1chain/wallet"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// mineChain builds a fresh miner node sharing the deterministic test genesis and
// mines `count` valid blocks on it, returning the node and the mined blocks in
// height order (blocks[0] is height 1). Because genesis depends only on the
// funded alloc + timestamp + difficulty (not the miner key), these blocks link
// onto any newNetNode(fa) honest node's genesis.
func mineChain(t *testing.T, fa core.Address, count int) (*node.Node, []core.Block) {
	t.Helper()
	m := newNetNode(t, fa)
	blocks := make([]core.Block, 0, count)
	for i := 0; i < count; i++ {
		b, err := m.MineBlock()
		if err != nil {
			t.Fatalf("mineChain block %d: %v", i, err)
		}
		blocks = append(blocks, b)
	}
	return m, blocks
}

// craftBadPoW returns a copy of valid whose declared Difficulty is bumped to an
// unreachable value WITHOUT re-mining, so the block hash cannot meet the target.
// Parent linkage/height/tx root remain intact, isolating the PoW failure.
func craftBadPoW(valid core.Block) core.Block {
	bad := valid
	bad.Header.Difficulty = 250 // 250 leading zero bits is unreachable -> ErrBadPoW
	return bad
}

// craftBadStateRoot returns a copy of valid with a tampered StateRoot, re-mined
// so PoW passes and ONLY the re-derived state root check can reject it.
func craftBadStateRoot(valid core.Block) core.Block {
	bad := valid
	bad.Header.StateRoot = core.SumHash([]byte("adversary-tampered-state-root"))
	consensus.Mine(&bad.Header, 0) // re-solve PoW for the tampered header
	return bad
}

// makeForgedTx returns a transaction validly signed by one key but whose From is
// overwritten to a different address, so wallet.Verify must reject it.
func makeForgedTx(t *testing.T) core.Transaction {
	t.Helper()
	signer, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("signer key: %v", err)
	}
	victim, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("victim key: %v", err)
	}
	var tx core.Transaction
	tx.To = victim.Address()
	tx.Value = 1
	tx.Nonce = 0
	tx.ChainID = chain.DefaultChainID
	signer.Sign(&tx)          // From=signer, signature over SigningHash(From=signer)
	tx.From = victim.Address() // forge: claim to be the victim
	return tx
}

// floodBlock publishes raw data to the block topic `times` times.
func floodBlock(ctx context.Context, p *P2P, data []byte, times int) {
	for i := 0; i < times; i++ {
		_ = p.block.Publish(ctx, data)
	}
}

// floodTx publishes raw data to the tx topic `times` times.
func floodTx(ctx context.Context, p *P2P, data []byte, times int) {
	for i := 0; i < times; i++ {
		_ = p.tx.Publish(ctx, data)
	}
}

// pollMempoolLen waits until n's mempool holds at least want txs, refloods each
// round to defeat gossip timing, and returns whether it reached the target.
func pollMempoolLen(n *node.Node, want int, reflood func(), deadline time.Time) bool {
	for {
		if n.MempoolLen() >= want {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		if reflood != nil {
			reflood()
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// advSetup builds an honest node + its peer and a malicious peer, connects them,
// and waits for the gossip mesh to form on both topics. It returns the honest
// node, the honest P2P, and the attacker P2P.
func advSetup(t *testing.T, ctx context.Context, fa core.Address) (*node.Node, *P2P, *P2P) {
	t.Helper()
	honest := newNetNode(t, fa)
	atkNode := newNetNode(t, fa)
	hh, ph := startPeer(t, ctx, honest)
	ah, pa := startPeer(t, ctx, atkNode)
	if err := Connect(ctx, hh, ah); err != nil {
		t.Fatalf("connect honest<->attacker: %v", err)
	}
	waitMeshPeers(t, pa, 1, time.Now().Add(10*time.Second))
	waitMeshPeers(t, ph, 1, time.Now().Add(10*time.Second))
	return honest, ph, pa
}

// --- Case 1: invalid-PoW block gossiped ------------------------------------

func TestAdvP2P01InvalidPoWGossiped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	faucet, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("faucet: %v", err)
	}
	fa := faucet.Address()

	honest, _, pa := advSetup(t, ctx, fa)
	genesis := honest.Head()

	_, blocks := mineChain(t, fa, 1)
	valid := blocks[0]
	bad := craftBadPoW(valid)

	// Deterministic: direct acceptance path rejects with the exact sentinel.
	if err := honest.AcceptExternalBlock(bad); !errors.Is(err, chain.ErrBadPoW) {
		t.Fatalf("expected ErrBadPoW, got %v", err)
	}
	if headHash(honest) != genesis.Hash() {
		t.Fatalf("head changed after direct bad-PoW submit")
	}

	// Gossip the bad block repeatedly; head must never advance to it.
	badBytes, err := store.EncodeBlock(bad)
	if err != nil {
		t.Fatalf("encode bad block: %v", err)
	}
	floodBlock(ctx, pa, badBytes, 10)
	time.Sleep(2 * time.Second)
	if headHash(honest) != genesis.Hash() || honest.Head().Header.Height != 0 {
		t.Fatalf("head advanced on gossiped invalid-PoW block: height=%d", honest.Head().Header.Height)
	}

	// Liveness: a subsequent VALID block IS accepted over the network.
	validBytes, err := store.EncodeBlock(valid)
	if err != nil {
		t.Fatalf("encode valid block: %v", err)
	}
	if !pollConverged(valid, []*node.Node{honest},
		func() { floodBlock(ctx, pa, validBytes, 1) },
		time.Now().Add(30*time.Second)) {
		t.Fatalf("honest node did not accept subsequent valid block (height=%d)", honest.Head().Header.Height)
	}
}

// --- Case 2: forged-signature tx gossiped ----------------------------------

func TestAdvP2P02ForgedSignatureTxGossiped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	faucet, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("faucet: %v", err)
	}
	fa := faucet.Address()

	honest, _, pa := advSetup(t, ctx, fa)

	forged := makeForgedTx(t)

	// Deterministic: SubmitTx rejects the forged tx.
	if err := honest.SubmitTx(forged); !errors.Is(err, node.ErrBadSignature) {
		t.Fatalf("expected ErrBadSignature, got %v", err)
	}
	if honest.MempoolLen() != 0 {
		t.Fatalf("forged tx entered mempool via SubmitTx")
	}

	// Gossip the forged tx repeatedly; it must never be admitted.
	forgedBytes, err := encodeTx(forged)
	if err != nil {
		t.Fatalf("encode forged tx: %v", err)
	}
	floodTx(ctx, pa, forgedBytes, 10)
	time.Sleep(2 * time.Second)
	if honest.MempoolLen() != 0 {
		t.Fatalf("forged tx admitted via gossip: mempool=%d", honest.MempoolLen())
	}

	// Liveness: a VALID tx delivered over the same topic IS admitted (proves the
	// tx read loop survived the forged payload).
	var vtx core.Transaction
	recipient, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("recipient: %v", err)
	}
	vtx.To = recipient.Address()
	vtx.Value = 100
	vtx.Nonce = honest.Nonce(fa)
	vtx.ChainID = chain.DefaultChainID
	faucet.Sign(&vtx)
	vtxBytes, err := encodeTx(vtx)
	if err != nil {
		t.Fatalf("encode valid tx: %v", err)
	}
	if !pollMempoolLen(honest, 1, func() { floodTx(ctx, pa, vtxBytes, 1) }, time.Now().Add(30*time.Second)) {
		t.Fatalf("honest node did not admit subsequent valid tx")
	}

	// The only mined tx must be the valid one; no forged tx ever entered a block.
	blk, err := honest.MineBlock()
	if err != nil {
		t.Fatalf("mine: %v", err)
	}
	if len(blk.Txs) != 1 {
		t.Fatalf("mined block tx count = %d, want 1", len(blk.Txs))
	}
	if blk.Txs[0].Hash() != vtx.Hash() {
		t.Fatalf("mined tx is not the valid tx")
	}
}

// --- Case 3: tampered-StateRoot block gossiped -----------------------------

func TestAdvP2P03TamperedStateRootGossiped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	faucet, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("faucet: %v", err)
	}
	fa := faucet.Address()

	honest, _, pa := advSetup(t, ctx, fa)
	genesis := honest.Head()

	_, blocks := mineChain(t, fa, 1)
	valid := blocks[0]
	bad := craftBadStateRoot(valid)

	if err := honest.AcceptExternalBlock(bad); !errors.Is(err, chain.ErrBadStateRoot) {
		t.Fatalf("expected ErrBadStateRoot, got %v", err)
	}
	if headHash(honest) != genesis.Hash() {
		t.Fatalf("head changed after direct tampered-state-root submit")
	}

	badBytes, err := store.EncodeBlock(bad)
	if err != nil {
		t.Fatalf("encode bad block: %v", err)
	}
	floodBlock(ctx, pa, badBytes, 10)
	time.Sleep(2 * time.Second)
	if headHash(honest) != genesis.Hash() || honest.Head().Header.Height != 0 {
		t.Fatalf("head advanced on gossiped tampered-state-root block: height=%d", honest.Head().Header.Height)
	}

	// Liveness: valid block still accepted.
	validBytes, err := store.EncodeBlock(valid)
	if err != nil {
		t.Fatalf("encode valid block: %v", err)
	}
	if !pollConverged(valid, []*node.Node{honest},
		func() { floodBlock(ctx, pa, validBytes, 1) },
		time.Now().Add(30*time.Second)) {
		t.Fatalf("honest node did not accept subsequent valid block")
	}
}

// --- Case 4: malicious sync responder --------------------------------------

func TestAdvP2P04MaliciousSyncResponder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	faucet, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("faucet: %v", err)
	}
	fa := faucet.Address()

	honest := newNetNode(t, fa)
	hh, ph := startPeer(t, ctx, honest)
	genesis := honest.Head()

	// newMalPeer builds a bare libp2p host serving the given SyncProtocol
	// handler, connects the honest host to it, and returns its peer id.
	newMalPeer := func(handler network.StreamHandler) peer.ID {
		mh, err := NewHost(ctx, 0)
		if err != nil {
			t.Fatalf("mal host: %v", err)
		}
		t.Cleanup(func() { _ = mh.Close() })
		mh.SetStreamHandler(SyncProtocol, handler)
		if err := Connect(ctx, hh, mh); err != nil {
			t.Fatalf("connect honest<->mal: %v", err)
		}
		return mh.ID()
	}

	// (a) Garbage stream: responder writes non-gob bytes. The honest node must
	// not crash and must not adopt anything; Head stays at genesis.
	malA := newMalPeer(func(s network.Stream) {
		defer func() { _ = s.Close() }()
		var start uint64
		_ = binary.Read(s, binary.BigEndian, &start)
		_, _ = s.Write([]byte("\x00\xffGARBAGE-not-a-gob-stream\x99\x01\x02\x03\x04"))
	})
	_ = ph.Resync(ctx, malA, honest) // err may be nil or non-nil
	if headHash(honest) != genesis.Hash() || honest.Head().Header.Height != 0 {
		t.Fatalf("head changed after garbage sync response: height=%d", honest.Head().Header.Height)
	}

	// (b) Valid gob but an invalid (bad-PoW) block: apply must fail and abort.
	_, one := mineChain(t, fa, 1)
	badPoW := craftBadPoW(one[0])
	malB := newMalPeer(func(s network.Stream) {
		defer func() { _ = s.Close() }()
		var start uint64
		_ = binary.Read(s, binary.BigEndian, &start)
		_ = gob.NewEncoder(s).Encode(&badPoW)
	})
	if err := ph.Resync(ctx, malB, honest); err == nil {
		t.Fatalf("expected error adopting invalid synced block, got nil")
	}
	if headHash(honest) != genesis.Hash() || honest.Head().Header.Height != 0 {
		t.Fatalf("head changed after invalid-block sync response: height=%d", honest.Head().Header.Height)
	}

	// (c) Out-of-order / unknown-parent block: a height-2 block whose parent the
	// honest node lacks. Apply must fail with a linkage error and abort.
	_, two := mineChain(t, fa, 2)
	orphan := two[1] // height 2, parent (height 1) unknown to honest
	malC := newMalPeer(func(s network.Stream) {
		defer func() { _ = s.Close() }()
		var start uint64
		_ = binary.Read(s, binary.BigEndian, &start)
		_ = gob.NewEncoder(s).Encode(&orphan)
	})
	if err := ph.Resync(ctx, malC, honest); err == nil {
		t.Fatalf("expected error adopting out-of-order synced block, got nil")
	}
	if headHash(honest) != genesis.Hash() || honest.Head().Header.Height != 0 {
		t.Fatalf("head changed after out-of-order sync response: height=%d", honest.Head().Header.Height)
	}
}

// --- Case 5: duplicate / replay gossip flood -------------------------------

func TestAdvP2P05DuplicateReplayFlood(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	faucet, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("faucet: %v", err)
	}
	fa := faucet.Address()

	honest, _, pa := advSetup(t, ctx, fa)

	_, blocks := mineChain(t, fa, 1)
	valid := blocks[0]
	coinbase := valid.Header.Coinbase

	// First acceptance advances the head.
	if err := honest.AcceptExternalBlock(valid); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	if honest.Head().Header.Height != 1 {
		t.Fatalf("head height = %d, want 1", honest.Head().Header.Height)
	}

	// Replaying the SAME block many times is idempotent (nil, no double-apply).
	for i := 0; i < 50; i++ {
		if err := honest.AcceptExternalBlock(valid); err != nil {
			t.Fatalf("duplicate accept %d not idempotent: %v", i, err)
		}
	}
	if headHash(honest) != valid.Hash() || honest.Head().Header.Height != 1 {
		t.Fatalf("head diverged after replay: height=%d", honest.Head().Header.Height)
	}
	// The coinbase reward must have been credited EXACTLY once.
	if got := honest.Balance(coinbase); got != chain.BlockReward {
		t.Fatalf("coinbase balance = %d, want %d (reward double-applied)", got, chain.BlockReward)
	}

	// Gossip flood of the same block: still idempotent, no panic, head correct.
	validBytes, err := store.EncodeBlock(valid)
	if err != nil {
		t.Fatalf("encode block: %v", err)
	}
	floodBlock(ctx, pa, validBytes, 50)
	time.Sleep(2 * time.Second)
	if headHash(honest) != valid.Hash() || honest.Head().Header.Height != 1 {
		t.Fatalf("head diverged after gossip flood: height=%d", honest.Head().Header.Height)
	}
	if got := honest.Balance(coinbase); got != chain.BlockReward {
		t.Fatalf("coinbase balance after flood = %d, want %d", got, chain.BlockReward)
	}
}

// --- Case 6: unknown-parent orphan gossip ----------------------------------

func TestAdvP2P06UnknownParentOrphanGossip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	faucet, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("faucet: %v", err)
	}
	fa := faucet.Address()

	honest, _, pa := advSetup(t, ctx, fa)
	genesis := honest.Head()

	_, chain2 := mineChain(t, fa, 2)
	b1, b2 := chain2[0], chain2[1]

	// Deterministic: an orphan (height 2, parent unknown) is rejected outright.
	if err := honest.AcceptExternalBlock(b2); !errors.Is(err, chain.ErrUnknownParent) {
		t.Fatalf("expected ErrUnknownParent, got %v", err)
	}
	if headHash(honest) != genesis.Hash() {
		t.Fatalf("head changed after orphan submit")
	}

	// Gossip ONLY the orphan. The relaying attacker cannot serve the missing
	// parent (its own chain is at genesis), so the honest node must NOT apply
	// the orphan out of order; Head stays at genesis.
	b2Bytes, err := store.EncodeBlock(b2)
	if err != nil {
		t.Fatalf("encode b2: %v", err)
	}
	floodBlock(ctx, pa, b2Bytes, 5)
	time.Sleep(3 * time.Second)
	if honest.Head().Header.Height != 0 {
		t.Fatalf("orphan applied out of order: height=%d", honest.Head().Header.Height)
	}

	// Liveness/ordered recovery: once the parent is also delivered, the node
	// converges to height 2 in the correct order.
	b1Bytes, err := store.EncodeBlock(b1)
	if err != nil {
		t.Fatalf("encode b1: %v", err)
	}
	if !pollConverged(b2, []*node.Node{honest},
		func() { floodBlock(ctx, pa, b1Bytes, 1); floodBlock(ctx, pa, b2Bytes, 1) },
		time.Now().Add(30*time.Second)) {
		t.Fatalf("honest node did not converge to height 2 after ordered delivery (height=%d)", honest.Head().Header.Height)
	}
}

// --- Case 7: junk topic payload --------------------------------------------

func TestAdvP2P07JunkTopicPayload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	faucet, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("faucet: %v", err)
	}
	fa := faucet.Address()

	honest, _, pa := advSetup(t, ctx, fa)
	genesis := honest.Head()

	junks := [][]byte{
		{0xDE, 0xAD, 0xBE, 0xEF},
		[]byte("total garbage not a block or tx \x00\x01\x02"),
		{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
		[]byte(""),
	}
	for _, j := range junks {
		floodBlock(ctx, pa, j, 5)
		floodTx(ctx, pa, j, 5)
	}
	time.Sleep(2 * time.Second)

	// Decode failures must be logged+dropped: no state change, no crash.
	if headHash(honest) != genesis.Hash() || honest.Head().Header.Height != 0 {
		t.Fatalf("head advanced on junk payload: height=%d", honest.Head().Header.Height)
	}
	if honest.MempoolLen() != 0 {
		t.Fatalf("junk tx entered mempool: %d", honest.MempoolLen())
	}

	// Liveness: the read loops survived the junk and still accept a valid block.
	_, blocks := mineChain(t, fa, 1)
	valid := blocks[0]
	validBytes, err := store.EncodeBlock(valid)
	if err != nil {
		t.Fatalf("encode valid block: %v", err)
	}
	if !pollConverged(valid, []*node.Node{honest},
		func() { floodBlock(ctx, pa, validBytes, 1) },
		time.Now().Add(30*time.Second)) {
		t.Fatalf("block read loop died after junk: valid block not accepted")
	}
}

// --- Case 8: eclipse attempt -- many sybil peer connections -----------------
//
// Genuinely new ground, not overlapping cases 1-7: those all use exactly one
// attacker peer connected to one honest node. Before this session, p2p/host.go
// had no libp2p.ConnectionManager, ConnectionGater, or peer-scoring
// configured at all -- libp2p's own default is UNBOUNDED inbound
// connections, so an attacker surrounding a victim with many sybil
// identities (dominating its connection slots / GossipSub mesh, the actual
// eclipse-attack mechanism) had no defense to run into. NewHost now attaches
// a real connmgr.BasicConnMgr (connLow/connHigh in host.go); this proves it
// actually bounds the connection count and that the node stays functional
// -- specifically, still able to reach a genuine peer -- afterward.

func TestAdvP2P08EclipseAttemptManySybilPeers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	faucet, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("faucet: %v", err)
	}
	fa := faucet.Address()

	honest := newNetNode(t, fa)
	hh, ph := startPeer(t, ctx, honest)

	// Each sybil is a DISTINCT libp2p identity dialing the honest host --
	// the actual shape of an eclipse attempt. One attacker reconnecting many
	// times would not exercise this: libp2p dedupes connections by peer ID,
	// so that is just one connection, not many.
	const sybilCount = connHigh + 40
	for i := 0; i < sybilCount; i++ {
		sh, err := NewHost(ctx, 0)
		if err != nil {
			t.Fatalf("sybil host %d: %v", i, err)
		}
		t.Cleanup(func() { _ = sh.Close() })
		if err := Connect(ctx, sh, hh); err != nil {
			// A dial racing the connection manager's own trimming can
			// legitimately fail once the honest side is already at
			// capacity -- that is the defense working, not a test failure.
			continue
		}
	}

	// The connection manager trims down toward its high watermark on its
	// own background schedule (a silence period, ~10s by default, plus a
	// grace period before any single connection becomes eligible) -- poll
	// rather than assert immediately.
	deadline := time.Now().Add(90 * time.Second)
	var finalConns int
	for {
		finalConns = len(hh.Network().Conns())
		if finalConns <= connHigh {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("connection count never came down to the high watermark: %d connections (limit %d) after %d sybil attempts",
				finalConns, connHigh, sybilCount)
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("connections bounded at %d (limit %d) after %d sybil attempts", finalConns, connHigh, sybilCount)

	// Liveness: the honest node must still be able to reach and converge
	// with a genuinely honest peer after the attempted eclipse -- not just
	// "still running", but still able to do the one thing an eclipse
	// attack is specifically meant to deny.
	friend := newNetNode(t, fa)
	fh, pf := startPeer(t, ctx, friend)
	if err := Connect(ctx, hh, fh); err != nil {
		t.Fatalf("connect honest<->friend after eclipse attempt: %v", err)
	}
	waitMeshPeers(t, ph, 1, time.Now().Add(20*time.Second))
	waitMeshPeers(t, pf, 1, time.Now().Add(20*time.Second))

	blk, err := friend.MineBlock()
	if err != nil {
		t.Fatalf("friend mine: %v", err)
	}
	blkBytes, err := store.EncodeBlock(blk)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !pollConverged(blk, []*node.Node{honest},
		func() { floodBlock(ctx, pf, blkBytes, 1) },
		time.Now().Add(30*time.Second)) {
		t.Fatalf("honest node did not converge with a genuine peer after the eclipse attempt (height=%d)", honest.Head().Header.Height)
	}
}

// --- Case 9: inbound sync-stream flood --------------------------------------
//
// p2p/sync.go's maxConcurrentSyncs only ever bounded this node's own
// OUTBOUND catch-up requests; nothing capped how many peers could
// simultaneously open an inbound SyncProtocol stream against this node as
// RESPONDER. This proves the fix (inboundSyncSem) actually rejects excess
// concurrent streams rather than accepting all of them: many sybil peers
// each open a stream and hold it open (never sending the 8-byte start
// height the responder is waiting to read, never closing it) -- streams the
// cap rejects should be closed by the honest side almost immediately
// (fast read-return, well under our own read deadline); streams within the
// cap stay genuinely open (read only returns once OUR deadline expires).
func TestAdvP2P09InboundSyncStreamFlood(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	faucet, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("faucet: %v", err)
	}
	fa := faucet.Address()

	honest := newNetNode(t, fa)
	hh, ph := startPeer(t, ctx, honest)

	const floodCount = maxConcurrentInboundSyncs * 5
	var mu sync.Mutex
	streams := make([]network.Stream, 0, floodCount)
	var wg sync.WaitGroup
	for i := 0; i < floodCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sh, err := NewHost(ctx, 0)
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = sh.Close() })
			if err := Connect(ctx, sh, hh); err != nil {
				return
			}
			s, err := sh.NewStream(ctx, hh.ID(), SyncProtocol)
			if err != nil {
				return
			}
			mu.Lock()
			streams = append(streams, s)
			mu.Unlock()
		}()
	}
	wg.Wait()
	t.Logf("opened %d/%d flood streams", len(streams), floodCount)

	// Classify each stream by WHICH KIND of error Read returns, not by how
	// fast it returns: a latency-window heuristic (e.g. "returned in under
	// 250ms") is a race against CI scheduling jitter -- a loaded runner can
	// delay the honest side's handler goroutine from even reaching its
	// s.Reset() call past the window, misclassifying a genuinely-rejected
	// stream as held-open. Checking the error type instead is exact: a
	// stream the remote already Reset() returns some non-timeout error
	// (often immediately, but the exact latency doesn't matter); a stream
	// still genuinely held open by an accepted handler only ever returns
	// once OUR OWN read deadline fires, which is always a net.Error with
	// Timeout() true. All streams are checked concurrently so the total
	// wall time is one readDeadline, not floodCount of them.
	const readDeadline = 3 * time.Second
	rejected := 0
	var mu2 sync.Mutex
	var wg2 sync.WaitGroup
	for _, s := range streams {
		wg2.Add(1)
		go func(s network.Stream) {
			defer wg2.Done()
			_ = s.SetReadDeadline(time.Now().Add(readDeadline))
			buf := make([]byte, 1)
			_, err := s.Read(buf)
			timedOut := false
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				timedOut = true
			}
			if !timedOut {
				mu2.Lock()
				rejected++
				mu2.Unlock()
			}
			_ = s.Reset()
		}(s)
	}
	wg2.Wait()
	wantRejectedAtLeast := len(streams) - maxConcurrentInboundSyncs
	t.Logf("%d/%d flood streams rejected (cap %d)", rejected, len(streams), maxConcurrentInboundSyncs)
	if wantRejectedAtLeast > 0 && rejected < wantRejectedAtLeast {
		t.Fatalf("expected at least %d of %d flood streams to be rejected by the inbound sync cap (%d), got %d",
			wantRejectedAtLeast, len(streams), maxConcurrentInboundSyncs, rejected)
	}

	// Liveness: the honest node still meshes with and converges from a
	// genuinely new peer while the flood is ongoing.
	friend := newNetNode(t, fa)
	fh, pf := startPeer(t, ctx, friend)
	if err := Connect(ctx, hh, fh); err != nil {
		t.Fatalf("connect honest<->friend during flood: %v", err)
	}
	waitMeshPeers(t, ph, 1, time.Now().Add(20*time.Second))
	waitMeshPeers(t, pf, 1, time.Now().Add(20*time.Second))

	fblk, err := friend.MineBlock()
	if err != nil {
		t.Fatalf("friend mine: %v", err)
	}
	fblkBytes, err := store.EncodeBlock(fblk)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !pollConverged(fblk, []*node.Node{honest},
		func() { floodBlock(ctx, pf, fblkBytes, 1) },
		time.Now().Add(30*time.Second)) {
		t.Fatalf("honest node did not converge with a new peer during the inbound sync flood (height=%d)", honest.Head().Header.Height)
	}
}
