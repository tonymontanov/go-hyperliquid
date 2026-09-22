# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

## [1.0.0] — 2026-09-22 — Perpetuals

First public release. The order path is verified against the official Python
SDK's signing vectors and an in-process exchange mock; live confirmation of
the exchange's acceptance of the docs-only `f` / `a` flags is still pending.

### Added
- Common layer: root `hyperliquid` package (`Client`, `Config`, error / logger /
  metrics re-exports, rate-limit events); `types` (`Fixed`, `Precision`, `Cloid`,
  requests, market and account types).
- Signing: hand-written MessagePack writer, L1 phantom-agent and user-signed
  EIP-712 schemes, pure-Go secp256k1 signer, lock-free per-signer nonce generator.
  30 reference vectors generated with the official Python SDK.
- Actions: `order`, `cancel`, `cancelByCloid`, `modify`, `batchModify`,
  `scheduleCancel`, `noop`, `updateLeverage`, `updateIsolatedMargin` (incl. the
  docs-only `f` / `a` flags, builder codes, `expiresAfter`, `vaultAddress`).
- Transports: REST (`/info`, `/exchange`, proxy / custom client), supervised
  WebSocket (resubscribe, keepalive, `post` requests).
- SDK-side rate-limit accounting: IP weight window, address-budget mirror,
  `RateLimitEventObserver`, optional local fail-fast guard.
- `perpetuals` section: Trading (REST and WS post), Account, MarketData, Stream.
- Examples `market-data` (keyless) and `simple-trade` (testnet, gated).
- Tooling: Makefile, golangci-lint config, GitHub Actions (fmt, vet, race,
  cgo-free build, lint), vector generator script.
- Shared WebSocket subscriptions: several consumers of one feed share a single
  exchange subscription; `ErrSubscriptionConflict` guards incompatible payloads.
- `Stream().WatchAccountState` (`clearinghouseState` snapshots) and
  `Stream().IsConnected()`.
- Own RFC 6979 signing routine on decred primitives (byte-identical to
  `ecdsa.SignCompact`, 12 allocations per signature instead of 29).

### Fixed (found by live smoke runs before the first release)
- `DefaultConfig()` + `Testnet = true` kept the mainnet URLs.
- Exchange statistics with more than 8 decimals failed `Fixed` decoding.
- One out-of-range `allMids` entry (testnet junk pair) failed the whole request.

[1.0.0]: https://github.com/tonymontanov/go-hyperliquid/releases/tag/v1.0.0
