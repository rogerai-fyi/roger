package attach

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
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

// privateDSN redirects THIS package's Postgres tests to their own database.
//
// WHY: the parity harness TRUNCATEs its tables, which is safe within one package because its
// tests run sequentially - but `go test ./...` runs PACKAGES in parallel against the ONE
// shared ROGERAI_TEST_DATABASE_URL. Without this, a truncate here wipes rows the broker
// suite is mid-scenario on, and the failure surfaces over there as something inexplicable.
// internal/store hit exactly this and solved it the same way; the comment there is the
// standing record of how long it took to diagnose.
//
// A DSN that does not parse as a URL keeps the old shared-database behaviour.
var privateOnce sync.Once

func privateDSN(t *testing.T, dsn, suffix string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil || u.Path == "" || u.Path == "/" {
		return dsn
	}
	name := strings.TrimPrefix(u.Path, "/") + "_" + suffix
	privateOnce.Do(func() {
		admin, aerr := sql.Open("pgx", dsn)
		if aerr != nil {
			t.Fatalf("private db: open admin: %v", aerr)
		}
		defer admin.Close()
		// No CREATE DATABASE IF NOT EXISTS in PostgreSQL: create and tolerate "already exists".
		if _, cerr := admin.Exec(`CREATE DATABASE "` + name + `"`); cerr != nil &&
			!strings.Contains(cerr.Error(), "already exists") {
			t.Fatalf("private db: create %s: %v", name, cerr)
		}
	})
	u.Path = "/" + name
	return u.String()
}

func parityStores(t *testing.T) map[string]Store {
	t.Helper()
	out := map[string]Store{"mem": NewMemStore()}
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		return out
	}
	db, err := sql.Open("pgx", privateDSN(t, dsn, "stationattach"))
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
			require.NoError(t, s.PutAuthorization(withSecret(Authorization{
				ID: authorID, Network: net, StationID: station, Owner: owner,
				Origin:       Origin{Kind: OriginJoined, TowerID: tower},
				AssertionKey: keyA, SessionKey: keyK, CeilingHash: "ceil-1",
				IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
			})))
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
		require.NoError(t, s.PutAuthorization(withSecret(Authorization{
			ID: "auth-2", Network: net, StationID: "st-2", Owner: owner,
			Origin:       Origin{Kind: OriginJoined, TowerID: tower},
			AssertionKey: "A2", SessionKey: keyK,
			IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		})))
		p := Proof{
			AuthID: "auth-2", Secret: inviteSecret, Network: net, StationID: "st-2", Owner: owner,
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
		require.NoError(t, s.PutAuthorization(withSecret(Authorization{
			ID: "auth-2", Network: net, StationID: "st-2", Owner: owner,
			Origin:       Origin{Kind: OriginJoined, TowerID: tower},
			AssertionKey: keyA, SessionKey: keyK,
			IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		})))
		at, err := r.Admit(Proof{
			AuthID: "auth-2", Secret: inviteSecret, Network: net, StationID: "st-2", Owner: owner,
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
	db, err := sql.Open("pgx", privateDSN(t, dsn, "stationattach"))
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
		require.NoError(t, pg.PutAuthorization(withSecret(Authorization{
			ID: spec.id, Network: net, StationID: fmt.Sprintf("st-%d", i), Owner: owner,
			Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: "A", SessionKey: "K",
			IssuedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
			Consumed: spec.consumed, ConsumedBy: "st-x",
		})))
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

// --- the audit's findings, pinned in both stores ----------------------------

// A LIVE Station ID cannot be re-attached under a second invitation.
//
// Found by audit. memStore.Admit used to overwrite the record - silently resetting state
// active->quarantine, epoch to 1, and the authorization lineage - while Postgres hit the
// station_id primary key and reported ErrUnavailable, a permanent refusal dressed as a
// transient one that invites an infinite retry. Reachable because the invite route only
// refuses a Station that is ALREADY attached, so two invitations can exist for one ID.
func TestParityALiveStationCannotBeReattached(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, now time.Time) {
		first, err := r.Admit(goodProof())
		require.NoError(t, err)
		_, err = r.Promote(station)
		require.NoError(t, err)

		second, secret, err := NewInvite(Authorization{
			ID: "auth-2", Network: net, StationID: station, Owner: owner,
			Origin:       Origin{Kind: OriginJoined, TowerID: tower},
			AssertionKey: keyA, SessionKey: keyK,
		}, time.Hour, now.Add(-time.Minute))
		require.NoError(t, err)
		require.NoError(t, s.PutAuthorization(second))

		p := goodProof()
		p.AuthID, p.Secret = "auth-2", secret
		_, err = r.Admit(p)
		require.ErrorIs(t, err, ErrRejected,
			"a second invitation must not replace a live attachment")
		require.NotErrorIs(t, err, ErrUnavailable,
			"and it is a permanent answer, not a blip to retry against forever")

		// The original is untouched, including the state promotion moved it to.
		got, ok, err := s.ByStation(station)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, StateActive, got.State, "the live attachment kept its state")
		require.Equal(t, first.AuthID, got.AuthID, "and its lineage")
	})
}

// Two DISTINCT invitations sharing a key, redeemed concurrently. Postgres refuses one via a
// partial unique index; the memory store must refuse one too, or the stores disagree exactly
// where it matters. A sequential test cannot see this - checkBindings catches it first.
func TestParityConcurrentAttachmentsCannotShareAKey(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, now time.Time) {
		second, secret, err := NewInvite(Authorization{
			ID: "auth-2", Network: net, StationID: "st-2", Owner: owner,
			Origin: Origin{Kind: OriginJoined, TowerID: tower},
			// A DIFFERENT Station, the SAME secure-session key.
			AssertionKey: "A2", SessionKey: keyK,
		}, time.Hour, now.Add(-time.Minute))
		require.NoError(t, err)
		require.NoError(t, s.PutAuthorization(second))

		var wg sync.WaitGroup
		results := make([]error, 2)
		start := make(chan struct{})
		proofs := []Proof{goodProof(), {
			AuthID: "auth-2", Secret: secret, Network: net, StationID: "st-2", Owner: owner,
			Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: "A2", SessionKey: keyK,
		}}
		for i := range proofs {
			wg.Add(1)
			go func(i int) { defer wg.Done(); <-start; _, results[i] = r.Admit(proofs[i]) }(i)
		}
		close(start)
		wg.Wait()

		wins := 0
		for _, err := range results {
			if err == nil {
				wins++
			}
		}
		require.Equal(t, 1, wins,
			"exactly one may hold a secure-session key, whichever store is underneath")

		// And the loser left nothing behind.
		live := 0
		for _, id := range []string{station, "st-2"} {
			if at, ok, err := s.ByStation(id); err == nil && ok && at.Live() {
				live++
			}
		}
		require.Equal(t, 1, live)
	})
}

// A store refusal must reach the caller AS a refusal.
//
// Found by the second audit: Registry.Admit wrapped every store error as ErrUnavailable with
// %v, which flattened the sentinel - so the permanent refusals the stores had just learned to
// raise arrived at the handler as "try again in a moment", reinstating the infinite-retry bug
// the previous fix claimed to close. A test that only counts winners cannot see this.
func TestParityALoserLearnsItIsPermanent(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, now time.Time) {
		_, err := r.Admit(goodProof())
		require.NoError(t, err)

		second, secret, err := NewInvite(Authorization{
			ID: "auth-2", Network: net, StationID: station, Owner: owner,
			Origin:       Origin{Kind: OriginJoined, TowerID: tower},
			AssertionKey: keyA, SessionKey: keyK,
		}, time.Hour, now.Add(-time.Minute))
		require.NoError(t, err)
		require.NoError(t, s.PutAuthorization(second))

		p := goodProof()
		p.AuthID, p.Secret = "auth-2", secret
		_, err = r.Admit(p)
		require.ErrorIs(t, err, ErrRejected, "a permanent answer must arrive as a refusal")
		require.NotErrorIs(t, err, ErrUnavailable,
			"reporting it as an outage invites a caller to retry forever against something "+
				"that will never change")
	})
}

// capOwner keeps these tests off the fixture's owner, which already holds one seeded
// invitation - counting it would make every cap here off by one for a reason that has
// nothing to do with the cap.
const capOwner = "cap-owner-pub"

// --- the invitation cap, in both stores -------------------------------------

// THE CAP IS ENFORCED BY THE WRITE, not by counting first.
//
// Counting and then inserting is a check-then-act: concurrent calls all read the same count,
// all pass, and all insert - overshooting by the caller's concurrency once per TTL window. A
// cap that only holds when nobody is trying is not a cap. Postgres takes a per-owner advisory
// lock; the memory store counts under the same held mutex it writes with.
func TestParityTheInvitationCapHoldsUnderConcurrency(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, _ *Registry, now time.Time) {
		const max = 5
		const racers = 20

		var wg sync.WaitGroup
		wrote := make([]bool, racers)
		start := make(chan struct{})
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				a, _, err := NewInvite(Authorization{
					ID: fmt.Sprintf("inv-%d", i), Network: net,
					StationID: fmt.Sprintf("st-%d", i), Owner: capOwner,
					Origin:       Origin{Kind: OriginJoined, TowerID: tower},
					AssertionKey: fmt.Sprintf("A%d", i), SessionKey: fmt.Sprintf("K%d", i),
				}, time.Hour, now)
				if err != nil {
					return
				}
				<-start
				wrote[i], _ = s.PutAuthorizationCapped(a, max)
			}(i)
		}
		close(start)
		wg.Wait()

		got := 0
		for _, w := range wrote {
			if w {
				got++
			}
		}
		require.Equal(t, max, got,
			"exactly the cap may be written, however many callers arrive at once")
	})
}

// A different owner has their own allowance: the cap bounds an account, not the table.
func TestParityTheCapIsPerOwner(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, _ *Registry, now time.Time) {
		mint := func(who, id string) bool {
			a, _, err := NewInvite(Authorization{
				ID: id, Network: net, StationID: "st-" + id, Owner: who,
				Origin:       Origin{Kind: OriginJoined, TowerID: tower},
				AssertionKey: "A" + id, SessionKey: "K" + id,
			}, time.Hour, now)
			require.NoError(t, err)
			ok, err := s.PutAuthorizationCapped(a, 1)
			require.NoError(t, err)
			return ok
		}
		require.True(t, mint(capOwner, "one"))
		require.False(t, mint(capOwner, "two"), "the owner is at their cap")
		require.True(t, mint("someone-else", "three"), "another account is unaffected")
	})
}

// A spent or expired invitation frees allowance - otherwise an operator who uses the feature
// as designed eventually cannot invite at all.
func TestParityConsumedAndExpiredInvitationsDoNotCountAgainstTheCap(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, _ *Registry, now time.Time) {
		spent, _, err := NewInvite(Authorization{
			ID: "spent", Network: net, StationID: "st-a", Owner: capOwner,
			Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: "Aa", SessionKey: "Ka",
		}, time.Hour, now)
		require.NoError(t, err)
		spent.Consumed, spent.ConsumedBy = true, "st-a"
		require.NoError(t, s.PutAuthorization(spent))

		stale, _, err := NewInvite(Authorization{
			ID: "stale", Network: net, StationID: "st-b", Owner: capOwner,
			Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: "Ab", SessionKey: "Kb",
		}, time.Minute, now.Add(-2*time.Hour))
		require.NoError(t, err)
		require.NoError(t, s.PutAuthorization(stale))

		fresh, _, err := NewInvite(Authorization{
			ID: "fresh", Network: net, StationID: "st-c", Owner: capOwner,
			Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: "Ac", SessionKey: "Kc",
		}, time.Hour, now)
		require.NoError(t, err)
		ok, err := s.PutAuthorizationCapped(fresh, 1)
		require.NoError(t, err)
		require.True(t, ok, "neither a spent nor an expired invitation occupies the allowance")
	})
}

// Reaping clears expired unredeemed invitations and keeps the consumed ones that answer a
// lost-response retry. Both stores, because a reaper that behaves differently on the durable
// side is a reaper nobody can reason about.
func TestParityReapDropsTheStaleAndKeepsTheAnswerable(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, _ *Registry, now time.Time) {
		stale, _, err := NewInvite(Authorization{
			ID: "stale", Network: net, StationID: "st-a", Owner: capOwner,
			Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: "Aa", SessionKey: "Ka",
		}, time.Minute, now.Add(-2*time.Hour))
		require.NoError(t, err)
		require.NoError(t, s.PutAuthorization(stale))

		spent := stale
		spent.ID, spent.Consumed, spent.ConsumedBy = "spent", true, "st-a"
		require.NoError(t, s.PutAuthorization(spent))

		live, _, err := NewInvite(Authorization{
			ID: "live", Network: net, StationID: "st-c", Owner: capOwner,
			Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: "Ac", SessionKey: "Kc",
		}, time.Hour, now)
		require.NoError(t, err)
		require.NoError(t, s.PutAuthorization(live))

		n, err := s.Reap(now)
		require.NoError(t, err)
		require.Equal(t, int64(1), n)

		for id, want := range map[string]bool{"stale": false, "spent": true, "live": true} {
			_, ok, err := s.Authorization(id)
			require.NoError(t, err)
			require.Equal(t, want, ok, "invitation %q", id)
		}
	})
}
