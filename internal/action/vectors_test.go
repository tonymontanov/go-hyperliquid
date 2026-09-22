/*
FILE: internal/action/vectors_test.go

DESCRIPTION:
Byte-for-byte parity tests against the official Python SDK. For every vector
the action is built with THIS package's builders and four things are compared
with the Python output:
 1. the MessagePack bytes        (msgpack.packb);
 2. the JSON form                (json.dumps, compact separators);
 3. the connectionId             (action_hash);
 4. the signature r / s / v for mainnet AND testnet (sign_l1_action).

Vectors marked "(official vector)" also appear verbatim in the Python SDK's own
tests/signing_test.py. The private key is the public test key of that suite.
*/

package action

import (
	"encoding/hex"
	"testing"

	"github.com/tonymontanov/go-hyperliquid/internal/signing"
	"github.com/tonymontanov/go-hyperliquid/types"
)

// testPrivateKey — public test key from the official SDK test-suite. Not a real wallet.
const testPrivateKey string = "0x0123456789012345678901234567890123456789012345678901234567890123"

type wantSignature struct {
	r string
	s string
	v byte
}

type signingVector struct {
	name         string
	json         string
	msgpackHex   string
	nonce        uint64
	vault        string
	expiresAfter uint64
	hasExpires   bool
	connectionID string
	mainnet      wantSignature
	testnet      wantSignature
}

func fx(s string) types.Fixed { return types.MustParseFixed(s) }

func mustCloid(t testing.TB, s string) types.Cloid {
	t.Helper()
	var c, err = types.ParseCloid(s)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func limitWire(asset uint32, isBuy bool, px, sz string, reduceOnly bool, tif types.TimeInForce, cloid types.Cloid) OrderWire {
	return OrderWire{Asset: asset, IsBuy: isBuy, Px: fx(px), Sz: fx(sz), ReduceOnly: reduceOnly, Tif: tif, Cloid: cloid}
}

func triggerWire(asset uint32, isBuy bool, px, sz string, reduceOnly, isMarket bool, triggerPx string, tpsl types.Tpsl) OrderWire {
	return OrderWire{Asset: asset, IsBuy: isBuy, Px: fx(px), Sz: fx(sz), ReduceOnly: reduceOnly, IsTrigger: true, IsMarket: isMarket, TriggerPx: fx(triggerPx), Tpsl: tpsl}
}

// buildVectorActions mirrors CASES of scripts/gen-signing-vectors.py.
func buildVectorActions(t testing.TB) map[string]Action {
	var none types.Cloid
	var cloid1 = mustCloid(t, "0x00000000000000000000000000000001")
	var cloid2 = mustCloid(t, "0x1234567890abcdef1234567890abcdef")
	var builder, err = signing.ParseAddress("0x5E9ee1089755c3435139848e47e6635505d5a13a") // mixed case on purpose
	if err != nil {
		t.Fatal(err)
	}

	var batch = make([]OrderWire, 0, 20)
	for i := 0; i < 20; i++ {
		var px = types.FixedFromInt(int64(1000 + i))
		batch = append(batch, OrderWire{Asset: uint32(i), IsBuy: i%2 == 0, Px: px, Sz: fx("0.5"), Tif: types.TimeInForceAlo})
	}

	return map[string]Action{
		"order gtc (official vector)":        &Order{Orders: []OrderWire{limitWire(1, true, "100", "100", false, types.TimeInForceGtc, none)}, Grouping: types.GroupingNone},
		"order with cloid (official vector)": &Order{Orders: []OrderWire{limitWire(1, true, "100", "100", false, types.TimeInForceGtc, cloid1)}, Grouping: types.GroupingNone},
		"trigger sl (official vector)":       &Order{Orders: []OrderWire{triggerWire(1, true, "100", "100", false, true, "103", types.TpslStopLoss)}, Grouping: types.GroupingNone},
		"order ioc fractional":               &Order{Orders: []OrderWire{limitWire(4, true, "1670.1", "0.0147", false, types.TimeInForceIoc, none)}, Grouping: types.GroupingNone},
		"order alo vault expires":            &Order{Orders: []OrderWire{limitWire(0, false, "86759.0", "0.000120", true, types.TimeInForceAlo, cloid2)}, Grouping: types.GroupingNone},
		"order batch of 20 (array16)":        &Order{Orders: batch, Grouping: types.GroupingNone},
		"order normalTpsl with builder": &Order{
			Orders: []OrderWire{
				limitWire(3, true, "25.5", "10", false, types.TimeInForceGtc, none),
				triggerWire(3, false, "30", "10", true, false, "29.5", types.TpslTakeProfit),
			},
			Grouping: types.GroupingNormalTpsl,
			Builder:  BuilderFee{Set: true, Address: builder, Fee: 10},
		},
		"order hip3 asset id": &Order{Orders: []OrderWire{limitWire(110000, true, "0.9", "100", false, types.TimeInForceIoc, none)}, Grouping: types.GroupingNone},
		"cancel two":          &Cancel{Cancels: []CancelWire{{Asset: 1, Oid: 123}, {Asset: 10000, Oid: 91490942310}}},
		"cancel fast":         &Cancel{Cancels: []CancelWire{{Asset: 1, Oid: 123}}, Fast: true},
		"cancelByCloid":       &CancelByCloid{Cancels: []CancelByCloidWire{{Asset: 1, Cloid: cloid2}}},
		"cancelByCloid fast":  &CancelByCloid{Cancels: []CancelByCloidWire{{Asset: 1, Cloid: cloid2}}, Fast: true},
		"modify by oid (docs shape)": &Modify{
			Modify: ModifyWire{Ref: OrderRef{Oid: 123}, Order: limitWire(1, true, "101", "100", false, types.TimeInForceAlo, none)},
		},
		"modify by cloid always place (docs shape)": &Modify{
			Modify:      ModifyWire{Ref: OrderRef{Cloid: cloid2}, Order: limitWire(1, true, "101", "100", false, types.TimeInForceGtc, cloid1)},
			AlwaysPlace: true,
		},
		"batchModify": &BatchModify{Modifies: []ModifyWire{
			{Ref: OrderRef{Oid: 123}, Order: limitWire(1, true, "101", "100", false, types.TimeInForceAlo, none)},
			{Ref: OrderRef{Cloid: cloid2}, Order: limitWire(2, false, "0.5", "7", true, types.TimeInForceGtc, cloid1)},
		}},
		"batchModify always place": &BatchModify{
			Modifies:    []ModifyWire{{Ref: OrderRef{Oid: 123}, Order: limitWire(1, true, "101", "100", false, types.TimeInForceAlo, none)}},
			AlwaysPlace: true,
		},
		"scheduleCancel unset (official vector)": &ScheduleCancel{},
		"scheduleCancel time (official vector)":  &ScheduleCancel{HasTime: true, TimeMs: 123456789},
		"noop":                                   &Noop{},
		"updateLeverage":                         &UpdateLeverage{Asset: 1, IsCross: true, Leverage: 20},
		"updateIsolatedMargin add":               &UpdateIsolatedMargin{Asset: 1, IsBuy: true, Ntli: 1000000},
		"updateIsolatedMargin remove":            &UpdateIsolatedMargin{Asset: 1, IsBuy: true, Ntli: -2500000},
	}
}

func TestActionsMatchPythonSDK(t *testing.T) {
	var signer, err = signing.NewSigner(testPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	var actions = buildVectorActions(t)
	if len(actions) != len(generatedVectors) {
		t.Fatalf("builders (%d) and generated vectors (%d) are out of sync", len(actions), len(generatedVectors))
	}

	for _, vector := range generatedVectors {
		var act, ok = actions[vector.name]
		if !ok {
			t.Errorf("%s: no Go builder for this vector", vector.name)
			continue
		}

		var packed = act.AppendMsgpack(nil)
		if got := hex.EncodeToString(packed); got != vector.msgpackHex {
			t.Errorf("%s: msgpack mismatch\n got %s\nwant %s", vector.name, got, vector.msgpackHex)
			continue
		}
		if got := string(act.AppendJSON(nil)); got != vector.json {
			t.Errorf("%s: JSON mismatch\n got %s\nwant %s", vector.name, got, vector.json)
		}

		var vault *signing.Address
		if vector.vault != "" {
			var parsed, parseErr = signing.ParseAddress(vector.vault)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			vault = &parsed
		}

		var connectionID [32]byte
		signing.ActionHash(&connectionID, append([]byte(nil), packed...), vector.nonce, vault, vector.expiresAfter, vector.hasExpires)
		if got := hex.EncodeToString(connectionID[:]); got != vector.connectionID {
			t.Errorf("%s: connectionId mismatch\n got %s\nwant %s", vector.name, got, vector.connectionID)
		}

		var networks = []struct {
			isMainnet bool
			want      wantSignature
		}{{true, vector.mainnet}, {false, vector.testnet}}
		for _, network := range networks {
			var sig, _, signErr = signer.SignL1Action(append([]byte(nil), packed...), vector.nonce, vault, vector.expiresAfter, vector.hasExpires, network.isMainnet)
			if signErr != nil {
				t.Fatal(signErr)
			}
			if sig.RHex() != network.want.r || sig.SHex() != network.want.s || sig.V != network.want.v {
				t.Errorf("%s (mainnet=%v): signature mismatch\n got r=%s s=%s v=%d\nwant r=%s s=%s v=%d",
					vector.name, network.isMainnet, sig.RHex(), sig.SHex(), sig.V, network.want.r, network.want.s, network.want.v)
			}
		}
	}
}

func TestBatchLen(t *testing.T) {
	var actions = buildVectorActions(t)
	if got := actions["order batch of 20 (array16)"].BatchLen(); got != 20 {
		t.Errorf("order batch BatchLen = %d", got)
	}
	if got := actions["cancel two"].BatchLen(); got != 2 {
		t.Errorf("cancel BatchLen = %d", got)
	}
	if got := actions["noop"].BatchLen(); got != 1 {
		t.Errorf("noop BatchLen = %d", got)
	}
}

func BenchmarkOrderAppendMsgpack(b *testing.B) {
	var act = &Order{Orders: []OrderWire{limitWire(0, true, "86759", "0.00012", false, types.TimeInForceAlo, mustCloid(b, "0x1234567890abcdef1234567890abcdef"))}, Grouping: types.GroupingNone}
	var buf = make([]byte, 0, 512)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = act.AppendMsgpack(buf[:0])
	}
}

func BenchmarkOrderAppendJSON(b *testing.B) {
	var act = &Order{Orders: []OrderWire{limitWire(0, true, "86759", "0.00012", false, types.TimeInForceAlo, mustCloid(b, "0x1234567890abcdef1234567890abcdef"))}, Grouping: types.GroupingNone}
	var buf = make([]byte, 0, 512)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = act.AppendJSON(buf[:0])
	}
}

func BenchmarkSignL1Order(b *testing.B) {
	var signer, err = signing.NewSigner(testPrivateKey)
	if err != nil {
		b.Fatal(err)
	}
	var act = &Order{Orders: []OrderWire{limitWire(0, true, "86759", "0.00012", false, types.TimeInForceAlo, mustCloid(b, "0x1234567890abcdef1234567890abcdef"))}, Grouping: types.GroupingNone}
	var buf = make([]byte, 0, 512)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = act.AppendMsgpack(buf[:0])
		_, buf, _ = signer.SignL1Action(buf, uint64(i), nil, 0, false, true)
	}
}
