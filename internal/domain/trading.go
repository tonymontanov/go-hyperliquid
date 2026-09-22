/*
FILE: internal/domain/trading.go

DESCRIPTION:
Unified trading functions of the common layer (see profile.go for the rule).
Every function validates locally, resolves coins through the section's
registry, builds the section-agnostic action and hands it to the engine.

VALIDATION BEFORE NETWORK:
A rejected action still costs a round trip and address-based rate-limit
budget, so everything that is a deterministic function of the payload is
checked locally and returned as ErrorKindInvalidRequest without any I/O:
unknown / delisted coin, non-positive size, price or size off the exchange
grid (types.Precision), invalid tif / tpsl / grouping, empty batch.
The SDK does NOT round silently: callers normalise with types.Precision
(NormalizePrice / NormalizeSize) and choose the rounding direction themselves.

MODIFY:
Both modify flavours are sent as "batchModify" — the shape used and proven by
the official Python SDK. The single "modify" action exists only in the GitBook
docs; its builder is implemented and vector-tested (internal/action) and will
be switched on after confirmation on testnet.

RESULTS:
One types.ActionStatus per request item, in request order. A rejection of the
WHOLE action is returned as the error of the call.
*/

package domain

import (
	"context"
	"strconv"

	"github.com/tonymontanov/go-hyperliquid/internal/action"
	"github.com/tonymontanov/go-hyperliquid/internal/engine"
	"github.com/tonymontanov/go-hyperliquid/internal/signing"
	"github.com/tonymontanov/go-hyperliquid/types"
)

// orderInvalid builds the error of one order of a batch. The message is
// assembled only on the failure path — the success path allocates nothing here.
func (p *Profile) orderInvalid(operation string, index int, coin string, detail string) error {
	return p.invalid(operation, "order["+strconv.Itoa(index)+"] "+coin+": "+detail)
}

// orderWire validates one request and converts it to its wire form.
func (p *Profile) orderWire(ctx context.Context, operation string, index int, req *types.OrderRequest) (action.OrderWire, error) {
	var wire action.OrderWire
	var info *types.AssetInfo
	var err error
	info, err = p.resolve(ctx, operation, req.Coin)
	if err != nil {
		return wire, err
	}
	if info.IsDelisted {
		return wire, p.orderInvalid(operation, index, req.Coin, "asset is delisted")
	}
	if !info.Precision.ValidateSize(req.Size) {
		return wire, p.orderInvalid(operation, index, req.Coin, "size "+req.Size.String()+" must be positive with at most "+strconv.Itoa(info.SzDecimals)+" decimals")
	}
	if !info.Precision.ValidatePrice(req.Price) {
		return wire, p.orderInvalid(operation, index, req.Coin, "price "+req.Price.String()+" violates the 5 significant figures / max decimals rule")
	}

	wire.Asset = info.AssetID
	wire.IsBuy = req.IsBuy
	wire.Px = req.Price
	wire.Sz = req.Size
	wire.ReduceOnly = req.ReduceOnly
	wire.Cloid = req.Cloid

	if req.IsTrigger {
		if !req.Tpsl.Valid() {
			return wire, p.orderInvalid(operation, index, req.Coin, "trigger order needs Tpsl tp or sl")
		}
		if !info.Precision.ValidatePrice(req.TriggerPrice) {
			return wire, p.orderInvalid(operation, index, req.Coin, "trigger price "+req.TriggerPrice.String()+" violates the 5 significant figures / max decimals rule")
		}
		wire.IsTrigger = true
		wire.TriggerPx = req.TriggerPrice
		wire.IsMarket = req.TriggerIsMarket
		wire.Tpsl = req.Tpsl
		return wire, nil
	}
	if !req.TimeInForce.Valid() {
		return wire, p.orderInvalid(operation, index, req.Coin, "limit order needs TimeInForce Alo, Ioc or Gtc")
	}
	wire.Tif = req.TimeInForce
	return wire, nil
}

// PlaceOrders places one or more orders in a single "order" action.
func PlaceOrders(ctx context.Context, e *engine.Engine, p *Profile, requests []types.OrderRequest, options types.OrderOptions, transport engine.Transport) ([]types.ActionStatus, error) {
	const operation string = "PlaceOrders"
	if len(requests) == 0 {
		return nil, p.invalid(operation, "empty batch")
	}
	var grouping types.Grouping = options.Grouping
	if grouping == "" {
		grouping = types.GroupingNone
	}
	if !grouping.Valid() {
		return nil, p.invalid(operation, "invalid grouping "+string(grouping))
	}

	var act *action.Order = &action.Order{
		Orders:   make([]action.OrderWire, len(requests)),
		Grouping: grouping,
	}
	var err error
	var i int
	for i = 0; i < len(requests); i++ {
		act.Orders[i], err = p.orderWire(ctx, operation, i, &requests[i])
		if err != nil {
			return nil, err
		}
	}
	if options.Builder != nil {
		var builder signing.Address
		builder, err = signing.ParseAddress(options.Builder.Address)
		if err != nil {
			return nil, p.invalid(operation, "invalid builder address")
		}
		act.Builder = action.BuilderFee{Set: true, Address: builder, Fee: options.Builder.FeeTenthsBps}
	}

	var result engine.ActionResult
	result, err = e.Action(ctx, act, engine.ActionOptions{
		Transport:      transport,
		ExpiresAfterMs: options.ExpiresAfterMs,
		Category:       engine.CategoryPlace,
	})
	if err != nil {
		return nil, err
	}
	return result.Statuses, nil
}

// ModifyOrders replaces one or more resting orders ("batchModify").
func ModifyOrders(ctx context.Context, e *engine.Engine, p *Profile, requests []types.ModifyRequest, options types.ModifyOptions, transport engine.Transport) ([]types.ActionStatus, error) {
	const operation string = "ModifyOrders"
	if len(requests) == 0 {
		return nil, p.invalid(operation, "empty batch")
	}
	var act *action.BatchModify = &action.BatchModify{
		Modifies:    make([]action.ModifyWire, len(requests)),
		AlwaysPlace: options.AlwaysPlace,
	}
	var err error
	var i int
	for i = 0; i < len(requests); i++ {
		if requests[i].Oid == 0 && !requests[i].RefCloid.IsSet() {
			return nil, p.invalid(operation, "modify["+strconv.Itoa(i)+"]: neither Oid nor RefCloid is set")
		}
		act.Modifies[i].Ref = action.OrderRef{Oid: requests[i].Oid, Cloid: requests[i].RefCloid}
		act.Modifies[i].Order, err = p.orderWire(ctx, operation, i, &requests[i].Order)
		if err != nil {
			return nil, err
		}
	}

	var result engine.ActionResult
	result, err = e.Action(ctx, act, engine.ActionOptions{
		Transport:      transport,
		ExpiresAfterMs: options.ExpiresAfterMs,
		Category:       engine.CategoryAmend,
	})
	if err != nil {
		return nil, err
	}
	return result.Statuses, nil
}

// CancelOrders cancels orders by exchange order id ("cancel").
func CancelOrders(ctx context.Context, e *engine.Engine, p *Profile, requests []types.CancelRequest, options types.CancelOptions, transport engine.Transport) ([]types.ActionStatus, error) {
	const operation string = "CancelOrders"
	if len(requests) == 0 {
		return nil, p.invalid(operation, "empty batch")
	}
	var act *action.Cancel = &action.Cancel{
		Cancels: make([]action.CancelWire, len(requests)),
		Fast:    options.Fast,
	}
	var i int
	for i = 0; i < len(requests); i++ {
		if requests[i].Oid == 0 {
			return nil, p.invalid(operation, "cancel["+strconv.Itoa(i)+"]: Oid is zero")
		}
		var info *types.AssetInfo
		var err error
		info, err = p.resolve(ctx, operation, requests[i].Coin)
		if err != nil {
			return nil, err
		}
		act.Cancels[i] = action.CancelWire{Asset: info.AssetID, Oid: requests[i].Oid}
	}

	var result engine.ActionResult
	var err error
	result, err = e.Action(ctx, act, engine.ActionOptions{
		Transport:      transport,
		ExpiresAfterMs: options.ExpiresAfterMs,
		Category:       engine.CategoryCancel,
	})
	if err != nil {
		return nil, err
	}
	return result.Statuses, nil
}

// CancelOrdersByCloid cancels orders by client order id ("cancelByCloid").
func CancelOrdersByCloid(ctx context.Context, e *engine.Engine, p *Profile, requests []types.CancelByCloidRequest, options types.CancelOptions, transport engine.Transport) ([]types.ActionStatus, error) {
	const operation string = "CancelOrdersByCloid"
	if len(requests) == 0 {
		return nil, p.invalid(operation, "empty batch")
	}
	var act *action.CancelByCloid = &action.CancelByCloid{
		Cancels: make([]action.CancelByCloidWire, len(requests)),
		Fast:    options.Fast,
	}
	var i int
	for i = 0; i < len(requests); i++ {
		if !requests[i].Cloid.IsSet() {
			return nil, p.invalid(operation, "cancel["+strconv.Itoa(i)+"]: Cloid is not set")
		}
		var info *types.AssetInfo
		var err error
		info, err = p.resolve(ctx, operation, requests[i].Coin)
		if err != nil {
			return nil, err
		}
		act.Cancels[i] = action.CancelByCloidWire{Asset: info.AssetID, Cloid: requests[i].Cloid}
	}

	var result engine.ActionResult
	var err error
	result, err = e.Action(ctx, act, engine.ActionOptions{
		Transport:      transport,
		ExpiresAfterMs: options.ExpiresAfterMs,
		Category:       engine.CategoryCancel,
	})
	if err != nil {
		return nil, err
	}
	return result.Statuses, nil
}

/*
ScheduleCancel arms (timeMs > 0) or disarms (timeMs == 0) the dead man's
switch: at timeMs the exchange cancels ALL open orders of the account.

Exchange rules: the time must be at least 5 seconds in the future; at most 10
triggers per day, reset at 00:00 UTC.
*/
func ScheduleCancel(ctx context.Context, e *engine.Engine, timeMs uint64, transport engine.Transport) error {
	var act *action.ScheduleCancel = &action.ScheduleCancel{HasTime: timeMs != 0, TimeMs: timeMs}
	var err error
	_, err = e.Action(ctx, act, engine.ActionOptions{Transport: transport, Category: engine.CategoryCancel})
	return err
}

/*
Noop consumes a nonce without doing anything. nonce == 0 generates a fresh one.

Sending a noop with the SAME nonce as an in-flight order is the technique the
docs recommend for invalidating that order: whichever lands first makes the
other one a duplicate nonce.
*/
func Noop(ctx context.Context, e *engine.Engine, nonce uint64, transport engine.Transport) (uint64, error) {
	var result engine.ActionResult
	var err error
	result, err = e.Action(ctx, &action.Noop{}, engine.ActionOptions{Transport: transport, Nonce: nonce, Category: engine.CategoryOther})
	return result.Nonce, err
}

// UpdateLeverage sets cross or isolated leverage of a margined asset.
func UpdateLeverage(ctx context.Context, e *engine.Engine, p *Profile, coin string, leverage int, isCross bool) error {
	const operation string = "UpdateLeverage"
	var info *types.AssetInfo
	var err error
	info, err = p.resolve(ctx, operation, coin)
	if err != nil {
		return err
	}
	if leverage < 1 || (info.MaxLeverage > 0 && leverage > info.MaxLeverage) {
		return p.invalid(operation, coin+": leverage "+strconv.Itoa(leverage)+" is outside 1.."+strconv.Itoa(info.MaxLeverage))
	}
	if isCross && info.OnlyIsolated {
		return p.invalid(operation, coin+": the asset allows isolated margin only")
	}
	var act *action.UpdateLeverage = &action.UpdateLeverage{Asset: info.AssetID, IsCross: isCross, Leverage: uint32(leverage)}
	_, err = e.Action(ctx, act, engine.ActionOptions{Category: engine.CategoryOther})
	return err
}

// UpdateIsolatedMargin adds (ntli > 0) or removes (ntli < 0) isolated margin.
// ntli is a USDC amount with 6 decimals: 1_000_000 = 1 USD.
func UpdateIsolatedMargin(ctx context.Context, e *engine.Engine, p *Profile, coin string, ntli int64) error {
	const operation string = "UpdateIsolatedMargin"
	var info *types.AssetInfo
	var err error
	info, err = p.resolve(ctx, operation, coin)
	if err != nil {
		return err
	}
	if ntli == 0 {
		return p.invalid(operation, coin+": amount is zero")
	}
	var act *action.UpdateIsolatedMargin = &action.UpdateIsolatedMargin{Asset: info.AssetID, IsBuy: true, Ntli: ntli}
	_, err = e.Action(ctx, act, engine.ActionOptions{Category: engine.CategoryOther})
	return err
}
