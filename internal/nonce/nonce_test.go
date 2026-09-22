/*
FILE: internal/nonce/nonce_test.go

DESCRIPTION:
Tests of the lock-free nonce generator: strict monotonicity under a frozen
clock, fast-forwarding to the wall clock, uniqueness under heavy concurrency,
and sharing per signer address.
*/

package nonce

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestNextMonotonicWithFrozenClock(t *testing.T) {
	var g = NewWithClock(func() int64 { return 1_700_000_000_000 })
	var first = g.Next()
	if first != 1_700_000_000_000 {
		t.Fatalf("first nonce = %d", first)
	}
	for i := 1; i <= 1000; i++ {
		var got = g.Next()
		if got != first+uint64(i) {
			t.Fatalf("nonce %d = %d, want %d", i, got, first+uint64(i))
		}
	}
}

func TestNextFastForwardsToClock(t *testing.T) {
	var clock atomic.Int64
	clock.Store(1000)
	var g = NewWithClock(clock.Load)
	for i := 0; i < 50; i++ {
		g.Next()
	}
	if g.Last() != 1049 {
		t.Fatalf("burst must run ahead of the clock: last = %d", g.Last())
	}
	clock.Store(5000)
	if got := g.Next(); got != 5000 {
		t.Fatalf("generator must fast-forward to the clock: got %d", got)
	}
	clock.Store(4000) // clock stepped backwards (NTP): stay monotonic
	if got := g.Next(); got != 5001 {
		t.Fatalf("generator must stay monotonic on a backwards clock: got %d", got)
	}
}

func TestNextUniqueUnderConcurrency(t *testing.T) {
	const workers int = 16
	const perWorker int = 20000
	var g = New()
	var results = make([][]uint64, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			var out = make([]uint64, perWorker)
			for i := 0; i < perWorker; i++ {
				out[i] = g.Next()
			}
			results[w] = out
		}(w)
	}
	wg.Wait()

	var seen = make(map[uint64]struct{}, workers*perWorker)
	for w := 0; w < workers; w++ {
		var previous uint64
		for _, n := range results[w] {
			if n <= previous {
				t.Fatalf("worker %d: nonce %d is not greater than %d", w, n, previous)
			}
			previous = n
			if _, dup := seen[n]; dup {
				t.Fatalf("duplicate nonce %d", n)
			}
			seen[n] = struct{}{}
		}
	}
}

func TestForSignerSharesGenerator(t *testing.T) {
	var a = [20]byte{1}
	var b = [20]byte{2}
	var first = ForSigner(a)
	if first != ForSigner(a) {
		t.Fatal("same signer must share one generator")
	}
	if ForSigner(a) == ForSigner(b) {
		t.Fatal("different signers must not share a generator")
	}
}

func BenchmarkNext(b *testing.B) {
	var g = New()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.Next()
	}
}

func BenchmarkNextParallel(b *testing.B) {
	var g = New()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			g.Next()
		}
	})
}
