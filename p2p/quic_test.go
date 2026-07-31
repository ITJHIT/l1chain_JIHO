package p2p

import (
	"context"
	"strings"
	"testing"
	"time"

	"l1chain/node"
	"l1chain/wallet"
)

// TestHostListensOnQUICAndStillGossips proves NewHostWithConfig's new QUIC
// listen address actually activates (not just silently ignored) and that
// real block gossip still converges over the QUIC-inclusive listen set --
// zero regression to the pre-existing TCP-only path, since the two hosts
// below use exactly the same NewHost/startPeer construction every other p2p
// test in this package already uses.
func TestHostListensOnQUICAndStillGossips(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	faucet, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("faucet: %v", err)
	}
	fa := faucet.Address()

	a := newNetNode(t, fa)
	b := newNetNode(t, fa)
	ah, ap := startPeer(t, ctx, a)
	bh, _ := startPeer(t, ctx, b)

	hasQUIC := false
	for _, addr := range ah.Addrs() {
		if strings.Contains(addr.String(), "quic-v1") {
			hasQUIC = true
			break
		}
	}
	if !hasQUIC {
		t.Fatalf("host addrs = %v, want at least one /quic-v1 listen address", ah.Addrs())
	}

	if err := Connect(ctx, ah, bh); err != nil {
		t.Fatalf("connect: %v", err)
	}

	blk, err := a.MineBlock()
	if err != nil {
		t.Fatalf("MineBlock: %v", err)
	}
	if !pollConverged(blk, []*node.Node{b},
		func() { _ = ap.AnnounceBlock(blk) },
		time.Now().Add(20*time.Second)) {
		t.Fatalf("b did not converge to a block gossiped over the QUIC-inclusive listen set (height=%d)", b.Head().Header.Height)
	}
}
