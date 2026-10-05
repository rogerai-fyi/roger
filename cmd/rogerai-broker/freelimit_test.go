package main

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

// freelimit_test.go - the free-traffic buckets (contract §14.3): one limit across instances
// over the shared store, this instance's own bucket during an outage, and 0 = off.

func TestFreeLimitsSharedAndOutage(t *testing.T) {
	t.Setenv("ROGERAI_FREE_RATE_RPM", "3")
	t.Setenv("ROGERAI_FREE_STATION_RPM", "100")
	t.Setenv("ROGERAI_FREE_PIN_RPM", "2")
	mr := miniredis.RunT(t)
	now := time.Now()
	mk := func() *broker {
		vs, err := newValkeyStore("redis://" + mr.Addr())
		require.NoError(t, err)
		return pairBroker(t, vs, &now)
	}
	a, b := mk(), mk()
	refused := func(br *broker, ip, node string, pinned bool) (bool, string) {
		w := httptest.NewRecorder()
		got := br.freeTrafficRefused(w, ip, node, "caller", pinned)
		return got, w.Body.String()
	}
	for i, br := range []*broker{a, b, a} {
		ok, body := refused(br, "203.0.113.1", "n1", false)
		require.False(t, ok, "request %d is within the shared per-IP limit: %s", i, body)
	}
	ok, body := refused(b, "203.0.113.1", "n1", false)
	require.True(t, ok, "the fourth, on either instance, is over 3 rpm")
	require.Contains(t, body, "free_rate_limited")

	ok, _ = refused(a, "203.0.113.2", "n1", true)
	require.False(t, ok)
	ok, _ = refused(b, "203.0.113.3", "n1", true)
	require.False(t, ok)
	ok, body = refused(a, "203.0.113.4", "n1", true)
	require.True(t, ok, "the pin cap is per caller, across IPs and instances")
	require.Contains(t, body, "free_pin_limited")

	// An outage never lifts the limit: each instance enforces its own bucket.
	mr.SetError("ERR down")
	for i := 0; i < 3; i++ {
		ok, _ = refused(a, "203.0.113.9", "n2", false)
		require.False(t, ok)
	}
	ok, _ = refused(a, "203.0.113.9", "n2", false)
	require.True(t, ok)

	t.Setenv("ROGERAI_FREE_RATE_RPM", "0")
	ok, _ = refused(a, "203.0.113.9", "n2", false)
	require.False(t, ok, "0 turns the per-IP limit off")
}
