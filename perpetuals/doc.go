/*
Package perpetuals is the section layer for Hyperliquid "Perpetuals" — the
validator-operated perp dex, called "the first perp dex" in the API docs
(dex = "").

The package is a THIN specialisation of the common layer. Everything that is
identical across sections — signing, transports, action builders, order
placement, info requests, streams — lives in the root package and in
internal/* (see internal/domain). This package contributes only what is
specific to the section:

  - asset id       = index of the coin in meta.universe;
  - MAX_DECIMALS   = 6 (price decimals are limited to 6 - szDecimals);
  - dex            = "" in every dex-scoped request;
  - coin naming    = plain names ("BTC", "ETH", "kPEPE"); spot pairs ("@107",
    "PURR/USDC") and builder-deployed perps ("xyz:XYZ100"),
    which share several account-wide answers and streams, are
    filtered out by the section's asset registry;
  - margin actions = updateLeverage / updateIsolatedMargin, positions.

It never imports or calls another section.

# Usage

	var client *hyperliquid.Client
	client, err = hyperliquid.NewClient(cfg)
	var perps *perpetuals.Client = perpetuals.NewClient(client)

	var info types.AssetInfo
	info, err = perps.MarketData().GetAssetInfo(ctx, "BTC")
	var price types.Fixed = info.Precision.NormalizePrice(rawPrice, types.RoundDown)

	statuses, err = perps.Trading().CreateOrder(ctx, types.OrderRequest{
		Coin: "BTC", IsBuy: true, Price: price, Size: size,
		TimeInForce: types.TimeInForceAlo, Cloid: cloid,
	}, types.OrderOptions{})

Sub-clients: Trading(), Account(), MarketData(), Stream(). Trading().WS()
returns the same trading API routed through WebSocket post requests.
*/
package perpetuals
