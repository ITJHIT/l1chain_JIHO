"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  getExchangeBalance,
  getLastAuction,
  getOrderBook,
  getOrderBookDepth,
} from "@/lib/rpc";
import type {
  ExchangeBalanceJSON,
  LastAuctionJSON,
  LevelJSON,
  OrderBookDepthJSON,
  OrderJSON,
} from "@/lib/types";

const POLL_MS = 2000;

function short(hex: string, n = 12): string {
  return hex.length > n ? `${hex.slice(0, n)}…` : hex;
}

function LevelRows({ levels, side }: { levels: LevelJSON[]; side: "bid" | "ask" }) {
  if (levels.length === 0) {
    return (
      <tr>
        <td colSpan={2} className="muted">
          No {side}s
        </td>
      </tr>
    );
  }
  return (
    <>
      {levels.map((lv, i) => (
        <tr key={`${side}-${lv.price}-${i}`} data-testid={`${side}-row`}>
          <td className={side === "bid" ? "ok" : "err"}>{lv.price}</td>
          <td>{lv.qty}</td>
        </tr>
      ))}
    </>
  );
}

export default function ExchangePage() {
  const [depth, setDepth] = useState<OrderBookDepthJSON>({ bids: [], asks: [] });
  const [orders, setOrders] = useState<OrderJSON[]>([]);
  const [lastAuction, setLastAuction] = useState<LastAuctionJSON | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const busy = useRef(false);

  const [addr, setAddr] = useState("");
  const [bal, setBal] = useState<ExchangeBalanceJSON | null>(null);
  const [balErr, setBalErr] = useState<string | null>(null);
  const [balLoading, setBalLoading] = useState(false);

  const refresh = useCallback(async () => {
    if (busy.current) return;
    busy.current = true;
    try {
      const [d, o, a] = await Promise.all([
        getOrderBookDepth(),
        getOrderBook(),
        getLastAuction(),
      ]);
      setDepth(d);
      setOrders(o);
      setLastAuction(a);
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

  async function lookupBalance(e: React.FormEvent) {
    e.preventDefault();
    setBalErr(null);
    setBal(null);
    setBalLoading(true);
    try {
      setBal(await getExchangeBalance(addr.trim()));
    } catch (e2) {
      setBalErr(String(e2));
    } finally {
      setBalLoading(false);
    }
  }

  return (
    <main>
      <h1>On-Chain Exchange</h1>
      <p className="muted">
        A price-time-priority limit order book, matched inside the state
        transition itself: placing or cancelling an order is an ordinary
        signed transaction, and the book&apos;s contents are folded into the
        same state root every other balance is. This view reads it straight
        off chain state via getOrderBookDepth / getOrderBook / getLastAuction
        -- there is no separate off-chain indexer standing behind it.
      </p>
      {err && (
        <p className="err" data-testid="error">
          {err}
        </p>
      )}

      <div className="panel">
        <div className="muted">Last batch-auction clear</div>
        {lastAuction ? (
          <div className="kv" data-testid="last-auction">
            <div>Price</div>
            <div className="mono ok">{lastAuction.price}</div>
            <div>Volume</div>
            <div className="mono">{lastAuction.volume}</div>
            <div>At height</div>
            <div className="mono">{lastAuction.height}</div>
          </div>
        ) : (
          <div className="muted" data-testid="last-auction-none">
            None yet -- this chain has never cleared a batch (fresh, or
            running Continuous mode).
          </div>
        )}
      </div>

      <h2>Depth</h2>
      <div className="row" style={{ alignItems: "flex-start", gap: 24 }}>
        <div className="panel" style={{ flex: 1 }}>
          <div className="muted" style={{ marginBottom: 8 }}>
            Bids
          </div>
          <table data-testid="bids-table">
            <thead>
              <tr>
                <th>Price</th>
                <th>Qty</th>
              </tr>
            </thead>
            <tbody>
              <LevelRows levels={depth.bids} side="bid" />
            </tbody>
          </table>
        </div>
        <div className="panel" style={{ flex: 1 }}>
          <div className="muted" style={{ marginBottom: 8 }}>
            Asks
          </div>
          <table data-testid="asks-table">
            <thead>
              <tr>
                <th>Price</th>
                <th>Qty</th>
              </tr>
            </thead>
            <tbody>
              <LevelRows levels={depth.asks} side="ask" />
            </tbody>
          </table>
        </div>
      </div>

      <h2>Resting orders</h2>
      <div className="panel">
        <table data-testid="orders-table">
          <thead>
            <tr>
              <th>Height</th>
              <th>Index</th>
              <th>Account</th>
              <th>Side</th>
              <th>Price</th>
              <th>Qty</th>
            </tr>
          </thead>
          <tbody>
            {orders.map((o) => (
              <tr key={`${o.height}-${o.index}`} data-testid="order-row">
                <td>{o.height}</td>
                <td>{o.index}</td>
                <td className="mono">{short(o.account, 16)}</td>
                <td className={o.side === "buy" ? "ok" : "err"}>{o.side}</td>
                <td>{o.price}</td>
                <td>{o.qty}</td>
              </tr>
            ))}
            {orders.length === 0 && (
              <tr>
                <td colSpan={6} className="muted">
                  No resting orders
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <h2>Exchange balance</h2>
      <form className="panel" onSubmit={lookupBalance}>
        <div className="field">
          <label htmlFor="ex-addr">Address (20-byte hex)</label>
          <input
            id="ex-addr"
            data-testid="ex-addr-input"
            placeholder="2b748c78bab45534805eb4fcc1276bdf45218149"
            value={addr}
            onChange={(e) => setAddr(e.target.value)}
          />
        </div>
        <button type="submit" data-testid="ex-addr-submit" disabled={balLoading}>
          {balLoading ? "Looking up…" : "Get exchange balance"}
        </button>
      </form>
      {balErr && (
        <p className="err" data-testid="ex-error">
          {balErr}
        </p>
      )}
      {bal && (
        <div className="panel kv" data-testid="ex-balance">
          <div>Base held</div>
          <div className="mono">{bal.base}</div>
          <div>Base locked</div>
          <div className="mono">{bal.lockedBase}</div>
          <div>Quote locked</div>
          <div className="mono">{bal.lockedQuote}</div>
        </div>
      )}
    </main>
  );
}
