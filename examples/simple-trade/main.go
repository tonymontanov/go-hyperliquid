/*
FILE: examples/simple-trade/main.go

DESCRIPTION:
Live order life-cycle smoke test of the Perpetuals section — TESTNET ONLY:

 1. load metadata, read the book;
 2. place a post-only (Alo) BUY far below the market with a client order id;
 3. query it (orderStatus by cloid, open orders);
 4. modify its price;
 5. cancel it by cloid, then verify that a second cancel reports MissingOrder;
 6. print the SDK-side rate-limit accounting and the address budget.

The order rests ~10% below the best bid, so it does not trade. The example
refuses to run unless HYPERLIQUID_ALLOW_LIVE=1 and always targets the testnet.

USAGE:
    cp .env.example .env   # fill in TESTNET credentials
    ./scripts/run.sh ./examples/simple-trade

ENVIRONMENT (names match the desk's credential convention):
    HYPERLIQUID_PERPETUALS_TESTNET_API_KEY      master account address (0x...)
    HYPERLIQUID_PERPETUALS_TESTNET_SECRET_KEY   API wallet private key
    HYPERLIQUID_PERPETUALS_TESTNET_PASSPHRASE   optional sub-account / vault address
    HYPERLIQUID_ALLOW_LIVE                      must be "1"
    HYPERLIQUID_COIN                            default BTC
    HYPERLIQUID_NOTIONAL                        order value in USDC, default 12
    HYPERLIQUID_USE_WS_POST                     "1" → trade over WebSocket post

SECURITY: the private key is read from the environment only; it is never
printed. Never put a mainnet key into this example's environment.
*/

package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	hyperliquid "github.com/tonymontanov/go-hyperliquid"
	"github.com/tonymontanov/go-hyperliquid/perpetuals"
	"github.com/tonymontanov/go-hyperliquid/types"
)

// errNotAllowed — the safety gate is closed.
var errNotAllowed = errors.New("refusing to send orders: set HYPERLIQUID_ALLOW_LIVE=1 (testnet only)")

func main() {
	var err error = run()
	if err != nil {
		fmt.Println("FAILED:", err)
		if errors.Is(err, errNotAllowed) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

// step wraps an error with the name of the failed step.
func step(name string, err error) error {
	return fmt.Errorf("at %s: %w", name, err)
}

// run holds the whole example so that deferred cleanups (client.Close, ctx
// cancel) run before the process exits with a status code.
func run() error {
	if os.Getenv("HYPERLIQUID_ALLOW_LIVE") != "1" {
		return errNotAllowed
	}
	var coin string = os.Getenv("HYPERLIQUID_COIN")
	if coin == "" {
		coin = "BTC"
	}
	var notional float64 = 12
	if raw := os.Getenv("HYPERLIQUID_NOTIONAL"); raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil && parsed >= 10 {
			notional = parsed
		}
	}

	var cfg hyperliquid.Config = hyperliquid.DefaultConfig()
	cfg.Testnet = true // hard-wired: this example never trades on mainnet
	cfg.AccountAddress = os.Getenv("HYPERLIQUID_PERPETUALS_TESTNET_API_KEY")
	cfg.PrivateKey = os.Getenv("HYPERLIQUID_PERPETUALS_TESTNET_SECRET_KEY")
	cfg.VaultAddress = os.Getenv("HYPERLIQUID_PERPETUALS_TESTNET_PASSPHRASE")
	cfg.RateLimitEventObserver = func(e hyperliquid.RateLimitEvent) {
		fmt.Printf("  [rate] %-28s weight=%-3d used=%d/%d orders=%d\n", e.Kind, e.Weight, e.UsedWeight, e.WeightLimit, e.OrderCount)
	}

	var client *hyperliquid.Client
	var err error
	client, err = hyperliquid.NewClient(cfg)
	if err != nil {
		return step("NewClient", err)
	}
	defer func() { _ = client.Close() }()
	if !client.CanSign() {
		return step("credentials", errors.New("HYPERLIQUID_PERPETUALS_TESTNET_SECRET_KEY is empty"))
	}
	fmt.Printf("signer (API wallet) = %s\naccount (info user) = %s\n", client.SignerAddress(), client.UserAddress())

	var perps *perpetuals.Client = perpetuals.NewClient(client)
	var trading *perpetuals.TradingClient = perps.Trading()
	var ctx context.Context
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if os.Getenv("HYPERLIQUID_USE_WS_POST") == "1" {
		if err = client.WarmUpPost(ctx); err != nil {
			return step("WarmUpPost", err)
		}
		trading = trading.WS()
		fmt.Println("transport: WebSocket post")
	}

	var info types.AssetInfo
	info, err = perps.MarketData().GetAssetInfo(ctx, coin)
	if err != nil {
		return step("GetAssetInfo", err)
	}
	var book types.OrderBookSnapshot
	book, err = perps.MarketData().GetOrderBook(ctx, coin, perpetuals.OrderBookOptions{})
	if err != nil {
		return step("GetOrderBook", err)
	}
	if len(book.Bids()) == 0 {
		return step("GetOrderBook", errors.New("empty bid side"))
	}

	var bestBid types.Fixed = book.Bids()[0].Price
	var rawPrice types.Fixed
	rawPrice, _ = types.FixedFromFloat64(bestBid.Float64() * 0.9)
	var price types.Fixed = info.Precision.NormalizePrice(rawPrice, types.RoundDown)
	var rawSize types.Fixed
	rawSize, _ = types.FixedFromFloat64(notional / price.Float64())
	var size types.Fixed = info.Precision.NormalizeSize(rawSize, types.RoundUp)

	var cloidBytes [16]byte
	_, _ = rand.Read(cloidBytes[:])
	var cloid types.Cloid = types.CloidFromBytes(cloidBytes)
	fmt.Printf("%s: assetID=%d bestBid=%s → Alo BUY %s @ %s cloid=%s\n", coin, info.AssetID, bestBid, size, price, cloid)

	var started time.Time = time.Now()
	var status types.ActionStatus
	status, err = trading.CreateOrder(ctx, types.OrderRequest{
		Coin: coin, IsBuy: true, Price: price, Size: size, TimeInForce: types.TimeInForceAlo, Cloid: cloid,
	}, types.OrderOptions{ExpiresAfterMs: uint64(time.Now().Add(15 * time.Second).UnixMilli())})
	if err != nil {
		return step("CreateOrder", err)
	}
	fmt.Printf("1. placed: %s oid=%d in %s\n", status.Kind, status.Oid, time.Since(started).Round(time.Millisecond))

	var state types.OrderState
	state, err = trading.GetOrderStatus(ctx, 0, cloid)
	if err != nil {
		return step("GetOrderStatus", err)
	}
	if !state.Found {
		return step("GetOrderStatus", errors.New("the exchange does not know the order"))
	}
	fmt.Printf("2. orderStatus: %s px=%s sz=%s tif=%s\n", state.Status, state.Order.LimitPx, state.Order.Sz, state.Order.Tif)

	var open []types.OpenOrder
	open, err = trading.GetOpenOrders(ctx, coin)
	if err != nil {
		return step("GetOpenOrders", err)
	}
	fmt.Printf("3. open orders on %s: %d\n", coin, len(open))

	var newPrice types.Fixed = info.Precision.NormalizePrice(price-info.Precision.TickSize(price)*5, types.RoundDown)
	started = time.Now()
	status, err = trading.ModifyOrder(ctx, types.ModifyRequest{RefCloid: cloid, Order: types.OrderRequest{
		Coin: coin, IsBuy: true, Price: newPrice, Size: size, TimeInForce: types.TimeInForceAlo, Cloid: cloid,
	}}, types.ModifyOptions{})
	if err != nil {
		return step("ModifyOrder", err)
	}
	fmt.Printf("4. modified to %s: %s oid=%d in %s\n", newPrice, status.Kind, status.Oid, time.Since(started).Round(time.Millisecond))

	started = time.Now()
	err = trading.CancelOrderByCloid(ctx, types.CancelByCloidRequest{Coin: coin, Cloid: cloid}, types.CancelOptions{})
	if err != nil {
		return step("CancelOrderByCloid", err)
	}
	fmt.Printf("5. cancelled in %s\n", time.Since(started).Round(time.Millisecond))

	err = trading.CancelOrderByCloid(ctx, types.CancelByCloidRequest{Coin: coin, Cloid: cloid}, types.CancelOptions{})
	fmt.Printf("6. second cancel → IsMissingOrder=%v (%v)\n", hyperliquid.IsMissingOrder(err), err)

	var limit types.UserRateLimit
	limit, err = perps.Account().GetUserRateLimit(ctx)
	if err != nil {
		return step("GetUserRateLimit", err)
	}
	fmt.Printf("7. address budget: used=%d cap=%d cumVlm=%s | IP weight used=%d/%d\n",
		limit.NRequestsUsed, limit.NRequestsCap, limit.CumVlm, client.IPWeightUsed(), hyperliquid.IPWeightLimitPerMinute)
	fmt.Println("OK")
	return nil
}
