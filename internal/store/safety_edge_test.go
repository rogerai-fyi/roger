package store

import (
	"testing"
)

// TestSafetyEmptyAccountGuardsParity locks the empty-accountID no-op guards on every
// owner-keyed safety primitive (a missing/anonymous id must never write a row), on BOTH
// backends.
func TestSafetyEmptyAccountGuardsParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			if n, err := db.OwnerStrike("", StrikeEmptyOutput, "{}", "k"); err != nil || n != 0 {
				t.Errorf("[%s] OwnerStrike(\"\") = %d,%v want 0,nil", name, n, err)
			}
			if err := db.BanOwner("", "x", "{}"); err != nil {
				t.Errorf("[%s] BanOwner(\"\") = %v want nil", name, err)
			}
			if banned, reason, err := db.IsOwnerBanned(""); err != nil || banned || reason != "" {
				t.Errorf("[%s] IsOwnerBanned(\"\") = %v,%q,%v want false,\"\",nil", name, banned, reason, err)
			}
			if err := db.SetAccountRecountHold("", true); err != nil {
				t.Errorf("[%s] SetAccountRecountHold(\"\") = %v want nil", name, err)
			}
			if n, err := db.ForgiveOwner(""); err != nil || n != 0 {
				t.Errorf("[%s] ForgiveOwner(\"\") = %d,%v want 0,nil", name, n, err)
			}
			// No banned owner was created by any of the empty-id calls.
			if bo, _ := db.BannedOwners(); len(bo) != 0 {
				t.Errorf("[%s] BannedOwners = %+v, want empty (empty-id guards wrote nothing)", name, bo)
			}
			_ = db.Close()
		})
	}
}

// TestForgiveOwnerScopedParity locks that ForgiveOwner deletes ONLY the target account's
// strikes/ban/hold and leaves an unrelated account fully intact (the non-matching "keep"
// branch), on BOTH backends.
func TestForgiveOwnerScopedParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			target, other := "pk-forgive", "pk-keep"
			_, _ = db.OwnerStrike(target, StrikeEmptyOutput, "{}", "t1")
			_, _ = db.OwnerStrike(target, StrikeRecountDiscrepancy, "{}", "t2")
			_ = db.BanOwner(target, "abuse", "{}")
			_ = db.SetAccountRecountHold(target, true)
			// An unrelated, innocent account that must survive the forgive.
			_, _ = db.OwnerStrike(other, StrikeEmptyOutput, "{}", "o1")
			_ = db.BanOwner(other, "separate", "{}")
			// Capture the other account's strike count BEFORE the forgive (Mem records a ban
			// marker as a strike, Postgres does not, so the absolute count differs per backend;
			// what must hold is that ForgiveOwner(target) leaves it UNCHANGED).
			otherBefore, _ := db.StrikesByOwner(other, 0)

			forgiven, err := db.ForgiveOwner(target)
			if err != nil {
				t.Fatal(err)
			}
			// At least the 2 real strikes are forgiven (Mem also counts the ban marker).
			if forgiven < 2 {
				t.Errorf("[%s] forgiven = %d, want >= 2 (target strikes)", name, forgiven)
			}
			// Target wiped.
			if rem, _ := db.StrikesByOwner(target, 0); len(rem) != 0 {
				t.Errorf("[%s] target strikes after forgive = %d, want 0", name, len(rem))
			}
			if banned, _, _ := db.IsOwnerBanned(target); banned {
				t.Errorf("[%s] target still banned after forgive", name)
			}
			// Other account fully intact (the keep branch ran): count unchanged.
			if rem, _ := db.StrikesByOwner(other, 0); len(rem) != len(otherBefore) {
				t.Errorf("[%s] other strikes = %d, want %d (untouched)", name, len(rem), len(otherBefore))
			}
			if banned, reason, _ := db.IsOwnerBanned(other); !banned || reason != "separate" {
				t.Errorf("[%s] other ban = %v/%q, want true/separate (untouched)", name, banned, reason)
			}
			_ = db.Close()
		})
	}
}

// TestSafetyLimitCapsParity locks the limit cap on every newest-first safety listing
// (limit>0 stops the scan early), on BOTH backends.
func TestSafetyLimitCapsParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			// Two queued CSAM incidents -> PendingCSAMReports(1) returns one.
			_, _ = db.PreserveCSAM(CSAMIncident{Pseudonym: "p1", Content: []byte("c1")})
			_, _ = db.PreserveCSAM(CSAMIncident{Pseudonym: "p2", Content: []byte("c2")})
			if got, _ := db.PendingCSAMReports(1); len(got) != 1 {
				t.Errorf("[%s] PendingCSAMReports(1) = %d, want 1", name, len(got))
			}

			// Two strikes -> StrikesByOwner(1) returns one.
			_, _ = db.OwnerStrike("pk-lim", StrikeEmptyOutput, "{}", "l1")
			_, _ = db.OwnerStrike("pk-lim", StrikeRecountDiscrepancy, "{}", "l2")
			if got, _ := db.StrikesByOwner("pk-lim", 1); len(got) != 1 {
				t.Errorf("[%s] StrikesByOwner(1) = %d, want 1", name, len(got))
			}

			// Two reports on one node -> ReportsByNode(1) returns one.
			_, _ = db.AddReport(Report{Category: "abuse", NodeID: "nlim", IP: "1.1.1.1"})
			_, _ = db.AddReport(Report{Category: "spam", NodeID: "nlim", IP: "2.2.2.2"})
			if got, _ := db.ReportsByNode("nlim", 1); len(got) != 1 {
				t.Errorf("[%s] ReportsByNode(1) = %d, want 1", name, len(got))
			}

			// Two appeals for one owner + a second owner's open appeal -> both listings cap at 1.
			_, _ = db.AddAppeal(Appeal{AccountID: "pk-lim", Reason: "a1"})
			_, _ = db.AddAppeal(Appeal{AccountID: "pk-lim", Reason: "a2"})
			_, _ = db.AddAppeal(Appeal{AccountID: "pk-other", Reason: "a3"})
			if got, _ := db.AppealsByOwner("pk-lim", 1); len(got) != 1 {
				t.Errorf("[%s] AppealsByOwner(1) = %d, want 1", name, len(got))
			}
			if got, _ := db.PendingAppeals(1); len(got) != 1 {
				t.Errorf("[%s] PendingAppeals(1) = %d, want 1", name, len(got))
			}
			_ = db.Close()
		})
	}
}

// TestSafetyMiscEdgeParity covers MarkCSAMReported on an unknown id (no-op, not an error)
// and the SetAccountRecountHold set->refresh->clear toggle, on BOTH backends.
func TestSafetyMiscEdgeParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			if err := db.MarkCSAMReported(999999); err != nil {
				t.Errorf("[%s] MarkCSAMReported(unknown) = %v, want nil (no-op)", name, err)
			}
			// Set, then refresh (re-flag), then clear: all must succeed.
			acct := "pk-hold"
			if err := db.SetAccountRecountHold(acct, true); err != nil {
				t.Fatal(err)
			}
			if err := db.SetAccountRecountHold(acct, true); err != nil { // refresh / ON CONFLICT update
				t.Fatal(err)
			}
			if err := db.SetAccountRecountHold(acct, false); err != nil { // clear
				t.Fatal(err)
			}
			_ = db.Close()
		})
	}
}
