package tui

// ONE STORE, TWO GOROUTINES. The TUI update loop and the browser console's HTTP handlers
// write the SAME LimitStore. Before mu that was a concurrent map access on the operator's
// money settings - two writers is a hard `fatal error: concurrent map writes`, not a subtle
// drift. This test hammers the store the way both surfaces do (Set/Update from writers,
// Snapshot/resolve from readers) and is meaningful under `go test -race`: it must not race
// and must not crash. Run in CI with -race.

import (
	"sync"
	"testing"
)

func TestLimitStoreIsSafeUnderConcurrentAccess(t *testing.T) {
	s := &LimitStore{Models: map[string]Limit{}}

	models := []string{"a", "b", "c", "d"}
	var wg sync.WaitGroup

	// Writers via the exported Set (the TUI's own editor path) ...
	for _, m := range models {
		m := m
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				s.Set(m, Limit{MaxIn: 1, MaxOut: 2, MinTPS: 3, Quants: []string{"Q4_K_M"}})
			}
		}()
	}
	// ... and via Update (the browser console's read-modify-write merge) at the same time.
	for _, m := range models {
		m := m
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				s.Update(m, func(cur Limit) Limit {
					cur.MaxOut = 5 // edit the "form field"; MaxIn/Quants carried
					return cur
				})
			}
		}()
	}
	// Readers: Snapshot (console render) and resolve (routing) concurrently with the writers.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				_ = s.Snapshot()
				for _, m := range models {
					_ = s.resolve(m)
				}
			}
		}()
	}
	wg.Wait()

	// Whichever writer won last, the invariant holds: every model that survives carries a
	// non-zero MaxIn - Update never zeroed the money cap it did not edit.
	for m, l := range s.Snapshot() {
		if l.MaxIn != 1 {
			t.Fatalf("model %q lost its carried MaxIn under concurrency: %+v", m, l)
		}
	}
}
