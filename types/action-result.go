/*
FILE: types/action-result.go

DESCRIPTION:
Typed results of exchange actions (layer 1, shared by every section).

EXCHANGE RESPONSE SHAPES:
  {"status":"ok","response":{"type":"order","data":{"statuses":[<status>...]}}}
  {"status":"ok","response":{"type":"cancel","data":{"statuses":[<status>...]}}}
  {"status":"ok","response":{"type":"default"}}
  {"status":"err","response":"<text>"}          — whole action rejected

where <status> is one of
  {"resting":{"oid":77738308,"cloid":"0x..."}}
  {"filled":{"totalSz":"0.02","avgPx":"1891.4","oid":77747314,"cloid":"0x..."}}
  {"error":"Order must have minimum value of $10."}
  "success"                                      — a cancel went through
  "waitingForFill" / "waitingForTrigger"         — children of TP/SL groupings

The last two string statuses are NOT described on the exchange-endpoint page;
they are handled because the grouping feature produces them (see also the
order status list of the orderStatus info request).

A batch answer always has one status per request item, in request order. A
pre-validation failure rejects the WHOLE action with {"status":"err"}; the SDK
returns it as an error of the call instead of fabricating per-item statuses.
*/

package types

// ActionStatusKind — discriminator of ActionStatus.
type ActionStatusKind uint8

const (
	// ActionStatusUnknown — a status shape the SDK does not recognise; Raw
	// holds the original JSON.
	ActionStatusUnknown ActionStatusKind = iota
	// ActionStatusResting — the order rests in the book.
	ActionStatusResting
	// ActionStatusFilled — the order was (at least partly) filled immediately.
	ActionStatusFilled
	// ActionStatusSuccess — "success": the cancel was applied.
	ActionStatusSuccess
	// ActionStatusWaitingForFill — TP/SL child waiting for the parent fill.
	ActionStatusWaitingForFill
	// ActionStatusWaitingForTrigger — trigger order accepted, not triggered yet.
	ActionStatusWaitingForTrigger
	// ActionStatusError — the item was rejected; Err holds the classified error.
	ActionStatusError
)

// String returns a stable name of the kind.
func (k ActionStatusKind) String() string {
	switch k {
	case ActionStatusResting:
		return "resting"
	case ActionStatusFilled:
		return "filled"
	case ActionStatusSuccess:
		return "success"
	case ActionStatusWaitingForFill:
		return "waitingForFill"
	case ActionStatusWaitingForTrigger:
		return "waitingForTrigger"
	case ActionStatusError:
		return "error"
	default:
		return "unknown"
	}
}

// ActionStatus — outcome of ONE item of an action (one order, one cancel, one
// modify).
type ActionStatus struct {
	Kind ActionStatusKind
	// Oid — exchange order id (resting / filled).
	Oid uint64
	// Cloid — client order id echoed by the exchange, when the order had one.
	Cloid Cloid
	// TotalSz / AvgPx — immediate execution (filled only).
	TotalSz Fixed
	AvgPx   Fixed
	// Err — classified rejection (Kind == ActionStatusError). The concrete
	// type is the SDK error (*hyperliquid.Error); use errors.As or the
	// hyperliquid.Is* helpers.
	Err error
	// Raw — original JSON of an unrecognised status.
	Raw string
}

// Accepted reports whether the exchange accepted the item (any non-error,
// recognised status).
func (s ActionStatus) Accepted() bool {
	return s.Kind != ActionStatusError && s.Kind != ActionStatusUnknown
}
