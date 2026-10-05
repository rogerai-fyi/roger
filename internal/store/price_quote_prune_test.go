package store

import (
	"os"
	"testing"
	"time"
)

// TestPruneExpiredPriceQuotes (audit fix 2026-10-04): expired price quotes are deleted in
// bounded batches; a live quote is never touched. Both stores.
func TestPruneExpiredPriceQuotes(t *testing.T) {
	stores := map[string]Store{"mem": NewMem()}
	if dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL"); dsn != "" {
		stores["postgres"] = freshPostgres(t, dsn)
	}
	for name, s := range stores {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			past := now.Add(-48 * time.Hour)
			for _, u := range []string{"a", "b", "c"} {
				if _, err := s.QuotePrice(u, "n", "m", 0.1, 0.3, past, time.Hour); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.QuotePrice("live", "n", "m", 0.1, 0.3, now, 24*time.Hour); err != nil {
				t.Fatal(err)
			}
			for i, want := range []int{2, 1, 0} {
				n, err := s.PruneExpiredPriceQuotes(now, 2)
				if err != nil {
					t.Fatal(err)
				}
				if n != want {
					t.Fatalf("prune #%d removed %d, want %d", i+1, n, want)
				}
			}
			q, err := s.QuotePrice("live", "n", "m", 9, 9, now, 24*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if q.Out != 0.3 {
				t.Fatalf("the live quote was pruned or replaced: out %v, want 0.3", q.Out)
			}
		})
	}
}
