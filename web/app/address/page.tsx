"use client";

import { useState } from "react";
import { getBalance } from "@/lib/rpc";

export default function AddressView() {
  const [addr, setAddr] = useState("");
  const [balance, setBalance] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  async function lookup(e: React.FormEvent) {
    e.preventDefault();
    setErr(null);
    setBalance(null);
    setLoading(true);
    try {
      const b = await getBalance(addr.trim());
      setBalance(b);
    } catch (e2) {
      setErr(String(e2));
    } finally {
      setLoading(false);
    }
  }

  return (
    <main>
      <h1>Address</h1>
      <form className="panel" onSubmit={lookup}>
        <div className="field">
          <label htmlFor="addr">Address (20-byte hex)</label>
          <input
            id="addr"
            data-testid="addr-input"
            placeholder="2b748c78bab45534805eb4fcc1276bdf45218149"
            value={addr}
            onChange={(e) => setAddr(e.target.value)}
          />
        </div>
        <button type="submit" data-testid="addr-submit" disabled={loading}>
          {loading ? "Looking up…" : "Get balance"}
        </button>
      </form>

      {err && <p className="err" data-testid="error">{err}</p>}
      {balance !== null && (
        <div className="panel">
          <div className="muted">Balance</div>
          <div className="big" data-testid="balance">{balance}</div>
          <div className="mono muted">{addr.trim()}</div>
        </div>
      )}
    </main>
  );
}
