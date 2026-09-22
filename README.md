# go-hyperliquid

Low-latency Go SDK for the [Hyperliquid](https://hyperliquid.xyz) exchange
(HyperCore L1, fully on-chain order book), built for HFT / market making.
Sibling of `go-okx`, `go-bybit`, `go-kucoin`, `go-aster`.

## Status

| Version | Scope | State |
|---|---|---|
| **v1.0** | Perpetuals (validator-operated perp dex) | code complete; order path awaits live testnet confirmation |
| v2.0 | Spot | planned |
| v2.5 | HIP-3 builder-deployed perps, HIP-4 outcome markets, TWAP, transfers, sub-accounts, vaults | planned |

See [handoff.md](handoff.md) for the roadmap and [docs/API-NOTES.md](docs/API-NOTES.md)
for the verified exchange facts (including every place where the official docs
and the official Python SDK disagree).

## Highlights

- **Signing byte-for-byte compatible with the official Python SDK** — L1 actions
  (MessagePack → keccak → phantom agent → EIP-712 → secp256k1) and user-signed
  actions, enforced by 30 reference vectors generated with that SDK
  (`scripts/gen-signing-vectors.py`).
- **Zero-allocation hot path**: hand-written MessagePack / JSON writers with an
  explicit, reviewable field order; `types.Fixed` (int64, 8 decimals) for prices
  and sizes; lock-free nonce generator and rate-limit window.
- **Exchange rounding rules without floats**: 5 significant figures,
  `MAX_DECIMALS − szDecimals`, integer-price rule — `types.Precision`.
- **Two transports for trading**: REST and WebSocket `post` requests behind the
  same API (`Trading()` / `Trading().WS()`).
- **SDK-side rate-limit accounting** (the exchange sends no headers): IP weights
  per request + address-budget mirror, reported through one observer.
- **Two-layer architecture**: one common layer, thin sections — no copy-paste.
- Pure Go (`CGO_ENABLED=0`), 5 dependencies, mainnet / testnet by config, proxy
  and custom `http.Client` support.

## Quick start

```go
var cfg hyperliquid.Config = hyperliquid.DefaultConfig()
cfg.Testnet = true
cfg.AccountAddress = os.Getenv("HYPERLIQUID_PERPETUALS_TESTNET_API_KEY")    // master account address
cfg.PrivateKey = os.Getenv("HYPERLIQUID_PERPETUALS_TESTNET_SECRET_KEY")     // API wallet private key

var client *hyperliquid.Client
client, err = hyperliquid.NewClient(cfg)
defer client.Close()

var perps *perpetuals.Client = perpetuals.NewClient(client)

var info types.AssetInfo
info, err = perps.MarketData().GetAssetInfo(ctx, "BTC")

var price types.Fixed = info.Precision.NormalizePrice(rawPrice, types.RoundDown) // bids round down
var size types.Fixed = info.Precision.NormalizeSize(rawSize, types.RoundDown)

var status types.ActionStatus
status, err = perps.Trading().CreateOrder(ctx, types.OrderRequest{
	Coin: "BTC", IsBuy: true, Price: price, Size: size,
	TimeInForce: types.TimeInForceAlo, // post-only
	Cloid:       cloid,
}, types.OrderOptions{})
```

### Authentication

Hyperliquid has no API key / secret — requests are signed by a wallet.

| Config field | Meaning |
|---|---|
| `AccountAddress` | master account address; the `user` of account-scoped info requests |
| `PrivateKey` | private key of the **API wallet** (agent) approved by that account |
| `VaultAddress` | optional sub-account / vault to trade on behalf of (sent as `vaultAddress`, becomes the info `user`) |

Never query info with the API wallet's own address — the exchange returns an
empty state instead of an error. The key is never logged, never echoed in
errors, removed from `Config` after `NewClient` and zeroed on `Close()`.
Recommended account mode for HFT: **standard** (unified / portfolio margin are
capped at 50k actions per day).

### Things that differ from a CEX

| | Hyperliquid |
|---|---|
| Market order | none — aggressive IOC limit (`MarketData().SlippagePrice`, `Account().ClosePosition`) |
| Time in force | `Alo` (post-only), `Ioc`, `Gtc`; no FOK |
| Tick size | none — 5 significant figures; use `AssetInfo.Precision`, do not cache a tick |
| Asset ids | from metadata, differ between mainnet and testnet |
| Cancel all | emulated (open orders + one batch cancel); exchange-side: `ScheduleCancel` dead man's switch |
| Order book | snapshot-only feed (≤20 levels, `Fast`: 5 levels); `WatchBBO` is the fastest public price feed |
| Order ack | `/exchange` answers after block inclusion |
| Errors | no codes — `hyperliquid.IsMissingOrder(err)`, `IsReason(err, ReasonBadAloPx)`, … |

## Layout

```
client.go config.go …   root package: Client, Config, errors, logger, metrics, rate-limit events
types/                  Fixed, Precision, Cloid, requests, market / account types
internal/               codec, msgpack, signing, nonce, action, rest, ws, ratelimit, assets, engine, domain
perpetuals/             section: Trading / Account / MarketData / Stream
examples/               market-data (keyless), simple-trade (testnet, gated)
scripts/                gen-signing-vectors.py, run.sh
```

## Examples

```bash
go run ./examples/market-data                         # keyless, testnet
HYPERLIQUID_MAINNET=1 go run ./examples/market-data   # keyless, mainnet (read-only)

cp .env.example .env                                  # TESTNET credentials, HYPERLIQUID_ALLOW_LIVE=1
./scripts/run.sh ./examples/simple-trade              # place → query → modify → cancel, far from the market
```

Order-sending examples are hard-wired to the testnet and refuse to run without
`HYPERLIQUID_ALLOW_LIVE=1`.

## Development

```bash
make check    # gofmt + vet + tests with -race
make bench    # hot-path benchmarks (ns/op, allocs/op)
make vectors PYSDK=<python-sdk clone> PYTHON=<venv python>   # regenerate reference vectors
```

Benchmarks (Apple M4 Pro, Go 1.25): order → MessagePack 47 ns, → JSON 43 ns,
hash + EIP-712 digest 0.75 µs, nonce 31 ns, `Fixed` wire format 13 ns — all
0 allocs/op; secp256k1 signature 20.5 µs (pure Go, RFC 6979).

## Code style

House style of the sibling SDKs: English comments, `FILE:` header blocks,
explicit `var x T = ...` declarations, `context.Context` first, JSON only via
`internal/codec`, no panics in library code, zero allocations on hot paths.
Details: [handoff.md §4](handoff.md).

## License

See [LICENSE](LICENSE).
