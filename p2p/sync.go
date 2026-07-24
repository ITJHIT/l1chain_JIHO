package p2p

import (
	"context"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"time"

	"l1chain/core"
	"l1chain/node"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// SyncProtocol is the libp2p stream protocol used to catch a new or lagging
// node up to a peer's canonical head. The requester writes an 8-byte
// big-endian start height; the responder streams gob-encoded canonical blocks
// from that height to its head, then closes the stream.
const SyncProtocol = protocol.ID("/l1/sync/1.0.0")

// maxBlocksPerSync bounds how many blocks a single sync response streams, so a
// peer cannot force unbounded work / memory with one request.
const maxBlocksPerSync = 1024

// maxBlockBytes caps the gob-encoded size of any single synced block, and
// maxSyncBytes is the total read budget for one sync stream (blocks x per-block
// cap). The requester wraps the stream in an io.LimitReader with this budget so
// a crafted, oversized response cannot exhaust memory.
const (
	maxBlockBytes = 1 << 20 // 1 MiB per block
	maxSyncBytes  = maxBlocksPerSync * maxBlockBytes
)

// syncStreamTimeout bounds one full request/response exchange. Both sides set a
// stream deadline and the requester derives a context with this timeout, so a
// silent or trickling peer cannot pin a sync open indefinitely.
const syncStreamTimeout = 30 * time.Second

// registerSyncHandler installs the responder side of SyncProtocol on the host.
func (p *P2P) registerSyncHandler(n *node.Node) {
	p.host.SetStreamHandler(SyncProtocol, func(s network.Stream) {
		defer func() { _ = s.Close() }()

		// Bound the whole responder exchange so a slow/silent requester cannot
		// pin the handler open.
		_ = s.SetDeadline(time.Now().Add(syncStreamTimeout))

		var start uint64
		if err := binary.Read(s, binary.BigEndian, &start); err != nil {
			_ = s.Reset()
			return
		}

		blocks := n.CanonicalBlocksFrom(start)
		if len(blocks) > maxBlocksPerSync {
			blocks = blocks[:maxBlocksPerSync]
		}

		enc := gob.NewEncoder(s)
		for i := range blocks {
			if err := enc.Encode(&blocks[i]); err != nil {
				_ = s.Reset()
				return
			}
		}
	})
}

// syncFrom opens a SyncProtocol stream to peerID, requests canonical blocks
// starting at height start, and applies each in order via
// node.AcceptExternalBlock. Blocks arrive parent-before-child so linkage holds.
// A clean EOF ends the stream. Individual apply errors (e.g. a duplicate that
// arrived via gossip meanwhile) are non-fatal; a genuine linkage failure is
// returned so the caller can decide.
func (p *P2P) syncFrom(ctx context.Context, peerID peer.ID, start uint64, n *node.Node) error {
	ctx, cancel := context.WithTimeout(ctx, syncStreamTimeout)
	defer cancel()

	s, err := p.host.NewStream(ctx, peerID, SyncProtocol)
	if err != nil {
		return fmt.Errorf("p2p: open sync stream: %w", err)
	}
	defer func() { _ = s.Close() }()
	// Bound the whole request/response so a trickling peer cannot stall us.
	_ = s.SetDeadline(time.Now().Add(syncStreamTimeout))

	if err := binary.Write(s, binary.BigEndian, start); err != nil {
		_ = s.Reset()
		return fmt.Errorf("p2p: write sync request: %w", err)
	}
	// Signal the request is complete; the responder only needs the height.
	_ = s.CloseWrite()

	// Cap total bytes consumed so a crafted oversized response cannot OOM us.
	dec := gob.NewDecoder(io.LimitReader(s, maxSyncBytes))
	for {
		var b core.Block
		if err := dec.Decode(&b); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return fmt.Errorf("p2p: decode synced block: %w", err)
		}
		if err := n.AcceptExternalBlock(b); err != nil {
			// Tolerate races (block also delivered by gossip); linkage errors
			// mean the peer's stream is inconsistent — stop and report.
			return fmt.Errorf("p2p: apply synced block h=%d: %w", b.Header.Height, err)
		}
	}
}
