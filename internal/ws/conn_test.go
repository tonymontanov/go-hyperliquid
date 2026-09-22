/*
FILE: internal/ws/conn_test.go

DESCRIPTION:
Tests of the supervised WS connection against an in-process mock server
(httptest + gorilla upgrader). No network access. Covered: the greeting frame
is skipped, subscribe frames and routing keys, resubscribe + Reset after a
reconnect, post correlation, fail-fast and fail-pending semantics, keepalive.
*/

package ws

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tonymontanov/go-hyperliquid/internal/codec"
)

// mockServer — scriptable WS server. onFrame is invoked for every client frame.
type mockServer struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	conns    []*websocket.Conn
	frames   []string
	onFrame  func(conn *websocket.Conn, frame string)
	accepted atomic.Int64
}

func newMockServer(t *testing.T, onFrame func(conn *websocket.Conn, frame string)) *mockServer {
	var m = &mockServer{t: t, onFrame: onFrame}
	var upgrader = websocket.Upgrader{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var conn, err = upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		m.accepted.Add(1)
		m.mu.Lock()
		m.conns = append(m.conns, conn)
		m.mu.Unlock()
		// Real server greets with a plain-text frame.
		_ = conn.WriteMessage(websocket.TextMessage, []byte("Websocket connection established."))
		for {
			var _, frame, readErr = conn.ReadMessage()
			if readErr != nil {
				return
			}
			m.mu.Lock()
			m.frames = append(m.frames, string(frame))
			m.mu.Unlock()
			if m.onFrame != nil {
				m.onFrame(conn, string(frame))
			}
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockServer) url() string { return "ws" + strings.TrimPrefix(m.srv.URL, "http") }

func (m *mockServer) framesSnapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.frames...)
}

func (m *mockServer) dropAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, conn := range m.conns {
		_ = conn.Close()
	}
	m.conns = nil
}

func testConfig(url string) Config {
	return Config{
		URL:                     url,
		HandshakeTimeout:        2 * time.Second,
		ReadTimeout:             5 * time.Second,
		WriteTimeout:            2 * time.Second,
		PingInterval:            50 * time.Millisecond,
		ReconnectInitialBackoff: 10 * time.Millisecond,
		ReconnectMaxBackoff:     50 * time.Millisecond,
		ReconnectJitter:         0.1,
		ReadBufferSize:          4096,
		WriteBufferSize:         4096,
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	var deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func TestSubscribeRoutingAndGreeting(t *testing.T) {
	var server = newMockServer(t, func(conn *websocket.Conn, frame string) {
		if strings.Contains(frame, `"subscribe"`) && strings.Contains(frame, `"l2Book"`) {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"channel":"subscriptionResponse","data":{"method":"subscribe","subscription":{"type":"l2Book","coin":"BTC"}}}`))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"channel":"l2Book","data":{"coin":"ETH","time":1,"levels":[[],[]]}}`))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"channel":"l2Book","data":{"coin":"BTC","time":2,"levels":[[],[]]}}`))
		}
	})
	var conn = NewConn(testConfig(server.url()), nil, nil)
	var ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	conn.Start(ctx)
	defer conn.Close()

	var got atomic.Value
	var err = conn.Subscribe(&Subscription{
		Key:     RouteKey("l2Book", "BTC"),
		Payload: []byte(`{"type":"l2Book","coin":"BTC"}`),
		Handler: func(data []byte) { got.Store(string(data)) },
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "BTC book push", func() bool { return got.Load() != nil })
	if coin := codec.GetString([]byte(got.Load().(string)), "coin"); coin != "BTC" {
		t.Fatalf("handler received a push of another coin: %s", coin)
	}
	var frames = server.framesSnapshot()
	if len(frames) == 0 || frames[0] != `{"method":"subscribe","subscription":{"type":"l2Book","coin":"BTC"}}` {
		t.Fatalf("unexpected subscribe frame: %v", frames)
	}
}

func TestPushRouteKeys(t *testing.T) {
	var cases = []struct {
		channel string
		data    string
		want    string
	}{
		{"l2Book", `{"coin":"kPEPE"}`, "l2Book:kPEPE"},
		{"bbo", `{"coin":"@107"}`, "bbo:@107"},
		{"trades", `[{"coin":"xyz:XYZ100","side":"B"}]`, "trades:xyz:XYZ100"},
		{"candle", `{"t":1,"T":2,"s":"BTC","i":"1m"}`, "candle:BTC,1m"},
		{"activeAssetCtx", `{"coin":"BTC","ctx":{}}`, "activeAssetCtx:BTC"},
		{"activeSpotAssetCtx", `{"coin":"@107","ctx":{}}`, "activeAssetCtx:@107"},
		{"user", `{"fills":[]}`, "userEvents"},
		{"orderUpdates", `[]`, "orderUpdates"},
		{"userFills", `{"user":"0xabc","fills":[]}`, "userFills"},
	}
	for _, c := range cases {
		if got := pushRouteKey(c.channel, []byte(c.data)); got != c.want {
			t.Errorf("pushRouteKey(%s) = %q, want %q", c.channel, got, c.want)
		}
	}
	if RouteKey("candle", "BTC,1m") != "candle:BTC,1m" || RouteKey("orderUpdates", "") != "orderUpdates" {
		t.Error("RouteKey must mirror pushRouteKey")
	}
}

func TestReconnectResubscribesAndResets(t *testing.T) {
	var server = newMockServer(t, nil)
	var conn = NewConn(testConfig(server.url()), nil, nil)
	var ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	conn.Start(ctx)
	defer conn.Close()

	var resets atomic.Int64
	var err = conn.Subscribe(&Subscription{
		Key:     RouteKey("bbo", "BTC"),
		Payload: []byte(`{"type":"bbo","coin":"BTC"}`),
		Handler: func([]byte) {},
		Reset:   func() { resets.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first subscribe frame", func() bool { return len(server.framesSnapshot()) >= 1 })

	server.dropAll()
	waitFor(t, "second connection", func() bool { return server.accepted.Load() >= 2 })
	waitFor(t, "resubscribe frame", func() bool {
		var count int
		for _, frame := range server.framesSnapshot() {
			if strings.Contains(frame, `"subscribe"`) {
				count++
			}
		}
		return count >= 2
	})
	if resets.Load() < 1 {
		t.Fatal("Reset must run before the resubscribe")
	}
}

func TestPostCorrelationAndErrors(t *testing.T) {
	var server = newMockServer(t, func(conn *websocket.Conn, frame string) {
		if !strings.Contains(frame, `"method":"post"`) {
			return
		}
		var id = codec.GetString([]byte(frame), "id")
		switch {
		case strings.Contains(frame, `"silent"`):
			return // never reply → timeout
		case strings.Contains(frame, `"drop"`):
			_ = conn.Close() // die with the request pending
		case strings.Contains(frame, `"bad"`):
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"channel":"post","data":{"id":`+id+`,"response":{"type":"error","payload":"422 Unprocessable"}}}`))
		default:
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"channel":"post","data":{"id":`+id+`,"response":{"type":"action","payload":{"status":"ok","response":{"type":"default"}}}}}`))
		}
	})
	var conn = NewConn(testConfig(server.url()), nil, nil)
	var ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	if _, err := conn.Post(ctx, PostTypeAction, []byte(`{}`), time.Second); !errors.Is(err, ErrConnNotReady) {
		t.Fatalf("Post before connect: error = %v, want ErrConnNotReady", err)
	}

	conn.Start(ctx)
	defer conn.Close()
	if err := conn.EnsureReady(ctx); err != nil {
		t.Fatal(err)
	}

	var result, err = conn.Post(ctx, PostTypeAction, []byte(`{"action":{"type":"noop"}}`), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.Type != PostTypeAction || !strings.Contains(string(result.Payload), `"status":"ok"`) {
		t.Fatalf("unexpected result: %+v", result)
	}
	var frames = server.framesSnapshot()
	if want := `{"method":"post","id":1,"request":{"type":"action","payload":{"action":{"type":"noop"}}}}`; frames[len(frames)-1] != want {
		t.Fatalf("post frame\n got %s\nwant %s", frames[len(frames)-1], want)
	}

	result, err = conn.Post(ctx, PostTypeInfo, []byte(`{"type":"bad"}`), time.Second)
	if err != nil || result.Type != PostTypeError || string(result.Payload) != `"422 Unprocessable"` {
		t.Fatalf("error reply: result=%+v err=%v", result, err)
	}

	if _, err = conn.Post(ctx, PostTypeInfo, []byte(`{"type":"silent"}`), 60*time.Millisecond); !errors.Is(err, ErrPostTimeout) {
		t.Fatalf("silent post: error = %v, want ErrPostTimeout", err)
	}

	if _, err = conn.Post(ctx, PostTypeAction, []byte(`{"type":"drop"}`), 2*time.Second); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("dropped post: error = %v, want ErrDisconnected", err)
	}
}

func TestKeepaliveAndClose(t *testing.T) {
	var pings atomic.Int64
	var server = newMockServer(t, func(conn *websocket.Conn, frame string) {
		if frame == `{"method":"ping"}` {
			pings.Add(1)
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"channel":"pong"}`))
		}
	})
	var conn = NewConn(testConfig(server.url()), nil, nil)
	conn.Start(context.Background())
	waitFor(t, "two pings", func() bool { return pings.Load() >= 2 })

	conn.Close()
	conn.Close() // idempotent
	if err := conn.Subscribe(&Subscription{Key: "allMids", Payload: []byte(`{"type":"allMids"}`), Handler: func([]byte) {}}); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("Subscribe after Close: error = %v", err)
	}
	if err := conn.EnsureReady(context.Background()); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("EnsureReady after Close: error = %v", err)
	}
	if err := conn.Subscribe(nil); !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("Subscribe(nil): error = %v", err)
	}
}

func TestSharedRouteKey(t *testing.T) {
	// The mock pushes one trades frame per client ping, from its own read
	// goroutine (gorilla connections allow a single concurrent writer).
	var server = newMockServer(t, func(conn *websocket.Conn, frame string) {
		if frame == `{"method":"ping"}` {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"channel":"trades","data":[{"coin":"BTC","side":"B","px":"1","sz":"1","time":1,"hash":"0x","tid":1,"users":["a","b"]}]}`))
		}
	})
	var conn = NewConn(testConfig(server.url()), nil, nil)
	var ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	conn.Start(ctx)
	defer conn.Close()

	var first, second atomic.Int64
	var payload = []byte(`{"type":"trades","coin":"BTC"}`)
	var subA = &Subscription{Key: RouteKey("trades", "BTC"), Payload: payload, Handler: func([]byte) { first.Add(1) }}
	var subB = &Subscription{Key: RouteKey("trades", "BTC"), Payload: payload, Handler: func([]byte) { second.Add(1) }}
	if err := conn.Subscribe(subA); err != nil {
		t.Fatal(err)
	}
	if err := conn.Subscribe(subB); err != nil {
		t.Fatal(err)
	}
	var conflicting = &Subscription{Key: RouteKey("trades", "BTC"), Payload: []byte(`{"type":"trades","coin":"BTC","x":1}`), Handler: func([]byte) {}}
	if err := conn.Subscribe(conflicting); !errors.Is(err, ErrSubscriptionConflict) {
		t.Fatalf("different payload on a shared key: error = %v", err)
	}
	waitFor(t, "both consumers", func() bool { return first.Load() >= 2 && second.Load() >= 2 })
	if first.Load() != second.Load() && first.Load()+1 != second.Load() && first.Load() != second.Load()+1 {
		t.Fatalf("consumers of one key must see the same pushes: %d vs %d", first.Load(), second.Load())
	}

	if err := conn.Unsubscribe(subA); err != nil {
		t.Fatal(err)
	}
	var frozen = first.Load()
	var secondBefore = second.Load()
	waitFor(t, "remaining consumer keeps receiving", func() bool { return second.Load() >= secondBefore+3 })
	if first.Load() > frozen+1 { // one push may have been in flight during Unsubscribe
		t.Fatalf("an unsubscribed consumer keeps receiving pushes: %d → %d", frozen, first.Load())
	}
	if err := conn.Unsubscribe(subB); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "single subscribe + single unsubscribe frame", func() bool {
		var subscribes, unsubscribes int
		for _, frame := range server.framesSnapshot() {
			if strings.Contains(frame, `"method":"subscribe"`) {
				subscribes++
			}
			if strings.Contains(frame, `"method":"unsubscribe"`) {
				unsubscribes++
			}
		}
		return subscribes == 1 && unsubscribes == 1
	})
}
