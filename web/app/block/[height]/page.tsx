"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { getBlockByHeight } from "@/lib/rpc";
import type { BlockJSON } from "@/lib/types";

export default function BlockView({ params }: { params: { height: string } }) {
  const height = Number(params.height);
  const [block, setBlock] = useState<BlockJSON | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const b = await getBlockByHeight(height);
        if (!alive) return;
        setBlock(b);
        setErr(null);
      } catch (e) {
        if (alive) setErr(String(e));
      } finally {
        if (alive) setLoaded(true);
      }
    })();
    return () => {
      alive = false;
    };
  }, [height]);

  return (
    <main>
      <h1>Block #{height}</h1>
      <p className="row">
        <Link href={`/block/${Math.max(0, height - 1)}`}>← prev</Link>
        <Link href={`/block/${height + 1}`}>next →</Link>
      </p>
      {err && <p className="err" data-testid="error">{err}</p>}
      {loaded && !block && !err && (
        <p className="muted" data-testid="not-found">No block at height {height}.</p>
      )}
      {block && (
        <>
          <div className="panel">
            <h2>Header</h2>
            <div className="kv" data-testid="block-header">
              <div>height</div>
              <div data-testid="hdr-height">{block.header.height}</div>
              <div>hash</div>
              <div className="mono" data-testid="hdr-hash">{block.hash}</div>
              <div>prevHash</div>
              <div className="mono">{block.header.prevHash}</div>
              <div>merkleRoot</div>
              <div className="mono">{block.header.merkleRoot}</div>
              <div>stateRoot</div>
              <div className="mono">{block.header.stateRoot}</div>
              <div>coinbase</div>
              <div className="mono" data-testid="hdr-coinbase">{block.header.coinbase}</div>
              <div>difficulty</div>
              <div>{block.header.difficulty}</div>
              <div>nonce</div>
              <div>{block.header.nonce}</div>
              <div>timestamp</div>
              <div>
                {block.header.timestamp}{" "}
                <span className="muted">
                  ({new Date(block.header.timestamp * 1000).toISOString()})
                </span>
              </div>
            </div>
          </div>

          <div className="panel">
            <h2>Transactions ({block.txs.length})</h2>
            {block.txs.length === 0 ? (
              <p className="muted">No transactions in this block.</p>
            ) : (
              <table data-testid="tx-table">
                <thead>
                  <tr>
                    <th>Hash</th>
                    <th>From</th>
                    <th>To</th>
                    <th>Value</th>
                    <th>Nonce</th>
                  </tr>
                </thead>
                <tbody>
                  {block.txs.map((t) => (
                    <tr key={t.hash}>
                      <td className="mono">
                        <Link href={`/tx?hash=${t.hash}`}>{t.hash.slice(0, 16)}…</Link>
                      </td>
                      <td className="mono">{t.from.slice(0, 12)}…</td>
                      <td className="mono">{t.to.slice(0, 12)}…</td>
                      <td>{t.value}</td>
                      <td>{t.nonce}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </>
      )}
    </main>
  );
}
