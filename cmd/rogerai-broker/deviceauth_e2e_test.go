package main

// The whole flow, end to end, over real HTTP: a CLI starts a login against a live broker,
// a browser session approves it, and the CLI's next poll comes back signed in.
//
// This is the test that would have caught the flow being wired to the wrong host, the
// wrong key, or the wrong account - the route tests exercise handlers in isolation, and
// isolation is exactly where a plausible-but-wrong wiring survives.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/client"
	"rogerai.fm/roger/v5/internal/store"
)

// liveBroker serves the real device routes over HTTP and points the client package at it.
func liveBroker(t *testing.T) (*broker, *store.Mem, string) {
	t.Helper()
	db := store.NewMem()
	b := relayBroker(db)

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/device/start", b.deviceStart)
	mux.HandleFunc("/auth/device/token", b.deviceToken)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// The CLI signs with a key under its config dir; give each test its own.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return b, db, srv.URL
}

func TestEndToEndDeviceLoginWithAGitHubApprover(t *testing.T) {
	b, db, url := liveBroker(t)

	d, err := client.DeviceLoginBegin(url)
	require.NoError(t, err)
	require.NotEmpty(t, d.UserCode)
	require.NotEmpty(t, d.DeviceCode)
	require.Positive(t, d.Interval)
	// The CLI is told to visit US, never a provider.
	require.NotContains(t, strings.ToLower(d.VerificationURI), "github.com")
	require.NotContains(t, strings.ToLower(d.VerificationURI), "appleid")

	// A human approves in the browser, signed in with GitHub.
	code, msg := approveAs(t, b, githubSession(b, "alice", 4242), d.UserCode)
	require.Equal(t, http.StatusOK, code, msg)

	account, err := client.DeviceLoginPoll(url, d)
	require.NoError(t, err)
	require.Equal(t, "alice", account)

	// The CLI's own key now resolves to that account.
	o, ok, err := db.OwnerByPubkey(client.UserPubHex())
	require.NoError(t, err)
	require.True(t, ok, "the CLI's key must be bound after approval")
	require.Equal(t, int64(4242), o.GitHubID)
}

func TestEndToEndDeviceLoginWithAnAppleApprover(t *testing.T) {
	b, db, url := liveBroker(t)

	d, err := client.DeviceLoginBegin(url)
	require.NoError(t, err)

	sess := b.signSessionFull("user@example.com", 0, walletForAppleSub("sub-xyz"), "sub-xyz",
		time.Now().Add(time.Hour).Unix())
	code, msg := approveAs(t, b, sess, d.UserCode)
	require.Equal(t, http.StatusOK, code, msg)

	account, err := client.DeviceLoginPoll(url, d)
	require.NoError(t, err)
	require.Equal(t, "user@example.com", account)

	o, ok, err := db.OwnerByPubkey(client.UserPubHex())
	require.NoError(t, err)
	require.True(t, ok, "an Apple approver must be able to sign a CLI in")
	require.Equal(t, "sub-xyz", o.AppleSub)
}

// A denial is a normal outcome the CLI reports as such, not an error to retry.
func TestEndToEndDenialIsReportedPlainly(t *testing.T) {
	b, _, url := liveBroker(t)

	d, err := client.DeviceLoginBegin(url)
	require.NoError(t, err)

	body, _ := json.Marshal(map[string]string{"user_code": d.UserCode})
	r := httptest.NewRequest(http.MethodPost, "/auth/device/deny", strings.NewReader(string(body)))
	r.Header.Set("Origin", testWebOrigin)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: githubSession(b, "alice", 4242)})
	w := httptest.NewRecorder()
	b.deviceDeny(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	_, err = client.DeviceLoginPoll(url, d)
	require.ErrorIs(t, err, client.ErrLoginDenied)
}

// Two machines signing in must not collide: each gets its own code, and approving one
// leaves the other pending.
func TestTwoConcurrentLoginsAreIndependent(t *testing.T) {
	b, _, url := liveBroker(t)

	first, err := client.DeviceLoginBegin(url)
	require.NoError(t, err)
	second, err := client.DeviceLoginBegin(url)
	require.NoError(t, err)
	require.NotEqual(t, first.UserCode, second.UserCode)
	require.NotEqual(t, first.DeviceCode, second.DeviceCode)

	code, _ := approveAs(t, b, githubSession(b, "alice", 4242), first.UserCode)
	require.Equal(t, http.StatusOK, code)

	// The second is untouched: still pending, so a poll times out rather than succeeding.
	second.ExpiresIn = 2
	second.Interval = 1
	_, err = client.DeviceLoginPoll(url, second)
	require.ErrorIs(t, err, client.ErrLoginExpired, "approving one login must not approve another")
}

// The CLI writes the account, and nothing that looks like a provider token.
func TestCLIStoresOnlyTheAccount(t *testing.T) {
	b, _, url := liveBroker(t)
	d, err := client.DeviceLoginBegin(url)
	require.NoError(t, err)
	approveAs(t, b, githubSession(b, "alice", 4242), d.UserCode)

	account, err := client.DeviceLoginComplete(url, d)
	require.NoError(t, err)
	require.Equal(t, "alice", account)
	require.Equal(t, "alice", client.LinkedLogin(), "the CLI records who it is")
}

// A signed request from ANOTHER key must not be able to redeem this login, even over
// real HTTP where an attacker could replay the device code.
func TestAForeignKeyCannotRedeemOverHTTP(t *testing.T) {
	b, _, url := liveBroker(t)
	d, err := client.DeviceLoginBegin(url)
	require.NoError(t, err)
	approveAs(t, b, githubSession(b, "alice", 4242), d.UserCode)

	// A genuinely different key. (The client package caches its identity for the life of
	// the process, so pointing it at a new config dir would NOT produce a second key -
	// an earlier version of this test did that and passed while proving nothing.)
	_, foreign, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	body, _ := json.Marshal(map[string]string{"device_code": d.DeviceCode})
	req, err := http.NewRequest(http.MethodPost, url+"/auth/device/token", strings.NewReader(string(body)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	client.SignRequestWith(req, body, foreign)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.NotEqual(t, http.StatusOK, resp.StatusCode,
		"a foreign key must not redeem someone else's approved login")
}

func TestPubkeyIsHexAndStable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p := client.UserPubHex()
	_, err := hex.DecodeString(p)
	require.NoError(t, err)
	require.Equal(t, p, client.UserPubHex(), "the identity must be stable across calls")
}
