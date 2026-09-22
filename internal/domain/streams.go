/*
FILE: internal/domain/streams.go

DESCRIPTION:
Unified WebSocket streams of the common layer. One generic helper (watch)
registers a subscription on the shared stream connection and ties its lifetime
to the caller's ctx; thin typed functions define the subscription JSON, the
route key and the payload type.

SUBSCRIPTIONS (official websocket/subscriptions page):
  l2Book          {"type":"l2Book","coin":C[,"nSigFigs":N][,"mantissa":M][,"fast":true]}
  bbo             {"type":"bbo","coin":C}
  trades          {"type":"trades","coin":C}
  candle          {"type":"candle","coin":C,"interval":I}
  activeAssetCtx  {"type":"activeAssetCtx","coin":C}
  orderUpdates    {"type":"orderUpdates","user":U}
  userFills       {"type":"userFills","user":U[,"aggregateByTime":true]}
  clearinghouseState {"type":"clearinghouseState","user":U,"dex":D}

SHARED FEEDS:
Several Watch* calls may target the same exchange feed (two consumers of the
trades of one coin, order updates feeding both an order ledger and an order
book overlay). The connection subscribes the exchange once and delivers every
push to all of them; each call decodes into its own value. Two l2Book streams
of one coin with DIFFERENT aggregation options are rejected (the pushes are
indistinguishable on the wire).

LIFETIME:
The connection belongs to the root Client. Cancelling ctx unsubscribes THIS
subscription only. On EVERY connect of the socket — the first one included,
when the subscription was registered before it — every subscription is
replayed by the connection; onReset is invoked first so stateful consumers can
drop state. A subscription registered on an already live socket gets no
onReset until the next reconnect.

HANDLER CONTRACT (hot path):
  - handlers run sequentially on the connection's read goroutine — they must
    not block;
  - the value passed to a handler is REUSED for the next push of the same
    subscription (slices keep their capacity, so steady-state decoding of a
    book does not allocate level storage). Copy what must outlive the call.

ERROR POLICY (same as the sibling SDKs):
A push that fails to decode is logged and dropped; errHandler is NOT called
for it (one malformed frame must not tear a stream down). errHandler receives
only failures of the subscription itself.

l2Book "fast" (docs: "5 levels if fast, 20 levels if slow"): observed live on
2026-09-22 — fast pushes arrive about every 0.5 s, the default 20-level feed
noticeably slower. Latency-sensitive consumers should combine bbo + fast book.
*/

package domain

import (
	"context"
	"strconv"

	"github.com/tonymontanov/go-hyperliquid/internal/codec"
	"github.com/tonymontanov/go-hyperliquid/internal/hllog"
	"github.com/tonymontanov/go-hyperliquid/internal/ws"
	"github.com/tonymontanov/go-hyperliquid/types"
)

// StreamDeps — what a stream needs from the client.
type StreamDeps struct {
	Conn   *ws.Conn
	Logger hllog.Logger
}

// watch registers a subscription and unsubscribes it when ctx is done.
func watch(ctx context.Context, deps StreamDeps, key string, payload []byte, handler func(data []byte), onReset func(), errHandler func(error)) error {
	var subscription *ws.Subscription = &ws.Subscription{Key: key, Payload: payload, Handler: handler, Reset: onReset}
	var err error = deps.Conn.Subscribe(subscription)
	if err != nil {
		if errHandler != nil {
			errHandler(err)
		}
		return err
	}
	go func() {
		<-ctx.Done()
		// Removes THIS consumer only; the exchange is unsubscribed when the
		// last consumer of the key leaves. ErrConnClosed after Client.Close is
		// expected and ignored.
		_ = deps.Conn.Unsubscribe(subscription)
	}()
	return nil
}

// dropped logs a push that failed to decode.
func dropped(deps StreamDeps, channel string, err error) {
	deps.Logger.Warn("stream: dropped a malformed push", hllog.Str("channel", channel), hllog.Err(err))
}

// L2BookStreamOptions — options of the l2Book subscription.
type L2BookStreamOptions struct {
	L2BookOptions
	// Fast — 5 levels per side at the fast cadence instead of 20 levels.
	Fast bool
}

// WatchL2Book streams book snapshots of coin. One l2Book subscription per coin
// per client: pushes of different aggregations are indistinguishable.
func WatchL2Book(ctx context.Context, deps StreamDeps, p *Profile, coin string, options L2BookStreamOptions, handler func(*types.OrderBookSnapshot), onReset func(), errHandler func(error)) error {
	const operation string = "WatchL2Book"
	var err error = p.requireCoin(operation, coin)
	if err == nil {
		err = p.validateL2BookOptions(operation, options.L2BookOptions)
	}
	if err != nil {
		if errHandler != nil {
			errHandler(err)
		}
		return err
	}
	var payload []byte = make([]byte, 0, 96)
	payload = append(payload, `{"type":"l2Book","coin":`...)
	payload = appendJSONText(payload, coin)
	payload = appendL2BookParams(payload, options.L2BookOptions)
	if options.Fast {
		payload = append(payload, `,"fast":true`...)
	}
	payload = append(payload, '}')

	var snapshot types.OrderBookSnapshot
	return watch(ctx, deps, ws.RouteKey("l2Book", coin), payload, func(data []byte) {
		snapshot.Levels[0] = snapshot.Levels[0][:0]
		snapshot.Levels[1] = snapshot.Levels[1][:0]
		if decodeErr := codec.Unmarshal(data, &snapshot); decodeErr != nil {
			dropped(deps, "l2Book", decodeErr)
			return
		}
		handler(&snapshot)
	}, onReset, errHandler)
}

// coinPayload builds {"type":T,"coin":C}.
func coinPayload(subscriptionType string, coin string) []byte {
	var payload []byte = make([]byte, 0, 64)
	payload = append(payload, `{"type":"`...)
	payload = append(payload, subscriptionType...)
	payload = append(payload, `","coin":`...)
	payload = appendJSONText(payload, coin)
	return append(payload, '}')
}

// WatchBBO streams the best bid / offer of coin ("sent only if the bbo changes
// on a block").
func WatchBBO(ctx context.Context, deps StreamDeps, p *Profile, coin string, handler func(*types.BBO), errHandler func(error)) error {
	var err error = p.requireCoin("WatchBBO", coin)
	if err != nil {
		if errHandler != nil {
			errHandler(err)
		}
		return err
	}
	var bid, ask types.OrderBookLevel
	var bbo types.BBO
	return watch(ctx, deps, ws.RouteKey("bbo", coin), coinPayload("bbo", coin), func(data []byte) {
		// Point the decoder at reusable level storage; a null side resets the
		// pointer to nil.
		bbo.Levels[0] = &bid
		bbo.Levels[1] = &ask
		if decodeErr := codec.Unmarshal(data, &bbo); decodeErr != nil {
			dropped(deps, "bbo", decodeErr)
			return
		}
		handler(&bbo)
	}, nil, errHandler)
}

// WatchTrades streams public trades of coin. The slice is reused between pushes.
func WatchTrades(ctx context.Context, deps StreamDeps, p *Profile, coin string, handler func([]types.Trade), errHandler func(error)) error {
	var err error = p.requireCoin("WatchTrades", coin)
	if err != nil {
		if errHandler != nil {
			errHandler(err)
		}
		return err
	}
	var trades []types.Trade
	return watch(ctx, deps, ws.RouteKey("trades", coin), coinPayload("trades", coin), func(data []byte) {
		trades = trades[:0]
		if decodeErr := codec.Unmarshal(data, &trades); decodeErr != nil {
			dropped(deps, "trades", decodeErr)
			return
		}
		handler(trades)
	}, nil, errHandler)
}

// WatchCandles streams the current candle of coin / interval.
func WatchCandles(ctx context.Context, deps StreamDeps, p *Profile, coin string, interval string, handler func(*types.Candle), errHandler func(error)) error {
	const operation string = "WatchCandles"
	var err error = p.requireCoin(operation, coin)
	if err == nil && (interval == "" || !safeText(interval)) {
		err = p.invalid(operation, "invalid interval")
	}
	if err != nil {
		if errHandler != nil {
			errHandler(err)
		}
		return err
	}
	var payload []byte = make([]byte, 0, 96)
	payload = append(payload, `{"type":"candle","coin":`...)
	payload = appendJSONText(payload, coin)
	payload = append(payload, `,"interval":`...)
	payload = appendJSONText(payload, interval)
	payload = append(payload, '}')

	var candle types.Candle
	return watch(ctx, deps, ws.RouteKey("candle", coin+","+interval), payload, func(data []byte) {
		if decodeErr := codec.Unmarshal(data, &candle); decodeErr != nil {
			dropped(deps, "candle", decodeErr)
			return
		}
		handler(&candle)
	}, nil, errHandler)
}

// WatchPerpAssetCtx streams mark / oracle / mid prices, funding and open
// interest of a perpetual asset ("activeAssetCtx").
func WatchPerpAssetCtx(ctx context.Context, deps StreamDeps, p *Profile, coin string, handler func(*types.ActivePerpAssetCtx), errHandler func(error)) error {
	var err error = p.requireCoin("WatchPerpAssetCtx", coin)
	if err != nil {
		if errHandler != nil {
			errHandler(err)
		}
		return err
	}
	var active types.ActivePerpAssetCtx
	return watch(ctx, deps, ws.RouteKey("activeAssetCtx", coin), coinPayload("activeAssetCtx", coin), func(data []byte) {
		active.Ctx.ImpactPxs = active.Ctx.ImpactPxs[:0]
		if decodeErr := codec.Unmarshal(data, &active); decodeErr != nil {
			dropped(deps, "activeAssetCtx", decodeErr)
			return
		}
		handler(&active)
	}, nil, errHandler)
}

// userPayload builds {"type":T,"user":U}.
func userPayload(subscriptionType string, user string) []byte {
	var payload []byte = make([]byte, 0, 112)
	payload = append(payload, `{"type":"`...)
	payload = append(payload, subscriptionType...)
	payload = append(payload, `","user":`...)
	payload = appendJSONText(payload, user)
	return append(payload, '}')
}

/*
WatchOrderUpdates streams order status changes of user ("orderUpdates").

The pushes carry orders of EVERY section of the account (perp coins, "@N" spot
pairs, "dex:COIN") and no user field; sections filter by their own registry.
onReset fires on every (re)connect: updates may have been missed while the
socket was down, so consumers should re-seed from OpenOrders.
*/
func WatchOrderUpdates(ctx context.Context, deps StreamDeps, p *Profile, user string, handler func([]types.OrderUpdate), onReset func(), errHandler func(error)) error {
	var err error = p.requireUser("WatchOrderUpdates", user)
	if err != nil {
		if errHandler != nil {
			errHandler(err)
		}
		return err
	}
	var updates []types.OrderUpdate
	return watch(ctx, deps, ws.RouteKey("orderUpdates", ""), userPayload("orderUpdates", user), func(data []byte) {
		updates = updates[:0]
		if decodeErr := codec.Unmarshal(data, &updates); decodeErr != nil {
			dropped(deps, "orderUpdates", decodeErr)
			return
		}
		handler(updates)
	}, onReset, errHandler)
}

/*
WatchClearinghouseState streams absolute snapshots of the margin account of the
profile's dex ("clearinghouseState": positions, margin summaries,
withdrawable). Docs shape: {"type":"clearinghouseState","user":U,"dex":D}.

The push cadence is not documented; consumers must treat every push as the full
current state (never as a delta). onReset fires on every (re)connect.
*/
func WatchClearinghouseState(ctx context.Context, deps StreamDeps, p *Profile, user string, handler func(*types.AccountStateUpdate), onReset func(), errHandler func(error)) error {
	var err error = p.requireUser("WatchClearinghouseState", user)
	if err != nil {
		if errHandler != nil {
			errHandler(err)
		}
		return err
	}
	var payload []byte = make([]byte, 0, 128)
	payload = append(payload, `{"type":"clearinghouseState","user":`...)
	payload = appendJSONText(payload, user)
	payload = append(payload, `,"dex":`...)
	payload = appendJSONText(payload, p.Dex)
	payload = append(payload, '}')

	var update types.AccountStateUpdate
	return watch(ctx, deps, ws.RouteKey("clearinghouseState", p.Dex), payload, func(data []byte) {
		update.State.AssetPositions = update.State.AssetPositions[:0]
		if decodeErr := codec.Unmarshal(data, &update); decodeErr != nil {
			dropped(deps, "clearinghouseState", decodeErr)
			return
		}
		handler(&update)
	}, onReset, errHandler)
}

/*
WatchUserFills streams executions of user ("userFills"). The first push after
every (re)subscribe has IsSnapshot == true and replays recent history — this
is how the exchange covers fills missed during a reconnect; consumers must
de-duplicate by (Tid, Oid) or skip snapshots they have already processed.
*/
func WatchUserFills(ctx context.Context, deps StreamDeps, p *Profile, user string, aggregateByTime bool, handler func(*types.UserFills), onReset func(), errHandler func(error)) error {
	var err error = p.requireUser("WatchUserFills", user)
	if err != nil {
		if errHandler != nil {
			errHandler(err)
		}
		return err
	}
	var payload []byte = userPayload("userFills", user)
	if aggregateByTime {
		payload = payload[:len(payload)-1]
		payload = append(payload, `,"aggregateByTime":`...)
		payload = strconv.AppendBool(payload, true)
		payload = append(payload, '}')
	}
	var fills types.UserFills
	return watch(ctx, deps, ws.RouteKey("userFills", ""), payload, func(data []byte) {
		fills.IsSnapshot = false
		fills.Fills = fills.Fills[:0]
		if decodeErr := codec.Unmarshal(data, &fills); decodeErr != nil {
			dropped(deps, "userFills", decodeErr)
			return
		}
		handler(&fills)
	}, onReset, errHandler)
}
