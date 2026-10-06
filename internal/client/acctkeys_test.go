package client

// acctkeys_test.go: the account-key client (signed /account/keys calls, the key table) and the
// proxy relaying with an account key instead of the device signature. The end-to-end CLI path
// against a real broker is pinned by cmd/rogerai-broker/key_cli_bdd_test.go.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKeysCalls(t *testing.T) {
	var got []string
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path)
		bodies = append(bodies, b)
		require.NotEmpty(t, r.Header.Get("X-Roger-Sig"), "every key call is signed")
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"keys":[{"id":"key_a","name":"ci","reset":"weekly","usage_weekly":1.5,"limit_usd":5}]}`))
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"key_b","name":"n","secret":"rog-key_abc"}`))
		case r.Method == http.MethodPatch:
			_, _ = w.Write([]byte(`{"id":"key_b","disabled":true}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	keys, err := KeysList(srv.URL)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.Equal(t, 1.5, keys[0].windowUsage())

	k, secret, err := KeysMint(srv.URL, map[string]any{"name": "n"})
	require.NoError(t, err)
	require.Equal(t, "key_b", k.ID)
	require.Equal(t, "rog-key_abc", secret)

	k, err = KeysSet(srv.URL, "key_b", map[string]any{"disabled": true})
	require.NoError(t, err)
	require.Equal(t, "disabled", k.State())

	require.NoError(t, KeysRevoke(srv.URL, "key_b"))
	require.Equal(t, []string{"GET /account/keys", "POST /account/keys", "PATCH /account/keys/key_b", "DELETE /account/keys/key_b"}, got)
	require.JSONEq(t, `{"name":"n"}`, string(bodies[1]))
	require.JSONEq(t, `{"disabled":true}`, string(bodies[2]))
}

func TestKeysCallErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{http.StatusUnauthorized, `{"error":{"message":"log in to manage keys"}}`, "log in first - run `roger login`"},
		{http.StatusBadRequest, `{"error":{"code":"invalid_key_field","message":"limit_usd must be a number >= 0"}}`, "limit_usd must be a number >= 0"},
		{http.StatusBadGateway, `upstream gone`, "broker returned status 502"},
	} {
		_, err := KeysList(jsonServer(t, tc.status, tc.body, nil))
		require.ErrorContains(t, err, tc.want)
	}
	_, err := KeysList("http://127.0.0.1:1")
	require.ErrorIs(t, err, ErrBrokerUnreachable)
}

func TestWriteKeysTable(t *testing.T) {
	exp := "2027-01-01T00:00:00Z"
	var buf bytes.Buffer
	WriteKeysTable(&buf, []AccountKey{
		{ID: "key_1", Name: "ci", Hint: "...abcd", LimitUSD: 5, Reset: "daily", UsageDaily: 1.25},
		{ID: "key_2", Name: "bot", Hint: "...ef01", Reset: "none", Usage: 3, ExpiresAt: &exp, Expired: true},
		{ID: "key_3", Reset: "monthly", UsageMonthly: 2, Disabled: true},
		{ID: "key_4", Reset: "weekly", UsageWeekly: 4},
	})
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 5)
	require.Regexp(t, `^ID\s+NAME\s+HINT\s+LIMIT\s+USED\s+RESETS\s+EXPIRES\s+STATE$`, lines[0])
	require.Regexp(t, `^key_1\s+ci\s+\.\.\.abcd\s+\$5\.00\s+\$1\.25\s+daily\s+never\s+active$`, lines[1])
	require.Regexp(t, `^key_2\s+bot\s+\.\.\.ef01\s+unlimited\s+\$3\.00\s+none\s+2027-01-01T00:00:00Z\s+expired$`, lines[2])
	require.Contains(t, lines[3], "$2.00")
	require.Contains(t, lines[3], "disabled")
	require.Contains(t, lines[4], "$4.00")
}

func TestIsAccountKey(t *testing.T) {
	require.True(t, IsAccountKey("rog-key_x"))
	for _, s := range []string{"", "rog-key_", "rog-grant_x", "sk-x"} {
		require.False(t, IsAccountKey(s), s)
	}
}

// TestProxyRelaysWithKeyBearer: with KeyBearer set, the relay carries the key as its bearer
// and no device signature (or legacy X-Roger-User); without it, the relay is signed.
func TestProxyRelaysWithKeyBearer(t *testing.T) {
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{}})
	}))
	defer srv.Close()
	send := func(o ProxyOptions) {
		rec := httptest.NewRecorder()
		ProxyHandler(o).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`)))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	send(ProxyOptions{Broker: srv.URL, User: "u", KeyBearer: "rog-key_secret"})
	require.Equal(t, "Bearer rog-key_secret", hdr.Get("Authorization"))
	for _, h := range []string{"X-Roger-Sig", "X-Roger-Pubkey", "X-Roger-User"} {
		require.Empty(t, hdr.Get(h), h)
	}
	send(ProxyOptions{Broker: srv.URL, User: "u"})
	require.Empty(t, hdr.Get("Authorization"))
	require.NotEmpty(t, hdr.Get("X-Roger-Sig"))
}
