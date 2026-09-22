/*
FILE: internal/ratelimit/window.go

DESCRIPTION:
Lock-free accounting of the two Hyperliquid budgets.

Window — sliding 60-second window of consumed IP weight.
  The window is a ring of 60 one-second buckets. Each bucket is ONE
  atomic.Uint64 packing (unix second << 32 | weight), so "the bucket belongs to
  an older second → reset it" and "add weight" happen in a single CAS and no
  update can be lost. Add and Used never block and never allocate.

AddressBudget — local mirror of the address-based request budget.
  The authoritative numbers come from the userRateLimit info request
  (nRequestsUsed / nRequestsCap); between syncs the SDK adds the cost of every
  action it sends. Several processes trading the same account share the budget
  on the exchange side, so callers must resync periodically.

MAIN FUNCTIONS:
  - NewWindow(limit) / (Window).Add / Used / Remaining
  - (AddressBudget).Sync / Consume / Snapshot
*/

package ratelimit

import (
	"sync/atomic"
	"time"
)

// windowSeconds — length of the sliding window.
const windowSeconds int64 = 60

// Window — sliding one-minute weight window. Safe for concurrent use.
type Window struct {
	limit   int64
	buckets [windowSeconds]atomic.Uint64
	// now — clock in unix seconds, replaceable in tests.
	now func() int64
}

// wallClockSeconds returns the current unix time in seconds.
func wallClockSeconds() int64 {
	return time.Now().Unix()
}

// NewWindow creates a window with the given per-minute limit.
func NewWindow(limit int64) *Window {
	return &Window{limit: limit, now: wallClockSeconds}
}

// NewWindowWithClock creates a window with a custom clock (tests).
func NewWindowWithClock(limit int64, now func() int64) *Window {
	return &Window{limit: limit, now: now}
}

// Limit returns the configured per-minute limit.
func (w *Window) Limit() int64 { return w.limit }

// Add records weight consumed now and returns the weight used within the
// window after the addition.
func (w *Window) Add(weight int64) int64 {
	var second int64 = w.now()
	if weight > 0 {
		var bucket *atomic.Uint64 = &w.buckets[second%windowSeconds]
		for {
			var old uint64 = bucket.Load()
			var accumulated uint64
			if int64(old>>32) == second&0xffffffff {
				accumulated = old & 0xffffffff
			}
			accumulated += uint64(weight)
			if accumulated > 0xffffffff {
				accumulated = 0xffffffff
			}
			var updated uint64 = uint64(second&0xffffffff)<<32 | accumulated
			if bucket.CompareAndSwap(old, updated) {
				break
			}
		}
	}
	return w.usedAt(second)
}

// Used returns the weight consumed within the last 60 seconds.
func (w *Window) Used() int64 {
	return w.usedAt(w.now())
}

// Remaining returns limit - Used(), never negative.
func (w *Window) Remaining() int64 {
	var remaining int64 = w.limit - w.Used()
	if remaining < 0 {
		return 0
	}
	return remaining
}

// usedAt sums the buckets that belong to (second-60, second].
func (w *Window) usedAt(second int64) int64 {
	var total int64
	var i int64
	for i = 0; i < windowSeconds; i++ {
		var packed uint64 = w.buckets[i].Load()
		var stamp int64 = int64(packed >> 32)
		var age int64 = (second & 0xffffffff) - stamp
		if age >= 0 && age < windowSeconds {
			total += int64(packed & 0xffffffff)
		}
	}
	return total
}

// AddressBudget — local mirror of the address-based request budget.
type AddressBudget struct {
	used     atomic.Int64
	capacity atomic.Int64
	syncedAt atomic.Int64
}

// BudgetSnapshot — point-in-time view of an AddressBudget.
type BudgetSnapshot struct {
	// Used — requests consumed (exchange value at the last sync + local sends).
	Used int64
	// Capacity — nRequestsCap at the last sync; 0 until the first sync.
	Capacity int64
	// SyncedAtMs — unix ms of the last Sync; 0 until the first sync.
	SyncedAtMs int64
}

// Remaining returns Capacity - Used, never negative; 0 before the first sync.
func (s BudgetSnapshot) Remaining() int64 {
	if s.Capacity <= s.Used {
		return 0
	}
	return s.Capacity - s.Used
}

// Sync overwrites the mirror with authoritative exchange numbers.
func (b *AddressBudget) Sync(used int64, capacity int64) {
	b.used.Store(used)
	b.capacity.Store(capacity)
	b.syncedAt.Store(time.Now().UnixMilli())
}

// Consume records n requests sent since the last sync.
func (b *AddressBudget) Consume(n int64) {
	if n > 0 {
		b.used.Add(n)
	}
}

// Snapshot returns the current view.
func (b *AddressBudget) Snapshot() BudgetSnapshot {
	return BudgetSnapshot{
		Used:       b.used.Load(),
		Capacity:   b.capacity.Load(),
		SyncedAtMs: b.syncedAt.Load(),
	}
}
