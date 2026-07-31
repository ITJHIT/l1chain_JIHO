package p2p

import (
	"context"
	"io"
	"testing"
	"time"

	"l1chain/wallet"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// TestDHTDiscoveryFindsAndDialsPeers proves the same property p2p/mdns.go
// already proves for LAN peer auto-discovery, via a different bootstrap
// mechanism: three hosts, each seeded (bootstrapped) with the OTHER two's
// AddrInfo, advertise under a shared rendezvous and must end up mutually
// connected purely through DHT FindPeers + auto-dial -- not a NAT-crossing
// test, just a different discovery mechanism among already mutually
// TCP-dialable hosts.
func TestDHTDiscoveryFindsAndDialsPeers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	faucet, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("faucet: %v", err)
	}
	fa := faucet.Address()

	a := newNetNode(t, fa)
	b := newNetNode(t, fa)
	c := newNetNode(t, fa)

	ah, _ := startPeer(t, ctx, a)
	bh, _ := startPeer(t, ctx, b)
	ch, _ := startPeer(t, ctx, c)

	const rendezvous = "test-rendezvous"
	info := func(h host.Host) peer.AddrInfo {
		return peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()}
	}

	closers := make([]io.Closer, 0, 3)
	t.Cleanup(func() {
		for _, closer := range closers {
			_ = closer.Close()
		}
	})

	closeA, err := EnableDHTDiscovery(ctx, ah, []peer.AddrInfo{info(bh), info(ch)}, rendezvous)
	if err != nil {
		t.Fatalf("EnableDHTDiscovery(a): %v", err)
	}
	closers = append(closers, closeA)
	closeB, err := EnableDHTDiscovery(ctx, bh, []peer.AddrInfo{info(ah), info(ch)}, rendezvous)
	if err != nil {
		t.Fatalf("EnableDHTDiscovery(b): %v", err)
	}
	closers = append(closers, closeB)
	closeC, err := EnableDHTDiscovery(ctx, ch, []peer.AddrInfo{info(ah), info(bh)}, rendezvous)
	if err != nil {
		t.Fatalf("EnableDHTDiscovery(c): %v", err)
	}
	closers = append(closers, closeC)

	deadline := time.Now().Add(90 * time.Second)
	hosts := []host.Host{ah, bh, ch}
	for {
		allConnected := true
		for i := range hosts {
			for j := range hosts {
				if i == j {
					continue
				}
				if hosts[i].Network().Connectedness(hosts[j].ID()) != network.Connected {
					allConnected = false
				}
			}
		}
		if allConnected {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("hosts did not mutually connect via DHT discovery within the deadline (a<->b=%v a<->c=%v b<->c=%v)",
				ah.Network().Connectedness(bh.ID()), ah.Network().Connectedness(ch.ID()), bh.Network().Connectedness(ch.ID()))
		}
		time.Sleep(500 * time.Millisecond)
	}
}
