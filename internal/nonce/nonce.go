/*
FILE: internal/nonce/nonce.go

DESCRIPTION:
Lock-free nonce generator for Hyperliquid actions.

EXCHANGE RULES (official "Nonces and API wallets" page):
  - The exchange stores the 100 highest nonces PER SIGNER. A new nonce must be
    larger than the smallest of them and must never have been used.
  - A nonce must lie within (T - 2 days, T + 1 day), T = block time in ms.
  - Nonces are tracked per signer (API wallet), not per account: one API
    wallet signing for the master, a sub-account and a vault shares ONE set.
  - Recommended scheme: "an atomic counter, fast-forwarded to the current
    time in milliseconds".

ALGORITHM:
    next = max(now_ms, last + 1)        // CAS loop on an atomic.Int64
The value tracks wall-clock milliseconds, so it always stays inside the
allowed window; under bursts (several actions in the same millisecond) it runs
ahead of the clock by at most the burst size and converges back on the next
idle millisecond. No mutex, no syscall besides the clock read; contention is
resolved by retrying the CAS.

SHARING:
Because nonces belong to the SIGNER, two SDK clients created with the same key
inside one process MUST share one generator, otherwise they can emit the same
millisecond twice. ForSigner returns a process-wide generator keyed by the
signer address. Different processes using the same API wallet cannot be
coordinated by the SDK — the exchange guidance is one API wallet per process.

MAIN FUNCTIONS:
  - New()             : standalone generator (tests, custom wiring).
  - ForSigner(key)    : process-wide generator of a signer address.
  - (Generator).Next  : next nonce, lock-free, zero allocations.
*/

package nonce

import (
	"sync"
	"sync/atomic"
	"time"
)

// Generator — monotonic millisecond nonce source. Safe for concurrent use.
type Generator struct {
	last atomic.Int64
	// now — clock, replaceable in tests.
	now func() int64
}

// wallClockMs returns the current Unix time in milliseconds.
func wallClockMs() int64 {
	return time.Now().UnixMilli()
}

// New creates a standalone generator driven by the wall clock.
func New() *Generator {
	return &Generator{now: wallClockMs}
}

// NewWithClock creates a generator with a custom clock (tests).
func NewWithClock(now func() int64) *Generator {
	return &Generator{now: now}
}

// Next returns a nonce strictly greater than every nonce returned before by
// this generator and not smaller than the current time in milliseconds.
func (g *Generator) Next() uint64 {
	for {
		var last int64 = g.last.Load()
		var next int64 = g.now()
		if next <= last {
			next = last + 1
		}
		if g.last.CompareAndSwap(last, next) {
			return uint64(next)
		}
	}
}

// Last returns the most recently issued nonce (0 if none). Diagnostics only.
func (g *Generator) Last() uint64 {
	return uint64(g.last.Load())
}

// registry — process-wide generators keyed by the 20-byte signer address.
var registry sync.Map

// ForSigner returns the process-wide generator of the given signer address.
// The lookup uses a sync.Map and is meant for client construction, not for
// the hot path: callers keep the returned pointer.
func ForSigner(signer [20]byte) *Generator {
	var existing any
	var ok bool
	existing, ok = registry.Load(signer)
	if ok {
		return existing.(*Generator)
	}
	var actual any
	actual, _ = registry.LoadOrStore(signer, New())
	return actual.(*Generator)
}
