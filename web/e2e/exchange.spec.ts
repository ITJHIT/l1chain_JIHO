import { expect, test } from "@playwright/test";
import { addressFromPrivHex, signTx } from "../lib/sign";
import { encodePlace, EXCHANGE_ADDRESS } from "../lib/exchange";
import { EXCHANGE_TRADER_ADDR, EXCHANGE_TRADER_PRIV, RPC_URL } from "./constants";

// Places a real signed order the same way any RPC client would (build calldata
// with encodePlace, sign with signTx, submit as an ordinary transaction), then
// confirms the /exchange page renders it -- both the aggregated depth level
// and the individual resting order -- once the node mines it. No UI form
// drives the placement: this exercises the read/render path against a real
// chain, the same way explorer.spec.ts exercises the address page against a
// real funded balance rather than a mock.
//
// Uses EXCHANGE_TRADER_PRIV, a genesis-funded identity dedicated to this spec,
// rather than FUNDED_PRIV: send.spec.ts spends FUNDED_PRIV's nonce 0 and
// hardcodes that assumption in its UI form ("fresh account => nonce 0"), and
// Playwright's file execution order across specs is not this file's to
// assume. A dedicated key's first transaction is unambiguously nonce 0
// regardless of what any other spec does.

async function rpcCall<T>(method: string, params: unknown[]): Promise<T> {
  const res = await fetch(RPC_URL, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: 1, method, params }),
  });
  const data = (await res.json()) as { result?: T; error?: { message: string } };
  if (data.error) throw new Error(`${method}: ${data.error.message}`);
  return data.result as T;
}

async function waitMined(txHash: string, timeoutMs = 60_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const found = await rpcCall<unknown>("getTxByHash", [txHash]).catch(() => null);
    if (found) return;
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error(`tx ${txHash} was not mined within ${timeoutMs}ms`);
}

test("exchange page: a real placed order shows up in depth and the order table", async ({
  page,
}) => {
  expect(addressFromPrivHex(EXCHANGE_TRADER_PRIV)).toBe(EXCHANGE_TRADER_ADDR);

  const price = 4242n;
  const qty = 7n;
  const orderTx = await signTx(EXCHANGE_TRADER_PRIV, {
    to: EXCHANGE_ADDRESS,
    value: 0n,
    nonce: 0n, // this key's first and only transaction in the suite
    gasLimit: 0n,
    data: Uint8Array.from(Buffer.from(encodePlace("buy", price, qty), "hex")),
  });
  const orderTxHash = await rpcCall<{ txHash: string }>("sendRawTx", [orderTx]).then(
    (r) => r.txHash,
  );
  expect(orderTxHash).toMatch(/^[0-9a-f]{64}$/);
  await waitMined(orderTxHash);

  await page.goto("/exchange");

  // The specific level this order created is present, not just SOME bid row --
  // the e2e node mines continuously, so other tests' activity may add rows too.
  await expect
    .poll(
      async () => {
        const rows = page.getByTestId("bid-row");
        const n = await rows.count();
        for (let i = 0; i < n; i++) {
          const cells = rows.nth(i).locator("td");
          const p = (await cells.nth(0).textContent())?.trim();
          const q = (await cells.nth(1).textContent())?.trim();
          if (p === price.toString() && q === qty.toString()) return true;
        }
        return false;
      },
      { timeout: 20_000, intervals: [1000] },
    )
    .toBeTruthy();

  // The individual resting order is listed too, owned by the trader address.
  const orderRow = page.getByTestId("order-row").filter({ hasText: price.toString() });
  await expect(orderRow.first()).toBeVisible({ timeout: 20_000 });
  await expect(orderRow.first()).toContainText(EXCHANGE_TRADER_ADDR.slice(0, 16));

  // No batch has cleared on this Continuous-mode e2e chain.
  await expect(page.getByTestId("last-auction-none")).toBeVisible();

  // Exchange-balance lookup for the trader reflects the lock (price * qty).
  await page.getByTestId("ex-addr-input").fill(EXCHANGE_TRADER_ADDR);
  await page.getByTestId("ex-addr-submit").click();
  const balPanel = page.getByTestId("ex-balance");
  await expect(balPanel).toBeVisible({ timeout: 15_000 });
  await expect(balPanel).toContainText((price * qty).toString());
});
