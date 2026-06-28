package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
