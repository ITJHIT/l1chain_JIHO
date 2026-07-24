package p2p

import (
	"context"
	"testing"
	"time"

	"l1chain/core"
	"l1chain/node"
	"l1chain/wallet"

	"github.com/libp2p/go-libp2p/core/host"
)

const testDifficulty = 7

// fixedGenesisTS pins the genesis timestamp so independently constructed nodes
// share an identical genesis block (and hash), the precondition for any block
// mined on one node to link on the others.
const fixedGenesisTS int64 = 1_700_000_000

// newNetNode builds an in-memory node with a deterministic genesis shared by
// every node in the test network.
func newNetNode(t *testing.T, faucet core.Address) *node.Node {
	t.Helper()
	k, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	n, err := node.New(node.Config{
		MinerKey:         k,
		Difficulty:       testDifficulty,
		GenesisAlloc:     map[core.Address]uint64{faucet: 1_000_000},
		GenesisTimestamp: fixedGenesisTS,
	})
	if err != nil {
		t.Fatalf("node.New: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

// headHash returns the head block hash of n (Head() returns a value, whose
// Hash() has a pointer receiver, so it must be bound to a variable first).
func headHash(n *node.Node) core.Hash {
	b := n.Head()
	return b.Hash()
}

// startPeer spins up a real libp2p host + gossip for n and registers cleanup.
func startPeer(t *testing.T, ctx context.Context, n *node.Node) (host.Host, *P2P) {
	t.Helper()
	h, err := NewHost(ctx, 0)
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	p, err := NewP2P(ctx, h)
	if err != nil {
		_ = h.Close()
		t.Fatalf("NewP2P: %v", err)
	}
	if err := Wire(ctx, n, p); err != nil {
		_ = h.Close()
		t.Fatalf("Wire: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h, p
}

// pollConverged waits until every node's head matches want (hash + height) or
// the deadline passes. announce is invoked each round to (re)broadcast so
// GossipSub delivery is guaranteed once the mesh forms; a single delivered
// block triggers a full sync of any missing prefix.
func pollConverged(want core.Block, nodes []*node.Node, announce func(), deadline time.Time) bool {
	for {
		all := true
		for _, n := range nodes {
			h := n.Head()
			if h.Hash() != want.Hash() || h.Header.Height != want.Header.Height {
				all = false
				break
			}
		}
		if all {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		if announce != nil {
			announce()
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func TestThreeNodeConvergence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	faucet, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("faucet key: %v", err)
	}
	fa := faucet.Address()

	n1 := newNetNode(t, fa)
	n2 := newNetNode(t, fa)
	n3 := newNetNode(t, fa)

	if headHash(n1) != headHash(n2) || headHash(n2) != headHash(n3) {
		t.Fatalf("genesis hashes diverge: %x %x %x",
			headHash(n1), headHash(n2), headHash(n3))
	}

	h1, p1 := startPeer(t, ctx, n1)
	h2, _ := startPeer(t, ctx, n2)
	h3, _ := startPeer(t, ctx, n3)

	// Topology: 1 <-> 2 <-> 3 (node3 only reachable through node2).
	if err := Connect(ctx, h1, h2); err != nil {
		t.Fatalf("connect 1-2: %v", err)
	}
	if err := Connect(ctx, h2, h3); err != nil {
		t.Fatalf("connect 2-3: %v", err)
	}

	// Give GossipSub a moment to exchange subscriptions / form the mesh.
	waitMeshPeers(t, p1, 1, time.Now().Add(10*time.Second))

	// Mine several blocks on node1 and announce each.
	const nBlocks = 5
	for i := 0; i < nBlocks; i++ {
		b, err := n1.MineBlock()
		if err != nil {
			t.Fatalf("mine block %d: %v", i, err)
		}
		if err := p1.AnnounceBlock(b); err != nil {
			t.Fatalf("announce block %d: %v", i, err)
		}
	}

	head := n1.Head()
	if head.Header.Height != nBlocks {
		t.Fatalf("node1 head height = %d, want %d", head.Header.Height, nBlocks)
	}

	ok := pollConverged(head, []*node.Node{n2, n3},
		func() { _ = p1.AnnounceBlock(n1.Head()) },
		time.Now().Add(40*time.Second))
	if !ok {
		t.Fatalf("nodes did not converge: n1=%d n2=%d n3=%d (head %x)",
			n1.Head().Header.Height, n2.Head().Header.Height, n3.Head().Header.Height,
			head.Hash())
	}

	// Explicit re-assert on the winning invariant.
	if headHash(n2) != head.Hash() || headHash(n3) != head.Hash() {
		t.Fatalf("head mismatch after converge")
	}
}

func TestPartitionReconvergence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	faucet, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("faucet key: %v", err)
	}
	fa := faucet.Address()

	n1 := newNetNode(t, fa)
	n2 := newNetNode(t, fa)
	n3 := newNetNode(t, fa)

	h1, p1 := startPeer(t, ctx, n1)
	h2, _ := startPeer(t, ctx, n2)
	h3, p3 := startPeer(t, ctx, n3)

	if err := Connect(ctx, h1, h2); err != nil {
		t.Fatalf("connect 1-2: %v", err)
	}
	if err := Connect(ctx, h2, h3); err != nil {
		t.Fatalf("connect 2-3: %v", err)
	}
	waitMeshPeers(t, p1, 1, time.Now().Add(10*time.Second))

	// Phase 1: converge all three.
	const initial = 3
	for i := 0; i < initial; i++ {
		b, err := n1.MineBlock()
		if err != nil {
			t.Fatalf("mine %d: %v", i, err)
		}
		_ = p1.AnnounceBlock(b)
	}
	converged := n1.Head()
	if !pollConverged(converged, []*node.Node{n2, n3},
		func() { _ = p1.AnnounceBlock(n1.Head()) },
		time.Now().Add(40*time.Second)) {
		t.Fatalf("initial convergence failed: n2=%d n3=%d",
			n2.Head().Header.Height, n3.Head().Header.Height)
	}
	behindHeight := n3.Head().Header.Height // == initial

	// Phase 2: PARTITION node3 (drop the only link it has, 2<->3).
	if err := h2.Network().ClosePeer(h3.ID()); err != nil {
		t.Fatalf("close 2->3: %v", err)
	}
	if err := h3.Network().ClosePeer(h2.ID()); err != nil {
		t.Fatalf("close 3->2: %v", err)
	}
	// Confirm the connection is actually gone before relying on the partition.
	if !pollNoConn(h2, h3.ID(), time.Now().Add(5*time.Second)) {
		t.Fatalf("node3 not partitioned: still connected")
	}

	// Mine extra blocks on node1; they must reach node2 but not node3.
	const extra = 4
	for i := 0; i < extra; i++ {
		b, err := n1.MineBlock()
		if err != nil {
			t.Fatalf("mine extra %d: %v", i, err)
		}
		_ = p1.AnnounceBlock(b)
	}
	newHead := n1.Head()

	// node2 catches up to the new head.
	if !pollConverged(newHead, []*node.Node{n2},
		func() { _ = p1.AnnounceBlock(n1.Head()) },
		time.Now().Add(40*time.Second)) {
		t.Fatalf("node2 did not receive post-partition blocks: n2=%d want %d",
			n2.Head().Header.Height, newHead.Header.Height)
	}

	// node3 must have fallen behind (still at the pre-partition height).
	if got := n3.Head().Header.Height; got != behindHeight {
		t.Fatalf("node3 unexpectedly advanced during partition: got %d want %d",
			got, behindHeight)
	}
	if n3.Head().Header.Height >= newHead.Header.Height {
		t.Fatalf("node3 not behind: n3=%d n1=%d",
			n3.Head().Header.Height, newHead.Header.Height)
	}

	// Phase 3: RECONNECT node3 and trigger sync.
	if err := Connect(ctx, h3, h2); err != nil {
		t.Fatalf("reconnect 3-2: %v", err)
	}
	if err := p3.Resync(ctx, h2.ID(), n3); err != nil {
		t.Fatalf("resync node3: %v", err)
	}

	// node3 re-converges to the same head.
	if !pollConverged(newHead, []*node.Node{n3}, nil,
		time.Now().Add(40*time.Second)) {
		t.Fatalf("node3 did not re-converge: n3=%d want %d (%x)",
			n3.Head().Header.Height, newHead.Header.Height, newHead.Hash())
	}
	if headHash(n3) != newHead.Hash() {
		t.Fatalf("node3 head hash mismatch after re-convergence")
	}
}

// waitMeshPeers polls until p's block topic has at least min peers or deadline.
func waitMeshPeers(t *testing.T, p *P2P, min int, deadline time.Time) {
	t.Helper()
	for time.Now().Before(deadline) {
		if len(p.block.ListPeers()) >= min {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Not fatal: gossip may still deliver; convergence poll is the real gate.
}

// pollNoConn waits until h has no live connection to id.
func pollNoConn(h host.Host, id interface{ String() string }, deadline time.Time) bool {
	for {
		connected := false
		for _, c := range h.Network().Conns() {
			if c.RemotePeer().String() == id.String() {
				connected = true
				break
			}
		}
		if !connected {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}
