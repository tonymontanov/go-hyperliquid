/*
FILE: internal/hlerr/errors_test.go

DESCRIPTION:
Contract test of the exchange error-text classification. The texts are copied
VERBATIM from the official "Error responses" and "Signing" pages — if the
exchange rewords them, this table is where the change becomes visible.
*/

package hlerr

import (
	"errors"
	"fmt"
	"testing"
)

func TestMapExchangeText(t *testing.T) {
	var cases = []struct {
		text   string
		kind   ErrorKind
		reason Reason
	}{
		{"Price must be divisible by tick size.", ErrorKindInvalidRequest, ReasonTick},
		{"Order must have minimum value of $10.", ErrorKindInvalidRequest, ReasonMinTradeNtl},
		{"Order must have minimum value of 10 USDC.", ErrorKindInvalidRequest, ReasonMinTradeNtl},
		{"Insufficient margin to place order.", ErrorKindExchange, ReasonPerpMargin},
		{"Reduce only order would increase position.", ErrorKindExchange, ReasonReduceOnly},
		{"Post only order would have immediately matched, bbo was 86759@86760.", ErrorKindExchange, ReasonBadAloPx},
		{"Order could not immediately match against any resting orders.", ErrorKindExchange, ReasonIocCancel},
		{"Invalid TP/SL price.", ErrorKindInvalidRequest, ReasonBadTriggerPx},
		{"No liquidity available for market order.", ErrorKindExchange, ReasonMarketOrderNoLiquidity},
		{"Order would increase open interest while open interest is capped", ErrorKindExchange, ReasonOpenInterestCap},
		{"Order rejected due to price more aggressive than oracle while at open interest cap", ErrorKindExchange, ReasonOpenInterestCap},
		{"Order would increase open interest too quickly", ErrorKindExchange, ReasonOpenInterestCap},
		{"(Spot-only) Order has insufficient spot balance to trade", ErrorKindExchange, ReasonInsufficientSpotBalance},
		{"Order price too far from oracle", ErrorKindExchange, ReasonOracle},
		{"Order would cause position to exceed margin tier limit at current leverage", ErrorKindExchange, ReasonPerpMaxPosition},
		{"Order was never placed, already canceled, or filled.", ErrorKindExchange, ReasonMissingOrder},
		{"L1 error: User or API Wallet 0x0123456789012345678901234567890123456789 does not exist.", ErrorKindAuth, ReasonSignerUnknown},
		{"Must deposit before performing actions. User: 0x0123456789012345678901234567890123456789", ErrorKindAuth, ReasonMustDeposit},
		{"Something the SDK has never seen", ErrorKindExchange, ReasonNone},
		{"", ErrorKindUnknown, ReasonNone},
	}
	for _, c := range cases {
		var kind, reason = MapExchangeText(c.text)
		if kind != c.kind || reason != c.reason {
			t.Errorf("%q → (%s, %s), want (%s, %s)", c.text, kind, reason, c.kind, c.reason)
		}
	}
}

func TestErrorHelpers(t *testing.T) {
	var cause = errors.New("boom")
	var err error = fmt.Errorf("wrapped: %w", New(ErrorKindNetwork, "rest: /info: request failed", cause))
	if !IsNetwork(err) || IsRateLimit(err) || !errors.Is(err, cause) {
		t.Fatalf("kind helpers / unwrap broken: %v", err)
	}
	var missing error = FromExchangeText("Order was never placed, already canceled, or filled.")
	if !IsMissingOrder(missing) || !IsExchange(missing) || IsMissingOrder(err) {
		t.Fatalf("IsMissingOrder broken: %v", missing)
	}
	var e *Error
	if !errors.As(missing, &e) || e.HTTPStatus != 200 || e.Message == "" {
		t.Fatalf("exchange error must keep the verbatim text: %+v", e)
	}
	for status, want := range map[int]ErrorKind{429: ErrorKindRateLimit, 401: ErrorKindAuth, 403: ErrorKindAuth, 500: ErrorKindNetwork, 502: ErrorKindNetwork, 422: ErrorKindInvalidRequest, 200: ErrorKindUnknown} {
		if got := MapHTTPStatus(status); got != want {
			t.Errorf("MapHTTPStatus(%d) = %s, want %s", status, got, want)
		}
	}
}
