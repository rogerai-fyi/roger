package main

// toweraudit_sealed_test.go covers the SEALED half of the audit-transcript intake, which
// had never run: a hub node's audit answer arrives as an envelope sealed to Core's key, and
// the handler must open it - or refuse it - before anything else about the audit happens.
//
// AN OBSERVATION THESE TESTS RECORD RATHER THAN FIX: the sealed unpack runs BEFORE the
// tower-signature check, so every refusal below is reachable UNSIGNED. That is a small
// pre-auth crypto cost (an envelope.OpenWith per anonymous POST, body-capped) and a mild
// oracle ("does not open" vs "not an envelope"), and moving the auth first would close
// both. Until then, these tests deliberately post unsigned - if someone reorders the
// checks, the 403s that replace these 400s will fail here and the improvement gets noticed.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/towercore/envelope"
)

func postSealedTranscript(t *testing.T, srvURL string, payload map[string]any) (int, string) {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	resp, err := http.Post(srvURL+"/tower/audit/transcript", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}

func TestASealedAuditAnswerIsRefusedUnlessItOpensForThisAttempt(t *testing.T) {
	b, srv := towerTestBroker(t)
	corePub, err := envelope.PublicKeyOf(b.tower.envelopeKey)
	require.NoError(t, err)

	t.Run("not base64", func(t *testing.T) {
		code, body := postSealedTranscript(t, srv.URL, map[string]any{
			"tower_id": "tw-1", "attempt_id": "att-1", "available": true,
			"sealed_bundle": "%%%"})
		require.Equal(t, http.StatusBadRequest, code)
		require.Contains(t, body, "not valid base64")
	})
	t.Run("not an envelope", func(t *testing.T) {
		code, body := postSealedTranscript(t, srv.URL, map[string]any{
			"tower_id": "tw-1", "attempt_id": "att-1", "available": true,
			"sealed_bundle": base64.StdEncoding.EncodeToString([]byte("just bytes"))})
		require.Equal(t, http.StatusBadRequest, code)
		require.Contains(t, body, "not a sealed envelope")
	})
	t.Run("sealed for another attempt does not open", func(t *testing.T) {
		// Sealed to the RIGHT key but with another attempt's AAD: the envelope is genuine
		// and still must not open here, or a bundle could be replayed across audits.
		sealed, serr := envelope.SealTo(corePub, []byte(`{"transcript":"dA=="}`), "att-OTHER")
		require.NoError(t, serr)
		raw, merr := sealed.Marshal()
		require.NoError(t, merr)
		code, body := postSealedTranscript(t, srv.URL, map[string]any{
			"tower_id": "tw-1", "attempt_id": "att-1", "available": true,
			"sealed_bundle": base64.StdEncoding.EncodeToString(raw)})
		require.Equal(t, http.StatusBadRequest, code)
		require.Contains(t, body, "does not open for this attempt")
	})
	t.Run("opens but is not a bundle", func(t *testing.T) {
		sealed, serr := envelope.SealTo(corePub, []byte("not json"), "att-1")
		require.NoError(t, serr)
		raw, merr := sealed.Marshal()
		require.NoError(t, merr)
		code, body := postSealedTranscript(t, srv.URL, map[string]any{
			"tower_id": "tw-1", "attempt_id": "att-1", "available": true,
			"sealed_bundle": base64.StdEncoding.EncodeToString(raw)})
		require.Equal(t, http.StatusBadRequest, code)
		require.Contains(t, body, "not a transcript bundle")
	})
	t.Run("a well-formed bundle from a non-tower still hits the signature wall", func(t *testing.T) {
		// The unpack succeeds; the very next check is the tower's own signature, and an
		// anonymous caller must die there - this is the boundary the observation above is
		// about, pinned from its far side.
		sealed, serr := envelope.SealTo(corePub, []byte(`{"transcript":"dA==","request":"","response":""}`), "att-1")
		require.NoError(t, serr)
		raw, merr := sealed.Marshal()
		require.NoError(t, merr)
		code, body := postSealedTranscript(t, srv.URL, map[string]any{
			"tower_id": "tw-1", "attempt_id": "att-1", "available": true,
			"sealed_bundle": base64.StdEncoding.EncodeToString(raw)})
		require.Equal(t, http.StatusForbidden, code)
		require.Contains(t, body, "Tower's own signed request")
	})
}
