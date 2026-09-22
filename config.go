/*
FILE: config.go

DESCRIPTION:
config.go defines the SDK configuration structs (see spec §5.5) together with
the mainnet / testnet endpoints and default values.

MAIN FUNCTIONS:
  - DefaultConfig(): returns Config with mainnet endpoints and default values
    for timeouts / reconnect / keepalive.
  - (Config).withDefaults(): fills empty Config fields with defaults. Inside
    the SDK the config is ALWAYS passed through withDefaults() first; the
    caller's struct is never mutated.

ENDPOINTS:
  mainnet : https://api.hyperliquid.xyz          wss://api.hyperliquid.xyz/ws
  testnet : https://api.hyperliquid-testnet.xyz  wss://api.hyperliquid-testnet.xyz/ws
Config.Testnet selects the pair AND the signing source of L1 actions ("a" on
mainnet, "b" on testnet) — a mainnet signature is rejected by the testnet and
vice versa. Explicitly set URLs are never overridden (mock servers, proxies).

AUTHENTICATION (wallet model — there is no API key / secret):
  - AccountAddress : address of the MASTER account that is traded. Used as the
                     "user" of account-scoped info requests. When an API wallet
                     signs, the exchange derives the account from the wallet's
                     approval, so the address is never part of an action.
  - PrivateKey     : hex private key of the signing wallet. Use an API wallet
                     (agent) approved by the master account; the master key
                     itself also works but should stay off trading hosts.
  - VaultAddress   : optional sub-account or vault to trade on behalf of. When
                     set it is sent as "vaultAddress" with every action and it
                     replaces AccountAddress as the "user" of info requests
                     (the docs: querying with the agent or master address
                     returns an empty result for a sub-account).
Info requests must never use the API wallet's own address: the exchange
answers with an empty state instead of an error.

DEPENDENCIES:
Standard library:
  - time: timeouts and reconnect/keepalive intervals.
  - net/http, net/url: optional proxy / custom HTTP client injection.
*/

package hyperliquid

import (
	"net/http"
	"net/url"
	"time"
)

// Hyperliquid transport URLs. Declared as vars rather than const so tests can
// override them (e.g. to point at a mock server).
var (
	// MainnetRestURL — production REST base URL (/info, /exchange).
	MainnetRestURL string = "https://api.hyperliquid.xyz"
	// MainnetWsURL — production WebSocket URL.
	MainnetWsURL string = "wss://api.hyperliquid.xyz/ws"
	// TestnetRestURL — testnet REST base URL.
	TestnetRestURL string = "https://api.hyperliquid-testnet.xyz"
	// TestnetWsURL — testnet WebSocket URL.
	TestnetWsURL string = "wss://api.hyperliquid-testnet.xyz/ws"
)

// Config — public SDK configuration. Passed to NewClient.
type Config struct {
	// AccountAddress — master account address ("0x" + 40 hex, any case).
	// Optional for public market data; required for account-scoped requests
	// unless VaultAddress is set or the PrivateKey is the master key itself.
	AccountAddress string
	// PrivateKey — signing wallet private key, hex with optional 0x prefix.
	// Empty → the client can only access public and read-only endpoints.
	// Never logged, never echoed in errors.
	PrivateKey string
	// VaultAddress — optional sub-account / vault address (see file header).
	VaultAddress string

	// Testnet — selects testnet endpoints and the testnet signing source.
	Testnet bool

	// REST — REST transport settings. Empty fields take DefaultConfig().REST.
	REST RestConfig
	// WS — WebSocket transport settings. Empty fields take DefaultConfig().WS.
	WS WsConfig

	// AssetRefreshInterval — how often sections refresh exchange metadata
	// (asset ids, szDecimals). Asset ids differ between mainnet and testnet
	// and new assets are listed continuously. Default: 5m. Negative disables
	// the background refresh (metadata is then loaded once, on first use).
	AssetRefreshInterval time.Duration

	// Logger — optional logger. If nil, NoopLogger() is used.
	Logger Logger
	// Metrics — optional counter factory. If nil, NoopMetrics() is used.
	Metrics CounterFactory
	// UserAgent — User-Agent of REST requests. Default: "go-hyperliquid/v1".
	UserAgent string

	// RateLimitEventObserver — optional hook called SYNCHRONOUSLY after every
	// request with the SDK-side rate-limit accounting (see rate-limit-event.go
	// for the contract). If nil — no-op, zero overhead.
	RateLimitEventObserver func(RateLimitEvent)
	// RejectWhenRateLimited — when true, a request whose weight does not fit
	// into the SDK-side one-minute IP window fails locally with
	// ErrorKindRateLimit instead of being sent. Default: false (the desk's own
	// rate limiter decides).
	RejectWhenRateLimited bool
}

// RestConfig — HTTP transport settings.
type RestConfig struct {
	// BaseURL — REST base URL. Default: MainnetRestURL (TestnetRestURL when
	// Config.Testnet).
	BaseURL string
	// RequestTimeout — timeout of a single REST request. Default: 10s. For
	// latency-critical calls pass a ctx with its own deadline. Note that
	// /exchange answers only after the action is included in a block.
	RequestTimeout time.Duration
	// MaxIdleConns — idle connection pool size. Default: 100.
	MaxIdleConns int
	// MaxIdleConnsPerHost — pool size per host. Default: 100.
	MaxIdleConnsPerHost int
	// IdleConnTimeout — keep-alive idle timeout. Default: 90s.
	IdleConnTimeout time.Duration
	// Proxy — optional proxy selector. Default: http.ProxyFromEnvironment
	// (HTTP_PROXY / HTTPS_PROXY / NO_PROXY are honoured).
	Proxy func(*http.Request) (*url.URL, error)
	// HTTPClient — optional fully custom *http.Client (own TLS config, dialer,
	// transport). When set, the pool / proxy / timeout fields are ignored.
	HTTPClient *http.Client
}

// WsConfig — WebSocket transport settings.
type WsConfig struct {
	// URL — WebSocket URL. Default: MainnetWsURL (TestnetWsURL when Testnet).
	URL string
	// HandshakeTimeout — connection handshake timeout. Default: 10s.
	HandshakeTimeout time.Duration
	// ReadTimeout — read deadline of a single frame; refreshed by every frame
	// including pong. Must exceed PingInterval. Default: 45s.
	ReadTimeout time.Duration
	// WriteTimeout — write timeout of a single frame. Default: 5s.
	WriteTimeout time.Duration
	// PingInterval — client keepalive period. The server closes a connection
	// it has not written to for 60s. Default: 20s.
	PingInterval time.Duration
	// ReconnectInitialBackoff — initial delay between reconnects. Default: 200ms.
	ReconnectInitialBackoff time.Duration
	// ReconnectMaxBackoff — upper bound of the backoff. Default: 10s.
	ReconnectMaxBackoff time.Duration
	// ReconnectJitter — relative jitter [0..1] applied to the backoff. Default: 0.2.
	ReconnectJitter float64
	// ReadBufferSize — gorilla/websocket read buffer size. Default: 64KB.
	ReadBufferSize int
	// WriteBufferSize — gorilla/websocket write buffer size. Default: 16KB.
	WriteBufferSize int
	// PostTimeout — how long a WS post request waits for its reply. Default: 10s.
	PostTimeout time.Duration
	// Proxy — optional proxy selector. Default: http.ProxyFromEnvironment.
	Proxy func(*http.Request) (*url.URL, error)
}

// DefaultConfig returns a Config with all sensible defaults (mainnet
// endpoints + production timeouts).
func DefaultConfig() Config {
	return Config{
		REST: RestConfig{
			BaseURL:             MainnetRestURL,
			RequestTimeout:      10 * time.Second,
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     90 * time.Second,
		},
		WS: WsConfig{
			URL:                     MainnetWsURL,
			HandshakeTimeout:        10 * time.Second,
			ReadTimeout:             45 * time.Second,
			WriteTimeout:            5 * time.Second,
			PingInterval:            20 * time.Second,
			ReconnectInitialBackoff: 200 * time.Millisecond,
			ReconnectMaxBackoff:     10 * time.Second,
			ReconnectJitter:         0.2,
			ReadBufferSize:          64 * 1024,
			WriteBufferSize:         16 * 1024,
			PostTimeout:             10 * time.Second,
		},
		AssetRefreshInterval: 5 * time.Minute,
		Logger:               NoopLogger(),
		Metrics:              NoopMetrics(),
		UserAgent:            "go-hyperliquid/v1",
	}
}

// withDefaults returns a Config where all empty fields are filled with values
// from DefaultConfig(). Used inside NewClient — the user-supplied Config is
// never mutated.
func (c Config) withDefaults() Config {
	var def Config = DefaultConfig()

	// Endpoints: for Testnet use the testnet hosts by default. If the user
	// explicitly set a URL — do NOT override it.
	var defRest string = def.REST.BaseURL
	var defWs string = def.WS.URL
	if c.Testnet {
		defRest = TestnetRestURL
		defWs = TestnetWsURL
	}
	// DefaultConfig() pre-fills the MAINNET URLs. A caller who starts from it
	// and only flips Testnet must end up on the testnet — otherwise requests
	// signed for the testnet would be sent to mainnet. So the mainnet default
	// counts as "not set" when Testnet is on; any other URL is kept.
	if c.REST.BaseURL == "" || (c.Testnet && c.REST.BaseURL == MainnetRestURL) {
		c.REST.BaseURL = defRest
	}
	if c.REST.RequestTimeout == 0 {
		c.REST.RequestTimeout = def.REST.RequestTimeout
	}
	if c.REST.MaxIdleConns == 0 {
		c.REST.MaxIdleConns = def.REST.MaxIdleConns
	}
	if c.REST.MaxIdleConnsPerHost == 0 {
		c.REST.MaxIdleConnsPerHost = def.REST.MaxIdleConnsPerHost
	}
	if c.REST.IdleConnTimeout == 0 {
		c.REST.IdleConnTimeout = def.REST.IdleConnTimeout
	}

	if c.WS.URL == "" || (c.Testnet && c.WS.URL == MainnetWsURL) {
		c.WS.URL = defWs
	}
	if c.WS.HandshakeTimeout == 0 {
		c.WS.HandshakeTimeout = def.WS.HandshakeTimeout
	}
	if c.WS.ReadTimeout == 0 {
		c.WS.ReadTimeout = def.WS.ReadTimeout
	}
	if c.WS.WriteTimeout == 0 {
		c.WS.WriteTimeout = def.WS.WriteTimeout
	}
	if c.WS.PingInterval == 0 {
		c.WS.PingInterval = def.WS.PingInterval
	}
	if c.WS.ReconnectInitialBackoff == 0 {
		c.WS.ReconnectInitialBackoff = def.WS.ReconnectInitialBackoff
	}
	if c.WS.ReconnectMaxBackoff == 0 {
		c.WS.ReconnectMaxBackoff = def.WS.ReconnectMaxBackoff
	}
	if c.WS.ReconnectJitter == 0 {
		c.WS.ReconnectJitter = def.WS.ReconnectJitter
	}
	if c.WS.ReadBufferSize == 0 {
		c.WS.ReadBufferSize = def.WS.ReadBufferSize
	}
	if c.WS.WriteBufferSize == 0 {
		c.WS.WriteBufferSize = def.WS.WriteBufferSize
	}
	if c.WS.PostTimeout == 0 {
		c.WS.PostTimeout = def.WS.PostTimeout
	}

	if c.AssetRefreshInterval == 0 {
		c.AssetRefreshInterval = def.AssetRefreshInterval
	}
	if c.Logger == nil {
		c.Logger = NoopLogger()
	}
	if c.Metrics == nil {
		c.Metrics = NoopMetrics()
	}
	if c.UserAgent == "" {
		c.UserAgent = def.UserAgent
	}
	return c
}

// validate checks the transport fields. Credentials are validated in NewClient
// (they need parsing); they are optional because public data works keyless.
func (c Config) validate() error {
	if c.REST.BaseURL == "" {
		return NewError(ErrorKindInvalidRequest, "config: REST.BaseURL is empty", nil)
	}
	if c.WS.URL == "" {
		return NewError(ErrorKindInvalidRequest, "config: WS.URL is empty", nil)
	}
	if c.WS.ReadTimeout <= c.WS.PingInterval {
		return NewError(ErrorKindInvalidRequest, "config: WS.ReadTimeout must exceed WS.PingInterval", nil)
	}
	return nil
}
