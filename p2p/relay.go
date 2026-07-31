package p2p

import (
	"context"
	"fmt"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	relayclient "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"

	ma "github.com/multiformats/go-multiaddr"
)

// EnableRelayService turns h into a circuit-relay-v2 relay: other peers can
// reserve a slot on it and be reached through it without needing a directly
// dialable address of their own. Client-side circuit-v2 support (the ability
// to DIAL through a relay) is already on by default for every host
// NewHostWithConfig builds -- libp2p.EnableRelay() is enabled by default in
// libp2p.New (see go-libp2p's own options.go) -- so this is only needed on
// the node that chooses to BE a relay. The returned *relay.Relay must be
// Closed by the caller.
func EnableRelayService(h host.Host) (*relayv2.Relay, error) {
	r, err := relayv2.New(h)
	if err != nil {
		return nil, fmt.Errorf("p2p: enable relay service: %w", err)
	}
	return r, nil
}

// ReserveRelaySlot asks relay to reserve a slot for h, so a peer that never
// learns h's own direct address can still reach h at
// CircuitAddr(relay.ID, h.ID()). Must be called (and kept renewed by the
// underlying reservation's own refresh, handled by go-libp2p) before any
// peer dials that circuit address.
func ReserveRelaySlot(ctx context.Context, h host.Host, relay peer.AddrInfo) error {
	if _, err := relayclient.Reserve(ctx, h, relay); err != nil {
		return fmt.Errorf("p2p: reserve relay slot on %s: %w", relay.ID, err)
	}
	return nil
}

// CircuitAddr builds the /p2p/<relayID>/p2p-circuit/p2p/<targetID> multiaddr
// a peer dials (via ConnectAddr) to reach targetID through relayID, without
// ever learning targetID's own direct address.
func CircuitAddr(relayID, targetID peer.ID) (ma.Multiaddr, error) {
	m, err := ma.NewMultiaddr(fmt.Sprintf("/p2p/%s/p2p-circuit/p2p/%s", relayID, targetID))
	if err != nil {
		return nil, fmt.Errorf("p2p: build circuit multiaddr: %w", err)
	}
	return m, nil
}
