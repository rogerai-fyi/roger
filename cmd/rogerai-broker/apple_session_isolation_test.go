package main

// apple_session_isolation_test.go enforces features/security/apple_session_isolation.feature:
// an Apple web session (githubID==0) must NEVER resolve or mutate a GitHub owner via any
// session-login-keyed handler. Drives the REAL handlers against store.NewMem() with a real
// signed session cookie — no mocks. RED before the gid-gate + safe-login fix.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// ghAppleBroker binds a GitHub owner "apple" (the real high-value collision target) + returns
// an Apple web session cookie value (login "apple", githubID 0, an Apple wallet).
func ghAppleBroker(t *testing.T) (*broker, string) {
	t.Helper()
	mem := store.NewMem()
	b := relayBroker(mem)
	if err := mem.BindOwner(store.Owner{GitHubID: 501, Login: "apple", Pubkey: "ghapplepub", Email: "ops@apple-operator.test", ConnectID: "acct_apple", ConnectStatus: "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddCredits("u_gh_501", 50); err != nil {
		t.Fatal(err)
	}
	// Give the GitHub "apple" owner a grant so a leak would be observable.
	_ = mem.CreateGrant(store.Grant{ID: "g_apple", SecretHash: "h", Owner: "ghapplepub", Free: true, Label: "apple-bot"})
	// An Apple WEB session: no github id, an Apple wallet, login = the literal "apple".
	cookie := b.signSessionWallet("apple", 0, "u_apple_deadbeef", time.Now().Add(time.Hour).Unix())
	return b, cookie
}

func withSession(r *http.Request, cookie string) *http.Request {
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	return r
}

func TestAppleSessionCannotReadGitHubAccount(t *testing.T) {
	b, cookie := ghAppleBroker(t)
	w := httptest.NewRecorder()
	b.account(w, withSession(httptest.NewRequest(http.MethodGet, "/account", nil), cookie))
	body := w.Body.String()
	if strings.Contains(body, "ops@apple-operator.test") {
		t.Fatalf("GET /account leaked the GitHub owner's email to an Apple session:\n%s", body)
	}
	if strings.Contains(body, "earnings") {
		t.Errorf("GET /account leaked the GitHub owner's earnings split:\n%s", body)
	}
}

func TestAppleSessionCannotExportGitHubOwner(t *testing.T) {
	b, cookie := ghAppleBroker(t)
	w := httptest.NewRecorder()
	b.accountExport(w, withSession(httptest.NewRequest(http.MethodPost, "/account/export", nil), cookie))
	body := w.Body.String()
	for _, leak := range []string{"ops@apple-operator.test", "operator_ledger", "\"payouts\""} {
		if strings.Contains(body, leak) {
			t.Errorf("account export leaked %q for the GitHub owner:\n%s", leak, body)
		}
	}
}

func TestAppleSessionCannotManageGitHubGrants(t *testing.T) {
	b, cookie := ghAppleBroker(t)
	w := httptest.NewRecorder()
	b.grants(w, withSession(httptest.NewRequest(http.MethodGet, "/grants", nil), cookie))
	if strings.Contains(w.Body.String(), "apple-bot") || strings.Contains(w.Body.String(), "g_apple") {
		t.Fatalf("GET /grants listed the GitHub owner's grant to an Apple session:\n%s", w.Body.String())
	}
}

func TestAppleSessionCannotPatchGitHubEmail(t *testing.T) {
	b, cookie := ghAppleBroker(t)
	w := httptest.NewRecorder()
	r := withSession(httptest.NewRequest(http.MethodPatch, "/account", strings.NewReader(`{"email":"attacker@evil.test"}`)), cookie)
	b.account(w, r)
	o, _, _ := b.db.OwnerByLogin("apple")
	if o.Email == "attacker@evil.test" {
		t.Fatal("an Apple session PATCHed the GitHub owner's email — write takeover")
	}
}

func TestAppleSessionCannotDeleteGitHubOwner(t *testing.T) {
	b, cookie := ghAppleBroker(t)
	// zero the Apple wallet so the delete isn't blocked by a balance guard (isolate the bug).
	w := httptest.NewRecorder()
	b.accountDelete(w, withSession(httptest.NewRequest(http.MethodPost, "/account/delete", nil), cookie))
	o, found, _ := b.db.OwnerByLogin("apple")
	if !found || o.Anonymized {
		t.Fatalf("an Apple session anonymized/deleted the GitHub owner (found=%v anonymized=%v) — delete takeover", found, o.Anonymized)
	}
}

// TestAppleWebLoginNeverGitHubShaped enforces A2 (source hardening): a no-email Apple web
// login is "apple:"+short(sub) — never GitHub-shaped (':' is illegal in a GitHub login),
// never the bare literal "apple", and distinct per Apple sub so two no-email users don't
// collide with each other. With an email, the email is the handle ('@' is equally illegal).
func TestAppleWebLoginNeverGitHubShaped(t *testing.T) {
	cases := []struct {
		name       string
		email, sub string
	}{
		{"no-email sub A", "", "apple-sub-A"},
		{"no-email sub B", "", "apple-sub-B"},
		{"relay email", "x9k2@privaterelay.appleid.com", "apple-sub-A"},
	}
	seen := map[string]string{}
	for _, tc := range cases {
		got := appleWebLogin(tc.email, tc.sub)
		if got == "apple" || got == "" {
			t.Fatalf("%s: login %q is the colliding literal/empty", tc.name, got)
		}
		if !strings.ContainsAny(got, ":@") {
			t.Errorf("%s: login %q is GitHub-shaped (no ':' or '@')", tc.name, got)
		}
		if tc.email != "" && got != tc.email {
			t.Errorf("%s: email present but login = %q", tc.name, got)
		}
		if prev, dup := seen[got]; dup {
			t.Errorf("%s: login %q collides with case %q", tc.name, got, prev)
		}
		seen[got] = tc.name
	}
}

// TestGitHubSessionStillResolvesOwnUnchanged: no regression — a real GitHub session reads its
// own owner + grants.
func TestGitHubSessionStillResolvesOwn(t *testing.T) {
	b, _ := ghAppleBroker(t)
	cookie := b.signSession("apple", 501, time.Now().Add(time.Hour).Unix()) // the REAL github "apple", gid!=0
	w := httptest.NewRecorder()
	b.account(w, withSession(httptest.NewRequest(http.MethodGet, "/account", nil), cookie))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["email"] != "ops@apple-operator.test" {
		t.Fatalf("a real GitHub session for login=apple gid=501 should see its own email, got %v", out["email"])
	}
}
