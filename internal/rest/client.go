/*
FILE: internal/rest/client.go

DESCRIPTION:
HTTP transport of the SDK. Hyperliquid exposes exactly two REST endpoints, both
POST with a JSON body:
  - /info     : public and account data; the request kind is the "type" field;
  - /exchange : signed actions.
This package knows nothing about actions, signing or sections — it moves bytes,
classifies transport-level failures and reports rate-limit metadata.

RESPONSIBILITIES:
 1. Own a tuned http.Client (keep-alive pool, HTTP/2, optional proxy) or use a
    caller-supplied one (Config.HTTPClient).
 2. POST a prepared body and return the raw response body.
 3. Map non-200 statuses to *hlerr.Error (429 → RateLimit, 5xx → Network, ...).
    Hyperliquid answers such requests with plain text or an empty body, so the
    text (truncated) becomes the error message.
 4. Call the rate-limit observer SYNCHRONOUSLY after every received response.

ERROR STRATEGY:
  - ctx cancellation / deadline, dial and read errors → ErrorKindNetwork.
  - HTTP status != 200                                 → hlerr.MapHTTPStatus.
  - HTTP 200 → body returned as is; {"status":"err"} envelopes are interpreted
    one level up (internal/engine), where the action context is known.

SECURITY NOTES:
Request bodies of /exchange contain signatures. The transport never logs bodies.

DEPENDENCIES:
- internal/hlerr, internal/hllog.
*/

package rest

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tonymontanov/go-hyperliquid/internal/hlerr"
	"github.com/tonymontanov/go-hyperliquid/internal/hllog"
)

const (
	// PathInfo — info endpoint path.
	PathInfo string = "/info"
	// PathExchange — exchange endpoint path.
	PathExchange string = "/exchange"

	// maxErrorBodyBytes — how much of a non-200 body is kept in the error text.
	maxErrorBodyBytes int = 512
	// maxResponseBytes — hard cap of a response body (spotMeta on testnet is
	// ~450 KB; historical fills can reach a few MB).
	maxResponseBytes int64 = 64 << 20
)

// RequestMeta — rate-limit metadata of one request, supplied by the caller.
type RequestMeta struct {
	// Kind — "info:<type>" or "action:<type>", e.g. "info:l2Book", "action:order".
	Kind string
	// Weight — IP weight charged for the request (see internal/ratelimit).
	Weight int64
	// OrderCount — number of batched items of an action (address-based cost);
	// 0 for info requests.
	OrderCount int
	// Category — coarse category: place / amend / cancel / query / market.
	Category string
}

// Observer — rate-limit hook. Called synchronously after every received HTTP
// response (any status). Not called when no response arrived.
type Observer func(path string, status int, meta RequestMeta)

// Config — transport settings.
type Config struct {
	BaseURL             string
	RequestTimeout      time.Duration
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	IdleConnTimeout     time.Duration
	UserAgent           string
	// Proxy — optional proxy selector. nil → http.ProxyFromEnvironment.
	Proxy func(*http.Request) (*url.URL, error)
	// HTTPClient — optional fully custom client; when set, the pool / proxy
	// fields above are ignored.
	HTTPClient *http.Client
	Observer   Observer
}

// Client — REST transport. Safe for concurrent use.
type Client struct {
	cfg         Config
	httpClient  *http.Client
	ownsClient  bool
	infoURL     string
	exchangeURL string
	logger      hllog.Logger
}

// NewClient builds the transport. It performs no network I/O.
func NewClient(cfg Config, logger hllog.Logger) *Client {
	if logger == nil {
		logger = hllog.Noop()
	}
	var base string = strings.TrimRight(cfg.BaseURL, "/")
	var client *Client = &Client{
		cfg:         cfg,
		infoURL:     base + PathInfo,
		exchangeURL: base + PathExchange,
		logger:      logger,
	}
	if cfg.HTTPClient != nil {
		client.httpClient = cfg.HTTPClient
		return client
	}

	var proxy func(*http.Request) (*url.URL, error) = cfg.Proxy
	if proxy == nil {
		proxy = http.ProxyFromEnvironment
	}
	var transport *http.Transport = &http.Transport{
		Proxy:               proxy,
		MaxIdleConns:        cfg.MaxIdleConns,
		MaxIdleConnsPerHost: cfg.MaxIdleConnsPerHost,
		IdleConnTimeout:     cfg.IdleConnTimeout,
		ForceAttemptHTTP2:   true,
	}
	client.httpClient = &http.Client{Transport: transport, Timeout: cfg.RequestTimeout}
	client.ownsClient = true
	return client
}

// Close releases idle connections of the SDK-owned client.
func (c *Client) Close() {
	if c == nil || !c.ownsClient {
		return
	}
	c.httpClient.CloseIdleConnections()
}

// PostInfo sends a prepared JSON body to /info and returns the response body.
func (c *Client) PostInfo(ctx context.Context, body []byte, meta RequestMeta) ([]byte, error) {
	return c.post(ctx, c.infoURL, PathInfo, body, meta)
}

// PostExchange sends a prepared JSON body to /exchange and returns the
// response body.
func (c *Client) PostExchange(ctx context.Context, body []byte, meta RequestMeta) ([]byte, error) {
	return c.post(ctx, c.exchangeURL, PathExchange, body, meta)
}

// post performs one POST round trip.
func (c *Client) post(ctx context.Context, fullURL string, path string, body []byte, meta RequestMeta) ([]byte, error) {
	var req *http.Request
	var err error
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(body))
	if err != nil {
		return nil, hlerr.New(hlerr.ErrorKindInvalidRequest, "rest: build request", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", c.cfg.UserAgent)
	}

	var resp *http.Response
	resp, err = c.httpClient.Do(req)
	if err != nil {
		return nil, hlerr.New(hlerr.ErrorKindNetwork, "rest: "+path+": request failed", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if c.cfg.Observer != nil {
		c.cfg.Observer(path, resp.StatusCode, meta)
	}

	var payload []byte
	payload, err = readBody(resp)
	if err != nil {
		var readErr *hlerr.Error = hlerr.New(hlerr.ErrorKindNetwork, "rest: "+path+": read response", err)
		readErr.HTTPStatus = resp.StatusCode
		return nil, readErr
	}

	if resp.StatusCode != http.StatusOK {
		var text string = string(payload)
		if len(text) > maxErrorBodyBytes {
			text = text[:maxErrorBodyBytes]
		}
		var statusErr *hlerr.Error = hlerr.New(hlerr.MapHTTPStatus(resp.StatusCode), "rest: "+path+": "+strings.TrimSpace(text), nil)
		statusErr.HTTPStatus = resp.StatusCode
		return nil, statusErr
	}
	return payload, nil
}

// readBody reads the whole body, pre-sizing the buffer from Content-Length.
func readBody(resp *http.Response) ([]byte, error) {
	var limited io.Reader = io.LimitReader(resp.Body, maxResponseBytes)
	if resp.ContentLength > 0 && resp.ContentLength < maxResponseBytes {
		var buf *bytes.Buffer = bytes.NewBuffer(make([]byte, 0, resp.ContentLength+1))
		var err error
		_, err = buf.ReadFrom(limited)
		return buf.Bytes(), err
	}
	return io.ReadAll(limited)
}
