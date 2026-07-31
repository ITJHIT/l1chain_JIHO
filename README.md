# l1chain — a from-scratch Layer-1 blockchain in Go

[![CI](https://github.com/ITJHIT/l1chain_JIHO/actions/workflows/ci.yml/badge.svg)](https://github.com/ITJHIT/l1chain_JIHO/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](#license)

A learning/portfolio Layer-1 blockchain built from first principles in Go: Proof-of-Work consensus, an account-based native coin, real libp2p peer-to-peer networking, a custom stack VM, an embedded EVM that runs standard ERC-20 contracts, a JSON-RPC node, a CLI wallet, and a React/Next block explorer.

Every component is implemented from scratch (not a fork), test-covered, and adversarially red-teamed.

Two companion repos round out the portfolio this belongs to: [`lowlat-oms-core`](https://github.com/ITJHIT/lowlat-oms-core) (a low-latency C++ order management system — the off-chain counterpart to this chain's consensus-facing constraints) and [`onchain-orderbook`](https://github.com/ITJHIT/onchain-orderbook) (the deterministic matching engine wired into this chain's [On-chain exchange](#on-chain-exchange) below).

```
┌────────────────────────────────────────────────────────────────────┐
│  cmd/l1 (CLI + node daemon)      web/ (Next.js block explorer)      │
│        │  JSON-RPC (rpc/)              │  same-origin /api/rpc proxy │
│        ▼                               ▼                             │
│  node/  ── mempool · miner · RWMutex-guarded state access           │
│    │                                                                │
│    ├── chain/     block linkage · longest-chain reorg · state trans.│
│    ├── consensus/ PoW · difficulty retarget (~10s)                  │
│    ├── p2p/       libp2p host · gossipsub · /l1/sync stream          │
│    ├── vm/        custom stack VM (gas, storage, CALL)   [M3]        │
│    ├── evm/       embedded go-ethereum EVM · ERC-20      [M4]        │
│    ├── state/     StateDB, MPT-backed (secure trie + proofs)        │
│    ├── store/     BoltDB persistence                               │
│    ├── wallet/    secp256k1 keys · sign · verify                   │
│    └── core/      block · tx · merkle · hash · address             │
└────────────────────────────────────────────────────────────────────┘
```

## Screenshots

The Next.js block explorer (`web/`), driven by the JSON-RPC node:

| Block view | Address balance | Browser-signed send |
|---|---|---|
| ![block](docs/screenshots/block-view.png) | ![balance](docs/screenshots/address-balance.png) | ![send](docs/screenshots/send-recipient-balance.png) |

_The "Browser-signed send" shot shows a recipient credited 1234 by a transaction signed entirely in the browser (secp256k1 via `@noble`) and accepted verbatim by the Go node's verifier._

## Milestones

| Milestone | Scope | Status |
|-----------|-------|--------|
| **M1** | Block/tx/merkle, account coin, PoW + difficulty, longest-chain reorg, secp256k1 wallet, BoltDB persistence, JSON-RPC, CLI | ✅ done |
| **M2** | Real libp2p P2P (gossipsub + stream sync), multiprocess/LAN 3-node convergence + partition re-convergence, React/Next block explorer with in-browser signing | ✅ done |
| **M3** | Custom stack VM: gas metering, contract deploy/call, journaled storage, deterministic mining==validation | ✅ done |
| **M4** | EVM compatibility (hybrid): B2 subset-from-scratch (M3) + B1 embedded go-ethereum `core/vm` running a real ERC-20 over keccak/MPT state | ✅ done |
| **Exchange** | A deterministic limit order book inside the state transition, with a batch-auction matching mode and RPC/explorer exposure. See [On-chain exchange](#on-chain-exchange) below | ✅ done |
| **M5** | Real from-scratch SHA-256 Merkle Patricia Trie (two-level: world trie + per-account storage tries) replacing the placeholder flat-hash state root; light-client account/storage proofs over RPC + independent CLI verification; a genuine 3-container Docker Compose testnet with real libp2p peer discovery; eclipse-attack + inbound-sync-flood defenses (`ConnectionManager`, stream cap) with adversarial tests proven to fail without them | ✅ done |

## Features

- **Consensus** — Bitcoin-style Proof-of-Work with leading-zero-bit targets, difficulty retargeting toward a ~10s block interval, and longest-(heaviest-)chain reorg with full state replay.
- **Native coin** — account/balance model, ECDSA secp256k1 signatures, genesis allocation, fixed block reward (coinbase credited into canonical state), infinite supply.
- **P2P** — real `go-libp2p` hosts over TCP, GossipSub for block/tx propagation, a `/l1/sync/1.0.0` stream protocol for catch-up sync, bounded (deadlines + size caps, a connection-count watermark, and an inbound-sync-stream cap as of M5) against slow/malicious/many-sybil peers. Blocks from the network are **never trusted** — every one is re-validated through `chain.AddBlock`.
- **Smart contracts** — a from-scratch stack VM (M3) with Ethereum-numbered opcodes, gas, out-of-gas revert, and CALL depth limits; plus an embedded go-ethereum EVM (M4) that deploys and runs a standard ERC-20 with keccak-derived mapping storage.
- **Tooling** — JSON-RPC (`getChainHead`, `getBlockByHeight`, `getBalance`, `sendRawTx`, `getTxByHash`, `getOrderBookDepth`, `getOrderBook`, `getLastAuction`, `getExchangeBalance`, `getAccountProof`, `getStorageProof`), a CLI (`wallet`/`balance` [`--verify` for a real light-client check, not a trusted `getBalance`]/`send`/`node`), and a Next.js explorer with **in-browser secp256k1 signing** (a browser-signed tx is accepted verbatim by the Go verifier) plus a live `/exchange` order-book view.
- **On-chain exchange** — a deterministic limit order book reachable through ordinary signed transactions, in either continuous or batch-auction matching mode. See below.

## Quickstart

Requires Go 1.26+ and Node 18+.

```bash
# build
go build ./cmd/l1

# run a mining node with an RPC endpoint
go run ./cmd/l1 node --rpc-addr 127.0.0.1:8545 --mine-interval 2s --difficulty 8

# in another shell: wallet + queries
go run ./cmd/l1 wallet new
go run ./cmd/l1 balance --addr <hex> --rpc http://127.0.0.1:8545
go run ./cmd/l1 send --key <priv-hex> --to <addr> --value 100 --rpc http://127.0.0.1:8545
```

### Multi-node (real libp2p)

```bash
# node 1 (miner) — prints its own /p2p/<id> multiaddr on startup
go run ./cmd/l1 node --rpc-addr 127.0.0.1:8545 --listen 4001 \
  --genesis-timestamp 1750000000 --mine-interval 2s --difficulty 8

# node 2 — dials node 1, shares the same --genesis-timestamp
go run ./cmd/l1 node --rpc-addr 127.0.0.1:8546 --listen 4002 \
  --genesis-timestamp 1750000000 --peers /ip4/127.0.0.1/tcp/4001/p2p/<node1-id>
```

### Multi-node testnet (Docker Compose)

The manual 2-node walkthrough above generalized to a real 3-container mesh —
`docker-compose.yml` builds and runs node1 (seed + sole miner) plus node2/
node3 (pure validators that sync via gossip/catch-up and never mine):

```bash
docker compose up --build
# once "mined block" lines appear in node1's log, from another shell:
curl -s -XPOST http://localhost:8545 -d '{"jsonrpc":"2.0","id":1,"method":"getChainHead","params":[]}'
curl -s -XPOST http://localhost:8546 -d '{"jsonrpc":"2.0","id":1,"method":"getChainHead","params":[]}'
# node2/node3's height and hash converge on node1's within a few
# mine-interval cycles
```

Real containers, not an in-process test harness handing each node the
others' addresses directly: a libp2p peer ID is unknowable to a sibling
container's static config until the process has already started and printed
it, and `0.0.0.0` (what a container binds to be reachable from its
siblings at all) is not a dialable *destination* either. `docker/
entrypoint.sh` resolves both — node1 advertises `/dns4/node1/tcp/4001/
p2p/<id>` (Docker's own internal DNS, not a raw IP) over a shared volume
once it knows its own peer ID; node2/node3 block on that file before
starting. CI drives this exact compose file end-to-end and polls all three
nodes' RPC from outside the compose network until they agree.

Real convergence, from a green CI run (three separate containers, polled
over plain HTTP from outside the compose network entirely):

```
attempt 1: node1=0:cee5a2765c67c7197306262bfeee96b9a8583f8fee5fb36acfa7601c0b33da52 node2= node3=
attempt 2: node1=1:00272b56d87b82993f681a4bd38227b192357c632866861e77e7ec486d0bb2e8 node2=1:00272b56d87b82993f681a4bd38227b192357c632866861e77e7ec486d0bb2e8 node3=1:00272b56d87b82993f681a4bd38227b192357c632866861e77e7ec486d0bb2e8
attempt 3: node1=3:003d6b63d7b74c63f67cd3e5fd09863ee4bf64b1ba763ba34b51cf3e40558362 node2=3:003d6b63d7b74c63f67cd3e5fd09863ee4bf64b1ba763ba34b51cf3e40558362 node3=3:003d6b63d7b74c63f67cd3e5fd09863ee4bf64b1ba763ba34b51cf3e40558362
converged at 3:003d6b63d7b74c63f67cd3e5fd09863ee4bf64b1ba763ba34b51cf3e40558362
```

At attempt 1, node1 has mined alone (node2/node3 return nothing — they
haven't received node1's advertised multiaddr yet). By attempt 2 all three
already agree at height 1; by attempt 3 all three agree at height 3, hash
and all. ([full job log](https://github.com/ITJHIT/l1chain_JIHO/actions/runs/30559467454/job/90928429823))

### Block explorer

```bash
cd web
npm install
NEXT_PUBLIC_RPC_URL=http://127.0.0.1:8545 npm run dev   # http://localhost:3000
```

## Testing

```bash
go vet ./...
go test ./...          # 13 packages, unit + integration + determinism
cd web && npx playwright test   # explorer + browser-signed send + exchange order-book E2E
```

Each milestone was adversarially red-teamed; the reports live in [`artifacts/`](artifacts/):

- `m1-redteam-report.json` — double-spend, replay, forgery, invalid PoW, tampered roots, deep-reorg no-leakage, overflow, coinbase tampering
- `m2-p2p-redteam-report.json` — invalid/forged/tampered gossip, malicious sync responder, replay flood, orphan, junk payload
- `m3-vm-redteam-report.json` — infinite loop OOG, stack under/overflow, invalid jumps, storage isolation, reentrancy bounds, determinism
- `m4-evm-redteam-report.json` — OOG deploy/call, revert integrity, gas-bounded loops, reentrancy, deterministic MPT root
- `m5-p2p-hardening-redteam-report.json` — eclipse attempt (many sybil peer connections, bounded by a real libp2p connection manager), inbound sync-stream flood (bounded by a per-node concurrency cap), liveness preserved under both

## Design notes

- **Determinism is consensus-critical.** Mining and validation compute block state roots through the *identical* derivation path (replay from genesis, sorted map-free hashing, no wall-clock/randomness). Contract execution (M3) and the state root fold are deterministic so every node agrees.
- **StateDB is an interface** (`state/StateDB`), which is what let the M1 flat-hash placeholder be swapped (M5) for a real Merkle-Patricia-Trie (`state/mpt.go`, now the production default) without touching block validation, RPC, or the VM — the same seam M4's EVM state uses.
- **The EVM (M4) is an execution capability**, cleanly isolated from chain consensus (nothing imports `l1chain/evm`). Full opcode/precompile/MPT byte-for-byte chain-consensus equivalence is a documented long-term goal.

## On-chain exchange

A price-time-priority limit order book, matched *inside the state transition
itself* rather than bolted on beside it — built on
[`github.com/ITJHIT/onchain-orderbook`](https://github.com/ITJHIT/onchain-orderbook),
a separate module that is deterministic by construction (no floating point, no
map-iteration-order dependence, everything else consensus needs) and tested
there against eight independent engines, a replay from genesis, and a
demonstration of the map-iteration hazard it avoids. `exchange/` is the seam
that makes that engine reachable from an ordinary chain, without either side
having to know much about the other.

- **An order is an ordinary signed transaction.** `exchange.Address` is a
  reserved account the state transition routes to instead of the VM — the
  same shape as a precompile. Nothing about signing, nonce ordering, the
  mempool or gossip changes; the book inherits replay protection and
  exact-nonce sequencing for free.
- **Order identity is `(block height, index within block)`**, never a local
  counter — the only source of IDs two validators agree on without
  coordinating, and that a replay from genesis reproduces exactly. Getting
  this right took a real bug fix: the state-transition helper that first
  wired this in threaded the position through correctly, but neither of the
  two functions the actual mining/validation path calls
  (`Chain.AddBlock`'s `applyBlockRewarded`, `Chain.CandidateStateRoot`) did —
  they still called the older, position-less `ApplyTx`. Every order minted on
  a real chain would have collided on `OrderID{0,0}`, and `Cancel` would not
  have been able to tell one user's resting order from another's. A
  regression test drives the real `Chain.AddBlock`/`CandidateStateRoot` path
  (not a lower-level helper) across multiple blocks specifically so this
  class of bug cannot hide behind a single-block test again.
- **Book state lives in the exchange account's own storage**, so `StateRoot`
  folds it in automatically — it already sorts storage keys. A second root
  for the book would have been a second thing that could disagree with the
  first.
- **The native coin is the quote asset.** A fill moves real chain balance,
  not a parallel ledger that needs reconciling against it.
- **Two matching modes, and the difference is the point.** `Continuous`
  matches each order as it lands, so whoever is earlier in the block takes
  the better price — on a chain, "earlier" is decided by whoever orders the
  block, which is exactly the option a block producer can sell.
  `BatchAuction` (`Chain.SetExchangeMode`, applied identically by mining and
  validation, same as `ChainID`) clears every order in a block at one uniform
  price instead, via `exchange.BatchSession`: every placement stays
  unmatched until every other placement in the same block has also been
  staged, so position stops mattering. Demonstrated, not just asserted: one
  seller rests supply for exactly one of two competing buyers, and running
  the identical three transactions with the two buyers swapped gives 10/0
  under `Continuous` (whichever is earlier wins everything) and 5/5 under
  `BatchAuction`, either order.
- **Reachable over RPC and the explorer** — `getOrderBookDepth`,
  `getOrderBook`, `getLastAuction`, `getExchangeBalance`, and a live
  `/exchange` page in `web/` polling them, built the same way the rest of the
  explorer is (see `lib/rpc.ts`, `lib/exchange.ts` for the client-side
  calldata encoding that mirrors `exchange.go` exactly, the way `lib/sign.ts`
  already mirrors transaction signing).
- **Stated limit, not silently skipped:** genesis currently premines only
  the native/quote side (`Genesis.Alloc`); there is no minting transaction
  yet for the base asset, so a real crossing trade needs `exchange.CreditBase`
  called directly on state — a genesis/test helper, not something a live
  chain can do today. The batch-clear-and-settle mechanism itself is
  verified directly against real balances (`exchange` package tests,
  `chain/exchange_mev_test.go`); what is not yet exercised is that exact
  path through a full `node.New` → mine → RPC round trip, because nothing
  can fund the sell side of it yet. Genesis base-asset premine, parallel to
  the existing native `Alloc` and threaded through the store's durable
  round-trip the same way, would close this.

## Hardening (post-M4)

Implemented on top of the milestones:

- **Gossip topic validators** — invalid-PoW / bad-merkle blocks and forged-signature txs are rejected before network propagation (anti-amplification), on top of application-time re-validation.
- **mDNS LAN discovery** (`--mdns`) — nodes on the same network auto-discover and connect without explicit `--peers`.
- **Transaction chain-id** — a `ChainID` is folded into the signing preimage, so a tx signed for one chain cannot be replayed on another; enforced identically on the mining and validation paths (`ErrBadChainID`).
- **Bounded mempool** — configurable size cap with a reject-when-full policy (`ErrMempoolFull`).
- **String-encoded amounts on the wire** — large `uint256`/`uint64` fields are decimal strings in JSON-RPC (and the browser signer), avoiding JS `Number` precision loss above 2^53.

Still deferred (heavier / environment-bound):

- Real-internet NAT traversal: DHT peer discovery + AutoNAT/relay (mDNS covers LAN; internet relay untested in the sandbox).
- EVM: run solc-compiled OpenZeppelin contracts, event logs, precompiles. The chain's own canonical state is a real SHA-256 MPT as of M5 (`state/mpt.go`) — what remains deferred is specifically unifying it with the EVM module's own internal, separately-keyed keccak/MPT execution state (M4), which still exists as its own isolated store.

## License

MIT (learning/portfolio project — not audited, not for production value).
