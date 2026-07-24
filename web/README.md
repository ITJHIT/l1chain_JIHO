# L1 Block Explorer (web/)

A Next.js (App Router, TypeScript) single-page block explorer for the
`go-l1-chain` node. It consumes the node's JSON-RPC 2.0 API and never touches the
Go code.

## What it does

- **Home (`/`)** — current chain head (height + hash), auto-refreshing every 2s,
  plus the latest 10 blocks (walked from head down via `getBlockByHeight`).
- **Block (`/block/<height>`)** — full header (height, prevHash, merkleRoot,
  stateRoot, coinbase, difficulty, nonce, timestamp, hash) and the block's txs.
- **Address (`/address`)** — enter a 20-byte hex address → `getBalance`.
- **Tx (`/tx?hash=<hex>`)** — enter a 32-byte tx hash → `getTxByHash`.
- **Send (`/send`)** — build, sign (in-browser), and submit a value transfer.

## RPC client, proxy, and CORS

- Typed client: `lib/rpc.ts` (`getChainHead`, `getBlockByHeight`, `getBalance`,
  `getTxByHash`, `sendRawTx`). Wire shapes mirror `rpc/server.go` exactly
  (`lib/types.ts`): all hashes/addresses are lowercase hex **without** a `0x`
  prefix; positional params; the `{jsonrpc,id,result|error}` envelope.
- **CORS handling:** `rpc/server.go` sends only a `Content-Type` header (no
  `Access-Control-Allow-Origin`) and rejects non-`POST` methods (no OPTIONS
  pre-flight). A cross-origin browser fetch would be blocked. So the browser
  always calls the **same-origin** proxy `app/api/rpc/route.ts`, which forwards
  the JSON-RPC envelope verbatim to the Go node and returns its response
  verbatim. No Go changes needed.
- **RPC target env var:** the proxy forwards to `RPC_URL` (or
  `NEXT_PUBLIC_RPC_URL`), default `http://127.0.0.1:8545`.

## Send-form signing decision

Signing happens **in the browser** with `@noble/secp256k1` + `@noble/hashes`
(`lib/sign.ts`), reproducing the Go node conventions exactly:

- **Address:** last 20 bytes of `SHA-256(uncompressed pubkey X||Y)`
  (`wallet/wallet.go` `pubKeyAddress`).
- **SigningHash:** `SHA-256(From || To || Value(u64 BE) || Nonce(u64 BE) ||
  GasLimit(u64 BE) || Data)` (`core/transaction.go` `preimage`).
- **Signature:** 65-byte dcrd-compatible recoverable compact signature
  `[27 + recid + 4][R(32)][S(32)]` — the `+4` is dcrd's "compressed key" flag,
  matching `wallet.go` `Key.Sign` (`ecdsa.SignCompact(priv, hash, true)`) and
  `wallet.Verify` (`ecdsa.RecoverCompact`). noble emits canonical low-S sigs.

The private key is only used locally to sign; it is not sent to the server.

## Run it

### 1. Start the Go node (from the repo root, with the portable Go toolchain)

```sh
export GOROOT="C:\\Users\\82104\\.go-sdk"
export GOCACHE="C:\\Users\\82104\\AppData\\Local\\Temp\\gocache"
export GOPATH="C:\\Users\\82104\\go"
export GOMODCACHE="C:\\Users\\82104\\go\\pkg\\mod"

"$GOROOT\\bin\\go.exe" run ./cmd/l1 node \
  --rpc-addr 127.0.0.1:8545 \
  --miner-key 7ad3ea99db00e5ef4bd93e035865432fe07b5ecfad1ff7b403e89da1d0eeded9 \
  --difficulty 4 \
  --alloc 2b748c78bab45534805eb4fcc1276bdf45218149:1000000 \
  --mine-interval 2s
```

### 2. Start the app (from `web/`)

```sh
npm install
npm run build
npm run start          # http://localhost:3000  (proxy -> 127.0.0.1:8545)
# or: npm run dev
```

Override the RPC target: `RPC_URL=http://127.0.0.1:8546 npm run start`.

## End-to-end test (Playwright)

The E2E harness (`e2e/`) builds a standalone node binary, starts it on a test
port (`8546`) with a funded genesis alloc + 1s mining, builds and starts the Next
app on port `3100`, then drives Chromium.

```sh
npm install
npm run e2e:install          # playwright install --with-deps chromium
npm run e2e                  # playwright test
```

The test `explorer: head increases, block header shows coinbase, funded balance
is non-zero` asserts: the home head height grows over time (poll-based), a block
view renders a 40-hex coinbase, and the funded address balance is non-zero.
Screenshots are written to `web/e2e-artifacts/` (`block-view.png`,
`address-balance.png`).
