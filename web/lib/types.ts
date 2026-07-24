// Wire shapes mirrored EXACTLY from go-l1-chain/rpc/server.go.
// All hashes/addresses are lowercase hex WITHOUT a 0x prefix (see core.Hash.Hex
// / core.Address.Hex, which use hex.EncodeToString).

// getChainHead result: {"height":<u64>,"hash":"<hex>"}
export interface ChainHead {
  height: number;
  hash: string;
}

// HeaderJSON (rpc/server.go type HeaderJSON)
export interface HeaderJSON {
  height: number;
  prevHash: string;
  merkleRoot: string;
  stateRoot: string;
  coinbase: string;
  timestamp: number;
  difficulty: number;
  nonce: number;
}

// TxJSON (rpc/server.go type TxJSON). Data/Signature are hex strings (no 0x).
export interface TxJSON {
  from: string;
  to: string;
  value: number;
  nonce: number;
  gasLimit: number;
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
