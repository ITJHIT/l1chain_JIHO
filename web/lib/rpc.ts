import type { BlockJSON, ChainHead, TxJSON } from "./types";

// Typed JSON-RPC 2.0 client for the L1 node.
//
// The browser always talks to the SAME-ORIGIN proxy at /api/rpc (see
// app/api/rpc/route.ts) which forwards to the Go node (NEXT_PUBLIC_RPC_URL,
// default http://127.0.0.1:8545). Server-side callers may override the base.
//
// Request/response envelope + method names + param shapes mirror
// go-l1-chain/rpc/server.go and rpc/client.go exactly:
//   {"jsonrpc":"2.0","id":1,"method":"<m>","params":[...]}  (positional params)
//   {"jsonrpc":"2.0","id":1,"result":<any>}                 (success)
//   {"jsonrpc":"2.0","id":1,"error":{"code":<n>,"message":"..."}} (failure)

export const RPC_PROXY_PATH = "/api/rpc";

interface RpcResponse<T> {
  jsonrpc: string;
  id: unknown;
  result?: T;
  error?: { code: number; message: string };
}

let idSeq = 1;

async function rpcCall<T>(
  method: string,
  params: unknown[],
  base: string = RPC_PROXY_PATH,
): Promise<T | null> {
  const res = await fetch(base, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: idSeq++, method, params }),
    cache: "no-store",
  });
  if (!res.ok && res.status >= 500) {
    // The proxy returns a JSON-RPC error body even on 502; try to parse it.
  }
  const data = (await res.json()) as RpcResponse<T>;
  if (data.error) {
    throw new Error(`RPC ${method}: [${data.error.code}] ${data.error.message}`);
  }
  // The Go server returns JSON `null` for not-found (getBlockByHeight /
  // getTxByHash). Preserve that as `null`.
  return data.result === undefined ? null : data.result;
}

// getChainHead() -> {height, hash}. The wire encodes height as a decimal string
// (Go `,string` tag); parse it to a number for the UI (chain height is small).
export async function getChainHead(base?: string): Promise<ChainHead> {
  const r = await rpcCall<{ height: string; hash: string }>("getChainHead", [], base);
  if (!r) throw new Error("getChainHead returned null");
  return { height: Number(r.height), hash: r.hash };
}

// getBlockByHeight(height u64) -> <block> | null
export async function getBlockByHeight(
  height: number,
  base?: string,
): Promise<BlockJSON | null> {
  return rpcCall<BlockJSON>("getBlockByHeight", [height], base);
}

// getBalance(addrHex) -> {balance}. Returned as a DECIMAL STRING for
// full-precision display (balances can exceed 2^53).
export async function getBalance(addrHex: string, base?: string): Promise<string> {
  const r = await rpcCall<{ balance: string }>("getBalance", [addrHex], base);
  if (!r) throw new Error("getBalance returned null");
  return r.balance;
}

// getTxByHash(hashHex) -> <tx> | null
export async function getTxByHash(
  hashHex: string,
  base?: string,
): Promise<TxJSON | null> {
  return rpcCall<TxJSON>("getTxByHash", [hashHex], base);
}

// sendRawTx(tx <TxJSON>) -> {txHash}
export async function sendRawTx(tx: TxJSON, base?: string): Promise<string> {
  const r = await rpcCall<{ txHash: string }>("sendRawTx", [tx], base);
  if (!r) throw new Error("sendRawTx returned null");
  return r.txHash;
}
