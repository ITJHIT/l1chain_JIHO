// Package rpc exposes a minimal JSON-RPC 2.0 API over the Go stdlib net/http
// for a node.Node. It defines the wire request/response envelope, hex-based
// JSON representations of transactions and blocks (so the API is human-readable
// and CLI-friendly), and a tiny Client helper used by the CLI and tests.
//
// Supported methods:
//
//	getChainHead()                 -> {"height":<u64>, "hash":"<hex>"}
//	getBlockByHeight(height u64)   -> <block> | null
//	getBalance(addrHex string)     -> {"balance":<u64>}
//	sendRawTx(tx <TxJSON>)         -> {"txHash":"<hex>"}
//	getTxByHash(hashHex string)    -> <tx> | null
//	getOrderBookDepth()            -> {"bids":[{"price","qty"}...],"asks":[...]} best-first
//	getOrderBook()                 -> [{"height","index","account","side","price","qty"}...]
//	getLastAuction()               -> {"price","volume","height"} | null (never cleared / Continuous mode)
//	getExchangeBalance(addrHex)    -> {"base","lockedBase","lockedQuote"}
//	getAccountProof(addrHex)       -> {"height","stateRoot","proof":<AccountProofJSON>} | null
//	getStorageProof(addrHex, keyHex) -> {"height","stateRoot","proof":<StorageProofJSON>} | null
//
// getAccountProof/getStorageProof are the light-client primitive: the caller
// verifies the returned proof against a StateRoot it trusts on its own (e.g.
// from a header whose PoW it checked itself), rather than trusting this
// node's getBalance response blindly. See state.VerifyAccountProof /
// state.VerifyStorageProof.
//
// Request envelope (JSON-RPC 2.0), params is a positional array:
//
//	{"jsonrpc":"2.0","id":1,"method":"getBalance","params":["<addrHex>"]}
//
// Response envelope:
//
//	{"jsonrpc":"2.0","id":1,"result":<any>}          // success
//	{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"..."}}  // failure
package rpc

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"l1chain/core"
	"l1chain/exchange"
	"l1chain/node"
	"l1chain/state"
	"l1chain/trie"
)

// JSON-RPC 2.0 standard error codes used by this server.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// Request is a JSON-RPC 2.0 request envelope with positional params.
type Request struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id"`
	Method  string            `json:"method"`
	Params  []json.RawMessage `json:"params"`
}

// Response is a JSON-RPC 2.0 response envelope.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is the JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// TxJSON is the wire representation of a core.Transaction. Addresses, hashes,
// Data and Signature are hex strings; the large integer fields (Value, Nonce,
// GasLimit, ChainID, GasFeeCap, GasTipCap) are encoded as DECIMAL STRINGS via
// the encoding/json ",string" option so a JS client never loses precision
// above 2^53.
type TxJSON struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Value     uint64 `json:"value,string"`
	Nonce     uint64 `json:"nonce,string"`
	GasLimit  uint64 `json:"gasLimit,string"`
	ChainID   uint64 `json:"chainId,string"`
	GasFeeCap uint64 `json:"gasFeeCap,string"`
	GasTipCap uint64 `json:"gasTipCap,string"`
	Data      string `json:"data"`
	Signature string `json:"signature"`
	Hash      string `json:"hash"`
}

// HeaderJSON is the wire representation of a core.Header. Hashes/addresses are
// hex; the u64 fields that can grow large (Height, Nonce, BaseFee, GasUsed)
// are decimal strings. ProposerSig is a hex string like Data/Signature on
// TxJSON -- empty for every PoW block (M8), populated for a PoS block.
type HeaderJSON struct {
	Height      uint64 `json:"height,string"`
	PrevHash    string `json:"prevHash"`
	MerkleRoot  string `json:"merkleRoot"`
	StateRoot   string `json:"stateRoot"`
	Coinbase    string `json:"coinbase"`
	Timestamp   int64  `json:"timestamp"`
	Difficulty  uint32 `json:"difficulty"`
	Nonce       uint64 `json:"nonce,string"`
	BaseFee     uint64 `json:"baseFee,string"`
	GasUsed     uint64 `json:"gasUsed,string"`
	ProposerSig string `json:"proposerSig"`
}

// BlockJSON is the hex-encoded wire representation of a core.Block.
type BlockJSON struct {
	Header HeaderJSON `json:"header"`
	Hash   string     `json:"hash"`
	Txs    []TxJSON   `json:"txs"`
}

// AccountJSON is the wire representation of a state.Account.
type AccountJSON struct {
	Balance     uint64 `json:"balance,string"`
	Nonce       uint64 `json:"nonce,string"`
	CodeHash    string `json:"codeHash"`
	StorageRoot string `json:"storageRoot"`
}

// AccountProofJSON is the wire representation of a state.AccountProof: the
// claimed account plus an ordered root-to-leaf list of hex-encoded trie node
// encodings, verifiable offline against a StateRoot the caller trusts on its
// own (see state.VerifyAccountProof) -- this is the light-client primitive
// getAccountProof exists for.
type AccountProofJSON struct {
	Account AccountJSON `json:"account"`
	Proof   []string    `json:"proof"`
}

// StorageProofJSON is the wire representation of a state.StorageProof: a
// chained two-stage proof, the account proof plus a slot proof rooted at
// that account's own (proven) StorageRoot.
type StorageProofJSON struct {
	AccountProof AccountProofJSON `json:"accountProof"`
	SlotProof    []string         `json:"slotProof"`
	Value        string           `json:"value"`
}

func accountToJSON(a state.Account) AccountJSON {
	return AccountJSON{
		Balance:     a.Balance,
		Nonce:       a.Nonce,
		CodeHash:    a.CodeHash.Hex(),
		StorageRoot: a.StorageRoot.Hex(),
	}
}

func proofToJSON(p trie.Proof) []string {
	out := make([]string, len(p))
	for i, n := range p {
		out[i] = hex.EncodeToString(n)
	}
	return out
}

func accountProofToJSON(ap state.AccountProof) AccountProofJSON {
	return AccountProofJSON{Account: accountToJSON(ap.Account), Proof: proofToJSON(ap.Proof)}
}

func storageProofToJSON(sp state.StorageProof) StorageProofJSON {
	return StorageProofJSON{
		AccountProof: accountProofToJSON(sp.AccountProof),
		SlotProof:    proofToJSON(sp.SlotProof),
		Value:        sp.Value.Hex(),
	}
}

// accountFromJSON, proofFromJSON and the Account/StorageProofFromJSON below
// exist for the verifying side (the CLI light-client path): they turn the
// wire form back into the Go types state.VerifyAccountProof/
// VerifyStorageProof actually check.

func accountFromJSON(j AccountJSON) (state.Account, error) {
	codeHash, err := hashFromHex(j.CodeHash)
	if err != nil {
		return state.Account{}, fmt.Errorf("codeHash: %w", err)
	}
	storageRoot, err := hashFromHex(j.StorageRoot)
	if err != nil {
		return state.Account{}, fmt.Errorf("storageRoot: %w", err)
	}
	return state.Account{Balance: j.Balance, Nonce: j.Nonce, CodeHash: codeHash, StorageRoot: storageRoot}, nil
}

func proofFromJSON(j []string) (trie.Proof, error) {
	out := make(trie.Proof, len(j))
	for i, s := range j {
		b, err := hexBytes(s)
		if err != nil {
			return nil, fmt.Errorf("proof[%d]: %w", i, err)
		}
		out[i] = b
	}
	return out, nil
}

func accountProofFromJSON(j AccountProofJSON) (state.AccountProof, error) {
	acct, err := accountFromJSON(j.Account)
	if err != nil {
		return state.AccountProof{}, err
	}
	proof, err := proofFromJSON(j.Proof)
	if err != nil {
		return state.AccountProof{}, err
	}
	return state.AccountProof{Account: acct, Proof: proof}, nil
}

func storageProofFromJSON(j StorageProofJSON) (state.StorageProof, error) {
	ap, err := accountProofFromJSON(j.AccountProof)
	if err != nil {
		return state.StorageProof{}, err
	}
	slotProof, err := proofFromJSON(j.SlotProof)
	if err != nil {
		return state.StorageProof{}, err
	}
	value, err := hashFromHex(j.Value)
	if err != nil {
		return state.StorageProof{}, fmt.Errorf("value: %w", err)
	}
	return state.StorageProof{AccountProof: ap, SlotProof: slotProof, Value: value}, nil
}

// ---- conversions -------------------------------------------------------------

// TxToJSON converts a core.Transaction to its wire form.
func TxToJSON(tx core.Transaction) TxJSON {
	h := tx.Hash()
	return TxJSON{
		From:      tx.From.Hex(),
		To:        tx.To.Hex(),
		Value:     tx.Value,
		Nonce:     tx.Nonce,
		GasLimit:  tx.GasLimit,
		ChainID:   tx.ChainID,
		GasFeeCap: tx.GasFeeCap,
		GasTipCap: tx.GasTipCap,
		Data:      hex.EncodeToString(tx.Data),
		Signature: hex.EncodeToString(tx.Signature),
		Hash:      h.Hex(),
	}
}

// TxFromJSON converts a wire tx to a core.Transaction. The Hash field is
// ignored (recomputed from content).
func TxFromJSON(j TxJSON) (core.Transaction, error) {
	from, err := addressFromHex(j.From)
	if err != nil {
		return core.Transaction{}, fmt.Errorf("from: %w", err)
	}
	to, err := addressFromHex(j.To)
	if err != nil {
		return core.Transaction{}, fmt.Errorf("to: %w", err)
	}
	data, err := hexBytes(j.Data)
	if err != nil {
		return core.Transaction{}, fmt.Errorf("data: %w", err)
	}
	sig, err := hexBytes(j.Signature)
	if err != nil {
		return core.Transaction{}, fmt.Errorf("signature: %w", err)
	}
	return core.Transaction{
		From:      from,
		To:        to,
		Value:     j.Value,
		Nonce:     j.Nonce,
		GasLimit:  j.GasLimit,
		ChainID:   j.ChainID,
		GasFeeCap: j.GasFeeCap,
		GasTipCap: j.GasTipCap,
		Data:      data,
		Signature: sig,
	}, nil
}

// BlockToJSON converts a core.Block to its wire form.
func BlockToJSON(b core.Block) BlockJSON {
	txs := make([]TxJSON, len(b.Txs))
	for i := range b.Txs {
		txs[i] = TxToJSON(b.Txs[i])
	}
	return BlockJSON{
		Header: HeaderJSON{
			Height:      b.Header.Height,
			PrevHash:    b.Header.PrevHash.Hex(),
			MerkleRoot:  b.Header.MerkleRoot.Hex(),
			StateRoot:   b.Header.StateRoot.Hex(),
			Coinbase:    b.Header.Coinbase.Hex(),
			Timestamp:   b.Header.Timestamp,
			Difficulty:  b.Header.Difficulty,
			Nonce:       b.Header.Nonce,
			BaseFee:     b.Header.BaseFee,
			GasUsed:     b.Header.GasUsed,
			ProposerSig: hex.EncodeToString(b.Header.ProposerSig),
		},
		Hash: b.Hash().Hex(),
		Txs:  txs,
	}
}

// HeaderFromJSON reconstructs a core.Header from its wire form. It lets a client
// recompute Header.Hash() from the returned fields (including Coinbase and, for
// a PoS block, ProposerSig -- Hash() folds it in as of M8, see core/block.go).
func HeaderFromJSON(j HeaderJSON) (core.Header, error) {
	prevHash, err := hashFromHex(j.PrevHash)
	if err != nil {
		return core.Header{}, fmt.Errorf("prevHash: %w", err)
	}
	merkleRoot, err := hashFromHex(j.MerkleRoot)
	if err != nil {
		return core.Header{}, fmt.Errorf("merkleRoot: %w", err)
	}
	stateRoot, err := hashFromHex(j.StateRoot)
	if err != nil {
		return core.Header{}, fmt.Errorf("stateRoot: %w", err)
	}
	coinbase, err := addressFromHex(j.Coinbase)
	if err != nil {
		return core.Header{}, fmt.Errorf("coinbase: %w", err)
	}
	proposerSig, err := hexBytes(j.ProposerSig)
	if err != nil {
		return core.Header{}, fmt.Errorf("proposerSig: %w", err)
	}
	return core.Header{
		Height:      j.Height,
		PrevHash:    prevHash,
		MerkleRoot:  merkleRoot,
		StateRoot:   stateRoot,
		Coinbase:    coinbase,
		Timestamp:   j.Timestamp,
		Difficulty:  j.Difficulty,
		Nonce:       j.Nonce,
		BaseFee:     j.BaseFee,
		GasUsed:     j.GasUsed,
		ProposerSig: proposerSig,
	}, nil
}

// BlockFromJSON reconstructs a core.Block from its wire form.
func BlockFromJSON(j BlockJSON) (core.Block, error) {
	header, err := HeaderFromJSON(j.Header)
	if err != nil {
		return core.Block{}, err
	}
	txs := make([]core.Transaction, len(j.Txs))
	for i := range j.Txs {
		tx, err := TxFromJSON(j.Txs[i])
		if err != nil {
			return core.Block{}, fmt.Errorf("tx %d: %w", i, err)
		}
		txs[i] = tx
	}
	return core.Block{Header: header, Txs: txs}, nil
}

func addressFromHex(s string) (core.Address, error) {
	var a core.Address
	b, err := hexBytes(s)
	if err != nil {
		return a, err
	}
	if len(b) != core.AddrLen {
		return a, fmt.Errorf("address must be %d bytes, got %d", core.AddrLen, len(b))
	}
	copy(a[:], b)
	return a, nil
}

func hashFromHex(s string) (core.Hash, error) {
	var h core.Hash
	b, err := hexBytes(s)
	if err != nil {
		return h, err
	}
	if len(b) != core.HashLen {
		return h, fmt.Errorf("hash must be %d bytes, got %d", core.HashLen, len(b))
	}
	copy(h[:], b)
	return h, nil
}

// hexBytes decodes an optionally-"0x"-prefixed hex string ("" -> nil).
func hexBytes(s string) ([]byte, error) {
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		s = s[2:]
	}
	if s == "" {
		return nil, nil
	}
	return hex.DecodeString(s)
}

// ---- server ------------------------------------------------------------------

type server struct {
	node *node.Node
}

// NewServer returns an http.Handler serving the JSON-RPC API over n. Mount it at
// any path (e.g. http.Handle("/", rpc.NewServer(n))).
func NewServer(n *node.Node) http.Handler {
	return &server{node: n}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, nil, codeInvalidRequest, "only POST is supported")
		return
	}
	defer r.Body.Close()

	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, nil, codeParseError, "parse error: "+err.Error())
		return
	}

	result, rpcErr := s.dispatch(req)
	if rpcErr != nil {
		writeErr(w, req.ID, rpcErr.Code, rpcErr.Message)
		return
	}
	writeResult(w, req.ID, result)
}

func (s *server) dispatch(req Request) (any, *RPCError) {
	switch req.Method {
	case "getChainHead":
		head := s.node.Head()
		return map[string]any{
			"height": strconv.FormatUint(head.Header.Height, 10),
			"hash":   head.Hash().Hex(),
		}, nil

	case "getBlockByHeight":
		var height uint64
		if err := decodeParam(req.Params, 0, &height); err != nil {
			return nil, &RPCError{codeInvalidParams, err.Error()}
		}
		b, ok := s.node.GetBlockByHeight(height)
		if !ok {
			return nil, nil // JSON null
		}
		return BlockToJSON(b), nil

	case "getBalance":
		var addrHex string
		if err := decodeParam(req.Params, 0, &addrHex); err != nil {
			return nil, &RPCError{codeInvalidParams, err.Error()}
		}
		addr, err := addressFromHex(addrHex)
		if err != nil {
			return nil, &RPCError{codeInvalidParams, err.Error()}
		}
		return map[string]any{"balance": strconv.FormatUint(s.node.Balance(addr), 10)}, nil

	case "sendRawTx":
		var txj TxJSON
		if err := decodeParam(req.Params, 0, &txj); err != nil {
			return nil, &RPCError{codeInvalidParams, err.Error()}
		}
		tx, err := TxFromJSON(txj)
		if err != nil {
			return nil, &RPCError{codeInvalidParams, err.Error()}
		}
		if err := s.node.SubmitTx(tx); err != nil {
			return nil, &RPCError{codeInvalidParams, err.Error()}
		}
		return map[string]any{"txHash": tx.Hash().Hex()}, nil

	case "getTxByHash":
		var hashHex string
		if err := decodeParam(req.Params, 0, &hashHex); err != nil {
			return nil, &RPCError{codeInvalidParams, err.Error()}
		}
		h, err := hashFromHex(hashHex)
		if err != nil {
			return nil, &RPCError{codeInvalidParams, err.Error()}
		}
		tx, ok := s.node.GetTxByHash(h)
		if !ok {
			return nil, nil // JSON null
		}
		return TxToJSON(tx), nil

	case "getOrderBookDepth":
		bids, asks, err := s.node.OrderBookDepth()
		if err != nil {
			return nil, &RPCError{codeInternalError, err.Error()}
		}
		return map[string]any{"bids": levelsToJSON(bids), "asks": levelsToJSON(asks)}, nil

	case "getOrderBook":
		views, err := s.node.OrderBookSnapshot()
		if err != nil {
			return nil, &RPCError{codeInternalError, err.Error()}
		}
		out := make([]map[string]any, len(views))
		for i, v := range views {
			out[i] = map[string]any{
				"height":  strconv.FormatUint(v.Height, 10),
				"index":   v.Index,
				"account": v.Account.Hex(),
				"side":    v.Side.String(),
				"price":   strconv.FormatInt(int64(v.Price), 10),
				"qty":     strconv.FormatInt(int64(v.Qty), 10),
			}
		}
		return out, nil

	case "getLastAuction":
		price, volume, height, ok := s.node.LastAuction()
		if !ok {
			return nil, nil // JSON null: no batch auction has cleared yet
		}
		return map[string]any{
			"price":  strconv.FormatInt(price, 10),
			"volume": strconv.FormatInt(volume, 10),
			"height": strconv.FormatUint(height, 10),
		}, nil

	case "getExchangeBalance":
		var addrHex string
		if err := decodeParam(req.Params, 0, &addrHex); err != nil {
			return nil, &RPCError{codeInvalidParams, err.Error()}
		}
		addr, err := addressFromHex(addrHex)
		if err != nil {
			return nil, &RPCError{codeInvalidParams, err.Error()}
		}
		base, lockedBase, lockedQuote := s.node.ExchangeBalance(addr)
		return map[string]any{
			"base":        strconv.FormatUint(base, 10),
			"lockedBase":  strconv.FormatUint(lockedBase, 10),
			"lockedQuote": strconv.FormatUint(lockedQuote, 10),
		}, nil

	case "getAccountProof":
		var addrHex string
		if err := decodeParam(req.Params, 0, &addrHex); err != nil {
			return nil, &RPCError{codeInvalidParams, err.Error()}
		}
		addr, err := addressFromHex(addrHex)
		if err != nil {
			return nil, &RPCError{codeInvalidParams, err.Error()}
		}
		ap, height, root, ok := s.node.AccountProof(addr)
		if !ok {
			return nil, nil // JSON null: no account at this address
		}
		return map[string]any{
			"height":    strconv.FormatUint(height, 10),
			"stateRoot": root.Hex(),
			"proof":     accountProofToJSON(ap),
		}, nil

	case "getStorageProof":
		var addrHex, keyHex string
		if err := decodeParam(req.Params, 0, &addrHex); err != nil {
			return nil, &RPCError{codeInvalidParams, err.Error()}
		}
		if err := decodeParam(req.Params, 1, &keyHex); err != nil {
			return nil, &RPCError{codeInvalidParams, err.Error()}
		}
		addr, err := addressFromHex(addrHex)
		if err != nil {
			return nil, &RPCError{codeInvalidParams, err.Error()}
		}
		key, err := hashFromHex(keyHex)
		if err != nil {
			return nil, &RPCError{codeInvalidParams, err.Error()}
		}
		sp, height, root, ok := s.node.StorageProof(addr, key)
		if !ok {
			return nil, nil // JSON null: no account, or the slot was never set
		}
		return map[string]any{
			"height":    strconv.FormatUint(height, 10),
			"stateRoot": root.Hex(),
			"proof":     storageProofToJSON(sp),
		}, nil

	default:
		return nil, &RPCError{codeMethodNotFound, "unknown method: " + req.Method}
	}
}

// levelsToJSON converts aggregated price levels to their wire form. Price and
// qty are decimal strings for the same reason TxJSON's large integers are: a
// JS client's Number cannot hold an int64 exactly above 2^53.
func levelsToJSON(levels []exchange.Level) []map[string]any {
	out := make([]map[string]any, len(levels))
	for i, lv := range levels {
		out[i] = map[string]any{
			"price": strconv.FormatInt(int64(lv.Price), 10),
			"qty":   strconv.FormatInt(int64(lv.Qty), 10),
		}
	}
	return out
}

// decodeParam decodes the i-th positional param into dst.
func decodeParam(params []json.RawMessage, i int, dst any) error {
	if i >= len(params) {
		return fmt.Errorf("missing param at index %d", i)
	}
	return json.Unmarshal(params[i], dst)
}

func writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		writeErr(w, id, codeInternalError, err.Error())
		return
	}
	writeJSON(w, Response{JSONRPC: "2.0", ID: id, Result: raw})
}

func writeErr(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	writeJSON(w, Response{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}})
}

func writeJSON(w http.ResponseWriter, resp Response) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
