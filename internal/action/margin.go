/*
FILE: internal/action/margin.go

DESCRIPTION:
Leverage and margin actions of the common layer. They are meaningful only for
margined sections (perpetuals, HIP-3), but they live here because the wire
format is identical for every perp dex — the section only resolves the asset.

WIRE SHAPES (field order is part of the signature):
  updateLeverage       : {"type":"updateLeverage","asset":<asset>,"isCross":<bool>,"leverage":<int>}
  updateIsolatedMargin : {"type":"updateIsolatedMargin","asset":<asset>,"isBuy":<bool>,"ntli":<int>}

ntli — signed amount of USDC with 6 decimals (1000000 = 1 USD); a negative
value removes margin. The docs note that isBuy "won't have any effect until
hedge mode is introduced"; the Python SDK always sends true.
*/

package action

import (
	"strconv"

	"github.com/tonymontanov/go-hyperliquid/internal/msgpack"
)

// UpdateLeverage — "updateLeverage" action.
type UpdateLeverage struct {
	Asset    uint32
	IsCross  bool
	Leverage uint32
}

// Type implements Action.
func (a *UpdateLeverage) Type() string { return "updateLeverage" }

// BatchLen implements Action.
func (a *UpdateLeverage) BatchLen() int { return 1 }

// AppendMsgpack implements Action.
func (a *UpdateLeverage) AppendMsgpack(b []byte) []byte {
	b = msgpack.AppendMapHeader(b, 4)
	b = msgpack.AppendString(b, "type")
	b = msgpack.AppendString(b, "updateLeverage")
	b = msgpack.AppendString(b, "asset")
	b = msgpack.AppendUint(b, uint64(a.Asset))
	b = msgpack.AppendString(b, "isCross")
	b = msgpack.AppendBool(b, a.IsCross)
	b = msgpack.AppendString(b, "leverage")
	return msgpack.AppendUint(b, uint64(a.Leverage))
}

// AppendJSON implements Action.
func (a *UpdateLeverage) AppendJSON(b []byte) []byte {
	b = append(b, `{"type":"updateLeverage","asset":`...)
	b = strconv.AppendUint(b, uint64(a.Asset), 10)
	b = append(b, `,"isCross":`...)
	b = appendJSONBool(b, a.IsCross)
	b = append(b, `,"leverage":`...)
	b = strconv.AppendUint(b, uint64(a.Leverage), 10)
	return append(b, '}')
}

// UpdateIsolatedMargin — "updateIsolatedMargin" action.
type UpdateIsolatedMargin struct {
	Asset uint32
	IsBuy bool
	// Ntli — signed USDC amount with 6 decimals; negative removes margin.
	Ntli int64
}

// Type implements Action.
func (a *UpdateIsolatedMargin) Type() string { return "updateIsolatedMargin" }

// BatchLen implements Action.
func (a *UpdateIsolatedMargin) BatchLen() int { return 1 }

// AppendMsgpack implements Action.
func (a *UpdateIsolatedMargin) AppendMsgpack(b []byte) []byte {
	b = msgpack.AppendMapHeader(b, 4)
	b = msgpack.AppendString(b, "type")
	b = msgpack.AppendString(b, "updateIsolatedMargin")
	b = msgpack.AppendString(b, "asset")
	b = msgpack.AppendUint(b, uint64(a.Asset))
	b = msgpack.AppendString(b, "isBuy")
	b = msgpack.AppendBool(b, a.IsBuy)
	b = msgpack.AppendString(b, "ntli")
	return msgpack.AppendInt(b, a.Ntli)
}

// AppendJSON implements Action.
func (a *UpdateIsolatedMargin) AppendJSON(b []byte) []byte {
	b = append(b, `{"type":"updateIsolatedMargin","asset":`...)
	b = strconv.AppendUint(b, uint64(a.Asset), 10)
	b = append(b, `,"isBuy":`...)
	b = appendJSONBool(b, a.IsBuy)
	b = append(b, `,"ntli":`...)
	b = strconv.AppendInt(b, a.Ntli, 10)
	return append(b, '}')
}
