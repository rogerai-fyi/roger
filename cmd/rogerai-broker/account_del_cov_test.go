package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogerai-fyi/roger/internal/store"
)

// TestAccountDelete covers the GDPR soft-delete: unauthenticated -> 401, a positive
// balance blocks with 409, and a clean (zero-balance) account anonymizes with 200.
func TestAccountDelete(t *testing.T) {
	b, _ := brokerWithOwner(t) // owner octocat/7, wallet u_gh_7, b.priv set

	// Not logged in -> 401.
	w := httptest.NewRecorder()
	b.accountDelete(w, httptest.NewRequest(http.MethodPost, "/account/delete", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("delete(anon) = %d, want 401", w.Code)
	}

	// Positive balance -> 409 (must resolve funds first).
	_, _ = b.db.AddCredits("u_gh_7", 5)
	wc := httptest.NewRecorder()
	b.accountDelete(wc, sessionReq(b, http.MethodPost, "/account/delete", "octocat", 7))
	if wc.Code != http.StatusConflict {
		t.Fatalf("delete(balance>0) = %d, want 409", wc.Code)
	}

	// Zero the balance -> clean delete (200, anonymized).
	_, _ = b.db.AddCredits("u_gh_7", -5)
	wok := httptest.NewRecorder()
	b.accountDelete(wok, sessionReq(b, http.MethodPost, "/account/delete", "octocat", 7))
	if wok.Code != http.StatusOK {
		t.Fatalf("delete(clean) = %d, want 200: %s", wok.Code, wok.Body.String())
	}
	// Deleting the account must expire BOTH the session cookie AND the readable
	// signed-in hint - otherwise the deleted user's browser keeps the stale flag and
	// goes on probing /account (401). (features/security/web_session_hint.feature.)
	for _, name := range []string{sessionCookie, signedInHint} {
		c := cookieByName(wok, name)
		if c == nil || c.MaxAge >= 0 || c.Value != "" {
			t.Errorf("delete must expire %q, got %+v", name, c)
		}
	}
	if _, ok, _ := b.db.OwnerByLogin("octocat"); ok {
		t.Error("a deleted login should no longer resolve (anonymized)")
	}
}

// TestAdminActivity covers the admin cross-account ledger stream: 403 without the key,
// and a keyed GET returns the recent ledger rows.
func TestAdminActivity(t *testing.T) {
	mem := store.NewMem()
	_, _ = mem.AddCredits("u1", 10) // a topup ledger row to surface
	b := &broker{db: mem, adminKey: "super-secret"}

	// No admin key -> 403.
	w := httptest.NewRecorder()
	b.adminActivity(w, httptest.NewRequest(http.MethodGet, "/admin/activity", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("adminActivity without key = %d, want 403", w.Code)
	}

	// Keyed -> 200.
	r := httptest.NewRequest(http.MethodGet, "/admin/activity?limit=10", nil)
	r.Header.Set("X-Roger-Admin", "super-secret")
	w2 := httptest.NewRecorder()
	b.adminActivity(w2, r)
	if w2.Code != http.StatusOK {
		t.Fatalf("adminActivity keyed = %d, want 200: %s", w2.Code, w2.Body.String())
	}
}
