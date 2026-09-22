/*
FILE: internal/domain/profile.go

DESCRIPTION:
Package domain holds the UNIFIED domain functions of the common layer: placing,
modifying and cancelling orders, the info requests and the WebSocket streams
that are identical for every section of the exchange.

THE TWO-LAYER RULE, IN CODE:

	// common layer (this package) — one implementation for the whole exchange
	func PlaceOrders(ctx, e, profile, requests, options, transport) (...)

	// section layer — the unified function with the section's specifics
	func (t *TradingClient) CreateBatchOrders(ctx, requests, options) (...) {
		return domain.PlaceOrders(ctx, t.c.engine(), t.c.profile(), requests, options, t.transport)
	}

A section never calls another section; everything two sections could share
lives here and is parameterised by Profile — the ONLY thing a section brings:
  - Section  : name used in error messages ("perpetuals", "spot", ...);
  - Dex      : perp dex name of dex-scoped requests ("" = the first perp dex);
  - Registry : coin → asset id / precision, built by the section's own
               metadata loader (asset id formula, MAX_DECIMALS, coin naming).

MAIN ENTITIES:
  - Profile          : section parameters.
  - appendJSONText   : safe JSON string writer for caller-supplied text.
  - invalid          : InvalidRequest error constructor with the house message
                       format "<section>.<Operation>: <detail>".
*/

package domain

import (
	"context"

	"github.com/tonymontanov/go-hyperliquid/internal/assets"
	"github.com/tonymontanov/go-hyperliquid/internal/hlerr"
	"github.com/tonymontanov/go-hyperliquid/types"
)

// Profile — everything a section contributes to the unified functions.
type Profile struct {
	// Section — section name for error messages.
	Section string
	// Dex — perp dex name for dex-scoped requests; "" for the first perp dex
	// and for sections without a dex.
	Dex string
	// Registry — asset registry of the section.
	Registry *assets.Registry
}

// invalid builds an InvalidRequest error: "<section>.<operation>: <detail>".
func (p *Profile) invalid(operation string, detail string) *hlerr.Error {
	return hlerr.New(hlerr.ErrorKindInvalidRequest, p.Section+"."+operation+": "+detail, nil)
}

// Invalid is the exported form of invalid for validation done by sections.
func (p *Profile) Invalid(operation string, detail string) error {
	return p.invalid(operation, detail)
}

// Resolve returns the asset of a coin, loading metadata on first use.
func (p *Profile) Resolve(ctx context.Context, operation string, coin string) (*types.AssetInfo, error) {
	return p.resolve(ctx, operation, coin)
}

// resolve returns the asset of a coin, loading metadata on first use.
func (p *Profile) resolve(ctx context.Context, operation string, coin string) (*types.AssetInfo, error) {
	if coin == "" {
		return nil, p.invalid(operation, "coin is empty")
	}
	var snapshot *assets.Snapshot
	var err error
	snapshot, err = p.Registry.Get(ctx)
	if err != nil {
		return nil, err
	}
	var info *types.AssetInfo
	var ok bool
	info, ok = snapshot.ByCoin(coin)
	if !ok {
		return nil, p.invalid(operation, "unknown coin "+coin)
	}
	return info, nil
}

// safeText reports whether s can be embedded into a JSON string without
// escaping. Coins, intervals and addresses always can; anything else is a
// caller bug and is rejected instead of being escaped.
func safeText(s string) bool {
	var i int
	for i = 0; i < len(s); i++ {
		var c byte = s[i]
		if c < 0x20 || c == '"' || c == '\\' || c >= 0x7f {
			return false
		}
	}
	return true
}

// appendJSONText appends `"s"`. The caller must have checked safeText(s).
func appendJSONText(b []byte, s string) []byte {
	b = append(b, '"')
	b = append(b, s...)
	return append(b, '"')
}
