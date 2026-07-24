package rpc_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"l1chain/chain"
	"l1chain/core"
	"l1chain/node"
	"l1chain/rpc"
	"l1chain/wallet"
)

// TestSingleNodeE2E drives a real Node behind an httptest JSON-RPC server:
// wallet A (funded at genesis) sends a signed transfer to B via sendRawTx, the
// node mines it, and every read method reflects the result.
func TestSingleNodeE2E(t *testing.T) {
	a, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey(A): %v", err)
	}
	b, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey(B): %v", err)
	}
	miner, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey(miner): %v", err)
	}

	const difficulty = 6
	n, err := node.New(node.Config{
		MinerKey:     miner,
		Difficulty:   difficulty,
		GenesisAlloc: map[core.Address]uint64{a.Address(): 1000},
	})
	if err != nil {
		t.Fatalf("node.New: %v", err)
	}
	defer n.Close()

	srv := httptest.NewServer(rpc.NewServer(n))
	defer srv.Close()
	client := rpc.NewClient(srv.URL)

	// Sanity: genesis head is height 0.
	head, err := client.GetChainHead()
	if err != nil {
		t.Fatalf("GetChainHead: %v", err)
	}
	if head.Height != 0 {
		t.Fatalf("genesis head height = %d, want 0", head.Height)
	}

	// A -> B transfer, signed, submitted via RPC.
	tx := core.Transaction{To: b.Address(), Value: 400, Nonce: 0, GasLimit: 21000, ChainID: chain.DefaultChainID}
	a.Sign(&tx)
	txHash, err := client.SendRawTx(tx)
	if err != nil {
		t.Fatalf("SendRawTx: %v", err)
	}
	if txHash != tx.Hash().Hex() {
		t.Fatalf("returned txHash %s != %s", txHash, tx.Hash().Hex())
	}

	// Drive mining (single node).
	if _, err := n.MineBlock(); err != nil {
		t.Fatalf("MineBlock: %v", err)
	}

	// getChainHead advanced.
	head, err = client.GetChainHead()
	if err != nil {
		t.Fatalf("GetChainHead(2): %v", err)
	}
	if head.Height != 1 {
		t.Fatalf("head height after mine = %d, want 1", head.Height)
	}

	// getBalance(B) reflects the transfer.
	balB, err := client.GetBalance(b.Address().Hex())
	if err != nil {
		t.Fatalf("GetBalance(B): %v", err)
	}
	if balB != 400 {
		t.Fatalf("B balance = %d, want 400", balB)
	}
	balA, err := client.GetBalance(a.Address().Hex())
	if err != nil {
		t.Fatalf("GetBalance(A): %v", err)
	}
	if balA != 600 {
		t.Fatalf("A balance = %d, want 600", balA)
	}

	// getBlockByHeight returns the mined block containing the tx.
	blk, ok, err := client.GetBlockByHeight(1)
	if err != nil {
		t.Fatalf("GetBlockByHeight: %v", err)
	}
	if !ok {
		t.Fatalf("GetBlockByHeight(1) not found")
	}
	if len(blk.Txs) != 1 || blk.Txs[0].Hash != txHash {
		t.Fatalf("block does not contain the expected tx")
	}
	if blk.Header.Height != 1 {
		t.Fatalf("block height = %d, want 1", blk.Header.Height)
	}

	// getTxByHash finds the tx.
	gotTx, ok, err := client.GetTxByHash(txHash)
	if err != nil {
		t.Fatalf("GetTxByHash: %v", err)
	}
	if !ok {
		t.Fatalf("GetTxByHash(%s) not found", txHash)
	}
	if gotTx.To != b.Address().Hex() || gotTx.Value != 400 {
		t.Fatalf("tx mismatch: to=%s value=%d", gotTx.To, gotTx.Value)
	}

	// Unknown tx hash returns null (found=false).
	var zero core.Hash
	if _, ok, err := client.GetTxByHash(zero.Hex()); err != nil || ok {
		t.Fatalf("GetTxByHash(zero) = found %v err %v, want not found", ok, err)
	}
}

// TestBlockJSONCoinbaseRecomputesHash verifies the RPC block wire form carries
// the header Coinbase so a client can independently recompute Header.Hash() and
// match getChainHead's advertised hash.
func TestBlockJSONCoinbaseRecomputesHash(t *testing.T) {
	miner, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey(miner): %v", err)
	}
	funded, err := wallet.NewKey()
	if err != nil {
		t.Fatalf("NewKey(funded): %v", err)
	}

	const difficulty = 6
	n, err := node.New(node.Config{
		MinerKey:     miner,
		Difficulty:   difficulty,
		GenesisAlloc: map[core.Address]uint64{funded.Address(): 1000},
	})
	if err != nil {
		t.Fatalf("node.New: %v", err)
	}
	defer n.Close()

	srv := httptest.NewServer(rpc.NewServer(n))
	defer srv.Close()
	client := rpc.NewClient(srv.URL)

	if _, err := n.MineBlock(); err != nil {
		t.Fatalf("MineBlock: %v", err)
	}

	head, err := client.GetChainHead()
	if err != nil {
		t.Fatalf("GetChainHead: %v", err)
	}

	blk, ok, err := client.GetBlockByHeight(head.Height)
	if err != nil {
		t.Fatalf("GetBlockByHeight: %v", err)
	}
	if !ok {
		t.Fatalf("GetBlockByHeight(%d) not found", head.Height)
	}

	// Coinbase must be present and equal the miner address.
	if blk.Header.Coinbase != miner.Address().Hex() {
		t.Fatalf("block coinbase = %q, want %q", blk.Header.Coinbase, miner.Address().Hex())
	}

	// A client with only the wire fields can recompute Header.Hash().
	recon, err := rpc.HeaderFromJSON(blk.Header)
	if err != nil {
		t.Fatalf("HeaderFromJSON: %v", err)
	}
	if got := recon.Hash().Hex(); got != head.Hash {
		t.Fatalf("recomputed header hash = %s, want getChainHead hash %s", got, head.Hash)
	}
	if got := blk.Hash; got != head.Hash {
		t.Fatalf("block JSON hash = %s, want %s", got, head.Hash)
	}
}

// TestLargeValueWireIsStringAndRoundTrips proves item (D): tx amounts above the
// JS safe-integer limit (2^53) are carried on the JSON wire as DECIMAL STRINGS
// and parse back to the exact uint64 with no float64 precision loss.
func TestLargeValueWireIsStringAndRoundTrips(t *testing.T) {
	const huge = uint64(9_007_199_254_740_993)      // 2^53 + 1 (not exact as float64)
	const maxU64 = uint64(18446744073709551615)     // math.MaxUint64

	tx := core.Transaction{
		From:      core.Address{1},
		To:        core.Address{2},
		Value:     huge,
		Nonce:     maxU64,
		GasLimit:  huge,
		ChainID:   chain.DefaultChainID,
		Signature: []byte{1},
	}

	raw, err := json.Marshal(rpc.TxToJSON(tx))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The large integer fields MUST be quoted strings on the wire (not bare
	// numbers a JS parser would coerce to a lossy float64).
	for _, want := range []string{
		`"value":"9007199254740993"`,
		`"nonce":"18446744073709551615"`,
		`"gasLimit":"9007199254740993"`,
		`"chainId":"1337"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("wire form %s missing substring %s", raw, want)
		}
	}

	// It parses back to the exact uint64s (precision preserved).
	var back rpc.TxJSON
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tx2, err := rpc.TxFromJSON(back)
	if err != nil {
		t.Fatalf("TxFromJSON: %v", err)
	}
	if tx2.Value != huge || tx2.GasLimit != huge || tx2.Nonce != maxU64 || tx2.ChainID != chain.DefaultChainID {
		t.Fatalf("round-trip mismatch: value=%d gasLimit=%d nonce=%d chainId=%d",
			tx2.Value, tx2.GasLimit, tx2.Nonce, tx2.ChainID)
	}
	// Value survives the number that a naive JSON-number decode would corrupt:
	// float64(2^53+1) == float64(2^53), so a bare-number wire would read huge-1.
	if float64(huge) == float64(huge-1) && tx2.Value == huge-1 {
		t.Fatalf("precision lost: got %d", tx2.Value)
	}
}
