/*
FILE: internal/domain/info.go

DESCRIPTION:
Unified info requests of the common layer (POST /info or WS post "info").
Request bodies are written by hand in the exact documented shape; responses
decode straight into the public types of package types.

REQUESTS (official info-endpoint pages):
  l2Book              {"type":"l2Book","coin":C[,"nSigFigs":N[,"mantissa":M]]}
  allMids             {"type":"allMids","dex":D}
  candleSnapshot      {"type":"candleSnapshot","req":{"coin":C,"interval":I,"startTime":S,"endTime":E}}
  frontendOpenOrders  {"type":"frontendOpenOrders","user":U,"dex":D}
  orderStatus         {"type":"orderStatus","user":U,"oid":<number|cloid>}
  userFills           {"type":"userFills","user":U[,"aggregateByTime":true]}
  userFillsByTime     {"type":"userFillsByTime","user":U,"startTime":S[,"endTime":E][,"aggregateByTime":true]}
  userRateLimit       {"type":"userRateLimit","user":U}
  clearinghouseState  {"type":"clearinghouseState","user":U,"dex":D}
  meta                {"type":"meta","dex":D}
  metaAndAssetCtxs    {"type":"metaAndAssetCtxs","dex":D}

"dex" semantics: "" is the first (validator-operated) perp dex. The docs note
that "spot mids / open orders are only included with the first perp dex", so a
section filters the shared answers by its own registry.

LIMITS (docs): l2Book — at most 20 levels per side; candleSnapshot — the 5000
most recent candles; userFills — at most 2000; userFillsByTime — 2000 per
response, only the 10000 most recent fills are available.
*/

package domain

import (
	"context"
	"strconv"

	"github.com/tonymontanov/go-hyperliquid/internal/codec"
	"github.com/tonymontanov/go-hyperliquid/internal/engine"
	"github.com/tonymontanov/go-hyperliquid/internal/hlerr"
	"github.com/tonymontanov/go-hyperliquid/types"
)

// requireUser validates the account address used as "user".
func (p *Profile) requireUser(operation string, user string) error {
	if user == "" {
		return p.invalid(operation, "account address is not configured (Config.AccountAddress / VaultAddress)")
	}
	if !safeText(user) {
		return p.invalid(operation, "malformed account address")
	}
	return nil
}

// requireCoin validates a coin name for embedding into a request body.
func (p *Profile) requireCoin(operation string, coin string) error {
	if coin == "" {
		return p.invalid(operation, "coin is empty")
	}
	if !safeText(coin) {
		return p.invalid(operation, "malformed coin name")
	}
	return nil
}

// L2BookOptions — aggregation of an l2Book request. Zero value = full precision.
type L2BookOptions struct {
	// NSigFigs — aggregate levels to 2..5 significant figures; 0 → none.
	NSigFigs int
	// Mantissa — 1, 2 or 5; only allowed when NSigFigs is 5; 0 → none.
	Mantissa int
}

// appendL2BookParams appends the optional aggregation fields.
func appendL2BookParams(b []byte, options L2BookOptions) []byte {
	if options.NSigFigs != 0 {
		b = append(b, `,"nSigFigs":`...)
		b = strconv.AppendInt(b, int64(options.NSigFigs), 10)
		if options.Mantissa != 0 {
			b = append(b, `,"mantissa":`...)
			b = strconv.AppendInt(b, int64(options.Mantissa), 10)
		}
	}
	return b
}

// validateL2BookOptions checks the documented value ranges.
func (p *Profile) validateL2BookOptions(operation string, options L2BookOptions) error {
	if options.NSigFigs != 0 && (options.NSigFigs < 2 || options.NSigFigs > 5) {
		return p.invalid(operation, "nSigFigs must be 2..5")
	}
	if options.Mantissa != 0 {
		if options.NSigFigs != 5 {
			return p.invalid(operation, "mantissa is only allowed when nSigFigs is 5")
		}
		if options.Mantissa != 1 && options.Mantissa != 2 && options.Mantissa != 5 {
			return p.invalid(operation, "mantissa must be 1, 2 or 5")
		}
	}
	return nil
}

// L2Book fetches a book snapshot (at most 20 levels per side).
func L2Book(ctx context.Context, e *engine.Engine, p *Profile, coin string, options L2BookOptions, transport engine.Transport) (types.OrderBookSnapshot, error) {
	const operation string = "L2Book"
	var out types.OrderBookSnapshot
	var err error = p.requireCoin(operation, coin)
	if err != nil {
		return out, err
	}
	err = p.validateL2BookOptions(operation, options)
	if err != nil {
		return out, err
	}
	var body []byte = make([]byte, 0, 96)
	body = append(body, `{"type":"l2Book","coin":`...)
	body = appendJSONText(body, coin)
	body = appendL2BookParams(body, options)
	body = append(body, '}')
	err = e.Info(ctx, "l2Book", body, &out, engine.InfoOptions{Transport: transport, Category: engine.CategoryMarket}, nil)
	return out, err
}

/*
AllMids fetches mid prices of the profile's dex, keyed by coin. accept selects
the coins to keep (nil keeps all).

The answer of the first perp dex mixes every section (perp names, "@N" spot
pairs, "#N" outcomes), and values are parsed ONE BY ONE: a single malformed or
out-of-range entry must not fail the request (observed on testnet on
2026-09-22: a junk spot pair with mid "1844674407370.9553222656"). Entries
that do not parse are skipped.
*/
func AllMids(ctx context.Context, e *engine.Engine, p *Profile, accept func(coin string) bool, transport engine.Transport) (map[string]types.Fixed, error) {
	var body []byte = make([]byte, 0, 64)
	body = append(body, `{"type":"allMids","dex":`...)
	body = appendJSONText(body, p.Dex)
	body = append(body, '}')
	var raw map[string]string
	var err error = e.Info(ctx, "allMids", body, &raw, engine.InfoOptions{Transport: transport, Category: engine.CategoryMarket}, nil)
	if err != nil {
		return nil, err
	}
	var out map[string]types.Fixed = make(map[string]types.Fixed, len(raw))
	var coin string
	var text string
	for coin, text = range raw {
		if accept != nil && !accept(coin) {
			continue
		}
		var mid types.Fixed
		var parseErr error
		mid, parseErr = types.ParseFixedLenient(text)
		if parseErr != nil {
			continue
		}
		out[coin] = mid
	}
	return out, nil
}

// CandleSnapshot fetches candles of [startMs, endMs].
func CandleSnapshot(ctx context.Context, e *engine.Engine, p *Profile, coin string, interval string, startMs int64, endMs int64) ([]types.Candle, error) {
	const operation string = "CandleSnapshot"
	var err error = p.requireCoin(operation, coin)
	if err != nil {
		return nil, err
	}
	if interval == "" || !safeText(interval) {
		return nil, p.invalid(operation, "invalid interval")
	}
	if endMs < startMs {
		return nil, p.invalid(operation, "endMs is before startMs")
	}
	var body []byte = make([]byte, 0, 160)
	body = append(body, `{"type":"candleSnapshot","req":{"coin":`...)
	body = appendJSONText(body, coin)
	body = append(body, `,"interval":`...)
	body = appendJSONText(body, interval)
	body = append(body, `,"startTime":`...)
	body = strconv.AppendInt(body, startMs, 10)
	body = append(body, `,"endTime":`...)
	body = strconv.AppendInt(body, endMs, 10)
	body = append(body, '}', '}')
	var out []types.Candle
	err = e.Info(ctx, "candleSnapshot", body, &out, engine.InfoOptions{Category: engine.CategoryMarket}, func() int { return len(out) })
	return out, err
}

// userDexBody builds {"type":T,"user":U,"dex":D}.
func userDexBody(infoType string, user string, dex string) []byte {
	var body []byte = make([]byte, 0, 128)
	body = append(body, `{"type":"`...)
	body = append(body, infoType...)
	body = append(body, `","user":`...)
	body = appendJSONText(body, user)
	body = append(body, `,"dex":`...)
	body = appendJSONText(body, dex)
	return append(body, '}')
}

// OpenOrders fetches the resting orders of user on the profile's dex
// ("frontendOpenOrders" — a superset of "openOrders" that carries origSz,
// cloid, reduceOnly, tif and trigger fields).
func OpenOrders(ctx context.Context, e *engine.Engine, p *Profile, user string, transport engine.Transport) ([]types.OpenOrder, error) {
	var err error = p.requireUser("OpenOrders", user)
	if err != nil {
		return nil, err
	}
	var out []types.OpenOrder
	err = e.Info(ctx, "frontendOpenOrders", userDexBody("frontendOpenOrders", user, p.Dex), &out, engine.InfoOptions{Transport: transport}, nil)
	return out, err
}

// orderStatusResponse — raw shape of the orderStatus answer.
type orderStatusResponse struct {
	Status string `json:"status"`
	Order  struct {
		Order             types.OpenOrder `json:"order"`
		Status            string          `json:"status"`
		StatusTimestampMs int64           `json:"statusTimestamp"`
	} `json:"order"`
}

// OrderStatus queries one order by oid, or by cloid when it is set.
func OrderStatus(ctx context.Context, e *engine.Engine, p *Profile, user string, oid uint64, cloid types.Cloid, transport engine.Transport) (types.OrderState, error) {
	const operation string = "OrderStatus"
	var out types.OrderState
	var err error = p.requireUser(operation, user)
	if err != nil {
		return out, err
	}
	if oid == 0 && !cloid.IsSet() {
		return out, p.invalid(operation, "neither oid nor cloid is set")
	}
	var body []byte = make([]byte, 0, 128)
	body = append(body, `{"type":"orderStatus","user":`...)
	body = appendJSONText(body, user)
	body = append(body, `,"oid":`...)
	if cloid.IsSet() {
		body = append(body, '"')
		body = cloid.AppendHex(body)
		body = append(body, '"')
	} else {
		body = strconv.AppendUint(body, oid, 10)
	}
	body = append(body, '}')

	var raw orderStatusResponse
	err = e.Info(ctx, "orderStatus", body, &raw, engine.InfoOptions{Transport: transport}, nil)
	if err != nil {
		return out, err
	}
	if raw.Status != "order" {
		// {"status":"unknownOid"}
		return out, nil
	}
	out.Found = true
	out.Order = raw.Order.Order
	out.Status = raw.Order.Status
	out.StatusTimestampMs = raw.Order.StatusTimestampMs
	return out, nil
}

// UserFills fetches the most recent fills (at most 2000).
func UserFills(ctx context.Context, e *engine.Engine, p *Profile, user string, aggregateByTime bool) ([]types.Fill, error) {
	var err error = p.requireUser("UserFills", user)
	if err != nil {
		return nil, err
	}
	var body []byte = make([]byte, 0, 128)
	body = append(body, `{"type":"userFills","user":`...)
	body = appendJSONText(body, user)
	if aggregateByTime {
		body = append(body, `,"aggregateByTime":true`...)
	}
	body = append(body, '}')
	var out []types.Fill
	err = e.Info(ctx, "userFills", body, &out, engine.InfoOptions{}, func() int { return len(out) })
	return out, err
}

// UserFillsByTime fetches fills of [startMs, endMs]; endMs == 0 → now.
func UserFillsByTime(ctx context.Context, e *engine.Engine, p *Profile, user string, startMs int64, endMs int64, aggregateByTime bool) ([]types.Fill, error) {
	var err error = p.requireUser("UserFillsByTime", user)
	if err != nil {
		return nil, err
	}
	var body []byte = make([]byte, 0, 160)
	body = append(body, `{"type":"userFillsByTime","user":`...)
	body = appendJSONText(body, user)
	body = append(body, `,"startTime":`...)
	body = strconv.AppendInt(body, startMs, 10)
	if endMs != 0 {
		body = append(body, `,"endTime":`...)
		body = strconv.AppendInt(body, endMs, 10)
	}
	if aggregateByTime {
		body = append(body, `,"aggregateByTime":true`...)
	}
	body = append(body, '}')
	var out []types.Fill
	err = e.Info(ctx, "userFillsByTime", body, &out, engine.InfoOptions{}, func() int { return len(out) })
	return out, err
}

// UserRateLimit fetches the address-based rate-limit state and syncs the
// engine's local budget mirror with it.
func UserRateLimit(ctx context.Context, e *engine.Engine, p *Profile, user string) (types.UserRateLimit, error) {
	var out types.UserRateLimit
	var err error = p.requireUser("UserRateLimit", user)
	if err != nil {
		return out, err
	}
	var body []byte = make([]byte, 0, 96)
	body = append(body, `{"type":"userRateLimit","user":`...)
	body = appendJSONText(body, user)
	body = append(body, '}')
	err = e.Info(ctx, "userRateLimit", body, &out, engine.InfoOptions{}, nil)
	if err != nil {
		return out, err
	}
	e.Budget().Sync(out.NRequestsUsed, out.NRequestsCap)
	return out, nil
}

// ClearinghouseState fetches the margin account state of the profile's dex.
func ClearinghouseState(ctx context.Context, e *engine.Engine, p *Profile, user string, transport engine.Transport) (types.ClearinghouseState, error) {
	var out types.ClearinghouseState
	var err error = p.requireUser("ClearinghouseState", user)
	if err != nil {
		return out, err
	}
	err = e.Info(ctx, "clearinghouseState", userDexBody("clearinghouseState", user, p.Dex), &out, engine.InfoOptions{Transport: transport}, nil)
	return out, err
}

// PerpUniverseEntry — one asset of a perp dex "meta" answer. Only documented
// keys are modelled; the live API sends more (marginTableId, marginMode, ...)
// and unknown keys are ignored by the decoder.
type PerpUniverseEntry struct {
	Name         string `json:"name"`
	SzDecimals   int    `json:"szDecimals"`
	MaxLeverage  int    `json:"maxLeverage"`
	OnlyIsolated bool   `json:"onlyIsolated"`
	IsDelisted   bool   `json:"isDelisted"`
}

// PerpMeta — "meta" answer of a perp dex.
type PerpMeta struct {
	Universe []PerpUniverseEntry `json:"universe"`
	// CollateralToken — index of the collateral token in spotMeta.tokens
	// (0 = USDC for the first perp dex).
	CollateralToken int `json:"collateralToken"`
}

// dexBody builds {"type":T,"dex":D}.
func dexBody(infoType string, dex string) []byte {
	var body []byte = make([]byte, 0, 64)
	body = append(body, `{"type":"`...)
	body = append(body, infoType...)
	body = append(body, `","dex":`...)
	body = appendJSONText(body, dex)
	return append(body, '}')
}

// PerpMetaOf fetches the "meta" of the profile's perp dex.
func PerpMetaOf(ctx context.Context, e *engine.Engine, p *Profile) (PerpMeta, error) {
	var out PerpMeta
	var err error = e.Info(ctx, "meta", dexBody("meta", p.Dex), &out, engine.InfoOptions{Category: engine.CategoryMarket}, nil)
	return out, err
}

// PerpMetaAndAssetCtxs fetches [meta, assetCtxs] of the profile's perp dex.
// ctxs[i] belongs to meta.Universe[i].
func PerpMetaAndAssetCtxs(ctx context.Context, e *engine.Engine, p *Profile) (PerpMeta, []types.PerpAssetCtx, error) {
	var meta PerpMeta
	var ctxs []types.PerpAssetCtx
	var pair []codec.RawMessage
	var err error = e.Info(ctx, "metaAndAssetCtxs", dexBody("metaAndAssetCtxs", p.Dex), &pair, engine.InfoOptions{Category: engine.CategoryMarket}, nil)
	if err != nil {
		return meta, nil, err
	}
	if len(pair) != 2 {
		return meta, nil, hlerr.New(hlerr.ErrorKindUnknown, p.Section+".PerpMetaAndAssetCtxs: expected a [meta, ctxs] pair", nil)
	}
	err = codec.Unmarshal(pair[0], &meta)
	if err != nil {
		return meta, nil, hlerr.New(hlerr.ErrorKindUnknown, p.Section+".PerpMetaAndAssetCtxs: parse meta", err)
	}
	err = codec.Unmarshal(pair[1], &ctxs)
	if err != nil {
		return meta, nil, hlerr.New(hlerr.ErrorKindUnknown, p.Section+".PerpMetaAndAssetCtxs: parse asset contexts", err)
	}
	return meta, ctxs, nil
}
