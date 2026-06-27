package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDashboardReads covers the consumer/owner dashboard read handlers via owner+session
// auth: /me, /console, and /earnings (incl. its 400/401/403 guards + the owned-node 200).
func TestDashboardReads(t *testing.T) {
	b, o := brokerWithOwner(t)
	_ = b.db.BindNode("n1", o.Pubkey)
	_, _ = b.db.AddCredits("u_gh_7", 5)

	// /me (consumer dashboard) via session.
	wm := httptest.NewRecorder()
	b.me(wm, sessionReq(b, http.MethodGet, "/me", "octocat", 7))
	if wm.Code != http.StatusOK {
		t.Fatalf("/me = %d, want 200: %s", wm.Code, wm.Body.String())
	}

	// /console feed via session.
	wc := httptest.NewRecorder()
	b.console(wc, sessionReq(b, http.MethodGet, "/console", "octocat", 7))
	if wc.Code != http.StatusOK {
		t.Fatalf("/console = %d, want 200: %s", wc.Code, wc.Body.String())
	}

	// /earnings: missing node -> 400.
	w400 := httptest.NewRecorder()
	b.earnings(w400, sessionReq(b, http.MethodGet, "/earnings", "octocat", 7))
	if w400.Code != http.StatusBadRequest {
		t.Fatalf("/earnings no-node = %d, want 400", w400.Code)
	}
	// /earnings: anonymous -> 401.
	w401 := httptest.NewRecorder()
	b.earnings(w401, httptest.NewRequest(http.MethodGet, "/earnings?node=n1", nil))
	if w401.Code != http.StatusUnauthorized {
		t.Fatalf("/earnings anon = %d, want 401", w401.Code)
	}
	// /earnings: a node the owner does NOT own -> 403.
	w403 := httptest.NewRecorder()
	b.earnings(w403, sessionReq(b, http.MethodGet, "/earnings?node=not-mine", "octocat", 7))
	if w403.Code != http.StatusForbidden {
		t.Fatalf("/earnings not-owned = %d, want 403", w403.Code)
	}
	// /earnings: an owned node -> 200.
	wok := httptest.NewRecorder()
	b.earnings(wok, sessionReq(b, http.MethodGet, "/earnings?node=n1", "octocat", 7))
	if wok.Code != http.StatusOK {
		t.Fatalf("/earnings owned = %d, want 200: %s", wok.Code, wok.Body.String())
	}
}
