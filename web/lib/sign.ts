import { sha256 } from "@noble/hashes/sha256";
import * as secp from "@noble/secp256k1";
import type { TxJSON } from "./types";

// In-browser secp256k1 signing that reproduces go-l1-chain EXACTLY.
//
// Address derivation (wallet/wallet.go pubKeyAddress):
//   digest = SHA-256( uncompressed_pubkey_without_0x04_tag )   // X||Y, 64 bytes
//   address = last 20 bytes of digest
//
// Transaction SigningHash (core/transaction.go preimage(withSig=false)):
//   From(20) || To(20) || Value(u64 BE) || Nonce(u64 BE) || GasLimit(u64 BE) ||
//   ChainID(u64 BE) || GasFeeCap(u64 BE) || GasTipCap(u64 BE) || Data
//   then SHA-256 of that preimage. ChainID sits at a FIXED position right after
//   GasLimit and before Data, byte-for-byte matching core/transaction.go, so the
//   signature commits to the chain id (replay protection across chains).
//   GasFeeCap/GasTipCap (M9) sit right after ChainID, signed over the same way
//   -- a sender commits to its own fee ceiling and priority offer; neither can
//   be tampered with post-signature, exactly like every other field here.
//
// Signature (wallet/wallet.go Key.Sign -> dcrd ecdsa.SignCompact(priv, hash, true)):
//   65 bytes: [recovery byte][R(32)][S(32)]
//   recovery byte = 27 + recid + 4   (the +4 is dcrd's "compressed pubkey" flag,
//   because Sign passes isCompressedKey=true). dcrd's RecoverCompact (used by
//   wallet.Verify) expects this exact layout, and noble emits canonical low-S
//   signatures by default, matching dcrd.

function hexToBytes(h: string): Uint8Array {
  const s = h.startsWith("0x") || h.startsWith("0X") ? h.slice(2) : h;
  if (s.length % 2 !== 0) throw new Error("odd-length hex");
  const out = new Uint8Array(s.length / 2);
  for (let i = 0; i < out.length; i++) {
    out[i] = parseInt(s.slice(i * 2, i * 2 + 2), 16);
  }
  return out;
}

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

/** Derive the 20-byte account address (hex, no 0x) from a private key hex. */
export function addressFromPrivHex(privHex: string): string {
  const priv = hexToBytes(privHex);
  const uncompressed = secp.getPublicKey(priv, false); // 65 bytes: 0x04 || X || Y
  const digest = sha256(uncompressed.slice(1)); // hash X||Y (drop 0x04 tag)
  return bytesToHex(digest.slice(digest.length - 20)); // last 20 bytes
}

/**
 * CHAIN_ID is the replay-protection domain. It MUST match go-l1-chain's
 * chain.DefaultChainID (1337) and node Config.ChainID, or the node rejects the
 * signed tx with ErrBadChainID even though the signature is valid.
 */
export const CHAIN_ID = 1337n;

export interface UnsignedTx {
  to: string; // 20-byte hex (no 0x)
  value: bigint;
  nonce: bigint;
  gasLimit: bigint;
  chainId?: bigint; // defaults to CHAIN_ID when omitted
  gasFeeCap?: bigint; // M9, GasFeeCap: defaults to 0n (fee-exempt paths ignore it)
  gasTipCap?: bigint; // M9, GasTipCap: defaults to 0n (see gasFeeCap's own note)
  data?: Uint8Array;
}

function signingPreimage(from: Uint8Array, tx: UnsignedTx): Uint8Array {
  const to = hexToBytes(tx.to);
  if (from.length !== 20) throw new Error("from must be 20 bytes");
  if (to.length !== 20) throw new Error("to must be 20 bytes");
  const data = tx.data ?? new Uint8Array(0);
  const parts = [
    from,
    to,
    u64be(tx.value),
    u64be(tx.nonce),
    u64be(tx.gasLimit),
    u64be(tx.chainId ?? CHAIN_ID),
    u64be(tx.gasFeeCap ?? 0n),
    u64be(tx.gasTipCap ?? 0n),
    data,
  ];
  const total = parts.reduce((a, p) => a + p.length, 0);
  const buf = new Uint8Array(total);
  let off = 0;
  for (const p of parts) {
    buf.set(p, off);
    off += p.length;
  }
  return buf;
}

/** Build a fully-signed TxJSON ready for sendRawTx, matching the Go wire form. */
export async function signTx(privHex: string, tx: UnsignedTx): Promise<TxJSON> {
  const priv = hexToBytes(privHex);
  const fromHex = addressFromPrivHex(privHex);
  const from = hexToBytes(fromHex);

  const preimage = signingPreimage(from, tx);
  const signingHash = sha256(preimage); // core.SumHash == SHA-256

  const sig = await secp.signAsync(signingHash, priv); // canonical low-S
  const compact = sig.toCompactRawBytes(); // 64 bytes: R || S
  const recoveryByte = 27 + sig.recovery + 4; // dcrd compressed-key compact format
  const sig65 = new Uint8Array(65);
  sig65[0] = recoveryByte;
  sig65.set(compact, 1);

  const data = tx.data ?? new Uint8Array(0);
  return {
    from: fromHex,
    to: tx.to,
    // Large integer fields go on the wire as DECIMAL STRINGS (matching the Go
    // server's `,string` JSON tags) so nothing is truncated at 2^53.
    value: tx.value.toString(),
    nonce: tx.nonce.toString(),
    gasLimit: tx.gasLimit.toString(),
    chainId: (tx.chainId ?? CHAIN_ID).toString(),
    gasFeeCap: (tx.gasFeeCap ?? 0n).toString(),
    gasTipCap: (tx.gasTipCap ?? 0n).toString(),
    data: bytesToHex(data),
    signature: bytesToHex(sig65),
    hash: "", // recomputed server-side; TxFromJSON ignores it
  };
}
