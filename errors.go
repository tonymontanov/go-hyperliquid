/*
FILE: errors.go

DESCRIPTION:
Public re-export of the SDK error model. The implementation lives in
internal/hlerr so that every internal package can use it without importing the
root package (import cycle). Type ALIASES keep errors.As / errors.Is working
across the boundary: hyperliquid.Error and hlerr.Error are the same type.

Hyperliquid publishes no numeric error codes. The SDK classifies the exchange
error TEXT into Reason values (see internal/hlerr for the table, taken from the
official "Error responses" page); the verbatim text stays in Error.Message.

USAGE:

	var err error = trading.CancelOrder(ctx, req)
	if hyperliquid.IsMissingOrder(err) {
		// the order is already gone — usually a benign outcome
	}
*/

package hyperliquid

import "github.com/tonymontanov/go-hyperliquid/internal/hlerr"

// Error — unified SDK error type.
type Error = hlerr.Error

// ErrorKind — SDK error category.
type ErrorKind = hlerr.ErrorKind

// Reason — fine-grained exchange rejection reason derived from the error text.
type Reason = hlerr.Reason

// Error categories.
const (
	ErrorKindUnknown        ErrorKind = hlerr.ErrorKindUnknown
	ErrorKindNetwork        ErrorKind = hlerr.ErrorKindNetwork
	ErrorKindRateLimit      ErrorKind = hlerr.ErrorKindRateLimit
	ErrorKindAuth           ErrorKind = hlerr.ErrorKindAuth
	ErrorKindInvalidRequest ErrorKind = hlerr.ErrorKindInvalidRequest
	ErrorKindExchange       ErrorKind = hlerr.ErrorKindExchange
)

// Exchange rejection reasons.
const (
	ReasonNone                    Reason = hlerr.ReasonNone
	ReasonTick                    Reason = hlerr.ReasonTick
	ReasonMinTradeNtl             Reason = hlerr.ReasonMinTradeNtl
	ReasonPerpMargin              Reason = hlerr.ReasonPerpMargin
	ReasonReduceOnly              Reason = hlerr.ReasonReduceOnly
	ReasonBadAloPx                Reason = hlerr.ReasonBadAloPx
	ReasonIocCancel               Reason = hlerr.ReasonIocCancel
	ReasonBadTriggerPx            Reason = hlerr.ReasonBadTriggerPx
	ReasonMarketOrderNoLiquidity  Reason = hlerr.ReasonMarketOrderNoLiquidity
	ReasonOpenInterestCap         Reason = hlerr.ReasonOpenInterestCap
	ReasonInsufficientSpotBalance Reason = hlerr.ReasonInsufficientSpotBalance
	ReasonOracle                  Reason = hlerr.ReasonOracle
	ReasonPerpMaxPosition         Reason = hlerr.ReasonPerpMaxPosition
	ReasonMissingOrder            Reason = hlerr.ReasonMissingOrder
	ReasonSignerUnknown           Reason = hlerr.ReasonSignerUnknown
	ReasonMustDeposit             Reason = hlerr.ReasonMustDeposit
	ReasonExpired                 Reason = hlerr.ReasonExpired
	ReasonNonce                   Reason = hlerr.ReasonNonce
)

// NewError creates an SDK error without an exchange reason.
func NewError(kind ErrorKind, msg string, cause error) *Error {
	return hlerr.New(kind, msg, cause)
}

// IsNetwork reports whether err is a network-class error.
func IsNetwork(err error) bool { return hlerr.IsNetwork(err) }

// IsRateLimit reports whether err is a rate-limit error.
func IsRateLimit(err error) bool { return hlerr.IsRateLimit(err) }

// IsAuth reports whether err is an authentication / signer error.
func IsAuth(err error) bool { return hlerr.IsAuth(err) }

// IsInvalidRequest reports whether err is a request-validation error.
func IsInvalidRequest(err error) bool { return hlerr.IsInvalidRequest(err) }

// IsExchange reports whether err is a state-dependent exchange rejection.
func IsExchange(err error) bool { return hlerr.IsExchange(err) }

// IsReason reports whether err carries the given exchange rejection reason.
func IsReason(err error, reason Reason) bool { return hlerr.IsReason(err, reason) }

// IsMissingOrder reports whether a cancel / modify failed because the order
// "was never placed, already canceled, or filled".
func IsMissingOrder(err error) bool { return hlerr.IsMissingOrder(err) }
