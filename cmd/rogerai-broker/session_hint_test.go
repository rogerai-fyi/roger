package main

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"
)

// cookieByName returns the Set-Cookie entry for name from a recorded response, or nil.
func cookieByName(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// TestSetWebSessionCookies: login sets the HttpOnly session credential AND a readable
// `roger_signed_in=1` hint, same expiry, the hint NOT HttpOnly + scoped to the web origin
// host - so the front-end can skip the logged-out /account probe. (features/security/
// web_session_hint.feature.)
func TestSetWebSessionCookies(t *testing.T) {
	t.Setenv("ROGERAI_WEB_ORIGIN", "https://rogerai.fyi")
	_, priv, _ := ed25519.GenerateKey(nil)
	b := &broker{priv: priv}
	rec := httptest.NewRecorder()
	exp := int64(2_000_000_000)

	b.setWebSessionCookies(rec, "octocat", 7, exp)

	sess := cookieByName(rec, sessionCookie)
	if sess == nil || !sess.HttpOnly || !sess.Secure || sess.Value == "" {
		t.Fatalf("session cookie = %+v, want a non-empty HttpOnly+Secure credential", sess)
	}
	hint := cookieByName(rec, signedInHint)
	if hint == nil {
		t.Fatal("login must set the roger_signed_in hint cookie")
	}
	if hint.Value != "1" {
		t.Errorf("hint value = %q, want \"1\" (presence flag, no identity)", hint.Value)
	}
	if hint.HttpOnly {
		t.Error("hint cookie must NOT be HttpOnly (the web JS reads it)")
	}
	if hint.Domain != "rogerai.fyi" {
		t.Errorf("hint Domain = %q, want the web origin host rogerai.fyi", hint.Domain)
	}
	if !hint.Secure {
		t.Error("hint cookie should be Secure")
	}
	if hint.Expires.Unix() != sess.Expires.Unix() {
		t.Errorf("hint expiry %v != session expiry %v (must match)", hint.Expires, sess.Expires)
	}
	// The hint must leak NOTHING beyond presence: no login, id, wallet, or signature.
	if hint.Value != "1" || len(hint.Value) != 1 {
		t.Errorf("hint must carry only \"1\", got %q", hint.Value)
	}
}

// TestClearWebSessionCookies: logout expires BOTH cookies (no stale "signed in" flag).
func TestClearWebSessionCookies(t *testing.T) {
	rec := httptest.NewRecorder()
	clearWebSessionCookies(rec)
	for _, name := range []string{sessionCookie, signedInHint} {
		c := cookieByName(rec, name)
		if c == nil {
			t.Fatalf("logout must clear %q", name)
		}
		if c.Value != "" || c.MaxAge >= 0 {
			t.Errorf("%q not expired on logout: value=%q maxage=%d", name, c.Value, c.MaxAge)
		}
	}
}

// TestWebOriginHost: the hint Domain is derived from ROGERAI_WEB_ORIGIN's host, with a
// safe empty fallback when unset/garbage (host-only hint, front-end falls back to probing).
func TestWebOriginHost(t *testing.T) {
	cases := []struct{ env, want string }{
		{"", "rogerai.fyi"},                       // default
		{"https://rogerai.fyi", "rogerai.fyi"},    // explicit prod
		{"https://app.example.com:8443", "app.example.com"}, // custom host (port stripped)
		{"::::not a url", ""},                      // unparseable -> empty (host-only fallback)
	}
	for _, c := range cases {
		if c.env == "" {
			t.Setenv("ROGERAI_WEB_ORIGIN", "")
		} else {
			t.Setenv("ROGERAI_WEB_ORIGIN", c.env)
		}
		if got := webOriginHost(); got != c.want {
			t.Errorf("webOriginHost(%q) = %q, want %q", c.env, got, c.want)
		}
	}
}
