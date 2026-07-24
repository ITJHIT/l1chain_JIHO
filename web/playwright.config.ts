import { defineConfig, devices } from "@playwright/test";

// Ports used by the E2E harness.
export const NEXT_PORT = 3100;
export const RPC_PORT = 8546;
export const BASE_URL = `http://127.0.0.1:${NEXT_PORT}`;
export const RPC_URL = `http://127.0.0.1:${RPC_PORT}`;

export default defineConfig({
  testDir: "./e2e",
  timeout: 120_000,
  expect: { timeout: 30_000 },
  fullyParallel: false,
  workers: 1,
  reporter: [["list"], ["html", { open: "never", outputFolder: "playwright-report" }]],
  outputDir: "test-results",

  // Starts the Go node (via a prebuilt binary) before the browser runs.
  globalSetup: "./e2e/global-setup.ts",
  globalTeardown: "./e2e/global-teardown.ts",

  use: {
    baseURL: BASE_URL,
    trace: "on-first-retry",
    screenshot: "only-on-failure",
  },

  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],

  // Build + start the Next app; point its RPC proxy at the test Go node.
  webServer: {
    command: `npm run build && npm run start -- -p ${NEXT_PORT}`,
    url: BASE_URL,
    reuseExistingServer: !process.env.CI,
    timeout: 180_000,
    env: {
      RPC_URL,
      NEXT_PUBLIC_RPC_URL: RPC_URL,
    },
  },
});
