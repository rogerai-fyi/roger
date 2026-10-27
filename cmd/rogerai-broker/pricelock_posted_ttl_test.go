package main

// pricelock_posted_ttl_test.go: a price that stays posted keeps its first-posted time for as long
// as it is seen, not only for the first postedSinceTTL (slice-6 audit 2026-10-06): an expired
// record would read the long-posted price as just posted.

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
)

func TestPostedSinceRefreshesWhileSeen(t *testing.T) {
	mr := miniredis.RunT(t)
	t.Setenv("ROGERAI_REDIS_URL", "redis://"+mr.Addr())
	t.Setenv("ROGERAI_MULTI_INSTANCE", "1")
	_, priv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	t.Cleanup(func() { _ = b.shared.Close() })

	clock := time.Now()
	b.nowFn = func() time.Time { return clock }
	advance := func(d time.Duration) { clock = clock.Add(d); mr.FastForward(d) }

	first, ok := b.notePosted("n1", "m", 0.1, 0.3)
	require.True(t, ok)
	advance(postedSinceTTL - 24*time.Hour) // seen again a day before the record would lapse
	again, ok := b.notePosted("n1", "m", 0.1, 0.3)
	require.True(t, ok)
	require.Equal(t, first.UnixMilli(), again.UnixMilli())
	advance(2 * 24 * time.Hour) // past the ORIGINAL expiry
	later, ok := b.notePosted("n1", "m", 0.1, 0.3)
	require.True(t, ok)
	require.Equal(t, first.UnixMilli(), later.UnixMilli(), "a price seen within the TTL keeps its first-posted time")
}
