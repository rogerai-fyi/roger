package reputation

// parity_test.go runs one scenario through BOTH reputation stores and requires the same
// answer. The two are written differently - a slice scanned in Go against grouped SQL counts
// - so agreement is a result rather than the same code asserted twice, which matters because
// a rate that differs between mem and PG would flag Towers on one deployment and not another.
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
	name := strings.TrimPrefix(u.Path, "/") + "_reputation"
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
	_, err = db.Exec(`TRUNCATE rogerai.tower_outcomes`)
	require.NoError(t, err)
	out["postgres"] = pg
	return out
}

func at(base time.Time, mins int) time.Time { return base.Add(time.Duration(mins) * time.Minute) }

func TestParityARateIsCountedTheSameOnBothStores(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 7; i++ {
				require.NoError(t, s.Record(Event{TowerID: "tw-1", AttemptID: idOf("c", i),
					Outcome: Corroborated, At: at(base, i)}))
			}
			for i := 0; i < 3; i++ {
				require.NoError(t, s.Record(Event{TowerID: "tw-1", AttemptID: idOf("u", i),
					Outcome: Uncorroborated, At: at(base, i)}))
			}
			tally, err := s.Tally("tw-1", base)
			require.NoError(t, err)
			require.Equal(t, 10, tally.Total)
			require.Equal(t, 7, tally.Corroborated)
			require.Equal(t, 3, tally.Uncorroborated)
			rate, known := tally.UncorroboratedRate()
			require.True(t, known)
			require.InDelta(t, 0.3, rate, 1e-9)
		})
	}
}

// IDEMPOTENT on (tower, attempt, outcome): a retried settlement write must not count twice.
func TestParityRecordingTheSameOutcomeTwiceCountsOnce(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			e := Event{TowerID: "tw-1", AttemptID: "att-1", Outcome: Uncorroborated, At: base}
			require.NoError(t, s.Record(e))
			require.NoError(t, s.Record(e))
			tally, err := s.Tally("tw-1", base)
			require.NoError(t, err)
			require.Equal(t, 1, tally.Total)
		})
	}
}

// The window excludes what is older than `since`, on both stores.
func TestParityTheWindowExcludesOldEvidence(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, s.Record(Event{TowerID: "tw-1", AttemptID: "old",
				Outcome: Uncorroborated, At: at(base, -120)}))
			require.NoError(t, s.Record(Event{TowerID: "tw-1", AttemptID: "new",
				Outcome: Corroborated, At: base}))
			tally, err := s.Tally("tw-1", at(base, -60))
			require.NoError(t, err)
			require.Equal(t, 1, tally.Total, "only the in-window outcome counts")
			require.Equal(t, 1, tally.Corroborated)
		})
	}
}

func TestParityTheFleetTallySumsEveryTower(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, s.Record(Event{TowerID: "tw-1", AttemptID: "a",
				Outcome: Corroborated, At: base}))
			require.NoError(t, s.Record(Event{TowerID: "tw-2", AttemptID: "b",
				Outcome: Uncorroborated, At: base}))
			fleet, err := s.FleetTally(base)
			require.NoError(t, err)
			require.Equal(t, 2, fleet.Total)
			require.Equal(t, 1, fleet.Corroborated)
			require.Equal(t, 1, fleet.Uncorroborated)
		})
	}
}

func TestParityOldEvidenceIsReaped(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, s.Record(Event{TowerID: "tw-1", AttemptID: "old",
				Outcome: Corroborated, At: at(base, -120)}))
			require.NoError(t, s.Record(Event{TowerID: "tw-1", AttemptID: "new",
				Outcome: Corroborated, At: base}))
			n, err := s.Reap(at(base, -60))
			require.NoError(t, err)
			require.Equal(t, int64(1), n)
			tally, err := s.Tally("tw-1", at(base, -1000))
			require.NoError(t, err)
			require.Equal(t, 1, tally.Total)

			// Reaping frees the idempotency key too, so a much later attempt reusing an id
			// (it will not, but the store must not assume) is not silently swallowed.
			require.NoError(t, s.Record(Event{TowerID: "tw-1", AttemptID: "old",
				Outcome: Corroborated, At: at(base, 5)}))
			tally, err = s.Tally("tw-1", base)
			require.NoError(t, err)
			require.Equal(t, 2, tally.Total)
		})
	}
}

func TestRecordRefusesMalformedOutcomes(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			base := time.Unix(1_700_000_000, 0)
			require.Error(t, s.Record(Event{AttemptID: "a", Outcome: Corroborated, At: base}))
			require.Error(t, s.Record(Event{TowerID: "t", Outcome: Corroborated, At: base}))
			require.Error(t, s.Record(Event{TowerID: "t", AttemptID: "a", Outcome: "made-up", At: base}))
			require.Error(t, s.Record(Event{TowerID: "t", AttemptID: "a", Outcome: Corroborated}))
		})
	}
}

func TestADurableReputationLedgerNeedsADatabase(t *testing.T) {
	_, err := NewPGStore(nil)
	require.ErrorContains(t, err, "needs a database handle")
}

func idOf(prefix string, i int) string {
	return prefix + "-" + string(rune('a'+i))
}

// Every outcome kind lands in its own column, on both stores - the mapping mem and PG share.
func TestParityEveryOutcomeKindIsCounted(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	kinds := []Outcome{Corroborated, Uncorroborated, Disputed, CanaryPass, CanaryFail, AuditMismatch}
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			for i, k := range kinds {
				require.NoError(t, s.Record(Event{TowerID: "tw-1",
					AttemptID: string(k), Outcome: k, At: at(base, i)}))
			}
			tally, err := s.Tally("tw-1", base)
			require.NoError(t, err)
			require.Equal(t, 6, tally.Total)
			require.Equal(t, 1, tally.Corroborated)
			require.Equal(t, 1, tally.Uncorroborated)
			require.Equal(t, 1, tally.Disputed)
			require.Equal(t, 1, tally.CanaryPass)
			require.Equal(t, 1, tally.CanaryFail)
			require.Equal(t, 1, tally.AuditMismatch)
			fr, known := tally.CanaryFailRate()
			require.True(t, known)
			require.InDelta(t, 0.5, fr, 1e-9)
		})
	}
}
