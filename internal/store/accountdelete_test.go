package store

import (
	"testing"
	"time"
)

// TestDeleteAccountScrubsEveryIdentifier pins what privacy.html promises the delete does:
// the GitHub id, the Apple sub, the name, the address and its verification stamp are all
// CLEARED, not merely made unreachable by the anonymized flag. The financial identity -
// the opaque pubkey - is what survives, because the retained ledger rows are keyed by it.
func TestDeleteAccountScrubsEveryIdentifier(t *testing.T) {
	m := NewMem()
	if err := m.BindOwner(Owner{
		GitHubID: 4242, Login: "octocat", AppleSub: "apple-sub-xyz",
		Pubkey: "pk1", Name: "Octo Cat", Email: "a@b.c",
		EmailVerifiedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := m.DeleteAccount("octocat"); err != nil || !ok {
		t.Fatalf("delete: ok=%v err=%v", ok, err)
	}

	// The row survives (financial retention) but carries nothing that names a person.
	o, ok, _ := m.OwnerByPubkey("pk1")
	if !ok {
		t.Fatal("the de-identified row must survive for financial retention")
	}
	if o.GitHubID != 0 {
		t.Errorf("github id retained: %d", o.GitHubID)
	}
	if o.AppleSub != "" {
		t.Errorf("apple sub retained: %q", o.AppleSub)
	}
	if o.Name != "" {
		t.Errorf("name retained: %q", o.Name)
	}
	if o.Email != "" || o.EmailVerifiedAt != 0 {
		t.Errorf("email retained: %q verified=%d", o.Email, o.EmailVerifiedAt)
	}
	if o.Login == "octocat" {
		t.Error("login not scrambled")
	}
	if !o.Anonymized || o.DeletedAt == 0 {
		t.Errorf("not marked deleted: anonymized=%v at=%d", o.Anonymized, o.DeletedAt)
	}

	// And no sign-in route reaches it again.
	if _, ok, _ := m.OwnerByLogin("octocat"); ok {
		t.Error("login still resolves")
	}
	if _, ok, _ := m.OwnerByAppleSub("apple-sub-xyz"); ok {
		t.Error("apple sub still resolves")
	}
	if _, ok, _ := m.OwnerByVerifiedEmail("a@b.c"); ok {
		t.Error("verified email still resolves")
	}
}
