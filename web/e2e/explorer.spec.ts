import { expect, test } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import { FUNDED_ADDR } from "./constants";

const ARTIFACT_DIR = path.join(__dirname, "..", "e2e-artifacts");

test.beforeAll(() => {
  fs.mkdirSync(ARTIFACT_DIR, { recursive: true });
});

test("explorer: head increases, block header shows coinbase, funded balance is non-zero", async ({
  page,
}) => {
  // --- Home: chain head is shown and increases over time ---------------------
  await page.goto("/");
  const headHeight = page.getByTestId("head-height");
  await expect(headHeight).toBeVisible();

  const first = Number(await headHeight.textContent());
  expect(Number.isFinite(first)).toBeTruthy();

  // Poll (not sleep) until the auto-refreshing head advances.
  await expect
    .poll(async () => Number(await headHeight.textContent()), {
      timeout: 30_000,
      intervals: [1000],
    })
    .toBeGreaterThan(first);

  const grown = Number(await headHeight.textContent());
  expect(grown).toBeGreaterThan(first);

  // At least one block row is rendered.
  await expect(page.getByTestId("block-row").first()).toBeVisible();

  // --- Block view: header incl. coinbase -------------------------------------
  // Use an early, definitely-mined height for stability.
  await page.goto(`/block/1`);
  await expect(page.getByTestId("block-header")).toBeVisible();
  await expect(page.getByTestId("hdr-height")).toHaveText("1");

  const coinbase = page.getByTestId("hdr-coinbase");
  await expect(coinbase).toBeVisible();
  const coinbaseText = (await coinbase.textContent())?.trim() ?? "";
  // 20-byte address => 40 lowercase hex chars.
  expect(coinbaseText).toMatch(/^[0-9a-f]{40}$/);

  const blockShot = path.join(ARTIFACT_DIR, "block-view.png");
  await page.screenshot({ path: blockShot, fullPage: true });
  expect(fs.existsSync(blockShot)).toBeTruthy();

  // --- Address view: funded genesis address has a non-zero balance -----------
  await page.goto("/address");
  await page.getByTestId("addr-input").fill(FUNDED_ADDR);
  await page.getByTestId("addr-submit").click();

  const balance = page.getByTestId("balance");
  await expect(balance).toBeVisible({ timeout: 15_000 });
  const bal = Number(await balance.textContent());
  expect(bal).toBeGreaterThan(0);

  const balanceShot = path.join(ARTIFACT_DIR, "address-balance.png");
  await page.screenshot({ path: balanceShot, fullPage: true });
  expect(fs.existsSync(balanceShot)).toBeTruthy();
});
