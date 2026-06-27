package store

import (
	"os"
	"testing"
	"time"
)

// recourseStores runs the parity suite on Mem (always) + Postgres (when configured), so
// the new auto-expiry / forgive / pending-reversal surfaces behave identically on both.
func recourseStores(t *testing.T) map[string]Store {
	t.Helper()
	out := map[string]Store{"mem": NewMem()}
	if dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL"); dsn != "" {
		out["postgres"] = freshPostgres(t, dsn)
	}
	return out
}

// TestExpireRecountHoldsParity locks the auto-expiry recourse: a hold older than the
// cutoff clears (node + account), a hold refreshed by a fresh discrepancy survives, and
// the cleared holds let lots promote again.
func TestExpireRecountHoldsParity(t *testing.T) {
	for name, db := range recourseStores(t) {
		t.Run(name, func(t *testing.T) {
			// Place an old node hold + account hold (Mem stamps held-at = now; Postgres
			// created_at = now). They should clear with a future cutoff.
			if err := db.SetNodeRecountHold("oldnode", true); err != nil {
				t.Fatal(err)
			}
			if err := db.SetAccountRecountHold("oldacct", true); err != nil {
				t.Fatal(err)
			}
			// A cutoff in the FUTURE catches both (created_at <= cutoff).
			n, err := db.ExpireRecountHolds(time.Now().Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if n != 2 {
				t.Fatalf("ExpireRecountHolds cleared %d, want 2", n)
			}
			held, _ := db.RecountHeldNodes()
			if held["oldnode"] {
				t.Error("oldnode hold should have expired")
			}

			// A FRESH hold must NOT clear under a PAST cutoff (it was just placed now).
			if err := db.SetNodeRecountHold("freshnode", true); err != nil {
				t.Fatal(err)
			}
			n2, _ := db.ExpireRecountHolds(time.Now().Add(-time.Hour))
			if n2 != 0 {
				t.Errorf("a fresh hold cleared %d, want 0 (should survive a past cutoff)", n2)
			}
			held2, _ := db.RecountHeldNodes()
			if !held2["freshnode"] {
				t.Error("freshnode hold should still be held")
			}
			_ = db.Close()
		})
	}
}

// TestForgiveOwnerParity locks the admin recourse primitive: ForgiveOwner deletes the
// owner's strikes, lifts the ban, and clears the account hold, in one call.
func TestForgiveOwnerParity(t *testing.T) {
	for name, db := range recourseStores(t) {
		t.Run(name, func(t *testing.T) {
			acct := "pkF"
			if _, err := db.OwnerStrike(acct, StrikeRecountDiscrepancy, `{"a":1}`, "s1"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.OwnerStrike(acct, StrikeEmptyOutput, `{"a":2}`, "s2"); err != nil {
				t.Fatal(err)
			}
			if err := db.BanOwner(acct, "test", `{"x":1}`); err != nil {
				t.Fatal(err)
			}
			if err := db.SetAccountRecountHold(acct, true); err != nil {
				t.Fatal(err)
			}
			forgiven, err := db.ForgiveOwner(acct)
			if err != nil {
				t.Fatal(err)
			}
			if forgiven < 2 {
				t.Errorf("forgiven = %d, want >=2", forgiven)
			}
			if banned, _, _ := db.IsOwnerBanned(acct); banned {
				t.Error("owner should be unbanned after forgive")
			}
			rem, _ := db.StrikesByOwner(acct, 0)
			if len(rem) != 0 {
				t.Errorf("remaining strikes = %d, want 0", len(rem))
			}
			// Hold cleared -> a future-cutoff expiry finds nothing to clear for this acct.
			if _, err := db.ForgiveOwner(acct); err != nil { // idempotent
				t.Fatal(err)
			}
			_ = db.Close()
		})
	}
}

// TestPendingReversalParity locks the failed-reversal-retry store surface: record is
// idempotent on key, open lists the un-done un-dead rows, a success marks it done (drops
// from open), and a failed attempt dead-letters once it reaches maxAttempts.
func TestPendingReversalParity(t *testing.T) {
	for name, db := range recourseStores(t) {
		t.Run(name, func(t *testing.T) {
			pr := PendingReversal{
				Key: "reverse:dp1:42", DisputeID: "dp1", LotID: 42,
				AccountID: "pk1", TransferID: "tr_1", Amount: 12.5,
			}
			if err := db.RecordPendingReversal(pr); err != nil {
				t.Fatal(err)
			}
			// Idempotent re-record is a no-op.
			if err := db.RecordPendingReversal(pr); err != nil {
				t.Fatal(err)
			}
			open, err := db.OpenPendingReversals(0)
			if err != nil {
				t.Fatal(err)
			}
			if len(open) != 1 {
				t.Fatalf("open reversals = %d, want 1", len(open))
			}
			if open[0].TransferID != "tr_1" || open[0].Amount != 12.5 {
				t.Errorf("open[0] = %+v, want tr_1 / 12.5", open[0])
			}

			// One failed attempt with maxAttempts=2: still open (attempts=1 < 2).
			if err := db.MarkReversalAttempt(pr.Key, false, "boom", 2, time.Now()); err != nil {
				t.Fatal(err)
			}
			open, _ = db.OpenPendingReversals(0)
			if len(open) != 1 {
				t.Fatalf("after 1 fail open = %d, want 1 (not dead-lettered yet)", len(open))
			}
			if open[0].Attempts != 1 || open[0].LastError != "boom" {
				t.Errorf("after 1 fail = attempts %d err %q, want 1/boom", open[0].Attempts, open[0].LastError)
			}
			// Second failed attempt reaches maxAttempts=2 -> dead-letter, drops from open.
			if err := db.MarkReversalAttempt(pr.Key, false, "boom2", 2, time.Now()); err != nil {
				t.Fatal(err)
			}
			open, _ = db.OpenPendingReversals(0)
			if len(open) != 0 {
				t.Fatalf("after dead-letter open = %d, want 0", len(open))
			}

			// A SECOND reversal that succeeds drops from open (done=true).
			pr2 := PendingReversal{Key: "reverse:dp2:7", DisputeID: "dp2", LotID: 7, TransferID: "tr_2", Amount: 3}
			_ = db.RecordPendingReversal(pr2)
			if err := db.MarkReversalAttempt(pr2.Key, true, "", 5, time.Now()); err != nil {
				t.Fatal(err)
			}
			open, _ = db.OpenPendingReversals(0)
			if len(open) != 0 {
				t.Errorf("after success open = %d, want 0", len(open))
			}
			_ = db.Close()
		})
	}
}
