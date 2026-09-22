# Hyperliquid API — verified notes

Working notes behind the SDK. Every statement is tagged with its source:

- **[DOC]** official GitBook docs (https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api), fetched 2026-09-22;
- **[PY]** official Python SDK `hyperliquid-dex/hyperliquid-python-sdk`, commit `2fdb18f` (2026-06-05);
- **[LIVE]** read-only public requests to mainnet / testnet on 2026-09-22 (no keys).

Rule of the project: nothing here is guessed. "Not documented" means exactly that.

## 1. Where the docs and the Python SDK disagree or fall short

| # | Topic | Finding | SDK decision |
|---|---|---|---|
| 1 | L1 signing algorithm | **Not in [DOC]** — the signing page only names the two schemes and defers to the SDK. [PY] `utils/signing.py` is the sole reference. | Ported 1:1, enforced by 30 [PY]-generated vectors. |
| 2 | In [DOC], absent from [PY] | `twapOrder` / `twapCancel`, single `modify`, cancel flag `f`, modify flag `a`, `userOutcome` + `outcomeMeta` (HIP-4), `agentSendAsset`, `reserveRequestWeight`, `hip3LiquidatorTransfer`, `topUpIsolatedOnlyMargin`, WS `post` requests, ~10 subscription types (`webData3`, `openOrders`, `clearinghouseState`, `spotState`, `allDexs*`, `fastAssetCtxs`, `twapStates`, …). | Key order taken from the [DOC] request tables; msgpack parity with msgpack-python is vector-tested; **exchange acceptance must be confirmed on testnet**. |
| 3 | In [PY], absent from [DOC] | `createSubAccount`, `subAccountTransfer`, `subAccountSpotTransfer`, `setReferrer`, multi-sig payloads, `evmUserModify`, units of `vaultTransfer.usd` (micro-USD int), priority grouping `{"p": N}` (only on the priority-fees page), `gossipPriorityBid`. | Follow [PY] when implemented (v2.5). |
| 4 | `signatureChainId` | [DOC] example `0xa4b1`; [PY] `0x66eee`. Both valid ("can be any chain"). | `0x66eee` — matches the [PY] vectors. |
| 5 | Signature `r` / `s` format | [DOC] shows only `{"r","s","v"}`; [PY] sends minimal hex (no leading zeros, `eth_utils.to_hex(int)`). | Same minimal hex as [PY]. |
| 6 | Request envelope | [DOC] lists `vaultAddress` / `expiresAfter` as optional; [PY] always sends both, `null` when unset. | Same as [PY]. |
| 7 | HIP-3 / outcome price decimals | MAX_DECIMALS is documented only for perps (6) and spot (8). [PY] `_slippage_price` treats every asset id ≥ 10000 as spot (8) — that includes HIP-3 perps (≥ 110000). Possibly a [PY] bug. | Open question for v2.5; verify on testnet. |
| 8 | Spot tick example | The prose example ("greater than 2") contradicts the formula `8 − szDecimals`. | The formula (also used by [PY] `examples/rounding.py`). |
| 9 | Live responses are richer than [DOC] | `meta`: `marginTableId`, `marginMode`, `isDelisted`, `collateralToken`; `perpDexs`: `subDeployers`, funding maps; `outcomeMeta`: top-level `questions`, `deployers`, `feeScale`, per-outcome `quoteToken`, `venue`. `allPerpMetas` example in [DOC] is outdated. | Decode leniently; model documented keys only. |
| 10 | Numeric types | [DOC] TypeScript types say `number` for candle OHLCV and asset-ctx fields; the wire carries **strings**. | `Fixed.UnmarshalJSON` accepts both. |
| 11 | >8 decimals | [LIVE] `premium":"0.0004493591"`, `dayNtlVlm":"5092112405.7967205048"`. Prices / sizes never exceed 8. | JSON decoding of `Fixed` truncates; volumes / OI are `decimal`. |
| 12 | Junk data on testnet | [LIVE] `allMids` contains `"@1120":"1844674407370.9553222656"`. | `allMids` values are parsed one by one; bad entries skipped. |
| 13 | HIP-4 status | One [DOC] page says "Testnet-only"; [LIVE] mainnet `outcomeMeta` returns 204 outcomes. | Treat as live on both networks. |
| 14 | l2Book cadence | [DOC]: "pushed on each block that is at least 0.5 since last push". [LIVE] (single observation): `fast:true` → 5 levels ≈ every 0.5 s; default 20 levels ≈ every 5 s. | Documented as observed; latency-sensitive users combine `bbo` + fast book. **Re-measure.** |
| 15 | `cloid` in fills | [DOC] `WsFill` has no `cloid`. | Not modelled until seen on testnet. |
| 16 | WS error frame | `{"channel":"error","data":"<text>"}` is **not documented**; [LIVE] e.g. "Already subscribed: …". | Logged + `OnServerError` hook. |
| 17 | `a = false` modify restriction | [DOC]: without always_place the new order "must be a non-trigger order, and must have TIF = ALO or a non-executable order with TIF = GTC" (tif then overridden to ALO). | Surfaced in `types.ModifyOptions`; verify on testnet. |

## 2. Endpoints

| | REST | WebSocket |
|---|---|---|
| mainnet | `https://api.hyperliquid.xyz` | `wss://api.hyperliquid.xyz/ws` |
| testnet | `https://api.hyperliquid-testnet.xyz` | `wss://api.hyperliquid-testnet.xyz/ws` |

`POST /info`, `POST /exchange`, `Content-Type: application/json`. [DOC]: the API
server replies to an action only after it is included in a committed L1 block.

## 3. Signing [PY]

```
connectionId = keccak256( msgpack(action) ++ nonce(8, BE)
                          ++ (0x00 | 0x01 ++ vault(20))
                          ++ [0x00 ++ expiresAfter(8, BE)] )        # only when set
phantom agent = { source: "a" (mainnet) | "b" (testnet), connectionId }
domain        = { name "Exchange", version "1", chainId 1337, verifyingContract 0x0 }
primaryType   = Agent(string source, bytes32 connectionId)
```

User-signed actions: domain `{HyperliquidSignTransaction, "1", chainId =
int(signatureChainId), 0x0}`, primary type `HyperliquidTransaction:<Name>`, the
action fields are the message (`hyperliquidChain` first). EIP-712 field lists:
see `internal/signing/sign_test.go` (usdSend, withdraw, approveAgent,
usdClassTransfer, spotSend, sendAsset, approveBuilderFee — all vector-tested).

[DOC] common signing mistakes: msgpack field order, trailing zeros, upper-case
addresses, trusting a local recover check. Symptom of a bad signature:
`"User or API Wallet 0x… does not exist."` naming an address that is not yours.

msgpack rules that matter ([PY] = msgpack-python): shortest integer form,
unsigned family for non-negative ints, `str` family for text, `fixarray` ≤ 15 →
`array16` (a batch of 16+ orders!), `fixmap` ≤ 15.

## 4. Nonces and API wallets [DOC]

- 100 highest nonces stored per signer; a new nonce must exceed the smallest of
  them and be unused; window `(T − 2 days, T + 1 day)`.
- Tracked per **signer**: one API wallet signing for the master, a sub-account
  and a vault shares one set. Recommended: one API wallet per trading process,
  an atomic counter fast-forwarded to the current ms.
- 1 unnamed + 3 named API wallets per account, +2 named per sub-account.
  `agentName` may carry `valid_until <timestamp>` (max 180 days). Never reuse an
  agent address: state is pruned on deregistration and old actions can replay.
- `noop` consumes a nonce — the recommended way to invalidate an in-flight order
  (send it with the same nonce).

## 5. Rate limits [DOC]

- IP: **1200 weight / minute**. Action: `1 + floor(batch_length / 40)`. Info: 2
  for `l2Book, allMids, clearinghouseState, orderStatus, spotClearinghouseState,
  exchangeStatus`; 60 for `userRole`; 20 otherwise; extra weight per 20 returned
  items for fills / funding / history requests, per 60 for `candleSnapshot`
  (**the amount per block is not documented** — the SDK charges 1).
- WS: 10 connections, 30 new connections / min, 1000 subscriptions, 10 unique
  users, 2000 messages / min, 100 inflight posts. Weight of WS posts: not
  documented (SDK charges the REST weight).
- Address-based (actions only, per user; sub-accounts are separate users):
  1 request per 1 USDC traded + 10 000 initial buffer; when exhausted 1 request
  / 10 s; cancels get `min(limit + 100000, limit × 2)`; a batch of n costs n; a
  stale `expiresAfter` costs 5×.
- Open orders: 1000 + 1 per 5M USDC volume (max 5000); at ≥ 1000 open orders
  reduce-only and trigger orders are rejected.
- Unified account / portfolio margin: **50k user actions per day**; standard
  mode has no such cap.
- Not documented: max batch size, HTTP status / body when limited, the error
  text of an exhausted address budget.

## 6. Prices, sizes, asset ids [DOC]

- Price: ≤ 5 significant figures AND ≤ `MAX_DECIMALS − szDecimals` decimals
  (perps 6, spot 8); integer prices always valid (`123456` ok, `12345.6` not).
- Size: `szDecimals` decimals. Trailing zeros must be removed before signing.
- Min order value: only via error text — `$10` perps, `10 {quote}` spot.
- Asset ids: perps = index in `meta.universe`; spot = `10000 + index` in
  `spotMeta.universe` (≠ token index: HYPE is token 150, pair `@107` on mainnet);
  HIP-3 = `100000 + perp_dex_index × 10000 + index` (`perpDexs[0]` is `null`);
  outcomes = `100_000_000 + 10 × outcome + side`, coin `#<enc>`, token `+<enc>`.
- [LIVE] ids differ per network: BTC is asset 0 on mainnet, 3 on testnet
  (234 vs 212 perps; 11 vs 266 perp dexs).
- Coin names: perps plain and **case-sensitive** (`kPEPE`); spot `PURR/USDC` or
  `@<index>`; HIP-3 `<dex>:<COIN>`. Account-wide answers (`frontendOpenOrders`,
  `userFills`, `orderUpdates`, `allMids` of dex "") mix all sections.

## 7. Actions used by v1.0 [DOC] (key order = signing order)

```
order          {"type":"order","orders":[{"a","b","p","s","r","t"[,"c"]}],"grouping":"na"[,"builder":{"b","f"}]}
               t = {"limit":{"tif":"Alo"|"Ioc"|"Gtc"}} | {"trigger":{"isMarket","triggerPx","tpsl":"tp"|"sl"}}
cancel         {"type":"cancel","cancels":[{"a","o"}][,"f":true]}
cancelByCloid  {"type":"cancelByCloid","cancels":[{"asset","cloid"}][,"f":true]}
modify         {"type":"modify","oid":<n|cloid>,"order":{…}[,"a":true]}           (docs-only)
batchModify    {"type":"batchModify","modifies":[{"oid","order"}][,"a":true]}
scheduleCancel {"type":"scheduleCancel"[,"time":ms]}     ≥ 5 s ahead, ≤ 10 triggers/day
updateLeverage {"type":"updateLeverage","asset","isCross","leverage"}
updateIsolatedMargin {"type":"updateIsolatedMargin","asset","isBuy","ntli"}   ntli: 6-decimals int
noop           {"type":"noop"}
```

Responses: `{"status":"ok","response":{"type":…,"data":{"statuses":[…]}}}` with
`{"resting":{oid}}`, `{"filled":{totalSz,avgPx,oid}}`, `{"error":text}`,
`"success"` (cancel); `{"status":"ok","response":{"type":"default"}}`; whole
action rejected: `{"status":"err","response":"<text>"}`. Pre-validation errors
reject the whole batch with ONE error. Error texts: the "Error responses" page
(mirrored verbatim in `internal/hlerr/errors_test.go`).

## 8. WebSocket [DOC]

`{"method":"subscribe"|"unsubscribe","subscription":{…}}`; pushes are
`{"channel","data"}` (`userEvents` arrives on channel `"user"`, spot asset ctx on
`"activeSpotAssetCtx"`). Post: `{"method":"post","id":N,"request":{"type":"info"|
"action","payload":{…}}}` → `{"channel":"post","data":{"id","response":{"type":
"info"|"action"|"error","payload"}}}` (info payload is wrapped as `{"type",
"data"}`; error payload is a string). Heartbeat: the server closes a connection
it has not written to for 60 s; `{"method":"ping"}` → `{"channel":"pong"}`.
`isSnapshot:true` marks the replay sent right after (re)subscribing to
time-series user streams — that is how missed data is recovered.

Book: REST and WS `l2Book` are **full snapshots**, ≤ 20 levels per side (`fast`:
5), optional `nSigFigs` 2..5 and `mantissa` 1|2|5 (only with nSigFigs 5). No
deltas, no sequence numbers, no checksum. Diffs exist only in node output
(non-validating node) — out of scope.
