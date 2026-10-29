package main

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/store"
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

	// The durable lock keeps the promo rule (store.LockPrice): a promo lock ends with its price,
	// a full lock protects against the hike.
	db, at := store.NewMem(), time.Now()
	_, ok, err := db.LockPrice("u", "n1", "m", 0.1, 0.1, at, time.Hour, store.LockOpts{Mint: true, Promo: true})
	require.NoError(t, err)
	require.True(t, ok)
	_, ok, _ = db.LockPrice("u", "n1", "m", 0.1, 0.1, at, time.Hour, store.LockOpts{})
	require.True(t, ok, "a promo lock lives while its price does")
	_, ok, _ = db.LockPrice("u", "n1", "m", 0.1, 0.6, at, time.Hour, store.LockOpts{})
	require.False(t, ok, "a promo lock ends when its price does")
	_, _, _ = db.LockPrice("u", "n2", "m", 0.1, 0.1, at, time.Hour, store.LockOpts{Mint: true})
	q, ok, _ := db.LockPrice("u", "n2", "m", 0.1, 0.6, at, time.Hour, store.LockOpts{})
	require.True(t, ok, "a full lock protects against the hike")
	require.Equal(t, 0.1, q.Out)

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
