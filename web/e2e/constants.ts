import path from "node:path";

// Repo root = .../go-l1-chain (web/e2e -> web -> repo root)
export const REPO_ROOT = path.resolve(__dirname, "..", "..");

// Portable Go toolchain (see task Environment section).
export const GO_ENV = {
  GOROOT: "C:\\Users\\82104\\.go-sdk",
  GOCACHE: "C:\\Users\\82104\\AppData\\Local\\Temp\\gocache",
  GOPATH: "C:\\Users\\82104\\go",
  GOMODCACHE: "C:\\Users\\82104\\go\\pkg\\mod",
};
export const GO_BIN = path.join(GO_ENV.GOROOT, "bin", "go.exe");

// Deterministic E2E identities (generated via `l1 wallet new`).
export const FUNDED_PRIV =
  "e786202f9a74f92d44b5ec8b92ec13a8a88221f8fd25cb95b2b457a87fc06d53";
export const FUNDED_ADDR = "2b748c78bab45534805eb4fcc1276bdf45218149";
export const FUNDED_ALLOC = 1_000_000;

export const MINER_PRIV =
  "7ad3ea99db00e5ef4bd93e035865432fe07b5ecfad1ff7b403e89da1d0eeded9";
export const MINER_ADDR = "32a0ec1f20e5b6457aa985628905baa8ce115565";

export const RPC_PORT = 8546;
export const RPC_ADDR = `127.0.0.1:${RPC_PORT}`;
export const RPC_URL = `http://${RPC_ADDR}`;
export const DIFFICULTY = 4;
export const MINE_INTERVAL = "1s";

export const NODE_DIR = path.join(__dirname, "..", ".l1node");
export const PID_FILE = path.join(NODE_DIR, "l1node-pid");
export const NODE_BIN = path.join(NODE_DIR, "l1node-e2e.exe");
