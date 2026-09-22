/*
FILE: internal/engine/decode.go

DESCRIPTION:
Decoding of /exchange responses into typed results (see types/action-result.go
for the documented shapes).

  {"status":"ok","response":{"type":"order","data":{"statuses":[...]}}}
  {"status":"ok","response":{"type":"default"}}
  {"status":"err","response":"<text>"}

Status elements are heterogeneous — a JSON string ("success",
"waitingForFill", "waitingForTrigger") or a single-key object (resting /
filled / error) — so each element is first captured as raw JSON and then
decoded by its first byte.
*/

package engine

import (
	"github.com/tonymontanov/go-hyperliquid/internal/codec"
	"github.com/tonymontanov/go-hyperliquid/internal/hlerr"
	"github.com/tonymontanov/go-hyperliquid/types"
)

// actionEnvelope — outer shape of an /exchange response.
type actionEnvelope struct {
	Status   string           `json:"status"`
	Response codec.RawMessage `json:"response"`
}

// actionResponse — "response" of a successful action.
type actionResponse struct {
	Type string `json:"type"`
	Data struct {
		Statuses []codec.RawMessage `json:"statuses"`
	} `json:"data"`
}

// statusObject — object form of one status element.
type statusObject struct {
	Resting *struct {
		Oid   uint64      `json:"oid"`
		Cloid types.Cloid `json:"cloid"`
	} `json:"resting"`
	Filled *struct {
		TotalSz types.Fixed `json:"totalSz"`
		AvgPx   types.Fixed `json:"avgPx"`
		Oid     uint64      `json:"oid"`
		Cloid   types.Cloid `json:"cloid"`
	} `json:"filled"`
	Error *string `json:"error"`
}

// decodeActionResponse fills result from a raw /exchange response body.
func decodeActionResponse(payload []byte, result *ActionResult) error {
	var env actionEnvelope
	var err error = codec.Unmarshal(payload, &env)
	if err != nil {
		return hlerr.New(hlerr.ErrorKindUnknown, "action: parse response envelope", err)
	}

	if env.Status != "ok" {
		// {"status":"err","response":"<text>"} — the whole action was rejected.
		var text string
		if unmarshalErr := codec.Unmarshal(env.Response, &text); unmarshalErr != nil {
			text = string(env.Response)
		}
		return hlerr.FromExchangeText(text)
	}

	var response actionResponse
	err = codec.Unmarshal(env.Response, &response)
	if err != nil {
		return hlerr.New(hlerr.ErrorKindUnknown, "action: parse response body", err)
	}
	result.ResponseType = response.Type
	if len(response.Data.Statuses) == 0 {
		return nil
	}

	result.Statuses = make([]types.ActionStatus, len(response.Data.Statuses))
	var i int
	for i = 0; i < len(response.Data.Statuses); i++ {
		result.Statuses[i] = decodeStatus(response.Data.Statuses[i])
	}
	return nil
}

// decodeStatus decodes one element of "statuses".
func decodeStatus(raw []byte) types.ActionStatus {
	var status types.ActionStatus
	if len(raw) == 0 {
		return status
	}

	if raw[0] == '"' {
		var text string
		if err := codec.Unmarshal(raw, &text); err != nil {
			status.Raw = string(raw)
			return status
		}
		switch text {
		case "success":
			status.Kind = types.ActionStatusSuccess
		case "waitingForFill":
			status.Kind = types.ActionStatusWaitingForFill
		case "waitingForTrigger":
			status.Kind = types.ActionStatusWaitingForTrigger
		default:
			status.Raw = string(raw)
		}
		return status
	}

	var object statusObject
	if err := codec.Unmarshal(raw, &object); err != nil {
		status.Raw = string(raw)
		return status
	}
	switch {
	case object.Error != nil:
		status.Kind = types.ActionStatusError
		status.Err = hlerr.FromExchangeText(*object.Error)
	case object.Resting != nil:
		status.Kind = types.ActionStatusResting
		status.Oid = object.Resting.Oid
		status.Cloid = object.Resting.Cloid
	case object.Filled != nil:
		status.Kind = types.ActionStatusFilled
		status.Oid = object.Filled.Oid
		status.Cloid = object.Filled.Cloid
		status.TotalSz = object.Filled.TotalSz
		status.AvgPx = object.Filled.AvgPx
	default:
		status.Raw = string(raw)
	}
	return status
}
