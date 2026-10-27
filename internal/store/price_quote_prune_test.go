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

// TestQuotePriceReadBackSurvivesAPruneInBetween (audit fix 2026-10-05): a quote that expires
// and is pruned right after QuotePrice meets it live must still be returned, never lost to a
// no-rows read-back (which would fail safe to a no-charge request). Postgres only: the
// in-memory store decides under one mutex and has no gap.
func TestQuotePriceReadBackSurvivesAPruneInBetween(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROGERAI_TEST_DATABASE_URL not set")
	}
	s := freshPostgres(t, dsn)
	t0 := time.Now()
	if _, err := s.QuotePrice("u", "n", "m", 0.1, 0.3, t0, time.Hour); err != nil {
		t.Fatal(err)
	}
	defer func() { quoteReadBackGapForTest = nil }()
	quoteReadBackGapForTest = func() {
		// The live quote's lock ends and the prune deletes it, between QuotePrice meeting it
		// and reading it back.
		if _, err := s.PruneExpiredPriceQuotes(t0.Add(2*time.Hour), 10); err != nil {
			t.Error(err)
		}
	}
	q, err := s.QuotePrice("u", "n", "m", 9, 9, t0.Add(30*time.Minute), time.Hour)
	if err != nil {
		t.Fatalf("the live quote was lost to the prune: %v", err)
	}
	if q.In != 0.1 || q.Out != 0.3 || !q.Until.Equal(time.Unix(0, t0.Add(time.Hour).UnixNano())) {
		t.Fatalf("quote = %+v, want the first quote (0.1/0.3 until t0+1h)", q)
	}
}
