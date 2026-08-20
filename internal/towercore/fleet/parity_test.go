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
		Modality: "text", Expires: expires,
		// The data-plane endpoint rides in every parity row so a store that DROPS it fails
		// loudly here rather than as edge consumers silently never being routed anywhere.
		Endpoint: "203.0.113.7:8443",
		// And so does the broker node id, for exactly the same reason - a lesson learned the
		// expensive way. When node_id was added this helper did not set it, so the value under
		// test was "" on both stores and nothing could disagree; PGStore.ByTower had in fact
		// failed to select the column at all. A field absent from this row is a field the
		// parity suites cannot police, however carefully they compare.
		NodeID: "n-" + station,
		// AND THE HUB CERTIFICATE PIN, for the third time and the same reason. It is what a
		// consumer checks the tower's certificate against before it submits sealed work; a
		// store that drops it hands that consumer a plaintext URL for a TLS listener, which
		// looks from the outside exactly like the tower being down.
		TLSSPKI: strings.Repeat("ab", 32),
		// AND THE PRICE, WHICH IS THE FOURTH TIME AND THE FIRST ONE ON THE MONEY PATH.
		//
		// These two were the last fields this helper left at their zero value, and zero is the
		// one value the parity suites cannot police: both stores returned 0, 0 == 0, and every
		// postgres subtest passed with price_in and price_out deleted from BOTH SELECTs and BOTH
		// Scans. That is not a hypothetical - it was run.
		//
		// The blast radius is the reason this is worse than node_id was. Authorize re-checks the
		// routable row's price against the public band with `if row.PriceIn != 0 || row.PriceOut
		// != 0 { ...band check... }` (cmd/rogerai-broker/toweredge.go), so a dropped column does
		// not read as "wrong price", it reads as "unpriced" - the band check is SKIPPED, the
		// grant is minted with no price pinned into it, and the attempt bills under the byte
		// tariff instead of the operator's listed per-token rate. No error, no log line, and the
		// canary mints at the same row's price. A guard that a missing column switches off is
		// worse than no guard, because the fleet view is where the number comes from.
		//
		// The two are DIFFERENT and neither is round, so a store that swaps the columns, or
		// binds one argument twice, fails here rather than comparing equal to itself. Both sit
		// inside towerPriceBand's public band for an ordinary model.
		PriceIn:  1200,
		PriceOut: 3400,
	}
}

// wholeRow asserts a row came back EXACTLY as it was written, on either store.
//
// Field-by-field assertions are how PGStore.ByTower came to drop node_id from its SELECT
// unnoticed, and how price_in/price_out could be deleted from both queries with ten postgres
// subtests still green. Comparing the whole struct means the next column added is covered the
// day it is added rather than the day somebody remembers to extend an assertion list.
//
// EXPIRES IS COMPARED SEPARATELY AND ON PURPOSE, rather than being copied from the result into
// the expectation to make the structs match - which is what the ByTower assertion used to do,
// and which compares the value against itself and could never fail. The two stores genuinely
// disagree about the REPRESENTATION of an instant: Postgres round-trips timestamptz at
// microsecond precision in UTC, and the in-memory store hands back the caller's own time.Time,
// wall clock, monotonic reading and location intact. Neither is wrong; a struct equality over
// them would fail for a reason that is not about the projection. So the instant is asserted as
// an instant, to a tolerance that is looser than Postgres's precision and far tighter than any
// divergence that would matter, and only then is the representation normalised away.
func wholeRow(t *testing.T, want, got Station) {
	t.Helper()
	require.WithinDuration(t, want.Expires, got.Expires, time.Millisecond,
		"the expiry came back as a different instant, not merely a different representation")
	want.Expires = got.Expires
	require.Equal(t, want, got, "every field written must come back, on both stores")
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

// The hub certificate pin must survive the round trip through BOTH stores, on BOTH read paths.
//
// ByTower is named explicitly because it is where the same mistake was made last time: when
// node_id was added, PGStore.ByTower did not select the column and nothing caught it, because
// the value being compared was empty on both sides. A canary reads its target through ByTower,
// so a pin lost there means Core probes over plaintext and records a reputation failure against
// every tower that turned TLS on - the change would punish exactly the right operators.
func TestParityTheHubCertificatePinSurvivesBothReadPaths(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		now := time.Now()
		pinned := row("tw-1", "st-1", "of-1", "m", now.Add(time.Hour))
		plain := row("tw-1", "st-2", "of-2", "m", now.Add(time.Hour))
		plain.TLSSPKI = "" // an operator who has not turned TLS on: still legal, still routable
		require.NoError(t, s.Replace("tw-1", []Station{pinned, plain}))

		cands, err := s.Candidates("m", now)
		require.NoError(t, err)
		require.Len(t, cands, 2)
		require.Equal(t, strings.Repeat("ab", 32), cands[0].TLSSPKI)
		require.Empty(t, cands[1].TLSSPKI, "a plaintext tower stays plaintext rather than inheriting a pin")

		byTower, err := s.ByTower("tw-1", now)
		require.NoError(t, err)
		require.Len(t, byTower, 2)
		require.Equal(t, strings.Repeat("ab", 32), byTower[0].TLSSPKI)
		require.Empty(t, byTower[1].TLSSPKI)
	})
}

// The name has always said WHOLE; it used to check four fields of eleven. It checks the row now,
// which is what makes the price columns - the ones an authorize prices a grant from - covered on
// the placement read path and not only on the canary's.
func TestParityCandidatesComeBackWhole(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		now := time.Now()
		exp := now.Add(time.Minute)
		want := row("tw-1", "st-1", "off-1", "m1", exp)
		require.NoError(t, s.Replace("tw-1", []Station{
			want,
			row("tw-1", "st-2", "off-2", "m2", exp),
		}))

		got, err := s.Candidates("m1", now)
		require.NoError(t, err)
		require.Len(t, got, 1)
		wholeRow(t, want, got[0])

		none, err := s.Candidates("nobody-serves-this", now)
		require.NoError(t, err)
		require.Empty(t, none)
	})
}

// A ROW IS PUBLISHED UNDER THE TOWER REPLACE WAS CALLED FOR, not under the one its own field
// happens to name, and the two stores must agree about that.
//
// PGStore binds the towerID ARGUMENT into every insert and never reads r.TowerID; the in-memory
// store used to keep the field. Every caller passes them equal - publishRoutable stamps both
// from one variable - so the divergence was invisible to a fixture that also always passed them
// equal, which is the same shape of blindness as a field left at its zero value. It is asserted
// rather than merely fixed because "the argument wins" is the durable store's behaviour and the
// reference has to be held to it.
func TestParityTheTowerAnArgumentNamesIsTheTowerTheRowIsPublishedUnder(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		now := time.Now()
		exp := now.Add(time.Hour)
		mislabelled := row("tw-somebody-else", "st-1", "of-1", "m", exp)
		require.NoError(t, s.Replace("tw-1", []Station{mislabelled}))

		got, err := s.Candidates("m", now)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, "tw-1", got[0].TowerID,
			"the row was published under the tower Replace names, so that is the tower it belongs to")

		mine, err := s.ByTower("tw-1", now)
		require.NoError(t, err)
		require.Len(t, mine, 1, "and it is that tower's row on the canary's read path too")
		require.Equal(t, "tw-1", mine[0].TowerID)

		none, err := s.ByTower("tw-somebody-else", now)
		require.NoError(t, err)
		require.Empty(t, none, "the field on the row never made it a second tower's row")
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

			// A SECOND ROUTABLE TOWER, so the assertion below is about a LIST and not about a
			// single element. With one expected member neither store's ordering could be wrong:
			// PGStore returned whatever DISTINCT gave it and the mem store ranged a map, and a
			// one-element result is sorted whatever you do to it. Both are ordered now, and this
			// is what would notice if either stopped being.
			alsoEdge := row("tw-alpha", "st-4", "of-4", "m", exp)
			require.NoError(t, s.Replace("tw-alpha", []Station{alsoEdge}))

			towers, err := s.RoutableTowers(now)
			require.NoError(t, err)
			require.Equal(t, []string{"tw-alpha", "tw-edge"}, towers,
				"only a Tower with an unexpired data plane can be canaried, and the fleet comes "+
					"back in the same order on both stores so a sweep walks it the same way twice")
		})
	}
}

// ByTower is a Tower's own unexpired rows - the canary's window into what it can probe -
// on both stores.
func TestParityByTower(t *testing.T) {
	now := time.Now()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			live := now.Add(time.Hour)
			require.NoError(t, s.Replace("tw-1", []Station{
				row("tw-1", "st-a", "of-a", "m", live),
				row("tw-1", "st-old", "of-old", "m", now.Add(-time.Minute)),
			}))
			require.NoError(t, s.Replace("tw-2", []Station{row("tw-2", "st-b", "of-b", "m", now.Add(time.Hour))}))

			mine, err := s.ByTower("tw-1", now)
			require.NoError(t, err)
			require.Len(t, mine, 1, "only this Tower's unexpired rows")
			require.Equal(t, "st-a", mine[0].StationID)
			require.Equal(t, "203.0.113.7:8443", mine[0].Endpoint)
			// Assert the WHOLE row, not two fields of it - see wholeRow, including why the
			// expiry is compared as an instant rather than copied out of the answer.
			wholeRow(t, row("tw-1", "st-a", "of-a", "m", live), mine[0])

			none, err := s.ByTower("tw-nobody", now)
			require.NoError(t, err)
			require.Empty(t, none)
		})
	}
}
