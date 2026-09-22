/*
FILE: metrics.go

DESCRIPTION:
Public re-export of the SDK metrics contract (implementation: internal/hlmet).
Counter / CounterFactory are shaped like prometheus counters but are not tied
to any backend. See internal/hlmet for the list of counter names.

The default factory is a no-op.
*/

package hyperliquid

import "github.com/tonymontanov/go-hyperliquid/internal/hlmet"

// Counter — a single monotonically increasing counter.
type Counter = hlmet.Counter

// CounterFactory — counter factory (name + label pairs).
type CounterFactory = hlmet.CounterFactory

// NoopMetrics returns the no-op counter factory.
func NoopMetrics() CounterFactory { return hlmet.Noop() }
