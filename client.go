/*
FILE: client.go

DESCRIPTION:
The root SDK Client — the public face of the COMMON layer. It owns everything
that is shared by all trading sections:
  - the signer (private key) and the per-signer nonce generator;
  - the REST transport and the two lazy WebSocket connections
    (stream: subscriptions, post: requests);
  - the unified request engine with SDK-side rate-limit accounting.

Sections are separate packages built ON TOP of the Client:

	var client *hyperliquid.Client
	client, err = hyperliquid.NewClient(cfg)
	var perps *perpetuals.Client = perpetuals.NewClient(client)

The section imports the root; the root never imports a section, so there is no
import cycle and — unlike the sibling SDKs — no `any` + type assertion on the
public API: the section constructor is an ordinary, compile-time checked
function. Importing only the sections you trade keeps the binary small.

LIFETIME:
WebSocket connections belong to the Client, not to the first Watch* call: they
are started with an internal context that lives until Close(). Cancelling the
ctx of a Watch* call unsubscribes THAT subscription only.

MAIN FUNCTIONS:
  - NewClient(cfg)          : constructor with Config validation and defaults.
  - (Client).Close()        : stops WS connections, releases idle HTTP
                              connections and zeroes the private key.
  - (Client).IPWeightUsed / AddressBudget : SDK-side rate-limit state.
  - (Client).Engine / StreamConn / UserAddress ... : plumbing for the section
                              packages of this module (they return internal
                              types, so code outside the module cannot use them).

DEPENDENCIES:
- internal/engine, internal/rest, internal/ws, internal/signing.
*/

package hyperliquid

import (
	"context"
	"sync"

	"github.com/tonymontanov/go-hyperliquid/internal/engine"
	"github.com/tonymontanov/go-hyperliquid/internal/rest"
	"github.com/tonymontanov/go-hyperliquid/internal/signing"
	"github.com/tonymontanov/go-hyperliquid/internal/ws"
)

// Client — root SDK object. Safe for concurrent use.
type Client struct {
	cfg    Config
	signer *signing.Signer
	rest   *rest.Client
	engine *engine.Engine
	logger Logger

	// user — address used as "user" in account-scoped info requests.
	user    signing.Address
	hasUser bool
	vault   *signing.Address

	lifeCtx    context.Context
	lifeCancel context.CancelFunc

	streamOnce sync.Once
	streamConn *ws.Conn
	postOnce   sync.Once
	postConn   *ws.Conn

	closeOnce sync.Once
}

// NewClient creates the root SDK client. cfg goes through withDefaults +
// validate. With an empty PrivateKey the client is read-only: market data and
// info requests work, every action returns an ErrorKindAuth error.
func NewClient(cfg Config) (*Client, error) {
	cfg = cfg.withDefaults()
	var err error = cfg.validate()
	if err != nil {
		return nil, err
	}

	var signer *signing.Signer
	signer, err = signing.NewSigner(cfg.PrivateKey)
	if err != nil {
		// err is a fixed sentinel: it never carries key material.
		return nil, NewError(ErrorKindAuth, "config: invalid PrivateKey", err)
	}

	var client *Client = &Client{cfg: cfg, signer: signer, logger: cfg.Logger}
	// The key now lives inside the signer only.
	client.cfg.PrivateKey = ""

	if cfg.VaultAddress != "" {
		var vault signing.Address
		vault, err = signing.ParseAddress(cfg.VaultAddress)
		if err != nil {
			return nil, NewError(ErrorKindInvalidRequest, "config: invalid VaultAddress", err)
		}
		client.vault = &vault
	}

	// "user" of info requests: vault/sub-account → master account → signer.
	switch {
	case client.vault != nil:
		client.user = *client.vault
		client.hasUser = true
	case cfg.AccountAddress != "":
		client.user, err = signing.ParseAddress(cfg.AccountAddress)
		if err != nil {
			return nil, NewError(ErrorKindInvalidRequest, "config: invalid AccountAddress", err)
		}
		client.hasUser = true
	case signer.Enabled():
		client.user = signer.Address()
		client.hasUser = true
	}
	if cfg.AccountAddress != "" && client.vault != nil {
		// Validate the master address even when the vault takes precedence.
		_, err = signing.ParseAddress(cfg.AccountAddress)
		if err != nil {
			return nil, NewError(ErrorKindInvalidRequest, "config: invalid AccountAddress", err)
		}
	}

	client.lifeCtx, client.lifeCancel = context.WithCancel(context.Background())

	client.rest = rest.NewClient(rest.Config{
		BaseURL:             cfg.REST.BaseURL,
		RequestTimeout:      cfg.REST.RequestTimeout,
		MaxIdleConns:        cfg.REST.MaxIdleConns,
		MaxIdleConnsPerHost: cfg.REST.MaxIdleConnsPerHost,
		IdleConnTimeout:     cfg.REST.IdleConnTimeout,
		UserAgent:           cfg.UserAgent,
		Proxy:               cfg.REST.Proxy,
		HTTPClient:          cfg.REST.HTTPClient,
	}, cfg.Logger)

	client.engine = engine.New(engine.Config{
		REST:                client.rest,
		PostConn:            client.PostConn,
		Signer:              signer,
		IsMainnet:           !cfg.Testnet,
		Vault:               client.vault,
		PostTimeout:         cfg.WS.PostTimeout,
		RejectWhenExhausted: cfg.RejectWhenRateLimited,
		Observer:            cfg.RateLimitEventObserver,
		Logger:              cfg.Logger,
	})
	return client, nil
}

// Config returns a copy of the final config (after withDefaults). PrivateKey
// is always empty in the returned value.
func (c *Client) Config() Config { return c.cfg }

// Logger returns the current logger.
func (c *Client) Logger() Logger { return c.logger }

// IsTestnet reports whether the client targets the testnet.
func (c *Client) IsTestnet() bool { return c.cfg.Testnet }

// CanSign reports whether a private key is configured.
func (c *Client) CanSign() bool { return c.signer.Enabled() }

// SignerAddress returns the lower-case address of the signing wallet, or ""
// when no key is configured.
func (c *Client) SignerAddress() string {
	if !c.signer.Enabled() {
		return ""
	}
	return c.signer.Address().Hex()
}

// UserAddress returns the lower-case address used as "user" in account-scoped
// info requests and user-scoped subscriptions, or "" when none is configured.
func (c *Client) UserAddress() string {
	if !c.hasUser {
		return ""
	}
	return c.user.Hex()
}

// IPWeightUsed returns the IP weight consumed by THIS client within the last
// minute, as accounted by the SDK (limit: IPWeightLimitPerMinute).
func (c *Client) IPWeightUsed() int64 { return c.engine.Window().Used() }

// AddressBudget returns the local mirror of the address-based request budget.
// It is authoritative only right after a userRateLimit sync performed by a
// section (Account().GetUserRateLimit).
func (c *Client) AddressBudget() AddressBudgetSnapshot { return c.engine.Budget().Snapshot() }

// LifeContext returns the context that lives until Close(). Sections bind
// their background work (metadata refresh) to it.
func (c *Client) LifeContext() context.Context { return c.lifeCtx }

// Engine returns the unified request layer. For the section packages of this
// module; the type is internal, so external code cannot use it.
func (c *Client) Engine() *engine.Engine { return c.engine }

// wsConfig builds the transport config shared by both WS connections.
func (c *Client) wsConfig() ws.Config {
	return ws.Config{
		URL:                     c.cfg.WS.URL,
		HandshakeTimeout:        c.cfg.WS.HandshakeTimeout,
		ReadTimeout:             c.cfg.WS.ReadTimeout,
		WriteTimeout:            c.cfg.WS.WriteTimeout,
		PingInterval:            c.cfg.WS.PingInterval,
		ReconnectInitialBackoff: c.cfg.WS.ReconnectInitialBackoff,
		ReconnectMaxBackoff:     c.cfg.WS.ReconnectMaxBackoff,
		ReconnectJitter:         c.cfg.WS.ReconnectJitter,
		ReadBufferSize:          c.cfg.WS.ReadBufferSize,
		WriteBufferSize:         c.cfg.WS.WriteBufferSize,
		Proxy:                   c.cfg.WS.Proxy,
	}
}

// StreamConn returns the shared subscription connection, creating and
// starting it on first use. For the section packages of this module.
func (c *Client) StreamConn() *ws.Conn {
	c.streamOnce.Do(func() {
		c.streamConn = ws.NewConn(c.wsConfig(), c.cfg.Logger, c.cfg.Metrics)
		c.streamConn.Start(c.lifeCtx)
	})
	return c.streamConn
}

// PostConn returns the shared post-request connection, creating and starting
// it on first use. For the section packages of this module.
func (c *Client) PostConn() *ws.Conn {
	c.postOnce.Do(func() {
		c.postConn = ws.NewConn(c.wsConfig(), c.cfg.Logger, c.cfg.Metrics)
		c.postConn.Start(c.lifeCtx)
	})
	return c.postConn
}

// WarmUpPost opens the post connection and waits until it is ready. Call it
// once before trading over WS post: the first order then does not pay for the
// TCP + TLS + WS handshake.
func (c *Client) WarmUpPost(ctx context.Context) error {
	return c.PostConn().EnsureReady(ctx)
}

// Close stops the WebSocket connections, releases idle HTTP connections and
// zeroes the private key. Safe to call multiple times.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		c.lifeCancel()
		// Resolve the lazy connections so that a first use after Close gets a
		// closed (never started) connection whose methods return
		// ws.ErrConnClosed instead of a nil pointer.
		c.streamOnce.Do(func() {
			c.streamConn = ws.NewConn(c.wsConfig(), c.cfg.Logger, c.cfg.Metrics)
		})
		c.postOnce.Do(func() {
			c.postConn = ws.NewConn(c.wsConfig(), c.cfg.Logger, c.cfg.Metrics)
		})
		c.streamConn.Close()
		c.postConn.Close()
		c.rest.Close()
		c.signer.Close()
	})
	return nil
}
