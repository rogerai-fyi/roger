package main

// The CSRF guard (csrfguard.go): a state-changing request carrying the session cookie must
// come from one of our own origins. See features/security/csrf_cookie_mutations.feature.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

func guardCall(t *testing.T, method, path, origin string, cookie, signed bool) (code int, reached bool) {
	t.Helper()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true; w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if cookie {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "whatever.sig"})
	}
	if signed {
		req.Header.Set(protocol.HeaderPubkey, "ab12")
	}
	rec := httptest.NewRecorder()
	csrfGuard(next).ServeHTTP(rec, req)
	return rec.Code, reached
}

func TestCSRFGuardDecisionTable(t *testing.T) {
	const ours, fyi, evil = "https://rogerai.fm", "https://rogerai.fyi", "https://evil.example"
	cases := []struct {
		name                 string
		method, path, origin string
		cookie, signed       bool
		pass                 bool
	}{
		{"foreign POST with cookie", "POST", "/account/delete", evil, true, false, false},
		{"foreign PATCH with cookie", "PATCH", "/account/limit", evil, true, false, false},
		{"foreign PUT with cookie", "PUT", "/anything", evil, true, false, false},
		{"foreign DELETE with cookie", "DELETE", "/grants/g1", evil, true, false, false},
		{"null origin with cookie", "POST", "/account/delete", "null", true, false, false},
		{"no origin with cookie", "POST", "/account/delete", "", true, false, false},
		{"our site POST", "POST", "/account/delete", ours, true, false, true},
		{"our other origin POST", "POST", "/grants", fyi, true, false, true},
		{"foreign GET with cookie", "GET", "/account", evil, true, false, true},
		{"foreign HEAD with cookie", "HEAD", "/account", evil, true, false, true},
		{"foreign preflight", "OPTIONS", "/account/delete", evil, true, false, true},
		{"cookie-less CLI POST, no origin", "POST", "/auth/device/start", "", false, false, true},
		{"cookie-less webhook POST", "POST", "/billing/webhook", "", false, false, true},
		{"cookie-less foreign POST", "POST", "/v1/chat/completions", evil, false, false, true},
		{"signed native request + stale cookie, no origin", "POST", "/account/delete", "", true, true, true},
		{"apple form post to the web callback", "POST", "/auth/apple/web/callback", "https://appleid.apple.com", true, false, true},
		{"apple exemption is only that path", "POST", "/account/delete", "https://appleid.apple.com", true, false, false},
	}
	for _, c := range cases {
		code, reached := guardCall(t, c.method, c.path, c.origin, c.cookie, c.signed)
		require.Equal(t, c.pass, reached, "%s: handler reached", c.name)
		if c.pass {
			require.Equal(t, http.StatusOK, code, c.name)
		} else {
			require.Equal(t, http.StatusForbidden, code, c.name)
		}
	}
}

func TestOnlyTheSessionCookieTriggersTheGuard(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest("POST", "/x", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: "some_analytics_cookie", Value: "1"})
	rec := httptest.NewRecorder()
	csrfGuard(next).ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "an unrelated cookie is not a session")
}

// The real destructive routes, behind the guard, hit by a foreign page with the victim's cookie.
func TestRealDestructiveRoutesAreProtectedEndToEnd(t *testing.T) {
	b, _ := emailTestBroker(t)
	b.seedFunds = 0
	require.NoError(t, b.db.BindOwner(store.Owner{Pubkey: "pk-v", GitHubID: 7, Login: "victim"}))
	cookie := &http.Cookie{Name: sessionCookie, Value: b.signSession("victim", 7, time.Now().Add(time.Hour).Unix())}

	mux := http.NewServeMux()
	mux.HandleFunc("/account/delete", b.accountDelete)
	mux.HandleFunc("/account/limit", b.accountLimit)
	mux.HandleFunc("/grants", b.grants)
	mux.HandleFunc("/owner/appeal", b.ownerAppeal)
	mux.HandleFunc("/auth/logout", b.authLogout)
	h := csrfGuard(mux)

	for _, c := range []struct{ method, path, body string }{
		{"POST", "/account/delete", ""},
		{"PATCH", "/account/limit", `{"monthly_cap":1000000}`},
		{"POST", "/grants", `{"name":"x","free":true}`},
		{"POST", "/owner/appeal", `{"reason":"forged"}`},
		{"POST", "/auth/logout", ""},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		req.Header.Set("Origin", "https://evil.example")
		req.Header.Set("Content-Type", "text/plain") // a "simple" request: no preflight
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusForbidden, rec.Code, "%s %s must be refused: %s", c.method, c.path, rec.Body.String())
	}
	o, _, _ := b.db.OwnerByPubkey("pk-v")
	require.False(t, o.Anonymized, "the account was not deleted")
	appeals, _ := b.db.AppealsByOwner("pk-v", 10)
	require.Empty(t, appeals, "no appeal was filed in the victim's name")
}
