package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAuthGitHubLogin covers the web OAuth entrypoint: 503 when unconfigured, and a 302
// redirect to GitHub (with a state cookie) when the client id + secret are set.
func TestAuthGitHubLogin(t *testing.T) {
	b := &broker{}

	// Unconfigured -> 503.
	t.Setenv("GITHUB_OAUTH_CLIENT_ID", "")
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "")
	w := httptest.NewRecorder()
	b.authGitHubLogin(w, httptest.NewRequest(http.MethodGet, "/auth/github", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured login = %d, want 503", w.Code)
	}

	// Configured -> 302 to github with a state cookie.
	t.Setenv("GITHUB_OAUTH_CLIENT_ID", "cid")
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "sec")
	w2 := httptest.NewRecorder()
	b.authGitHubLogin(w2, httptest.NewRequest(http.MethodGet, "/auth/github", nil))
	if w2.Code != http.StatusFound {
		t.Fatalf("configured login = %d, want 302", w2.Code)
	}
	if loc := w2.Header().Get("Location"); !strings.Contains(loc, "github.com/login/oauth/authorize") {
		t.Errorf("redirect = %q, want github authorize", loc)
	}
	var hasState bool
	for _, c := range w2.Result().Cookies() {
		if c.Name == "roger_oauth_state" && c.Value != "" {
			hasState = true
		}
	}
	if !hasState {
		t.Error("login should set an oauth state cookie")
	}
}

// TestAuthGitHubCallback covers the callback's early branches: 503 unconfigured, and the
// state-mismatch redirect (missing code/state) - without reaching github.com.
func TestAuthGitHubCallback(t *testing.T) {
	b := &broker{}
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "")
	w := httptest.NewRecorder()
	b.authGitHubCallback(w, httptest.NewRequest(http.MethodGet, "/auth/github/callback", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured callback = %d, want 503", w.Code)
	}

	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "sec")
	w2 := httptest.NewRecorder()
	b.authGitHubCallback(w2, httptest.NewRequest(http.MethodGet, "/auth/github/callback", nil)) // no code/state
	if w2.Code != http.StatusFound || !strings.Contains(w2.Header().Get("Location"), "error=state") {
		t.Errorf("missing-code callback = %d / %q, want 302 ?error=state", w2.Code, w2.Header().Get("Location"))
	}
}

// TestCheckoutDisabled covers the top-up checkout's not-configured branch (no Stripe key).
func TestCheckoutDisabled(t *testing.T) {
	b, _ := brokerWithOwner(t)
	b.bill.secretKey = "" // billing not configured
	w := httptest.NewRecorder()
	b.checkout(w, httptest.NewRequest(http.MethodPost, "/billing/checkout", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("disabled checkout = %d, want 503", w.Code)
	}
}

// TestConnectOnboardStub covers the Stripe Connect onboard DEV-STUB path (no
// STRIPE_SECRET_KEY): a bound operator gets a stub onboarding response + a stored
// connect status. Auth is the operator's web session.
func TestConnectOnboardStubAnonAndPersist(t *testing.T) {
	b, _ := brokerWithOwner(t)
	b.conn.secretKey = "" // force the dev stub
	b.conn.returnURL = "https://return.example"

	r := sessionReq(b, http.MethodPost, "/connect/onboard", "octocat", 7)
	w := httptest.NewRecorder()
	b.connectOnboard(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("connectOnboard stub = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["stub"] != true || resp["status"] != "onboarding" {
		t.Errorf("stub response = %+v, want stub onboarding", resp)
	}
	// The connect status was persisted on the owner.
	if o, ok, _ := b.db.OwnerByLogin("octocat"); !ok || o.ConnectStatus != "onboarding" {
		t.Errorf("connect status not persisted: %+v ok=%v", o, ok)
	}

	// Unauthenticated -> 401.
	w2 := httptest.NewRecorder()
	b.connectOnboard(w2, httptest.NewRequest(http.MethodPost, "/connect/onboard", nil))
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("anon connectOnboard = %d, want 401", w2.Code)
	}
}

// TestAccountGet covers the account snapshot read: balance + connect status + earnings
// split enrichment for a bound operator.
func TestAccountGet(t *testing.T) {
	b, o := brokerWithOwner(t)
	_, _ = b.db.AddCredits("u_gh_7", 8)
	_ = b.db.SetConnect("octocat", "acct_x", "active")

	w := httptest.NewRecorder()
	b.accountGet(w, httptest.NewRequest(http.MethodGet, "/account", nil), "octocat", 7, "u_gh_7")
	if w.Code != http.StatusOK {
		t.Fatalf("accountGet = %d, want 200", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["github_login"] != "octocat" {
		t.Errorf("accountGet login = %v", resp["github_login"])
	}
	if resp["balance"].(float64) < 7.9 {
		t.Errorf("accountGet balance = %v, want ~8", resp["balance"])
	}
	conn, _ := resp["connect"].(map[string]any)
	if conn["status"] != "active" {
		t.Errorf("accountGet connect = %+v, want active", conn)
	}
	_ = o
}
