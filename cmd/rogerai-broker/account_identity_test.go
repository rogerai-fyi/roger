package main

// /account must say WHO is signed in and WHETHER they are an operator, so every page can
// show an honest identity and an honest empty state instead of "-" and "could not load".
// Cost: fields the handler already has (the session and the owner row it already resolves).

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
)

func acctJSON(t *testing.T, b *broker, sess *http.Cookie) map[string]any {
	t.Helper()
	rec := getWithSession(b.account, "/account", sess)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func TestAccountReportsIdentityAndOperatorState(t *testing.T) {
	now := time.Now().Unix()

	t.Run("email, no owner row: a consumer", func(t *testing.T) {
		b, cap := emailTestBroker(t)
		a := acctJSON(t, b, signInByEmail(t, b, cap, "me@rogerai.fm"))
		require.Equal(t, "email", a["provider"])
		require.Equal(t, "me@rogerai.fm", a["email"])
		require.Equal(t, true, a["email_verified"], "an emailed code proved the address")
		require.Equal(t, false, a["operator"])
	})

	t.Run("email, bound CLI key: an operator", func(t *testing.T) {
		b, cap := emailTestBroker(t)
		require.NoError(t, b.db.BindOwner(store.Owner{Pubkey: "pk", Login: "op@rogerai.fm", Email: "op@rogerai.fm", EmailVerifiedAt: now}))
		a := acctJSON(t, b, signInByEmail(t, b, cap, "op@rogerai.fm"))
		require.Equal(t, "email", a["provider"])
		require.Equal(t, true, a["operator"])
	})

	t.Run("github session with an owner row: an operator", func(t *testing.T) {
		b, _ := emailTestBroker(t)
		require.NoError(t, b.db.BindOwner(store.Owner{Pubkey: "pk", GitHubID: 7, Login: "octocat"}))
		c := &http.Cookie{Name: sessionCookie, Value: b.signSession("octocat", 7, time.Now().Add(time.Hour).Unix())}
		a := acctJSON(t, b, c)
		require.Equal(t, "github", a["provider"])
		require.Equal(t, "octocat", a["login"])
		require.Equal(t, true, a["operator"])
		require.Equal(t, false, a["email_verified"], "no address was proven on this account")
	})

	t.Run("github web-only, no owner row: not an operator yet", func(t *testing.T) {
		b, _ := emailTestBroker(t)
		c := &http.Cookie{Name: sessionCookie, Value: b.signSession("newbie", 99, time.Now().Add(time.Hour).Unix())}
		a := acctJSON(t, b, c)
		require.Equal(t, "github", a["provider"])
		require.Equal(t, false, a["operator"])
	})

	t.Run("apple session", func(t *testing.T) {
		b, _ := emailTestBroker(t)
		c := &http.Cookie{Name: sessionCookie, Value: b.signSessionFull("a@b.com", 0, walletForAppleSub("sub-1"), "sub-1", time.Now().Add(time.Hour).Unix())}
		a := acctJSON(t, b, c)
		require.Equal(t, "apple", a["provider"])
		require.Equal(t, false, a["operator"])
	})

	t.Run("a github-linked account that signs in by email reports github", func(t *testing.T) {
		b, cap := emailTestBroker(t)
		require.NoError(t, b.db.BindOwner(store.Owner{Pubkey: "pk", GitHubID: 7, Login: "octocat", Email: "o@rogerai.fm", EmailVerifiedAt: now}))
		a := acctJSON(t, b, signInByEmail(t, b, cap, "o@rogerai.fm"))
		require.Equal(t, "github", a["provider"], "the session carries the GitHub identity")
		require.Equal(t, true, a["operator"])
		require.Equal(t, "o@rogerai.fm", a["email"])
		require.Equal(t, true, a["email_verified"])
	})
}
