/*
FILE: types/enums.go

DESCRIPTION:
Protocol-level enums shared by every section (layer 1). Values are the exact
wire strings of the Hyperliquid API — no translation layer, so a value read
from the exchange can be compared with these constants directly.

MAIN TYPES:
  - Side        : "B" (bid / buy) and "A" (ask / sell), as sent in fills,
                  trades and open orders.
  - TimeInForce : "Alo" (add liquidity only = post-only), "Ioc", "Gtc".
  - Tpsl        : "tp" / "sl" for trigger orders.
  - Grouping    : "na", "normalTpsl", "positionTpsl".
*/

package types

// Side — order / trade side as sent by the exchange.
type Side string

const (
	// SideBuy — "B": bid side (buy).
	SideBuy Side = "B"
	// SideSell — "A": ask side (sell).
	SideSell Side = "A"
)

// String returns the wire value.
func (s Side) String() string { return string(s) }

// IsBuy reports whether the side is the bid side.
func (s Side) IsBuy() bool { return s == SideBuy }

// SideFromIsBuy converts the boolean used by order actions to a Side.
func SideFromIsBuy(isBuy bool) Side {
	if isBuy {
		return SideBuy
	}
	return SideSell
}

// TimeInForce — limit order time in force.
type TimeInForce string

const (
	// TimeInForceAlo — add liquidity only (post-only). Rejected if it would
	// match immediately.
	TimeInForceAlo TimeInForce = "Alo"
	// TimeInForceIoc — immediate or cancel. The unfilled part is cancelled.
	TimeInForceIoc TimeInForce = "Ioc"
	// TimeInForceGtc — good till cancelled.
	TimeInForceGtc TimeInForce = "Gtc"
)

// String returns the wire value.
func (t TimeInForce) String() string { return string(t) }

// Valid reports whether t is one of the three exchange values.
func (t TimeInForce) Valid() bool {
	return t == TimeInForceAlo || t == TimeInForceIoc || t == TimeInForceGtc
}

// Tpsl — trigger order flavour.
type Tpsl string

const (
	// TpslTakeProfit — "tp".
	TpslTakeProfit Tpsl = "tp"
	// TpslStopLoss — "sl".
	TpslStopLoss Tpsl = "sl"
)

// String returns the wire value.
func (t Tpsl) String() string { return string(t) }

// Valid reports whether t is "tp" or "sl".
func (t Tpsl) Valid() bool { return t == TpslTakeProfit || t == TpslStopLoss }

// Grouping — order grouping of an "order" action.
type Grouping string

const (
	// GroupingNone — "na": independent orders.
	GroupingNone Grouping = "na"
	// GroupingNormalTpsl — "normalTpsl": parent order with attached TP/SL.
	GroupingNormalTpsl Grouping = "normalTpsl"
	// GroupingPositionTpsl — "positionTpsl": TP/SL bound to the whole position.
	GroupingPositionTpsl Grouping = "positionTpsl"
)

// String returns the wire value.
func (g Grouping) String() string { return string(g) }

// Valid reports whether g is one of the three exchange values.
func (g Grouping) Valid() bool {
	return g == GroupingNone || g == GroupingNormalTpsl || g == GroupingPositionTpsl
}
