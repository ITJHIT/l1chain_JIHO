package rpc

import (
	"bytes"
	"encoding/json"
	"net/http"

	"l1chain/core"
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
