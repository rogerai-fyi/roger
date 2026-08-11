package dispatch

// parity_test.go runs one scenario through BOTH attempt stores and requires the same answer.
//
// The two are written deliberately differently - a held mutex and a map against a conditional
// UPDATE and a row count - so agreement between them is a result rather than a restatement of
// the same code twice. That matters more here than almost anywhere else in the system: these
// stores exist to enforce "exactly once", and an exactly-once rule that holds in memory and
// not in the database is worse than none, because everything above it is written believing it.
//
// Without ROGERAI_TEST_DATABASE_URL the durable half skips and the memory half still runs.

import (
	"crypto/ed25519"
	"crypto/rand"
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

// privateDSN redirects THIS package's Postgres tests to their own database.
//
// `go test ./...` runs packages in parallel against one DSN, and this suite deletes rows.
// internal/store and towercore/attach both hit that and solved it the same way; the failure
// shows up in the OTHER package as something inexplicable, which is what made it expensive.
func privateDSN(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil || u.Path == "" || u.Path == "/" {
		return dsn
	}
	name := strings.TrimPrefix(u.Path, "/") + "_dispatch"
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

// parityStores returns every store implementation under test.
func parityStores(t *testing.T) map[string]Store {
	t.Helper()
	out := map[string]Store{"mem": NewMemStore()}

	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		return out
	}
	db, err := sql.Open("pgx", privateDSN(t, dsn))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	// The schema is provisioned by an admin in production; a test database has to make it.
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	pg, err := NewPGStore(db)
	require.NoError(t, err)
	// A clean slate per test, scoped to THIS package's table.
	_, err = db.Exec(`TRUNCATE rogerai.tower_attempts`)
	require.NoError(t, err)
	out["postgres"] = pg
	return out
}

func parityRecord(t *testing.T, id, tower string, deadline time.Time) Record {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return Record{
		AttemptID: id, JobID: "job-" + id, TowerID: tower, StationID: "st-1",
		StationEpoch: 2, Model: "m1", Modality: "text", RequestDigest: "digest",
		Nonce: "nonce-" + id, Deadline: deadline, Grant: []byte(`{"grant":true}`),
		Request: []byte(`{"request":true}`), AssertionKey: pub, State: StateIssued,
	}
}

func eachStore(t *testing.T, fn func(t *testing.T, s Store)) {
	t.Helper()
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) { fn(t, s) })
	}
}

// What was put in comes back out, including the key a receipt will be verified against.
func TestParityRoundTrip(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		now := time.Now()
		want := parityRecord(t, "att-round", "tw-1", now.Add(time.Minute))
		require.NoError(t, s.Put(want))

		got, ok, err := s.Get("att-round")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, want.AttemptID, got.AttemptID)
		require.Equal(t, want.TowerID, got.TowerID)
		require.Equal(t, want.StationID, got.StationID)
		require.Equal(t, want.StationEpoch, got.StationEpoch)
		require.Equal(t, want.RequestDigest, got.RequestDigest)
		require.Equal(t, want.Grant, got.Grant)
		require.Equal(t, want.Request, got.Request,
			"the bytes another instance will hand the Tower must survive the round trip")
		require.Equal(t, []byte(want.AssertionKey), []byte(got.AssertionKey),
			"the key the receipt is checked against must survive the round trip")
		require.Equal(t, StateIssued, got.State)
		require.WithinDuration(t, want.Deadline, got.Deadline, time.Second)

		_, ok, err = s.Get("att-nobody")
		require.NoError(t, err)
		require.False(t, ok)
	})
}

// EXACTLY ONE CLAIM WINS, and the loser is told which of the reasons it was.
func TestParityClaimHappensOnce(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		now := time.Now()
		require.NoError(t, s.Put(parityRecord(t, "att-claim", "tw-1", now.Add(time.Minute))))

		got, err := s.ClaimByID("att-claim", "tw-1", now)
		require.NoError(t, err)
		require.Equal(t, StateClaimed, got.State)

		_, err = s.ClaimByID("att-claim", "tw-1", now)
		require.ErrorIs(t, err, ErrAlreadyClaimed)
	})
}

// Another Tower gets NOT FOUND, not "forbidden": an attempt id is not a secret, and telling
// one Tower that another's attempt exists is an oracle it has no business having.
func TestParityAnotherTowerSeesNothing(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		now := time.Now()
		require.NoError(t, s.Put(parityRecord(t, "att-mine", "tw-1", now.Add(time.Minute))))

		_, err := s.ClaimByID("att-mine", "tw-2", now)
		require.ErrorIs(t, err, ErrNotFound)
		_, err = s.ClaimByID("att-nobody", "tw-1", now)
		require.ErrorIs(t, err, ErrNotFound)

		// And it is still claimable by the Tower it belongs to: the wrong one consumed nothing.
		_, err = s.ClaimByID("att-mine", "tw-1", now)
		require.NoError(t, err)
	})
}

// The poll: any waiting attempt, claimed in the same step, and never the same one twice.
func TestParityClaimNextIsAQueueThatCannotDoubleServe(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		now := time.Now()
		require.NoError(t, s.Put(parityRecord(t, "att-a", "tw-1", now.Add(time.Minute))))
		require.NoError(t, s.Put(parityRecord(t, "att-b", "tw-1", now.Add(time.Minute))))
		require.NoError(t, s.Put(parityRecord(t, "att-other", "tw-2", now.Add(time.Minute))))

		seen := map[string]bool{}
		for i := 0; i < 2; i++ {
			got, ok, err := s.ClaimNext("tw-1", now)
			require.NoError(t, err)
			require.True(t, ok)
			require.False(t, seen[got.AttemptID], "the same attempt was handed out twice")
			require.Equal(t, StateClaimed, got.State)
			seen[got.AttemptID] = true
		}
		require.Len(t, seen, 2)

		// Nothing left for this Tower, and the other Tower's work was never a candidate.
		_, ok, err := s.ClaimNext("tw-1", now)
		require.NoError(t, err)
		require.False(t, ok)

		got, ok, err := s.ClaimNext("tw-2", now)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "att-other", got.AttemptID)
	})
}

// CONCURRENT POLLS. This is the whole reason the store exists: two brokers asking at once
// must not both be handed the same work.
func TestParityConcurrentPollsNeverDoubleServe(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		now := time.Now()
		const attempts = 8
		for i := 0; i < attempts; i++ {
			require.NoError(t, s.Put(parityRecord(t,
				"att-race-"+string(rune('a'+i)), "tw-1", now.Add(time.Minute))))
		}

		var mu sync.Mutex
		seen := map[string]int{}
		var wg sync.WaitGroup
		for i := 0; i < attempts*2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, ok, err := s.ClaimNext("tw-1", now)
				if err != nil || !ok {
					return
				}
				mu.Lock()
				seen[got.AttemptID]++
				mu.Unlock()
			}()
		}
		wg.Wait()

		require.Len(t, seen, attempts, "every attempt should have been handed out once")
		for id, n := range seen {
			require.Equal(t, 1, n, "%s was handed out %d times", id, n)
		}
	})
}

// EXACTLY ONE RESULT SETTLES, and it must have been claimed first.
func TestParitySettlementHappensOnce(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		now := time.Now()
		require.NoError(t, s.Put(parityRecord(t, "att-settle", "tw-1", now.Add(time.Minute))))

		_, err := s.Settle("att-settle", now)
		require.ErrorIs(t, err, ErrNotClaimed, "nothing executed, so there is no result")

		_, err = s.ClaimByID("att-settle", "tw-1", now)
		require.NoError(t, err)

		got, err := s.Settle("att-settle", now)
		require.NoError(t, err)
		require.Equal(t, StateSettled, got.State)

		_, err = s.Settle("att-settle", now)
		require.ErrorIs(t, err, ErrAlreadySettled)

		// And a settled attempt cannot be re-claimed and served again.
		_, err = s.ClaimByID("att-settle", "tw-1", now)
		require.ErrorIs(t, err, ErrAlreadySettled)

		_, err = s.Settle("att-nobody", now)
		require.ErrorIs(t, err, ErrNotFound)
	})
}

// CONCURRENT SETTLEMENT. Two brokers handed the same result must not both accept it.
func TestParityConcurrentSettlementAcceptsOne(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		now := time.Now()
		require.NoError(t, s.Put(parityRecord(t, "att-race-settle", "tw-1", now.Add(time.Minute))))
		_, err := s.ClaimByID("att-race-settle", "tw-1", now)
		require.NoError(t, err)

		var wg sync.WaitGroup
		won := make(chan struct{}, 16)
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, serr := s.Settle("att-race-settle", now); serr == nil {
					won <- struct{}{}
				}
			}()
		}
		wg.Wait()
		close(won)
		require.Len(t, won, 1, "exactly one settlement may be accepted")
	})
}

// The deadline ends an attempt for every operation, not just some of them.
func TestParityAnExpiredAttemptIsDeadEverywhere(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		now := time.Now()
		require.NoError(t, s.Put(parityRecord(t, "att-old", "tw-1", now.Add(time.Minute))))
		later := now.Add(2 * time.Minute)

		_, err := s.ClaimByID("att-old", "tw-1", later)
		require.ErrorIs(t, err, ErrExpired)

		_, ok, err := s.ClaimNext("tw-1", later)
		require.NoError(t, err)
		require.False(t, ok, "expired work must not be handed out")

		// Claimed in time, settled too late: still refused.
		require.NoError(t, s.Put(parityRecord(t, "att-slow", "tw-1", now.Add(time.Minute))))
		_, err = s.ClaimByID("att-slow", "tw-1", now)
		require.NoError(t, err)
		_, err = s.Settle("att-slow", later)
		require.ErrorIs(t, err, ErrExpired)
	})
}

// Reaping bounds the table and spares what is still live.
func TestParityReapingTakesOnlyTheDead(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		now := time.Now()
		require.NoError(t, s.Put(parityRecord(t, "att-dead", "tw-1", now.Add(time.Minute))))
		require.NoError(t, s.Put(parityRecord(t, "att-live", "tw-1", now.Add(time.Hour))))

		n, err := s.Reap(now.Add(2 * time.Minute))
		require.NoError(t, err)
		require.Equal(t, int64(1), n)

		_, ok, err := s.Get("att-dead")
		require.NoError(t, err)
		require.False(t, ok)
		_, ok, err = s.Get("att-live")
		require.NoError(t, err)
		require.True(t, ok, "the live attempt survived the sweep")
	})
}

// Re-putting the same attempt id changes nothing. A retry after a lost response must not
// quietly reset an attempt somebody has already claimed.
func TestParityPuttingTheSameAttemptTwiceIsIdempotent(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		now := time.Now()
		rec := parityRecord(t, "att-twice", "tw-1", now.Add(time.Minute))
		require.NoError(t, s.Put(rec))
		_, err := s.ClaimByID("att-twice", "tw-1", now)
		require.NoError(t, err)

		require.NoError(t, s.Put(rec))
		got, ok, err := s.Get("att-twice")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, StateClaimed, got.State, "a re-put must not un-claim an attempt")
	})
}

// The registry's own view of the queue, over whichever store it was given.
func TestParityTheRegistryHandsOutWaitingWork(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		r := NewWithStore(Config{Network: "roger-public", Signer: priv, Lifetime: time.Minute}, s)

		_, _, ok, err := r.ClaimNext("tw-1")
		require.NoError(t, err)
		require.False(t, ok, "an idle Tower is handed nothing")

		g, err := r.Issue(Target{
			TowerID: "tw-1", StationID: "st-1", Model: "m1", Modality: "text",
			AssertionKey: pub,
		}, []byte(`{"x":1}`))
		require.NoError(t, err)

		got, request, ok, err := r.ClaimNext("tw-1")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, g.AttemptID, got.AttemptID)
		require.NotEmpty(t, got.Signed, "the Tower is handed the signed grant, not a summary")
		// AND THE REQUEST. A Tower handed an authorization without the bytes it authorizes
		// has nothing to relay, and the instance serving the poll may never have seen them.
		require.Equal(t, `{"x":1}`, string(request))

		// Claimed by the handout, so it is not offered twice and cannot be claimed again.
		_, _, ok, err = r.ClaimNext("tw-1")
		require.NoError(t, err)
		require.False(t, ok)
		_, err = r.Claim(g.AttemptID, "tw-1")
		require.ErrorIs(t, err, ErrAlreadyClaimed)
	})
}

// A durable store needs a database, and says so rather than returning something that fails
// later at a distance from the mistake.
func TestADurableStoreNeedsADatabase(t *testing.T) {
	_, err := NewPGStore(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "database handle")
}

// A database that has gone away is reported, never mistaken for "no such attempt". Treating
// an unreachable store as an empty one would hand out work twice: the claim would look
// unclaimed to every instance that could not read it.
func TestADeadDatabaseIsReportedRatherThanReadAsEmpty(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set ROGERAI_TEST_DATABASE_URL to exercise the durable store")
	}
	db, err := sql.Open("pgx", privateDSN(t, dsn))
	require.NoError(t, err)
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	s, err := NewPGStore(db)
	require.NoError(t, err)
	require.NoError(t, db.Close()) // the database is gone from here on

	now := time.Now()
	require.Error(t, s.Put(parityRecord(t, "att-x", "tw-1", now.Add(time.Minute))))
	_, _, err = s.Get("att-x")
	require.Error(t, err)
	_, err = s.ClaimByID("att-x", "tw-1", now)
	require.Error(t, err)
	_, _, err = s.ClaimNext("tw-1", now)
	require.Error(t, err)
	_, err = s.Settle("att-x", now)
	require.Error(t, err)
	_, err = s.Reap(now)
	require.Error(t, err)
}
