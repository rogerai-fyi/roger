package main

// shared_limiters_test.go: the Idempotency-Key lookup bucket and the dry-run bucket are shared
// across instances like every other request limiter (slice-6 audit 2026-10-06), so a caller
// cannot get N times the bound by spreading lookups over N instances.

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
)

func TestIdemAndDryLimitersAreShared(t *testing.T) {
	mr := miniredis.RunT(t)
	t.Setenv("ROGERAI_REDIS_URL", "redis://"+mr.Addr())
	t.Setenv("ROGERAI_MULTI_INSTANCE", "1")
	_, priv, _ := ed25519.GenerateKey(nil)
	a := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	t.Cleanup(func() { _ = a.shared.Close(); _ = b.shared.Close() })

	ok, _ := a.idemRL.allowAt("payer-x", 1, 1)
	require.True(t, ok)
	ok, _ = b.idemRL.allowAt("payer-x", 1, 1)
	require.False(t, ok, "instance B sees the token instance A spent")

	for i := 0; i < 30; i++ { // the dry-run burst (default 30) spent on A
		ok, _ = a.dryLimiter().allow("caller-y")
		require.True(t, ok)
	}
	ok, _ = b.dryLimiter().allow("caller-y")
	require.False(t, ok, "instance B's dry-run bucket is the same bucket")
}
