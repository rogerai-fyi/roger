package store

import (
	"os"
	"testing"
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
			_ = db.Close()
		})
	}
}
