# handoff.md — go-hyperliquid SDK

Context document for continuing work across sessions. **Update after every
significant task** (architecture change, new module, refactoring).

Last update: 2026-09-22 (evening) — v1.0 SDK code complete; desk connector
implemented on branch `hyperliquid-connector` of sleipnir-trading-core (5
commits); both await live order-path verification on testnet.

---

## 1. Role & stack

- **What:** low-latency Go SDK for the Hyperliquid exchange (HyperCore L1, fully
  on-chain order book) for an HFT / market-making desk. Sibling of `go-okx`,
  `go-bybit`, `go-kucoin`, `go-aster`; consumed by `sleipnir-trading-core`.
- **Module:** `github.com/tonymontanov/go-hyperliquid` (v1.x). Following the
  precedent of the sibling SDKs the path becomes `.../go-hyperliquid/v2` at the
  v2.0 (spot) release.
- **Go:** 1.24 (the desk builds with Go 1.24.6 and `CGO_ENABLED=0` — the SDK
  must stay pure Go; CI enforces a cgo-free build).
- **Dependencies (5, same family as the siblings):**
  `gorilla/websocket`, `json-iterator/go`, `shopspring/decimal`,
  `decred/dcrd/dcrec/secp256k1/v4`, `golang.org/x/crypto` (legacy Keccak-256).
  No go-ethereum (cgo + forces a Go 1.26 toolchain), no msgpack library
  (hand-written writer, see §2).
- **Source ToR:** `docs/TS-SINGLE-EXCHANGE-SDK-RU.md` / `-EN` (shared spec of all
  single-exchange SDKs). Hyperliquid-specific deviations are listed in §2.4.

## 2. Architecture

### 2.1 Two-layer principle (no parallel copy-paste)

One **common layer** implements every request once; each **section** is a thin
specialisation that contributes only a `domain.Profile` (name, dex, asset
registry with the section's asset-id formula and MAX_DECIMALS). Sections never
import or call each other. Section names mirror the exchange's vocabulary.

```
section.Trading().CreateBatchOrders(ctx, reqs, opts)
   └─ domain.PlaceOrders(ctx, engine, profile, reqs, opts, transport)     ← unified
        └─ engine.Action(ctx, action, opts)  nonce → msgpack → sign → JSON → REST | WS post → decode
```

| Section (official name)                 | Package      | Core constant                  | Status |
|-----------------------------------------|--------------|--------------------------------|--------|
| Perpetuals (validator-operated, dex "") | `perpetuals` | `hyperliquid_perpetuals[_testnet]` | v1.0 ✅ |
| Spot                                    | `spot`       | `hyperliquid_spot[_testnet]`   | v2.0 📋 |
| HIP-3: Builder-deployed perpetuals      | `hip3`       | `hyperliquid_hip3[_testnet]`   | v2.5 📋 |
| HIP-4: Outcome markets                  | `outcomes`   | `hyperliquid_outcomes[_testnet]` | v2.5 📋 |

Naming approved by the owner on 2026-09-22.

### 2.2 Folder structure

```
go-hyperliquid/
├── client.go config.go errors.go logger.go metrics.go rate-limit-event.go doc.go
│                      root package `hyperliquid` = public face of the common layer
├── types/             layer-1 public types
│   ├── fixed.go precision.go      Fixed (int64, 8 decimals) + exchange rounding rules
│   ├── cloid.go enums.go          128-bit client order id (UUID-compatible), wire enums
│   ├── orders.go action-result.go requests, order views, per-item action statuses
│   └── market.go account.go       book / bbo / trades / candles / ctx; positions / fills
├── internal/
│   ├── codec/        jsoniter chokepoint (CaseSensitive!), RawMessage
│   ├── hlerr/ hllog/ hlmet/       error model, logger, metrics (re-exported by root)
│   ├── msgpack/      append-style MessagePack writer (no reflection)
│   ├── signing/      keccak, EIP-712 (L1 phantom agent + user-signed), signer, addresses
│   ├── nonce/        lock-free per-signer nonce generator
│   ├── action/       action builders: paired AppendMsgpack / AppendJSON, same field order
│   ├── rest/         POST /info, POST /exchange; status mapping; proxy / custom client
│   ├── ws/           supervised conn: subscriptions, keepalive, post requests
│   ├── ratelimit/    request weights + lock-free 60 s IP window + address budget mirror
│   ├── assets/       atomic asset registry (coin ↔ asset id ↔ precision)
│   ├── engine/       THE unified request layer (Info / Action), rate accounting, decode
│   └── domain/       unified domain functions parameterised by Profile
├── perpetuals/       section: client.go trading.go account.go market.go stream.go
├── examples/         market-data (keyless), simple-trade (testnet, gated)
├── scripts/          gen-signing-vectors.py (official Python SDK, offline), run.sh
└── docs/             ToR (RU/EN), API-NOTES.md (verified exchange facts)
```

Dependency direction: `types` and `internal/*` never import the root; the root
imports `internal/*`; sections import the root + `internal/*`. The root never
imports a section → no cycle and **no `any` + type assertion** on the public API
(`perpetuals.NewClient(client)` is an ordinary typed constructor — deliberate
deviation from the sibling SDKs' `client.Swap().(*swap.Client)`).

### 2.3 Key protocol facts (verified — see docs/API-NOTES.md for sources)

- Two POST endpoints: `/info` (`"type"` selects the request) and `/exchange`
  (signed actions) + `/ws` (subscriptions and `post` requests). `/exchange`
  answers only after the action is included in a block.
- **No API key/secret.** L1 actions: `keccak(msgpack(action) ++ nonce(8 BE) ++
  vault flag/addr ++ [0x00 ++ expiresAfter(8 BE)])` → phantom agent
  `{source: "a" mainnet | "b" testnet, connectionId}` → EIP-712 domain
  `Exchange / 1 / chainId 1337` → secp256k1 (RFC 6979). User-signed actions use
  domain `HyperliquidSignTransaction` and `signatureChainId` (we send `0x66eee`
  like the Python SDK). The algorithm is NOT in the GitBook docs — the Python
  SDK is the only reference; parity is enforced by vector tests.
- **Field order is part of the signature**; `"f"` (fast cancel) and `"a"`
  (always_place) must be OMITTED when false; prices/sizes have no trailing zeros;
  addresses are lower-case.
- Nonce: per SIGNER, 100 highest kept, window (T−2d, T+1d). One API wallet per
  trading process (desk runs one core per account → no cross-process sharing).
- Asset ids differ between mainnet and testnet (BTC = 0 / 3 on 2026-09-22) →
  always from metadata. Perps: index in `meta.universe`; spot: 10000 + index;
  HIP-3: 100000 + dexIndex·10000 + index; outcomes: 100000000 + 10·outcome + side.
- Price: ≤5 significant figures AND ≤ MAX_DECIMALS − szDecimals decimals
  (6 perps / 8 spot); integers always valid. No static tick size.
- Book feed is **snapshot-only** (≤20 levels, `fast` = 5 levels). No deltas / seq
  / checksum → the ToR's snapshot+delta engine does not apply (§2.4).
- No rate-limit headers → SDK-side accounting: IP 1200 weight/min (info 2/20/60,
  action 1 + ⌊n/40⌋); address budget via `userRateLimit` (batch of n costs n).
- Errors have no codes: classification is by TEXT (`internal/hlerr`).
- Account mode: recommend **standard**; unified / portfolio margin are capped at
  50k actions/day and move balances to the spot clearinghouse state.

### 2.4 Deliberate deviations from the ToR / sibling SDKs

| Topic | Decision | Why |
|---|---|---|
| Orderbook engine (snapshot+delta+seq+gap) | not built; snapshot types + `bbo` | the public feed has no deltas |
| Numerics | `types.Fixed` on hot paths, `decimal` elsewhere | zero-alloc requirement; localised to SDK + connector (owner's condition) |
| Section accessors | `perpetuals.NewClient(client)` | compile-time safety, no `any` |
| Rate limits | SDK-side weight accounting + observer | the exchange sends no headers |
| Market / FOK orders, cancel-all | market = IOC + slippage; FOK unsupported; cancel-all emulated | no native actions |
| WS lifetime | conns belong to the Client; ctx of `Watch*` unsubscribes one stream | shared connection (10-conn IP limit) |

## 3. Roadmap

### ✅ Done (2026-09-22) — v1.0 SDK, Perpetuals
- M0 skeleton, tooling: Makefile, `.golangci.yml`, GitHub Actions (fmt, vet, race,
  cgo-free build, lint), `.env.example`, `scripts/run.sh`.
- M1 signing: msgpack writer, L1 + user-signed EIP-712, signer (decred), nonce.
  **30 reference vectors** (22 action-level × mainnet/testnet + 8 user-signed)
  generated by the official Python SDK — all byte-identical.
- M2 transport: REST, supervised WS (resubscribe + Reset, keepalive, post
  requests with fail-fast / fail-pending semantics), rate-limit window.
- M3 common domain layer: engine, asset registry, actions (order, cancel,
  cancelByCloid, modify, batchModify, scheduleCancel, noop, updateLeverage,
  updateIsolatedMargin), info requests, streams.
- M4 `perpetuals` section + contract tests (request body, signature recovery,
  statuses, validation-before-network, section filtering, rate accounting, WS).
- M5 examples; keyless live smoke on testnet AND mainnet (found and fixed: the
  `DefaultConfig()+Testnet` URL trap, >8-decimal statistics, junk `allMids`
  entry — each has a regression test).
- Later the same day, driven by the desk connector: shared WS subscriptions
  (several consumers of one route key → one exchange subscription, copy-on-
  write fan-out, `ErrSubscriptionConflict` for a different payload on the same
  key); `clearinghouseState` stream (`Stream().WatchAccountState`, absolute
  account snapshots) + `StreamClient.IsConnected()`; own RFC 6979 signing on
  decred primitives (`internal/signing/rfc6979.go`, byte-identical, 12 allocs
  instead of 29 — the rest are inside decred's math/big inverse; pinned by
  `TestSignDigestAllocationBudget`, differential test vs `ecdsa.SignCompact`).
- M6 desk connector — see the core's `handoff.md` («🔧 В работе (22.09.2026)»).

Benchmarks (Apple M4 Pro): msgpack 47 ns / JSON 43 ns / digest 0.75 µs / nonce
31 ns / Fixed wire 13 ns — all 0 allocs; ECDSA sign 20.8 µs, 12 allocs
(decred's `ecdsa.SignCompact`: 20.7 µs, 29 allocs).

### 🔧 In progress
- **Live verification of the ORDER path on testnet** — run by the owner (keys
  never leave his machine): `./scripts/run.sh ./examples/simple-trade`, also with
  `HYPERLIQUID_USE_WS_POST=1`. Until it passes, treat the docs-only shapes as
  unproven on the exchange side: `"f"`, `"a"`, single `modify` (not used yet).

### 📋 Planned
- (done 2026-09-22) module published: `main` + tag `v1.0.0` on GitHub, the
  desk's `go.mod` requires it directly.
- Allocation-free modular inverse (safegcd) to remove the last 12 allocs of a
  signature — only worth it with a benchmark showing GC-jitter impact.
- To verify on testnet: is `cloid` present in fills (not in the docs' WsFill);
  the `a=false` modify restriction; the 20-level l2Book cadence (docs 0.5 s,
  observed slower); top-level error text for an exhausted address budget.
- v2.0 `spot`; v2.5 `hip3`, `outcomes`, TWAP, transfers (usdClassTransfer,
  sendAsset), sub-accounts, vaults, approveAgent / approveBuilderFee
  (signing for user-signed actions is already implemented and vector-tested).

## 4. Rules & code style

- English comments and docs everywhere (public project). Every file starts with
  the `FILE / DESCRIPTION / MAIN FUNCTIONS / DEPENDENCIES` block; em-dash doc
  comments on exported identifiers; a comment on every const of an enum.
- camelCase locals, PascalCase exports, initialisms upper-case (`AssetID`,
  `URL`), millisecond timestamps suffixed `Ms`. File names kebab-case
  (`action-result.go`), tests `*_test.go`.
- **Explicit declarations:** `var x T = ...`, pre-declared multi-returns, loop
  counters declared outside `for`; `:=` only in `if err := ...` guards,
  `select` cases and tests.
- `context.Context` first; never `context.Background()` inside a call that has
  a ctx (the Client's life context is the one documented exception).
- JSON only through `internal/codec`; no `encoding/json`. No panics in library
  code (only `MustParseFixed`, for tests/constants).
- **Hot path = zero allocations** (order build, msgpack, JSON body, nonce, rate
  window, price normalisation). Error messages are assembled on failure paths
  only. A hot-path change ships with before/after benchmarks; allocs/op must
  not grow. `make bench`.
- Validation before network; the SDK never rounds caller prices silently.
- **Never invent API fields.** Model only documented keys (decode leniently);
  when docs and the Python SDK disagree, record it in `docs/API-NOTES.md`.
- Sections never import each other; shared code moves DOWN into `internal/domain`.
- Tests: stdlib `testing`, inline fixtures, no network. Live checks are runnable
  examples behind `HYPERLIQUID_ALLOW_LIVE=1`, testnet only.

## 5. Integration secrets (names only — NEVER real keys)

| Variable | Meaning |
|---|---|
| `HYPERLIQUID_PERPETUALS_TESTNET_API_KEY` | master account address |
| `HYPERLIQUID_PERPETUALS_TESTNET_SECRET_KEY` | API wallet (agent) private key |
| `HYPERLIQUID_PERPETUALS_TESTNET_PASSPHRASE` | optional sub-account / vault address |
| `HYPERLIQUID_PERPETUALS_API_KEY / _SECRET_KEY / _PASSPHRASE` | mainnet triple (desk) |
| `HYPERLIQUID_ALLOW_LIVE=1` | safety gate of order-sending examples |

Mapping onto the desk's `credentials.json`: `api_key` = master address,
`secret_key` = API wallet key, `passphrase` = optional vault address (same
pattern as Aster). `.env` is git-ignored; the key is never logged, never echoed
in errors, wiped from `Config` after `NewClient` and zeroed on `Close()`.

Endpoints: mainnet `https://api.hyperliquid.xyz` / `wss://api.hyperliquid.xyz/ws`;
testnet `https://api.hyperliquid-testnet.xyz` / `wss://…-testnet.xyz/ws`.
Official references: GitBook API docs, `hyperliquid-dex/hyperliquid-python-sdk`
(vectors generated from commit 2fdb18f, 2026-06-05).
