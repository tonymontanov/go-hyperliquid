/*
FILE: internal/action/action.go

DESCRIPTION:
Common layer for exchange actions (POST /exchange, WS post "action").

Every action has TWO wire forms that must agree field-for-field:
  - MessagePack — hashed and signed (scheme 1, see internal/signing);
  - JSON        — sent to the exchange, which re-serialises it to MessagePack
                  and recovers the signer from the hash.
A mismatch in field ORDER, an extra "f": false, or a trailing zero in a price
silently changes the recovered address and the exchange answers with
"User or API Wallet 0x... does not exist". To make such bugs impossible to
introduce by accident, each action implements both writers side by side in one
file, with the same explicit field order, and both are covered by vector tests
generated with the official Python SDK.

Actions in this package are SECTION-AGNOSTIC: they operate on resolved asset
ids and already-normalised Fixed values. Sections (perpetuals, spot, ...)
resolve symbols and apply their precision rules, then hand the wires over to
the unified request functions of internal/engine.

MAIN ENTITIES:
  - Action      : interface implemented by every action.
  - OrderWire   : one order in wire terms (used by order / modify actions).
  - appendJSON* : tiny allocation-free JSON writers. All string values written
                  by this package are hex, enums or plain decimals, so no
                  escaping is required.

DEPENDENCIES:
- internal/msgpack: MessagePack writer.
- types: Fixed, Cloid, enums.
*/

package action

import (
	"strconv"

	"github.com/tonymontanov/go-hyperliquid/internal/msgpack"
	"github.com/tonymontanov/go-hyperliquid/types"
)

// Action — an exchange action that can serialise itself into both wire forms.
type Action interface {
	// Type returns the wire value of the "type" field ("order", "cancel", ...).
	Type() string
	// AppendMsgpack appends the MessagePack form used for hashing.
	AppendMsgpack(b []byte) []byte
	// AppendJSON appends the JSON form sent to the exchange.
	AppendJSON(b []byte) []byte
	// BatchLen returns the number of batched items (orders, cancels,
	// modifies). Used for rate-limit accounting: the IP weight of an action is
	// 1 + floor(BatchLen / 40) and the address-based cost is BatchLen. Actions
	// without a batch return 1.
	BatchLen() int
}

// appendJSONKey appends `"key":`.
func appendJSONKey(b []byte, key string) []byte {
	b = append(b, '"')
	b = append(b, key...)
	return append(b, '"', ':')
}

// appendJSONString appends `"value"` (no escaping — see package comment).
func appendJSONString(b []byte, value string) []byte {
	b = append(b, '"')
	b = append(b, value...)
	return append(b, '"')
}

// appendJSONBool appends true / false.
func appendJSONBool(b []byte, value bool) []byte {
	if value {
		return append(b, "true"...)
	}
	return append(b, "false"...)
}

// appendJSONFixed appends a Fixed as a JSON string in exchange wire form.
func appendJSONFixed(b []byte, value types.Fixed) []byte {
	b = append(b, '"')
	b = value.AppendWire(b)
	return append(b, '"')
}

// appendJSONCloid appends a cloid as a JSON string.
func appendJSONCloid(b []byte, cloid types.Cloid) []byte {
	b = append(b, '"')
	b = cloid.AppendHex(b)
	return append(b, '"')
}

// appendMsgpackFixed appends a Fixed as a MessagePack string in wire form.
func appendMsgpackFixed(b []byte, value types.Fixed) []byte {
	var scratch [24]byte
	return msgpack.AppendStringBytes(b, value.AppendWire(scratch[:0]))
}

// appendMsgpackCloid appends a cloid as a MessagePack string.
func appendMsgpackCloid(b []byte, cloid types.Cloid) []byte {
	var scratch [2 + types.CloidLength*2]byte
	return msgpack.AppendStringBytes(b, cloid.AppendHex(scratch[:0]))
}

// OrderWire — one order in wire terms. Px / Sz / TriggerPx must already be
// normalised by the section (types.Precision); Asset must be resolved.
type OrderWire struct {
	Asset      uint32
	IsBuy      bool
	Px         types.Fixed
	Sz         types.Fixed
	ReduceOnly bool

	// IsTrigger selects the order type: false → {"limit":{"tif"}},
	// true → {"trigger":{"isMarket","triggerPx","tpsl"}}.
	IsTrigger bool
	Tif       types.TimeInForce
	TriggerPx types.Fixed
	IsMarket  bool
	Tpsl      types.Tpsl

	// Cloid — optional; omitted from both wire forms when not set.
	Cloid types.Cloid
}

// appendMsgpack appends the order map: a, b, p, s, r, t, [c].
func (o *OrderWire) appendMsgpack(b []byte) []byte {
	var fields int = 6
	if o.Cloid.IsSet() {
		fields = 7
	}
	b = msgpack.AppendMapHeader(b, fields)
	b = msgpack.AppendString(b, "a")
	b = msgpack.AppendUint(b, uint64(o.Asset))
	b = msgpack.AppendString(b, "b")
	b = msgpack.AppendBool(b, o.IsBuy)
	b = msgpack.AppendString(b, "p")
	b = appendMsgpackFixed(b, o.Px)
	b = msgpack.AppendString(b, "s")
	b = appendMsgpackFixed(b, o.Sz)
	b = msgpack.AppendString(b, "r")
	b = msgpack.AppendBool(b, o.ReduceOnly)
	b = msgpack.AppendString(b, "t")
	b = msgpack.AppendMapHeader(b, 1)
	if o.IsTrigger {
		b = msgpack.AppendString(b, "trigger")
		b = msgpack.AppendMapHeader(b, 3)
		b = msgpack.AppendString(b, "isMarket")
		b = msgpack.AppendBool(b, o.IsMarket)
		b = msgpack.AppendString(b, "triggerPx")
		b = appendMsgpackFixed(b, o.TriggerPx)
		b = msgpack.AppendString(b, "tpsl")
		b = msgpack.AppendString(b, string(o.Tpsl))
	} else {
		b = msgpack.AppendString(b, "limit")
		b = msgpack.AppendMapHeader(b, 1)
		b = msgpack.AppendString(b, "tif")
		b = msgpack.AppendString(b, string(o.Tif))
	}
	if o.Cloid.IsSet() {
		b = msgpack.AppendString(b, "c")
		b = appendMsgpackCloid(b, o.Cloid)
	}
	return b
}

// appendJSON appends the order object with the same field order.
func (o *OrderWire) appendJSON(b []byte) []byte {
	b = append(b, '{')
	b = appendJSONKey(b, "a")
	b = strconv.AppendUint(b, uint64(o.Asset), 10)
	b = append(b, ',')
	b = appendJSONKey(b, "b")
	b = appendJSONBool(b, o.IsBuy)
	b = append(b, ',')
	b = appendJSONKey(b, "p")
	b = appendJSONFixed(b, o.Px)
	b = append(b, ',')
	b = appendJSONKey(b, "s")
	b = appendJSONFixed(b, o.Sz)
	b = append(b, ',')
	b = appendJSONKey(b, "r")
	b = appendJSONBool(b, o.ReduceOnly)
	b = append(b, ',')
	b = appendJSONKey(b, "t")
	if o.IsTrigger {
		b = append(b, `{"trigger":{"isMarket":`...)
		b = appendJSONBool(b, o.IsMarket)
		b = append(b, `,"triggerPx":`...)
		b = appendJSONFixed(b, o.TriggerPx)
		b = append(b, `,"tpsl":`...)
		b = appendJSONString(b, string(o.Tpsl))
		b = append(b, '}', '}')
	} else {
		b = append(b, `{"limit":{"tif":`...)
		b = appendJSONString(b, string(o.Tif))
		b = append(b, '}', '}')
	}
	if o.Cloid.IsSet() {
		b = append(b, ',')
		b = appendJSONKey(b, "c")
		b = appendJSONCloid(b, o.Cloid)
	}
	return append(b, '}')
}
