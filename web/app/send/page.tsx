"use client";

import { useState } from "react";
import { sendRawTx } from "@/lib/rpc";
import { addressFromPrivHex, signTx } from "@/lib/sign";

// SEND-FORM SIGNING DECISION
// --------------------------
// We sign IN THE BROWSER with @noble/secp256k1 + @noble/hashes, reproducing the
// Go node's conventions exactly (see web/lib/sign.ts):
//   - address = last 20 bytes of SHA-256(uncompressed pubkey X||Y)   [wallet.go]
//   - SigningHash = SHA-256(From||To||Value||Nonce||GasLimit||Data)  [transaction.go]
//   - 65-byte dcrd-compatible recoverable compact signature          [wallet.go Sign]
// The signed TxJSON is submitted via the same-origin /api/rpc proxy -> sendRawTx.
// The private key never leaves the browser except as a signature; it is NOT sent
// to the server.

export default function SendPage() {
  const [key, setKey] = useState("");
  const [to, setTo] = useState("");
  const [value, setValue] = useState("1");
  const [nonce, setNonce] = useState("0");
  const [from, setFrom] = useState("");
  const [result, setResult] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  function deriveFrom(k: string) {
    setKey(k);
    try {
      setFrom(k.trim() ? addressFromPrivHex(k.trim()) : "");
      setErr(null);
    } catch {
      setFrom("");
    }
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr(null);
    setResult(null);
    setBusy(true);
    try {
      const tx = await signTx(key.trim(), {
        to: to.trim(),
        value: BigInt(value || "0"),
        nonce: BigInt(nonce || "0"),
        gasLimit: 21000n,
      });
      const txHash = await sendRawTx(tx);
      setResult(txHash);
    } catch (e2) {
      setErr(String(e2));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main>
      <h1>Send transaction</h1>
      <p className="muted">
        Signed in-browser (secp256k1). Address derivation and signature layout
        reproduce the Go node exactly. The private key is only used locally to sign.
      </p>
      <form className="panel" onSubmit={submit}>
        <div className="field">
          <label htmlFor="key">Sender private key (hex)</label>
          <input
            id="key"
            data-testid="send-key"
            value={key}
            onChange={(e) => deriveFrom(e.target.value)}
          />
        </div>
        <div className="field">
          <label>Derived from address</label>
          <div className="mono" data-testid="send-from">{from || "—"}</div>
        </div>
        <div className="field">
          <label htmlFor="to">To (20-byte hex)</label>
          <input id="to" data-testid="send-to" value={to} onChange={(e) => setTo(e.target.value)} />
        </div>
        <div className="row">
          <div className="field" style={{ flex: 1 }}>
            <label htmlFor="value">Value</label>
            <input id="value" data-testid="send-value" value={value} onChange={(e) => setValue(e.target.value)} />
          </div>
          <div className="field" style={{ flex: 1 }}>
            <label htmlFor="nonce">Nonce</label>
            <input id="nonce" data-testid="send-nonce" value={nonce} onChange={(e) => setNonce(e.target.value)} />
          </div>
        </div>
        <button type="submit" data-testid="send-submit" disabled={busy}>
          {busy ? "Signing & sending…" : "Sign & send"}
        </button>
      </form>

      {err && <p className="err" data-testid="error">{err}</p>}
      {result && (
        <div className="panel">
          <div className="ok">Submitted.</div>
          <div className="muted">txHash</div>
          <div className="mono" data-testid="send-result">{result}</div>
        </div>
      )}
    </main>
  );
}
