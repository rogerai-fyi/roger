package main

// Returning a person to where they were after they sign in.
//
// Without this, someone who lands on /device.html signed out is sent to sign in and then
// dropped at the dashboard - losing the code they were about to approve. With it, the
// login routes carry a `next`, and the callback returns them there.
//
// The whole risk of a return parameter is the open redirect: an attacker sends
// ?next=https://evil.example and the victim is bounced off-site by a link that genuinely
// came from us. So the only accepted form is a same-site absolute PATH.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSafeNextAcceptsSameSitePaths(t *testing.T) {
	for _, ok := range []string{
		"/device.html",
		"/device.html?code=ABCD2468",
		"/account.html",
		"/",
	} {
		require.Equal(t, ok, safeNext(ok), "%q is a same-site path", ok)
	}
}

// Every one of these is a way to leave the site, and each has been a real open-redirect
// bug somewhere. They must all fall back to the default destination.
func TestSafeNextRefusesEverythingThatCouldLeaveTheSite(t *testing.T) {
	for name, bad := range map[string]string{
		"absolute http":        "http://evil.example/",
		"absolute https":       "https://evil.example/",
		"protocol relative":    "//evil.example/",
		"backslash trick":      "/\\evil.example",
		"scheme relative tab":  "/\tevil",
		"javascript":           "javascript:alert(1)",
		"data":                 "data:text/html,x",
		"empty":                "",
		"relative no slash":    "device.html",
		"encoded protocol rel": "/%2Fevil.example",
		"newline injection":    "/device.html\nLocation: http://evil.example",
		"crlf injection":       "/device.html\r\nSet-Cookie: x=1",
	} {
		t.Run(name, func(t *testing.T) {
			require.Empty(t, safeNext(bad), "%q must not be accepted as a return target", bad)
		})
	}
}

func TestReturnTargetFallsBackToTheDashboard(t *testing.T) {
	require.Equal(t, dashboardURL(), returnTarget(""))
	require.Equal(t, dashboardURL(), returnTarget("https://evil.example"))
}

func TestReturnTargetKeepsASafePath(t *testing.T) {
	got := returnTarget("/device.html?code=ABCD2468")
	require.Contains(t, got, "/device.html?code=ABCD2468")
	require.True(t, len(got) > len("/device.html"), "the return target is absolute, on our own site")
	require.NotContains(t, got, "evil")
}

// --- the cookie round trip -------------------------------------------------

func TestNextCookieRoundTripsASafeDestination(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/auth/github/login?next=%2Fdevice.html%3Fcode%3DABCD2468", nil)
	w := httptest.NewRecorder()
	setNextCookie(w, r)

	res := w.Result()
	defer res.Body.Close()
	var got *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == nextCookie {
			got = c
		}
	}
	require.NotNil(t, got, "a safe next must be recorded")
	require.Equal(t, "/device.html?code=ABCD2468", got.Value)
	require.True(t, got.HttpOnly, "the return destination is not JS's business")

	// And it comes back resolved to our own origin.
	r2 := httptest.NewRequest(http.MethodGet, "/auth/github/callback", nil)
	r2.AddCookie(got)
	w2 := httptest.NewRecorder()
	require.Contains(t, takeNextCookie(w2, r2), "/device.html?code=ABCD2468")
}

func TestAnUnsafeNextIsNeverRecorded(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/auth/github/login?next=https%3A%2F%2Fevil.example", nil)
	w := httptest.NewRecorder()
	setNextCookie(w, r)

	res := w.Result()
	defer res.Body.Close()
	for _, c := range res.Cookies() {
		require.NotEqual(t, nextCookie, c.Name, "an off-site next must not be recorded at all")
	}
}

// A cookie is client-supplied. Even one we set must be re-validated on the way out, or a
// tampered cookie reopens the hole the query check closed.
func TestATamperedNextCookieFallsBackToTheDashboard(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/auth/github/callback", nil)
	r.AddCookie(&http.Cookie{Name: nextCookie, Value: "https://evil.example/"})
	w := httptest.NewRecorder()
	require.Equal(t, dashboardURL(), takeNextCookie(w, r))
}

func TestNoNextCookieFallsBackToTheDashboard(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/auth/github/callback", nil)
	w := httptest.NewRecorder()
	require.Equal(t, dashboardURL(), takeNextCookie(w, r))
}
