// Package p2p provides real libp2p peer-to-peer networking for the l1chain
// full node: a TCP libp2p host, GossipSub propagation of blocks and
// transactions over two topics, and a stream-based chain-sync protocol for new
// or lagging nodes to catch up.
//
// It never trusts a peer: every block received from the network is validated
// through node.AcceptExternalBlock, which drives the same chain.AddBlock path
// (PoW, merkle root, re-derived state root, linkage) used for locally mined
// blocks. Invalid blocks are dropped, not applied.
package p2p

import (
	"context"
	"fmt"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"

	ma "github.com/multiformats/go-multiaddr"
)

// NewHost builds a real libp2p host listening on 127.0.0.1 over TCP with a
// freshly generated Ed25519 identity. Pass listenPort 0 to bind an ephemeral
// port (recommended for tests). The returned host must be Closed by the caller.
func NewHost(ctx context.Context, listenPort int) (host.Host, error) {
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		return nil, fmt.Errorf("p2p: generate identity: %w", err)
	}
	h, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", listenPort)),
	)
	if err != nil {
		return nil, fmt.Errorf("p2p: new host: %w", err)
	}
	return h, nil
}

// Connect dials host b from host a, registering b's listen addresses in a's
// peerstore first so the transport can reach it.
func Connect(ctx context.Context, a, b host.Host) error {
	info := peer.AddrInfo{ID: b.ID(), Addrs: b.Addrs()}
	a.Peerstore().AddAddrs(info.ID, info.Addrs, peerstore.PermanentAddrTTL)
	if err := a.Connect(ctx, info); err != nil {
		return fmt.Errorf("p2p: connect %s -> %s: %w", a.ID(), b.ID(), err)
	}
	return nil
}

// FullAddrs returns h's listen addresses with the /p2p/<peer-id> component
// appended, i.e. complete dialable multiaddrs a remote node can pass to
// ConnectAddr. Loopback ephemeral ports resolve to concrete /tcp/<port> values
// once the host is listening.
func FullAddrs(h host.Host) []string {
	info := peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()}
	p2pAddrs, err := peer.AddrInfoToP2pAddrs(&info)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(p2pAddrs))
	for _, a := range p2pAddrs {
		out = append(out, a.String())
	}
	return out
}

// ConnectAddr dials the peer described by a full multiaddr string (one that
// includes a /p2p/<peer-id> component), registering its transport address in
// h's peerstore first. It is the CLI/multiprocess counterpart to Connect, which
// takes two in-process hosts.
func ConnectAddr(ctx context.Context, h host.Host, addr string) error {
	m, err := ma.NewMultiaddr(addr)
	if err != nil {
		return fmt.Errorf("p2p: parse multiaddr %q: %w", addr, err)
	}
	info, err := peer.AddrInfoFromP2pAddr(m)
	if err != nil {
		return fmt.Errorf("p2p: addr info from %q: %w", addr, err)
	}
	h.Peerstore().AddAddrs(info.ID, info.Addrs, peerstore.PermanentAddrTTL)
	if err := h.Connect(ctx, *info); err != nil {
		return fmt.Errorf("p2p: connect %s -> %s: %w", h.ID(), info.ID, err)
	}
	return nil
}
