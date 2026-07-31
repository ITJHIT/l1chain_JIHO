package p2p

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"l1chain/node"
	"l1chain/wallet"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	"github.com/marcopolo/simnet"

	ma "github.com/multiformats/go-multiaddr"
)

// The helpers below (mockEventTracer, simnetSourceIPSelector,
// quicSimnetTransport, connectToRelay, learnAddrs,
// waitForHolePunchSvcActive, ensureDirectConn) are a direct port of
// go-libp2p's own p2p/protocol/holepunch/holepunch_test.go -- the exact
// mechanism its own TestEndToEndSimConnect uses to prove genuine
// (non-mocked) DCUtR hole-punching against a real, packet-level,
// address-restricted-cone-NAT-like simulator
// (github.com/marcopolo/simnet's SimpleFirewallRouter). Two deliberate
// deviations from upstream, both noted inline where they occur: the relay
// host reuses this repo's own already-merged EnableRelayService instead of
// upstream's libp2p.WithFxOption(fx.Invoke(...)) (avoids promoting
// go.uber.org/fx off // indirect for a single test file), and
// ensureNoHolePunchStream compares network.Stream.Protocol() rather than
// upstream's own s.ID() (a per-stream identifier that can never equal a
// protocol string, making that particular upstream assertion vacuous).

type mockEventTracer struct {
	mu     sync.Mutex
	events []*holepunch.Event
}

func (m *mockEventTracer) Trace(evt *holepunch.Event) {
	m.mu.Lock()
	m.events = append(m.events, evt)
	m.mu.Unlock()
}

func (m *mockEventTracer) getEvents() []*holepunch.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*holepunch.Event{}, m.events...)
}

var _ holepunch.EventTracer = (*mockEventTracer)(nil)

type simnetSourceIPSelector struct {
	ip atomic.Pointer[net.IP]
}

func (s *simnetSourceIPSelector) PreferredSourceIPForDestination(_ *net.UDPAddr) (net.IP, error) {
	return *s.ip.Load(), nil
}

// quicSimnetTransport builds the libp2p.QUICReuse option that splices a
// simnet.NewSimConn-backed net.PacketConn into QUIC's own connection
// manager, so all of this host's QUIC traffic actually flows through
// router's simulated firewall/NAT instead of a real socket.
//
// Deviates from go-libp2p's own holepunch_test.go here: that file's
// quicSimnet never calls SetUpPacketReceiver, because go-libp2p v0.48.0's
// go.mod pins marcopolo/simnet at an older v0.0.4. This repo's go.sum
// resolves the same dependency to v0.0.7 (a newer transitive requirement
// elsewhere in the module graph forces the higher version via MVS), and
// v0.0.7's SimConn.WriteTo panics if SetUpPacketReceiver was never called
// -- confirmed directly against v0.0.7's own vendored source
// (simconn.go:260-263) after CI caught the panic on the first push.
// router itself satisfies PacketReceiver (SimpleFirewallRouter.RecvPacket),
// so wiring WriteTo's outbound packets to router.RecvPacket is the correct
// "send up to the router for delivery" half of the same AddNode-registered
// "receive down from the router" wiring already below.
func quicSimnetTransport(isPubliclyReachable bool, router *simnet.SimpleFirewallRouter) libp2p.Option {
	sel := &simnetSourceIPSelector{}
	return libp2p.QUICReuse(
		quicreuse.NewConnManager,
		quicreuse.OverrideSourceIPSelector(func() (quicreuse.SourceIPSelector, error) {
			return sel, nil
		}),
		quicreuse.OverrideListenUDP(func(_ string, address *net.UDPAddr) (net.PacketConn, error) {
			sel.ip.Store(&address.IP)
			if isPubliclyReachable {
				router.SetAddrPubliclyReachable(address)
			}
			c := simnet.NewSimConn(address)
			c.SetUpPacketReceiver(router)
			router.AddNode(address, c)
			return c, nil
		}),
	)
}

// connectToRelay enables client-side circuit relay and registers relay as a
// static relay candidate, so the resulting host acquires (and keeps
// renewed) a circuit-relay-v2 reservation on it.
func connectToRelay(relay *host.Host) libp2p.Option {
	return func(cfg *libp2p.Config) error {
		if relay == nil {
			return nil
		}
		r := *relay
		info := peer.AddrInfo{ID: r.ID(), Addrs: r.Addrs()}
		return cfg.Apply(
			libp2p.EnableRelay(),
			libp2p.EnableAutoRelayWithStaticRelays([]peer.AddrInfo{info}),
		)
	}
}

func learnAddrs(h1, h2 host.Host) {
	h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), peerstore.ConnectedAddrTTL)
	h2.Peerstore().AddAddrs(h1.ID(), h1.Addrs(), peerstore.ConnectedAddrTTL)
}

func waitForHolePunchSvcActive(t *testing.T, h host.Host) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, p := range h.Mux().Protocols() {
			if p == holepunch.Protocol {
				return
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("holepunch service never became active on %s", h.ID())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ensureDirectConn waits until h1<->h2 have a connection with no
// /p2p-circuit component in its remote multiaddr in BOTH directions, i.e.
// hole punching actually replaced the relayed connection with a direct one.
func ensureDirectConn(t *testing.T, h1, h2 host.Host) {
	t.Helper()
	direct := func(a, b host.Host) bool {
		for _, c := range a.Network().ConnsToPeer(b.ID()) {
			if _, err := c.RemoteMultiaddr().ValueForProtocol(ma.P_CIRCUIT); err != nil {
				return true
			}
		}
		return false
	}
	deadline := time.Now().Add(5 * time.Second)
	for !direct(h1, h2) || !direct(h2, h1) {
		if !time.Now().Before(deadline) {
			t.Fatalf("h1<->h2 never converged to a direct (non-relayed) connection")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ensureNoHolePunchStream waits until neither side has a lingering
// /libp2p/dcutr stream open, in either direction.
func ensureNoHolePunchStream(t *testing.T, h1, h2 host.Host) {
	t.Helper()
	clean := func(a, b host.Host) bool {
		for _, c := range a.Network().ConnsToPeer(b.ID()) {
			for _, s := range c.GetStreams() {
				if s.Protocol() == holepunch.Protocol {
					return false
				}
			}
		}
		return true
	}
	deadline := time.Now().Add(5 * time.Second)
	for !clean(h1, h2) || !clean(h2, h1) {
		if !time.Now().Before(deadline) {
			t.Fatalf("a /libp2p/dcutr stream is still open after hole punching completed")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestDCUtRHolePunchesThroughSimulatedNAT proves genuine (non-mocked) DCUtR
// hole-punching using go-libp2p's own upstream proof technique -- see the
// package-level comment above for the exact provenance and the two
// deliberate deviations from it -- then goes further than upstream's own
// test: once the direct (non-relayed) connection is established, a real
// block is mined and gossiped end-to-end over exactly that punched
// connection, the same extend-the-raw-proof-with-real-gossip-convergence
// technique p2p/relay_test.go already used for circuit-relay.
//
// Event-count tolerance is INHERITED, not "fixed": per upstream's own
// comment on this exact scenario, the hole-punch INITIATOR (h1 here)
// legitimately sees either 2 or 3 events -- a genuinely-punched direct
// connection can complete WHILE the DCUtR coordination handshake is still
// running, "from time to time," not on a guaranteed schedule -- only the
// RESPONDER (h2) gets a strict 4-event assertion. Tightening h1's
// assertion to a fixed count would make this test flaky, not more correct.
func TestDCUtRHolePunchesThroughSimulatedNAT(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	faucet, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("faucet: %v", err)
	}
	fa := faucet.Address()

	router := &simnet.SimpleFirewallRouter{}

	relay, err := libp2p.New(
		quicSimnetTransport(true, router),
		libp2p.ListenAddrs(ma.StringCast("/ip4/1.2.0.1/udp/8000/quic-v1")),
		libp2p.DisableRelay(),
		libp2p.ResourceManager(&network.NullResourceManager{}),
	)
	if err != nil {
		t.Fatalf("new relay host: %v", err)
	}
	defer relay.Close()
	if _, err := EnableRelayService(relay); err != nil {
		t.Fatalf("EnableRelayService: %v", err)
	}

	h1tr := &mockEventTracer{}
	h1, err := libp2p.New(
		quicSimnetTransport(false, router),
		libp2p.EnableHolePunching(holepunch.WithTracer(h1tr), holepunch.DirectDialTimeout(100*time.Millisecond)),
		libp2p.ListenAddrs(ma.StringCast("/ip4/2.2.0.1/udp/8000/quic-v1")),
		libp2p.ResourceManager(&network.NullResourceManager{}),
		libp2p.ForceReachabilityPrivate(),
	)
	if err != nil {
		t.Fatalf("new h1: %v", err)
	}
	defer h1.Close()

	h2tr := &mockEventTracer{}
	h2, err := libp2p.New(
		quicSimnetTransport(false, router),
		libp2p.ListenAddrs(ma.StringCast("/ip4/2.2.0.2/udp/8001/quic-v1")),
		libp2p.ResourceManager(&network.NullResourceManager{}),
		connectToRelay(&relay),
		libp2p.EnableHolePunching(holepunch.WithTracer(h2tr), holepunch.DirectDialTimeout(100*time.Millisecond)),
		libp2p.ForceReachabilityPrivate(),
	)
	if err != nil {
		t.Fatalf("new h2: %v", err)
	}
	defer h2.Close()

	waitForHolePunchSvcActive(t, h1)
	waitForHolePunchSvcActive(t, h2)

	learnAddrs(h1, h2)
	if err := Connect(ctx, h1, h2); err != nil {
		t.Fatalf("h1 connect h2 (via relay): %v", err)
	}

	ensureDirectConn(t, h1, h2)
	ensureNoHolePunchStream(t, h1, h2)

	var h2Events []*holepunch.Event
	eventsDeadline := time.Now().Add(2 * time.Second)
	for {
		h2Events = h2tr.getEvents()
		if len(h2Events) == 4 {
			break
		}
		if !time.Now().Before(eventsDeadline) {
			t.Fatalf("h2 (responder) recorded %d hole-punch events, want 4: %+v", len(h2Events), h2Events)
		}
		time.Sleep(50 * time.Millisecond)
	}
	wantSeq := []string{
		holepunch.DirectDialEvtT,
		holepunch.StartHolePunchEvtT,
		holepunch.HolePunchAttemptEvtT,
		holepunch.EndHolePunchEvtT,
	}
	for i, want := range wantSeq {
		if h2Events[i].Type != want {
			t.Fatalf("h2 event[%d].Type = %q, want %q", i, h2Events[i].Type, want)
		}
	}

	h1Events := h1tr.getEvents()
	if len(h1Events) != 2 && len(h1Events) != 3 {
		t.Fatalf("h1 (initiator) recorded %d hole-punch events, want 2 or 3: %+v", len(h1Events), h1Events)
	}
	if h1Events[0].Type != holepunch.StartHolePunchEvtT {
		t.Fatalf("h1 event[0].Type = %q, want %q", h1Events[0].Type, holepunch.StartHolePunchEvtT)
	}
	if h1Events[1].Type != holepunch.HolePunchAttemptEvtT {
		t.Fatalf("h1 event[1].Type = %q, want %q", h1Events[1].Type, holepunch.HolePunchAttemptEvtT)
	}
	if len(h1Events) == 3 && h1Events[2].Type != holepunch.EndHolePunchEvtT {
		t.Fatalf("h1 event[2].Type = %q, want %q", h1Events[2].Type, holepunch.EndHolePunchEvtT)
	}

	// Going further than upstream: wire real chain nodes onto h1/h2 and
	// prove a mined block gossips end-to-end over exactly the direct,
	// post-hole-punch connection just proven above.
	n1 := newNetNode(t, fa)
	n2 := newNetNode(t, fa)

	p1, err := NewP2P(ctx, h1)
	if err != nil {
		t.Fatalf("NewP2P h1: %v", err)
	}
	if err := Wire(ctx, n1, p1); err != nil {
		t.Fatalf("Wire n1: %v", err)
	}
	p2, err := NewP2P(ctx, h2)
	if err != nil {
		t.Fatalf("NewP2P h2: %v", err)
	}
	if err := Wire(ctx, n2, p2); err != nil {
		t.Fatalf("Wire n2: %v", err)
	}

	blk, err := n1.MineBlock()
	if err != nil {
		t.Fatalf("MineBlock: %v", err)
	}
	if !pollConverged(blk, []*node.Node{n2},
		func() { _ = p1.AnnounceBlock(blk) },
		time.Now().Add(20*time.Second)) {
		t.Fatalf("n2 did not converge to a block gossiped over the hole-punched connection (height=%d)", n2.Head().Header.Height)
	}
}
