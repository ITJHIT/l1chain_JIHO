// Package p2p provides real libp2p peer-to-peer networking for the l1chain
// full node: a libp2p host listening on both TCP and QUIC, GossipSub
// propagation of blocks and transactions over two topics, and a stream-based
// chain-sync protocol for new or lagging nodes to catch up.
//
// It never trusts a peer: every block received from the network is validated
// through node.AcceptExternalBlock, which drives the same chain.AddBlock path
// (PoW, merkle root, re-derived state root, linkage) used for locally mined
// blocks. Invalid blocks are dropped, not applied.
package p2p

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p"
	coreconnmgr "github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	// The concrete BasicConnMgr implementation lives in p2p/net/connmgr, a
	// DIFFERENT package from core/connmgr (which only declares the
	// ConnManager interface libp2p.ConnectionManager expects) -- both are
	// named "connmgr", hence the alias above on the interface import.
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"

	ma "github.com/multiformats/go-multiaddr"
)

// Connection watermarks for the connection manager every host gets by
// default (see NewHostWithConfig). Found missing entirely, not just
// under-tuned: NewHost previously had no libp2p.ConnectionManager at all,
// meaning no cap of any kind on inbound connections -- an attacker opening
// many sybil connections to a victim (an eclipse attempt) had nothing to
// push back against short of the OS's own file-descriptor limit. connLow is
// the floor libp2p will never trim below even under pressure (small on
// purpose -- this node's own real workload rarely needs more than a
// handful of peers); connHigh is where trimming of the least useful
// connections begins.
const (
	connLow  = 20
	connHigh = 100
)

func newConnManager() (coreconnmgr.ConnManager, error) {
	cm, err := connmgr.NewConnManager(connLow, connHigh, connmgr.WithGracePeriod(10*time.Second))
	if err != nil {
		return nil, fmt.Errorf("p2p: connection manager: %w", err)
	}
	return cm, nil
}

// NewHost builds a real libp2p host listening on 127.0.0.1 over TCP with a
// freshly generated Ed25519 identity. Pass listenPort 0 to bind an ephemeral
// port (recommended for tests). The returned host must be Closed by the caller.
func NewHost(ctx context.Context, listenPort int) (host.Host, error) {
	return NewHostWithConfig(ctx, HostConfig{ListenPort: listenPort})
}

// HostConfig configures NewHostWithConfig. ListenHost defaults to
// "127.0.0.1" when empty, matching NewHost's fixed behavior. IdentityKey
// pins the libp2p peer identity across restarts when non-nil; nil generates
// a fresh Ed25519 identity every call, also matching NewHost.
type HostConfig struct {
	ListenHost  string
	ListenPort  int
	IdentityKey crypto.PrivKey
}

// NewHostWithConfig is NewHost with two additional knobs NewHost's tests
// never needed, but a real multi-container deployment does:
//
//   - ListenHost: binding 127.0.0.1 (NewHost's fixed choice) makes a host
//     unreachable from sibling Docker containers -- loopback is only
//     reachable from inside the SAME container's network namespace, so a
//     compose service dialing another by container name would never
//     connect even though the port is correctly EXPOSEd. ListenHost lets a
//     deployment bind 0.0.0.0 (or a specific interface) instead.
//   - IdentityKey: NewHost generates a fresh Ed25519 identity -- and
//     therefore a fresh peer ID and full dialable multiaddr -- on every
//     call, unknowable until the process has already started and printed
//     it. A compose file (or any static config) cannot reference a peer ID
//     it doesn't know yet in another service's --peers flag. Pinning the
//     identity makes the multiaddr computable ahead of time.
func NewHostWithConfig(ctx context.Context, cfg HostConfig) (host.Host, error) {
	listenHost := cfg.ListenHost
	if listenHost == "" {
		listenHost = "127.0.0.1"
	}
	priv := cfg.IdentityKey
	if priv == nil {
		var err error
		priv, _, err = crypto.GenerateKeyPair(crypto.Ed25519, -1)
		if err != nil {
			return nil, fmt.Errorf("p2p: generate identity: %w", err)
		}
	}
	cm, err := newConnManager()
	if err != nil {
		return nil, err
	}
	h, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(
			fmt.Sprintf("/ip4/%s/tcp/%d", listenHost, cfg.ListenPort),
			// QUIC transport is already registered on every host by
			// go-libp2p's own DefaultTransports (this repo's ListenAddrs
			// never call libp2p.Transport, leaving cfg.Transports nil) --
			// the only missing piece was a QUIC-shaped listen address.
			// Same port number as TCP: UDP and TCP are separate namespaces,
			// so there is no actual conflict, and it keeps one port to
			// document/firewall per node instead of two.
			fmt.Sprintf("/ip4/%s/udp/%d/quic-v1", listenHost, cfg.ListenPort),
		),
		libp2p.ConnectionManager(cm),
	)
	if err != nil {
		return nil, fmt.Errorf("p2p: new host: %w", err)
	}
	return h, nil
}

// IdentityFromSeed deterministically derives a libp2p Ed25519 identity from
// an arbitrary hex-encoded seed: the same seed always yields the same
// PrivKey (and therefore the same peer ID), which is the whole point --
// pinning a node's identity via a CLI flag. The seed is hashed to exactly
// ed25519's 32-byte seed size first, so any hex string of any length is
// accepted, not just a pre-formatted 32-byte key.
func IdentityFromSeed(hexSeed string) (crypto.PrivKey, error) {
	raw, err := hex.DecodeString(strings.TrimPrefix(hexSeed, "0x"))
	if err != nil {
		return nil, fmt.Errorf("p2p: identity seed must be hex: %w", err)
	}
	seed := sha256.Sum256(raw)
	priv, _, err := crypto.GenerateEd25519Key(bytes.NewReader(seed[:]))
	if err != nil {
		return nil, fmt.Errorf("p2p: derive identity from seed: %w", err)
	}
	return priv, nil
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
