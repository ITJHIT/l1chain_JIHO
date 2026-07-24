import { expect, test, type APIRequestContext } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import { FUNDED_ADDR, FUNDED_PRIV } from "./constants";
import type { TxJSON } from "../lib/types";

const ARTIFACT_DIR = path.join(__dirname, "..", "e2e-artifacts");

// A fresh recipient, deterministic but distinct from the funded + miner
// genesis identities. Starts with a zero balance on every fresh node.
const RECIPIENT_ADDR = "00112233445566778899aabbccddeeff00112233";
const SEND_VALUE = 1234;

const RPC_PROXY = "/api/rpc";

test.beforeAll(() => {
  fs.mkdirSync(ARTIFACT_DIR, { recursive: true });
});

// Minimal same-origin JSON-RPC helper going through the Next /api/rpc proxy
// (exactly the path the browser uses), driven from the test's request context.
let rpcId = 1;
async function rpc<T>(
  request: APIRequestContext,
  method: string,
  params: unknown[],
): Promise<T | null> {
  const res = await request.post(RPC_PROXY, {
    data: { jsonrpc: "2.0", id: rpcId++, method, params },
    headers: { "Content-Type": "application/json" },
  });
  const body = (await res.json()) as {
    result?: T;
    error?: { code: number; message: string };
  };
  if (body.error) {
    throw new Error(`RPC ${method}: [${body.error.code}] ${body.error.message}`);
  }
  return body.result === undefined ? null : body.result;
}

async function getBalance(request: APIRequestContext, addr: string): Promise<number> {
  const r = await rpc<{ balance: string }>(request, "getBalance", [addr]);
  if (!r) throw new Error(`getBalance(${addr}) returned null`);
  return Number(r.balance);
}

test("send: browser-signed tx is accepted by the Go node, mined, and credits the recipient", async ({
  page,
  request,
}) => {
  // --- Baseline balances (before the send) -----------------------------------
  const recipientBefore = await getBalance(request, RECIPIENT_ADDR);
  const funderBefore = await getBalance(request, FUNDED_ADDR);
  expect(funderBefore).toBeGreaterThanOrEqual(SEND_VALUE);

  // --- Fill + submit the in-browser signing form -----------------------------
  await page.goto("/send");
  await page.getByTestId("send-key").fill(FUNDED_PRIV);

  // The page derives the sender address locally from the private key; it must
  // match the funded genesis address or the signature preimage is wrong.
  await expect(page.getByTestId("send-from")).toHaveText(FUNDED_ADDR);

  await page.getByTestId("send-to").fill(RECIPIENT_ADDR);
  await page.getByTestId("send-value").fill(String(SEND_VALUE));
  await page.getByTestId("send-nonce").fill("0"); // fresh account => nonce 0
  await page.getByTestId("send-submit").click();

  // If the Go verifier rejects the browser signature, the UI shows an error.
  // Surface that explicitly so cross-language signing drift is caught (no fake
  // pass): fail with the exact node error message.
  const errorEl = page.getByTestId("error");
  const resultEl = page.getByTestId("send-result");
  await expect(async () => {
    if (await errorEl.isVisible()) {
      throw new Error(
        `send rejected by node (signing drift?): ${await errorEl.textContent()}`,
      );
    }
    await expect(resultEl).toBeVisible();
  }).toPass({ timeout: 30_000 });

  const txHash = (await resultEl.textContent())?.trim() ?? "";
  expect(txHash).toMatch(/^[0-9a-f]{64}$/);

  // --- Poll getTxByHash until the tx is mined (poll, not sleep) --------------
  const minedTx = await test.step("poll getTxByHash until mined", async () => {
    let found: TxJSON | null = null;
    await expect
      .poll(async () => {
        found = await rpc<TxJSON>(request, "getTxByHash", [txHash]);
        return found !== null;
      }, { timeout: 60_000, intervals: [500, 1000] })
      .toBeTruthy();
    return found as unknown as TxJSON;
  });

  // The mined tx must originate from the funded address (from == funded).
  expect(minedTx.from).toBe(FUNDED_ADDR);
  expect(minedTx.to).toBe(RECIPIENT_ADDR);
  // value is a decimal string on the wire (Go `,string` tag): parse before compare.
  expect(Number(minedTx.value)).toBe(SEND_VALUE);

  // --- Recipient balance increased by exactly the sent value -----------------
  await expect
    .poll(async () => getBalance(request, RECIPIENT_ADDR), {
      timeout: 60_000,
      intervals: [500, 1000],
    })
    .toBe(recipientBefore + SEND_VALUE);

  const recipientAfter = await getBalance(request, RECIPIENT_ADDR);
  expect(recipientAfter - recipientBefore).toBe(SEND_VALUE);

  // --- Receipt-style screenshot ----------------------------------------------
  // Render an explorer address view for the recipient so the screenshot shows
  // the credited (non-uniform) balance, then also capture the send receipt.
  await page.goto("/address");
  await page.getByTestId("addr-input").fill(RECIPIENT_ADDR);
  await page.getByTestId("addr-submit").click();
  const balanceEl = page.getByTestId("balance");
  await expect(balanceEl).toBeVisible({ timeout: 15_000 });
  await expect
    .poll(async () => Number(await balanceEl.textContent()), { timeout: 15_000 })
    .toBe(recipientBefore + SEND_VALUE);

  const shot = path.join(ARTIFACT_DIR, "send-recipient-balance.png");
  await page.screenshot({ path: shot, fullPage: true });
  expect(fs.existsSync(shot)).toBeTruthy();

  console.log(
    `[send.spec] txHash=${txHash} recipientBefore=${recipientBefore} ` +
      `recipientAfter=${recipientAfter} delta=${recipientAfter - recipientBefore} ` +
      `from=${minedTx.from}`,
  );
});
