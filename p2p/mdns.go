package p2p

// mDNS LAN peer auto-discovery. When enabled, a node advertises itself and
// listens for other l1chain nodes on the local network segment under a shared
// service tag, dialing any peer it discovers. This lets nodes on the same LAN
// mesh without any explicit --peers multiaddrs.

import (
	"context"
	"fmt"
	"log"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
)

// DefaultMDNSTag is the service tag l1chain nodes rendezvous on for LAN
// discovery. Nodes must share the same tag to find each other.
const DefaultMDNSTag = "l1chain-mdns"

// mdnsNotifee reacts to mDNS peer discoveries by dialing the found peer so a
// libp2p connection (and thus a gossip mesh) forms automatically.
type mdnsNotifee struct {
	ctx  context.Context
	host host.Host
}

// HandlePeerFound is invoked by the mDNS service for every peer advertised on
// the LAN under the same service tag. It dials the peer; self-discoveries and
// already-connected peers are skipped/harmless.
func (n *mdnsNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == n.host.ID() {
		return // ignore ourselves
	}
	if err := n.host.Connect(n.ctx, pi); err != nil {
		log.Printf("p2p: mdns connect %s failed: %v", pi.ID, err)
		return
	}
	log.Printf("p2p: mdns discovered and connected to %s", pi.ID)
}

// EnableMDNS starts LAN mDNS discovery for h under serviceTag (empty selects
// DefaultMDNSTag). Discovered peers are auto-dialed via the notifee. The
// service is bound to ctx: it stops when ctx is cancelled or the host closes.
// A nil error means the service started; on a LAN where multicast is blocked it
// still starts cleanly but may simply never discover a peer.
func EnableMDNS(ctx context.Context, h host.Host, serviceTag string) error {
	if serviceTag == "" {
		serviceTag = DefaultMDNSTag
	}
	svc := mdns.NewMdnsService(h, serviceTag, &mdnsNotifee{ctx: ctx, host: h})
	if err := svc.Start(); err != nil {
		return fmt.Errorf("p2p: start mdns: %w", err)
	}
	// Stop the service when ctx is cancelled so it does not outlive the node.
	go func() {
		<-ctx.Done()
		_ = svc.Close()
	}()
	return nil
}
