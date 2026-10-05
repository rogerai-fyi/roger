package main

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

// pricelock_test.go - promo locks (contract §14.12): the posted-since time is shared
// (first poster wins), a promo lock ends with its price, an unknown time mints the full lock.

func TestPostedSinceSharedAndPromoLock(t *testing.T) {
	mr := miniredis.RunT(t)
	now := time.Now()
	vsA, _ := newValkeyStore("redis://" + mr.Addr())
	vsB, _ := newValkeyStore("redis://" + mr.Addr())
	a, b := pairBroker(t, vsA, &now), pairBroker(t, vsB, &now)

	first, ok := a.notePosted("n1", "m", 0.1, 0.6)
	require.True(t, ok)
	now = now.Add(2 * time.Hour)
	got, ok := b.notePosted("n1", "m", 0.1, 0.6)
	require.True(t, ok)
	require.Equal(t, first.UnixMilli(), got.UnixMilli(), "the first instance to see the price owns its time")
	require.False(t, b.promoLock("n1", "m", 0.1, 0.6), "posted 2 h: a full lock")
	require.True(t, b.promoLock("n1", "m", 0.1, 0.1), "a price first seen now: a promo lock")

	q := priceQuote{in: 0.1, out: 0.1, until: time.Now().Add(time.Hour), promo: true}
	require.True(t, q.live(time.Now(), 0.1, 0.1))
	require.False(t, q.live(time.Now(), 0.1, 0.6), "a promo lock ends when its price does")
	q.promo = false
	require.True(t, q.live(time.Now(), 0.1, 0.6), "a full lock protects against the hike")

	mr.SetError("ERR down")
	require.False(t, a.promoLock("n1", "m", 0.2, 0.2), "unknown posted-since: the full lock")
}

func TestPostedSinceSingleInstance(t *testing.T) {
	now := time.Now()
	b := pairBroker(t, newMemStore(), &now)
	b.notePostedPrices(b.nodes["none"]) // an empty registration records nothing
	first, ok := b.notePosted("n1", "m", 1, 1)
	require.True(t, ok)
	now = now.Add(time.Minute)
	again, _ := b.notePosted("n1", "m", 1, 1)
	require.Equal(t, first, again)
	t.Setenv("ROGERAI_LOCK_MIN_POSTED", "30s")
	require.False(t, b.promoLock("n1", "m", 1, 1))
}
