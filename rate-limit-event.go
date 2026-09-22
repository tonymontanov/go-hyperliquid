/*
FILE: rate-limit-event.go

DESCRIPTION:
Public rate-limit accounting event.

Hyperliquid returns NO rate-limit headers, so — unlike the sibling SDKs, which
forward exchange headers — go-hyperliquid accounts for limits on its own side
and reports the result of that accounting:

  - IP-based limit: 1200 weight / minute. Every request carries its documented
    weight (info: 2 / 20 / 60 (+ per-item surcharge); action:
    1 + floor(batch / 40)); UsedWeight is the SDK's one-minute sliding window.
  - Address-based limit (actions only): OrderCount is the cost of the request
    (a batch of n costs n). The authoritative budget is read with the
    userRateLimit info request; see (Client).AddressBudget.

CONTRACT (same as the sibling SDKs):
The observer is called SYNCHRONOUSLY in the goroutine that executed the
request and blocks its return. Implementations must be O(1) — typically a
non-blocking send to a buffered channel. nil observer → zero overhead.

The SDK counts only its OWN requests. Other processes behind the same IP or
trading the same account consume the same exchange budgets invisibly.
*/

package hyperliquid

import (
	"github.com/tonymontanov/go-hyperliquid/internal/engine"
	"github.com/tonymontanov/go-hyperliquid/internal/ratelimit"
)

// RateLimitEvent — accounting event emitted after every request.
type RateLimitEvent = engine.Event

// Transport — how a request travels to the exchange.
type Transport = engine.Transport

// Transports.
const (
	// TransportREST — HTTP POST (default).
	TransportREST Transport = engine.TransportREST
	// TransportWS — WebSocket post request.
	TransportWS Transport = engine.TransportWS
)

// Rate-limit categories (RateLimitEvent.Category).
const (
	RateLimitCategoryPlace  string = engine.CategoryPlace
	RateLimitCategoryAmend  string = engine.CategoryAmend
	RateLimitCategoryCancel string = engine.CategoryCancel
	RateLimitCategoryQuery  string = engine.CategoryQuery
	RateLimitCategoryMarket string = engine.CategoryMarket
	RateLimitCategoryOther  string = engine.CategoryOther
)

// IPWeightLimitPerMinute — documented IP weight budget.
const IPWeightLimitPerMinute int64 = ratelimit.IPWeightLimitPerMinute

// AddressBudgetSnapshot — local view of the address-based request budget.
type AddressBudgetSnapshot = ratelimit.BudgetSnapshot
