package store

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestTrackedHoldSweepParity drives the deploy-orphan backstop on BOTH backends through the
// Store interface: HoldFor records a TRACKED reservation (same wallet debit as Hold), the
// ReleaseStaleHolds sweep reclaims a hold older than the cutoff returning the EXACT held
// amount, a within-window hold is never reclaimed, the sweep is idempotent on a re-run, a
// captured (Finalize) hold and a deferred-released (ReleaseHoldFor) hold are cleared so a
// later sweep never double-refunds them.
func TestTrackedHoldSweepParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			// scenario 2: a stranded hold is reclaimed, exact amount restored.
			if _, err := db.AddCredits("alice", 10); err != nil {
				t.Fatal(err)
			}
			t0 := time.Now()
			if ok, err := db.HoldFor("alice", "req-1", 4); err != nil || !ok {
				t.Fatalf("HoldFor ok=%v err=%v", ok, err)
			}
			if bal, _ := db.BalanceOf("alice", 0); !approx(bal, 6) {
				t.Fatalf("after HoldFor balance=%v, want 6", bal)
			}
			// scenario 3: a hold inside its window is NEVER released.
			if n, err := db.ReleaseStaleHolds(t0.Add(-time.Hour)); err != nil || n != 0 {
				t.Fatalf("within-window sweep released n=%d err=%v, want 0", n, err)
			}
			if bal, _ := db.BalanceOf("alice", 0); !approx(bal, 6) {
				t.Fatalf("after within-window sweep balance=%v, want 6 (untouched)", bal)
			}
			// scenario 2 cont.: past the TTL it is reclaimed, exact amount.
			if n, err := db.ReleaseStaleHolds(t0.Add(time.Hour)); err != nil || n != 1 {
				t.Fatalf("stale sweep released n=%d err=%v, want 1", n, err)
			}
			if bal, _ := db.BalanceOf("alice", 0); !approx(bal, 10) {
				t.Fatalf("after stale sweep balance=%v, want 10 (exact restore)", bal)
			}
			// idempotent re-run: nothing left to release.
			if n, err := db.ReleaseStaleHolds(t0.Add(time.Hour)); err != nil || n != 0 {
				t.Fatalf("re-sweep released n=%d err=%v, want 0 (idempotent)", n, err)
			}
			if bal, _ := db.BalanceOf("alice", 0); !approx(bal, 10) {
				t.Fatalf("after re-sweep balance=%v, want 10 (no double-refund)", bal)
			}
		})
	}
}

// TestTrackedHoldClearedByFinalizeParity: a captured hold is cleared, so a later sweep does
// not reclaim it (no double-refund), and the money math is the normal Finalize result.
func TestTrackedHoldClearedByFinalizeParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			_ = db.BindNode("n1", "op1")
			if _, err := db.AddCredits("alice", 10); err != nil {
				t.Fatal(err)
			}
			if ok, err := db.HoldFor("alice", "req-1", 5); err != nil || !ok {
				t.Fatalf("HoldFor ok=%v err=%v", ok, err)
			}
			rec := protocol.UsageReceipt{RequestID: "req-1", TS: 1}
			if _, err := db.Finalize("alice", "n1", 5, 2, 1.4, rec); err != nil {
				t.Fatalf("Finalize: %v", err)
			}
			if bal, _ := db.BalanceOf("alice", 0); !approx(bal, 8) {
				t.Fatalf("after Finalize balance=%v, want 8", bal)
			}
			if e, _ := db.EarningsOf("n1"); !approx(e, 1.4) {
				t.Fatalf("earnings=%v, want 1.4", e)
			}
			// the captured hold must NOT be reclaimable - the row was cleared.
			if n, err := db.ReleaseStaleHolds(time.Now().Add(time.Hour)); err != nil || n != 0 {
				t.Fatalf("post-capture sweep released n=%d err=%v, want 0 (no double-refund)", n, err)
			}
			if bal, _ := db.BalanceOf("alice", 0); !approx(bal, 8) {
				t.Fatalf("post-capture balance=%v, want 8 (unchanged)", bal)
			}
		})
	}
}

// TestReleaseHoldForParity: the deferred relay release refunds + clears the tracked hold
// idempotently - a second call (or a sweep after it) is a no-op, never a double-refund.
func TestReleaseHoldForParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			if _, err := db.AddCredits("alice", 10); err != nil {
				t.Fatal(err)
			}
			if ok, err := db.HoldFor("alice", "req-1", 5); err != nil || !ok {
				t.Fatalf("HoldFor ok=%v err=%v", ok, err)
			}
			if bal, err := db.ReleaseHoldFor("alice", "req-1"); err != nil || !approx(bal, 10) {
				t.Fatalf("ReleaseHoldFor bal=%v err=%v, want 10", bal, err)
			}
			// idempotent: a second deferred release is a no-op (the row is gone).
			if bal, err := db.ReleaseHoldFor("alice", "req-1"); err != nil || !approx(bal, 10) {
				t.Fatalf("2nd ReleaseHoldFor bal=%v err=%v, want 10 (no double-refund)", bal, err)
			}
			// and a later sweep finds nothing.
			if n, err := db.ReleaseStaleHolds(time.Now().Add(time.Hour)); err != nil || n != 0 {
				t.Fatalf("post-release sweep released n=%d err=%v, want 0", n, err)
			}
			if bal, _ := db.BalanceOf("alice", 0); !approx(bal, 10) {
				t.Fatalf("final balance=%v, want 10", bal)
			}
		})
	}
}

// TestReleaseStaleHoldsBoundaryMem pins the cutoff inclusivity (placed_at <= cutoff is
// stale; strictly after is live) with an exact, white-box placed_at.
func TestReleaseStaleHoldsBoundaryMem(t *testing.T) {
	m := NewMem()
	if _, err := m.AddCredits("alice", 100); err != nil {
		t.Fatal(err)
	}
	if ok, err := m.HoldFor("alice", "r", 10); err != nil || !ok {
		t.Fatalf("HoldFor ok=%v err=%v", ok, err)
	}
	at := time.Unix(1_000_000, 0)
	m.mu.Lock()
	ph := m.pendingHolds["r"]
	ph.placedAt = at.Unix()
	m.pendingHolds["r"] = ph
	m.mu.Unlock()

	// cutoff one second BEFORE placed_at: live, not released.
	if n, _ := m.ReleaseStaleHolds(at.Add(-time.Second)); n != 0 {
		t.Fatalf("cutoff < placed_at released %d, want 0", n)
	}
	// cutoff EXACTLY at placed_at: at-or-before -> released.
	if n, _ := m.ReleaseStaleHolds(at); n != 1 {
		t.Fatalf("cutoff == placed_at released %d, want 1 (inclusive)", n)
	}
	if bal, _ := m.BalanceOf("alice", 0); !approx(bal, 100) {
		t.Fatalf("balance=%v, want 100", bal)
	}
}

// TestReleaseStaleHoldsCrossInstancePostgres is the scenario-4 money path: two broker
// instances (two independent *Postgres handles to the SAME database) run the sweep
// concurrently. The atomic delete-and-credit claim must release the stranded hold EXACTLY
// once - no double-release, no wallet drift. Real Postgres only (no mocks).
func TestReleaseStaleHoldsCrossInstancePostgres(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("cross-instance sweep needs ROGERAI_TEST_DATABASE_URL (a real Postgres) - skipping")
	}
	inst1 := freshPostgres(t, dsn)                     // truncates, then is instance #1
	inst2, err := NewPostgres(storePrivateDSN(t, dsn)) // instance #2, SAME (private) database
	if err != nil {
		t.Fatalf("second instance: %v", err)
	}
	if _, err := inst1.AddCredits("alice", 10); err != nil {
		t.Fatal(err)
	}
	if ok, err := inst1.HoldFor("alice", "req-1", 7); err != nil || !ok {
		t.Fatalf("HoldFor ok=%v err=%v", ok, err)
	}
	cutoff := time.Now().Add(time.Hour)
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for _, inst := range []*Postgres{inst1, inst2} {
		wg.Add(1)
		go func(p *Postgres) {
			defer wg.Done()
			n, err := p.ReleaseStaleHolds(cutoff)
			if err != nil {
				t.Errorf("ReleaseStaleHolds: %v", err)
			}
			mu.Lock()
			total += n
			mu.Unlock()
		}(inst)
	}
	wg.Wait()
	if total != 1 {
		t.Fatalf("two instances released %d holds in total, want exactly 1 (double-release/drift)", total)
	}
	if bal, _ := inst1.BalanceOf("alice", 0); !approx(bal, 10) {
		t.Fatalf("balance=%v, want 10 (restored exactly once)", bal)
	}
}
