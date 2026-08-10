package head

import (
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Both stores, one contract. The implementations differ in the way that matters - a mutex
// and a comparison in Go, versus a conditional upsert the database evaluates - so agreement
// is a real result. That asymmetry is exactly what shipped the band occupancy bug.

const tower = "tw-1"

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

func stores(t *testing.T) map[string]Store {
	t.Helper()
	out := map[string]Store{"mem": NewMemStore()}
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		return out
	}
	db, err := sql.Open("pgx", privateDSN(t, dsn, "towerhead"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	pg, err := NewPGStore(db)
	require.NoError(t, err)
	_, err = db.Exec(`TRUNCATE rogerai.tower_inventory_head`)
	require.NoError(t, err)
	out["postgres"] = pg
	return out
}

func each(t *testing.T, fn func(t *testing.T, r *Reconciler, s Store)) {
	t.Helper()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_700_000_000, 0).UTC()
			fn(t, New(s, func() time.Time { return now }), s)
		})
	}
}

// The case that pays for the table: a fleet reconnecting after a deploy sends ~100 bytes
// each instead of ~5.4 MB each.
func TestAMatchingHeadResumes(t *testing.T) {
	each(t, func(t *testing.T, r *Reconciler, _ Store) {
		advanced, err := r.Accept(tower, 40, "hash-40")
		require.NoError(t, err)
		require.True(t, advanced)

		out, err := r.Reconcile(tower, 40, "hash-40")
		require.NoError(t, err)
		require.Equal(t, Resume, out)
		require.False(t, out.NeedsFullInventory(),
			"an exact match is the whole point - it must not cost a snapshot")
		require.False(t, out.Suspicious())
	})
}

func TestEveryOtherCaseAsksForEverything(t *testing.T) {
	each(t, func(t *testing.T, r *Reconciler, _ Store) {
		_, err := r.Accept(tower, 40, "hash-40")
		require.NoError(t, err)

		for _, tc := range []struct {
			name    string
			rev     int64
			hash    string
			want    Outcome
			suspect bool
			whyItIs string
		}{
			{"the same revision under a different hash", 40, "hash-other", Fork, true,
				"the Tower signed two different objects as one revision"},
			{"a revision behind ours", 39, "hash-39", Replay, true,
				"a chain position at or behind one we already accepted"},
			{"a revision ahead of anything we accepted", 41, "hash-41", NeedFull, false,
				"we never accepted it, so there is nothing to resume from"},
			{"no claim at all", 0, "", NeedFull, false,
				"a Tower starting clean is honest, not suspicious"},
			{"a revision with no hash", 40, "", NeedFull, false,
				"an unverifiable claim is not a resume"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				out, err := r.Reconcile(tower, tc.rev, tc.hash)
				require.NoError(t, err)
				require.Equal(t, tc.want, out, tc.whyItIs)
				require.True(t, out.NeedsFullInventory(),
					"anything but an exact match must re-validate from scratch")
				require.Equal(t, tc.suspect, out.Suspicious(),
					"whether this is evidence about the Tower or ordinary bookkeeping")
			})
		}
	})
}

func TestAnUnknownTowerAsksForEverythingWithoutSuspicion(t *testing.T) {
	each(t, func(t *testing.T, r *Reconciler, _ Store) {
		out, err := r.Reconcile("tw-never-seen", 7, "hash-7")
		require.NoError(t, err)
		require.Equal(t, NeedFull, out)
		require.False(t, out.Suspicious(),
			"a first connect is not evidence of anything")
	})
}

// The property that makes this safe across instances.
func TestAHeadNeverMovesBackwards(t *testing.T) {
	each(t, func(t *testing.T, r *Reconciler, s Store) {
		_, err := r.Accept(tower, 40, "hash-40")
		require.NoError(t, err)

		// A slower instance finishing an OLDER revision must not rewind the chain - a rewound
		// head would make the next reconnect look like a fork to everybody.
		advanced, err := r.Accept(tower, 39, "hash-39")
		require.NoError(t, err)
		require.False(t, advanced, "an older revision must not be recorded")

		advanced, err = r.Accept(tower, 40, "hash-40-different")
		require.NoError(t, err)
		require.False(t, advanced, "nor may the same revision be rewritten")

		h, ok, err := s.Head(tower)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, int64(40), h.Revision)
		require.Equal(t, "hash-40", h.Hash, "the recorded head is untouched")

		advanced, err = r.Accept(tower, 41, "hash-41")
		require.NoError(t, err)
		require.True(t, advanced, "but it still advances")
	})
}

// Concurrency is where a read-then-write implementation would lose. Many writers, one
// winner per revision, and the head ends at the highest.
func TestConcurrentRecordsLandOnTheHighestRevision(t *testing.T) {
	each(t, func(t *testing.T, r *Reconciler, s Store) {
		const writers = 16
		var wg sync.WaitGroup
		wins := make([]bool, writers)
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				// Deliberately out of order, so no writer can rely on arriving last.
				rev := int64(writers - i)
				wins[i], _ = r.Accept(tower, rev, "hash")
			}(i)
		}
		wg.Wait()

		h, ok, err := s.Head(tower)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, int64(writers), h.Revision,
			"the head must end at the highest revision anyone recorded, whatever the order")
	})
}

func TestForgettingATowerLeavesNothingToResumeFrom(t *testing.T) {
	each(t, func(t *testing.T, r *Reconciler, _ Store) {
		_, err := r.Accept(tower, 40, "hash-40")
		require.NoError(t, err)
		require.NoError(t, r.Forget(tower))

		_, ok, err := r.Head(tower)
		require.NoError(t, err)
		require.False(t, ok)

		// A revoked Tower must not leave a head a later impostor could "resume".
		out, err := r.Reconcile(tower, 40, "hash-40")
		require.NoError(t, err)
		require.Equal(t, NeedFull, out)
	})
}

func TestAcceptRefusesAnIncompleteHead(t *testing.T) {
	each(t, func(t *testing.T, r *Reconciler, _ Store) {
		for _, tc := range []struct {
			name string
			id   string
			rev  int64
			hash string
		}{
			{"no Tower", "", 1, "h"},
			{"a zero revision", tower, 0, "h"},
			{"a negative revision", tower, -1, "h"},
			{"no hash", tower, 1, ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := r.Accept(tc.id, tc.rev, tc.hash)
				require.Error(t, err, "an incomplete head is not something to record")
			})
		}
	})
}

// --- outage behaviour -------------------------------------------------------

type brokenStore struct{ Store }

func (brokenStore) Head(string) (Head, bool, error) { return Head{}, false, errors.New("db down") }
func (brokenStore) Record(Head) (bool, error)       { return false, errors.New("db down") }
func (brokenStore) Forget(string) error             { return errors.New("db down") }

// An instance that cannot read its own record must ask for everything. Resuming on the
// Tower's say-so would mean trusting unverified input precisely when we have nothing to
// check it against.
func TestAnUnreadableStoreAsksForEverything(t *testing.T) {
	r := New(brokenStore{}, nil)

	out, err := r.Reconcile(tower, 40, "hash-40")
	require.Equal(t, NeedFull, out, "the safe answer is the only answer available")
	require.ErrorIs(t, err, ErrUnavailable,
		"and the caller must be able to tell an outage from a decision, to log the difference")

	_, err = r.Accept(tower, 41, "hash-41")
	require.ErrorIs(t, err, ErrUnavailable)

	_, _, err = r.Head(tower)
	require.ErrorIs(t, err, ErrUnavailable)

	require.ErrorIs(t, r.Forget(tower), ErrUnavailable)
}

func TestOutcomeNamesAreStable(t *testing.T) {
	// These strings end up in logs and operator tooling; a silent rename would break both.
	require.Equal(t, "resume", Resume.String())
	require.Equal(t, "need-full", NeedFull.String())
	require.Equal(t, "replay", Replay.String())
	require.Equal(t, "fork", Fork.String())
	require.Equal(t, "unknown", Outcome(99).String())
}

func TestPGStoreNeedsADatabase(t *testing.T) {
	_, err := NewPGStore(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "database handle")
}

func TestAClosedPoolReportsAnOutage(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("no ROGERAI_TEST_DATABASE_URL")
	}
	db, err := sql.Open("pgx", privateDSN(t, dsn, "towerhead"))
	require.NoError(t, err)
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	pg, err := NewPGStore(db)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = pg.Record(Head{TowerID: tower, Revision: 1, Hash: "h", UpdatedAt: time.Now()})
	require.ErrorIs(t, err, ErrUnavailable)
	_, _, err = pg.Head(tower)
	require.ErrorIs(t, err, ErrUnavailable)
	require.ErrorIs(t, pg.Forget(tower), ErrUnavailable)

	_, err = NewPGStore(db)
	require.Error(t, err, "a store that cannot apply its schema must not report success")
}

// The CHECK constraint is the database refusing what the code already refuses. Belt and
// braces on the one column whose monotonicity everything else rests on.
func TestTheDatabaseRefusesANonPositiveRevision(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("no ROGERAI_TEST_DATABASE_URL")
	}
	db, err := sql.Open("pgx", privateDSN(t, dsn, "towerhead"))
	require.NoError(t, err)
	defer db.Close()
	_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	_, err = NewPGStore(db)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO rogerai.tower_inventory_head(tower_id,revision,hash,updated_at)
	                  VALUES ('tw-bad', 0, 'h', now())`)
	require.Error(t, err, "revision 0 is not a position on any chain")
}
