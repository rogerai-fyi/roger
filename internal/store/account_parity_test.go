package store

import (
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestAccountManagementParity covers the owner/account surface on BOTH backends:
// bind + the two lookups (pubkey / login), email update, the one-time welcome claim,
// Stripe Connect linkage, banned-owner listing, and the GDPR-style delete (which
// anonymizes: the login stops resolving while the pubkey row survives, marked).
func TestAccountManagementParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			if err := db.BindOwner(Owner{Pubkey: "pk1", GitHubID: 1, Login: "alice", Name: "Alice", Email: "a@x.com"}); err != nil {
				t.Fatalf("BindOwner: %v", err)
			}

			o, ok, err := db.OwnerByPubkey("pk1")
			if err != nil || !ok || o.Login != "alice" {
				t.Fatalf("OwnerByPubkey = %+v ok=%v err=%v", o, ok, err)
			}
			if _, ok, _ := db.OwnerByPubkey("nope"); ok {
				t.Errorf("OwnerByPubkey(unknown) ok=true, want false")
			}
			if ol, ok, _ := db.OwnerByLogin("alice"); !ok || ol.Pubkey != "pk1" {
				t.Errorf("OwnerByLogin(alice) = %+v ok=%v, want pk1", ol, ok)
			}

			// Email update by login.
			if up, ok, _ := db.UpdateAccount("alice", "new@x.com"); !ok || up.Email != "new@x.com" {
				t.Errorf("UpdateAccount = %+v ok=%v, want email new@x.com", up, ok)
			}

			// One-time welcome claim: first wins, second is a no-op.
			if claimed, _ := db.ClaimWelcome("pk1"); !claimed {
				t.Errorf("first ClaimWelcome = false, want true")
			}
			if claimed, _ := db.ClaimWelcome("pk1"); claimed {
				t.Errorf("second ClaimWelcome = true, want false (already welcomed)")
			}

			// Stripe Connect linkage round-trips via the login lookup.
			if err := db.SetConnect("alice", "acct_123", "active"); err != nil {
				t.Fatalf("SetConnect: %v", err)
			}
			if ol, _, _ := db.OwnerByLogin("alice"); ol.ConnectID != "acct_123" || ol.ConnectStatus != "active" {
				t.Errorf("connect linkage = %q/%q, want acct_123/active", ol.ConnectID, ol.ConnectStatus)
			}

			// Banned-owner listing.
			_ = db.BanOwner("pk1", "fraud", "{}")
			if bans, _ := db.BannedOwners(); bans["pk1"] != "fraud" {
				t.Errorf("BannedOwners = %v, want pk1->fraud", bans)
			}

			// Delete anonymizes: login stops resolving; the pubkey row survives, marked.
			if ok, _ := db.DeleteAccount("alice"); !ok {
				t.Errorf("DeleteAccount(alice) = false, want true")
			}
			if _, ok, _ := db.OwnerByLogin("alice"); ok {
				t.Errorf("OwnerByLogin after delete = ok, want gone (anonymized)")
			}
			if o, ok, _ := db.OwnerByPubkey("pk1"); !ok || !o.Anonymized || o.Email != "" {
				t.Errorf("post-delete pubkey row = %+v ok=%v, want anonymized + email cleared", o, ok)
			}
			if ok, _ := db.DeleteAccount("alice"); ok {
				t.Errorf("second DeleteAccount = true, want false (already gone)")
			}
		})
	}
}

// TestMiscStoreParity covers the smaller money/idempotency helpers on BOTH backends:
// SpendOf, the recent-receipt feeds (by user / by node), the generic event-once guard
// (MarkProcessed), the credit-once webhook primitive (CreditOnce), and ReleaseHold.
func TestMiscStoreParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			user := "u-misc"
			_ = db.BindNode("n-misc", "acct-misc")
			if _, err := db.AddCredits(user, 100); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Settle(user, "n-misc", 7, 4, protocol.UsageReceipt{
				RequestID: "rq-misc", Model: "m", PromptTokens: 3, CompletionTokens: 5, TS: 10,
			}); err != nil {
				t.Fatal(err)
			}

			// SpendOf sums settled cost for the wallet.
			if s, _ := db.SpendOf(user); !approx(s, 7) {
				t.Errorf("SpendOf = %v, want 7", s)
			}
			// Recent feeds surface the receipt for the consumer and the node.
			if r, _ := db.RecentByUser(user, 10); len(r) != 1 || r[0].RequestID != "rq-misc" {
				t.Errorf("RecentByUser = %+v, want the one receipt", r)
			}
			if r, _ := db.RecentByNode("n-misc", 10); len(r) != 1 || r[0].Node != "n-misc" {
				t.Errorf("RecentByNode = %+v, want the one receipt", r)
			}

			// MarkProcessed: first time true, replay false.
			if first, _ := db.MarkProcessed("evt-1"); !first {
				t.Errorf("first MarkProcessed = false, want true")
			}
			if first, _ := db.MarkProcessed("evt-1"); first {
				t.Errorf("replayed MarkProcessed = true, want false")
			}

			// CreditOnce: credits exactly once per key (webhook idempotency).
			credited, bal1, _ := db.CreditOnce("topup-key", user, 25)
			if !credited {
				t.Errorf("first CreditOnce credited=false, want true")
			}
			credited2, bal2, _ := db.CreditOnce("topup-key", user, 25)
			if credited2 || !approx(bal1, bal2) {
				t.Errorf("replayed CreditOnce credited=%v bal %v->%v, want no double-credit", credited2, bal1, bal2)
			}

			// ReleaseHold returns the reserved funds to the wallet balance.
			before, _ := db.PeekBalance(user)
			if ok, err := db.Hold(user, 10); err != nil || !ok {
				t.Fatalf("Hold ok=%v err=%v", ok, err)
			}
			held, _ := db.PeekBalance(user)
			if !approx(held, before-10) {
				t.Errorf("post-hold balance = %v, want %v", held, before-10)
			}
			rb, err := db.ReleaseHold(user, 10)
			if err != nil || !approx(rb, before) {
				t.Errorf("ReleaseHold = %v (err %v), want %v restored", rb, err, before)
			}
		})
	}
}

// TestNodeBanExpiryParity covers the auto-expiry of report-threshold node bans on BOTH
// backends: a "report …" ban older than the cutoff is auto-lifted, while a permanent
// (manual/crypto) ban is never auto-lifted.
func TestNodeBanExpiryParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			if err := db.BanNode("n-report", "report threshold"); err != nil {
				t.Fatal(err)
			}
			if err := db.BanNode("n-manual", "manual abuse"); err != nil {
				t.Fatal(err)
			}
			cleared, err := db.ExpireNodeBans(time.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if len(cleared) != 1 || cleared[0] != "n-report" {
				t.Fatalf("ExpireNodeBans cleared = %v, want only n-report", cleared)
			}
			bans, _ := db.BannedNodes()
			if _, ok := bans["n-report"]; ok {
				t.Errorf("report-threshold ban should auto-lift")
			}
			if _, ok := bans["n-manual"]; !ok {
				t.Errorf("manual ban must survive auto-expiry")
			}
		})
	}
}
