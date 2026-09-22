/*
FILE: perpetuals/client.go

DESCRIPTION:
Section client of Hyperliquid Perpetuals. Holds the section Profile (the only
section-specific input of the unified functions) and the four domain
sub-clients.

SECTION SPECIFICS DEFINED HERE:
  - MaxDecimals = 6;
  - Dex = "" (the first, validator-operated perp dex);
  - the asset loader: info "meta" with dex "" → asset id = index in universe.

MAIN FUNCTIONS:
  - NewClient(parent)   : section constructor; no network I/O. Exchange
                          metadata is loaded on first use and then refreshed
                          every Config.AssetRefreshInterval.
  - Trading / Account / MarketData / Stream : sub-clients.
  - RefreshAssets(ctx)  : force a metadata reload (e.g. after a listing).
*/

package perpetuals

import (
	"context"

	hyperliquid "github.com/tonymontanov/go-hyperliquid"
	"github.com/tonymontanov/go-hyperliquid/internal/assets"
	"github.com/tonymontanov/go-hyperliquid/internal/domain"
	"github.com/tonymontanov/go-hyperliquid/internal/engine"
	"github.com/tonymontanov/go-hyperliquid/types"
)

const (
	// SectionName — name of the section (exchange vocabulary: "Perpetuals").
	SectionName string = "perpetuals"
	// Dex — perp dex name of the section: the first perp dex is the empty string.
	Dex string = ""
	// MaxDecimals — MAX_DECIMALS of perpetuals: a price carries at most
	// 6 - szDecimals decimal places.
	MaxDecimals int = 6
)

// Client — Perpetuals section client. Safe for concurrent use.
type Client struct {
	parent   *hyperliquid.Client
	profile  domain.Profile
	registry *assets.Registry

	trading *TradingClient
	account *AccountClient
	market  *MarketDataClient
	stream  *StreamClient
}

// NewClient builds the section on top of the root client.
func NewClient(parent *hyperliquid.Client) *Client {
	var c *Client = &Client{parent: parent}
	c.registry = assets.NewRegistry(c.loadAssets)
	c.profile = domain.Profile{Section: SectionName, Dex: Dex, Registry: c.registry}

	c.trading = &TradingClient{c: c, transport: engine.TransportREST}
	c.account = &AccountClient{c: c}
	c.market = &MarketDataClient{c: c}
	c.stream = &StreamClient{c: c}

	var cfg hyperliquid.Config = parent.Config()
	c.registry.StartAutoRefresh(parent.LifeContext(), cfg.AssetRefreshInterval, cfg.Logger,
		cfg.Metrics.Counter("hyperliquid_asset_registry_refresh_total", "section", SectionName))
	return c
}

// Trading returns the order management sub-client (REST transport).
func (c *Client) Trading() *TradingClient { return c.trading }

// Account returns the account / position sub-client.
func (c *Client) Account() *AccountClient { return c.account }

// MarketData returns the market data sub-client.
func (c *Client) MarketData() *MarketDataClient { return c.market }

// Stream returns the WebSocket streams sub-client.
func (c *Client) Stream() *StreamClient { return c.stream }

// RefreshAssets reloads exchange metadata immediately.
func (c *Client) RefreshAssets(ctx context.Context) error {
	return c.registry.Refresh(ctx)
}

// engine / profile / user — private shortcuts of the sub-clients.
func (c *Client) engine() *engine.Engine     { return c.parent.Engine() }
func (c *Client) prof() *domain.Profile      { return &c.profile }
func (c *Client) user() string               { return c.parent.UserAddress() }
func (c *Client) logger() hyperliquid.Logger { return c.parent.Logger() }

// loadAssets is the section's metadata loader: meta(dex "") → asset id = index.
func (c *Client) loadAssets(ctx context.Context) ([]types.AssetInfo, error) {
	var meta domain.PerpMeta
	var err error
	meta, err = domain.PerpMetaOf(ctx, c.engine(), &c.profile)
	if err != nil {
		return nil, err
	}
	var list []types.AssetInfo = make([]types.AssetInfo, len(meta.Universe))
	var i int
	for i = 0; i < len(meta.Universe); i++ {
		list[i] = types.AssetInfo{
			AssetID:      uint32(i),
			Coin:         meta.Universe[i].Name,
			SzDecimals:   meta.Universe[i].SzDecimals,
			MaxLeverage:  meta.Universe[i].MaxLeverage,
			OnlyIsolated: meta.Universe[i].OnlyIsolated,
			IsDelisted:   meta.Universe[i].IsDelisted,
			Precision:    types.Precision{SzDecimals: meta.Universe[i].SzDecimals, MaxDecimals: MaxDecimals},
		}
	}
	return list, nil
}

// ensureAssets makes sure exchange metadata is loaded (network I/O on first
// use only). Methods that filter account-wide answers call it up front.
func (c *Client) ensureAssets(ctx context.Context) error {
	var err error
	_, err = c.registry.Get(ctx)
	return err
}

// owns reports whether coin belongs to this section. Account-wide answers
// (open orders, order updates, fills, mids) mix every section of the account.
// Never performs I/O — safe on the WS read goroutine; callers run
// ensureAssets first.
func (c *Client) owns(coin string) bool {
	var snapshot *assets.Snapshot = c.registry.Peek()
	if snapshot == nil {
		return false
	}
	var ok bool
	_, ok = snapshot.ByCoin(coin)
	return ok
}
