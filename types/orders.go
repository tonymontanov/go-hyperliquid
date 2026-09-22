/*
FILE: types/orders.go

DESCRIPTION:
Order-related domain types shared by every section (layer 1): requests accepted
by Trading() sub-clients and order views returned by the exchange.

REQUESTS use native exchange vocabulary (coin, isBuy, tif, cloid) and Fixed
numerics — they sit on the order hot path and must not allocate.

VIEWS mirror the exchange JSON one-to-one (field names in the json tags are the
exchange's). Sources:
  - OpenOrder   : info "frontendOpenOrders" (superset of "openOrders");
  - OrderUpdate : WS "orderUpdates" (WsOrder / WsBasicOrder);
  - OrderState  : info "orderStatus".
Fields the exchange omits for a given order decode to their zero value.
*/

package types

// AssetInfo — static description of one tradable asset inside one section,
// built from exchange metadata.
type AssetInfo struct {
	// AssetID — numeric asset id used in actions. Section-specific encoding:
	// perpetuals: index in meta.universe; spot: 10000 + index in
	// spotMeta.universe; HIP-3: 100000 + dexIndex*10000 + index; outcomes:
	// 100000000 + 10*outcome + side. Differs between mainnet and testnet.
	AssetID uint32
	// Coin — exchange coin name used in info requests and streams ("BTC",
	// "kPEPE", "@107", "PURR/USDC", "xyz:XYZ100").
	Coin string
	// SzDecimals — size decimals.
	SzDecimals int
	// MaxLeverage — maximum leverage (margined sections; 0 otherwise).
	MaxLeverage int
	// OnlyIsolated — cross margin is not allowed for the asset.
	OnlyIsolated bool
	// IsDelisted — the asset is delisted and cannot be traded.
	IsDelisted bool
	// Precision — rounding rules of the asset (szDecimals + section MAX_DECIMALS).
	Precision Precision
}

// OrderRequest — one order to place. Exactly one of the two order kinds is
// described: a limit order (IsTrigger == false, TimeInForce set) or a trigger
// order (IsTrigger == true, TriggerPrice / TriggerIsMarket / Tpsl set).
//
// Hyperliquid has no native market order: a "market" order is an aggressive
// limit order with TimeInForceIoc (see MarketData().SlippagePrice helpers of
// the sections).
type OrderRequest struct {
	Coin       string
	IsBuy      bool
	Price      Fixed
	Size       Fixed
	ReduceOnly bool

	TimeInForce TimeInForce

	IsTrigger       bool
	TriggerPrice    Fixed
	TriggerIsMarket bool
	Tpsl            Tpsl

	// Cloid — optional client order id.
	Cloid Cloid
}

// BuilderInfo — optional builder code of an order action.
type BuilderInfo struct {
	// Address — builder address ("0x" + 40 hex).
	Address string
	// FeeTenthsBps — fee in tenths of a basis point: 10 = 1 bp.
	FeeTenthsBps uint32
}

// OrderOptions — options of a whole order action (all orders of one request).
type OrderOptions struct {
	// Grouping — "" is treated as GroupingNone.
	Grouping Grouping
	// Builder — optional builder code; nil → none.
	Builder *BuilderInfo
	// ExpiresAfterMs — the exchange rejects the action after this timestamp
	// (unix ms); 0 → unset. A stale expiresAfter costs 5x on the address-based
	// rate limit (docs).
	ExpiresAfterMs uint64
}

// ModifyRequest — replace an existing order, referenced by Oid or by Cloid
// (RefCloid wins when set), with Order.
type ModifyRequest struct {
	Oid      uint64
	RefCloid Cloid
	Order    OrderRequest
}

// ModifyOptions — options of a modify / batchModify action.
type ModifyOptions struct {
	// AlwaysPlace — "a" flag (docs-only): place the new order even if the
	// cancel of the old one failed. When false the exchange requires the new
	// order to be a non-trigger Alo order, or a non-executable Gtc order whose
	// tif it then overrides to Alo (official exchange-endpoint page).
	AlwaysPlace    bool
	ExpiresAfterMs uint64
}

// CancelRequest — cancel by exchange order id.
type CancelRequest struct {
	Coin string
	Oid  uint64
}

// CancelByCloidRequest — cancel by client order id.
type CancelByCloidRequest struct {
	Coin  string
	Cloid Cloid
}

// CancelOptions — options of a cancel / cancelByCloid action.
type CancelOptions struct {
	// Fast — "f" flag (docs-only). Rejected by the exchange for trigger
	// orders; "currently has no other effect", and after a future network
	// upgrade cancels will be prioritised iff it is set.
	Fast           bool
	ExpiresAfterMs uint64
}

// OpenOrder — one resting order (info "frontendOpenOrders").
type OpenOrder struct {
	Coin             string `json:"coin"`
	Side             Side   `json:"side"`
	LimitPx          Fixed  `json:"limitPx"`
	Sz               Fixed  `json:"sz"`
	OrigSz           Fixed  `json:"origSz"`
	Oid              uint64 `json:"oid"`
	TimestampMs      int64  `json:"timestamp"`
	Cloid            Cloid  `json:"cloid"`
	ReduceOnly       bool   `json:"reduceOnly"`
	OrderType        string `json:"orderType"`
	Tif              string `json:"tif"`
	IsTrigger        bool   `json:"isTrigger"`
	TriggerPx        Fixed  `json:"triggerPx"`
	TriggerCondition string `json:"triggerCondition"`
	IsPositionTpsl   bool   `json:"isPositionTpsl"`
}

// BasicOrder — order part of a WS order update (WsBasicOrder).
type BasicOrder struct {
	Coin        string `json:"coin"`
	Side        Side   `json:"side"`
	LimitPx     Fixed  `json:"limitPx"`
	Sz          Fixed  `json:"sz"`
	Oid         uint64 `json:"oid"`
	TimestampMs int64  `json:"timestamp"`
	OrigSz      Fixed  `json:"origSz"`
	Cloid       Cloid  `json:"cloid"`
}

// OrderUpdate — one element of a WS "orderUpdates" push (WsOrder). Status is
// the raw exchange status string ("open", "filled", "canceled", "triggered",
// "rejected", "marginCanceled", ... — see the orderStatus docs for the list).
type OrderUpdate struct {
	Order             BasicOrder `json:"order"`
	Status            string     `json:"status"`
	StatusTimestampMs int64      `json:"statusTimestamp"`
}

// Order status values most relevant for order tracking (raw exchange strings).
const (
	OrderStatusOpen      string = "open"
	OrderStatusFilled    string = "filled"
	OrderStatusCanceled  string = "canceled"
	OrderStatusTriggered string = "triggered"
	OrderStatusRejected  string = "rejected"
)

// OrderState — answer of info "orderStatus". Found is false when the exchange
// replied {"status":"unknownOid"}.
type OrderState struct {
	Found             bool
	Order             OpenOrder
	Status            string
	StatusTimestampMs int64
}
