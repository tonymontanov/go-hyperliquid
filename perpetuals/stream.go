/*
FILE: perpetuals/stream.go

DESCRIPTION:
WebSocket streams of the Perpetuals section. Each Watch* is the unified stream
of the common layer (internal/domain) with the section profile; the section
adds only coin validation and filtering of account-wide pushes.

LIFETIME AND CONTRACT:
  - Watch* returns after the subscription is registered; pushes arrive on the
    client's shared stream connection until ctx is cancelled (that
    unsubscribes this stream only) or the root Client is closed;
  - handlers run on the connection's read goroutine: they must not block, and
    the value they receive is REUSED for the next push — copy what you keep;
  - after a reconnect subscriptions are replayed automatically; onReset (where
    offered) fires first, so consumers can drop or re-seed local state;
  - a malformed push is logged and dropped, errHandler is not called for it.

PRIVATE STREAMS NEED NO LOGIN:
orderUpdates / userFills only name the account address. They carry events of
EVERY section of the account, so the section filters them by its registry.
*/

package perpetuals

import (
	"context"

	"github.com/tonymontanov/go-hyperliquid/internal/domain"
	"github.com/tonymontanov/go-hyperliquid/types"
)

// StreamClient — WebSocket streams sub-client.
type StreamClient struct {
	c *Client
}

// deps returns the shared stream connection and the logger.
func (s *StreamClient) deps() domain.StreamDeps {
	return domain.StreamDeps{Conn: s.c.parent.StreamConn(), Logger: s.c.logger()}
}

// check validates that coin belongs to the section before subscribing.
func (s *StreamClient) check(ctx context.Context, operation string, coin string, errHandler func(error)) error {
	var err error
	_, err = s.c.prof().Resolve(ctx, operation, coin)
	if err != nil && errHandler != nil {
		errHandler(err)
	}
	return err
}

// IsConnected reports whether the shared stream connection has a live socket
// right now. Consumers that replay state on every (re)connect through onReset
// use it to decide whether the FIRST connect is still ahead (onReset will
// fire) or already happened (they must seed state themselves).
func (s *StreamClient) IsConnected() bool {
	return s.c.parent.StreamConn().IsConnected()
}

// OrderbookStreamOptions — options of WatchOrderbook.
type OrderbookStreamOptions = domain.L2BookStreamOptions

// WatchOrderbook streams book snapshots of coin: 20 levels per side, or 5
// levels at the fast cadence with options.Fast. onReset may be nil.
func (s *StreamClient) WatchOrderbook(ctx context.Context, coin string, options OrderbookStreamOptions, handler func(*types.OrderBookSnapshot), onReset func(), errHandler func(error)) error {
	var err error = s.check(ctx, "WatchOrderbook", coin, errHandler)
	if err != nil {
		return err
	}
	return domain.WatchL2Book(ctx, s.deps(), s.c.prof(), coin, options, handler, onReset, errHandler)
}

// WatchBBO streams the best bid / offer of coin — the lowest-latency public
// price feed ("sent only if the bbo changes on a block").
func (s *StreamClient) WatchBBO(ctx context.Context, coin string, handler func(*types.BBO), errHandler func(error)) error {
	var err error = s.check(ctx, "WatchBBO", coin, errHandler)
	if err != nil {
		return err
	}
	return domain.WatchBBO(ctx, s.deps(), s.c.prof(), coin, handler, errHandler)
}

// WatchTrades streams public trades of coin.
func (s *StreamClient) WatchTrades(ctx context.Context, coin string, handler func([]types.Trade), errHandler func(error)) error {
	var err error = s.check(ctx, "WatchTrades", coin, errHandler)
	if err != nil {
		return err
	}
	return domain.WatchTrades(ctx, s.deps(), s.c.prof(), coin, handler, errHandler)
}

// WatchCandles streams the current candle of coin / interval.
func (s *StreamClient) WatchCandles(ctx context.Context, coin string, interval string, handler func(*types.Candle), errHandler func(error)) error {
	var err error = s.check(ctx, "WatchCandles", coin, errHandler)
	if err != nil {
		return err
	}
	return domain.WatchCandles(ctx, s.deps(), s.c.prof(), coin, interval, handler, errHandler)
}

// WatchAssetCtx streams mark / oracle / mid prices, funding and open interest
// of coin.
func (s *StreamClient) WatchAssetCtx(ctx context.Context, coin string, handler func(*types.ActivePerpAssetCtx), errHandler func(error)) error {
	var err error = s.check(ctx, "WatchAssetCtx", coin, errHandler)
	if err != nil {
		return err
	}
	return domain.WatchPerpAssetCtx(ctx, s.deps(), s.c.prof(), coin, handler, errHandler)
}

// preload loads exchange metadata before a user stream starts, so that the
// per-push section filter never performs I/O on the read goroutine.
func (s *StreamClient) preload(ctx context.Context, errHandler func(error)) error {
	var err error = s.c.ensureAssets(ctx)
	if err != nil && errHandler != nil {
		errHandler(err)
	}
	return err
}

// WatchOrderUpdates streams order status changes of the section's orders.
// onReset fires on every (re)connect — re-seed from Trading().GetOpenOrders.
func (s *StreamClient) WatchOrderUpdates(ctx context.Context, handler func([]types.OrderUpdate), onReset func(), errHandler func(error)) error {
	var err error = s.preload(ctx, errHandler)
	if err != nil {
		return err
	}
	var own []types.OrderUpdate
	return domain.WatchOrderUpdates(ctx, s.deps(), s.c.prof(), s.c.user(), func(updates []types.OrderUpdate) {
		own = own[:0]
		var i int
		for i = 0; i < len(updates); i++ {
			if s.c.owns(updates[i].Order.Coin) {
				own = append(own, updates[i])
			}
		}
		if len(own) > 0 {
			handler(own)
		}
	}, onReset, errHandler)
}

// WatchAccountState streams ABSOLUTE snapshots of the perpetuals margin account
// (positions, margin summaries, withdrawable). Every push is the full current
// state, never a delta. onReset fires on every (re)connect.
func (s *StreamClient) WatchAccountState(ctx context.Context, handler func(*types.AccountStateUpdate), onReset func(), errHandler func(error)) error {
	return domain.WatchClearinghouseState(ctx, s.deps(), s.c.prof(), s.c.user(), handler, onReset, errHandler)
}

// WatchUserFills streams executions of the section. The first push after every
// (re)subscribe is a snapshot of recent fills (IsSnapshot == true).
func (s *StreamClient) WatchUserFills(ctx context.Context, aggregateByTime bool, handler func(*types.UserFills), onReset func(), errHandler func(error)) error {
	var err error = s.preload(ctx, errHandler)
	if err != nil {
		return err
	}
	var own types.UserFills
	return domain.WatchUserFills(ctx, s.deps(), s.c.prof(), s.c.user(), aggregateByTime, func(fills *types.UserFills) {
		own.IsSnapshot = fills.IsSnapshot
		own.User = fills.User
		own.Fills = own.Fills[:0]
		var i int
		for i = 0; i < len(fills.Fills); i++ {
			if s.c.owns(fills.Fills[i].Coin) {
				own.Fills = append(own.Fills, fills.Fills[i])
			}
		}
		if len(own.Fills) > 0 || own.IsSnapshot {
			handler(&own)
		}
	}, onReset, errHandler)
}
