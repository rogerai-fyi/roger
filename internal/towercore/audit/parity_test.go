package audit

// parity_test.go runs the wanted list through BOTH stores and requires the same answer. The
// two differ - a map scanned in Go against SQL with DELETE ... RETURNING - so agreement is a
// result, which matters because an audit that fired on one deployment and not another would
// let content go unreviewed exactly where the store happened to be Postgres.
//
// Without ROGERAI_TEST_DATABASE_URL the durable half skips and the memory half still runs.

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
	name := strings.TrimPrefix(u.Path, "/") + "_audit"
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
	_, err = db.Exec(`TRUNCATE rogerai.tower_audit_wanted`)
	require.NoError(t, err)
	out["postgres"] = pg
	return out
}

func want(tower, attempt string, deadline time.Time) Wanted {
	return Wanted{TowerID: tower, AttemptID: attempt, StationID: "st-1",
		RequestDigest: "rq", ResponseDigest: "rs", Deadline: deadline}
}

func TestParityWantedIsListedForItsTowerOnly(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, s.Want(want("tw-1", "a", base.Add(time.Hour))))
			require.NoError(t, s.Want(want("tw-2", "b", base.Add(time.Hour))))
			p, err := s.Pending("tw-1", base)
			require.NoError(t, err)
			require.Len(t, p, 1)
			require.Equal(t, "a", p[0].AttemptID)
			require.Equal(t, "rs", p[0].ResponseDigest)
		})
	}
}

func TestParityWantIsIdempotent(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, s.Want(want("tw-1", "a", base.Add(time.Hour))))
			require.NoError(t, s.Want(want("tw-1", "a", base.Add(2*time.Hour))))
			p, err := s.Pending("tw-1", base)
			require.NoError(t, err)
			require.Len(t, p, 1)
		})
	}
}

func TestParityResolveRemovesFromTheList(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, s.Want(want("tw-1", "a", base.Add(time.Hour))))
			require.NoError(t, s.Resolve("a"))
			p, err := s.Pending("tw-1", base)
			require.NoError(t, err)
			require.Empty(t, p)
			// Resolving something absent is a no-op, not an error: the courier retries.
			require.NoError(t, s.Resolve("nope"))
		})
	}
}

// Overdue reads-and-removes exactly once, and a still-pending one is not swept.
func TestParityOverdueIsClaimedOnce(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, s.Want(want("tw-1", "late", base.Add(-time.Minute))))
			require.NoError(t, s.Want(want("tw-1", "fresh", base.Add(time.Hour))))

			od, err := s.Overdue(base)
			require.NoError(t, err)
			require.Len(t, od, 1)
			require.Equal(t, "late", od[0].AttemptID)

			// Swept once: a second call finds nothing, so a "cannot produce" is reported once.
			od, err = s.Overdue(base)
			require.NoError(t, err)
			require.Empty(t, od)

			// The fresh one is still pending.
			p, err := s.Pending("tw-1", base)
			require.NoError(t, err)
			require.Len(t, p, 1)
		})
	}
}

// A past-deadline attempt is not handed out as pending work - it belongs to Overdue now.
func TestParityPendingExcludesOverdue(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, s.Want(want("tw-1", "late", base.Add(-time.Minute))))
			p, err := s.Pending("tw-1", base)
			require.NoError(t, err)
			require.Empty(t, p)
		})
	}
}

func TestWantRefusesMalformed(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			require.Error(t, s.Want(Wanted{AttemptID: "a", StationID: "s", ResponseDigest: "d", Deadline: base}))
			require.Error(t, s.Want(Wanted{TowerID: "t", StationID: "s", ResponseDigest: "d", Deadline: base}))
			require.Error(t, s.Want(Wanted{TowerID: "t", AttemptID: "a", ResponseDigest: "d", Deadline: base}))
			require.Error(t, s.Want(Wanted{TowerID: "t", AttemptID: "a", StationID: "s", Deadline: base}))
			require.Error(t, s.Want(Wanted{TowerID: "t", AttemptID: "a", StationID: "s", ResponseDigest: "d"}))
		})
	}
}

func TestADurableAuditListNeedsADatabase(t *testing.T) {
	_, err := NewPGStore(nil)
	require.ErrorContains(t, err, "needs a database handle")
}
