package main

// Regression: an email sign-in must be a first-class ACCOUNT for every dashboard read.
//
// The bug this pins: isAccountWallet knew only u_gh_ and u_apple_, so an email session's
// wallet (u_email_) read as an anonymous keypair. /metrics/series answered 401, dashboard.js
// sent the person to /login.html, and login.html (which only asks /account) sent them
// straight back: an endless login <-> dashboard refresh loop after typing the code.

import (
	"time"

	"net/http"
	"net/http/httptest"
	"rogerai.fm/roger/v6/internal/store"
	"testing"

	"github.com/stretchr/testify/require"
)

// signInByEmail runs the real start+verify routes and returns the session cookie.
func signInByEmail(t *testing.T, b *broker, cap *capturedMail, addr string) *http.Cookie {
	t.Helper()
	require.Equal(t, http.StatusOK, postJSON(t, b.emailStart, "/auth/email/start", map[string]string{"email": addr}).Code)
	waitForMail(t, cap, 1)
	verify := postJSON(t, b.emailVerify, "/auth/email/verify", map[string]string{"email": addr, "code": codeFromMail(t, cap)})
	require.Equal(t, http.StatusOK, verify.Code)
	for _, c := range verify.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("no session cookie minted")
	return nil
}

func getWithSession(h http.HandlerFunc, path string, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Origin", testWebOrigin)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestAnEmailSessionIsAnAccountForEveryDashboardRead(t *testing.T) {
	b, cap := emailTestBroker(t)
	sess := signInByEmail(t, b, cap, "loop@rogerai.fm")

	for name, h := range map[string]http.HandlerFunc{
		"/me":             b.me,
		"/metrics/series": b.metricsSeries,
	} {
		rec := getWithSession(h, name, sess)
		require.Equal(t, http.StatusOK, rec.Code, "%s must not 401 an email session (that 401 is the login loop): %s", name, rec.Body.String())
	}

	rec := getWithSession(b.me, "/me", sess)
	require.Contains(t, rec.Body.String(), `"logged_in":true`)
}

func TestWalletPredicates(t *testing.T) {
	cases := []struct {
		wallet            string
		account, reserved bool
	}{
		{"u_gh_123", true, true},
		{"u_apple_0123456789abcdef", true, true},
		{"u_email_0123456789abcdef", true, true}, // guessable from an address: same leak guard
		{"u_0123456789abcdef", false, true},      // anonymous pubkey-derived
		{"g_abc", false, true},
		{"someone", false, false},
		{"", false, false},
	}
	for _, c := range cases {
		require.Equal(t, c.account, isAccountWallet(c.wallet), "isAccountWallet(%q)", c.wallet)
		require.Equal(t, c.reserved, reservedID(c.wallet), "reservedID(%q)", c.wallet)
	}
}

// An address that already belongs to a GitHub-linked account must sign in as THAT account in
// full: the same identity the GitHub callback would mint (gid set), so every owner-gated
// route resolves it. A gid-less session carrying a GitHub handle as its "login" resolved to
// no owner anywhere (payouts, stations, device approval all refused it).
func TestEmailSignInToAGitHubLinkedAccountCarriesTheGitHubIdentity(t *testing.T) {
	b, cap := emailTestBroker(t)
	require.NoError(t, b.db.BindOwner(store.Owner{
		Pubkey: "pk-1", GitHubID: 7, Login: "octocat",
		Email: "octocat@rogerai.fm", EmailVerifiedAt: time.Now().Unix(),
	}))
	sess := signInByEmail(t, b, cap, "octocat@rogerai.fm")

	login, gid, wallet, _, ok := b.verifySessionFull(sess.Value)
	require.True(t, ok)
	require.Equal(t, "octocat", login)
	require.Equal(t, int64(7), gid, "a GitHub-linked account's email session is a GitHub-shaped session")
	require.Equal(t, "u_gh_7", wallet)

	rec := getWithSession(b.account, "/account", sess)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"email":"octocat@rogerai.fm"`)
}

// An email-only operator (device login bound a CLI key to their verified address) is a real
// operator: stations, keys and the account view must all resolve them, not 403.
func TestAnEmailOnlyOperatorIsNotSecondClass(t *testing.T) {
	b, cap := emailTestBroker(t)
	require.NoError(t, b.db.BindOwner(store.Owner{
		Pubkey: "pk-e", Email: "op@rogerai.fm", EmailVerifiedAt: time.Now().Unix(),
	}))
	sess := signInByEmail(t, b, cap, "op@rogerai.fm")

	for path, h := range map[string]http.HandlerFunc{
		"/stations": b.stations,
		"/grants":   b.grants,
		"/account":  b.account,
	} {
		rec := getWithSession(h, path, sess)
		require.Equal(t, http.StatusOK, rec.Code, "%s must serve an email-only operator: %s", path, rec.Body.String())
	}
	acct := getWithSession(b.account, "/account", sess)
	require.Contains(t, acct.Body.String(), `"email":"op@rogerai.fm"`)
}

// Deleting an account is a store-policy obligation; an email account must be able to do it
// in-app, not be told it "can't be deleted yet".
func TestAnEmailAccountCanDeleteItself(t *testing.T) {
	b, cap := emailTestBroker(t)
	b.seedFunds = 0 // no welcome balance to block the delete
	require.NoError(t, b.db.BindOwner(store.Owner{
		Pubkey: "pk-d", Email: "bye@rogerai.fm", EmailVerifiedAt: time.Now().Unix(),
	}))
	sess := signInByEmail(t, b, cap, "bye@rogerai.fm")
	req := httptest.NewRequest(http.MethodPost, "/account/delete", nil)
	req.Header.Set("Origin", testWebOrigin)
	req.AddCookie(sess)
	rec := httptest.NewRecorder()
	b.accountDelete(rec, req)
	require.NotEqual(t, http.StatusConflict, rec.Code, "must not say it can't be deleted in-app: %s", rec.Body.String())
}
