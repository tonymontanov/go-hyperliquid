/*
FILE: internal/hlerr/errors.go

DESCRIPTION:
SDK error type + categories + Hyperliquid error mapping. Placed in an internal
package so that any internal/* package (rest, ws, engine) can use it without an
import cycle on the root hyperliquid package. The root package re-exports these
entities via type aliases.

HYPERLIQUID SPECIFICS:
Hyperliquid has NO numeric error codes. Failures arrive in three shapes:
 1. HTTP status != 200 with a plain-text (or empty) body — transport-level
    rejections: 429 (IP rate limit), 422 (body failed to deserialize), 5xx.
 2. HTTP 200 with {"status":"err","response":"<text>"} — the whole action was
    rejected by pre-validation (bad signature, nonce, rate limit, empty batch…).
 3. HTTP 200 with {"status":"ok",...,"statuses":[{"error":"<text>"}]} — a
    per-order / per-cancel rejection inside a batch.

Because the only machine-readable signal is the error text, the SDK classifies
messages into a small Reason enum using the strings published on the official
"Error responses" page. The original text is always preserved in Message.

See also: errors.go in the root (re-export).
*/

package hlerr

import (
	"errors"
	"fmt"
	"strings"
)

// ErrorKind — SDK error category.
type ErrorKind uint8

const (
	// ErrorKindUnknown — fallback when the SDK could not classify the failure.
	ErrorKindUnknown ErrorKind = iota
	// ErrorKindNetwork — transport-level failures (timeout, conn reset, DNS,
	// ctx cancelled, EOF on WS, 5xx). The caller may retry with backoff.
	ErrorKindNetwork
	// ErrorKindRateLimit — IP weight limit (HTTP 429) or address-based limit.
	ErrorKindRateLimit
	// ErrorKindAuth — signature / signer / account problems. Not retryable.
	ErrorKindAuth
	// ErrorKindInvalidRequest — the request is malformed or violates exchange
	// rules that are a deterministic function of the payload. Not retryable.
	ErrorKindInvalidRequest
	// ErrorKindExchange — the exchange processed the request and rejected it
	// for a state-dependent reason (margin, liquidity, missing order…).
	ErrorKindExchange
)

// String — human-readable category name.
func (k ErrorKind) String() string {
	switch k {
	case ErrorKindNetwork:
		return "network"
	case ErrorKindRateLimit:
		return "rate_limit"
	case ErrorKindAuth:
		return "auth"
	case ErrorKindInvalidRequest:
		return "invalid_request"
	case ErrorKindExchange:
		return "exchange"
	default:
		return "unknown"
	}
}

// Reason — fine-grained rejection reason derived from the exchange error text.
// Hyperliquid publishes no numeric codes, so Reason plays the role that the
// exchange code plays in sibling SDKs.
type Reason uint8

const (
	// ReasonNone — no specific reason recognised.
	ReasonNone Reason = iota
	// ReasonTick — "Price must be divisible by tick size."
	ReasonTick
	// ReasonMinTradeNtl — "Order must have minimum value of $10." (perps) or
	// "... of 10 {quote_token}." (spot).
	ReasonMinTradeNtl
	// ReasonPerpMargin — "Insufficient margin to place order."
	ReasonPerpMargin
	// ReasonReduceOnly — "Reduce only order would increase position."
	ReasonReduceOnly
	// ReasonBadAloPx — "Post only order would have immediately matched, ...".
	ReasonBadAloPx
	// ReasonIocCancel — "Order could not immediately match against any resting orders."
	ReasonIocCancel
	// ReasonBadTriggerPx — "Invalid TP/SL price."
	ReasonBadTriggerPx
	// ReasonMarketOrderNoLiquidity — "No liquidity available for market order."
	ReasonMarketOrderNoLiquidity
	// ReasonOpenInterestCap — any of the open-interest cap rejections.
	ReasonOpenInterestCap
	// ReasonInsufficientSpotBalance — "(Spot-only) Order has insufficient spot balance to trade".
	ReasonInsufficientSpotBalance
	// ReasonOracle — "Order price too far from oracle".
	ReasonOracle
	// ReasonPerpMaxPosition — "Order would cause position to exceed margin tier limit ...".
	ReasonPerpMaxPosition
	// ReasonMissingOrder — "Order was never placed, already canceled, or filled."
	ReasonMissingOrder
	// ReasonSignerUnknown — "User or API Wallet 0x... does not exist." Almost
	// always means the signature recovered to a wrong address (bad signing
	// payload) or the API wallet is not approved / was pruned.
	ReasonSignerUnknown
	// ReasonMustDeposit — "Must deposit before performing actions."
	ReasonMustDeposit
	// ReasonExpired — the action arrived after its expiresAfter timestamp.
	ReasonExpired
	// ReasonNonce — the nonce is outside the allowed window or already used.
	ReasonNonce
)

// String — stable machine-friendly reason name (used in logs and metrics labels).
func (r Reason) String() string {
	switch r {
	case ReasonTick:
		return "tick"
	case ReasonMinTradeNtl:
		return "min_trade_ntl"
	case ReasonPerpMargin:
		return "perp_margin"
	case ReasonReduceOnly:
		return "reduce_only"
	case ReasonBadAloPx:
		return "bad_alo_px"
	case ReasonIocCancel:
		return "ioc_cancel"
	case ReasonBadTriggerPx:
		return "bad_trigger_px"
	case ReasonMarketOrderNoLiquidity:
		return "market_order_no_liquidity"
	case ReasonOpenInterestCap:
		return "open_interest_cap"
	case ReasonInsufficientSpotBalance:
		return "insufficient_spot_balance"
	case ReasonOracle:
		return "oracle"
	case ReasonPerpMaxPosition:
		return "perp_max_position"
	case ReasonMissingOrder:
		return "missing_order"
	case ReasonSignerUnknown:
		return "signer_unknown"
	case ReasonMustDeposit:
		return "must_deposit"
	case ReasonExpired:
		return "expired"
	case ReasonNonce:
		return "nonce"
	default:
		return "none"
	}
}

// Error — unified SDK error type.
type Error struct {
	Kind       ErrorKind
	HTTPStatus int
	// Reason — classified exchange rejection reason; ReasonNone when the text
	// was not recognised or the error did not come from the exchange.
	Reason Reason
	// Message — SDK message ("<subclient>.<Method>: <detail>") or the verbatim
	// exchange error text.
	Message string
	Cause   error
}

// Error implements the error interface.
func (e *Error) Error() string {
	switch {
	case e.Reason != ReasonNone && e.Cause != nil:
		return fmt.Sprintf("hyperliquid %s: reason=%s status=%d msg=%q: %v", e.Kind, e.Reason, e.HTTPStatus, e.Message, e.Cause)
	case e.Reason != ReasonNone:
		return fmt.Sprintf("hyperliquid %s: reason=%s status=%d msg=%q", e.Kind, e.Reason, e.HTTPStatus, e.Message)
	case e.Cause != nil:
		return fmt.Sprintf("hyperliquid %s: status=%d msg=%q: %v", e.Kind, e.HTTPStatus, e.Message, e.Cause)
	default:
		return fmt.Sprintf("hyperliquid %s: status=%d msg=%q", e.Kind, e.HTTPStatus, e.Message)
	}
}

// Unwrap — for errors.Is/As.
func (e *Error) Unwrap() error { return e.Cause }

// New creates a *Error without an exchange reason.
func New(kind ErrorKind, msg string, cause error) *Error {
	return &Error{Kind: kind, Message: msg, Cause: cause}
}

// FromExchangeText builds a *Error from a verbatim exchange error text
// (top-level {"status":"err"} response or a per-order {"error": ...} status).
func FromExchangeText(text string) *Error {
	var kind ErrorKind
	var reason Reason
	kind, reason = MapExchangeText(text)
	return &Error{Kind: kind, Reason: reason, HTTPStatus: 200, Message: text}
}

// IsNetwork returns true if err has category Network.
func IsNetwork(err error) bool { return matchKind(err, ErrorKindNetwork) }

// IsRateLimit returns true if err has category RateLimit.
func IsRateLimit(err error) bool { return matchKind(err, ErrorKindRateLimit) }

// IsAuth returns true if err has category Auth.
func IsAuth(err error) bool { return matchKind(err, ErrorKindAuth) }

// IsInvalidRequest returns true if err has category InvalidRequest.
func IsInvalidRequest(err error) bool { return matchKind(err, ErrorKindInvalidRequest) }

// IsExchange returns true if err has category Exchange.
func IsExchange(err error) bool { return matchKind(err, ErrorKindExchange) }

// IsReason returns true if err carries the given exchange rejection reason.
func IsReason(err error, reason Reason) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Reason == reason
	}
	return false
}

// IsMissingOrder returns true if a cancel / modify was rejected because the
// order is already gone ("Order was never placed, already canceled, or
// filled."). Callers usually treat this as a benign outcome.
func IsMissingOrder(err error) bool { return IsReason(err, ReasonMissingOrder) }

func matchKind(err error, kind ErrorKind) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind == kind
	}
	return false
}

// textRule — one substring → classification rule. Rules are checked in order.
type textRule struct {
	needle string
	kind   ErrorKind
	reason Reason
}

// exchangeTextRules — classification table. Needles for the per-order errors
// are taken verbatim from the official "Error responses" page; the top-level
// needles come from the "Signing" page and from the rate-limit page. Matching
// is case-insensitive on a lowered copy of the text.
var exchangeTextRules []textRule = []textRule{
	{"divisible by tick size", ErrorKindInvalidRequest, ReasonTick},
	{"must have minimum value", ErrorKindInvalidRequest, ReasonMinTradeNtl},
	{"invalid tp/sl price", ErrorKindInvalidRequest, ReasonBadTriggerPx},
	{"insufficient margin", ErrorKindExchange, ReasonPerpMargin},
	{"reduce only order would increase position", ErrorKindExchange, ReasonReduceOnly},
	{"post only order would have immediately matched", ErrorKindExchange, ReasonBadAloPx},
	{"could not immediately match", ErrorKindExchange, ReasonIocCancel},
	{"no liquidity available for market order", ErrorKindExchange, ReasonMarketOrderNoLiquidity},
	{"open interest", ErrorKindExchange, ReasonOpenInterestCap},
	{"insufficient spot balance", ErrorKindExchange, ReasonInsufficientSpotBalance},
	{"too far from oracle", ErrorKindExchange, ReasonOracle},
	{"exceed margin tier limit", ErrorKindExchange, ReasonPerpMaxPosition},
	{"never placed, already canceled, or filled", ErrorKindExchange, ReasonMissingOrder},
	{"user or api wallet", ErrorKindAuth, ReasonSignerUnknown},
	{"must deposit before performing actions", ErrorKindAuth, ReasonMustDeposit},
	{"already expired", ErrorKindInvalidRequest, ReasonExpired},
	{"nonce", ErrorKindInvalidRequest, ReasonNonce},
	{"too many cumulative requests", ErrorKindRateLimit, ReasonNone},
	{"rate limit", ErrorKindRateLimit, ReasonNone},
}

/*
MapExchangeText classifies a verbatim exchange error text.

Unknown texts map to (ErrorKindExchange, ReasonNone): the exchange answered,
so the failure is not a transport problem, but the SDK cannot say more.
*/
func MapExchangeText(text string) (ErrorKind, Reason) {
	if text == "" {
		return ErrorKindUnknown, ReasonNone
	}
	var lowered string = strings.ToLower(text)
	var i int
	for i = 0; i < len(exchangeTextRules); i++ {
		if strings.Contains(lowered, exchangeTextRules[i].needle) {
			return exchangeTextRules[i].kind, exchangeTextRules[i].reason
		}
	}
	return ErrorKindExchange, ReasonNone
}

// MapHTTPStatus returns the SDK error category for an HTTP status code (when
// the response is not a JSON action/info envelope).
func MapHTTPStatus(status int) ErrorKind {
	switch {
	case status == 429:
		return ErrorKindRateLimit
	case status == 401 || status == 403:
		return ErrorKindAuth
	case status >= 500:
		return ErrorKindNetwork
	case status >= 400:
		return ErrorKindInvalidRequest
	default:
		return ErrorKindUnknown
	}
}
