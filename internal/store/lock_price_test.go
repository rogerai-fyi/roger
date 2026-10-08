package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLockPriceMintAndPromoParity pins the price lock's two relay rules on both stores: only a
// mint writes a lock (a read of a station with none answers ok=false and writes nothing), and a
// promo lock ends when the station's price moves while a full lock protects against the hike.
func TestLockPriceMintAndPromoParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now, win := time.Now(), time.Hour
			user := "lp-" + name
			_, ok, err := db.LockPrice(user, "n-read", "m", 1, 1, now, win, LockOpts{})
			require.NoError(t, err)
			require.False(t, ok, "a read mints nothing")
			qs, err := db.PriceQuotes("n-read", "m")
			require.NoError(t, err)
			require.Empty(t, qs, "the read wrote no row")

			q, ok, err := db.LockPrice(user, "n-promo", "m", 1, 1, now, win, LockOpts{Mint: true, Promo: true})
			require.NoError(t, err)
			require.True(t, ok)
			require.True(t, q.Promo)
			_, ok, err = db.LockPrice(user, "n-promo", "m", 1, 2, now, win, LockOpts{})
			require.NoError(t, err)
			require.False(t, ok, "a promo lock ends when its price does")
			q, ok, err = db.LockPrice(user, "n-promo", "m", 1, 2, now, win, LockOpts{Mint: true})
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, 2.0, q.Out, "a mint replaces the dead promo lock at the new price")
			require.False(t, q.Promo)

			_, _, err = db.LockPrice(user, "n-full", "m", 1, 1, now, win, LockOpts{Mint: true})
			require.NoError(t, err)
			q, ok, err = db.LockPrice(user, "n-full", "m", 5, 5, now, win, LockOpts{Mint: true})
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, 1.0, q.In, "a full lock holds the minted price against the hike")
		})
	}
}
