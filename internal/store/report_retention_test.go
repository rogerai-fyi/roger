package store

import (
	"testing"
	"time"
)

// TestPurgeReportsHorizonsAndCSAMExemption locks the retention contract on rogerai.reports
// in BOTH directions, in both stores, because either direction alone is a passing test that
// proves nothing: a purge that deletes everything "passes" a vanish-only assertion, and a
// purge that deletes nothing "passes" a survive-only one.
//
// The three things it pins:
//
//   - an ordinary report past the ordinary horizon VANISHES (the table is bounded at all);
//   - a csam-category report past the ORDINARY horizon SURVIVES (POST /report is the only
//     writer of those rows and does not go through PreserveCSAM, so this table is the sole
//     copy of a child-safety tip - see csamReportRetention);
//   - a csam-category report past the CSAM horizon vanishes too (the exemption is a longer
//     horizon, not an exemption from retention - otherwise "category":"csam" on an
//     unauthenticated endpoint is a free pass around the whole sweep).
//
// Plus the boundary, which is where the two stores are most likely to disagree: created_at
// exactly AT the cutoff is deleted (Mem compares <=, so the SQL must too).
func TestPurgeReportsHorizonsAndCSAMExemption(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			day := 24 * time.Hour
			now := time.Now()
			at := func(age time.Duration) int64 { return now.Add(-age).Unix() }

			rows := []Report{
				{Category: "abuse", NodeID: "n", Detail: "fresh", IP: "1.1.1.1", CreatedAt: at(1 * day)},
				{Category: "abuse", NodeID: "n", Detail: "stale", IP: "2.2.2.2", CreatedAt: at(40 * day)},
				{Category: "abuse", NodeID: "n", Detail: "boundary", IP: "3.3.3.3", CreatedAt: at(30 * day)},
				{Category: ReportCategoryCSAM, NodeID: "n", Detail: "csam-stale", IP: "4.4.4.4", CreatedAt: at(40 * day)},
				{Category: ReportCategoryCSAM, NodeID: "n", Detail: "csam-ancient", IP: "5.5.5.5", CreatedAt: at(200 * day)},
			}
			for _, r := range rows {
				if _, err := db.AddReport(r); err != nil {
					t.Fatal(err)
				}
			}
			// Guard the whole test against the commonest way a retention test lies: assert the
			// rows are actually THERE before anything is purged.
			if got, _ := db.ReportsByNode("n", 0); len(got) != len(rows) {
				t.Fatalf("setup: %d rows written, want %d", len(got), len(rows))
			}

			present := func(t *testing.T, want ...string) {
				t.Helper()
				got, err := db.ReportsByNode("n", 0)
				if err != nil {
					t.Fatal(err)
				}
				have := map[string]bool{}
				for _, r := range got {
					have[r.Detail] = true
				}
				for _, w := range want {
					if !have[w] {
						t.Errorf("report %q should have SURVIVED, it is gone", w)
					}
					delete(have, w)
				}
				for leftover := range have {
					t.Errorf("report %q should have been PURGED, it is still here", leftover)
				}
			}

			// Ordinary horizon 30 days, csam horizon 100 days.
			n, err := db.PurgeReports(now.Add(-30*day), now.Add(-100*day))
			if err != nil {
				t.Fatal(err)
			}
			// "stale" and "boundary" are ordinary and at/past 30d; "csam-ancient" is past 100d.
			if n != 3 {
				t.Errorf("PurgeReports removed %d rows, want 3", n)
			}
			present(t, "fresh", "csam-stale")

			// Idempotent: a second identical sweep removes nothing and touches nothing.
			if n, err := db.PurgeReports(now.Add(-30*day), now.Add(-100*day)); err != nil || n != 0 {
				t.Errorf("second PurgeReports removed %d rows (err %v), want 0", n, err)
			}
			present(t, "fresh", "csam-stale")

			// Collapse both horizons: the csam row is now past ITS horizon and goes too, which
			// is what makes the exemption a longer window rather than an escape from the sweep.
			if n, err := db.PurgeReports(now.Add(-30*day), now.Add(-30*day)); err != nil || n != 1 {
				t.Errorf("uniform PurgeReports removed %d rows (err %v), want 1", n, err)
			}
			present(t, "fresh")
			_ = db.Close()
		})
	}
}
