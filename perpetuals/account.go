/*
FILE: perpetuals/account.go

DESCRIPTION:
Account and position management of the Perpetuals section: account state,
positions, leverage, isolated margin, position close, fills, rate-limit state.
Requests are the unified functions of the common layer with the section
profile; the section adds position lookup and the market-close recipe.

POSITION MODEL:
Hyperliquid is one-way only (position type "oneWay"): one signed position per
coin — szi > 0 long, szi < 0 short. There is no hedge mode and no position
mode switch.

CLOSE POSITION:
There is no native market order. ClosePosition follows the recipe of the
official Python SDK (market_close): a reduce-only IOC limit order priced
through the book by a slippage fraction off the current mid.

ACCOUNT ABSTRACTION MODES:
In "standard" mode clearinghouseState carries both positions and balances.
Under "unified account" / "portfolio margin" the docs state that balances are
shown in the SPOT clearinghouse state and that those modes are limited to 50k
user actions per day — standard mode is the one suited for high-frequency
trading.
*/

package perpetuals

import (
	"context"

	"github.com/tonymontanov/go-hyperliquid/internal/domain"
	"github.com/tonymontanov/go-hyperliquid/internal/engine"
	"github.com/tonymontanov/go-hyperliquid/types"
)

// DefaultCloseSlippage — slippage fraction of ClosePosition when the caller
// passes 0: 5%, the default of the official Python SDK.
const DefaultCloseSlippage float64 = 0.05

// AccountClient — account / position sub-client.
type AccountClient struct {
	c *Client
}

// GetAccountState returns the margin account state of the first perp dex.
func (a *AccountClient) GetAccountState(ctx context.Context) (types.ClearinghouseState, error) {
	return domain.ClearinghouseState(ctx, a.c.engine(), a.c.prof(), a.c.user(), engine.TransportREST)
}

// GetPosition returns the position of coin. found is false when the account
// holds no position in it.
func (a *AccountClient) GetPosition(ctx context.Context, coin string) (types.Position, bool, error) {
	var empty types.Position
	var err error
	_, err = a.c.prof().Resolve(ctx, "GetPosition", coin)
	if err != nil {
		return empty, false, err
	}
	var state types.ClearinghouseState
	state, err = a.GetAccountState(ctx)
	if err != nil {
		return empty, false, err
	}
	var i int
	for i = 0; i < len(state.AssetPositions); i++ {
		if state.AssetPositions[i].Position.Coin == coin {
			return state.AssetPositions[i].Position, true, nil
		}
	}
	return empty, false, nil
}

// SetLeverage sets the leverage of coin; isCross selects cross or isolated
// margin. The value is validated against the asset's maxLeverage locally.
func (a *AccountClient) SetLeverage(ctx context.Context, coin string, leverage int, isCross bool) error {
	return domain.UpdateLeverage(ctx, a.c.engine(), a.c.prof(), coin, leverage, isCross)
}

// UpdateIsolatedMargin adds (amount > 0) or removes (amount < 0) isolated
// margin of coin. amount is in USDC; it is sent with 6 decimals.
func (a *AccountClient) UpdateIsolatedMargin(ctx context.Context, coin string, amount types.Fixed) error {
	// Fixed has 8 decimals, ntli has 6: drop the two extra digits exactly.
	if int64(amount)%100 != 0 {
		return a.c.prof().Invalid("UpdateIsolatedMargin", "amount has more than 6 decimals")
	}
	return domain.UpdateIsolatedMargin(ctx, a.c.engine(), a.c.prof(), coin, int64(amount)/100)
}

/*
ClosePosition closes the whole position of coin with a reduce-only IOC order
priced slippage (fraction, 0 → DefaultCloseSlippage) through the current mid.

Returns the order status; found is false (and nothing is sent) when there is
no position. A partially filled IOC leaves a remainder — callers that need a
guaranteed flat position should re-check and repeat.
*/
func (a *AccountClient) ClosePosition(ctx context.Context, coin string, slippage float64, cloid types.Cloid) (types.ActionStatus, bool, error) {
	const operation string = "ClosePosition"
	var none types.ActionStatus
	if slippage < 0 || slippage >= 1 {
		return none, false, a.c.prof().Invalid(operation, "slippage must be in [0, 1)")
	}
	if slippage == 0 {
		slippage = DefaultCloseSlippage
	}

	var info *types.AssetInfo
	var err error
	info, err = a.c.prof().Resolve(ctx, operation, coin)
	if err != nil {
		return none, false, err
	}
	var position types.Position
	var found bool
	position, found, err = a.GetPosition(ctx, coin)
	if err != nil || !found || position.Szi.IsZero() {
		return none, false, err
	}

	var size types.Fixed
	size, err = types.FixedFromDecimal(position.Szi.Abs())
	if err != nil {
		return none, true, a.c.prof().Invalid(operation, "position size does not fit the fixed-point range")
	}
	// Closing a long sells, closing a short buys.
	var isBuy bool = position.Szi.IsNegative()

	var price types.Fixed
	price, err = a.c.market.SlippagePrice(ctx, coin, isBuy, slippage)
	if err != nil {
		return none, true, err
	}

	var status types.ActionStatus
	status, err = a.c.trading.CreateOrder(ctx, types.OrderRequest{
		Coin:        coin,
		IsBuy:       isBuy,
		Price:       price,
		Size:        info.Precision.NormalizeSize(size, types.RoundDown),
		ReduceOnly:  true,
		TimeInForce: types.TimeInForceIoc,
		Cloid:       cloid,
	}, types.OrderOptions{})
	return status, true, err
}

// GetUserFills returns the most recent fills of the section (at most 2000
// account-wide fills are scanned).
func (a *AccountClient) GetUserFills(ctx context.Context, aggregateByTime bool) ([]types.Fill, error) {
	var err error = a.c.ensureAssets(ctx)
	if err != nil {
		return nil, err
	}
	var all []types.Fill
	all, err = domain.UserFills(ctx, a.c.engine(), a.c.prof(), a.c.user(), aggregateByTime)
	if err != nil {
		return nil, err
	}
	return a.ownFills(all), nil
}

// GetUserFillsByTime returns fills of the section within [startMs, endMs]
// (endMs == 0 → now). At most 2000 fills per response.
func (a *AccountClient) GetUserFillsByTime(ctx context.Context, startMs int64, endMs int64, aggregateByTime bool) ([]types.Fill, error) {
	var err error = a.c.ensureAssets(ctx)
	if err != nil {
		return nil, err
	}
	var all []types.Fill
	all, err = domain.UserFillsByTime(ctx, a.c.engine(), a.c.prof(), a.c.user(), startMs, endMs, aggregateByTime)
	if err != nil {
		return nil, err
	}
	return a.ownFills(all), nil
}

// ownFills keeps the fills whose coin belongs to the section.
func (a *AccountClient) ownFills(all []types.Fill) []types.Fill {
	var out []types.Fill = all[:0]
	var i int
	for i = 0; i < len(all); i++ {
		if a.c.owns(all[i].Coin) {
			out = append(out, all[i])
		}
	}
	return out
}

// GetUserRateLimit returns the address-based rate-limit state of the account
// and syncs the client's local budget mirror (Client.AddressBudget).
func (a *AccountClient) GetUserRateLimit(ctx context.Context) (types.UserRateLimit, error) {
	return domain.UserRateLimit(ctx, a.c.engine(), a.c.prof(), a.c.user())
}
