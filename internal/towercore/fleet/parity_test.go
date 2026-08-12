package fleet

// parity_test.go runs one scenario through both projections and requires the same answer.
//
// The stakes are lower here than for the attempt store - this is a read model, and every
// dispatch re-checks the attachment before issuing anything - but the two are still written
// differently enough to disagree, and a fleet view that says a Station is routable when it is
// not costs a caller a whole attempt deadline.

import (
	"database/sql"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

var privateOnce sync.Once

func privateDSN(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil || u.Path == "" || u.Path == "/" {
		return dsn
	}
	name := strings.TrimPrefix(u.Path, "/") + "_fleet"
	privateOnce.Do(func() {
		admin, aerr := sql.Open("pgx", dsn)
		if aerr != nil {
			t.Fatalf("private db: open admin: %v", aerr)
		}
		defer admin.Close()
		if _, cerr := admin.Exec(`CREATE DATABASE "` + name + `"`); cerr != nil &&
			!strings.Contains(cerr.Error(), "already exists") {
			t.Fatalf("private db: create %s: %v", name, cerr)
		}
	})
	u.Path = "/" + name
	return u.String()
}

func stores(t *testing.T) map[string]Store {
	t.Helper()
	out := map[string]Store{"mem": NewMemStore()}
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		return out
	}
	db, err := sql.Open("pgx", privateDSN(t, dsn))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	pg, err := NewPGStore(db)
	require.NoError(t, err)
	_, err = db.Exec(`TRUNCATE rogerai.tower_routable`)
	require.NoError(t, err)
	out["postgres"] = pg
	return out
}

func each(t *testing.T, fn func(t *testing.T, s Store)) {
	t.Helper()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) { fn(t, s) })
	}
}

func row(tower, station, offer, model string, expires time.Time) Station {
	return Station{
		TowerID: tower, StationID: station, OfferID: offer, Model: model,
		Modality: "text", Capacity: 4, Expires: expires,
		// The data-plane endpoint rides in every parity row so a store that DROPS it fails
		// loudly here rather than as edge consumers silently never being routed anywhere.
		Endpoint: "203.0.113.7:8443",
	}
}

// The endpoint must survive the round trip through BOTH stores: it is what an edge consumer
// is told to connect to, and a store that loses it demotes every Station behind that Tower
// to the Core-relayed path without anything failing.
func TestParityTheEndpointSurvivesTheRoundTrip(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			exp := time.Now().Add(time.Hour)
			require.NoError(t, s.Replace("tw-1", []Station{row("tw-1", "st-1", "of-1", "m", exp)}))
			got, err := s.Candidates("m", time.Now())
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, "203.0.113.7:8443", got[0].Endpoint)

			// And a Tower with NO endpoint stays that way rather than inheriting one.
			bare := row("tw-2", "st-2", "of-2", "m", exp)
			bare.Endpoint = ""
			require.NoError(t, s.Replace("tw-2", []Station{bare}))
			got, err = s.Candidates("m", time.Now())
			require.NoError(t, err)
			for _, r := range got {
				if r.TowerID == "tw-2" {
					require.Empty(t, r.Endpoint)
				}
			}
		})
	}
}

func TestParityCandidatesComeBackWhole(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		now := time.Now()
		require.NoError(t, s.Replace("tw-1", []Station{
			row("tw-1", "st-1", "off-1", "m1", now.Add(time.Minute)),
			row("tw-1", "st-2", "off-2", "m2", now.Add(time.Minute)),
		}))

		got, err := s.Candidates("m1", now)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, "tw-1", got[0].TowerID)
		require.Equal(t, "st-1", got[0].StationID)
		require.Equal(t, "off-1", got[0].OfferID)
		require.Equal(t, "text", got[0].Modality)
		require.Equal(t, int64(4), got[0].Capacity)

		none, err := s.Candidates("nobody-serves-this", now)
		require.NoError(t, err)
		require.Empty(t, none)
	})
}

// A REVISION IS A COMPLETE STATEMENT. Anything not in the new one is no longer offered, so a
// withdrawn Station must disappear rather than linger until something notices.
func TestParityReplaceWithdrawsWhatIsNoLongerOffered(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		now := time.Now()
		require.NoError(t, s.Replace("tw-1", []Station{
			row("tw-1", "st-1", "off-1", "m1", now.Add(time.Minute)),
			row("tw-1", "st-2", "off-2", "m1", now.Add(time.Minute)),
		}))
		got, err := s.Candidates("m1", now)
		require.NoError(t, err)
		require.Len(t, got, 2)

		// The next revision carries only one of them.
		require.NoError(t, s.Replace("tw-1", []Station{
			row("tw-1", "st-1", "off-1", "m1", now.Add(time.Minute)),
		}))
		got, err = s.Candidates("m1", now)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, "st-1", got[0].StationID)

		// And an EMPTY revision withdraws the lot: "I am here and I have nothing" is a real
		// statement a Tower makes, not an absence of one.
		require.NoError(t, s.Replace("tw-1", nil))
		got, err = s.Candidates("m1", now)
		require.NoError(t, err)
		require.Empty(t, got)
	})
}

// One Tower's revision never disturbs another's.
func TestParityReplaceTouchesOnlyItsOwnTower(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		now := time.Now()
		require.NoError(t, s.Replace("tw-1", []Station{row("tw-1", "st-1", "off-1", "m1", now.Add(time.Minute))}))
		require.NoError(t, s.Replace("tw-2", []Station{row("tw-2", "st-2", "off-2", "m1", now.Add(time.Minute))}))

		require.NoError(t, s.Replace("tw-1", nil))
		got, err := s.Candidates("m1", now)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, "tw-2", got[0].TowerID)
	})
}

// Expiry is the same rule as the in-memory view: offered until the accepted inventory ages
// out, wherever the question is asked.
func TestParityExpiredRowsAreNotCandidates(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		now := time.Now()
		require.NoError(t, s.Replace("tw-1", []Station{row("tw-1", "st-1", "off-1", "m1", now.Add(time.Minute))}))

		got, err := s.Candidates("m1", now.Add(2*time.Minute))
		require.NoError(t, err)
		require.Empty(t, got, "an aged-out inventory offers nothing")

		n, err := s.Reap(now.Add(2 * time.Minute))
		require.NoError(t, err)
		require.Equal(t, int64(1), n)
	})
}

// Draining removes the fleet AT ONCE - that is the whole point of draining rather than
// walking away and letting the freshness window run out.
func TestParityForgettingATowerIsImmediate(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		now := time.Now()
		require.NoError(t, s.Replace("tw-1", []Station{row("tw-1", "st-1", "off-1", "m1", now.Add(time.Hour))}))
		require.NoError(t, s.Forget("tw-1"))

		got, err := s.Candidates("m1", now)
		require.NoError(t, err)
		require.Empty(t, got)

		// Forgetting a Tower that was never here is not an error: a drain from an instance
		// that never accepted this Tower's inventory is an ordinary thing to happen.
		require.NoError(t, s.Forget("tw-nobody"))
	})
}

// Reaping spares the living.
func TestParityReapingSparesLiveRows(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		now := time.Now()
		require.NoError(t, s.Replace("tw-1", []Station{
			row("tw-1", "st-old", "off-old", "m1", now.Add(time.Minute)),
			row("tw-1", "st-new", "off-new", "m1", now.Add(time.Hour)),
		}))
		n, err := s.Reap(now.Add(2 * time.Minute))
		require.NoError(t, err)
		require.Equal(t, int64(1), n)

		got, err := s.Candidates("m1", now.Add(2*time.Minute))
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, "st-new", got[0].StationID)
	})
}

func TestADurableFleetViewNeedsADatabase(t *testing.T) {
	_, err := NewPGStore(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "database handle")
}

// A database that has gone away is reported, never read as an empty fleet: "nothing is
// routable" is an answer the router acts on, and it would be the wrong one.
func TestADeadDatabaseIsReportedRatherThanReadAsAnEmptyFleet(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set ROGERAI_TEST_DATABASE_URL to exercise the durable view")
	}
	db, err := sql.Open("pgx", privateDSN(t, dsn))
	require.NoError(t, err)
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	s, err := NewPGStore(db)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	now := time.Now()
	require.Error(t, s.Replace("tw-1", []Station{row("tw-1", "st-1", "off-1", "m1", now)}))
	_, err = s.Candidates("m1", now)
	require.Error(t, err)
	require.Error(t, s.Forget("tw-1"))
	_, err = s.Reap(now)
	require.Error(t, err)
}

// RoutableTowers lists distinct Towers that could carry an edge attempt - those with an
// unexpired endpoint row - on both stores, so a canary sweep probes the same fleet either way.
func TestParityRoutableTowers(t *testing.T) {
	now := time.Now()
	exp := now.Add(time.Hour)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			// tw-edge has an endpoint; tw-relayonly is routable but has no data plane; tw-stale
			// has an endpoint that has expired.
			withEndpoint := row("tw-edge", "st-1", "of-1", "m", exp)
			require.NoError(t, s.Replace("tw-edge", []Station{withEndpoint}))

			noEndpoint := row("tw-relayonly", "st-2", "of-2", "m", exp)
			noEndpoint.Endpoint = ""
			require.NoError(t, s.Replace("tw-relayonly", []Station{noEndpoint}))

			stale := row("tw-stale", "st-3", "of-3", "m", now.Add(-time.Minute))
			require.NoError(t, s.Replace("tw-stale", []Station{stale}))

			towers, err := s.RoutableTowers(now)
			require.NoError(t, err)
			require.Equal(t, []string{"tw-edge"}, towers,
				"only a Tower with an unexpired data plane can be canaried")
		})
	}
}

// ByTower is a Tower's own unexpired rows - the canary's window into what it can probe -
// on both stores.
func TestParityByTower(t *testing.T) {
	now := time.Now()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, s.Replace("tw-1", []Station{
				row("tw-1", "st-a", "of-a", "m", now.Add(time.Hour)),
				func() Station { r := row("tw-1", "st-old", "of-old", "m", now.Add(-time.Minute)); return r }(),
			}))
			require.NoError(t, s.Replace("tw-2", []Station{row("tw-2", "st-b", "of-b", "m", now.Add(time.Hour))}))

			mine, err := s.ByTower("tw-1", now)
			require.NoError(t, err)
			require.Len(t, mine, 1, "only this Tower's unexpired rows")
			require.Equal(t, "st-a", mine[0].StationID)
			require.Equal(t, "203.0.113.7:8443", mine[0].Endpoint)

			none, err := s.ByTower("tw-nobody", now)
			require.NoError(t, err)
			require.Empty(t, none)
		})
	}
}
