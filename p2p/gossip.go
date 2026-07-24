package p2p

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"log"

	"l1chain/chain"
	"l1chain/core"
	"l1chain/node"
	"l1chain/store"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

// GossipSub topic names for the l1chain network.
const (
	BlockTopic = "l1/blocks"
	TxTopic    = "l1/txs"
)

// maxConcurrentSyncs bounds catch-up syncs running at once across all peers.
const maxConcurrentSyncs = 4

// P2P wraps a libp2p host and a GossipSub instance with the two l1chain topics
// (blocks and txs) plus the chain-sync stream protocol. Blocks received over
// the network are validated through node.AcceptExternalBlock before acceptance.
type P2P struct {
	host  host.Host
	ps    *pubsub.PubSub
	block *pubsub.Topic
	tx    *pubsub.Topic

	blockSub *pubsub.Subscription
	txSub    *pubsub.Subscription

	// ctx is the lifetime context captured at Start, used by the Publish
	// helpers whose signatures intentionally omit a context argument.
	ctx context.Context

	// syncSem bounds the number of concurrent catch-up syncs dispatched off the
	// block read loop, so one slow peer can neither stall block intake nor let
	// syncs fan out without limit.
	syncSem chan struct{}
}

// NewP2P attaches a GossipSub router to h and joins the block and tx topics.
func NewP2P(ctx context.Context, h host.Host) (*P2P, error) {
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		return nil, fmt.Errorf("p2p: new gossipsub: %w", err)
	}
	bt, err := ps.Join(BlockTopic)
	if err != nil {
		return nil, fmt.Errorf("p2p: join %s: %w", BlockTopic, err)
	}
	tt, err := ps.Join(TxTopic)
	if err != nil {
		return nil, fmt.Errorf("p2p: join %s: %w", TxTopic, err)
	}
	return &P2P{host: h, ps: ps, block: bt, tx: tt, syncSem: make(chan struct{}, maxConcurrentSyncs)}, nil
}

// Host returns the underlying libp2p host.
func (p *P2P) Host() host.Host { return p.host }

// Start subscribes both topics, registers the sync responder, and launches the
// block/tx read loops. It returns once the subscriptions are live; the loops
// run until ctx is cancelled.
func (p *P2P) Start(ctx context.Context, n *node.Node) error {
	p.ctx = ctx
	p.registerSyncHandler(n)

	bs, err := p.block.Subscribe()
	if err != nil {
		return fmt.Errorf("p2p: subscribe %s: %w", BlockTopic, err)
	}
	ts, err := p.tx.Subscribe()
	if err != nil {
		return fmt.Errorf("p2p: subscribe %s: %w", TxTopic, err)
	}
	p.blockSub = bs
	p.txSub = ts

	go p.readBlocks(ctx, n, bs)
	go p.readTxs(ctx, n, ts)
	return nil
}

// Wire is a convenience over Start: it begins gossip processing for n. Local
// block announcements are pushed with AnnounceBlock (see below); GossipSub
// handles fan-out from there.
func Wire(ctx context.Context, n *node.Node, p *P2P) error {
	return p.Start(ctx, n)
}

// readBlocks consumes block messages, validates each via AcceptExternalBlock,
// and on ErrUnknownParent syncs the missing canonical prefix from the relaying
// peer before retrying. Invalid blocks are logged and dropped.
func (p *P2P) readBlocks(ctx context.Context, n *node.Node, sub *pubsub.Subscription) {
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			return // context cancelled or subscription closed
		}
		if msg.ReceivedFrom == p.host.ID() {
			continue // our own publish, already applied locally
		}
		b, err := store.DecodeBlock(msg.Data)
		if err != nil {
			log.Printf("p2p: drop undecodable block: %v", err)
			continue
		}
		p.handleBlock(ctx, n, b, msg.ReceivedFrom)
	}
}

// handleBlock applies b, recovering from a missing-parent gap by syncing from
// the relaying peer and retrying once.
func (p *P2P) handleBlock(ctx context.Context, n *node.Node, b core.Block, from peer.ID) {
	err := n.AcceptExternalBlock(b)
	if err == nil {
		return
	}
	if errors.Is(err, chain.ErrUnknownParent) && from != "" && from != p.host.ID() {
		// Dispatch the catch-up sync OFF the read loop: a single slow peer must
		// not stall block intake for every other peer. Bound concurrency via
		// syncSem; if all slots are busy, drop this gap block (gossip re-delivery
		// or a later block will re-trigger the sync).
		select {
		case p.syncSem <- struct{}{}:
		default:
			log.Printf("p2p: sync slots busy, deferring gap block h=%d", b.Header.Height)
			return
		}
		go func() {
			defer func() { <-p.syncSem }()
			start := n.Head().Header.Height + 1
			if serr := p.syncFrom(ctx, from, start, n); serr != nil {
				log.Printf("p2p: sync from %s failed: %v", from, serr)
				return
			}
			if rerr := n.AcceptExternalBlock(b); rerr != nil {
				log.Printf("p2p: drop block h=%d after sync: %v", b.Header.Height, rerr)
			}
		}()
		return
	}
	log.Printf("p2p: drop invalid block h=%d: %v", b.Header.Height, err)
}

// readTxs consumes tx messages and offers each to the mempool; admission
// errors (dup/bad nonce/insufficient balance) are expected and ignored.
func (p *P2P) readTxs(ctx context.Context, n *node.Node, sub *pubsub.Subscription) {
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			return
		}
		if msg.ReceivedFrom == p.host.ID() {
			continue
		}
		tx, err := decodeTx(msg.Data)
		if err != nil {
			log.Printf("p2p: drop undecodable tx: %v", err)
			continue
		}
		_ = n.SubmitTx(tx)
	}
}

// PublishBlock broadcasts b to the block topic. Peers validate it on receipt.
func (p *P2P) PublishBlock(b core.Block) error {
	data, err := store.EncodeBlock(b)
	if err != nil {
		return fmt.Errorf("p2p: encode block: %w", err)
	}
	return p.block.Publish(p.ctx, data)
}

// AnnounceBlock is the local node's hook after mining a block: publish it so
// GossipSub fans it out to peers.
func (p *P2P) AnnounceBlock(b core.Block) error { return p.PublishBlock(b) }

// Resync catches the local node up to peer `from` by requesting canonical
// blocks starting at local head+1 and applying each via AcceptExternalBlock.
// It is the explicit trigger used on reconnect after a network partition, or
// any time a node suspects it is behind.
func (p *P2P) Resync(ctx context.Context, from peer.ID, n *node.Node) error {
	start := n.Head().Header.Height + 1
	return p.syncFrom(ctx, from, start, n)
}

// PublishTx broadcasts tx to the tx topic.
func (p *P2P) PublishTx(tx core.Transaction) error {
	data, err := encodeTx(tx)
	if err != nil {
		return fmt.Errorf("p2p: encode tx: %w", err)
	}
	return p.tx.Publish(p.ctx, data)
}

// encodeTx / decodeTx gob-serialize a transaction for the wire, mirroring the
// store block codec so Hash()/Signature round-trip exactly.
func encodeTx(tx core.Transaction) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&tx); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeTx(data []byte) (core.Transaction, error) {
	var tx core.Transaction
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&tx); err != nil {
		return core.Transaction{}, err
	}
	return tx, nil
}
