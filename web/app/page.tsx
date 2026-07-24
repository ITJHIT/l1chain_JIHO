"use client";

import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import { getBlockByHeight, getChainHead } from "@/lib/rpc";
import type { BlockJSON, ChainHead } from "@/lib/types";

const LATEST_N = 10;
const POLL_MS = 2000;

function short(hex: string, n = 12): string {
  return hex.length > n ? `${hex.slice(0, n)}…` : hex;
}

export default function Home() {
  const [head, setHead] = useState<ChainHead | null>(null);
  const [blocks, setBlocks] = useState<BlockJSON[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const busy = useRef(false);

  const refresh = useCallback(async () => {
    if (busy.current) return;
    busy.current = true;
    try {
      const h = await getChainHead();
      setHead(h);
      const heights: number[] = [];
      for (let i = 0; i < LATEST_N && h.height - i >= 0; i++) heights.push(h.height - i);
      const fetched = await Promise.all(heights.map((ht) => getBlockByHeight(ht)));
      setBlocks(fetched.filter((b): b is BlockJSON => b !== null));
      setErr(null);
    } catch (e) {
      setErr(String(e));
    } finally {
      busy.current = false;
    }
  }, []);

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, POLL_MS);
    return () => clearInterval(t);
  }, [refresh]);

  return (
    <main>
      <h1>Chain Head</h1>
      {err && <p className="err" data-testid="error">{err}</p>}
      <div className="panel">
        {head ? (
          <>
            <div className="muted">Current height</div>
            <div className="big" data-testid="head-height">
              {head.height}
            </div>
            <div className="muted" style={{ marginTop: 8 }}>
              Head hash
            </div>
            <div className="mono" data-testid="head-hash">
              {head.hash}
            </div>
          </>
        ) : (
          <div className="muted">Loading…</div>
        )}
      </div>

      <h2>Latest blocks</h2>
      <div className="panel">
        <table data-testid="blocks-table">
          <thead>
            <tr>
              <th>Height</th>
              <th>Hash</th>
              <th>Txs</th>
              <th>Coinbase</th>
            </tr>
          </thead>
          <tbody>
            {blocks.map((b) => (
              <tr key={b.hash} data-testid="block-row">
                <td>
                  <Link href={`/block/${b.header.height}`}>{b.header.height}</Link>
                </td>
                <td className="mono">
                  <Link href={`/block/${b.header.height}`}>{short(b.hash, 18)}</Link>
                </td>
                <td>{b.txs.length}</td>
                <td className="mono">{short(b.header.coinbase, 16)}</td>
              </tr>
            ))}
            {blocks.length === 0 && (
              <tr>
                <td colSpan={4} className="muted">
                  No blocks yet…
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </main>
  );
}
