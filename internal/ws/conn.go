/*
FILE: internal/ws/conn.go

DESCRIPTION:
Supervised WebSocket connection to the Hyperliquid /ws endpoint. One Conn can
carry subscriptions AND post requests; the SDK nevertheless opens two of them
(stream + post) so that a burst of market data can never delay the reply to an
order (head-of-line blocking inside one TCP stream).

Hyperliquid WS needs no login: user-scoped subscriptions only name the user
address, and post actions carry their own signature. Public and private
traffic therefore share the same code path.

RESPONSIBILITIES:
 1. supervise      : dial → run → on failure sleep (backoff * 2, capped, with
                     jitter) → redial, until ctx is cancelled or Close is called.
 2. subscriptions  : registry keyed by route key. Subscribing while
                     disconnected is legal — the registry is replayed on every
                     (re)connect, after calling each subscription's Reset hook.
 3. keepalive      : the server drops a connection it has not written to for
                     60 s, so the client sends {"method":"ping"} every
                     PingInterval; every received frame refreshes the read
                     deadline (silent-server detector).
 4. post requests  : Post() correlates a request with its reply by id. Requests
                     are NEVER buffered across reconnects: if the socket is down
                     the call fails fast with ErrConnNotReady, and every pending
                     request fails with ErrDisconnected when the socket dies —
                     silently re-sending an order after a reconnect would be
                     dangerous.

CONTRACT OF Subscription.Handler:
  - called sequentially from the read loop of the connection;
  - data is only valid during the call (the buffer is reused for the next
    frame) — decode it or copy it, never retain it;
  - must not block: a slow handler delays every other subscription of the Conn.
CONTRACT OF Subscription.Reset:
  - called on the connection goroutine under c.mu before (re)subscribing: it
    must be fast and non-blocking (drop local state, signal a channel).

DEPENDENCIES:
- github.com/gorilla/websocket: WS client.
- internal/codec, internal/hllog, internal/hlmet.
*/

package ws

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tonymontanov/go-hyperliquid/internal/codec"
	"github.com/tonymontanov/go-hyperliquid/internal/hllog"
	"github.com/tonymontanov/go-hyperliquid/internal/hlmet"
)

// Sentinel errors of the connection.
var (
	// ErrConnClosed — the Conn was closed permanently.
	ErrConnClosed error = errors.New("ws: connection closed")
	// ErrConnNotReady — no live socket right now (connecting / reconnecting).
	ErrConnNotReady error = errors.New("ws: connection is not ready")
	// ErrPostTimeout — no reply to a post request within the timeout. The
	// request MAY still have been processed by the exchange.
	ErrPostTimeout error = errors.New("ws: post request timed out")
	// ErrDisconnected — the socket died while a post request was pending. The
	// request MAY still have been processed by the exchange.
	ErrDisconnected error = errors.New("ws: disconnected while waiting for a post reply")
	// ErrInvalidSubscription — a Subscription is missing mandatory fields.
	ErrInvalidSubscription error = errors.New("ws: invalid subscription")
	// ErrSubscriptionConflict — the route key is already subscribed with a
	// DIFFERENT payload (pushes of the two would be indistinguishable).
	ErrSubscriptionConflict error = errors.New("ws: route key is already subscribed with a different payload")
)

// pingFrame — client keepalive frame.
var pingFrame []byte = []byte(`{"method":"ping"}`)

// Config — connection settings.
type Config struct {
	URL                     string
	HandshakeTimeout        time.Duration
	ReadTimeout             time.Duration
	WriteTimeout            time.Duration
	PingInterval            time.Duration
	ReconnectInitialBackoff time.Duration
	ReconnectMaxBackoff     time.Duration
	ReconnectJitter         float64
	ReadBufferSize          int
	WriteBufferSize         int
	// Proxy — optional proxy selector. nil → http.ProxyFromEnvironment.
	Proxy func(*http.Request) (*url.URL, error)
	// OnServerError — optional hook for {"channel":"error"} frames.
	OnServerError func(text string)
}

// Subscription — one registered subscription.
type Subscription struct {
	// Key — route key (see RouteKey); unique within a Conn.
	Key string
	// Payload — the JSON object sent as "subscription", e.g.
	// {"type":"l2Book","coin":"BTC"}.
	Payload []byte
	// Handler — receives the "data" part of every matching push.
	Handler func(data []byte)
	// Reset — optional; see the contract in the file header.
	Reset func()
}

// subscriptionGroup — every subscription sharing one route key. The exchange
// sees one subscription (payload); members is copy-on-write.
type subscriptionGroup struct {
	payload []byte
	members []*Subscription
}

// PostResult — reply to a post request.
type PostResult struct {
	// Type — "info", "action" or "error".
	Type string
	// Payload — owned copy of the response payload.
	Payload []byte
}

// pendingPost — a post request waiting for its reply.
type pendingPost struct {
	reply chan PostResult
	err   chan error
}

// Conn — supervised WS connection. Safe for concurrent use.
type Conn struct {
	cfg    Config
	logger hllog.Logger

	mu     sync.RWMutex
	subs   map[string]*subscriptionGroup
	socket *websocket.Conn
	closed bool

	writeMu   sync.Mutex
	startOnce sync.Once
	cancel    context.CancelFunc

	pendingMu sync.Mutex
	pending   map[uint64]pendingPost
	postSeq   atomic.Uint64

	cReceived    hlmet.Counter
	cDropped     hlmet.Counter
	cReconn      hlmet.Counter
	cSub         hlmet.Counter
	cPostTimeout hlmet.Counter
	cPostOrphan  hlmet.Counter
}

// NewConn creates a connection object. It performs no network I/O.
func NewConn(cfg Config, logger hllog.Logger, metrics hlmet.CounterFactory) *Conn {
	if logger == nil {
		logger = hllog.Noop()
	}
	if metrics == nil {
		metrics = hlmet.Noop()
	}
	return &Conn{
		cfg:          cfg,
		logger:       logger,
		subs:         make(map[string]*subscriptionGroup, 16),
		pending:      make(map[uint64]pendingPost, 16),
		cReceived:    metrics.Counter("hyperliquid_ws_messages_received_total"),
		cDropped:     metrics.Counter("hyperliquid_ws_messages_dropped_total"),
		cReconn:      metrics.Counter("hyperliquid_ws_reconnects_total"),
		cSub:         metrics.Counter("hyperliquid_ws_subscriptions_total"),
		cPostTimeout: metrics.Counter("hyperliquid_ws_post_timeout_total"),
		cPostOrphan:  metrics.Counter("hyperliquid_ws_post_orphan_total"),
	}
}

// Start launches the supervise loop. Idempotent: only the first call has
// effect. The connection lives until ctx is cancelled or Close is called.
func (c *Conn) Start(ctx context.Context) {
	c.startOnce.Do(func() {
		var superviseCtx context.Context
		var cancel context.CancelFunc
		superviseCtx, cancel = context.WithCancel(ctx)
		c.mu.Lock()
		c.cancel = cancel
		c.mu.Unlock()
		go c.supervise(superviseCtx)
	})
}

// Close terminates the connection permanently. Safe to call multiple times.
func (c *Conn) Close() {
	c.mu.Lock()
	c.closed = true
	var socket *websocket.Conn = c.socket
	var cancel context.CancelFunc = c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if socket != nil {
		_ = socket.Close()
	}
	c.failAllPending(ErrConnClosed)
}

// IsConnected reports whether a live socket exists right now.
func (c *Conn) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.socket != nil
}

// EnsureReady blocks until the socket is live, ctx is done or the Conn is closed.
func (c *Conn) EnsureReady(ctx context.Context) error {
	var ticker *time.Ticker = time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		c.mu.RLock()
		var ready bool = c.socket != nil
		var closed bool = c.closed
		c.mu.RUnlock()
		if closed {
			return ErrConnClosed
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

/*
Subscribe registers a subscription. Buffered semantics: the registration is
stored first, so subscribing while disconnected is legal — it is applied on the
next (re)connect. If a socket is live, the subscribe frame is sent immediately.

SHARED KEYS: several subscriptions may share one route key (two consumers of
the same "trades:BTC" feed). The exchange is subscribed ONCE — by the first of
them — and every push is delivered to all handlers, in registration order. A
later subscription with the same key must carry the same payload; a different
one (e.g. another l2Book aggregation of the same coin, which the protocol
cannot tell apart) is rejected with ErrSubscriptionConflict instead of silently
stealing or sharing the wrong feed.
*/
func (c *Conn) Subscribe(sub *Subscription) error {
	if sub == nil || sub.Key == "" || len(sub.Payload) == 0 || sub.Handler == nil {
		return ErrInvalidSubscription
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrConnClosed
	}
	var group *subscriptionGroup = c.subs[sub.Key]
	var first bool = group == nil
	if first {
		group = &subscriptionGroup{payload: sub.Payload}
		c.subs[sub.Key] = group
	} else if string(group.payload) != string(sub.Payload) {
		c.mu.Unlock()
		return ErrSubscriptionConflict
	}
	// Copy-on-write: dispatch iterates a slice header taken under RLock.
	var members []*Subscription = make([]*Subscription, 0, len(group.members)+1)
	members = append(members, group.members...)
	group.members = append(members, sub)
	var socket *websocket.Conn = c.socket
	c.mu.Unlock()

	if !first {
		return nil
	}
	c.cSub.Inc()
	if socket == nil {
		return nil // will subscribe on connect
	}
	return c.writeFrame(socket, subscriptionFrame("subscribe", sub.Payload))
}

// Unsubscribe removes ONE subscription (the pointer passed to Subscribe). The
// unsubscribe frame is sent when the last subscription of the key is gone.
// Unknown subscriptions are a no-op.
func (c *Conn) Unsubscribe(sub *Subscription) error {
	if sub == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrConnClosed
	}
	var group *subscriptionGroup = c.subs[sub.Key]
	if group == nil {
		c.mu.Unlock()
		return nil
	}
	var members []*Subscription = make([]*Subscription, 0, len(group.members))
	var i int
	for i = 0; i < len(group.members); i++ {
		if group.members[i] != sub {
			members = append(members, group.members[i])
		}
	}
	group.members = members
	var last bool = len(members) == 0
	if last {
		delete(c.subs, sub.Key)
	}
	var socket *websocket.Conn = c.socket
	c.mu.Unlock()

	if !last || socket == nil {
		return nil
	}
	return c.writeFrame(socket, subscriptionFrame("unsubscribe", group.payload))
}

// subscriptionFrame builds {"method":"<method>","subscription":<payload>}.
func subscriptionFrame(method string, payload []byte) []byte {
	var frame []byte = make([]byte, 0, len(payload)+48)
	frame = append(frame, `{"method":"`...)
	frame = append(frame, method...)
	frame = append(frame, `","subscription":`...)
	frame = append(frame, payload...)
	return append(frame, '}')
}

/*
Post sends a post request and waits for its reply.

requestType is PostTypeInfo or PostTypeAction; payload is the JSON object that
would be the HTTP body of the corresponding REST call. A reply of type
PostTypeError is returned as a normal PostResult — the caller owns the mapping
to SDK errors because it knows what was sent.

Fails fast with ErrConnNotReady when there is no live socket (no buffering
across reconnects). On ErrPostTimeout / ErrDisconnected the outcome on the
exchange side is UNKNOWN.
*/
func (c *Conn) Post(ctx context.Context, requestType string, payload []byte, timeout time.Duration) (PostResult, error) {
	c.mu.RLock()
	var socket *websocket.Conn = c.socket
	var closed bool = c.closed
	c.mu.RUnlock()
	if closed {
		return PostResult{}, ErrConnClosed
	}
	if socket == nil {
		return PostResult{}, ErrConnNotReady
	}

	var id uint64 = c.postSeq.Add(1)
	var waiter pendingPost = pendingPost{reply: make(chan PostResult, 1), err: make(chan error, 1)}

	// Register BEFORE writing: the reply can arrive before WriteMessage returns.
	c.pendingMu.Lock()
	c.pending[id] = waiter
	c.pendingMu.Unlock()

	var frame []byte = make([]byte, 0, len(payload)+96)
	frame = append(frame, `{"method":"post","id":`...)
	frame = strconv.AppendUint(frame, id, 10)
	frame = append(frame, `,"request":{"type":"`...)
	frame = append(frame, requestType...)
	frame = append(frame, `","payload":`...)
	frame = append(frame, payload...)
	frame = append(frame, '}', '}')

	var err error = c.writeFrame(socket, frame)
	if err != nil {
		c.dropPending(id)
		return PostResult{}, fmt.Errorf("ws: write post: %w", err)
	}

	var timer *time.Timer = time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-waiter.reply:
		return result, nil
	case err = <-waiter.err:
		return PostResult{}, err
	case <-timer.C:
		c.dropPending(id)
		c.cPostTimeout.Inc()
		return PostResult{}, ErrPostTimeout
	case <-ctx.Done():
		c.dropPending(id)
		return PostResult{}, ctx.Err()
	}
}

// dropPending forgets a pending post (timeout / cancellation / write error).
func (c *Conn) dropPending(id uint64) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

// failAllPending fails every pending post with err.
func (c *Conn) failAllPending(err error) {
	c.pendingMu.Lock()
	var id uint64
	var waiter pendingPost
	for id, waiter = range c.pending {
		waiter.err <- err
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()
}

// supervise — reconnect loop with backoff + jitter.
func (c *Conn) supervise(ctx context.Context) {
	var backoff time.Duration = c.cfg.ReconnectInitialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		var connected bool
		var err error
		connected, err = c.connectAndRun(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			c.logger.Warn("ws: connection error, will reconnect", hllog.Err(err))
		}
		c.cReconn.Inc()
		if connected {
			// The previous attempt reached a live socket: start the next
			// series of attempts from the initial delay again.
			backoff = c.cfg.ReconnectInitialBackoff
		}

		var sleep time.Duration = applyJitter(backoff, c.cfg.ReconnectJitter)
		select {
		case <-ctx.Done():
			return
		case <-time.After(sleep):
		}
		backoff = nextBackoff(backoff, c.cfg.ReconnectMaxBackoff)
	}
}

// connectAndRun dials, replays subscriptions and runs the read loop until the
// socket dies or ctx is cancelled. connected reports whether the dial succeeded.
func (c *Conn) connectAndRun(ctx context.Context) (bool, error) {
	var proxy func(*http.Request) (*url.URL, error) = c.cfg.Proxy
	if proxy == nil {
		proxy = http.ProxyFromEnvironment
	}
	var dialer websocket.Dialer = websocket.Dialer{
		Proxy:            proxy,
		HandshakeTimeout: c.cfg.HandshakeTimeout,
		ReadBufferSize:   c.cfg.ReadBufferSize,
		WriteBufferSize:  c.cfg.WriteBufferSize,
	}
	var socket *websocket.Conn
	var err error
	socket, _, err = dialer.DialContext(ctx, c.cfg.URL, nil)
	if err != nil {
		return false, fmt.Errorf("dial: %w", err)
	}

	// Publish the socket and reset stateful subscribers BEFORE any frame of
	// the new connection is processed, so a reconnect cannot mix old and new
	// state.
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = socket.Close()
		return true, nil
	}
	c.socket = socket
	var payloads [][]byte = make([][]byte, 0, len(c.subs))
	var group *subscriptionGroup
	for _, group = range c.subs {
		var m int
		for m = 0; m < len(group.members); m++ {
			if group.members[m].Reset != nil {
				group.members[m].Reset()
			}
		}
		payloads = append(payloads, group.payload)
	}
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.socket = nil
		c.mu.Unlock()
		_ = socket.Close()
		c.failAllPending(ErrDisconnected)
	}()

	var i int
	for i = 0; i < len(payloads); i++ {
		err = c.writeFrame(socket, subscriptionFrame("subscribe", payloads[i]))
		if err != nil {
			return true, fmt.Errorf("resubscribe: %w", err)
		}
	}

	// loopCtx stops the ping loop and the ctx watcher when the read loop exits.
	var loopCtx context.Context
	var stopLoops context.CancelFunc
	loopCtx, stopLoops = context.WithCancel(ctx)
	defer stopLoops()

	go c.pingLoop(loopCtx, socket)
	go func() {
		// Close the socket when ctx is cancelled so the blocking ReadMessage
		// returns promptly. Also runs on normal exit, where Close is a no-op
		// repeated by the deferred cleanup above.
		<-loopCtx.Done()
		_ = socket.Close()
	}()

	return true, c.readLoop(socket)
}

// pingLoop sends the keepalive frame every PingInterval.
func (c *Conn) pingLoop(ctx context.Context, socket *websocket.Conn) {
	var ticker *time.Ticker = time.NewTicker(c.cfg.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var err error = c.writeFrame(socket, pingFrame)
			if err != nil {
				// A failed write means the socket is dead: closing it makes
				// the read loop return and the supervisor reconnect.
				_ = socket.Close()
				return
			}
		}
	}
}

// readLoop reads and dispatches frames until the socket dies.
func (c *Conn) readLoop(socket *websocket.Conn) error {
	// One envelope per connection: codec.RawMessage.UnmarshalJSON copies into
	// env.Data[:0], so the payload buffer is reused from frame to frame.
	var env envelope
	for {
		var err error = socket.SetReadDeadline(time.Now().Add(c.cfg.ReadTimeout))
		if err != nil {
			return err
		}
		var msgType int
		var frame []byte
		msgType, frame, err = socket.ReadMessage()
		if err != nil {
			return err
		}
		if msgType != websocket.TextMessage {
			continue
		}
		c.cReceived.Inc()

		env.Channel = ""
		env.Data = env.Data[:0]
		err = codec.Unmarshal(frame, &env)
		if err != nil || env.Channel == "" {
			// Not an envelope: the plain-text greeting
			// "Websocket connection established." or noise.
			c.cDropped.Inc()
			continue
		}
		c.dispatch(&env)
	}
}

// dispatch routes one envelope.
func (c *Conn) dispatch(env *envelope) {
	switch env.Channel {
	case channelPong, channelSubscriptionResponse:
		return
	case channelPost:
		c.dispatchPost(env.Data)
		return
	case channelError:
		var text string
		if err := codec.Unmarshal(env.Data, &text); err != nil {
			text = string(env.Data)
		}
		c.logger.Warn("ws: server error frame", hllog.Str("text", text))
		if c.cfg.OnServerError != nil {
			c.cfg.OnServerError(text)
		}
		return
	}

	var key string = pushRouteKey(env.Channel, env.Data)
	c.mu.RLock()
	var group *subscriptionGroup = c.subs[key]
	var members []*Subscription
	if group != nil {
		// Copy-on-write slice: safe to iterate after releasing the lock.
		members = group.members
	}
	c.mu.RUnlock()
	if len(members) == 0 {
		c.cDropped.Inc()
		return
	}
	var i int
	for i = 0; i < len(members); i++ {
		members[i].Handler(env.Data)
	}
}

// dispatchPost delivers a post reply to its waiter.
func (c *Conn) dispatchPost(data []byte) {
	var reply postData
	var err error = codec.Unmarshal(data, &reply)
	if err != nil {
		c.cDropped.Inc()
		return
	}
	c.pendingMu.Lock()
	var waiter pendingPost
	var ok bool
	waiter, ok = c.pending[reply.ID]
	if ok {
		delete(c.pending, reply.ID)
	}
	c.pendingMu.Unlock()
	if !ok {
		// The caller already gave up (timeout / ctx): the reply is an orphan.
		c.cPostOrphan.Inc()
		return
	}
	// reply.Response.Payload was decoded into its own buffer (postData is a
	// fresh value per reply), so handing it over needs no extra copy.
	waiter.reply <- PostResult{Type: reply.Response.Type, Payload: reply.Response.Payload}
}

// writeFrame writes one text frame under the write mutex.
func (c *Conn) writeFrame(socket *websocket.Conn, frame []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	var err error = socket.SetWriteDeadline(time.Now().Add(c.cfg.WriteTimeout))
	if err != nil {
		return err
	}
	return socket.WriteMessage(websocket.TextMessage, frame)
}

// applyJitter multiplies d by a random factor in [1-j, 1+j].
func applyJitter(d time.Duration, jitter float64) time.Duration {
	if jitter <= 0 {
		return d
	}
	var factor float64 = 1 + jitter*(2*rand.Float64()-1)
	return time.Duration(float64(d) * factor)
}

// nextBackoff doubles the backoff up to the max.
func nextBackoff(d time.Duration, maxBackoff time.Duration) time.Duration {
	d *= 2
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}
