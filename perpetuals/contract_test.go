/*
FILE: perpetuals/contract_test.go

DESCRIPTION:
Contract tests of the Perpetuals section against an in-process mock of the
Hyperliquid API (no network access). The mock routes POST /info by the "type"
field, records POST /exchange bodies and serves /ws.

COVERED:
  - metadata → asset ids / precision (asset id = index in meta.universe);
  - the exact /exchange request body, and that its signature recovers to the
    signing wallet for the nonce / vaultAddress / expiresAfter it carries;
  - decoding of every documented status shape and of a whole-action rejection;
  - validation before network (no request leaves the process);
  - section filtering of account-wide answers (spot / builder-deployed coins);
  - emulated CancelAllOrders, ClosePosition recipe, SlippagePrice;
  - SDK-side rate-limit accounting (weights of info and action requests);
  - WS: BBO stream decoding and trading over WS post.

FIXTURES:
Response fixtures are trimmed copies of the examples on the official
info-endpoint / exchange-endpoint pages and of live public mainnet answers
(meta, l2Book) captured on 2026-09-22. An unknown info type answers 422 so a
request that went to the wrong place fails the test explicitly.
*/

package perpetuals

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/gorilla/websocket"

	hyperliquid "github.com/tonymontanov/go-hyperliquid"
	"github.com/tonymontanov/go-hyperliquid/internal/action"
	"github.com/tonymontanov/go-hyperliquid/internal/codec"
	"github.com/tonymontanov/go-hyperliquid/internal/signing"
	"github.com/tonymontanov/go-hyperliquid/types"
)

// testPrivateKey — public test key of the official SDK test-suite. Not a real wallet.
const testPrivateKey string = "0x0123456789012345678901234567890123456789012345678901234567890123"

// testSignerAddress — address of testPrivateKey.
const testSignerAddress string = "0x14791697260e4c9a71f18484c9f997b308e59325"

const testAccount string = "0x5e9ee1089755c3435139848e47e6635505d5a13a"

const fixtureMeta string = `{"universe":[
 {"szDecimals":5,"name":"BTC","maxLeverage":40,"marginTableId":56},
 {"szDecimals":4,"name":"ETH","maxLeverage":25,"marginTableId":55},
 {"szDecimals":0,"name":"kPEPE","maxLeverage":10,"marginTableId":52},
 {"szDecimals":1,"name":"OLD","maxLeverage":3,"marginTableId":3,"isDelisted":true},
 {"szDecimals":2,"name":"ISO","maxLeverage":3,"marginTableId":3,"onlyIsolated":true}
],"marginTables":[],"collateralToken":0}`

// "@1120" is a verbatim junk entry observed on testnet (2026-09-22): out of the
// Fixed range, it must not break the decoding of the other mids.
const fixtureAllMids string = `{"BTC":"86759.5","ETH":"2986.35","kPEPE":"0.011234","@107":"45.1","PURR/USDC":"0.21","@1120":"1844674407370.9553222656"}`

const fixtureL2Book string = `{"coin":"BTC","time":1758500000123,"levels":[
 [{"px":"86759.0","sz":"7.25913","n":25},{"px":"86758.0","sz":"0.5","n":2}],
 [{"px":"86760.0","sz":"1.2","n":4}]]}`

const fixtureOpenOrders string = `[
 {"coin":"BTC","side":"B","limitPx":"86000.0","sz":"0.001","oid":101,"timestamp":1000,"origSz":"0.001","cloid":"0x1234567890abcdef1234567890abcdef","reduceOnly":false,"orderType":"Limit","tif":"Alo","isTrigger":false,"triggerPx":"0.0","triggerCondition":"N/A","isPositionTpsl":false,"children":[]},
 {"coin":"ETH","side":"A","limitPx":"3100.0","sz":"0.5","oid":102,"timestamp":9999999999999,"origSz":"1.0","cloid":null,"reduceOnly":true,"orderType":"Limit","tif":"Gtc","isTrigger":false,"triggerPx":"0.0","triggerCondition":"N/A","isPositionTpsl":false,"children":[]},
 {"coin":"@107","side":"B","limitPx":"40.0","sz":"3","oid":103,"timestamp":1000,"origSz":"3","cloid":null,"reduceOnly":false,"orderType":"Limit","tif":"Gtc","isTrigger":false,"triggerPx":"0.0","triggerCondition":"N/A","isPositionTpsl":false,"children":[]}
]`

const fixtureClearinghouse string = `{"assetPositions":[{"position":{"coin":"ETH","cumFunding":{"allTime":"514.085417","sinceChange":"0.0","sinceOpen":"0.0"},"entryPx":"2986.3","leverage":{"rawUsd":"-95.059824","type":"isolated","value":20},"liquidationPx":null,"marginUsed":"4.967826","maxLeverage":50,"positionValue":"100.02765","returnOnEquity":"-0.0026789","szi":"-0.0335","unrealizedPnl":"-0.0134"},"type":"oneWay"}],
 "crossMaintenanceMarginUsed":"0.0","crossMarginSummary":{"accountValue":"13104.514502","totalMarginUsed":"0.0","totalNtlPos":"0.0","totalRawUsd":"13104.514502"},
 "marginSummary":{"accountValue":"13109.482328","totalMarginUsed":"4.967826","totalNtlPos":"100.02765","totalRawUsd":"13009.454678"},"time":1708622398623,"withdrawable":"13104.514502"}`

// mockExchange — scriptable Hyperliquid mock.
type mockExchange struct {
	t            *testing.T
	srv          *httptest.Server
	mu           sync.Mutex
	info         map[string]string
	infoCalls    map[string]int
	exchangeBody []string
	exchangeResp string
	wsFrames     []string
	onWsFrame    func(conn *websocket.Conn, frame string)
}

func newMockExchange(t *testing.T) *mockExchange {
	var m = &mockExchange{
		t: t,
		info: map[string]string{
			"meta":               fixtureMeta,
			"allMids":            fixtureAllMids,
			"l2Book":             fixtureL2Book,
			"frontendOpenOrders": fixtureOpenOrders,
			"clearinghouseState": fixtureClearinghouse,
			"userRateLimit":      `{"cumVlm":"2854574.593578","nRequestsUsed":2890,"nRequestsCap":2864574,"nRequestsSurplus":0}`,
			"orderStatus":        `{"status":"unknownOid"}`,
		},
		infoCalls:    map[string]int{},
		exchangeResp: `{"status":"ok","response":{"type":"default"}}`,
	}
	var upgrader = websocket.Upgrader{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			var conn, err = upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			_ = conn.WriteMessage(websocket.TextMessage, []byte("Websocket connection established."))
			for {
				var _, frame, readErr = conn.ReadMessage()
				if readErr != nil {
					return
				}
				m.mu.Lock()
				m.wsFrames = append(m.wsFrames, string(frame))
				var hook = m.onWsFrame
				m.mu.Unlock()
				if hook != nil {
					hook(conn, string(frame))
				}
			}
		}
		var raw, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		m.mu.Lock()
		defer m.mu.Unlock()
		switch r.URL.Path {
		case "/info":
			var infoType = codec.GetString(raw, "type")
			m.infoCalls[infoType]++
			var body, ok = m.info[infoType]
			if !ok {
				http.Error(w, "Failed to deserialize the JSON body into the target type", http.StatusUnprocessableEntity)
				return
			}
			_, _ = io.WriteString(w, body)
		case "/exchange":
			m.exchangeBody = append(m.exchangeBody, string(raw))
			_, _ = io.WriteString(w, m.exchangeResp)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockExchange) setExchangeResponse(body string) {
	m.mu.Lock()
	m.exchangeResp = body
	m.mu.Unlock()
}

func (m *mockExchange) lastExchangeBody() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.exchangeBody) == 0 {
		return ""
	}
	return m.exchangeBody[len(m.exchangeBody)-1]
}

func (m *mockExchange) exchangeCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.exchangeBody)
}

// newSection builds root client + section pointed at the mock.
func newSection(t *testing.T, m *mockExchange, mutate func(*hyperliquid.Config)) (*hyperliquid.Client, *Client) {
	t.Helper()
	var cfg = hyperliquid.DefaultConfig()
	cfg.Testnet = true
	cfg.REST.BaseURL = m.srv.URL
	cfg.WS.URL = "ws" + strings.TrimPrefix(m.srv.URL, "http") + "/ws"
	cfg.WS.PingInterval = 100 * time.Millisecond
	cfg.WS.ReadTimeout = 2 * time.Second
	cfg.WS.PostTimeout = time.Second
	cfg.AccountAddress = testAccount
	cfg.PrivateKey = testPrivateKey
	cfg.AssetRefreshInterval = -1
	if mutate != nil {
		mutate(&cfg)
	}
	var root, err = hyperliquid.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root, NewClient(root)
}

func fx(s string) types.Fixed { return types.MustParseFixed(s) }

func TestAssetsFromMeta(t *testing.T) {
	var m = newMockExchange(t)
	var _, perps = newSection(t, m, nil)
	var ctx = context.Background()

	var assets, err = perps.MarketData().GetAssets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 5 {
		t.Fatalf("assets = %d", len(assets))
	}
	var eth, _ = perps.MarketData().GetAssetInfo(ctx, "ETH")
	if eth.AssetID != 1 || eth.SzDecimals != 4 || eth.Precision.MaxDecimals != MaxDecimals || eth.MaxLeverage != 25 {
		t.Fatalf("ETH = %+v", eth)
	}
	var pepe, _ = perps.MarketData().GetAssetInfo(ctx, "kPEPE")
	if pepe.AssetID != 2 {
		t.Fatalf("kPEPE (case-sensitive coin) = %+v", pepe)
	}
	if _, err = perps.MarketData().GetAssetInfo(ctx, "KPEPE"); !hyperliquid.IsInvalidRequest(err) {
		t.Fatalf("unknown coin error = %v", err)
	}
	if m.infoCalls["meta"] != 1 {
		t.Fatalf("meta must be loaded once, got %d", m.infoCalls["meta"])
	}
}

// verifyExchangeBody checks the envelope and that the signature recovers to
// the signing wallet for the nonce / vault / expiresAfter carried by the body.
func verifyExchangeBody(t *testing.T, body string, act action.Action, wantAction string, vault string, expiresAfter uint64) {
	t.Helper()
	var envelope struct {
		Action    codec.RawMessage `json:"action"`
		Nonce     uint64           `json:"nonce"`
		Signature struct {
			R string `json:"r"`
			S string `json:"s"`
			V byte   `json:"v"`
		} `json:"signature"`
		VaultAddress *string `json:"vaultAddress"`
		ExpiresAfter *uint64 `json:"expiresAfter"`
	}
	if err := codec.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("exchange body is not JSON: %v\n%s", err, body)
	}
	if string(envelope.Action) != wantAction {
		t.Fatalf("action\n got %s\nwant %s", envelope.Action, wantAction)
	}
	if envelope.Nonce < 1_600_000_000_000 {
		t.Fatalf("nonce %d is not a millisecond timestamp", envelope.Nonce)
	}
	if (vault == "") != (envelope.VaultAddress == nil) || (vault != "" && *envelope.VaultAddress != vault) {
		t.Fatalf("vaultAddress = %v, want %q", envelope.VaultAddress, vault)
	}
	if (expiresAfter == 0) != (envelope.ExpiresAfter == nil) || (expiresAfter != 0 && *envelope.ExpiresAfter != expiresAfter) {
		t.Fatalf("expiresAfter = %v, want %d", envelope.ExpiresAfter, expiresAfter)
	}

	var vaultAddr *signing.Address
	if vault != "" {
		var parsed, err = signing.ParseAddress(vault)
		if err != nil {
			t.Fatal(err)
		}
		vaultAddr = &parsed
	}
	var connectionID, digest [32]byte
	signing.ActionHash(&connectionID, act.AppendMsgpack(nil), envelope.Nonce, vaultAddr, expiresAfter, expiresAfter != 0)
	signing.AgentDigest(&digest, &connectionID, false) // cfg.Testnet = true

	var compact = make([]byte, 65)
	compact[0] = envelope.Signature.V
	copy(compact[1:33], leftPad32(t, envelope.Signature.R))
	copy(compact[33:65], leftPad32(t, envelope.Signature.S))
	var pub, _, err = ecdsa.RecoverCompact(compact, digest[:])
	if err != nil {
		t.Fatalf("signature does not recover: %v", err)
	}
	var recovered [32]byte
	signing.Keccak256(&recovered, pub.SerializeUncompressed()[1:])
	if got := "0x" + hex.EncodeToString(recovered[12:]); got != testSignerAddress {
		t.Fatalf("signature recovers to %s, want %s", got, testSignerAddress)
	}
}

func leftPad32(t *testing.T, minimalHex string) []byte {
	t.Helper()
	var digits = strings.TrimPrefix(minimalHex, "0x")
	if len(digits)%2 == 1 {
		digits = "0" + digits
	}
	var raw, err = hex.DecodeString(digits)
	if err != nil {
		t.Fatal(err)
	}
	var out = make([]byte, 32)
	copy(out[32-len(raw):], raw)
	return out
}

func TestCreateOrderRequestAndStatuses(t *testing.T) {
	var m = newMockExchange(t)
	var _, perps = newSection(t, m, nil)
	var ctx = context.Background()
	var cloid, _ = types.ParseCloid("0x1234567890abcdef1234567890abcdef")

	m.setExchangeResponse(`{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":77738308,"cloid":"0x1234567890abcdef1234567890abcdef"}}]}}}`)
	var status, err = perps.Trading().CreateOrder(ctx, types.OrderRequest{
		Coin: "BTC", IsBuy: true, Price: fx("86000"), Size: fx("0.001"), TimeInForce: types.TimeInForceAlo, Cloid: cloid,
	}, types.OrderOptions{ExpiresAfterMs: 1_900_000_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if status.Kind != types.ActionStatusResting || status.Oid != 77738308 || status.Cloid.String() != cloid.String() {
		t.Fatalf("status = %+v", status)
	}
	var wantAction = `{"type":"order","orders":[{"a":0,"b":true,"p":"86000","s":"0.001","r":false,"t":{"limit":{"tif":"Alo"}},"c":"0x1234567890abcdef1234567890abcdef"}],"grouping":"na"}`
	var act = &action.Order{Orders: []action.OrderWire{{Asset: 0, IsBuy: true, Px: fx("86000"), Sz: fx("0.001"), Tif: types.TimeInForceAlo, Cloid: cloid}}, Grouping: types.GroupingNone}
	verifyExchangeBody(t, m.lastExchangeBody(), act, wantAction, "", 1_900_000_000_000)

	// Every documented status shape in one batch.
	m.setExchangeResponse(`{"status":"ok","response":{"type":"order","data":{"statuses":[
	 {"filled":{"totalSz":"0.02","avgPx":"1891.4","oid":77747314}},
	 {"error":"Order must have minimum value of $10."},
	 "waitingForFill","waitingForTrigger",{"surprise":1}]}}}`)
	var batch = []types.OrderRequest{
		{Coin: "ETH", IsBuy: true, Price: fx("1891.4"), Size: fx("0.02"), TimeInForce: types.TimeInForceIoc},
		{Coin: "ETH", IsBuy: true, Price: fx("1891.4"), Size: fx("0.0001"), TimeInForce: types.TimeInForceGtc},
		{Coin: "ETH", IsBuy: false, Price: fx("2000"), Size: fx("0.02"), ReduceOnly: true, IsTrigger: true, TriggerPrice: fx("1999"), Tpsl: types.TpslTakeProfit},
		{Coin: "ETH", IsBuy: false, Price: fx("1700"), Size: fx("0.02"), ReduceOnly: true, IsTrigger: true, TriggerIsMarket: true, TriggerPrice: fx("1750"), Tpsl: types.TpslStopLoss},
		{Coin: "ETH", IsBuy: true, Price: fx("1800"), Size: fx("0.02"), TimeInForce: types.TimeInForceGtc},
	}
	var statuses []types.ActionStatus
	statuses, err = perps.Trading().CreateBatchOrders(ctx, batch, types.OrderOptions{Grouping: types.GroupingNormalTpsl})
	if err != nil {
		t.Fatal(err)
	}
	var wantKinds = []types.ActionStatusKind{types.ActionStatusFilled, types.ActionStatusError, types.ActionStatusWaitingForFill, types.ActionStatusWaitingForTrigger, types.ActionStatusUnknown}
	for i, want := range wantKinds {
		if statuses[i].Kind != want {
			t.Errorf("status[%d] = %s, want %s", i, statuses[i].Kind, want)
		}
	}
	if statuses[0].TotalSz != fx("0.02") || statuses[0].AvgPx != fx("1891.4") || !statuses[0].Accepted() {
		t.Errorf("filled = %+v", statuses[0])
	}
	if !hyperliquid.IsReason(statuses[1].Err, hyperliquid.ReasonMinTradeNtl) || !hyperliquid.IsInvalidRequest(statuses[1].Err) {
		t.Errorf("item error = %v", statuses[1].Err)
	}
	if statuses[4].Raw != `{"surprise":1}` || statuses[4].Accepted() {
		t.Errorf("unknown status = %+v", statuses[4])
	}
	if !strings.Contains(m.lastExchangeBody(), `"grouping":"normalTpsl"`) {
		t.Errorf("grouping not sent: %s", m.lastExchangeBody())
	}
}

func TestWholeActionRejectionAndVault(t *testing.T) {
	var m = newMockExchange(t)
	const vault string = "0x1719884eb866cb12b2287399b15f7db5e7d775ea"
	var root, perps = newSection(t, m, func(cfg *hyperliquid.Config) { cfg.VaultAddress = strings.ToUpper(vault[:2]) + vault[2:] })
	var ctx = context.Background()

	if root.UserAddress() != vault {
		t.Fatalf("info user must be the vault: %s", root.UserAddress())
	}
	m.setExchangeResponse(`{"status":"ok","response":{"type":"cancel","data":{"statuses":["success"]}}}`)
	var err = perps.Trading().CancelOrder(ctx, types.CancelRequest{Coin: "ETH", Oid: 5}, types.CancelOptions{Fast: true})
	if err != nil {
		t.Fatal(err)
	}
	verifyExchangeBody(t, m.lastExchangeBody(), &action.Cancel{Cancels: []action.CancelWire{{Asset: 1, Oid: 5}}, Fast: true},
		`{"type":"cancel","cancels":[{"a":1,"o":5}],"f":true}`, vault, 0)

	m.setExchangeResponse(`{"status":"ok","response":{"type":"cancel","data":{"statuses":[{"error":"Order was never placed, already canceled, or filled."}]}}}`)
	err = perps.Trading().CancelOrder(ctx, types.CancelRequest{Coin: "ETH", Oid: 6}, types.CancelOptions{})
	if !hyperliquid.IsMissingOrder(err) {
		t.Fatalf("missing order error = %v", err)
	}

	m.setExchangeResponse(`{"status":"err","response":"User or API Wallet 0xdeadbeef00000000000000000000000000000000 does not exist."}`)
	err = perps.Trading().CancelOrderByCloid(ctx, types.CancelByCloidRequest{Coin: "ETH", Cloid: types.CloidFromBytes([16]byte{1})}, types.CancelOptions{})
	if !hyperliquid.IsAuth(err) || !hyperliquid.IsReason(err, hyperliquid.ReasonSignerUnknown) {
		t.Fatalf("whole-action rejection = %v", err)
	}
}

func TestValidationBeforeNetwork(t *testing.T) {
	var m = newMockExchange(t)
	var _, perps = newSection(t, m, nil)
	var ctx = context.Background()
	var good = types.OrderRequest{Coin: "BTC", IsBuy: true, Price: fx("86000"), Size: fx("0.001"), TimeInForce: types.TimeInForceGtc}

	var cases = map[string]func(r *types.OrderRequest){
		"unknown coin":     func(r *types.OrderRequest) { r.Coin = "NOPE" },
		"spot coin":        func(r *types.OrderRequest) { r.Coin = "@107" },
		"delisted":         func(r *types.OrderRequest) { r.Coin = "OLD"; r.Size = fx("1") },
		"price sig figs":   func(r *types.OrderRequest) { r.Price = fx("86000.5") },
		"zero price":       func(r *types.OrderRequest) { r.Price = 0 },
		"size decimals":    func(r *types.OrderRequest) { r.Size = fx("0.000001") },
		"zero size":        func(r *types.OrderRequest) { r.Size = 0 },
		"missing tif":      func(r *types.OrderRequest) { r.TimeInForce = "" },
		"bad tif":          func(r *types.OrderRequest) { r.TimeInForce = "FOK" },
		"trigger w/o tpsl": func(r *types.OrderRequest) { r.IsTrigger = true; r.TriggerPrice = fx("85000") },
		"trigger px invalid": func(r *types.OrderRequest) {
			r.IsTrigger = true
			r.Tpsl = types.TpslStopLoss
			r.TriggerPrice = fx("85000.55")
		},
	}
	for name, mutate := range cases {
		var request = good
		mutate(&request)
		var _, err = perps.Trading().CreateOrder(ctx, request, types.OrderOptions{})
		if !hyperliquid.IsInvalidRequest(err) {
			t.Errorf("%s: error = %v, want InvalidRequest", name, err)
		}
	}
	if _, err := perps.Trading().CreateBatchOrders(ctx, nil, types.OrderOptions{}); !hyperliquid.IsInvalidRequest(err) {
		t.Errorf("empty batch: %v", err)
	}
	if _, err := perps.Trading().CreateOrder(ctx, good, types.OrderOptions{Grouping: "weird"}); !hyperliquid.IsInvalidRequest(err) {
		t.Errorf("bad grouping: %v", err)
	}
	if err := perps.Account().SetLeverage(ctx, "BTC", 41, true); !hyperliquid.IsInvalidRequest(err) {
		t.Errorf("leverage above max: %v", err)
	}
	if err := perps.Account().SetLeverage(ctx, "ISO", 2, true); !hyperliquid.IsInvalidRequest(err) {
		t.Errorf("cross on isolated-only asset: %v", err)
	}
	if m.exchangeCount() != 0 {
		t.Fatalf("validation must not reach the network: %d exchange calls", m.exchangeCount())
	}
}

func TestReadOnlyClientCannotTrade(t *testing.T) {
	var m = newMockExchange(t)
	var root, perps = newSection(t, m, func(cfg *hyperliquid.Config) { cfg.PrivateKey = "" })
	if root.CanSign() {
		t.Fatal("client without a key must not sign")
	}
	var _, err = perps.Trading().CreateOrder(context.Background(), types.OrderRequest{Coin: "BTC", IsBuy: true, Price: fx("86000"), Size: fx("0.001"), TimeInForce: types.TimeInForceGtc}, types.OrderOptions{})
	if !hyperliquid.IsAuth(err) || m.exchangeCount() != 0 {
		t.Fatalf("error = %v, exchange calls = %d", err, m.exchangeCount())
	}
	if _, err = perps.MarketData().GetOrderBook(context.Background(), "BTC", OrderBookOptions{}); err != nil {
		t.Fatalf("public data must work keyless: %v", err)
	}
}

func TestOpenOrdersFilterAndCancelAll(t *testing.T) {
	var m = newMockExchange(t)
	var _, perps = newSection(t, m, nil)
	var ctx = context.Background()

	var orders, err = perps.Trading().GetOpenOrders(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 2 || orders[0].Coin != "BTC" || orders[1].Coin != "ETH" {
		t.Fatalf("section filter failed: %+v", orders)
	}
	if orders[0].Cloid.UUID() != "12345678-90ab-cdef-1234-567890abcdef" || orders[0].Tif != "Alo" || orders[1].Cloid.IsSet() || !orders[1].ReduceOnly {
		t.Fatalf("open order decode: %+v", orders)
	}

	m.setExchangeResponse(`{"status":"ok","response":{"type":"cancel","data":{"statuses":["success","success"]}}}`)
	var statuses []types.ActionStatus
	statuses, err = perps.Trading().CancelAllOrders(ctx, "")
	if err != nil || len(statuses) != 2 {
		t.Fatalf("CancelAllOrders: %v %+v", err, statuses)
	}
	if !strings.Contains(m.lastExchangeBody(), `"cancels":[{"a":0,"o":101},{"a":1,"o":102}]`) {
		t.Fatalf("cancel-all body: %s", m.lastExchangeBody())
	}

	var stale []types.OpenOrder
	stale, _, err = perps.Trading().CancelForgottenOrders(ctx, "", time.Minute)
	if err != nil || len(stale) != 1 || stale[0].Oid != 101 {
		t.Fatalf("CancelForgottenOrders: %v %+v", err, stale)
	}
}

func TestClosePositionAndSlippagePrice(t *testing.T) {
	var m = newMockExchange(t)
	var _, perps = newSection(t, m, nil)
	var ctx = context.Background()

	var mids, midsErr = perps.MarketData().GetAllMids(ctx)
	if midsErr != nil || len(mids) != 3 || mids["kPEPE"] != fx("0.011234") {
		t.Fatalf("GetAllMids must keep the section's coins only and survive junk entries: %v %v", midsErr, mids)
	}

	// BTC mid 86759.5, szDecimals 5 → 1 decimal, 5 significant figures → integers.
	var buy, err = perps.MarketData().SlippagePrice(ctx, "BTC", true, 0.05)
	if err != nil || buy != fx("91098") {
		t.Fatalf("buy slippage price = %s (%v)", buy, err)
	}
	var sell types.Fixed
	sell, err = perps.MarketData().SlippagePrice(ctx, "BTC", false, 0.05)
	if err != nil || sell != fx("82421") {
		t.Fatalf("sell slippage price = %s (%v)", sell, err)
	}

	var position, found, posErr = perps.Account().GetPosition(ctx, "ETH")
	if posErr != nil || !found || position.Szi.String() != "-0.0335" || position.LiquidationPx.Valid || !position.EntryPx.Valid {
		t.Fatalf("position = %+v found=%v err=%v", position, found, posErr)
	}

	m.setExchangeResponse(`{"status":"ok","response":{"type":"order","data":{"statuses":[{"filled":{"totalSz":"0.0335","avgPx":"2990.1","oid":1}}]}}}`)
	var status types.ActionStatus
	status, found, err = perps.Account().ClosePosition(ctx, "ETH", 0, types.Cloid{})
	if err != nil || !found || status.Kind != types.ActionStatusFilled {
		t.Fatalf("ClosePosition: %v found=%v %+v", err, found, status)
	}
	// Short position → buy, reduce-only IOC, mid 2986.35 * 1.05 = 3135.6675 → RoundUp to 3135.7.
	if !strings.Contains(m.lastExchangeBody(), `{"a":1,"b":true,"p":"3135.7","s":"0.0335","r":true,"t":{"limit":{"tif":"Ioc"}}}`) {
		t.Fatalf("close order body: %s", m.lastExchangeBody())
	}

	var before = m.exchangeCount()
	_, found, err = perps.Account().ClosePosition(ctx, "BTC", 0, types.Cloid{})
	if err != nil || found || m.exchangeCount() != before {
		t.Fatalf("no position: found=%v err=%v", found, err)
	}
}

func TestRateLimitAccounting(t *testing.T) {
	var m = newMockExchange(t)
	var mu sync.Mutex
	var events []hyperliquid.RateLimitEvent
	var root, perps = newSection(t, m, func(cfg *hyperliquid.Config) {
		cfg.RateLimitEventObserver = func(e hyperliquid.RateLimitEvent) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		}
	})
	var ctx = context.Background()

	if _, err := perps.MarketData().GetOrderBook(ctx, "BTC", OrderBookOptions{}); err != nil { // meta 20 + l2Book 2
		t.Fatal(err)
	}
	var orders = make([]types.OrderRequest, 40)
	for i := range orders {
		orders[i] = types.OrderRequest{Coin: "ETH", IsBuy: true, Price: fx("2000"), Size: fx("0.01"), TimeInForce: types.TimeInForceAlo}
	}
	if _, err := perps.Trading().CreateBatchOrders(ctx, orders, types.OrderOptions{}); err != nil { // 1 + 40/40 = 2
		t.Fatal(err)
	}
	if got := root.IPWeightUsed(); got != 24 {
		t.Fatalf("IP weight used = %d, want 24", got)
	}
	mu.Lock()
	var last = events[len(events)-1]
	mu.Unlock()
	if last.Kind != "action:order" || last.Weight != 2 || last.OrderCount != 40 || last.UsedWeight != 24 || last.WeightLimit != 1200 || last.Category != hyperliquid.RateLimitCategoryPlace || last.HTTPStatus != 200 {
		t.Fatalf("event = %+v", last)
	}

	var limit, err = perps.Account().GetUserRateLimit(ctx)
	if err != nil || limit.NRequestsCap != 2864574 {
		t.Fatalf("userRateLimit: %v %+v", err, limit)
	}
	var budget = root.AddressBudget()
	if budget.Used != 2890 || budget.Capacity != 2864574 || budget.Remaining() != 2861684 {
		t.Fatalf("budget = %+v", budget)
	}

	var _, guarded = newSection(t, m, func(cfg *hyperliquid.Config) { cfg.RejectWhenRateLimited = true })
	for i := 0; i < 59; i++ { // 59 * 20 = 1180, the next 20-weight request still fits
		if _, err = guarded.MarketData().GetAssetContexts(ctx); i == 0 && err == nil {
			t.Fatal("metaAndAssetCtxs has no fixture: expected a 422 error")
		}
	}
	if err = guarded.RefreshAssets(ctx); err != nil { // 1200
		t.Fatalf("request inside the budget must pass: %v", err)
	}
	if err = guarded.RefreshAssets(ctx); !hyperliquid.IsRateLimit(err) {
		t.Fatalf("exhausted window must fail locally: %v", err)
	}
}

func TestStreamsAndWsPost(t *testing.T) {
	var m = newMockExchange(t)
	m.onWsFrame = func(conn *websocket.Conn, frame string) {
		switch {
		case strings.Contains(frame, `"type":"bbo"`) && strings.Contains(frame, `"subscribe"`):
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"channel":"bbo","data":{"coin":"BTC","time":1758500000555,"bbo":[{"px":"86759.0","sz":"7.25913","n":25},null]}}`))
		case strings.Contains(frame, `"orderUpdates"`):
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"channel":"orderUpdates","data":[
			 {"order":{"coin":"@107","side":"B","limitPx":"40.0","sz":"3","oid":1,"timestamp":1,"origSz":"3"},"status":"open","statusTimestamp":1},
			 {"order":{"coin":"BTC","side":"A","limitPx":"90000.0","sz":"0.0","oid":2,"timestamp":2,"origSz":"0.001","cloid":"0x1234567890abcdef1234567890abcdef"},"status":"filled","statusTimestamp":3}]}`))
		case strings.Contains(frame, `"method":"post"`):
			var id = codec.GetString([]byte(frame), "id")
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"channel":"post","data":{"id":`+id+`,"response":{"type":"action","payload":{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":555}}]}}}}}}`))
		}
	}
	var root, perps = newSection(t, m, nil)
	var ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	var gotBBO = make(chan types.BBO, 1)
	var err = perps.Stream().WatchBBO(ctx, "BTC", func(b *types.BBO) {
		var copied = types.BBO{Coin: b.Coin, TimeMs: b.TimeMs}
		if b.Bid() != nil {
			var bid = *b.Bid()
			copied.Levels[0] = &bid
		}
		copied.Levels[1] = b.Ask()
		select {
		case gotBBO <- copied:
		default:
		}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-gotBBO:
		if b.Coin != "BTC" || b.Bid() == nil || b.Bid().Price != fx("86759") || b.Bid().Orders != 25 || b.Ask() != nil {
			t.Fatalf("bbo = %+v", b)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no bbo push")
	}
	if err = perps.Stream().WatchBBO(ctx, "@107", func(*types.BBO) {}, nil); !hyperliquid.IsInvalidRequest(err) {
		t.Fatalf("foreign coin stream: %v", err)
	}

	var gotUpdates = make(chan types.OrderUpdate, 4)
	var resets atomic.Int64
	err = perps.Stream().WatchOrderUpdates(ctx, func(updates []types.OrderUpdate) {
		for _, u := range updates {
			gotUpdates <- u
		}
	}, func() { resets.Add(1) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case u := <-gotUpdates:
		if u.Order.Coin != "BTC" || u.Status != types.OrderStatusFilled || u.Order.OrigSz != fx("0.001") || !u.Order.Cloid.IsSet() {
			t.Fatalf("order update = %+v", u)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no order update")
	}
	if len(gotUpdates) != 0 {
		t.Fatal("the spot order update must be filtered out")
	}

	if err = root.WarmUpPost(ctx); err != nil {
		t.Fatal(err)
	}
	var status types.ActionStatus
	status, err = perps.Trading().WS().CreateOrder(ctx, types.OrderRequest{Coin: "BTC", IsBuy: true, Price: fx("86000"), Size: fx("0.001"), TimeInForce: types.TimeInForceAlo}, types.OrderOptions{})
	if err != nil || status.Kind != types.ActionStatusResting || status.Oid != 555 {
		t.Fatalf("WS post order: %v %+v", err, status)
	}
	if m.exchangeCount() != 0 {
		t.Fatal("WS trading must not touch REST /exchange")
	}

	_ = root.Close()
	_, err = perps.Trading().WS().CreateOrder(context.Background(), types.OrderRequest{Coin: "BTC", IsBuy: true, Price: fx("86000"), Size: fx("0.001"), TimeInForce: types.TimeInForceAlo}, types.OrderOptions{})
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("after Close a WS order must fail fast with an SDK error, got %v", err)
	}
}
