// Wire shapes mirrored EXACTLY from go-l1-chain/rpc/server.go.
// All hashes/addresses are lowercase hex WITHOUT a 0x prefix (see core.Hash.Hex
// / core.Address.Hex, which use hex.EncodeToString).

// getChainHead result: {"height":<u64>,"hash":"<hex>"}
export interface ChainHead {
  height: number;
  hash: string;
}

// HeaderJSON (rpc/server.go type HeaderJSON). The large u64 fields (height,
// nonce, baseFee, gasUsed -- the latter two added M9) are decimal strings on
// the wire; the rest stay numbers. proposerSig is a hex string (M8), empty
// for a PoW block.
export interface HeaderJSON {
  height: string;
  prevHash: string;
  merkleRoot: string;
  stateRoot: string;
  coinbase: string;
  timestamp: number;
  difficulty: number;
  nonce: string;
  baseFee: string;
  gasUsed: string;
  proposerSig: string;
}

// TxJSON (rpc/server.go type TxJSON). Data/Signature are hex strings (no 0x);
// value/nonce/gasLimit/chainId/gasFeeCap/gasTipCap (the latter two added M9)
// are DECIMAL STRINGS (Go `,string` tags) so the browser never loses
// precision above 2^53.
export interface TxJSON {
  from: string;
  to: string;
  value: string;
  nonce: string;
  gasLimit: string;
  chainId: string;
  gasFeeCap: string;
  gasTipCap: string;
  data: string;
  signature: string;
  hash: string;
}

// BlockJSON (rpc/server.go type BlockJSON)
export interface BlockJSON {
  header: HeaderJSON;
  hash: string;
  txs: TxJSON[];
}

// getOrderBookDepth result: aggregated price levels, best-first each side.
// price/qty are decimal strings (can be negative in principle -- the wire
// type is a signed int64 -- though a resting order's qty never is).
export interface LevelJSON {
  price: string;
  qty: string;
}

export interface OrderBookDepthJSON {
  bids: LevelJSON[];
  asks: LevelJSON[];
}

// getOrderBook result element: one resting order, individually addressed.
export interface OrderJSON {
  height: string;
  index: number;
  account: string;
  side: "buy" | "sell";
  price: string;
  qty: string;
}

// getLastAuction result, or null if no batch auction has ever cleared.
export interface LastAuctionJSON {
  price: string;
  volume: string;
  height: string;
}

// getExchangeBalance result.
export interface ExchangeBalanceJSON {
  base: string;
  lockedBase: string;
  lockedQuote: string;
}
