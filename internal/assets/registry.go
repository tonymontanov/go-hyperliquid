/*
FILE: internal/assets/registry.go

DESCRIPTION:
Asset registry of the common layer: coin ↔ asset id ↔ precision.

WHY IT EXISTS:
Actions address assets by a numeric id that (a) is derived from exchange
metadata (index in meta.universe, 10000 + index in spotMeta.universe, ...),
(b) DIFFERS between mainnet and testnet and (c) changes as assets are listed.
The mapping therefore cannot be hard-coded: it is built from metadata on first
use and refreshed in the background.

The registry itself is SECTION-AGNOSTIC. It stores an immutable Snapshot behind
an atomic pointer; the section supplies the Loader that knows which metadata
request to call and how to turn it into []types.AssetInfo (asset id formula,
MAX_DECIMALS, coin naming). Lookups on the order hot path are a single atomic
load plus a map read — no locks, no allocations.

MAIN FUNCTIONS:
  - NewRegistry(loader)          : lazy registry.
  - (Registry).Get(ctx)          : current snapshot, loading it on first use
                                   (concurrent first callers share one load).
  - (Registry).Refresh(ctx)      : reload and atomically swap.
  - (Registry).StartAutoRefresh  : background refresh until ctx is done.
  - (Snapshot).ByCoin / ByID / All.
*/

package assets

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tonymontanov/go-hyperliquid/internal/hllog"
	"github.com/tonymontanov/go-hyperliquid/internal/hlmet"
	"github.com/tonymontanov/go-hyperliquid/types"
)

// Loader fetches exchange metadata and converts it into asset descriptions.
type Loader func(ctx context.Context) ([]types.AssetInfo, error)

// Snapshot — immutable view of the assets of one section.
type Snapshot struct {
	list       []types.AssetInfo
	byCoin     map[string]*types.AssetInfo
	byID       map[uint32]*types.AssetInfo
	loadedAtMs int64
}

// NewSnapshot indexes a list of assets. The list is owned by the snapshot.
func NewSnapshot(list []types.AssetInfo, loadedAtMs int64) *Snapshot {
	var s *Snapshot = &Snapshot{
		list:       list,
		byCoin:     make(map[string]*types.AssetInfo, len(list)),
		byID:       make(map[uint32]*types.AssetInfo, len(list)),
		loadedAtMs: loadedAtMs,
	}
	var i int
	for i = 0; i < len(list); i++ {
		s.byCoin[list[i].Coin] = &list[i]
		s.byID[list[i].AssetID] = &list[i]
	}
	return s
}

// ByCoin looks an asset up by its exchange coin name (case-sensitive: "kPEPE").
func (s *Snapshot) ByCoin(coin string) (*types.AssetInfo, bool) {
	var info *types.AssetInfo
	var ok bool
	info, ok = s.byCoin[coin]
	return info, ok
}

// ByID looks an asset up by its numeric asset id.
func (s *Snapshot) ByID(id uint32) (*types.AssetInfo, bool) {
	var info *types.AssetInfo
	var ok bool
	info, ok = s.byID[id]
	return info, ok
}

// All returns every asset of the snapshot. The slice must not be modified.
func (s *Snapshot) All() []types.AssetInfo { return s.list }

// LoadedAtMs returns the unix ms timestamp of the load.
func (s *Snapshot) LoadedAtMs() int64 { return s.loadedAtMs }

// Registry — lazily loaded, atomically swappable asset snapshot.
type Registry struct {
	loader  Loader
	current atomic.Pointer[Snapshot]
	// loadMu serialises loads so concurrent first callers share one request.
	loadMu sync.Mutex
}

// NewRegistry creates a registry. No I/O happens until the first Get / Refresh.
func NewRegistry(loader Loader) *Registry {
	return &Registry{loader: loader}
}

// Peek returns the current snapshot or nil when nothing is loaded yet. Never
// performs I/O — safe for hot paths that must not block.
func (r *Registry) Peek() *Snapshot {
	return r.current.Load()
}

// Get returns the current snapshot, loading it on first use.
func (r *Registry) Get(ctx context.Context) (*Snapshot, error) {
	var snapshot *Snapshot = r.current.Load()
	if snapshot != nil {
		return snapshot, nil
	}
	r.loadMu.Lock()
	defer r.loadMu.Unlock()
	snapshot = r.current.Load()
	if snapshot != nil {
		return snapshot, nil
	}
	return r.loadLocked(ctx)
}

// Refresh reloads metadata and swaps the snapshot. On failure the previous
// snapshot stays in place.
func (r *Registry) Refresh(ctx context.Context) error {
	r.loadMu.Lock()
	defer r.loadMu.Unlock()
	var err error
	_, err = r.loadLocked(ctx)
	return err
}

// loadLocked performs one load. Caller holds loadMu.
func (r *Registry) loadLocked(ctx context.Context) (*Snapshot, error) {
	var list []types.AssetInfo
	var err error
	list, err = r.loader(ctx)
	if err != nil {
		return nil, err
	}
	var snapshot *Snapshot = NewSnapshot(list, time.Now().UnixMilli())
	r.current.Store(snapshot)
	return snapshot, nil
}

// StartAutoRefresh refreshes the registry every interval until ctx is done.
// interval <= 0 disables the loop. Failures are logged and retried on the next
// tick — the previous snapshot keeps serving lookups.
func (r *Registry) StartAutoRefresh(ctx context.Context, interval time.Duration, logger hllog.Logger, refreshed hlmet.Counter) {
	if interval <= 0 {
		return
	}
	go func() {
		var ticker *time.Ticker = time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				var err error = r.Refresh(ctx)
				if err != nil {
					if ctx.Err() == nil {
						logger.Warn("assets: metadata refresh failed", hllog.Err(err))
					}
					continue
				}
				refreshed.Inc()
			}
		}
	}()
}
