package store

import (
	"os"
	"testing"
	"time"
)

// safetyStores returns the store backends to run the parity suite against: always Mem,
// plus Postgres when ROGERAI_TEST_DATABASE_URL is set (CI/local with a real DB). This
// guarantees the new csam_incidents / reports / banned_nodes tables behave identically
// on both backends.
func safetyStores(t *testing.T) map[string]Store {
	t.Helper()
	out := map[string]Store{"mem": NewMem()}
	if dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL"); dsn != "" {
		pg, err := NewPostgres(dsn)
		if err != nil {
			t.Fatalf("postgres: %v", err)
		}
		out["postgres"] = pg
	}
	return out
}

func TestSafetyStoreParity(t *testing.T) {
	for name, db := range safetyStores(t) {
		t.Run(name, func(t *testing.T) {
			// --- CSAM preserve + queue + mark reported ---
			id, err := db.PreserveCSAM(CSAMIncident{
				Pseudonym: "u_x", IP: "1.1.1.1", Category: "S4",
				Content: []byte("ciphertext-bytes"),
			})
			if err != nil {
				t.Fatal(err)
			}
			if id == 0 {
				t.Error("PreserveCSAM should return a non-zero id")
			}
			pending, err := db.PendingCSAMReports(0)
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) != 1 {
				t.Fatalf("want 1 queued incident, got %d", len(pending))
			}
			if pending[0].ReportState != CSAMQueued {
				t.Errorf("incident should default to queued, got %q", pending[0].ReportState)
			}
			if string(pending[0].Content) != "ciphertext-bytes" {
				t.Errorf("content round-trip mismatch: %q", pending[0].Content)
			}
			if err := db.MarkCSAMReported(pending[0].ID); err != nil {
				t.Fatal(err)
			}
			pending2, _ := db.PendingCSAMReports(0)
			if len(pending2) != 0 {
				t.Errorf("a reported incident must drain from the queue, got %d", len(pending2))
			}

			// --- reports + per-node count ---
			if _, err := db.AddReport(Report{Category: "abuse", NodeID: "n1", IP: "2.2.2.2"}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.AddReport(Report{Category: "spam", NodeID: "n1"}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.AddReport(Report{Category: "quality", NodeID: "n2"}); err != nil {
				t.Fatal(err)
			}
			if n, _ := db.ReportCountByNode("n1"); n != 2 {
				t.Errorf("n1 count = %d, want 2", n)
			}
			if n, _ := db.ReportCountByNode("n2"); n != 1 {
				t.Errorf("n2 count = %d, want 1", n)
			}
			reps, _ := db.ReportsByNode("n1", 0)
			if len(reps) != 2 {
				t.Errorf("ReportsByNode(n1) = %d, want 2", len(reps))
			}

			// --- ban set ---
			if err := db.BanNode("n1", "report threshold"); err != nil {
				t.Fatal(err)
			}
			if err := db.BanNode("n1", "again"); err != nil { // idempotent
				t.Fatal(err)
			}
			bans, err := db.BannedNodes()
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := bans["n1"]; !ok {
				t.Error("n1 should be banned")
			}
			if _, ok := bans["n2"]; ok {
				t.Error("n2 should NOT be banned")
			}
			// --- node-ban recovery: UnbanNode lifts it ---
			if err := db.UnbanNode("n1"); err != nil {
				t.Fatal(err)
			}
			if bans, _ := db.BannedNodes(); bans["n1"] != "" {
				t.Error("UnbanNode must lift the ban")
			}
			_ = db.Close()
		})
	}
}

// TestDistinctReporterCount locks the H2 dedup count: distinct reporter IPs within a
// window, NOT a raw all-time COUNT(*). One IP counts once; reports with no IP and reports
// older than the window do not count.
func TestDistinctReporterCount(t *testing.T) {
	for name, db := range safetyStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now().Unix()
			// Same IP three times -> counts ONCE.
			for i := 0; i < 3; i++ {
				_, _ = db.AddReport(Report{Category: "abuse", NodeID: "v", IP: "1.1.1.1", CreatedAt: now})
			}
			// Two more distinct IPs.
			_, _ = db.AddReport(Report{Category: "abuse", NodeID: "v", IP: "2.2.2.2", CreatedAt: now})
			_, _ = db.AddReport(Report{Category: "abuse", NodeID: "v", IP: "3.3.3.3", CreatedAt: now})
			// A report with NO reporter IP must not corroborate.
			_, _ = db.AddReport(Report{Category: "abuse", NodeID: "v", CreatedAt: now})
			// A stale report (well before the window) must age out.
			_, _ = db.AddReport(Report{Category: "abuse", NodeID: "v", IP: "9.9.9.9", CreatedAt: now - 100_000})

			if n, err := db.DistinctReporterCountByNode("v", now-1000); err != nil || n != 3 {
				t.Fatalf("distinct reporters in window = %d (err %v), want 3", n, err)
			}
			_ = db.Close()
		})
	}
}

// TestOwnerStrikeStatsDecayAndKinds locks the decay window + corroboration inputs:
// OwnerStrikeStats counts only strikes at/after `since` and across how many distinct
// kinds, excluding terminal ban markers.
func TestOwnerStrikeStatsDecayAndKinds(t *testing.T) {
	for name, db := range safetyStores(t) {
		t.Run(name, func(t *testing.T) {
			acct := "pk_decay"
			_, _ = db.OwnerStrike(acct, StrikeEmptyOutput, "{}", "k1")
			_, _ = db.OwnerStrike(acct, StrikeRecountDiscrepancy, "{}", "k2")
			// All strikes counted (since=0): 2 strikes across 2 distinct kinds.
			if w, k, _ := db.OwnerStrikeStats(acct, 0); w != 2 || k != 2 {
				t.Fatalf("stats(all) = %d/%d, want 2/2", w, k)
			}
			// Decay: a future cutoff excludes every existing strike.
			future := time.Now().Add(time.Hour).Unix()
			if w, k, _ := db.OwnerStrikeStats(acct, future); w != 0 || k != 0 {
				t.Fatalf("stats(future cutoff) = %d/%d, want 0/0 (decayed)", w, k)
			}
			// A ban marker strike (Mem) must NOT inflate the distinct-kind count.
			_ = db.BanOwner(acct, "x", "{}")
			if w, k, _ := db.OwnerStrikeStats(acct, 0); w != 2 || k != 2 {
				t.Fatalf("stats after ban = %d/%d, want 2/2 (ban marker excluded)", w, k)
			}
			_ = db.Close()
		})
	}
}

// TestAppealStoreParity locks the self-serve appeal storage: owner-scoped listing + the
// admin pending queue, identical on Mem and Postgres.
func TestAppealStoreParity(t *testing.T) {
	for name, db := range safetyStores(t) {
		t.Run(name, func(t *testing.T) {
			id, err := db.AddAppeal(Appeal{AccountID: "pk_a", NodeID: "n1", Reason: "false positive"})
			if err != nil || id == 0 {
				t.Fatalf("AddAppeal = %d, %v", id, err)
			}
			_, _ = db.AddAppeal(Appeal{AccountID: "pk_b", Reason: "other account"})
			// Owner-scoped: pk_a sees only its own appeal.
			as, _ := db.AppealsByOwner("pk_a", 0)
			if len(as) != 1 || as[0].NodeID != "n1" || as[0].State != AppealOpen {
				t.Fatalf("AppealsByOwner(pk_a) = %+v, want one open appeal for n1", as)
			}
			if other, _ := db.AppealsByOwner("pk_a", 0); len(other) == 1 && other[0].AccountID != "pk_a" {
				t.Fatal("appeal listing leaked another account")
			}
			// Admin queue: both open appeals are pending.
			if pend, _ := db.PendingAppeals(0); len(pend) != 2 {
				t.Fatalf("PendingAppeals = %d, want 2", len(pend))
			}
			_ = db.Close()
		})
	}
}
