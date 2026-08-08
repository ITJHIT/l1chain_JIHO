"use client";

import { useSearchParams } from "next/navigation";
import { Suspense, useEffect, useState } from "react";
import { getTxByHash } from "@/lib/rpc";
import type { TxJSON } from "@/lib/types";

function TxLookup() {
  const params = useSearchParams();
  const [hash, setHash] = useState(params.get("hash") ?? "");
  const [tx, setTx] = useState<TxJSON | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [searched, setSearched] = useState(false);

  async function run(h: string) {
    setErr(null);
    setTx(null);
    setSearched(true);
    try {
      setTx(await getTxByHash(h.trim()));
    } catch (e) {
      setErr(String(e));
    }
  }

  useEffect(() => {
    const q = params.get("hash");
    if (q) run(q);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <main>
      <h1>Transaction lookup</h1>
      <form
        className="panel"
        onSubmit={(e) => {
          e.preventDefault();
          run(hash);
        }}
      >
        <div className="field">
          <label htmlFor="txhash">Tx hash (32-byte hex)</label>
          <input
            id="txhash"
            data-testid="tx-input"
            value={hash}
            onChange={(e) => setHash(e.target.value)}
          />
        </div>
        <button type="submit" data-testid="tx-submit">
          Look up
        </button>
      </form>

      {err && <p className="err" data-testid="error">{err}</p>}
      {searched && !tx && !err && (
        <p className="muted" data-testid="not-found">No transaction with that hash.</p>
      )}
      {tx && (
        <div className="panel">
          <div className="kv" data-testid="tx-detail">
            <div>hash</div>
            <div className="mono">{tx.hash}</div>
            <div>from</div>
            <div className="mono">{tx.from}</div>
            <div>to</div>
            <div className="mono">{tx.to}</div>
            <div>value</div>
            <div>{tx.value}</div>
            <div>nonce</div>
            <div>{tx.nonce}</div>
            <div>gasLimit</div>
            <div>{tx.gasLimit}</div>
            <div>gasFeeCap</div>
            <div>{tx.gasFeeCap}</div>
            <div>gasTipCap</div>
            <div>{tx.gasTipCap}</div>
            <div>data</div>
            <div className="mono">{tx.data || "(empty)"}</div>
            <div>signature</div>
            <div className="mono">{tx.signature}</div>
          </div>
        </div>
      )}
    </main>
  );
}

export default function TxPage() {
  return (
    <Suspense fallback={<main><p className="muted">Loading…</p></main>}>
      <TxLookup />
    </Suspense>
  );
}
