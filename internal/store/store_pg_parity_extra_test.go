package store

// store_pg_parity_extra_test.go holds parity coverage for store methods whose Mem half was
// already pinned in-package while the Postgres half was only ever exercised from another
// package's suites (so this package never saw it): the verified-email identity lookup, the
// owner-level earnings freeze, and the strike-seeding test seam.

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The verified-email lookup is a security boundary (owners.email alone is user-editable
// profile text), so BOTH backends must apply the email_verified_at guard, the
// case-insensitive match, and the anonymized-row exclusion identically.
func TestOwnerByVerifiedEmailParity(t *testing.T) {
	for name, db := range parityStores(t) {
		db := db
		t.Run(name, func(t *testing.T) {
			now := time.Now().Unix()
			require.NoError(t, db.BindOwner(Owner{
				Pubkey: "pk-verified", Login: "someone@rogerai.fm",
				Email: "someone@rogerai.fm", EmailVerifiedAt: now,
			}))
			// Recorded but never proven: the profile field alone is not an identity.
			require.NoError(t, db.BindOwner(Owner{
				Pubkey: "pk-unproven", Login: "squatter", Email: "claimed@rogerai.fm",
			}))

			got, ok, err := db.OwnerByVerifiedEmail("someone@rogerai.fm")
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, "pk-verified", got.Pubkey)
			require.Equal(t, now, got.EmailVerifiedAt)

			got, ok, err = db.OwnerByVerifiedEmail("SoMeOne@RogerAI.FM")
			require.NoError(t, err)
			require.True(t, ok, "a stray spelling must not mint a second account")
			require.Equal(t, "pk-verified", got.Pubkey)

			_, ok, err = db.OwnerByVerifiedEmail("claimed@rogerai.fm")
			require.NoError(t, err)
			require.False(t, ok, "a self-asserted address must never resolve to an account")

			_, ok, err = db.OwnerByVerifiedEmail("nobody@rogerai.fm")
			require.NoError(t, err)
			require.False(t, ok)

			// A deleted (anonymized) account is not reachable by its old address.
			deleted, err := db.DeleteAccount("someone@rogerai.fm")
			require.NoError(t, err)
			require.True(t, deleted)
			_, ok, err = db.OwnerByVerifiedEmail("someone@rogerai.fm")
			require.NoError(t, err)
			require.False(t, ok, "a scrubbed, anonymized row is not reachable by its old address")
		})
	}
}

// The owner-level earnings freeze is read on every recourse decision, so the read must be a
// plain boolean on both backends: held / not held / never flagged, with a re-flag idempotent.
func TestAccountRecountHoldParity(t *testing.T) {
	for name, db := range parityStores(t) {
		db := db
		t.Run(name, func(t *testing.T) {
			held, err := db.AccountRecountHeld("acct-never")
			require.NoError(t, err)
			require.False(t, held, "an owner nobody flagged is not held")

			require.NoError(t, db.SetAccountRecountHold("acct-1", true))
			held, err = db.AccountRecountHeld("acct-1")
			require.NoError(t, err)
			require.True(t, held)

			// A re-flag re-arms auto-expiry rather than erroring on the existing row.
			require.NoError(t, db.SetAccountRecountHold("acct-1", true))
			held, err = db.AccountRecountHeld("acct-1")
			require.NoError(t, err)
			require.True(t, held)

			// The freeze is per owner.
			held, err = db.AccountRecountHeld("acct-2")
			require.NoError(t, err)
			require.False(t, held)

			require.NoError(t, db.SetAccountRecountHold("acct-1", false))
			held, err = db.AccountRecountHeld("acct-1")
			require.NoError(t, err)
			require.False(t, held, "lifting the freeze removes the row")

			// Clearing a freeze that was never placed is a no-op, and an empty account id is
			// refused silently rather than writing a row keyed on nothing.
			require.NoError(t, db.SetAccountRecountHold("acct-2", false))
			require.NoError(t, db.SetAccountRecountHold("", true))
			held, err = db.AccountRecountHeld("")
			require.NoError(t, err)
			require.False(t, held, "an empty account id must never become a held account")
		})
	}
}

// SeedStrikesForTest is the deliberate Mem seam that stages strikes with a created_at the
// time.Now()-stamped OwnerStrike path cannot produce - the decay-window scenarios need
// strikes dated OUTSIDE the window. Pinning it here keeps the seam honest: it must append
// (never replace), assign ids, respect an explicit id, and feed OwnerStrikeStats' window.
func TestSeedStrikesForTestStagesTheDecayWindow(t *testing.T) {
	const now = int64(1_800_000_000)
	m := NewMem()
	m.SeedStrikesForTest([]Strike{
		{AccountID: "acct-1", Kind: StrikeEmptyOutput, Evidence: `{"note":"old"}`, CreatedAt: now - 90*86400},
		{AccountID: "acct-1", Kind: StrikeEmptyOutput, Evidence: `{"note":"recent"}`, CreatedAt: now - 86400},
	})
	// A second call APPENDS (it is not a replace), and an explicit id is preserved.
	m.SeedStrikesForTest([]Strike{
		{AccountID: "acct-1", Kind: StrikeRecountDiscrepancy, CreatedAt: now - 3600},
		{ID: 4242, AccountID: "acct-2", Kind: StrikeImpossibleInput, CreatedAt: now - 3600},
	})

	all, err := m.StrikesByOwner("acct-1", 10)
	require.NoError(t, err)
	require.Len(t, all, 3, "the second call appended rather than replacing the first")
	ids := map[int64]bool{}
	for _, s := range all {
		require.NotZero(t, s.ID, "every seeded strike gets an id")
		require.False(t, ids[s.ID], "ids are unique")
		ids[s.ID] = true
	}
	other, err := m.StrikesByOwner("acct-2", 10)
	require.NoError(t, err)
	require.Len(t, other, 1)
	require.EqualValues(t, 4242, other[0].ID, "an explicit id is kept, not overwritten")

	// The point of the seam: a window that excludes the 90-day-old strike.
	windowed, kinds, err := m.OwnerStrikeStats("acct-1", now-30*86400)
	require.NoError(t, err)
	require.Equal(t, 2, windowed, "the 90-day-old strike has decayed out of the window")
	require.Equal(t, 2, kinds, "two distinct signal classes corroborate inside the window")

	windowed, kinds, err = m.OwnerStrikeStats("acct-1", 0)
	require.NoError(t, err)
	require.Equal(t, 3, windowed, "since<=0 counts every strike")
	require.Equal(t, 2, kinds)
}

// DB() is how a subsystem that owns its OWN tables in the same database (the tower schema,
// cmd/rogerai-broker/tower.go) shares this pool rather than opening a second one. It must hand
// back the LIVE pool - a nil or a fresh handle would silently double the connection footprint
// the accessor exists to keep honest.
func TestPostgresDBExposesTheLivePool(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROGERAI_TEST_DATABASE_URL not set; skipping shared-pool accessor test")
	}
	pg := freshPostgres(t, dsn)
	shared := pg.DB()
	require.NotNil(t, shared, "a subsystem must be able to share this pool")
	require.Same(t, pg.db, shared, "DB() shares the one pool; it must not open a second")
	require.NoError(t, shared.Ping(), "the shared handle is the live, reachable pool")
	// Closing the store closes what it handed out - one pool, one lifecycle owner.
	require.NoError(t, pg.Close())
	require.Error(t, shared.Ping(), "the store owns the lifecycle of the pool it shares")
}
