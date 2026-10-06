package main

import (
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/store"
)

// TestStaleHoldsSweepPrunesExpiredPriceQuotes (audit fix 2026-10-04): the periodic money
// sweep also deletes expired price quotes, so rogerai.price_quotes stays bounded.
func TestStaleHoldsSweepPrunesExpiredPriceQuotes(t *testing.T) {
	b := relayBroker(store.NewMem())
	past := time.Now().Add(-48 * time.Hour)
	if _, err := b.db.QuotePrice("old", "n", "m", 0.1, 0.3, past, time.Hour); err != nil {
		t.Fatal(err)
	}
	b.releaseStaleHoldsSweepOnce(time.Now().Add(-b.holdTTL))
	if n, _ := b.db.PruneExpiredPriceQuotes(time.Now(), 100); n != 0 {
		t.Fatalf("the sweep left %d expired quote(s) behind", n)
	}
}
