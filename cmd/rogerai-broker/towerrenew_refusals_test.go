package main

// towerrenew_refusals_test.go: the renewal intake's refusal doors, all at zero. Renewal is
// how a Tower keeps existing - and how an attacker would try to BECOME one, so what this
// handler refuses is as load-bearing as what it grants. The uniform "not valid" on a
// judged refusal is deliberate (the reason is logged, never handed to the prober), and
// these tests hold that: no assertion here ever expects the refusal to explain itself.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenewalRefusesItsMalformedAndUnsignedCallers(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := liveEdgeTower(t, b, srv, "renew-op", "203.0.113.44:8444")

	post := func(body []byte) (int, string) {
		resp, err := http.Post(srv.URL+"/tower/renew", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return resp.StatusCode, buf.String()
	}

	t.Run("not JSON", func(t *testing.T) {
		code, body := post([]byte("{nope"))
		require.Equal(t, http.StatusBadRequest, code)
		require.Contains(t, body, "malformed renewal request")
	})
	t.Run("no tower named", func(t *testing.T) {
		code, _ := post([]byte(`{"nonce":"n"}`))
		require.Equal(t, http.StatusBadRequest, code)
	})
	t.Run("unsigned is not the tower", func(t *testing.T) {
		code, body := post([]byte(`{"tower_id":"` + tw.id + `","nonce":"n"}`))
		require.Equal(t, http.StatusForbidden, code)
		require.Contains(t, body, "Tower's own signed request")
	})
	t.Run("signed but undecodable fields", func(t *testing.T) {
		payload, _ := json.Marshal(map[string]any{
			"tower_id": tw.id, "nonce": "n",
			"identity_key": "%%%", "signature": "AAAA", "csr": "AAAA"})
		code, body := tw.call(t, srv, "/tower/renew", payload, nil)
		require.Equal(t, http.StatusBadRequest, code)
		require.Contains(t, body, "malformed renewal request")
	})
	t.Run("a judged refusal is uniform", func(t *testing.T) {
		// Well-formed base64 wrapping garbage: the enroller judges and refuses, and the
		// answer must say nothing about WHY - the reason goes to the log, not the prober.
		payload, _ := json.Marshal(map[string]any{
			"tower_id": tw.id, "nonce": "not-a-real-nonce",
			"identity_key": base64.StdEncoding.EncodeToString([]byte("k")),
			"signature":    base64.StdEncoding.EncodeToString([]byte("s")),
			"csr":          base64.StdEncoding.EncodeToString([]byte("c"))})
		code, body := tw.call(t, srv, "/tower/renew", payload, nil)
		require.Equal(t, http.StatusBadRequest, code)
		require.Contains(t, body, "that renewal is not valid")
		require.NotContains(t, body, "nonce", "the refusal must not name which check failed")
	})
}
