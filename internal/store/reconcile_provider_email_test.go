package store

// reconcile_provider_email_test.go - ReconcileProviderEmail on both stores (founder ruling
// 2026-10-04, audit fix 2026-10-05). A provider sign-in records the address that provider
// reports; a stored address that was not proven by code is mailable when it matches the
// last report of ANY provider the row is linked to, unproven when every linked provider has
// reported and none matches, and left as it was while a linked provider has not reported
// yet. An Apple "Hide My Email" relay address therefore never withdraws the address GitHub
// reports for the same account.

import (
	"testing"
	"time"
)

func rpeOwner(t *testing.T, db Store, o Owner) {
	t.Helper()
	if err := db.BindOwner(o); err != nil {
		t.Fatalf("bind owner: %v", err)
	}
}

func rpeUnproven(t *testing.T, db Store, pub string) bool {
	t.Helper()
	o, ok, err := db.OwnerByPubkey(pub)
	if err != nil || !ok {
		t.Fatalf("owner %s: ok=%v err=%v", pub, ok, err)
	}
	return o.EmailUnproven
}

func TestReconcileProviderEmail(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			// Dual-linked row: GitHub reports the stored address, Apple a relay address.
			rpeOwner(t, db, Owner{Pubkey: "dual1", GitHubID: 9101, Login: "dual1", AppleSub: "as-dual1", Email: "gh@x.com"})
			if err := db.ReconcileProviderEmail(9101, "", "gh@x.com"); err != nil {
				t.Fatal(err)
			}
			if err := db.ReconcileProviderEmail(0, "as-dual1", "relay@privaterelay.appleid.com"); err != nil {
				t.Fatal(err)
			}
			if rpeUnproven(t, db, "dual1") {
				t.Error("dual-linked: an Apple relay address withdrew the address GitHub reports")
			}

			// Dual-linked, only Apple has reported (GitHub unknown): left as it was.
			rpeOwner(t, db, Owner{Pubkey: "dual2", GitHubID: 9102, Login: "dual2", AppleSub: "as-dual2", Email: "gh2@x.com"})
			if err := db.ReconcileProviderEmail(0, "as-dual2", "relay2@privaterelay.appleid.com"); err != nil {
				t.Fatal(err)
			}
			if rpeUnproven(t, db, "dual2") {
				t.Error("dual-linked: Apple alone withdrew an address GitHub has not re-reported")
			}

			// Dual-linked, both reported, neither matches: unproven.
			rpeOwner(t, db, Owner{Pubkey: "dual3", GitHubID: 9103, Login: "dual3", AppleSub: "as-dual3", Email: "typed@x.com"})
			_ = db.ReconcileProviderEmail(9103, "", "gh3@x.com")
			_ = db.ReconcileProviderEmail(0, "as-dual3", "relay3@privaterelay.appleid.com")
			if !rpeUnproven(t, db, "dual3") {
				t.Error("dual-linked: an address matching neither provider stayed mailable")
			}
			// ...and becomes mailable again when a provider reports it (any case).
			_ = db.ReconcileProviderEmail(9103, "", "TYPED@x.com")
			if rpeUnproven(t, db, "dual3") {
				t.Error("dual-linked: a matching GitHub report did not make the address mailable again")
			}

			// Single-linked GitHub row: mismatch is unproven, match is mailable.
			rpeOwner(t, db, Owner{Pubkey: "gh1", GitHubID: 9104, Login: "gh1", Email: "old@x.com"})
			_ = db.ReconcileProviderEmail(9104, "", "new@x.com")
			if !rpeUnproven(t, db, "gh1") {
				t.Error("GitHub-only: a mismatched address stayed mailable")
			}
			_ = db.ReconcileProviderEmail(9104, "", "OLD@x.com")
			if rpeUnproven(t, db, "gh1") {
				t.Error("GitHub-only: a matching report did not make the address mailable")
			}

			// An empty report changes nothing.
			rpeOwner(t, db, Owner{Pubkey: "ap1", AppleSub: "as-ap1", Email: "a@x.com"})
			_ = db.ReconcileProviderEmail(0, "as-ap1", "")
			if rpeUnproven(t, db, "ap1") {
				t.Error("an empty report changed the address")
			}

			// A code-proven address is never touched.
			rpeOwner(t, db, Owner{Pubkey: "pv1", GitHubID: 9105, Login: "pv1", Email: "proven@x.com", EmailVerifiedAt: time.Now().Unix()})
			_ = db.ReconcileProviderEmail(9105, "", "other@x.com")
			if rpeUnproven(t, db, "pv1") {
				t.Error("a code-proven address was withdrawn by a provider sign-in")
			}
		})
	}
}

// A report is recorded even on a code-proven row, so if the proof is later withdrawn (the
// account types a new address) the row is judged against what the providers last reported,
// identically on both stores.
func TestReconcileProviderEmailRecordsOnProvenRows(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			rpeOwner(t, db, Owner{Pubkey: "pv2", GitHubID: 9201, Login: "pv2", Email: "proven@x.com", EmailVerifiedAt: time.Now().Unix()})
			_ = db.ReconcileProviderEmail(9201, "", "gh@x.com")
			if _, ok, err := db.UpdateAccount("pv2", "gh@x.com"); err != nil || !ok {
				t.Fatalf("update account: ok=%v err=%v", ok, err)
			}
			// A typed address is unproven until a provider reports it again...
			if !rpeUnproven(t, db, "pv2") {
				t.Fatal("setup: a typed address must start unproven")
			}
			// ...and the next GitHub report of the same address makes it mailable.
			_ = db.ReconcileProviderEmail(9201, "", "gh@x.com")
			if rpeUnproven(t, db, "pv2") {
				t.Error("a typed address matching the GitHub report stayed unproven")
			}
		})
	}
}

// reportedEmails reads the two provider-reported addresses straight from the store: the
// Postgres columns (they are not loaded on Owner reads) or the in-memory row.
func reportedEmails(t *testing.T, db Store, pub string) (gh, apple string) {
	t.Helper()
	switch s := db.(type) {
	case *Postgres:
		var g, a *string
		if err := s.db.QueryRow(`SELECT github_reported_email, apple_reported_email FROM rogerai.owners WHERE pubkey=$1`, pub).Scan(&g, &a); err != nil {
			t.Fatalf("read reported emails: %v", err)
		}
		if g != nil {
			gh = *g
		}
		if a != nil {
			apple = *a
		}
	case *Mem:
		s.mu.Lock()
		defer s.mu.Unlock()
		o := s.owners[pub]
		gh, apple = o.GitHubReportedEmail, o.AppleReportedEmail
	default:
		t.Fatalf("unknown store %T", db)
	}
	return gh, apple
}

// TestDeleteAccountClearsReportedEmails: account deletion anonymizes the row, and the
// provider-reported addresses are personal data too, so they go with the rest.
func TestDeleteAccountClearsReportedEmails(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			rpeOwner(t, db, Owner{Pubkey: "gone1", GitHubID: 9201, Login: "gone1", AppleSub: "as-gone1", Email: "gh@gone.com"})
			if err := db.ReconcileProviderEmail(9201, "", "gh@gone.com"); err != nil {
				t.Fatal(err)
			}
			if err := db.ReconcileProviderEmail(0, "as-gone1", "relay@privaterelay.appleid.com"); err != nil {
				t.Fatal(err)
			}
			if gh, ap := reportedEmails(t, db, "gone1"); gh == "" || ap == "" {
				t.Fatalf("fixture: reported emails not recorded (gh=%q apple=%q)", gh, ap)
			}
			if ok, err := db.DeleteAccount("gone1"); err != nil || !ok {
				t.Fatalf("delete account: ok=%v err=%v", ok, err)
			}
			if gh, ap := reportedEmails(t, db, "gone1"); gh != "" || ap != "" {
				t.Errorf("a deleted account kept provider-reported emails: github=%q apple=%q", gh, ap)
			}
		})
	}
}

// Founder ruling 2026-10-05 (any verified address): a GitHub sign-in passes every address
// GitHub lists as verified. A row whose stored address matches one of them records that
// address as GitHub's report and stays mailable; any other row records the primary. Apple's
// report on the same row is never touched by a GitHub sign-in.
func TestReconcileProviderEmailAnyVerified(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			rpeOwner(t, db, Owner{Pubkey: "av1", GitHubID: 9301, Login: "av1", AppleSub: "as-av1", Email: "second@x.com"})
			rpeOwner(t, db, Owner{Pubkey: "av2", GitHubID: 9302, Login: "av2", Email: "typed@x.com"})
			if err := db.ReconcileProviderEmail(0, "as-av1", "relay@privaterelay.appleid.com"); err != nil {
				t.Fatal(err)
			}
			if err := db.ReconcileProviderEmail(9301, "", "real@x.com", "real@x.com", "SECOND@x.com"); err != nil {
				t.Fatal(err)
			}
			if rpeUnproven(t, db, "av1") {
				t.Error("a stored address GitHub verified as a secondary address was withdrawn")
			}
			if err := db.ReconcileProviderEmail(9302, "", "real@x.com", "real@x.com", "other@x.com"); err != nil {
				t.Fatal(err)
			}
			if !rpeUnproven(t, db, "av2") {
				t.Error("a stored address matching no verified address stayed mailable")
			}
			if gh, ap := reportedEmails(t, db, "av1"); gh != "SECOND@x.com" || ap != "relay@privaterelay.appleid.com" {
				t.Errorf("reports after a GitHub sign-in: github=%q (want the matching verified address), apple=%q (want untouched)", gh, ap)
			}
		})
	}
}
