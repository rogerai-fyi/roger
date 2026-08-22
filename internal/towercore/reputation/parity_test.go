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

// THE STATION DIMENSION, on both stores. A Tower's window splits by the Station each outcome
// concerns, the outcomes that name no Station land under the empty key, and the split adds back
// up to the flat tally - a grouped scan that disagreed with the ungrouped one would attribute a
// finding on one deployment and not on another, which is the whole reason these two are held
// against each other.
func TestParityAWindowSplitsByStation(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, s.Record(Event{TowerID: "tw-1", StationID: "st-a",
				AttemptID: "a1", Outcome: CanaryPass, At: base}))
			require.NoError(t, s.Record(Event{TowerID: "tw-1", StationID: "st-b",
				AttemptID: "b1", Outcome: CanaryFail, At: base}))
			require.NoError(t, s.Record(Event{TowerID: "tw-1", StationID: "st-b",
				AttemptID: "b2", Outcome: StationFault, At: base}))
			// A finding that concerns no single Station - a Tower advertising an unusable data
			// plane is reached before any Station is.
			require.NoError(t, s.Record(Event{TowerID: "tw-1",
				AttemptID: "t1", Outcome: CanaryFail, At: base}))
			// Another Tower's rows must not leak in.
			require.NoError(t, s.Record(Event{TowerID: "tw-2", StationID: "st-a",
				AttemptID: "x1", Outcome: CanaryFail, At: base}))

			byStation, err := s.TallyByStation("tw-1", base)
			require.NoError(t, err)
			require.Len(t, byStation, 3, "got %+v", byStation)
			require.Equal(t, 1, byStation["st-a"].CanaryPass)
			require.Equal(t, 1, byStation["st-b"].CanaryFail)
			require.Equal(t, 1, byStation["st-b"].StationFault)
			require.Equal(t, 1, byStation[""].CanaryFail,
				"an outcome naming no Station must not be filed under one")

			flat, err := s.Tally("tw-1", base)
			require.NoError(t, err)
			sum := 0
			for _, t2 := range byStation {
				sum += t2.Total
			}
			require.Equal(t, flat.Total, sum, "the split does not add up to the tally")
			require.Equal(t, 2, flat.CanaryFail)
			require.Equal(t, 1, flat.StationFault)
		})
	}
}

// THE STATION IS NOT PART OF THE IDEMPOTENCY KEY. An attempt has one terminal outcome, and
// widening the key with a fourth column would let two writes that disagreed about the Station
// both land - double-counting wearing an attribution's clothes. The first writer's value stands.
func TestParityTheStationIsNotPartOfTheIdempotencyKey(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, s.Record(Event{TowerID: "tw-1", StationID: "st-a",
				AttemptID: "att-1", Outcome: CanaryFail, At: base}))
			require.NoError(t, s.Record(Event{TowerID: "tw-1", StationID: "st-b",
				AttemptID: "att-1", Outcome: CanaryFail, At: base}))
			flat, err := s.Tally("tw-1", base)
			require.NoError(t, err)
			require.Equal(t, 1, flat.Total, "one attempt, one outcome, one row")
			byStation, err := s.TallyByStation("tw-1", base)
			require.NoError(t, err)
			require.Equal(t, 1, byStation["st-a"].CanaryFail)
			require.Empty(t, byStation["st-b"])
		})
	}
}

// A STATION FAULT IS COUNTED AND NEVER JUDGED. It is in the tally so it can be shown to an
// operator and so a future policy has something to read; it is in no rate, and it is not what
// takes a Tower off the network. See Evaluate for why the default runs the other way.
func TestAStationFaultNeverQuarantinesTheTower(t *testing.T) {
	p := DefaultPolicy()
	tower := Tally{Corroborated: 100, StationFault: 50}
	require.Equal(t, Clean, p.Evaluate(tower, Tally{}),
		"a Tower was taken off the network for the misbehaviour of a machine behind it")
	// And it moves no rate: the shares the policy reads are over settled attempts and canaries.
	rate, known := tower.UncorroboratedRate()
	require.True(t, known)
	require.Zero(t, rate)
	_, known = tower.CanaryFailRate()
	require.False(t, known)
	// While the Tower's OWN mismatch still does, on one event.
	require.Equal(t, Quarantine, p.Evaluate(Tally{Corroborated: 100, AuditMismatch: 1}, Tally{}))
	// Without subtracts it componentwise like every other counter, so a fleet baseline that
	// included it would not be left carrying one Tower's stations.
	require.Equal(t, 0, Tally{StationFault: 3}.Without(Tally{StationFault: 3}).StationFault)
}

// THE UPGRADE PATH, against a real Postgres that already has the table.
//
// The station column arrived after this table shipped, so it is an ALTER rather than a line in
// the CREATE - and a deployment that already has the table would never see a line added to the
// CREATE, because CREATE TABLE IF NOT EXISTS is a no-op on it. That failure is silent in the
// worst way: the store opens, records succeed, and every outcome is filed under no Station at
// all, which is exactly the state this work exists to leave.
//
// So this builds the PRE-CHANGE schema by hand, opens the store over it, and requires the
// attribution to survive. It goes red if the ALTER is ever folded back into the CREATE.
func TestTheStationColumnReachesATableThatAlreadyExists(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("no ROGERAI_TEST_DATABASE_URL")
	}
	db, err := sql.Open("pgx", privateDSN(t, dsn))
	require.NoError(t, err)
	defer db.Close()
	// Exactly the table as it stood before the station dimension.
	_, err = db.Exec(`DROP TABLE IF EXISTS rogerai.tower_outcomes`)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE rogerai.tower_outcomes (
		tower_id   TEXT        NOT NULL,
		attempt_id TEXT        NOT NULL,
		outcome    TEXT        NOT NULL,
		at         TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (tower_id, attempt_id, outcome))`)
	require.NoError(t, err)
	// One row written by the old code, before the column existed.
	base := time.Unix(1_700_000_000, 0)
	_, err = db.Exec(`INSERT INTO rogerai.tower_outcomes VALUES ('tw-up','old','canary_fail',$1)`, base)
	require.NoError(t, err)

	s, err := NewPGStore(db)
	require.NoError(t, err, "the migration did not apply to an existing table")
	require.NoError(t, s.Record(Event{TowerID: "tw-up", StationID: "st-new",
		AttemptID: "new", Outcome: StationFault, At: base}))

	byStation, err := s.TallyByStation("tw-up", base)
	require.NoError(t, err)
	require.Equal(t, 1, byStation["st-new"].StationFault,
		"a row written after the upgrade is not attributed: %+v", byStation)
	require.Equal(t, 1, byStation[""].CanaryFail,
		"a row written BEFORE the upgrade must read back as naming no Station, not as an error")

	// Leave the table as the rest of this file expects to find it.
	_, err = db.Exec(`TRUNCATE rogerai.tower_outcomes`)
	require.NoError(t, err)
}
