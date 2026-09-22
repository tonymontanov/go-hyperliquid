/*
FILE: internal/ratelimit/ratelimit_test.go

DESCRIPTION:
Tests of the weight table (official "Rate limits and user limits" page) and of
the lock-free sliding window / address budget.
*/

package ratelimit

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestActionWeight(t *testing.T) {
	var cases = []struct {
		batch int
		want  int64
	}{{0, 1}, {1, 1}, {39, 1}, {40, 2}, {79, 2}, {80, 3}, {-5, 1}}
	for _, c := range cases {
		if got := ActionWeight(c.batch); got != c.want {
			t.Errorf("ActionWeight(%d) = %d, want %d", c.batch, got, c.want)
		}
	}
}

func TestInfoWeight(t *testing.T) {
	var cases = []struct {
		infoType string
		want     int64
	}{
		{"l2Book", 2}, {"allMids", 2}, {"clearinghouseState", 2}, {"orderStatus", 2},
		{"spotClearinghouseState", 2}, {"exchangeStatus", 2},
		{"userRole", 60},
		{"meta", 20}, {"openOrders", 20}, {"userFills", 20}, {"candleSnapshot", 20},
	}
	for _, c := range cases {
		if got := InfoWeight(c.infoType); got != c.want {
			t.Errorf("InfoWeight(%s) = %d, want %d", c.infoType, got, c.want)
		}
	}
	if got := InfoItemsWeight("userFills", 2000); got != 100 {
		t.Errorf("InfoItemsWeight(userFills, 2000) = %d", got)
	}
	if got := InfoItemsWeight("candleSnapshot", 5000); got != 83 {
		t.Errorf("InfoItemsWeight(candleSnapshot, 5000) = %d", got)
	}
	if got := InfoItemsWeight("meta", 500); got != 0 {
		t.Errorf("InfoItemsWeight(meta) = %d", got)
	}
}

func TestWindowSlides(t *testing.T) {
	var clock atomic.Int64
	clock.Store(1_700_000_000)
	var w = NewWindowWithClock(IPWeightLimitPerMinute, clock.Load)

	if got := w.Add(20); got != 20 {
		t.Fatalf("used = %d", got)
	}
	clock.Add(30)
	if got := w.Add(2); got != 22 {
		t.Fatalf("used = %d", got)
	}
	if w.Remaining() != 1178 {
		t.Fatalf("remaining = %d", w.Remaining())
	}
	clock.Add(29) // first bucket is 59 s old: still inside
	if got := w.Used(); got != 22 {
		t.Fatalf("used at +59s = %d", got)
	}
	clock.Add(1) // 60 s old: expired
	if got := w.Used(); got != 2 {
		t.Fatalf("used at +60s = %d", got)
	}
	if got := w.Add(5); got != 7 { // reuses the expired bucket index
		t.Fatalf("used after bucket reuse = %d", got)
	}
	clock.Add(3600)
	if got := w.Used(); got != 0 {
		t.Fatalf("used after an hour = %d", got)
	}
}

func TestWindowConcurrentAddLosesNothing(t *testing.T) {
	var w = NewWindowWithClock(1<<40, func() int64 { return 42 })
	const workers int = 8
	const perWorker int = 5000
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				w.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := w.Used(); got != int64(workers*perWorker) {
		t.Fatalf("used = %d, want %d", got, workers*perWorker)
	}
}

func TestAddressBudget(t *testing.T) {
	var b AddressBudget
	if b.Snapshot().Remaining() != 0 {
		t.Fatal("unsynced budget must report 0 remaining")
	}
	b.Sync(100, 10_000)
	b.Consume(25)
	b.Consume(-3)
	var snap = b.Snapshot()
	if snap.Used != 125 || snap.Capacity != 10_000 || snap.Remaining() != 9_875 || snap.SyncedAtMs == 0 {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func BenchmarkWindowAdd(b *testing.B) {
	var w = NewWindow(IPWeightLimitPerMinute)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.Add(1)
	}
}
