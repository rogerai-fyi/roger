package main

// The refusals on forgetting a band, and on the auth routes' method guards.
//
// Forgetting is the one band operation that DESTROYS a record rather than changing its
// state, so each guard below is the difference between tidying a revoked row and losing a
// live one. The happy path is covered by band_management_bdd_test.go; what was missing was
// every way the handler says no.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
)

func forgetBroker(t *testing.T) (*broker, store.Owner) {
	t.Helper()
	db := store.NewMem()
	b := relayBroker(db)
	owner := store.Owner{GitHubID: 7, Login: "op-forget", Pubkey: "op-forget-key"}
	require.NoError(t, db.BindOwner(owner))
	return b, owner
}

// Forget destroys a record, so it must never be reachable by a method a browser can be
// tricked into issuing from a link or a prefetch.
func TestForgetBandRefusesANonPost(t *testing.T) {
	b, owner := forgetBroker(t)
	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodPut} {
		r := httptest.NewRequest(method, "/bands/some-id/forget", nil)
		w := httptest.NewRecorder()
		b.forgetBand(w, r, owner, "some-id")

		require.Equal(t, http.StatusMethodNotAllowed, w.Code, "%s reached forget", method)
		require.Equal(t, "POST", w.Header().Get("Allow"),
			"a 405 must name the method that would work")
	}
}

// A band that is still live cannot be forgotten - it has to be revoked first, or a
// station mid-conversation would lose the record that says who it belongs to. The message
// has to name the remedy, because "conflict" alone leaves the operator guessing.
func TestForgetBandRefusesABandThatIsNotRevoked(t *testing.T) {
	b, owner := forgetBroker(t)
	r := httptest.NewRequest(http.MethodPost, "/bands/never-existed/forget", nil)
	w := httptest.NewRecorder()
	b.forgetBand(w, r, owner, "never-existed")

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "revoke it first",
		"the refusal must name the remedy, not just the state")
}

// --- the auth routes' method guards ---------------------------------------

// Logout clears a session, so a GET that reached it would be a one-click logout from any
// page that could embed a link. It answers the browser's preflight and refuses anything
// that is not a POST.
func TestAuthLogoutAnswersPreflightAndRefusesGet(t *testing.T) {
	b, _ := deviceBroker(t)

	pre := httptest.NewRequest(http.MethodOptions, "/auth/logout", nil)
	pre.Header.Set("Origin", testWebOrigin)
	pw := httptest.NewRecorder()
	b.authLogout(pw, pre)
	require.Equal(t, http.StatusNoContent, pw.Code, "the preflight must be answered")

	get := httptest.NewRequest(http.MethodGet, "/auth/logout", nil)
	get.Header.Set("Origin", testWebOrigin)
	gw := httptest.NewRecorder()
	b.authLogout(gw, get)
	require.Equal(t, http.StatusMethodNotAllowed, gw.Code, "a GET must not log anyone out")
}

// A POST does log out, and must clear the credential rather than only reporting success -
// a 200 with the cookie still set is the failure that looks like success.
func TestAuthLogoutClearsTheSessionCookie(t *testing.T) {
	b, _ := deviceBroker(t)
	r := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	r.Header.Set("Origin", testWebOrigin)
	w := httptest.NewRecorder()
	b.authLogout(w, r)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	cleared := false
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && (c.Value == "" || c.MaxAge < 0) {
			cleared = true
		}
	}
	require.True(t, cleared, "logout returned ok without clearing the session cookie: %v",
		w.Result().Cookies())
}
