package stationattach

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// BOTH STORES, HELD TO THE SAME ASSERTIONS.
//
// A covered memory store beside an uncovered durable one is the exact shape that shipped
// the band occupancy bug in internal/store: Mem answered from a one-entry index, Postgres
// scanned every row, and the two disagreed only once a node had carried two bands. Nobody
// noticed because only Mem was exercised.
//
// The implementations here are deliberately different - a held mutex versus a locked row
// and a CAS - so agreement between them is a real result rather than a formality. The
// Postgres half is skipped when ROGERAI_TEST_DATABASE_URL is unset; cover-gate always
// provisions one.

func parityStores(t *testing.T) map[string]Store {
	t.Helper()
	out := map[string]Store{"mem": NewMemStore()}
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		return out
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// The schema is provisioned by an admin in production; a test database has to make it.
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	pg, err := NewPGStore(db)
	require.NoError(t, err)
	// A clean slate per test, scoped to THIS package's tables so a parallel package's rows
	// are never touched.
	_, err = db.Exec(`TRUNCATE rogerai.station_authorizations, rogerai.station_attachments`)
	require.NoError(t, err)
	out["postgres"] = pg
	return out
}

func eachStore(t *testing.T, fn func(t *testing.T, s Store, r *Registry, now time.Time)) {
	t.Helper()
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_700_000_000, 0).UTC()
			r := New(Config{Network: net, Now: func() time.Time { return now }}, s)
			require.NoError(t, s.PutAuthorization(Authorization{
				ID: authorID, Network: net, StationID: station, Owner: owner,
				Origin:       Origin{Kind: OriginJoined, TowerID: tower},
				AssertionKey: keyA, SessionKey: keyK, CeilingHash: "ceil-1",
				IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
			}))
			fn(t, s, r, now)
		})
	}
}

func TestParityAdmissionRecordsTheSameAttachment(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, now time.Time) {
		at, err := r.Admit(goodProof())
		require.NoError(t, err)
		require.Equal(t, station, at.StationID)
		require.Equal(t, StateQuarantine, at.State)
		require.Equal(t, int64(1), at.Epoch)
		require.Equal(t, "ceil-1", at.CeilingHash)

		// Re-read rather than trust the returned struct. This is the discipline the Postgres
		// CAS bug taught: a returned value can be right while the row is wrong.
		got, ok, err := s.ByStation(station)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, keyA, got.AssertionKey)
		require.Equal(t, keyK, got.SessionKey)
		require.Equal(t, Origin{Kind: OriginJoined, TowerID: tower}, got.Origin)
		require.Equal(t, StateQuarantine, got.State)
		require.Equal(t, authorID, got.AuthID)
		require.WithinDuration(t, now, got.AttachedAt, time.Second)

		auth, ok, err := s.Authorization(authorID)
		require.NoError(t, err)
		require.True(t, ok)
		require.True(t, auth.Consumed, "the invitation is spent in the same commit")
		require.Equal(t, station, auth.ConsumedBy)
	})
}

func TestParityOneAuthorizationSurvivesARace(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, _ time.Time) {
		const racers = 12
		var wg sync.WaitGroup
		errs := make([]error, racers)
		got := make([]Attachment, racers)
		start := make(chan struct{})
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				got[i], errs[i] = r.Admit(goodProof())
			}(i)
		}
		close(start)
		wg.Wait()

		for i := range errs {
			require.NoError(t, errs[i], "racer %d", i)
			require.Equal(t, station, got[i].StationID)
			require.Equal(t, int64(1), got[i].Epoch, "nobody may mint a second origin")
		}
		auth, _, err := s.Authorization(authorID)
		require.NoError(t, err)
		require.True(t, auth.Consumed)
	})
}

func TestParityRefusalLeavesTheInvitationSpendable(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, _ time.Time) {
		bad := goodProof()
		bad.AssertionKey = "not_the_named_key"
		_, err := r.Admit(bad)
		require.ErrorIs(t, err, ErrRejected)

		auth, ok, err := s.Authorization(authorID)
		require.NoError(t, err)
		require.True(t, ok)
		require.False(t, auth.Consumed,
			"a refused attachment must leave the invitation unspent in BOTH stores")

		// And the owner can still use it.
		at, err := r.Admit(goodProof())
		require.NoError(t, err)
		require.Equal(t, station, at.StationID)
	})
}

func TestParityAKeyIsHeldByOneLiveStationOnly(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, now time.Time) {
		_, err := r.Admit(goodProof())
		require.NoError(t, err)

		// A second Station presenting the SAME secure-session key.
		require.NoError(t, s.PutAuthorization(Authorization{
			ID: "auth-2", Network: net, StationID: "st-2", Owner: owner,
			Origin:       Origin{Kind: OriginJoined, TowerID: tower},
			AssertionKey: "A2", SessionKey: keyK,
			IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		}))
		p := Proof{
			AuthID: "auth-2", Network: net, StationID: "st-2", Owner: owner,
			Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: "A2", SessionKey: keyK,
		}
		_, err = r.Admit(p)
		require.ErrorIs(t, err, ErrRejected)
		require.Contains(t, err.Error(), "already bound to another Station")

		_, ok, err := s.ByStation("st-2")
		require.NoError(t, err)
		require.False(t, ok, "the refused Station must not exist in either store")
	})
}

// A retired Station releases its keys. Both stores must agree, because Mem filters on Live()
// in Go while Postgres filters in a partial index - the same rule expressed twice.
func TestParityARetiredStationReleasesItsKeys(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, now time.Time) {
		_, err := r.Admit(goodProof())
		require.NoError(t, err)

		_, ok, err := s.BySessionKey(keyK)
		require.NoError(t, err)
		require.True(t, ok, "a live Station holds its key")

		_, err = r.Revoke(station)
		require.NoError(t, err)

		_, ok, err = s.BySessionKey(keyK)
		require.NoError(t, err)
		require.False(t, ok, "a revoked Station must not hold its keys hostage")
		_, ok, err = s.ByAssertionKey(keyA)
		require.NoError(t, err)
		require.False(t, ok)

		// And the freed key may be attached to a genuinely new Station ID - which is exactly
		// the path the spec requires for a cross-kind migration.
		require.NoError(t, s.PutAuthorization(Authorization{
			ID: "auth-2", Network: net, StationID: "st-2", Owner: owner,
			Origin:       Origin{Kind: OriginJoined, TowerID: tower},
			AssertionKey: keyA, SessionKey: keyK,
			IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		}))
		at, err := r.Admit(Proof{
			AuthID: "auth-2", Network: net, StationID: "st-2", Owner: owner,
			Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: keyA, SessionKey: keyK,
		})
		require.NoError(t, err)
		require.Equal(t, "st-2", at.StationID)
	})
}

func TestParityLifecycleStatesRoundTrip(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, _ time.Time) {
		_, err := r.Admit(goodProof())
		require.NoError(t, err)

		for _, state := range []string{StateActive, StateDetached, StateRevoked} {
			ok, err := s.SetState(station, state)
			require.NoError(t, err)
			require.True(t, ok)
			got, found, err := s.ByStation(station)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, state, got.State, "state must survive the round trip")
		}

		ok, err := s.SetState("st-nobody", StateRevoked)
		require.NoError(t, err)
		require.False(t, ok, "an unknown Station reports no change, not an error")
	})
}

func TestParityAnUnknownInvitationIsARefusalNotAnOutage(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, _ time.Time) {
		p := goodProof()
		p.AuthID = "auth-nope"
		_, err := r.Admit(p)
		require.ErrorIs(t, err, ErrRejected)
		require.NotErrorIs(t, err, ErrUnavailable)
	})
}

// Reap clears expired UNCONSUMED invitations and keeps consumed ones, because a consumed
// record is what answers a lost-response retry. Postgres only: it is the durable cleanup.
func TestReapKeepsWhatAnswersARetry(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("no ROGERAI_TEST_DATABASE_URL")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer db.Close()
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	pg, err := NewPGStore(db)
	require.NoError(t, err)
	_, err = db.Exec(`TRUNCATE rogerai.station_authorizations, rogerai.station_attachments`)
	require.NoError(t, err)

	now := time.Unix(1_700_000_000, 0).UTC()
	for i, spec := range []struct {
		id       string
		consumed bool
	}{{"stale-unspent", false}, {"stale-spent", true}} {
		require.NoError(t, pg.PutAuthorization(Authorization{
			ID: spec.id, Network: net, StationID: fmt.Sprintf("st-%d", i), Owner: owner,
			Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: "A", SessionKey: "K",
			IssuedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
			Consumed: spec.consumed, ConsumedBy: "st-x",
		}))
	}

	n, err := pg.Reap(now)
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "only the unspent expired invitation is reaped")

	_, ok, err := pg.Authorization("stale-spent")
	require.NoError(t, err)
	require.True(t, ok, "a consumed invitation is the record that answers a retry - it stays")
	_, ok, err = pg.Authorization("stale-unspent")
	require.NoError(t, err)
	require.False(t, ok)
}
