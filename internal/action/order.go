/*
FILE: internal/action/order.go

DESCRIPTION:
Order-placing and order-modifying actions of the common layer.

WIRE SHAPES (field order is part of the signature):
  order       : {"type":"order","orders":[<order>...],"grouping":"na"[,"builder":{"b":"0x..","f":N}]}
  modify      : {"type":"modify","oid":<oid|cloid>,"order":<order>[,"a":true]}
  batchModify : {"type":"batchModify","modifies":[{"oid":<oid|cloid>,"order":<order>}...][,"a":true]}

"a" is the always_place flag of modify / batchModify. The official docs state:
"`a` must be skipped if false, i.e. actions hashed with `a: false` will be
rejected" — both writers therefore emit it only when true.

DOCS vs PYTHON SDK:
  - The single "modify" action and the "a" flag exist only in the GitBook docs;
    the Python SDK always sends batchModify without "a". The key order used
    here follows the docs' request tables.
  - "builder" is lower-cased by the Python SDK before signing; the SDK's
    Address type renders lower-case hex unconditionally.
*/

package action

import (
	"strconv"

	"github.com/tonymontanov/go-hyperliquid/internal/msgpack"
	"github.com/tonymontanov/go-hyperliquid/internal/signing"
	"github.com/tonymontanov/go-hyperliquid/types"
)

// BuilderFee — optional builder code attached to an "order" action.
type BuilderFee struct {
	// Set — false means "no builder" (the key is omitted).
	Set bool
	// Address — builder address ("b").
	Address signing.Address
	// Fee — fee in tenths of a basis point ("f"): 10 = 1 bp.
	Fee uint32
}

// Order — "order" action: one or more orders placed atomically in one request.
type Order struct {
	Orders   []OrderWire
	Grouping types.Grouping
	Builder  BuilderFee
}

// Type implements Action.
func (a *Order) Type() string { return "order" }

// BatchLen implements Action.
func (a *Order) BatchLen() int { return len(a.Orders) }

// AppendMsgpack implements Action.
func (a *Order) AppendMsgpack(b []byte) []byte {
	var fields int = 3
	if a.Builder.Set {
		fields = 4
	}
	b = msgpack.AppendMapHeader(b, fields)
	b = msgpack.AppendString(b, "type")
	b = msgpack.AppendString(b, "order")
	b = msgpack.AppendString(b, "orders")
	b = msgpack.AppendArrayHeader(b, len(a.Orders))
	var i int
	for i = 0; i < len(a.Orders); i++ {
		b = a.Orders[i].appendMsgpack(b)
	}
	b = msgpack.AppendString(b, "grouping")
	b = msgpack.AppendString(b, string(a.Grouping))
	if a.Builder.Set {
		var scratch [2 + signing.AddressLength*2]byte
		b = msgpack.AppendString(b, "builder")
		b = msgpack.AppendMapHeader(b, 2)
		b = msgpack.AppendString(b, "b")
		b = msgpack.AppendStringBytes(b, a.Builder.Address.AppendHex(scratch[:0]))
		b = msgpack.AppendString(b, "f")
		b = msgpack.AppendUint(b, uint64(a.Builder.Fee))
	}
	return b
}

// AppendJSON implements Action.
func (a *Order) AppendJSON(b []byte) []byte {
	b = append(b, `{"type":"order","orders":[`...)
	var i int
	for i = 0; i < len(a.Orders); i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = a.Orders[i].appendJSON(b)
	}
	b = append(b, `],"grouping":`...)
	b = appendJSONString(b, string(a.Grouping))
	if a.Builder.Set {
		b = append(b, `,"builder":{"b":"`...)
		b = a.Builder.Address.AppendHex(b)
		b = append(b, `","f":`...)
		b = strconv.AppendUint(b, uint64(a.Builder.Fee), 10)
		b = append(b, '}')
	}
	return append(b, '}')
}

// OrderRef — reference to an existing order: exchange oid or client cloid.
// Cloid wins when set.
type OrderRef struct {
	Oid   uint64
	Cloid types.Cloid
}

// appendMsgpack appends the reference as a uint (oid) or a string (cloid).
func (r *OrderRef) appendMsgpack(b []byte) []byte {
	if r.Cloid.IsSet() {
		return appendMsgpackCloid(b, r.Cloid)
	}
	return msgpack.AppendUint(b, r.Oid)
}

// appendJSON appends the reference as a number (oid) or a string (cloid).
func (r *OrderRef) appendJSON(b []byte) []byte {
	if r.Cloid.IsSet() {
		return appendJSONCloid(b, r.Cloid)
	}
	return strconv.AppendUint(b, r.Oid, 10)
}

// ModifyWire — one modification: which order and what it becomes.
type ModifyWire struct {
	Ref   OrderRef
	Order OrderWire
}

// Modify — single "modify" action (docs-only; see file header).
type Modify struct {
	Modify      ModifyWire
	AlwaysPlace bool
}

// Type implements Action.
func (a *Modify) Type() string { return "modify" }

// BatchLen implements Action.
func (a *Modify) BatchLen() int { return 1 }

// AppendMsgpack implements Action.
func (a *Modify) AppendMsgpack(b []byte) []byte {
	var fields int = 3
	if a.AlwaysPlace {
		fields = 4
	}
	b = msgpack.AppendMapHeader(b, fields)
	b = msgpack.AppendString(b, "type")
	b = msgpack.AppendString(b, "modify")
	b = msgpack.AppendString(b, "oid")
	b = a.Modify.Ref.appendMsgpack(b)
	b = msgpack.AppendString(b, "order")
	b = a.Modify.Order.appendMsgpack(b)
	if a.AlwaysPlace {
		b = msgpack.AppendString(b, "a")
		b = msgpack.AppendBool(b, true)
	}
	return b
}

// AppendJSON implements Action.
func (a *Modify) AppendJSON(b []byte) []byte {
	b = append(b, `{"type":"modify","oid":`...)
	b = a.Modify.Ref.appendJSON(b)
	b = append(b, `,"order":`...)
	b = a.Modify.Order.appendJSON(b)
	if a.AlwaysPlace {
		b = append(b, `,"a":true`...)
	}
	return append(b, '}')
}

// BatchModify — "batchModify" action.
type BatchModify struct {
	Modifies    []ModifyWire
	AlwaysPlace bool
}

// Type implements Action.
func (a *BatchModify) Type() string { return "batchModify" }

// BatchLen implements Action.
func (a *BatchModify) BatchLen() int { return len(a.Modifies) }

// AppendMsgpack implements Action.
func (a *BatchModify) AppendMsgpack(b []byte) []byte {
	var fields int = 2
	if a.AlwaysPlace {
		fields = 3
	}
	b = msgpack.AppendMapHeader(b, fields)
	b = msgpack.AppendString(b, "type")
	b = msgpack.AppendString(b, "batchModify")
	b = msgpack.AppendString(b, "modifies")
	b = msgpack.AppendArrayHeader(b, len(a.Modifies))
	var i int
	for i = 0; i < len(a.Modifies); i++ {
		b = msgpack.AppendMapHeader(b, 2)
		b = msgpack.AppendString(b, "oid")
		b = a.Modifies[i].Ref.appendMsgpack(b)
		b = msgpack.AppendString(b, "order")
		b = a.Modifies[i].Order.appendMsgpack(b)
	}
	if a.AlwaysPlace {
		b = msgpack.AppendString(b, "a")
		b = msgpack.AppendBool(b, true)
	}
	return b
}

// AppendJSON implements Action.
func (a *BatchModify) AppendJSON(b []byte) []byte {
	b = append(b, `{"type":"batchModify","modifies":[`...)
	var i int
	for i = 0; i < len(a.Modifies); i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"oid":`...)
		b = a.Modifies[i].Ref.appendJSON(b)
		b = append(b, `,"order":`...)
		b = a.Modifies[i].Order.appendJSON(b)
		b = append(b, '}')
	}
	b = append(b, ']')
	if a.AlwaysPlace {
		b = append(b, `,"a":true`...)
	}
	return append(b, '}')
}
