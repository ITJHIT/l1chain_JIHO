package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"l1chain/core"
	"l1chain/state"
)

// Client is a tiny JSON-RPC client for the API served by NewServer. It is used
// by the CLI (cmd/l1) and the end-to-end test.
type Client struct {
	URL  string
	HTTP *http.Client
}

// NewClient returns a Client targeting the given base URL (e.g.
// "http://127.0.0.1:8545").
func NewClient(url string) *Client {
	return &Client{URL: url, HTTP: http.DefaultClient}
}

// call performs a single JSON-RPC request and unmarshals result into out (unless
// out is nil). params are positional.
func (c *Client) call(method string, out any, params ...any) error {
	rawParams := make([]json.RawMessage, len(params))
	for i, p := range params {
		b, err := json.Marshal(p)
		if err != nil {
			return err
		}
		rawParams[i] = b
	}
	reqBody, err := json.Marshal(Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  method,
		Params:  rawParams,
	})
	if err != nil {
		return err
	}

	httpResp, err := c.HTTP.Post(c.URL, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	var resp Response
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return err
	}
	if resp.Error != nil {
		return resp.Error
	}
	if out != nil {
		if len(resp.Result) == 0 || string(resp.Result) == "null" {
			return nil // leave out at its zero value
		}
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}

// ChainHead is the getChainHead result.
type ChainHead struct {
	Height uint64 `json:"height,string"`
	Hash   string `json:"hash"`
}

// GetChainHead returns the current head height and hash.
func (c *Client) GetChainHead() (ChainHead, error) {
	var h ChainHead
	err := c.call("getChainHead", &h)
	return h, err
}

// GetBalance returns the balance of the address (hex).
func (c *Client) GetBalance(addrHex string) (uint64, error) {
	var out struct {
		Balance uint64 `json:"balance,string"`
	}
	if err := c.call("getBalance", &out, addrHex); err != nil {
		return 0, err
	}
	return out.Balance, nil
}

// GetBlockByHeight returns the block at height. found is false for a null result.
func (c *Client) GetBlockByHeight(height uint64) (BlockJSON, bool, error) {
	var b BlockJSON
	if err := c.call("getBlockByHeight", &b, height); err != nil {
		return BlockJSON{}, false, err
	}
	if b.Hash == "" {
		return BlockJSON{}, false, nil
	}
	return b, true, nil
}

// SendRawTx submits a signed transaction and returns its hash (hex).
func (c *Client) SendRawTx(tx core.Transaction) (string, error) {
	var out struct {
		TxHash string `json:"txHash"`
	}
	if err := c.call("sendRawTx", &out, TxToJSON(tx)); err != nil {
		return "", err
	}
	return out.TxHash, nil
}

// GetTxByHash returns the tx with hash (hex). found is false for a null result.
func (c *Client) GetTxByHash(hashHex string) (TxJSON, bool, error) {
	var t TxJSON
	if err := c.call("getTxByHash", &t, hashHex); err != nil {
		return TxJSON{}, false, err
	}
	if t.Hash == "" {
		return TxJSON{}, false, nil
	}
	return t, true, nil
}

// LevelJSON is one aggregated price level.
type LevelJSON struct {
	Price int64 `json:"price,string"`
	Qty   int64 `json:"qty,string"`
}

// OrderBookDepth is the getOrderBookDepth result: bids and asks, best-first.
type OrderBookDepth struct {
	Bids []LevelJSON `json:"bids"`
	Asks []LevelJSON `json:"asks"`
}

// GetOrderBookDepth returns the on-chain exchange's resting orders aggregated
// into price levels.
func (c *Client) GetOrderBookDepth() (OrderBookDepth, error) {
	var d OrderBookDepth
	err := c.call("getOrderBookDepth", &d)
	return d, err
}

// OrderJSON is one resting order.
type OrderJSON struct {
	Height  uint64 `json:"height,string"`
	Index   uint32 `json:"index"`
	Account string `json:"account"`
	Side    string `json:"side"`
	Price   int64  `json:"price,string"`
	Qty     int64  `json:"qty,string"`
}

// GetOrderBook returns every resting order individually, owner included.
func (c *Client) GetOrderBook() ([]OrderJSON, error) {
	var out []OrderJSON
	err := c.call("getOrderBook", &out)
	return out, err
}

// LastAuction is the getLastAuction result.
type LastAuction struct {
	Price  int64  `json:"price,string"`
	Volume int64  `json:"volume,string"`
	Height uint64 `json:"height,string"`
}

// GetLastAuction returns the most recent batch-auction clear. found is false
// if none has ever cleared (a fresh chain, or one running Continuous mode).
func (c *Client) GetLastAuction() (LastAuction, bool, error) {
	var a LastAuction
	if err := c.call("getLastAuction", &a); err != nil {
		return LastAuction{}, false, err
	}
	if a.Height == 0 && a.Price == 0 && a.Volume == 0 {
		return LastAuction{}, false, nil
	}
	return a, true, nil
}

// ExchangeBalance is the getExchangeBalance result.
type ExchangeBalance struct {
	Base        uint64 `json:"base,string"`
	LockedBase  uint64 `json:"lockedBase,string"`
	LockedQuote uint64 `json:"lockedQuote,string"`
}

// GetExchangeBalance returns addr's (hex) on-chain-exchange holdings.
func (c *Client) GetExchangeBalance(addrHex string) (ExchangeBalance, error) {
	var b ExchangeBalance
	err := c.call("getExchangeBalance", &b, addrHex)
	return b, err
}

// AccountProofResult is the getAccountProof result: the proof, plus the
// height/stateRoot it was generated against (so the caller can fetch that
// exact block's header and verify the two actually agree).
type AccountProofResult struct {
	Height    uint64           `json:"height,string"`
	StateRoot string           `json:"stateRoot"`
	Proof     AccountProofJSON `json:"proof"`
}

// GetAccountProof returns a Merkle proof of addr's (hex) account under the
// node's canonical head. found is false for a null result (no account at
// that address). This is the raw, unverified server response -- call
// VerifyAccountProof separately (against a StateRoot the caller trusts on
// its own) to actually check it rather than trust the node blindly.
func (c *Client) GetAccountProof(addrHex string) (AccountProofResult, bool, error) {
	var out AccountProofResult
	if err := c.call("getAccountProof", &out, addrHex); err != nil {
		return AccountProofResult{}, false, err
	}
	if out.StateRoot == "" {
		return AccountProofResult{}, false, nil
	}
	return out, true, nil
}

// StorageProofResult is the getStorageProof result.
type StorageProofResult struct {
	Height    uint64           `json:"height,string"`
	StateRoot string           `json:"stateRoot"`
	Proof     StorageProofJSON `json:"proof"`
}

// GetStorageProof returns a Merkle proof of addr's (hex) storage at key
// (hex) under the node's canonical head.
func (c *Client) GetStorageProof(addrHex, keyHex string) (StorageProofResult, bool, error) {
	var out StorageProofResult
	if err := c.call("getStorageProof", &out, addrHex, keyHex); err != nil {
		return StorageProofResult{}, false, err
	}
	if out.StateRoot == "" {
		return StorageProofResult{}, false, nil
	}
	return out, true, nil
}

// VerifyAccountProof decodes ap and checks it against stateRootHex/addrHex
// independently -- the caller supplies a StateRoot it trusts on its own
// (e.g. from a header whose PoW it verified itself), not one taken from the
// same response being checked. This is the actual light-client check: a
// node cannot make this pass by lying in getBalance/getAccountProof unless
// it can also forge a proof against a root it does not control.
func VerifyAccountProof(stateRootHex, addrHex string, ap AccountProofJSON) (state.Account, bool, error) {
	stateRoot, err := hashFromHex(stateRootHex)
	if err != nil {
		return state.Account{}, false, fmt.Errorf("stateRoot: %w", err)
	}
	addr, err := addressFromHex(addrHex)
	if err != nil {
		return state.Account{}, false, fmt.Errorf("addr: %w", err)
	}
	proof, err := accountProofFromJSON(ap)
	if err != nil {
		return state.Account{}, false, fmt.Errorf("proof: %w", err)
	}
	return proof.Account, state.VerifyAccountProof(stateRoot, addr, proof), nil
}

// VerifyStorageProof is VerifyAccountProof for a getStorageProof result.
func VerifyStorageProof(stateRootHex, addrHex, keyHex string, sp StorageProofJSON) (core.Hash, bool, error) {
	stateRoot, err := hashFromHex(stateRootHex)
	if err != nil {
		return core.Hash{}, false, fmt.Errorf("stateRoot: %w", err)
	}
	addr, err := addressFromHex(addrHex)
	if err != nil {
		return core.Hash{}, false, fmt.Errorf("addr: %w", err)
	}
	key, err := hashFromHex(keyHex)
	if err != nil {
		return core.Hash{}, false, fmt.Errorf("key: %w", err)
	}
	proof, err := storageProofFromJSON(sp)
	if err != nil {
		return core.Hash{}, false, fmt.Errorf("proof: %w", err)
	}
	return proof.Value, state.VerifyStorageProof(stateRoot, addr, key, proof), nil
}
