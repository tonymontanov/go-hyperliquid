/*
FILE: internal/engine/engine.go

DESCRIPTION:
The unified request layer of the SDK — the single place where a request to
Hyperliquid is assembled, signed, sent and decoded. Sections (perpetuals, spot,
hip3, outcomes) NEVER talk to the transports directly and never call each
other: every section method is this layer's function plus the section's own
specifics (asset resolution, precision, dex name).

    section.Trading().CreateOrder ──► Engine.Action ──► REST /exchange | WS post
    section.MarketData().GetBook  ──► Engine.Info   ──► REST /info     | WS post

ACTION PIPELINE (hot path):
  1. nonce          : lock-free generator shared per signer;
  2. MessagePack    : action.AppendMsgpack into a pooled buffer;
  3. hash + sign    : keccak → phantom agent → EIP-712 → secp256k1;
  4. JSON body      : action.AppendJSON into the same pooled buffer;
  5. transport      : REST (default) or WS post;
  6. rate accounting: IP weight window + address budget + observer event;
  7. decode         : {"status":"ok"| "err"} envelope → typed statuses.
Steps 1, 2, 4 and 6 do not allocate; step 3 allocates inside the ECDSA
implementation only (see internal/signing).

REQUEST BODY (mirrors the official Python SDK, which always sends both keys):
  {"action":{...},"nonce":N,"signature":{...},"vaultAddress":null|"0x..","expiresAfter":null|N}

TRANSPORT SELECTION:
REST is the default for everything. WS post is opt-in per call
(ActionOptions.Transport / InfoOptions.Transport) and is never used as a
silent fallback: when the socket is down the call fails fast, because
re-routing or re-sending an order behind the caller's back is unsafe.

DEPENDENCIES:
- internal/action, signing, nonce, rest, ws, ratelimit, codec, hlerr.
*/

package engine

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/tonymontanov/go-hyperliquid/internal/action"
	"github.com/tonymontanov/go-hyperliquid/internal/codec"
	"github.com/tonymontanov/go-hyperliquid/internal/hlerr"
	"github.com/tonymontanov/go-hyperliquid/internal/hllog"
	"github.com/tonymontanov/go-hyperliquid/internal/nonce"
	"github.com/tonymontanov/go-hyperliquid/internal/ratelimit"
	"github.com/tonymontanov/go-hyperliquid/internal/rest"
	"github.com/tonymontanov/go-hyperliquid/internal/signing"
	"github.com/tonymontanov/go-hyperliquid/internal/ws"
	"github.com/tonymontanov/go-hyperliquid/types"
)

// Transport — how a request travels to the exchange.
type Transport uint8

const (
	// TransportREST — HTTP POST (default).
	TransportREST Transport = iota
	// TransportWS — WebSocket post request.
	TransportWS
)

// Rate-limit categories reported to the observer.
const (
	CategoryPlace  string = "place"
	CategoryAmend  string = "amend"
	CategoryCancel string = "cancel"
	CategoryQuery  string = "query"
	CategoryMarket string = "market"
	CategoryOther  string = "other"
)

// Event — rate-limit accounting event emitted after every request.
type Event struct {
	// Kind — "info:<type>" or "action:<type>".
	Kind string
	// Transport — REST or WS.
	Transport Transport
	// HTTPStatus — status of the REST response; 0 for WS posts.
	HTTPStatus int
	// Weight — IP weight charged for this request.
	Weight int64
	// UsedWeight / WeightLimit — state of the one-minute IP window AFTER the
	// request, as accounted by the SDK (the exchange sends no headers).
	UsedWeight  int64
	WeightLimit int64
	// OrderCount — address-based cost of the request (0 for info).
	OrderCount int
	// Category — place / amend / cancel / query / market / other.
	Category string
}

// Config — engine wiring. Built by the root Client.
type Config struct {
	REST      *rest.Client
	PostConn  func() *ws.Conn // lazy accessor of the post connection
	Signer    *signing.Signer
	IsMainnet bool
	// Vault — optional sub-account / vault address sent as vaultAddress.
	Vault *signing.Address
	// PostTimeout — how long a WS post waits for its reply.
	PostTimeout time.Duration
	// RejectWhenExhausted — fail REST/WS requests locally with a RateLimit
	// error when the SDK-side IP window has no room for their weight.
	RejectWhenExhausted bool
	Observer            func(Event)
	Logger              hllog.Logger
}

// Engine — unified request layer. Safe for concurrent use.
type Engine struct {
	cfg     Config
	nonces  *nonce.Generator
	window  *ratelimit.Window
	budget  ratelimit.AddressBudget
	buffers sync.Pool
}

// New creates the engine.
func New(cfg Config) *Engine {
	if cfg.Logger == nil {
		cfg.Logger = hllog.Noop()
	}
	var generator *nonce.Generator = nonce.New()
	if cfg.Signer.Enabled() {
		generator = nonce.ForSigner([20]byte(cfg.Signer.Address()))
	}
	var e *Engine = &Engine{
		cfg:    cfg,
		nonces: generator,
		window: ratelimit.NewWindow(ratelimit.IPWeightLimitPerMinute),
	}
	e.buffers.New = func() any {
		var b []byte = make([]byte, 0, 2048)
		return &b
	}
	return e
}

// Window returns the SDK-side IP weight window.
func (e *Engine) Window() *ratelimit.Window { return e.window }

// Budget returns the local mirror of the address-based budget.
func (e *Engine) Budget() *ratelimit.AddressBudget { return &e.budget }

// NextNonce returns a fresh nonce of the configured signer. Exposed for the
// "noop with the same nonce" cancellation technique.
func (e *Engine) NextNonce() uint64 { return e.nonces.Next() }

// IsMainnet reports the configured network.
func (e *Engine) IsMainnet() bool { return e.cfg.IsMainnet }

// SignerAddress returns the address of the signing wallet (zero if disabled).
func (e *Engine) SignerAddress() signing.Address { return e.cfg.Signer.Address() }

// Vault returns the configured vault / sub-account address or nil.
func (e *Engine) Vault() *signing.Address { return e.cfg.Vault }

// account emits the rate-limit event and updates the SDK-side counters.
func (e *Engine) account(kind string, transport Transport, status int, weight int64, orderCount int, category string) {
	var used int64 = e.window.Add(weight)
	e.budget.Consume(int64(orderCount))
	if e.cfg.Observer == nil {
		return
	}
	e.cfg.Observer(Event{
		Kind:        kind,
		Transport:   transport,
		HTTPStatus:  status,
		Weight:      weight,
		UsedWeight:  used,
		WeightLimit: e.window.Limit(),
		OrderCount:  orderCount,
		Category:    category,
	})
}

// admit applies the optional local fail-fast guard. scope and name are joined
// only on the failure path so the hot path performs no string concatenation.
func (e *Engine) admit(scope string, name string, weight int64) error {
	if !e.cfg.RejectWhenExhausted {
		return nil
	}
	if e.window.Used()+weight > e.window.Limit() {
		return hlerr.New(hlerr.ErrorKindRateLimit, scope+"."+name+": SDK-side IP weight window exhausted", nil)
	}
	return nil
}

// actionKind returns the observer kind of an action type. Known types map to
// constants so that the order hot path does not allocate a string per call.
func actionKind(actionType string) string {
	switch actionType {
	case "order":
		return "action:order"
	case "cancel":
		return "action:cancel"
	case "cancelByCloid":
		return "action:cancelByCloid"
	case "modify":
		return "action:modify"
	case "batchModify":
		return "action:batchModify"
	case "scheduleCancel":
		return "action:scheduleCancel"
	case "noop":
		return "action:noop"
	default:
		return "action:" + actionType
	}
}

// InfoOptions — per-call options of an info request.
type InfoOptions struct {
	Transport Transport
	// Category — CategoryQuery (default) or CategoryMarket.
	Category string
}

/*
Info performs an info request.

infoType is the value of the "type" field (used for weights and diagnostics);
body is the complete JSON request object; dest receives the decoded response
(any JSON shape). items, when not nil, is called with dest after decoding and
must return the number of returned items — used for the info types whose
weight depends on the response size.
*/
func (e *Engine) Info(ctx context.Context, infoType string, body []byte, dest any, opts InfoOptions, items func() int) error {
	var weight int64 = ratelimit.InfoWeight(infoType)
	var err error = e.admit("info", infoType, weight)
	if err != nil {
		return err
	}
	var category string = opts.Category
	if category == "" {
		category = CategoryQuery
	}
	var kind string = "info:" + infoType

	var payload []byte
	if opts.Transport == TransportWS {
		payload, err = e.postWS(ctx, ws.PostTypeInfo, body)
		e.account(kind, TransportWS, 0, weight, 0, category)
		if err != nil {
			return err
		}
		// WS wraps the info response as {"type":"<infoType>","data":<response>}.
		var wrapped struct {
			Data codec.RawMessage `json:"data"`
		}
		err = codec.Unmarshal(payload, &wrapped)
		if err != nil {
			return hlerr.New(hlerr.ErrorKindUnknown, "info."+infoType+": parse ws envelope", err)
		}
		payload = wrapped.Data
	} else {
		var status int
		payload, err = e.cfg.REST.PostInfo(ctx, body, rest.RequestMeta{Kind: kind, Weight: weight, Category: category})
		status = statusOf(err)
		e.account(kind, TransportREST, status, weight, 0, category)
		if err != nil {
			return err
		}
	}

	if dest != nil {
		err = codec.Unmarshal(payload, dest)
		if err != nil {
			return hlerr.New(hlerr.ErrorKindUnknown, "info."+infoType+": parse response", err)
		}
	}
	if items != nil {
		var extra int64 = ratelimit.InfoItemsWeight(infoType, items())
		if extra > 0 {
			e.account(kind+":items", opts.Transport, 0, extra, 0, category)
		}
	}
	return nil
}

// statusOf extracts the HTTP status of a transport error (200 when err is nil).
func statusOf(err error) int {
	if err == nil {
		return 200
	}
	var e *hlerr.Error
	if errors.As(err, &e) {
		return e.HTTPStatus
	}
	return 0
}

// ActionOptions — per-call options of an exchange action.
type ActionOptions struct {
	Transport Transport
	// Nonce — explicit nonce; 0 → generate. Used by "noop with the same nonce".
	Nonce uint64
	// ExpiresAfterMs — reject the action after this timestamp (ms); 0 → unset.
	// A stale expiresAfter costs 5x on the address-based limit (docs).
	ExpiresAfterMs uint64
	// Category — rate-limit category of the action.
	Category string
}

// ActionResult — decoded successful action response.
type ActionResult struct {
	// Nonce — the nonce the action was signed with.
	Nonce uint64
	// ResponseType — "order", "cancel", "default", ...
	ResponseType string
	// Statuses — per-item outcomes (empty for "default" responses).
	Statuses []types.ActionStatus
}

/*
Action signs and sends an exchange action and decodes the reply.

Errors:
  - *hlerr.Error with Kind Network / RateLimit / InvalidRequest (transport);
  - *hlerr.Error built from the exchange text when the WHOLE action was
    rejected ({"status":"err"});
  - per-item rejections are NOT errors of the call: they are reported in
    ActionResult.Statuses with Kind == ActionStatusError.
*/
func (e *Engine) Action(ctx context.Context, act action.Action, opts ActionOptions) (ActionResult, error) {
	var result ActionResult
	var actionType string = act.Type()
	if !e.cfg.Signer.Enabled() {
		return result, hlerr.New(hlerr.ErrorKindAuth, "action."+actionType+": private key is not configured", signing.ErrSignerDisabled)
	}
	var batchLen int = act.BatchLen()
	var weight int64 = ratelimit.ActionWeight(batchLen)
	var err error = e.admit("action", actionType, weight)
	if err != nil {
		return result, err
	}

	var nonceValue uint64 = opts.Nonce
	if nonceValue == 0 {
		nonceValue = e.nonces.Next()
	}
	result.Nonce = nonceValue

	var pooled *[]byte = e.buffers.Get().(*[]byte)
	var buf []byte = (*pooled)[:0]

	// Steps 2-3: MessagePack → hash → signature.
	buf = act.AppendMsgpack(buf)
	var sig signing.Signature
	sig, buf, err = e.cfg.Signer.SignL1Action(buf, nonceValue, e.cfg.Vault, opts.ExpiresAfterMs, opts.ExpiresAfterMs != 0, e.cfg.IsMainnet)
	if err != nil {
		*pooled = buf
		e.buffers.Put(pooled)
		return result, hlerr.New(hlerr.ErrorKindAuth, "action."+actionType+": sign", err)
	}

	// Step 4: JSON body, reusing the same buffer.
	buf = buf[:0]
	buf = append(buf, `{"action":`...)
	buf = act.AppendJSON(buf)
	buf = append(buf, `,"nonce":`...)
	buf = strconv.AppendUint(buf, nonceValue, 10)
	buf = append(buf, `,"signature":`...)
	buf = sig.AppendJSON(buf)
	buf = append(buf, `,"vaultAddress":`...)
	if e.cfg.Vault == nil {
		buf = append(buf, "null"...)
	} else {
		buf = append(buf, '"')
		buf = e.cfg.Vault.AppendHex(buf)
		buf = append(buf, '"')
	}
	buf = append(buf, `,"expiresAfter":`...)
	if opts.ExpiresAfterMs == 0 {
		buf = append(buf, "null"...)
	} else {
		buf = strconv.AppendUint(buf, opts.ExpiresAfterMs, 10)
	}
	buf = append(buf, '}')

	var category string = opts.Category
	if category == "" {
		category = CategoryOther
	}
	var kind string = actionKind(actionType)

	// Step 5: transport.
	var payload []byte
	if opts.Transport == TransportWS {
		payload, err = e.postWS(ctx, ws.PostTypeAction, buf)
		e.account(kind, TransportWS, 0, weight, batchLen, category)
	} else {
		payload, err = e.cfg.REST.PostExchange(ctx, buf, rest.RequestMeta{Kind: kind, Weight: weight, OrderCount: batchLen, Category: category})
		e.account(kind, TransportREST, statusOf(err), weight, batchLen, category)
	}
	*pooled = buf
	e.buffers.Put(pooled)
	if err != nil {
		return result, err
	}

	// Step 7: decode.
	err = decodeActionResponse(payload, &result)
	return result, err
}

// postWS sends a post request over the post connection and maps its failures.
func (e *Engine) postWS(ctx context.Context, requestType string, body []byte) ([]byte, error) {
	if e.cfg.PostConn == nil {
		return nil, hlerr.New(hlerr.ErrorKindInvalidRequest, "ws post: transport is not configured", nil)
	}
	var conn *ws.Conn = e.cfg.PostConn()
	var reply ws.PostResult
	var err error
	reply, err = conn.Post(ctx, requestType, body, e.cfg.PostTimeout)
	if err != nil {
		return nil, hlerr.New(hlerr.ErrorKindNetwork, "ws post: "+requestType, err)
	}
	if reply.Type == ws.PostTypeError {
		// Payload is a JSON string mirroring the HTTP failure, e.g.
		// "429 Too Many Requests" or "422 Unprocessable Entity: ...".
		var text string
		if unmarshalErr := codec.Unmarshal(reply.Payload, &text); unmarshalErr != nil {
			text = string(reply.Payload)
		}
		var status int = leadingStatus(text)
		var kind hlerr.ErrorKind = hlerr.MapHTTPStatus(status)
		if status == 0 {
			kind = hlerr.ErrorKindInvalidRequest
		}
		var postErr *hlerr.Error = hlerr.New(kind, "ws post: "+text, nil)
		postErr.HTTPStatus = status
		return nil, postErr
	}
	return reply.Payload, nil
}

// leadingStatus parses a leading 3-digit HTTP status ("429 Too Many ..."), or 0.
func leadingStatus(text string) int {
	if len(text) < 3 {
		return 0
	}
	var value int
	var i int
	for i = 0; i < 3; i++ {
		if text[i] < '0' || text[i] > '9' {
			return 0
		}
		value = value*10 + int(text[i]-'0')
	}
	if len(text) > 3 && text[3] >= '0' && text[3] <= '9' {
		return 0
	}
	return value
}
