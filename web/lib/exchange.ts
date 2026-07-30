// Client-side mirror of go-l1-chain/exchange/exchange.go's calldata encoding.
// Reproduces it EXACTLY, the same way sign.ts reproduces transaction signing:
// this is what turns "place an order" into the calldata of an ordinary
// core.Transaction addressed to the exchange, so nothing about signing or
// submission has to change to support it.
//
// Calldata layout (exchange.go EncodePlace / EncodeCancel):
//   place:  op(1)=1 || side(1: 0=buy,1=sell) || price(8 BE) || qty(8 BE)
//   cancel: op(1)=2 || height(8 BE) || index(4 BE)

// The reserved account exchange.Address routes to instead of the VM
// (exchange.go): 0xEC, 0x0B, then 16 zero bytes, then 0x01.
export const EXCHANGE_ADDRESS = "ec0b000000000000000000000000000000000001";

export type Side = "buy" | "sell";

function bytesToHex(b: Uint8Array): string {
  let s = "";
  for (const x of b) s += x.toString(16).padStart(2, "0");
  return s;
}

function u64be(n: bigint): Uint8Array {
  const out = new Uint8Array(8);
  let v = n;
  for (let i = 7; i >= 0; i--) {
    out[i] = Number(v & 0xffn);
    v >>= 8n;
  }
  return out;
}

/** Calldata (hex, no 0x) for a limit-order placement. */
export function encodePlace(side: Side, price: bigint, qty: bigint): string {
  const out = new Uint8Array(18);
  out[0] = 1; // OpPlace
  out[1] = side === "sell" ? 1 : 0;
  out.set(u64be(price), 2);
  out.set(u64be(qty), 10);
  return bytesToHex(out);
}

/** Calldata (hex, no 0x) to cancel a resting order identified by (height, index). */
export function encodeCancel(height: bigint, index: number): string {
  const out = new Uint8Array(13);
  out[0] = 2; // OpCancel
  out.set(u64be(height), 1);
  out[9] = (index >>> 24) & 0xff;
  out[10] = (index >>> 16) & 0xff;
  out[11] = (index >>> 8) & 0xff;
  out[12] = index & 0xff;
  return bytesToHex(out);
}
