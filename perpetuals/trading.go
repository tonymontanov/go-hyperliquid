/*
FILE: perpetuals/trading.go

DESCRIPTION:
Order management of the Perpetuals section. Every method is the unified
function of the common layer (internal/domain) plus the section profile — no
request logic is implemented here.

TRANSPORT:
Trading() routes actions through REST; Trading().WS() returns the same API
routed through WebSocket post requests (call Client.WarmUpPost first so the
first order does not pay for the handshake). The WS variant never falls back
to REST silently: with the socket down a call fails fast with a Network error.

RESULT CONVENTION:
  - err != nil                    : the request failed or the exchange rejected
                                    the WHOLE action (nothing was applied);
  - statuses[i].Kind == ...Error  : item i was rejected, the others stand.
Single-item helpers (CreateOrder, ModifyOrder, CancelOrder...) fold an item
rejection into the returned error for convenience.

EMULATED OPERATIONS (Hyperliquid has no native action for them):
  - CancelAllOrders       : frontendOpenOrders → one batch cancel;
  - CancelForgottenOrders : the same, filtered by order age.
The exchange-side alternative for "cancel everything" is ScheduleCancel (dead
man's switch), which has a 5 s minimum delay and a 10-per-day trigger limit.
*/

package perpetuals

import (
	"context"
	"time"

	"github.com/tonymontanov/go-hyperliquid/internal/domain"
	"github.com/tonymontanov/go-hyperliquid/internal/engine"
	"github.com/tonymontanov/go-hyperliquid/types"
)

// TradingClient — order management sub-client.
type TradingClient struct {
	c         *Client
	transport engine.Transport
}

// WS returns the trading API routed through WebSocket post requests.
func (t *TradingClient) WS() *TradingClient {
	return &TradingClient{c: t.c, transport: engine.TransportWS}
}

// firstStatus folds a single-item answer: an item rejection becomes the error.
func firstStatus(statuses []types.ActionStatus, err error) (types.ActionStatus, error) {
	if err != nil {
		return types.ActionStatus{}, err
	}
	if len(statuses) == 0 {
		return types.ActionStatus{}, nil
	}
	if statuses[0].Kind == types.ActionStatusError {
		return statuses[0], statuses[0].Err
	}
	return statuses[0], nil
}

// CreateOrder places one order.
func (t *TradingClient) CreateOrder(ctx context.Context, request types.OrderRequest, options types.OrderOptions) (types.ActionStatus, error) {
	var requests [1]types.OrderRequest = [1]types.OrderRequest{request}
	return firstStatus(domain.PlaceOrders(ctx, t.c.engine(), t.c.prof(), requests[:], options, t.transport))
}

// CreateBatchOrders places several orders in one action (one nonce, one
// signature, IP weight 1 + floor(n/40), address-based cost n).
func (t *TradingClient) CreateBatchOrders(ctx context.Context, requests []types.OrderRequest, options types.OrderOptions) ([]types.ActionStatus, error) {
	return domain.PlaceOrders(ctx, t.c.engine(), t.c.prof(), requests, options, t.transport)
}

// ModifyOrder replaces one resting order.
func (t *TradingClient) ModifyOrder(ctx context.Context, request types.ModifyRequest, options types.ModifyOptions) (types.ActionStatus, error) {
	var requests [1]types.ModifyRequest = [1]types.ModifyRequest{request}
	return firstStatus(domain.ModifyOrders(ctx, t.c.engine(), t.c.prof(), requests[:], options, t.transport))
}

// ModifyBatchOrders replaces several resting orders in one action.
func (t *TradingClient) ModifyBatchOrders(ctx context.Context, requests []types.ModifyRequest, options types.ModifyOptions) ([]types.ActionStatus, error) {
	return domain.ModifyOrders(ctx, t.c.engine(), t.c.prof(), requests, options, t.transport)
}

// CancelOrder cancels one order by exchange order id. An already gone order
// yields an error for which hyperliquid.IsMissingOrder reports true.
func (t *TradingClient) CancelOrder(ctx context.Context, request types.CancelRequest, options types.CancelOptions) error {
	var requests [1]types.CancelRequest = [1]types.CancelRequest{request}
	var err error
	_, err = firstStatus(domain.CancelOrders(ctx, t.c.engine(), t.c.prof(), requests[:], options, t.transport))
	return err
}

// CancelBatchOrders cancels several orders by exchange order id.
func (t *TradingClient) CancelBatchOrders(ctx context.Context, requests []types.CancelRequest, options types.CancelOptions) ([]types.ActionStatus, error) {
	return domain.CancelOrders(ctx, t.c.engine(), t.c.prof(), requests, options, t.transport)
}

// CancelOrderByCloid cancels one order by client order id.
func (t *TradingClient) CancelOrderByCloid(ctx context.Context, request types.CancelByCloidRequest, options types.CancelOptions) error {
	var requests [1]types.CancelByCloidRequest = [1]types.CancelByCloidRequest{request}
	var err error
	_, err = firstStatus(domain.CancelOrdersByCloid(ctx, t.c.engine(), t.c.prof(), requests[:], options, t.transport))
	return err
}

// CancelBatchOrdersByCloid cancels several orders by client order id.
func (t *TradingClient) CancelBatchOrdersByCloid(ctx context.Context, requests []types.CancelByCloidRequest, options types.CancelOptions) ([]types.ActionStatus, error) {
	return domain.CancelOrdersByCloid(ctx, t.c.engine(), t.c.prof(), requests, options, t.transport)
}

// GetOpenOrders returns the resting orders of the section. coin == "" returns
// every perpetual; spot and builder-deployed orders of the account are
// filtered out.
func (t *TradingClient) GetOpenOrders(ctx context.Context, coin string) ([]types.OpenOrder, error) {
	var err error = t.c.ensureAssets(ctx)
	if err != nil {
		return nil, err
	}
	var all []types.OpenOrder
	all, err = domain.OpenOrders(ctx, t.c.engine(), t.c.prof(), t.c.user(), t.transport)
	if err != nil {
		return nil, err
	}
	var out []types.OpenOrder = all[:0]
	var i int
	for i = 0; i < len(all); i++ {
		if coin != "" && all[i].Coin != coin {
			continue
		}
		if coin == "" && !t.c.owns(all[i].Coin) {
			continue
		}
		out = append(out, all[i])
	}
	return out, nil
}

// GetOrderStatus queries one order by oid, or by cloid when it is set.
// OrderState.Found is false when the exchange does not know the order.
func (t *TradingClient) GetOrderStatus(ctx context.Context, oid uint64, cloid types.Cloid) (types.OrderState, error) {
	return domain.OrderStatus(ctx, t.c.engine(), t.c.prof(), t.c.user(), oid, cloid, t.transport)
}

// cancelSelected cancels the given open orders in one batch.
func (t *TradingClient) cancelSelected(ctx context.Context, orders []types.OpenOrder) ([]types.ActionStatus, error) {
	if len(orders) == 0 {
		return nil, nil
	}
	var requests []types.CancelRequest = make([]types.CancelRequest, len(orders))
	var i int
	for i = 0; i < len(orders); i++ {
		requests[i] = types.CancelRequest{Coin: orders[i].Coin, Oid: orders[i].Oid}
	}
	return domain.CancelOrders(ctx, t.c.engine(), t.c.prof(), requests, types.CancelOptions{}, t.transport)
}

// CancelAllOrders cancels every resting order of coin (coin == "" — of the
// whole section). Emulated: open orders query + one batch cancel. Orders that
// disappear between the two steps come back as MissingOrder item errors.
func (t *TradingClient) CancelAllOrders(ctx context.Context, coin string) ([]types.ActionStatus, error) {
	var orders []types.OpenOrder
	var err error
	orders, err = t.GetOpenOrders(ctx, coin)
	if err != nil {
		return nil, err
	}
	return t.cancelSelected(ctx, orders)
}

// CancelForgottenOrders cancels resting orders of coin older than ttl and
// returns the orders it tried to cancel together with the item statuses.
func (t *TradingClient) CancelForgottenOrders(ctx context.Context, coin string, ttl time.Duration) ([]types.OpenOrder, []types.ActionStatus, error) {
	var orders []types.OpenOrder
	var err error
	orders, err = t.GetOpenOrders(ctx, coin)
	if err != nil {
		return nil, nil, err
	}
	var deadlineMs int64 = time.Now().Add(-ttl).UnixMilli()
	var stale []types.OpenOrder = orders[:0]
	var i int
	for i = 0; i < len(orders); i++ {
		if orders[i].TimestampMs <= deadlineMs {
			stale = append(stale, orders[i])
		}
	}
	var statuses []types.ActionStatus
	statuses, err = t.cancelSelected(ctx, stale)
	return stale, statuses, err
}

// ScheduleCancel arms the dead man's switch: at timeMs (unix ms, at least 5 s
// ahead) the exchange cancels ALL open orders of the account — of every
// section. At most 10 triggers per day.
func (t *TradingClient) ScheduleCancel(ctx context.Context, timeMs uint64) error {
	if timeMs == 0 {
		return t.c.prof().Invalid("ScheduleCancel", "timeMs is zero; use UnscheduleCancel to disarm")
	}
	return domain.ScheduleCancel(ctx, t.c.engine(), timeMs, t.transport)
}

// UnscheduleCancel disarms the dead man's switch.
func (t *TradingClient) UnscheduleCancel(ctx context.Context) error {
	return domain.ScheduleCancel(ctx, t.c.engine(), 0, t.transport)
}

// NextNonce reserves a nonce of the signing wallet. Pass it as nonce to Noop
// to invalidate an in-flight action that was signed with the same nonce.
func (t *TradingClient) NextNonce() uint64 { return t.c.engine().NextNonce() }

// Noop consumes a nonce (0 → a fresh one) without any other effect.
func (t *TradingClient) Noop(ctx context.Context, nonce uint64) (uint64, error) {
	return domain.Noop(ctx, t.c.engine(), nonce, t.transport)
}
