/*
FILE: perpetuals/market.go

DESCRIPTION:
Market data of the Perpetuals section: asset metadata (ids, precision), book
snapshots, mids, candles, asset contexts (mark / oracle / funding), and the
price helpers every order path needs.

PRICES AND TICKS:
Hyperliquid has NO static tick size. A price may carry 5 significant figures
and at most 6 - szDecimals decimals, so the effective tick depends on the
price level and changes when the price crosses a power of ten. Use
AssetInfo.Precision (NormalizePrice / TickSize) instead of caching a tick.

ORDER BOOK:
The public book is snapshot-only, at most 20 levels per side — see
types.OrderBookSnapshot.
*/

package perpetuals

import (
	"context"

	"github.com/tonymontanov/go-hyperliquid/internal/assets"
	"github.com/tonymontanov/go-hyperliquid/internal/domain"
	"github.com/tonymontanov/go-hyperliquid/internal/engine"
	"github.com/tonymontanov/go-hyperliquid/types"
)

// MaxOrderBookLevels — levels per side of a book snapshot.
const MaxOrderBookLevels int = 20

// MarketDataClient — market data sub-client.
type MarketDataClient struct {
	c *Client
}

// GetAssets returns every asset of the section (loads metadata on first use).
// The slice is shared and must not be modified.
func (m *MarketDataClient) GetAssets(ctx context.Context) ([]types.AssetInfo, error) {
	var snapshot *assets.Snapshot
	var err error
	snapshot, err = m.c.registry.Get(ctx)
	if err != nil {
		return nil, err
	}
	return snapshot.All(), nil
}

// GetAssetInfo returns the asset id and precision of coin.
func (m *MarketDataClient) GetAssetInfo(ctx context.Context, coin string) (types.AssetInfo, error) {
	var info *types.AssetInfo
	var err error
	info, err = m.c.prof().Resolve(ctx, "GetAssetInfo", coin)
	if err != nil {
		return types.AssetInfo{}, err
	}
	return *info, nil
}

// OrderBookOptions — optional aggregation of a book request.
type OrderBookOptions = domain.L2BookOptions

// GetOrderBook returns a book snapshot of coin (at most 20 levels per side).
func (m *MarketDataClient) GetOrderBook(ctx context.Context, coin string, options OrderBookOptions) (types.OrderBookSnapshot, error) {
	var err error
	_, err = m.c.prof().Resolve(ctx, "GetOrderBook", coin)
	if err != nil {
		return types.OrderBookSnapshot{}, err
	}
	return domain.L2Book(ctx, m.c.engine(), m.c.prof(), coin, options, engine.TransportREST)
}

// GetAllMids returns mid prices of every perpetual of the section. The answer
// of the first perp dex also carries spot mids; they are filtered out.
func (m *MarketDataClient) GetAllMids(ctx context.Context) (map[string]types.Fixed, error) {
	var err error = m.c.ensureAssets(ctx)
	if err != nil {
		return nil, err
	}
	return domain.AllMids(ctx, m.c.engine(), m.c.prof(), m.c.owns, engine.TransportREST)
}

// GetMid returns the mid price of coin.
func (m *MarketDataClient) GetMid(ctx context.Context, coin string) (types.Fixed, error) {
	const operation string = "GetMid"
	var err error
	_, err = m.c.prof().Resolve(ctx, operation, coin)
	if err != nil {
		return 0, err
	}
	var all map[string]types.Fixed
	all, err = domain.AllMids(ctx, m.c.engine(), m.c.prof(), func(candidate string) bool { return candidate == coin }, engine.TransportREST)
	if err != nil {
		return 0, err
	}
	var mid types.Fixed
	var ok bool
	mid, ok = all[coin]
	if !ok || mid <= 0 {
		return 0, m.c.prof().Invalid(operation, coin+": the exchange reports no mid price")
	}
	return mid, nil
}

/*
SlippagePrice returns an aggressive limit price for an IOC "market" order:
mid * (1 + slippage) for buys, mid * (1 - slippage) for sells, snapped to the
valid price grid (RoundUp for buys, RoundDown for sells — never less
aggressive than requested).
*/
func (m *MarketDataClient) SlippagePrice(ctx context.Context, coin string, isBuy bool, slippage float64) (types.Fixed, error) {
	const operation string = "SlippagePrice"
	if slippage < 0 || slippage >= 1 {
		return 0, m.c.prof().Invalid(operation, "slippage must be in [0, 1)")
	}
	var info *types.AssetInfo
	var err error
	info, err = m.c.prof().Resolve(ctx, operation, coin)
	if err != nil {
		return 0, err
	}
	var mid types.Fixed
	mid, err = m.GetMid(ctx, coin)
	if err != nil {
		return 0, err
	}
	var factor float64 = 1 - slippage
	var mode types.RoundingMode = types.RoundDown
	if isBuy {
		factor = 1 + slippage
		mode = types.RoundUp
	}
	var raw types.Fixed
	raw, err = types.FixedFromFloat64(mid.Float64() * factor)
	if err != nil {
		return 0, m.c.prof().Invalid(operation, coin+": slippage price is out of range")
	}
	return info.Precision.NormalizePrice(raw, mode), nil
}

// GetCandles returns candles of [startMs, endMs] (the 5000 most recent are
// available). interval — one of the types.Interval* constants.
func (m *MarketDataClient) GetCandles(ctx context.Context, coin string, interval string, startMs int64, endMs int64) ([]types.Candle, error) {
	var err error
	_, err = m.c.prof().Resolve(ctx, "GetCandles", coin)
	if err != nil {
		return nil, err
	}
	return domain.CandleSnapshot(ctx, m.c.engine(), m.c.prof(), coin, interval, startMs, endMs)
}

// AssetContext — live context of one perpetual, paired with its coin.
type AssetContext struct {
	Coin string
	Ctx  types.PerpAssetCtx
}

// GetAssetContexts returns mark / oracle / mid prices, funding and open
// interest of every perpetual (info "metaAndAssetCtxs").
func (m *MarketDataClient) GetAssetContexts(ctx context.Context) ([]AssetContext, error) {
	var meta domain.PerpMeta
	var ctxs []types.PerpAssetCtx
	var err error
	meta, ctxs, err = domain.PerpMetaAndAssetCtxs(ctx, m.c.engine(), m.c.prof())
	if err != nil {
		return nil, err
	}
	var count int = len(meta.Universe)
	if len(ctxs) < count {
		count = len(ctxs)
	}
	var out []AssetContext = make([]AssetContext, count)
	var i int
	for i = 0; i < count; i++ {
		out[i] = AssetContext{Coin: meta.Universe[i].Name, Ctx: ctxs[i]}
	}
	return out, nil
}
