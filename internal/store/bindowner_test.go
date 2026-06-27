package store

import (
	"testing"
)

// TestBindOwnerReloginParity locks the re-login merge semantics of BindOwner on BOTH
// backends: a second bind for the same pubkey (a GitHub re-login) updates github_id +
// login, but NEVER clobbers a user-set email or a captured display name, preserves the
// original bind time, and carries the durable account-hub state (welcome, Connect) that a
// fresh GitHub login does not supply. A separate account whose email/name were EMPTY gets
// them filled from GitHub on the next login.
func TestBindOwnerReloginParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			// Account 1: user-set email + name, plus durable hub state.
			if err := db.BindOwner(Owner{Pubkey: "pk-relogin", GitHubID: 10, Login: "old-login", Name: "Set Name", Email: "user@set.com", CreatedAt: 12345}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ClaimWelcome("pk-relogin"); err != nil {
				t.Fatal(err)
			}
			if err := db.SetConnect("old-login", "acct_keep", "active"); err != nil {
				t.Fatal(err)
			}
			before, _, _ := db.OwnerByPubkey("pk-relogin")

			// Re-login: GitHub hands a new login + a different email/name. The user-set values win.
			if err := db.BindOwner(Owner{Pubkey: "pk-relogin", GitHubID: 10, Login: "new-login", Name: "GH Name", Email: "gh@github.com"}); err != nil {
				t.Fatal(err)
			}
			o, ok, err := db.OwnerByPubkey("pk-relogin")
			if err != nil || !ok {
				t.Fatalf("[%s] OwnerByPubkey ok=%v err=%v", name, ok, err)
			}
			if o.Login != "new-login" {
				t.Errorf("[%s] login = %q, want new-login (updated)", name, o.Login)
			}
			if o.Email != "user@set.com" {
				t.Errorf("[%s] email = %q, want user@set.com (user-set, not clobbered)", name, o.Email)
			}
			if o.Name != "Set Name" {
				t.Errorf("[%s] name = %q, want Set Name (preserved)", name, o.Name)
			}
			if o.CreatedAt != before.CreatedAt {
				t.Errorf("[%s] created_at = %d, want %d (preserved across re-login)", name, o.CreatedAt, before.CreatedAt)
			}
			if o.WelcomedAt == 0 || o.WelcomedAt != before.WelcomedAt {
				t.Errorf("[%s] welcomed_at = %d, want %d (durable, survives re-login)", name, o.WelcomedAt, before.WelcomedAt)
			}
			if o.ConnectID != "acct_keep" || o.ConnectStatus != "active" {
				t.Errorf("[%s] connect = %q/%q, want acct_keep/active (preserved)", name, o.ConnectID, o.ConnectStatus)
			}

			// Account 2: empty email + name on first bind -> GitHub fills both on re-login.
			if err := db.BindOwner(Owner{Pubkey: "pk-fill", GitHubID: 20, Login: "fill"}); err != nil {
				t.Fatal(err)
			}
			if err := db.BindOwner(Owner{Pubkey: "pk-fill", GitHubID: 20, Login: "fill", Name: "Filled", Email: "filled@github.com"}); err != nil {
				t.Fatal(err)
			}
			o2, _, _ := db.OwnerByPubkey("pk-fill")
			if o2.Email != "filled@github.com" || o2.Name != "Filled" {
				t.Errorf("[%s] fill = email %q / name %q, want filled from GitHub (were empty)", name, o2.Email, o2.Name)
			}
			_ = db.Close()
		})
	}
}
