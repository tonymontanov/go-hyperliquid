/*
FILE: types/market.go

DESCRIPTION:
Market-data domain types shared by every section (layer 1). They sit on the
read hot path (WS pushes), so all prices and sizes are Fixed and the structs
decode straight from the exchange JSON (json tags carry the exchange names).

ORDER BOOK MODEL — IMPORTANT:
The public Hyperliquid book feed is SNAPSHOT-ONLY. Both the info "l2Book"
request and the WS "l2Book" subscription return a full top-of-book snapshot
(at most 20 levels per side; 5 with fast:true over WS) — there are no deltas,
no sequence numbers and no checksum ("Snapshot feed, pushed on each block that
is at least 0.5 since last push"). Gap detection / resync, as specified for
delta feeds, does not apply; consumers detect staleness by TimeMs.

Sources: l2Book (WsBook / WsLevel), bbo (WsBbo), trades (WsTrade), candle /
candleSnapshot (Candle), activeAssetCtx + metaAndAssetCtxs (PerpsAssetCtx).
The docs' TypeScript definitions type several numerics as `number`, but the
wire carries strings — Fixed.UnmarshalJSON accepts both.
*/

package types

import "github.com/shopspring/decimal"

// OrderBookLevel — one aggregated price level (WsLevel).
type OrderBookLevel struct {
	Price Fixed `json:"px"`
	Size  Fixed `json:"sz"`
	// Orders — number of orders at the level ("n").
	Orders int `json:"n"`
}

// OrderBookSnapshot — full book snapshot. Levels[0] are bids (best first),
// Levels[1] are asks (best first), exactly as sent by the exchange.
type OrderBookSnapshot struct {
	Coin   string              `json:"coin"`
	TimeMs int64               `json:"time"`
	Levels [2][]OrderBookLevel `json:"levels"`
}

// Bids returns the bid side, best (highest) price first.
func (s *OrderBookSnapshot) Bids() []OrderBookLevel { return s.Levels[0] }

// Asks returns the ask side, best (lowest) price first.
func (s *OrderBookSnapshot) Asks() []OrderBookLevel { return s.Levels[1] }

// BBO — best bid and offer (WsBbo). A side is nil when the book side is empty.
type BBO struct {
	Coin   string             `json:"coin"`
	TimeMs int64              `json:"time"`
	Levels [2]*OrderBookLevel `json:"bbo"`
}

// Bid returns the best bid or nil.
func (b *BBO) Bid() *OrderBookLevel { return b.Levels[0] }

// Ask returns the best ask or nil.
func (b *BBO) Ask() *OrderBookLevel { return b.Levels[1] }

// Trade — one public trade (WsTrade). Tid is a 50-bit hash of the two order
// ids; (TimeMs, Coin, Tid) is globally unique.
type Trade struct {
	Coin   string    `json:"coin"`
	Side   Side      `json:"side"`
	Price  Fixed     `json:"px"`
	Size   Fixed     `json:"sz"`
	TimeMs int64     `json:"time"`
	Hash   string    `json:"hash"`
	Tid    int64     `json:"tid"`
	Users  [2]string `json:"users"` // [buyer, seller]
}

// Candle — one OHLCV candle (WS "candle" push and info "candleSnapshot").
type Candle struct {
	OpenTimeMs  int64  `json:"t"`
	CloseTimeMs int64  `json:"T"`
	Coin        string `json:"s"`
	Interval    string `json:"i"`
	Open        Fixed  `json:"o"`
	Close       Fixed  `json:"c"`
	High        Fixed  `json:"h"`
	Low         Fixed  `json:"l"`
	// Volume — base-asset volume. decimal: a day of a low-priced coin can
	// exceed the Fixed range.
	Volume decimal.Decimal `json:"v"`
	Trades int64           `json:"n"`
}

// Candle intervals accepted by the exchange.
const (
	Interval1m  string = "1m"
	Interval3m  string = "3m"
	Interval5m  string = "5m"
	Interval15m string = "15m"
	Interval30m string = "30m"
	Interval1h  string = "1h"
	Interval2h  string = "2h"
	Interval4h  string = "4h"
	Interval8h  string = "8h"
	Interval12h string = "12h"
	Interval1d  string = "1d"
	Interval3d  string = "3d"
	Interval1w  string = "1w"
	Interval1M  string = "1M"
)

// PerpAssetCtx — live context of a perpetual asset (metaAndAssetCtxs element,
// WS activeAssetCtx.ctx). MidPx, Premium and ImpactPxs are null on the wire
// when the book is empty; they decode to zero values.
//
// Price-like fields are Fixed. Funding / Premium are sent with up to 10
// decimals and are truncated to 8 (1e-8 of a rate is far below anything
// tradable). Volumes and open interest are decimal: they are large, carry
// more than 8 decimals and may exceed the Fixed range for low-priced coins.
type PerpAssetCtx struct {
	Funding      Fixed           `json:"funding"`
	OpenInterest decimal.Decimal `json:"openInterest"`
	PrevDayPx    Fixed           `json:"prevDayPx"`
	DayNtlVlm    decimal.Decimal `json:"dayNtlVlm"`
	Premium      Fixed           `json:"premium"`
	OraclePx     Fixed           `json:"oraclePx"`
	MarkPx       Fixed           `json:"markPx"`
	MidPx        Fixed           `json:"midPx"`
	ImpactPxs    []Fixed         `json:"impactPxs"`
	DayBaseVlm   decimal.Decimal `json:"dayBaseVlm"`
}

// ActivePerpAssetCtx — WS "activeAssetCtx" push of a perpetual asset.
type ActivePerpAssetCtx struct {
	Coin string       `json:"coin"`
	Ctx  PerpAssetCtx `json:"ctx"`
}
