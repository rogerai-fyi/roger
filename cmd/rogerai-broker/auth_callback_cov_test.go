package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGitHubOAuth points the token + user seams at a local server returning the given
// token and user JSON, restoring both on cleanup.
func fakeGitHubOAuth(t *testing.T, tokenJSON, userJSON string) {
	t.Helper()
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(tokenJSON))
	}))
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(userJSON))
	}))
	t.Cleanup(tok.Close)
	t.Cleanup(api.Close)
	oldT, oldA := ghAccessTokenURL, gitHubAPI
	ghAccessTokenURL, gitHubAPI = tok.URL, api.URL
	t.Cleanup(func() { ghAccessTokenURL, gitHubAPI = oldT, oldA })
}

// TestAuthGitHubCallbackGuards locks the OAuth-callback front gates: a non-GET is 405, and
// (with no client secret configured) any callback is 503.
func TestAuthGitHubCallbackGuards(t *testing.T) {
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "")
	b, _ := brokerWithOwner(t)

	// Non-GET -> 405.
	wm := httptest.NewRecorder()
	b.authGitHubCallback(wm, httptest.NewRequest(http.MethodPost, "/auth/github/callback", nil))
	if wm.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST callback = %d, want 405", wm.Code)
	}

	// Not configured -> 503.
	w := httptest.NewRecorder()
	b.authGitHubCallback(w, httptest.NewRequest(http.MethodGet, "/auth/github/callback", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured callback = %d, want 503", w.Code)
	}
}

// TestAuthGitHubCallbackStateMismatch locks the CSRF state gate: a callback whose state
// param does not match the signed state cookie redirects to ?error=state.
func TestAuthGitHubCallbackStateMismatch(t *testing.T) {
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "sec")
	b, _ := brokerWithOwner(t)
	r := httptest.NewRequest(http.MethodGet, "/auth/github/callback?code=c&state=WRONG", nil)
	r.AddCookie(&http.Cookie{Name: "roger_oauth_state", Value: "RIGHT"})
	w := httptest.NewRecorder()
	b.authGitHubCallback(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("state-mismatch callback = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc == "" || !strings.Contains(loc, "error=state") {
		t.Errorf("redirect = %q, want ?error=state", loc)
	}
}

// TestAuthGitHubCallbackExchangeFail locks the token-exchange failure redirect: a good
// state but a token endpoint that returns no access_token redirects to ?error=exchange.
func TestAuthGitHubCallbackExchangeFail(t *testing.T) {
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "sec")
	fakeGitHubOAuth(t, `{"error":"bad_code"}`, `{}`)
	b, _ := brokerWithOwner(t)
	r := httptest.NewRequest(http.MethodGet, "/auth/github/callback?code=c&state=S", nil)
	r.AddCookie(&http.Cookie{Name: "roger_oauth_state", Value: "S"})
	w := httptest.NewRecorder()
	b.authGitHubCallback(w, r)
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "error=exchange") {
		t.Errorf("redirect = %q, want ?error=exchange", loc)
	}
}

// TestAuthGitHubCallbackUserFail locks the user-fetch failure redirect: a good token but a
// /user endpoint returning an empty user (id 0) redirects to ?error=user.
func TestAuthGitHubCallbackUserFail(t *testing.T) {
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "sec")
	fakeGitHubOAuth(t, `{"access_token":"gho_x"}`, `{"id":0}`)
	b, _ := brokerWithOwner(t)
	r := httptest.NewRequest(http.MethodGet, "/auth/github/callback?code=c&state=S", nil)
	r.AddCookie(&http.Cookie{Name: "roger_oauth_state", Value: "S"})
	w := httptest.NewRecorder()
	b.authGitHubCallback(w, r)
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "error=user") {
		t.Errorf("redirect = %q, want ?error=user", loc)
	}
}

// TestAuthGitHubCallbackSuccess locks the happy path: a valid code+state and a resolvable
// GitHub user mints the session cookie and redirects to the dashboard.
func TestAuthGitHubCallbackSuccess(t *testing.T) {
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "sec")
	fakeGitHubOAuth(t, `{"access_token":"gho_x"}`, `{"id":7,"login":"octocat"}`)
	b, _ := brokerWithOwner(t)
	r := httptest.NewRequest(http.MethodGet, "/auth/github/callback?code=c&state=S", nil)
	r.AddCookie(&http.Cookie{Name: "roger_oauth_state", Value: "S"})
	w := httptest.NewRecorder()
	b.authGitHubCallback(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("success callback = %d, want 302", w.Code)
	}
	var gotSession bool
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			gotSession = true
		}
	}
	if !gotSession {
		t.Error("a successful callback must mint the session cookie")
	}
}
