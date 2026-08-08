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
│    ├── chain/     block linkage · finality gate · state trans.      │
│    ├── consensus/ PoW (default) or PoS, selected once at genesis [M8]│
│    ├── pos/       BLS12-381 validators · proposer · attest  [M8]     │
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
| **M6** | Genesis base-asset premine closing the on-chain exchange's last gap (a real crossing trade proven over the full `node.New` → RPC → `MineBlock` round trip); DHT peer discovery + circuit-relay-v2 P2P; real solc-compiled OpenZeppelin ERC20 + precompile calls through the EVM harness, frozen with CI-verified drift detection | ✅ done |
| **M7** | EVM/MPT unification: a new `evm/adapter` package bridges l1chain's own real SHA-256 MPT `state.StateDB` to go-ethereum's full 39-method `vm.StateDB` (snapshots/revert, refunds, self-destruct incl. EIP-6780, transient storage, access lists, `Finalise`), wired directly into `chain/transition.go` — a deployed EVM contract (`evm.DeployAddress`, magic-tag-dispatched) now executes as real chain consensus, not just an isolated harness. Two fully independent `*Chain` instances (one mining via `CandidateStateRoot`, one only ever validating via `AddBlock`) proven to agree on state root over a real EVM workload (a real solc-compiled ERC20 deploy, a nested call with a genuine mid-call revert, a same-tx self-destruct) | ✅ done |
| **M8** | Full Proof-of-Stake consensus mode, additive alongside PoW (a genesis-fixed choice, mirroring the on-chain exchange's own mode switch — PoW stays the default and every pre-existing PoW test is unaffected). Real stake-weighted block production (not a finality overlay on unchanged PoW blocks): BLS12-381 (`blst`) validator signing and aggregable checkpoint attestations, deterministic stake-weighted proposer selection, a finality gate that runs *before* the heaviest-chain reorg rule and rejects any competing branch once ≥2/3 of stake has finalized a checkpoint — even if that branch is nominally heavier — and equivocation detection (double-propose, double-attest) that jails a validator out of future proposer/attestation eligibility. See [Proof-of-Stake consensus](#proof-of-stake-consensus) below | ✅ done |
| **M9** | A real EIP-1559-style fee market for contract/EVM transactions: a per-block `BaseFee` that auto-adjusts toward a gas target (the same recurrence formula real EIP-1559 uses — denominator 8, elasticity 2×) and is independently re-derived and validated by every node from the parent header, never chosen freely by a miner/proposer; the base-fee portion of every payment is burned, the priority-fee portion goes to the block's `Coinbase` as a tip on top of `BlockReward`; a new `Chain.BuildBlockTxs` block builder that actually selects transactions by tip instead of dumping the whole mempool, always respecting per-sender nonce order. Applies identically under PoW or PoS (a state-transition concern, not a consensus-mode one). See [Fee market](#fee-market) below | ✅ done |

## Features

- **Consensus** — dual-mode, chosen once at genesis and never touched again (same shape as the on-chain exchange's own mode switch): Bitcoin-style Proof-of-Work by default (leading-zero-bit targets, difficulty retargeting toward a ~10s block interval, longest-(heaviest-)chain reorg with full state replay), or a full Proof-of-Stake mode (M8) — deterministic stake-weighted proposers, BLS12-381 aggregable attestations, and ≥2/3-stake finality. See [Proof-of-Stake consensus](#proof-of-stake-consensus) below.
- **Native coin** — account/balance model, ECDSA secp256k1 signatures, genesis allocation, fixed block reward (coinbase credited into canonical state), infinite supply.
- **Fee market** — a real EIP-1559-style `BaseFee` (M9) for contract/EVM transactions: auto-adjusts toward a gas target every block, burned on payment; a priority fee (tip) goes to the block producer on top of the fixed reward. See [Fee market](#fee-market) below.
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

`send` also accepts `--max-fee`/`--max-priority-fee` (M9, `GasFeeCap`/`GasTipCap`) so
the transaction it builds is never missing a wire field a contract/EVM-calling
client would need — but `send` itself only ever builds a plain transfer, which
is fee-exempt (see [Fee market](#fee-market) below), so they have no effect on
what `send` actually does today.

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

### Proof-of-Stake (PoS) mode

Everything above defaults to PoW. A PoS chain instead needs a genesis-fixed
validator set — each entry pairs a validator's ordinary secp256k1 address
(the same identity `--miner-key` already provides, reused as proposer
identity) with its BLS public key and stake. Since both keys are normally
freshly generated, the natural flow is to generate first, then start the
real node with those identities registered:

```bash
# generate the two identities first: --consensus pos with no --validators
# prints a fresh miner key and a fresh BLS key, then exits with
# ErrPoSRequiresValidators (no validator set registered yet) --
#   generated miner key: 3f2a... (address 0xabc...)
#   generated validator BLS key: 9e1c... (pubkey 0x8f4...)
go run ./cmd/l1 node --consensus pos

# a single self-proposing PoS node, using the identities printed above (its
# own validator is the only one registered, so it is selected for every slot)
go run ./cmd/l1 node --rpc-addr 127.0.0.1:8545 \
  --consensus pos --slot-interval 2s \
  --miner-key <generated-miner-key-hex> \
  --validator-bls-key <generated-bls-key-hex> \
  --validators <address-hex>:<bls-pubkey-hex>:100
```

`--validators` takes `addrHex:blsPubKeyHex:stake` pairs (comma-separated for
multiple validators), mirroring `--alloc`'s own parser. A node started with
`--validator-bls-key` omitted (or a key that doesn't match any registered
validator) only ever follows/validates PoS blocks and never proposes or
attests — mirrors `--miner-key`'s existing "empty = generate" convention on
the PoW side. See [Proof-of-Stake consensus](#proof-of-stake-consensus)
below for the full design and its named limitations.

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
- `m6-evm-solc-redteam-report.json` — real solc-compiled OpenZeppelin ERC20 deploy/mint/transfer with a real `Transfer` event and a real `Ownable` revert path, plus a real precompile-0x1 (`ecrecover`) call, both through genuine solc bytecode with CI-verified drift detection against the source
- `m7-evm-unification-redteam-report.json` — out-of-gas deploy, revert integrity, reentrancy bound, and cross-chain deterministic root, all proven at the real wired-consensus EVM path (`chain.ApplyTxAt`/`AddBlock`, not just the standalone harness M4/M6 exercised)
- `m8-pos-redteam-report.json` — forged proposer signature (correct claimed identity, wrong/garbage signature) and forged attestation signature both rejected; plus the four required PoS wiring-PR safety tests cited by name (wrong-proposer block, finality gate rejecting a nominally-heavier conflicting branch, double-propose jailing, double-attest jailing), all real CI evidence from the same run that proves the full pre-existing PoW suite unaffected
- `m9-fee-market-redteam-report.json` — a transaction exactly at the `BaseFee` boundary (zero headroom, still includable), the block gas cap enforced on the *sum* of actual usage across multiple transactions rather than any single one's, and the block builder's priced-out-sender exclusion proven not to corrupt selection for other senders; plus the eight required fee-market wiring-PR tests cited by name, all real CI evidence from the same run that proves the full pre-existing M1-M8 suite unaffected

## Design notes

- **Determinism is consensus-critical.** Mining and validation compute block state roots through the *identical* derivation path (replay from genesis, sorted map-free hashing, no wall-clock/randomness). Contract execution (M3) and the state root fold are deterministic so every node agrees.
- **StateDB is an interface** (`state/StateDB`), which is what let the M1 flat-hash placeholder be swapped (M5) for a real Merkle-Patricia-Trie (`state/mpt.go`, now the production default) without touching block validation, RPC, or the VM — the same seam M4's EVM state uses.
- **The EVM is wired into chain consensus as of M7.** Through M6 it was a self-contained execution capability nothing in `chain/` imported (`evm/runtime.go`'s `Harness` ran against go-ethereum's own real `*state.StateDB`, fully disconnected from l1chain's canonical state). `evm/adapter` closes that gap — a `vm.StateDB` implementation backed by l1chain's own real SHA-256 MPT `state.StateDB` — and `chain/transition.go` now dispatches a deployed EVM contract's transactions through it for real, same as the from-scratch VM (M3).

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
- **Genesis funds both sides, and a real crossing trade is proven over
  RPC.** `Genesis.BaseAlloc` (`--base-alloc` on `l1 node`, parallel to the
  existing native/quote `Alloc`/`--alloc`) credits the exchange's base asset
  at genesis via `exchange.CreditBase`, threaded through `Chain`,
  `node.Config`, and the store's durable round-trip the same way `Alloc`
  already was — so a resting sell order, and therefore a real crossing
  trade, is reachable on a freshly started node, not just inside a
  hand-built `state.StateDB` fed directly to `ApplyBlockWithMode`.
  `rpc/exchange_crossing_e2e_test.go` drives the full
  `node.New` → `sendRawTx` (RPC) → `MineBlock` → RPC-read round trip: a
  funded sell and a funded buy cross in the same block, and the clear
  (`getLastAuction`), both parties' settled balances, and the now-flat
  order book are all read back over real HTTP, not asserted in-process.

## Proof-of-Stake consensus

A full, real stake-weighted alternative to PoW — not a lightweight finality
overlay bolted onto unchanged PoW block production — selected once at genesis
and never touched again, exactly like the on-chain exchange's own
`Chain.SetExchangeMode`. Every pre-existing PoW test is unaffected: a PoW
block's `Header.ProposerSig` stays nil forever, so its hash preimage is
byte-for-byte identical to before M8.

- **Validator identity is BLS12-381** (`github.com/supranational/blst`, the
  min-pk convention — pubkeys in G1/48 bytes compressed, signatures in G2/96
  bytes compressed), chosen specifically for its aggregation properties:
  Ethereum's own beacon chain uses BLS over ECDSA for exactly this reason. A
  validator holds two separate keys — its existing secp256k1 identity
  (`--miner-key`, reused as `Header.Coinbase`/proposer identity, unchanged
  from PoW) and a new BLS key (`--validator-bls-key`) that signs blocks and
  attestations.
- **Proposer selection is deterministic and stake-weighted** — a "roulette
  wheel" over each height's active validator set, seeded by
  `hash(parentHash, height)`. Two fully independent `*Chain` instances (one
  proposing, one only ever validating via `AddBlock`) are proven to agree on
  both the selected proposer and the resulting state root after every block.
  **Named limitation**: this seed is *not* an unbiased, grinding-resistant
  randomness beacon (no VRF/RANDAO) — the proposer of block *N* fully
  determines the parent hash block *N+1*'s seed derives from, the moment
  they decide whether to publish. A single-block-lookahead grinding option
  exists; a real randomness beacon is out of scope for M8.
- **Attestations are ordinary signed transactions** to a reserved
  `pos.AttestAddress` (the same "an order is an ordinary transaction" shape
  the on-chain exchange uses) — replay protection, mempool admission, and
  P2P gossip come for free, no gossip-layer changes needed. Every 32 blocks
  (`pos.CheckpointInterval`) is a checkpoint validators vote on; a vote can
  only ride in a block *after* the checkpoint it targets, since a block
  cannot reference its own not-yet-determined hash.
- **Finality is a hard safety gate, not a heavier-chain tiebreaker.** Once
  ≥2/3 of stake has attested to a checkpoint, `Chain.AddBlock` rejects *any*
  block whose ancestry doesn't pass through it — checked *before* the
  existing heaviest-chain reorg rule ever runs, so a conflicting branch can
  never even begin to accumulate weight, let alone win a `td` comparison.
  Proven directly: a branch forking before a finalized checkpoint, made
  deliberately *heavier* by raw accumulated weight than the finalized
  branch's own early history, is still rejected on two independent chain
  instances.
- **Equivocation is detected and jailed, not economically slashed.**
  Double-proposing (two different valid blocks for the same parent) and
  double-attesting (two conflicting votes for the same checkpoint round) are
  both detected in `AddBlock` and immediately exclude the validator from
  future proposer selection and future attestation tallies — confirmed by
  test, not just logged. No stake burn/seizure: M8's stake is immutable
  genesis config, not a mutable balance, so there is nothing to seize
  without reopening live-deposit scope (see deferred list below).
- **PoW is fully preserved as a selectable legacy mode.** `consensus.Mode`
  defaults to `PoW`; every one of the ~100+ pre-existing PoW tests passes
  unchanged, in the same CI run as every new PoS test.

See [`m8-pos-redteam-report.json`](artifacts/m8-pos-redteam-report.json) for
adversarial evidence (forged proposer/attestation signatures) plus the
required safety-test citations, and the [PoS quickstart](#proof-of-stake-pos-mode)
above for a runnable example.

**Explicitly deferred for M8** (stated plainly, not silently avoided):

- No VRF/RANDAO-style unbiased randomness beacon (see the named grinding
  limitation above).
- No liveness/skip-slot mechanism — if the selected proposer for a height is
  offline, block production stalls at that height indefinitely. The single
  biggest liveness limitation of this design.
- No dynamic validator set — validators are genesis-fixed; no live deposit,
  activation delay, exit queue, or unbonding.
- No economic slashing — detection + jailing only, not stake burn/seizure.
- No cross-shard/sharding, no weak-subjectivity checkpoints, no
  light-client sync-committee-style proofs for PoS finality (existing MPT
  account/storage proofs are unrelated to this).
- No gossip-layer signature verification for PoS blocks — `blockTopicValidator`
  only checks structural shape (`len(ProposerSig) == pos.SignatureSize`) at
  the gossip layer, since it has no chain/validator-set reference today. Real
  verification is never skipped — it's just not pre-filtered pre-propagation
  the way PoW's `MeetsTarget` check is; it always runs, un-skippable, in
  `AcceptExternalBlock → AddBlock`.
- A narrow attestation-persistence gap: `AddBlock` tallies attestations for
  every accepted block (canonical or not, by design — this permanence is
  what makes finality survive a later reorg), but `store.Load` only replays
  the canonical branch on restart. A long-lived process and a freshly
  restarted one can end up with slightly different finality/jailing state,
  specifically for attestations that were only ever included in a since-
  orphaned block.

## Fee market

A real EIP-1559-style fee market, applying to contract/EVM transactions —
the two transaction shapes that already had a gas concept before M9. Plain
transfers, on-chain exchange orders, and PoS checkpoint attestations remain
exactly as fee-exempt as they always were; extending metered pricing to
paths that never had a gas concept would be a separate, much larger economic
redesign than "add EIP-1559 to the parts that already charge gas," not
something M9 silently expanded into. Applies identically under PoW or PoS —
the fee market is a state-transition concern, not a consensus-mode one.

- **`BaseFee` is never chosen freely.** Every block's `Header.BaseFee` is
  independently re-derived by `Chain.AddBlock` from the parent header
  (`chain.ComputeBaseFee` — unchanged at exactly the gas target, moves by up
  to 1/8 per block toward it, guaranteed to move by at least 1 unit when
  above target so integer division can never stall the correction) and
  rejected (`ErrBadBaseFee`) if a block's claimed value doesn't match — the
  fee-market analog of the PoW/PoS proposer checks. The elasticity multiplier
  (2×, same as real EIP-1559) means a block may go up to the full configured
  gas cap before `BaseFee` starts correcting it back down.
- **Every transaction pays a `GasFeeCap` (hard ceiling) and offers a
  `GasTipCap` (priority fee)** — the real dual-fee structure, not a
  single-price approximation. The actual price paid is
  `BaseFee + min(GasTipCap, GasFeeCap - BaseFee)`
  (`chain.EffectiveGasPrice`): the base-fee portion is burned — credited to
  nobody, the same silent-vanish mechanism this chain already had for every
  gas payment before M9, now an intentional, documented protocol rule — and
  the priority-fee portion is credited to the block's `Coinbase` as a tip, on
  top of the fixed `BlockReward`.
- **`Chain.BuildBlockTxs` replaces "dump the entire mempool."** Every
  plain-transfer/exchange/attestation transaction is still included
  unconditionally (unbounded — a named, pre-existing limitation this
  milestone doesn't change). Fee-priced transactions are greedily selected by
  highest effective priority fee, bounded by the chain's configured gas cap,
  always respecting each sender's own nonce order: a sender's queue is only
  ever advanced from the front, never skipped into, so a transaction that is
  priced out (`GasFeeCap` below `BaseFee`) or doesn't fit the remaining
  budget blocks every *later* transaction from that same sender in this
  block — including free-pass ones after it — exactly like a real
  transaction pool must (skipping ahead would make the block fail
  `AddBlock`'s own `ErrBadNonce` check).
- **The `BASEFEE` opcode (0x48) is real for the embedded EVM**, closing a
  limitation named since M7: `evm/adapter`'s `BlockContext.BaseFee` was
  hardcoded to zero through M8; a contract now reads the same real,
  consensus-agreed value `AddBlock` independently validated for that block.

See [`m9-fee-market-redteam-report.json`](artifacts/m9-fee-market-redteam-report.json)
for adversarial evidence (the exact `BaseFee` boundary, the gas cap enforced
on a multi-transaction sum rather than any single transaction, priced-out
senders not corrupting other senders' selection) plus the required
safety-test citations, and the [PoS quickstart](#proof-of-stake-pos-mode)-adjacent
`send` flags above for the (currently inert, since `send` only builds plain
transfers) CLI surface.

**Explicitly deferred for M9** (stated plainly, not silently avoided):

- No miner-voted gas limit adjustment — the block gas cap is fixed for the
  chain's entire life at genesis, unlike real Ethereum's slow ±1/1024-per-block
  miner nudge.
- No fee-pricing for plain transfers, exchange orders, or PoS attestations
  (see this section's own scope note above) — these remain exactly as free
  as they are today.
- No EIP-2930 access lists, no EIP-4844 blob fees.
- No mempool fee-bump replacement (resubmitting the same nonce at a higher
  tip to displace a stuck transaction).
- No receipt/`getTransactionReceipt` exposure of actual gas-used-per-transaction
  — a pre-existing M7 gap (see [Hardening](#hardening-post-m4) below), not
  newly closed here.
- No block-count/byte limit for the non-fee-priced transaction types (plain
  transfer/exchange/attestation) — the mempool's existing size cap is the
  only bound, unchanged from before M9.

## Hardening (post-M4)

Implemented on top of the milestones:

- **Gossip topic validators** — invalid-PoW / bad-merkle blocks and forged-signature txs are rejected before network propagation (anti-amplification), on top of application-time re-validation.
- **mDNS LAN discovery** (`--mdns`) — nodes on the same network auto-discover and connect without explicit `--peers`.
- **DHT peer discovery** (`--dht`, `--dht-bootstrap`) — the non-LAN counterpart to `--mdns`: a real Kademlia DHT (`github.com/libp2p/go-libp2p-kad-dht`, isolated under its own `/l1chain` protocol prefix so it never joins the public IPFS Amino DHT), advertising and finding other l1chain nodes under a shared rendezvous and auto-dialing whatever it finds. `TestDHTDiscoveryFindsAndDialsPeers` proves three real l1chain nodes, each seeded only with the *other two's* address (never a third), end up mutually connected purely through DHT `Advertise`/`FindPeers` — a different bootstrap mechanism, not a NAT-crossing claim.
- **Circuit-relay-v2** (`--relay-service`, `--relay`) — a node can choose to relay other peers (`--relay-service`), and a node behind one is reachable at `/p2p/<relay>/p2p-circuit/p2p/<this node>` without a directly dialable address of its own (`--relay`). `TestCircuitRelayConnectsTwoNodesNeverDirectlyDialed` proves it genuinely, not mocked: two real l1chain nodes that are never directly connected to each other reach real `Limited`-connectedness through a third relay node (the same property go-libp2p's own strongest relay test asserts), and — going further — a block mined on one gossip-converges to the other entirely through that relay circuit.
- **QUIC transport** (`p2p/host.go`) — every node now also listens over `/udp/<port>/quic-v1` alongside TCP (go-libp2p already silently registers QUIC via its own `DefaultTransports`; only a QUIC-shaped listen address was missing). `TestHostListensOnQUICAndStillGossips` proves the address is actually live, not just present in the option list, and that real block gossip still converges over the QUIC-inclusive listen set.
- **DCUtR hole-punching against a real simulated NAT** — `TestDCUtRHolePunchesThroughSimulatedNAT` ports go-libp2p's own strongest hole-punch proof (`p2p/protocol/holepunch`'s `TestEndToEndSimConnect`, backed by `github.com/marcopolo/simnet`'s packet-level, address-restricted-cone-NAT-like firewall simulator — a real simulator, not a mock) onto this repo's own node/host/gossip stack, then goes further: once hole punching replaces the relayed connection with a direct one, a real block gossip-converges over exactly that punched connection. One honest caveat: the test is excluded from the `-race` sweep specifically, because it reliably trips a confirmed, pre-existing data race inside go-libp2p v0.48.0's own holepunch service (a network notifiee goes live and can read a struct field its own constructor's caller is still writing) — root-caused by direct line-by-line reading of the vendored source and documented inline in `p2p/holepunch_test.go`, not a bug in this repo's code. It still runs, and passes, under the plain (non-race) test suite.
- **Transaction chain-id** — a `ChainID` is folded into the signing preimage, so a tx signed for one chain cannot be replayed on another; enforced identically on the mining and validation paths (`ErrBadChainID`).
- **Bounded mempool** — configurable size cap with a reject-when-full policy (`ErrMempoolFull`).
- **String-encoded amounts on the wire** — large `uint256`/`uint64` fields are decimal strings in JSON-RPC (and the browser signer), avoiding JS `Number` precision loss above 2^53.

Still deferred (heavier / environment-bound):

- **AutoNAT (self NAT detection).** DHT discovery, circuit-relay, and DCUtR hole-punching above are all real and proven (the last two, plus QUIC transport, as of this section). AutoNAT specifically — a node determining *whether it is itself* behind a NAT — remains unclaimed: go-libp2p's own AutoNAT tests only exercise this via mocked/scripted peer responses or a `ForceReachability*` override, never a real NAT, so there is nothing to honestly claim beyond what's already true by default (client-side relay dialing is on regardless of reachability).
- **Receipt / log persistence.** EVM/MPT unification is done as of M7 — deployed EVM contracts execute as real chain consensus against l1chain's own canonical SHA-256 MPT state, not an isolated store (see the M7 milestone row above and [`m7-evm-unification-redteam-report.json`](artifacts/m7-evm-unification-redteam-report.json)). What remains deferred: logs are correctly generated and journaled per call (a reverted nested call's log does not survive, the same guarantee `evm/adapter`'s own isolated tests prove) — but `applyEVMTx` never captures `sdb.Logs()` before returning, so nothing persists them. There is no receipt store, no `getTransactionReceipt` RPC method, and no header field committing to them — consistent with this repo's pre-existing precedent (M3's own contract calls never persisted receipts either, so this is not a regression), but a natural, separately-scoped follow-on.

## License

MIT (learning/portfolio project — not audited, not for production value).
