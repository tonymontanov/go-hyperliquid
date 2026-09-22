/*
FILE: examples/market-data/main.go

DESCRIPTION:
Keyless smoke example: public market data of the Perpetuals section over REST
and WebSocket. Needs no credentials and sends no orders, so it is safe to run
against mainnet.

USAGE:
    go run ./examples/market-data                 # testnet, BTC
    HYPERLIQUID_MAINNET=1 HYPERLIQUID_COIN=ETH go run ./examples/market-data

ENVIRONMENT:
    HYPERLIQUID_MAINNET   "1" → mainnet (default: testnet)
    HYPERLIQUID_COIN      perpetual coin name (default: BTC)
    HYPERLIQUID_SECONDS   how long to stream (default: 8)
*/

package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	hyperliquid "github.com/tonymontanov/go-hyperliquid"
	"github.com/tonymontanov/go-hyperliquid/perpetuals"
	"github.com/tonymontanov/go-hyperliquid/types"
)

func main() {
	var coin string = os.Getenv("HYPERLIQUID_COIN")
	if coin == "" {
		coin = "BTC"
	}
	var seconds int = 8
	if raw := os.Getenv("HYPERLIQUID_SECONDS"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			seconds = parsed
		}
	}

	var cfg hyperliquid.Config = hyperliquid.DefaultConfig()
	cfg.Testnet = os.Getenv("HYPERLIQUID_MAINNET") != "1"

	var client *hyperliquid.Client
	var err error
	client, err = hyperliquid.NewClient(cfg)
	if err != nil {
		fmt.Println("client:", err)
		os.Exit(1)
	}
	defer func() { _ = client.Close() }()
	var perps *perpetuals.Client = perpetuals.NewClient(client)

	var ctx context.Context
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(context.Background(), time.Duration(seconds+20)*time.Second)
	defer cancel()

	var assets []types.AssetInfo
	assets, err = perps.MarketData().GetAssets(ctx)
	if err != nil {
		fmt.Println("assets:", err)
		os.Exit(1)
	}
	var info types.AssetInfo
	info, err = perps.MarketData().GetAssetInfo(ctx, coin)
	if err != nil {
		fmt.Println("asset:", err)
		os.Exit(1)
	}
	fmt.Printf("testnet=%v assets=%d %s: assetID=%d szDecimals=%d maxLeverage=%d\n",
		cfg.Testnet, len(assets), coin, info.AssetID, info.SzDecimals, info.MaxLeverage)

	var book types.OrderBookSnapshot
	book, err = perps.MarketData().GetOrderBook(ctx, coin, perpetuals.OrderBookOptions{})
	if err != nil {
		fmt.Println("book:", err)
		os.Exit(1)
	}
	fmt.Printf("book: time=%d bids=%d asks=%d", book.TimeMs, len(book.Bids()), len(book.Asks()))
	if len(book.Bids()) > 0 && len(book.Asks()) > 0 {
		var bid types.Fixed = book.Bids()[0].Price
		fmt.Printf(" best=%s/%s tick@bid=%s lot=%s validBid=%v",
			bid, book.Asks()[0].Price, info.Precision.TickSize(bid), info.Precision.LotSize(), info.Precision.ValidatePrice(bid))
	}
	fmt.Println()

	var mid types.Fixed
	mid, err = perps.MarketData().GetMid(ctx, coin)
	fmt.Printf("mid=%s err=%v\n", mid, err)

	var nowMs int64 = time.Now().UnixMilli()
	var candles []types.Candle
	candles, err = perps.MarketData().GetCandles(ctx, coin, types.Interval1m, nowMs-5*60_000, nowMs)
	fmt.Printf("candles(1m, 5 min)=%d err=%v\n", len(candles), err)
	if len(candles) > 0 {
		var last types.Candle = candles[len(candles)-1]
		fmt.Printf("  last: o=%s h=%s l=%s c=%s v=%s n=%d\n", last.Open, last.High, last.Low, last.Close, last.Volume, last.Trades)
	}

	var contexts []perpetuals.AssetContext
	contexts, err = perps.MarketData().GetAssetContexts(ctx)
	fmt.Printf("assetCtxs=%d err=%v\n", len(contexts), err)
	var i int
	for i = 0; i < len(contexts); i++ {
		if contexts[i].Coin == coin {
			fmt.Printf("  %s mark=%s oracle=%s mid=%s funding=%s oi=%s\n", coin,
				contexts[i].Ctx.MarkPx, contexts[i].Ctx.OraclePx, contexts[i].Ctx.MidPx, contexts[i].Ctx.Funding, contexts[i].Ctx.OpenInterest)
		}
	}

	var bboCount, bookCount, tradeCount, ctxCount atomic.Int64
	var firstBook atomic.Int64
	var onErr = func(e error) { fmt.Println("stream error:", e) }
	_ = perps.Stream().WatchBBO(ctx, coin, func(b *types.BBO) {
		if bboCount.Add(1) == 1 && b.Bid() != nil && b.Ask() != nil {
			fmt.Printf("first bbo: %s x %s | %s x %s\n", b.Bid().Price, b.Bid().Size, b.Ask().Price, b.Ask().Size)
		}
	}, onErr)
	_ = perps.Stream().WatchOrderbook(ctx, coin, perpetuals.OrderbookStreamOptions{Fast: true}, func(s *types.OrderBookSnapshot) {
		if bookCount.Add(1) == 1 {
			firstBook.Store(int64(len(s.Bids())))
		}
	}, nil, onErr)
	_ = perps.Stream().WatchTrades(ctx, coin, func(trades []types.Trade) { tradeCount.Add(int64(len(trades))) }, onErr)
	_ = perps.Stream().WatchAssetCtx(ctx, coin, func(*types.ActivePerpAssetCtx) { ctxCount.Add(1) }, onErr)

	time.Sleep(time.Duration(seconds) * time.Second)
	fmt.Printf("streams in %ds: bbo=%d fastBook=%d (levels/side=%d) trades=%d assetCtx=%d\n",
		seconds, bboCount.Load(), bookCount.Load(), firstBook.Load(), tradeCount.Load(), ctxCount.Load())
	fmt.Printf("SDK-side IP weight used: %d / %d\n", client.IPWeightUsed(), hyperliquid.IPWeightLimitPerMinute)
}
