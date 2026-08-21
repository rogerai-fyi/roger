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

	n, err := pg.Reap(now, 24*time.Hour)
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

		n, err := s.Reap(now, 24*time.Hour)
		require.NoError(t, err)
		require.Equal(t, int64(1), n)

		for id, want := range map[string]bool{"stale": false, "spent": true, "live": true} {
			_, ok, err := s.Authorization(id)
			require.NoError(t, err)
			require.Equal(t, want, ok, "invitation %q", id)
		}
	})
}

// A consumed invitation is kept for retries and then it is NOT kept forever. Without a
// horizon, an operator looping invite -> redeem grows the table without bound - the same
// vector the per-owner cap closes for unredeemed rows, left open behind it.
func TestParityConsumedInvitationsAreKeptOnlyForTheRetryWindow(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, _ *Registry, now time.Time) {
		recent, _, err := NewInvite(Authorization{
			ID: "recent", Network: net, StationID: "st-r", Owner: capOwner,
			Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: "Ar", SessionKey: "Kr",
		}, time.Minute, now.Add(-2*time.Hour))
		require.NoError(t, err)
		recent.Consumed, recent.ConsumedBy = true, "st-r"
		require.NoError(t, s.PutAuthorization(recent))

		ancient, _, err := NewInvite(Authorization{
			ID: "ancient", Network: net, StationID: "st-a", Owner: capOwner,
			Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: "Aa", SessionKey: "Ka",
		}, time.Minute, now.Add(-90*24*time.Hour))
		require.NoError(t, err)
		ancient.Consumed, ancient.ConsumedBy = true, "st-a"
		require.NoError(t, s.PutAuthorization(ancient))

		_, err = s.Reap(now, 24*time.Hour)
		require.NoError(t, err)

		_, ok, err := s.Authorization("recent")
		require.NoError(t, err)
		require.True(t, ok, "a retry could still plausibly arrive for this one")
		_, ok, err = s.Authorization("ancient")
		require.NoError(t, err)
		require.False(t, ok, "no retry is still in flight after ninety days - this is storage")
	})
}

// Live attachments are capped per owner, enforced on the Admit path.
func TestParityLiveAttachmentsAreCappedPerOwner(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_700_000_000, 0).UTC()
			r := New(Config{
				Network: net, MaxLiveStationsPerOwner: 1,
				Now: func() time.Time { return now },
			}, s)

			seed := func(id string) error {
				a, secret, err := NewInvite(Authorization{
					ID: "inv-" + id, Network: net, StationID: id, Owner: capOwner,
					Origin:       Origin{Kind: OriginJoined, TowerID: tower},
					AssertionKey: "A" + id, SessionKey: "K" + id,
				}, time.Hour, now.Add(-time.Minute))
				require.NoError(t, err)
				require.NoError(t, s.PutAuthorization(a))
				_, aerr := r.Admit(Proof{
					AuthID: "inv-" + id, Secret: secret, Network: net, StationID: id, Owner: capOwner,
					Origin:       Origin{Kind: OriginJoined, TowerID: tower},
					AssertionKey: "A" + id, SessionKey: "K" + id,
				})
				return aerr
			}
			require.NoError(t, seed("st-one"))
			err := seed("st-two")
			require.ErrorIs(t, err, ErrRejected,
				"capping only the invitation leaves invite -> redeem as an unbounded loop")
			require.Contains(t, err.Error(), "already holds")

			// Retiring one frees the allowance.
			_, err = r.Revoke("st-one")
			require.NoError(t, err)
			require.NoError(t, seed("st-three"))
		})
	}
}

// A duplicate invitation id is refused the same way by both stores. Postgres has a primary
// key; the memory store used to overwrite silently.
func TestParityADuplicateInvitationIDIsRefusedByBoth(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, _ *Registry, now time.Time) {
		a, _, err := NewInvite(Authorization{
			ID: "dupe", Network: net, StationID: "st-d", Owner: capOwner,
			Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: "Ad", SessionKey: "Kd",
		}, time.Hour, now)
		require.NoError(t, err)
		ok, err := s.PutAuthorizationCapped(a, 10)
		require.NoError(t, err)
		require.True(t, ok)

		_, err = s.PutAuthorizationCapped(a, 10)
		require.ErrorIs(t, err, ErrRejected,
			"a duplicate id is a permanent answer, and both stores must give it")
	})
}

// --- the batch read and the detach path, in both stores ----------------------

// seedAttachments writes attachments straight through Admit so both stores go through their
// real commit path, and returns them keyed by Station. Each needs its own invitation: an
// invitation is one-use, which is the property half this file exists to protect.
func seedAttachments(t *testing.T, s Store, now time.Time, tw string, ids ...string) {
	t.Helper()
	for i, id := range ids {
		auth := withSecret(Authorization{
			ID: "auth-" + id, Network: net, StationID: id, Owner: owner,
			Origin:       Origin{Kind: OriginJoined, TowerID: tw},
			AssertionKey: fmt.Sprintf("A-%s-%d", id, i), SessionKey: fmt.Sprintf("K-%s-%d", id, i),
			IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		})
		require.NoError(t, s.PutAuthorization(auth))
		ok, err := s.Admit(auth.ID, Attachment{
			StationID: id, Owner: owner, AssertionKey: auth.AssertionKey,
			SessionKey: auth.SessionKey, Origin: auth.Origin, Epoch: 1,
			State: StateActive, AttachedAt: now, AuthID: auth.ID,
			NodeID: "n-" + id, Model: "m",
		})
		require.NoError(t, err)
		require.True(t, ok)
	}
}

// ByStations must answer EXACTLY what the same number of ByStation calls would, because that
// is the only thing that makes it a safe substitution on the placement path. Absent ids are
// absent (not zero values), a terminal row still comes back (the caller checks Live itself),
// and duplicates in the request collapse.
func TestParityByStationsMatchesByStationOneAtATime(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_700_000_000, 0).UTC()
			seedAttachments(t, s, now, tower, "st-a", "st-b", "st-gone")
			ok, err := s.SetState("st-gone", StateRevoked)
			require.NoError(t, err)
			require.True(t, ok)

			ask := []string{"st-a", "st-b", "st-a", "st-gone", "st-never-existed"}
			batch, err := s.ByStations(ask)
			require.NoError(t, err)

			for _, id := range ask {
				one, found, oerr := s.ByStation(id)
				require.NoError(t, oerr)
				got, inBatch := batch[id]
				require.Equal(t, found, inBatch,
					"%s: the batch read and the singular read disagree about whether it exists", id)
				if found {
					require.Equal(t, one, got, "%s: the batch read returned a different record", id)
				}
			}
			require.Len(t, batch, 3, "a duplicated id is one row, and an unknown id is no row")
			require.Equal(t, StateRevoked, batch["st-gone"].State,
				"a terminal row must still come back - the caller is the one that decides about liveness")

			// The empty ask is a real call on the authorize path (every candidate filtered out
			// before this point), and it must not be an error or a round trip's worth of nothing.
			empty, err := s.ByStations(nil)
			require.NoError(t, err)
			require.Empty(t, empty)
		})
	}
}

// THE DETACH PATH, which until now did not exist: nothing ever assigned StateDetached outside
// terminal reaping, so a machine that ran `roger share` once and pressed Ctrl-C stayed a live
// attachment forever. A Station whose machine has been seen recently must survive the sweep,
// one whose machine has not must not, and neither may reach across to another Tower.
func TestParityDetachIdleRetiresOnlyTheQuietStations(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_700_000_000, 0).UTC()
			seedAttachments(t, s, now, tower, "st-live", "st-quiet")
			seedAttachments(t, s, now, "tw-other", "st-elsewhere")

			// Only the live one is stamped, and the horizon sits between the stamp and the
			// attach instant every row shares.
			require.NoError(t, s.TouchRoutable([]string{"st-live"}, now.Add(time.Hour)))
			detached, err := s.DetachIdle(tower, now.Add(30*time.Minute))
			require.NoError(t, err)
			require.Equal(t, []string{"st-quiet"}, detached)

			quiet, _, err := s.ByStation("st-quiet")
			require.NoError(t, err)
			require.Equal(t, StateDormant, quiet.State,
				"the idle sweep is out-of-service, not end-of-identity - see StateDormant")
			require.False(t, quiet.Live(), "a dormant Station must carry no work")
			require.True(t, quiet.Recoverable(), "and must be able to come back")

			live, _, err := s.ByStation("st-live")
			require.NoError(t, err)
			require.Equal(t, StateActive, live.State, "a stamped Station is not idle")

			other, _, err := s.ByStation("st-elsewhere")
			require.NoError(t, err)
			require.Equal(t, StateActive, other.State, "the sweep is scoped to one Tower")

			// Idempotent: the row is no longer live, so a second sweep finds nothing to do.
			again, err := s.DetachIdle(tower, now.Add(30*time.Minute))
			require.NoError(t, err)
			require.Empty(t, again)
		})
	}
}

// seedClassicAttachment admits ONE operator-invited Station: no node id, no model, no hub
// token - the three things SelfAttached() keys on, all absent, which is exactly what the
// classic invite flow produces.
func seedClassicAttachment(t *testing.T, s Store, now time.Time, tw, id string) {
	t.Helper()
	auth := withSecret(Authorization{
		ID: "auth-" + id, Network: net, StationID: id, Owner: owner,
		Origin:       Origin{Kind: OriginJoined, TowerID: tw},
		AssertionKey: "A-" + id, SessionKey: "K-" + id,
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	})
	require.NoError(t, s.PutAuthorization(auth))
	ok, err := s.Admit(auth.ID, Attachment{
		StationID: id, Owner: owner, AssertionKey: auth.AssertionKey,
		SessionKey: auth.SessionKey, Origin: auth.Origin, Epoch: 1,
		State: StateActive, AttachedAt: now, AuthID: auth.ID,
	})
	require.NoError(t, err)
	require.True(t, ok)
}

// A CLASSIC STATION IS NEVER RETIRED BY THIS SWEEP, HOWEVER LONG IT SITS THERE.
//
// This is the leg that was missing, and its absence was not a coverage gap so much as an
// unstated assumption that turned out to be false. The sweep measures
// COALESCE(last_routable, attached_at), and last_routable is stamped by exactly one writer:
// publishRoutable, which joins an attachment's NODE ID to a broker's live registrations.
// A classic operator-invited Station has no node id - it is reached through its Tower's signed
// inventory and its machine never registers with a broker at all - so nothing on this side of
// the wire has ever seen it or ever can. Its COALESCE therefore stayed at attached_at forever,
// it crossed the seven-day horizon on schedule, and its own Tower's housekeeping tick retired
// it. StateDetached is terminal and unrecoverable, so that was an operator losing a Station
// permanently, on a timer, for being the kind of Station the stamp was never written for.
//
// The rule the stores now share: a row with no node id has no liveness evidence and no way to
// acquire any, so it is not a candidate for a sweep that retires on absence of evidence.
func TestParityAClassicStationIsNeverRetiredForBeingUnstampable(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_700_000_000, 0).UTC()
			seedClassicAttachment(t, s, now, tower, "st-classic")
			seedAttachments(t, s, now, tower, "st-self")

			// A horizon a year past everything either row could possibly be measured from.
			gone, err := s.DetachIdle(tower, now.Add(365*24*time.Hour))
			require.NoError(t, err)
			require.Equal(t, []string{"st-self"}, gone,
				"the sweep retired a Station it has no way to see alive")

			classic, _, err := s.ByStation("st-classic")
			require.NoError(t, err)
			require.Equal(t, StateActive, classic.State,
				"a classic Station was retired for having no node id to be stamped by")
			require.True(t, classic.Live())
		})
	}
}

// An UNSTAMPED row is measured from when it attached, not from the zero time. Getting this
// backwards would have retired every attachment in the fleet on the first sweep after deploy,
// because no existing row carries a stamp.
func TestParityAnUnstampedAttachmentIsMeasuredFromItsAttachTime(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_700_000_000, 0).UTC()
			seedAttachments(t, s, now, tower, "st-fresh")

			none, err := s.DetachIdle(tower, now.Add(-time.Second))
			require.NoError(t, err)
			require.Empty(t, none, "a never-stamped row attached a second ago is not idle")

			gone, err := s.DetachIdle(tower, now.Add(time.Second))
			require.NoError(t, err)
			require.Equal(t, []string{"st-fresh"}, gone,
				"past the horizon it is idle, measured from attached_at")
		})
	}
}

// A stamp for a Station that does not exist must not be remembered - otherwise it would
// pre-date a later attachment under the same id and make a fresh Station look ancient.
func TestParityTouchingAnUnknownStationRecordsNothing(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_700_000_000, 0).UTC()
			require.NoError(t, s.TouchRoutable([]string{"st-ghost"}, now.Add(-999*time.Hour)))
			seedAttachments(t, s, now, tower, "st-ghost")

			none, err := s.DetachIdle(tower, now.Add(-time.Second))
			require.NoError(t, err)
			require.Empty(t, none, "the ghost stamp outlived the Station it named")
		})
	}
}

// SEVEN DAYS UNSEEN WAS A PERMANENT LOSS OF THE STATION IDENTITY, AND THAT IS THE WHOLE
// FINDING.
//
// DetachIdle wrote StateDetached, which is terminal: checkBindings answers "this Station ID has
// been retired and cannot be reattached" to the very call agent.AttachTower makes on every
// single start - same persistent on-disk identity, same id, same keys - and ReapTerminal does
// not free the row for a fresh Station under that id for another month. So a two-week holiday,
// or a fortnight of downtime, converted a temporary outage into a permanent one.
//
// Worse than the timer is the dependency. The stamp DetachIdle measures has exactly one writer:
// publishRoutable joining an attachment's node id to a live registration. A liveness mirror
// broken for a week on the instance holding a Tower's link therefore retired every
// self-attached Station behind that Tower, irrecoverably, with nobody deciding anything.
//
// So the sweep is soft now, and this is the leg that says so: the same machine comes back.
func TestParityADormantStationComesBack(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_700_000_000, 0).UTC()
			seedAttachments(t, s, now, tower, "st-holiday")
			// Stamped once while it was alive, so the row carries a real last_routable to be
			// wrong about later.
			require.NoError(t, s.TouchRoutable([]string{"st-holiday"}, now))
			before, _, err := s.ByStation("st-holiday")
			require.NoError(t, err)

			// A fortnight away.
			gone, err := s.DetachIdle(tower, now.Add(14*24*time.Hour))
			require.NoError(t, err)
			require.Equal(t, []string{"st-holiday"}, gone)

			// AttachTower's call, verbatim in shape: the same Station ID, the same assertion
			// key, the same session key, the same owner, a fresh invitation.
			back := now.Add(15 * 24 * time.Hour)
			auth := withSecret(Authorization{
				ID: "auth-holiday-2", Network: net, StationID: "st-holiday", Owner: owner,
				Origin:       before.Origin,
				AssertionKey: before.AssertionKey, SessionKey: before.SessionKey,
				IssuedAt: back.Add(-time.Minute), ExpiresAt: back.Add(time.Hour),
			})
			require.NoError(t, s.PutAuthorization(auth))
			ok, err := s.Admit(auth.ID, Attachment{
				StationID: "st-holiday", Owner: owner, AssertionKey: before.AssertionKey,
				SessionKey: before.SessionKey, Origin: before.Origin, Epoch: before.Epoch + 1,
				State: StateActive, AttachedAt: back, AuthID: auth.ID,
				NodeID: before.NodeID, Model: before.Model,
			})
			require.NoError(t, err, "the machine that went on holiday could not come home")
			require.True(t, ok)

			woke, _, err := s.ByStation("st-holiday")
			require.NoError(t, err)
			require.Equal(t, StateActive, woke.State)
			require.True(t, woke.Live())
			require.Equal(t, before.Epoch+1, woke.Epoch, "a revival is a rehome and must fence the old origin")

			// AND THE OLD STAMP DID NOT COME WITH IT. Leaving last_routable in place would put
			// the fresh attachment straight back over the idle horizon - retired again on the
			// next sweep, seconds after coming home.
			// This horizon is past the OLD stamp and short of the new attach, so the row
			// survives only if the stale stamp was cleared and it is being measured from the
			// life it is actually living.
			none, err := s.DetachIdle(tower, back.Add(-time.Hour))
			require.NoError(t, err)
			require.Empty(t, none, "the revived Station was measured from its previous life's stamp")
		})
	}
}

// A DORMANT STATION KEEPS ITS KEYS. The assertion key is public - it rides in the clear on every
// hub poll and the node's own notice channel says so - so if going quiet released it, anybody
// could bind it to a Station of their own and the sleeping operator's return would be refused
// for a key they never gave up. Recovery that a stranger can block is not recovery.
func TestParityADormantStationStillHoldsItsKeys(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_700_000_000, 0).UTC()
			seedAttachments(t, s, now, tower, "st-asleep")
			sleeping, _, err := s.ByStation("st-asleep")
			require.NoError(t, err)
			_, err = s.DetachIdle(tower, now.Add(14*24*time.Hour))
			require.NoError(t, err)

			// A lookup by either key still finds it, which is what refuses the impostor below
			// and what the durable store's partial unique index enforces underneath.
			found, ok, err := s.ByAssertionKey(sleeping.AssertionKey)
			require.NoError(t, err)
			require.True(t, ok, "a dormant Station's assertion key reads as free")
			require.Equal(t, "st-asleep", found.StationID)

			// Somebody else, holding the public key they read off the wire, tries to take it.
			auth := withSecret(Authorization{
				ID: "auth-impostor", Network: net, StationID: "st-impostor", Owner: "other-owner-pub",
				Origin:       Origin{Kind: OriginJoined, TowerID: tower},
				AssertionKey: sleeping.AssertionKey, SessionKey: sleeping.SessionKey,
				IssuedAt: now, ExpiresAt: now.Add(time.Hour),
			})
			require.NoError(t, s.PutAuthorization(auth))
			won, err := s.Admit(auth.ID, Attachment{
				StationID: "st-impostor", Owner: "other-owner-pub",
				AssertionKey: sleeping.AssertionKey, SessionKey: sleeping.SessionKey,
				Origin: auth.Origin, Epoch: 1, State: StateActive, AttachedAt: now, AuthID: auth.ID,
				NodeID: "n-impostor", Model: "m",
			})
			require.Error(t, err, "a stranger took a sleeping Station's keys and blocked its return")
			require.False(t, won)
		})
	}
}

// AND THE SECOND HORIZON STILL ENDS IT, because a soft state that never hardens is just a table
// that grows. RetireDormant is the only sweep that makes a Station terminal, it runs on a
// horizon an order of magnitude longer, and nothing else may take that step for it.
func TestParityOnlyRetireDormantEndsAStationIdentity(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_700_000_000, 0).UTC()
			seedAttachments(t, s, now, tower, "st-gone")
			_, err := s.DetachIdle(tower, now.Add(14*24*time.Hour))
			require.NoError(t, err)

			// The TERMINAL reap must not touch it, however far past its own horizon it is:
			// dormant is not terminal, and a reap that deleted it would be the permanent loss
			// arriving by another route.
			n, err := s.ReapTerminal(now.Add(365 * 24 * time.Hour))
			require.NoError(t, err)
			require.Zero(t, n, "the terminal reap deleted a recoverable Station")
			still, ok, err := s.ByStation("st-gone")
			require.NoError(t, err)
			require.True(t, ok)
			require.True(t, still.Recoverable())

			// Nor does a horizon short of the dormant one.
			n, err = s.RetireDormant(now.Add(-time.Second))
			require.NoError(t, err)
			require.Zero(t, n)

			// Past it, the identity ends - and now it reads terminal to everybody.
			n, err = s.RetireDormant(now.Add(365 * 24 * time.Hour))
			require.NoError(t, err)
			require.Equal(t, int64(1), n)
			dead, _, err := s.ByStation("st-gone")
			require.NoError(t, err)
			require.Equal(t, StateDetached, dead.State)
			require.False(t, dead.Live())
			require.False(t, dead.Recoverable())

			// And its keys are free again, which terminal has always meant.
			_, ok, err = s.ByAssertionKey(dead.AssertionKey)
			require.NoError(t, err)
			require.False(t, ok, "a terminally retired Station still holds its keys hostage")
		})
	}
}

// A RE-WRITTEN INVITATION STAYS SPENT, AND THE TWO STORES HAVE TO AGREE ABOUT THAT.
//
// Postgres' PutAuthorization lists every column in its ON CONFLICT DO UPDATE except consumed
// and consumed_by: whether an invitation has been redeemed is the store's record of a race it
// arbitrated, and a later writer restating the invitation does not get to reopen it. The memory
// store replaced the whole row, so the same call left the same invitation UNSPENT - one store
// silently un-consuming what the other kept.
//
// It is not a difference anything in production exercises today, and it is still the most
// dangerous shape a parity gap can have, because Admit's very first question is
// `auth.Consumed`. Answer it wrongly and the replay branch is skipped: the caller runs on into
// checkBindings, hits the same-authorization short-circuit, is handed an EMPTY revived
// attachment and writes Epoch 1 over a Station sitting at 2. The next test is that failure seen
// from the store's side.
//
// THE OTHER DIRECTION IS DELIBERATELY NOT ASSERTED HERE, because the two stores still disagree
// about it and closing that gap is a separate piece of work with its own durable-store test.
// Marking an invitation consumed BY re-putting it - which toweredgeattach does when a
// self-attach is refused, so a refusal loop cannot fill an owner's invite cap - lands in the
// memory store and is dropped by Postgres. Asserting it here would make this test fail on the
// durable side for a defect it is not the fix for. See memstore.PutAuthorization for the rule
// both stores should end up on and pgstore.PutAuthorization for what is missing.
func TestParityRewritingAnInvitationDoesNotUnconsumeIt(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, now time.Time) {
		at, err := r.Admit(goodProof())
		require.NoError(t, err)
		require.Equal(t, int64(1), at.Epoch)

		spent, ok, err := s.Authorization(authorID)
		require.NoError(t, err)
		require.True(t, ok)
		require.True(t, spent.Consumed, "redeeming an invitation consumes it")

		// The same id written again, carrying the zero value for both flags - which is what any
		// caller re-minting from a fresh Authorization struct hands over.
		reissued := withSecret(Authorization{
			ID: authorID, Network: net, StationID: station, Owner: owner,
			Origin:       Origin{Kind: OriginJoined, TowerID: tower},
			AssertionKey: keyA, SessionKey: keyK, CeilingHash: "ceil-1",
			IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		})
		require.NoError(t, s.PutAuthorization(reissued))

		after, ok, err := s.Authorization(authorID)
		require.NoError(t, err)
		require.True(t, ok)
		require.True(t, after.Consumed,
			"a re-written invitation came back unspent: Admit would skip its replay branch and "+
				"attach against a station it has already attached")
		require.Equal(t, station, after.ConsumedBy)
	})
}

// AN EPOCH MAY ONLY GO UP, AND IT IS THE STORE THAT SAYS SO NOW.
//
// The Station epoch is what lets a settlement fence answer a grant minted under a superseded
// placement with a PERMANENT 410 instead of a retryable 503, and that argument is exactly one
// sentence long: nothing lowers it, so no retry can un-supersede the grant. Until this test the
// sentence was true only because Registry.Admit happened to be written that way - neither store
// had a constraint, and a caller that computed the wrong attachment could write the epoch
// backwards on either. An operator would then see honest receipts refused forever with a status
// code that tells the courier to throw them away.
//
// Driven through the STORE rather than through Admit deliberately: the invariant belongs to the
// row, and a test that could only reach it through the one caller that already respects it
// would be asserting the caller, not the rule.
func TestParityAnAttachmentsEpochNeverGoesBackwards(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, now time.Time) {
		at, err := r.Admit(goodProof())
		require.NoError(t, err)
		require.Equal(t, int64(1), at.Epoch)

		// Wake it once the honest way, so the row is at epoch 2 with a real history behind it.
		moved, err := s.SetState(station, StateDormant)
		require.NoError(t, err)
		require.True(t, moved)
		require.NoError(t, s.PutAuthorization(withSecret(Authorization{
			ID: "auth-2", Network: net, StationID: station, Owner: owner,
			Origin:       Origin{Kind: OriginJoined, TowerID: tower},
			AssertionKey: keyA, SessionKey: keyK, CeilingHash: "ceil-1",
			IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		})))
		revived := at
		revived.Epoch, revived.AuthID, revived.State = 2, "auth-2", StateQuarantine
		revived.AttachedAt = now
		won, err := s.Admit("auth-2", revived)
		require.NoError(t, err)
		require.True(t, won)

		// Now the write that must not land: same machine, same everything, an epoch that does
		// not advance. This is what Admit produces if it is handed a revived invitation whose
		// consumed flag was cleared underneath it.
		moved, err = s.SetState(station, StateDormant)
		require.NoError(t, err)
		require.True(t, moved)
		require.NoError(t, s.PutAuthorization(withSecret(Authorization{
			ID: "auth-3", Network: net, StationID: station, Owner: owner,
			Origin:       Origin{Kind: OriginJoined, TowerID: tower},
			AssertionKey: keyA, SessionKey: keyK, CeilingHash: "ceil-1",
			IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		})))
		regressed := revived
		regressed.Epoch, regressed.AuthID = 1, "auth-3"
		won, err = s.Admit("auth-3", regressed)
		require.False(t, won)
		require.ErrorIs(t, err, ErrRejected,
			"a write that lowers a Station's epoch must be refused, not merged")

		got, ok, err := s.ByStation(station)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, int64(2), got.Epoch, "the row kept the higher epoch")

		// AND THE REFUSAL COST NOBODY THEIR INVITATION, which is the standing rule for every
		// refusal on this path: the durable store does this by rolling its transaction back,
		// and the memory store by refusing before it consumes.
		unspent, ok, err := s.Authorization("auth-3")
		require.NoError(t, err)
		require.True(t, ok)
		require.False(t, unspent.Consumed, "a refused attachment must not spend the invitation")
	})
}
