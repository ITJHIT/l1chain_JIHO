package p2p

import (
	"context"
	"testing"
	"time"

	"l1chain/node"
	"l1chain/wallet"

	"github.com/libp2p/go-libp2p/core/network"
)

// TestCircuitRelayConnectsTwoNodesNeverDirectlyDialed proves genuine
// circuit-relay-v2 connectivity between two real l1chain nodes that are
// never directly connected to each other -- the same technique go-libp2p's
// own strongest relay test (circuitv2/relay/relay_test.go's TestBasicRelay)
// uses, applied to this repo's own node/host/gossip stack rather than bare
// libp2p hosts, and extended past it: after the raw relayed connection is
// proven, a block mined on target is checked for gossip convergence to
// dialer through that same relay.
//
// This is NOT a NAT-traversal test -- see the p2p "still deferred" README
// section for exactly what is and is not claimed. It proves the relay
// protocol itself works over real TCP, with zero new dependencies.
func TestCircuitRelayConnectsTwoNodesNeverDirectlyDialed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	faucet, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("faucet: %v", err)
	}
	fa := faucet.Address()

	target := newNetNode(t, fa)
	relayNode := newNetNode(t, fa)
	dialer := newNetNode(t, fa)

	targetHost, targetP2P := startPeer(t, ctx, target)
	relayHost, _ := startPeer(t, ctx, relayNode)
	dialerHost, _ := startPeer(t, ctx, dialer)

	if _, err := EnableRelayService(relayHost); err != nil {
		t.Fatalf("EnableRelayService: %v", err)
	}

	// target <-> relay <-> dialer. target and dialer are NEVER connected to
	// each other directly -- the only way dialer can reach target is through
	// the circuit relay.
	if err := Connect(ctx, targetHost, relayHost); err != nil {
		t.Fatalf("connect target->relay: %v", err)
	}
	if err := Connect(ctx, relayHost, dialerHost); err != nil {
		t.Fatalf("connect relay->dialer: %v", err)
	}

	relayInfo := relayHost.Peerstore().PeerInfo(relayHost.ID())
	if err := ReserveRelaySlot(ctx, targetHost, relayInfo); err != nil {
		t.Fatalf("ReserveRelaySlot: %v", err)
	}

	circuit, err := CircuitAddr(relayHost.ID(), targetHost.ID())
	if err != nil {
		t.Fatalf("CircuitAddr: %v", err)
	}
	if err := ConnectAddr(ctx, dialerHost, circuit.String()); err != nil {
		t.Fatalf("ConnectAddr(circuit): %v", err)
	}

	// The raw relayed connection: dialer must see target as Limited
	// (relayed, not direct) connectivity, exactly what TestBasicRelay itself
	// asserts.
	if got := dialerHost.Network().Connectedness(targetHost.ID()); got != network.Limited {
		t.Fatalf("dialer<->target connectedness = %v, want Limited (relayed)", got)
	}
	conns := dialerHost.Network().ConnsToPeer(targetHost.ID())
	if len(conns) != 1 {
		t.Fatalf("dialer has %d connections to target, want exactly 1", len(conns))
	}
	if !conns[0].Stat().Limited {
		t.Fatal("dialer<->target connection is not marked Limited")
	}

	// Going further than upstream's own test: real chain data (a gossiped,
	// PoW-validated block) crossing the relay circuit, not just raw bytes.
	blk, err := target.MineBlock()
	if err != nil {
		t.Fatalf("target.MineBlock: %v", err)
	}
	if !pollConverged(blk, []*node.Node{dialer},
		func() { _ = targetP2P.AnnounceBlock(blk) },
		time.Now().Add(30*time.Second)) {
		t.Fatalf("dialer did not converge to a block gossiped through the relay circuit (height=%d)", dialer.Head().Header.Height)
	}
}
