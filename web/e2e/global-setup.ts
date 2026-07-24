import { spawn, spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import {
  DIFFICULTY,
  FUNDED_ADDR,
  FUNDED_ALLOC,
  GO_BIN,
  GO_ENV,
  MINE_INTERVAL,
  MINER_PRIV,
  NODE_BIN,
  PID_FILE,
  REPO_ROOT,
  RPC_ADDR,
  RPC_URL,
} from "./constants";

async function rpcHead(): Promise<{ height: number; hash: string } | null> {
  try {
    const res = await fetch(RPC_URL, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "getChainHead", params: [] }),
    });
    // height is a decimal string on the wire (Go `,string` tag); parse to number.
    const data = (await res.json()) as { result?: { height: string; hash: string } };
    if (!data.result) return null;
    return { height: Number(data.result.height), hash: data.result.hash };
  } catch {
    return null;
  }
}

async function waitFor<T>(fn: () => Promise<T | null>, ms: number, label: string): Promise<T> {
  const deadline = Date.now() + ms;
  let last: T | null = null;
  while (Date.now() < deadline) {
    last = await fn();
    if (last) return last;
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error(`timed out waiting for ${label}`);
}

export default async function globalSetup() {
  const goEnv = { ...process.env, ...GO_ENV };

  fs.mkdirSync(path.dirname(NODE_BIN), { recursive: true });

  // 1. Build a standalone node binary so we can start/stop a single process
  //    cleanly (`go run` spawns an uncontrolled child on Windows).
  console.log("[e2e] building l1 node binary...");
  const build = spawnSync(GO_BIN, ["build", "-o", NODE_BIN, "./cmd/l1"], {
    cwd: REPO_ROOT,
    env: goEnv,
    encoding: "utf8",
  });
  if (build.status !== 0) {
    throw new Error(`go build failed:\n${build.stdout}\n${build.stderr}`);
  }

  // 2. Start the node with a funded genesis alloc and fast mining.
  console.log(`[e2e] starting node on ${RPC_ADDR} (funded ${FUNDED_ADDR})...`);
  const args = [
    "node",
    "--rpc-addr", RPC_ADDR,
    "--miner-key", MINER_PRIV,
    "--difficulty", String(DIFFICULTY),
    "--alloc", `${FUNDED_ADDR}:${FUNDED_ALLOC}`,
    "--mine-interval", MINE_INTERVAL,
  ];
  const logPath = path.join(path.dirname(PID_FILE), "l1node.log");
  fs.mkdirSync(path.dirname(PID_FILE), { recursive: true });
  const out = fs.openSync(logPath, "w");
  const child = spawn(NODE_BIN, args, {
    cwd: REPO_ROOT,
    env: goEnv,
    stdio: ["ignore", out, out],
    windowsHide: true,
  });
  child.unref();
  if (!child.pid) throw new Error("failed to spawn node process");
  fs.writeFileSync(PID_FILE, String(child.pid), "utf8");

  // 3. Wait until the RPC responds and blocks are being produced.
  const head0 = await waitFor(rpcHead, 30_000, "node RPC");
  console.log(`[e2e] node up, head height ${head0.height}`);
  await waitFor(
    async () => {
      const h = await rpcHead();
      return h && h.height > head0.height ? h : null;
    },
    30_000,
    "first mined block",
  );
  console.log("[e2e] node is mining. setup complete.");
}
