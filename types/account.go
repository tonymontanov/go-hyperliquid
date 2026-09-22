/*
FILE: types/account.go

DESCRIPTION:
Account-side domain types shared by margined sections (layer 1). These are off
the hot path, so numerics are shopspring/decimal, like in the sibling SDKs.
Structs decode straight from the exchange JSON; json tags carry the exchange
field names. Sources: info "clearinghouseState", "userFills" /
"userFillsByTime" + WS "userFills" (WsFill), info "userRateLimit".

NULLABLE FIELDS:
entryPx and liquidationPx are null for some positions (e.g. no liquidation
price under cross margin with ample collateral). NullDecimal keeps that
distinction instead of collapsing null into 0.
*/

package types

import "github.com/shopspring/decimal"

// NullDecimal — decimal that may be null on the wire.
type NullDecimal = decimal.NullDecimal

// Leverage — leverage setting of a position.
type Leverage struct {
	// Type — "cross" or "isolated".
	Type  string `json:"type"`
	Value int    `json:"value"`
	// RawUsd — only for isolated leverage.
	RawUsd decimal.Decimal `json:"rawUsd"`
}

// Leverage types.
const (
	LeverageCross    string = "cross"
	LeverageIsolated string = "isolated"
)

// CumFunding — cumulative funding of a position.
type CumFunding struct {
	AllTime     decimal.Decimal `json:"allTime"`
	SinceChange decimal.Decimal `json:"sinceChange"`
	SinceOpen   decimal.Decimal `json:"sinceOpen"`
}

// Position — one open position. Szi is signed: positive long, negative short.
type Position struct {
	Coin           string          `json:"coin"`
	Szi            decimal.Decimal `json:"szi"`
	EntryPx        NullDecimal     `json:"entryPx"`
	LiquidationPx  NullDecimal     `json:"liquidationPx"`
	Leverage       Leverage        `json:"leverage"`
	MarginUsed     decimal.Decimal `json:"marginUsed"`
	MaxLeverage    int             `json:"maxLeverage"`
	PositionValue  decimal.Decimal `json:"positionValue"`
	ReturnOnEquity decimal.Decimal `json:"returnOnEquity"`
	UnrealizedPnl  decimal.Decimal `json:"unrealizedPnl"`
	CumFunding     CumFunding      `json:"cumFunding"`
}

// AssetPosition — wrapper used by the exchange; Type is "oneWay" (Hyperliquid
// has no hedge mode).
type AssetPosition struct {
	Position Position `json:"position"`
	Type     string   `json:"type"`
}

// MarginSummary — account-level margin figures.
type MarginSummary struct {
	AccountValue    decimal.Decimal `json:"accountValue"`
	TotalMarginUsed decimal.Decimal `json:"totalMarginUsed"`
	TotalNtlPos     decimal.Decimal `json:"totalNtlPos"`
	TotalRawUsd     decimal.Decimal `json:"totalRawUsd"`
}

// ClearinghouseState — perpetuals account state of one perp dex.
//
// Under the "unified account" and "portfolio margin" abstraction modes the
// docs state that balances and holds are shown in the SPOT clearinghouse
// state and "individual perp dex user states are not meaningful" for them;
// positions are still reported here.
type ClearinghouseState struct {
	AssetPositions             []AssetPosition `json:"assetPositions"`
	CrossMaintenanceMarginUsed decimal.Decimal `json:"crossMaintenanceMarginUsed"`
	CrossMarginSummary         MarginSummary   `json:"crossMarginSummary"`
	MarginSummary              MarginSummary   `json:"marginSummary"`
	Withdrawable               decimal.Decimal `json:"withdrawable"`
	TimeMs                     int64           `json:"time"`
}

// AccountStateUpdate — WS "clearinghouseState" push: an ABSOLUTE snapshot of the
// margin account of one perp dex (positions, margin summaries, withdrawable).
// The inner state carries no "time" field on this channel (TimeMs stays 0).
type AccountStateUpdate struct {
	Dex   string             `json:"dex"`
	User  string             `json:"user"`
	State ClearinghouseState `json:"clearinghouseState"`
}

// Fill — one execution of the user (WsFill / userFills element).
type Fill struct {
	Coin          string          `json:"coin"`
	Px            decimal.Decimal `json:"px"`
	Sz            decimal.Decimal `json:"sz"`
	Side          Side            `json:"side"`
	TimeMs        int64           `json:"time"`
	StartPosition decimal.Decimal `json:"startPosition"`
	// Dir — frontend display string ("Open Long", "Close Short", ...).
	Dir       string          `json:"dir"`
	ClosedPnl decimal.Decimal `json:"closedPnl"`
	Hash      string          `json:"hash"`
	Oid       uint64          `json:"oid"`
	// Crossed — true when the order crossed the spread (taker).
	Crossed bool `json:"crossed"`
	// Fee — negative means rebate. Includes BuilderFee.
	Fee        decimal.Decimal `json:"fee"`
	FeeToken   string          `json:"feeToken"`
	BuilderFee decimal.Decimal `json:"builderFee"`
	Tid        int64           `json:"tid"`
}

// UserFills — WS "userFills" push. The first push after (re)subscribing has
// IsSnapshot == true and replays recent history; it "can be ignored if the
// previous messages were already processed" (docs).
type UserFills struct {
	IsSnapshot bool   `json:"isSnapshot"`
	User       string `json:"user"`
	Fills      []Fill `json:"fills"`
}

// UserRateLimit — address-based rate-limit state (info "userRateLimit").
type UserRateLimit struct {
	CumVlm           decimal.Decimal `json:"cumVlm"`
	NRequestsUsed    int64           `json:"nRequestsUsed"`
	NRequestsCap     int64           `json:"nRequestsCap"`
	NRequestsSurplus int64           `json:"nRequestsSurplus"`
}
