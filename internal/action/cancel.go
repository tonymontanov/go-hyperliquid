/*
FILE: internal/action/cancel.go

DESCRIPTION:
Cancel actions of the common layer.

WIRE SHAPES (field order is part of the signature):
  cancel        : {"type":"cancel","cancels":[{"a":<asset>,"o":<oid>}...][,"f":true]}
  cancelByCloid : {"type":"cancelByCloid","cancels":[{"asset":<asset>,"cloid":"0x.."}...][,"f":true]}
  scheduleCancel: {"type":"scheduleCancel"[,"time":<ms>]}
  noop          : {"type":"noop"}

"f" is the fast flag (docs-only, absent from the Python SDK). The docs state:
"`f` must be skipped if false, i.e. actions hashed with `f: false` will be
rejected" and "Orders with `f: true` are rejected if they refer to trigger
orders". Both writers emit it only when true.

noop marks its nonce as used without doing anything. The docs recommend it as
the most reliable way to invalidate an in-flight order: send a noop with the
SAME nonce as the order that must not land.
*/

package action

import (
	"strconv"

	"github.com/tonymontanov/go-hyperliquid/internal/msgpack"
	"github.com/tonymontanov/go-hyperliquid/types"
)

// CancelWire — one cancel by exchange order id.
type CancelWire struct {
	Asset uint32
	Oid   uint64
}

// Cancel — "cancel" action.
type Cancel struct {
	Cancels []CancelWire
	Fast    bool
}

// Type implements Action.
func (a *Cancel) Type() string { return "cancel" }

// BatchLen implements Action.
func (a *Cancel) BatchLen() int { return len(a.Cancels) }

// AppendMsgpack implements Action.
func (a *Cancel) AppendMsgpack(b []byte) []byte {
	var fields int = 2
	if a.Fast {
		fields = 3
	}
	b = msgpack.AppendMapHeader(b, fields)
	b = msgpack.AppendString(b, "type")
	b = msgpack.AppendString(b, "cancel")
	b = msgpack.AppendString(b, "cancels")
	b = msgpack.AppendArrayHeader(b, len(a.Cancels))
	var i int
	for i = 0; i < len(a.Cancels); i++ {
		b = msgpack.AppendMapHeader(b, 2)
		b = msgpack.AppendString(b, "a")
		b = msgpack.AppendUint(b, uint64(a.Cancels[i].Asset))
		b = msgpack.AppendString(b, "o")
		b = msgpack.AppendUint(b, a.Cancels[i].Oid)
	}
	if a.Fast {
		b = msgpack.AppendString(b, "f")
		b = msgpack.AppendBool(b, true)
	}
	return b
}

// AppendJSON implements Action.
func (a *Cancel) AppendJSON(b []byte) []byte {
	b = append(b, `{"type":"cancel","cancels":[`...)
	var i int
	for i = 0; i < len(a.Cancels); i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"a":`...)
		b = strconv.AppendUint(b, uint64(a.Cancels[i].Asset), 10)
		b = append(b, `,"o":`...)
		b = strconv.AppendUint(b, a.Cancels[i].Oid, 10)
		b = append(b, '}')
	}
	b = append(b, ']')
	if a.Fast {
		b = append(b, `,"f":true`...)
	}
	return append(b, '}')
}

// CancelByCloidWire — one cancel by client order id.
type CancelByCloidWire struct {
	Asset uint32
	Cloid types.Cloid
}

// CancelByCloid — "cancelByCloid" action.
type CancelByCloid struct {
	Cancels []CancelByCloidWire
	Fast    bool
}

// Type implements Action.
func (a *CancelByCloid) Type() string { return "cancelByCloid" }

// BatchLen implements Action.
func (a *CancelByCloid) BatchLen() int { return len(a.Cancels) }

// AppendMsgpack implements Action.
func (a *CancelByCloid) AppendMsgpack(b []byte) []byte {
	var fields int = 2
	if a.Fast {
		fields = 3
	}
	b = msgpack.AppendMapHeader(b, fields)
	b = msgpack.AppendString(b, "type")
	b = msgpack.AppendString(b, "cancelByCloid")
	b = msgpack.AppendString(b, "cancels")
	b = msgpack.AppendArrayHeader(b, len(a.Cancels))
	var i int
	for i = 0; i < len(a.Cancels); i++ {
		b = msgpack.AppendMapHeader(b, 2)
		b = msgpack.AppendString(b, "asset")
		b = msgpack.AppendUint(b, uint64(a.Cancels[i].Asset))
		b = msgpack.AppendString(b, "cloid")
		b = appendMsgpackCloid(b, a.Cancels[i].Cloid)
	}
	if a.Fast {
		b = msgpack.AppendString(b, "f")
		b = msgpack.AppendBool(b, true)
	}
	return b
}

// AppendJSON implements Action.
func (a *CancelByCloid) AppendJSON(b []byte) []byte {
	b = append(b, `{"type":"cancelByCloid","cancels":[`...)
	var i int
	for i = 0; i < len(a.Cancels); i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"asset":`...)
		b = strconv.AppendUint(b, uint64(a.Cancels[i].Asset), 10)
		b = append(b, `,"cloid":`...)
		b = appendJSONCloid(b, a.Cancels[i].Cloid)
		b = append(b, '}')
	}
	b = append(b, ']')
	if a.Fast {
		b = append(b, `,"f":true`...)
	}
	return append(b, '}')
}

// ScheduleCancel — "scheduleCancel" action (dead man's switch). HasTime false
// removes a previously scheduled cancel. Exchange rules: time must be at least
// 5 seconds in the future; at most 10 triggers per day (reset at 00:00 UTC).
type ScheduleCancel struct {
	HasTime bool
	TimeMs  uint64
}

// Type implements Action.
func (a *ScheduleCancel) Type() string { return "scheduleCancel" }

// BatchLen implements Action.
func (a *ScheduleCancel) BatchLen() int { return 1 }

// AppendMsgpack implements Action.
func (a *ScheduleCancel) AppendMsgpack(b []byte) []byte {
	if !a.HasTime {
		b = msgpack.AppendMapHeader(b, 1)
		b = msgpack.AppendString(b, "type")
		return msgpack.AppendString(b, "scheduleCancel")
	}
	b = msgpack.AppendMapHeader(b, 2)
	b = msgpack.AppendString(b, "type")
	b = msgpack.AppendString(b, "scheduleCancel")
	b = msgpack.AppendString(b, "time")
	return msgpack.AppendUint(b, a.TimeMs)
}

// AppendJSON implements Action.
func (a *ScheduleCancel) AppendJSON(b []byte) []byte {
	b = append(b, `{"type":"scheduleCancel"`...)
	if a.HasTime {
		b = append(b, `,"time":`...)
		b = strconv.AppendUint(b, a.TimeMs, 10)
	}
	return append(b, '}')
}

// Noop — "noop" action: consumes a nonce and does nothing else.
type Noop struct{}

// Type implements Action.
func (a *Noop) Type() string { return "noop" }

// BatchLen implements Action.
func (a *Noop) BatchLen() int { return 1 }

// AppendMsgpack implements Action.
func (a *Noop) AppendMsgpack(b []byte) []byte {
	b = msgpack.AppendMapHeader(b, 1)
	b = msgpack.AppendString(b, "type")
	return msgpack.AppendString(b, "noop")
}

// AppendJSON implements Action.
func (a *Noop) AppendJSON(b []byte) []byte {
	return append(b, `{"type":"noop"}`...)
}
