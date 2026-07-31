package p2p

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"

	dht "github.com/libp2p/go-libp2p-kad-dht"
)

// DefaultDHTRendezvous is the namespace l1chain nodes advertise/search under
// when using DHT-based discovery.
const DefaultDHTRendezvous = "l1chain-dht"

// dhtProtocolPrefix isolates l1chain's DHT overlay from the public IPFS
// Amino DHT: without a distinct prefix, a node given a real-internet
// bootstrap peer would speak the same wire protocol as the public IPFS DHT
// and could route into it instead of staying confined to other l1chain
// nodes.
const dhtProtocolPrefix = protocol.ID("/l1chain")

// EnableDHTDiscovery starts a Kademlia DHT on h (server mode) and returns a
// handle whose Close stops it. It advertises h under rendezvous and runs a
// background loop that periodically looks up other peers advertising the
// same rendezvous and dials any not already connected -- the non-LAN
// counterpart to EnableMDNS (p2p/mdns.go): same shape (advertise + discover
// + auto-dial), a different bootstrap mechanism for hosts that are already
// mutually TCP-dialable but don't share an mDNS-visible network segment.
// bootstrapPeers seeds the DHT's routing table; at least one live peer is
// required for discovery to find anything beyond an empty table. The loop
// runs until ctx is cancelled or Close is called.
func EnableDHTDiscovery(ctx context.Context, h host.Host, bootstrapPeers []peer.AddrInfo, rendezvous string) (io.Closer, error) {
	kad, err := dht.New(ctx, h, dht.Mode(dht.ModeServer), dht.ProtocolPrefix(dhtProtocolPrefix))
	if err != nil {
		return nil, fmt.Errorf("p2p: new dht: %w", err)
	}
	if err := kad.Bootstrap(ctx); err != nil {
		_ = kad.Close()
		return nil, fmt.Errorf("p2p: dht bootstrap: %w", err)
	}
	for _, pi := range bootstrapPeers {
		if pi.ID == h.ID() {
			continue
		}
		// Best-effort: one unreachable bootstrap peer must not stop the
		// others from seeding the routing table.
		_ = h.Connect(ctx, pi)
	}

	disc := drouting.NewRoutingDiscovery(kad)
	if _, err := disc.Advertise(ctx, rendezvous); err != nil {
		_ = kad.Close()
		return nil, fmt.Errorf("p2p: dht advertise: %w", err)
	}

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			found, err := disc.FindPeers(ctx, rendezvous)
			if err != nil {
				continue
			}
			for pi := range found {
				if pi.ID == h.ID() {
					continue
				}
				if h.Network().Connectedness(pi.ID) == network.Connected {
					continue
				}
				_ = h.Connect(ctx, pi)
			}
		}
	}()

	return kad, nil
}
