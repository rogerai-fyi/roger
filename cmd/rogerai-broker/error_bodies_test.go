package main

// error_bodies_test.go: the station-failure bodies a consumer receives (slice-6 review
// 2026-10-06). Every one is a broker envelope carrying the request id,.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func errMeta(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var v struct {
		Error struct {
			Code     string         `json:"code"`
			Type     string         `json:"type"`
			Metadata map[string]any `json:"metadata"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &v))
	require.NotEmpty(t, v.Error.Code)
	return v.Error.Metadata
}

func TestConsumerRejectedBodyIsAnEnvelope(t *testing.T) {
	h := http.Header{}
	h.Set("X-RogerAI-Request-Id", "0123456789abcdef")
	body := consumerRejectedBody(h, http.StatusBadRequest, "st-1", []byte(`{"error":"bad parameter"}`))
	meta := errMeta(t, body)
	require.Equal(t, "0123456789abcdef", meta["request_id"])
	require.Equal(t, "st-1", meta["station"])
	require.Equal(t, `{"error":"bad parameter"}`, meta["raw"])
	var v map[string]map[string]any
	require.NoError(t, json.Unmarshal(body, &v))
	require.Equal(t, "consumer_rejected", v["error"]["code"])
	require.Equal(t, "invalid_request_error", v["error"]["type"])
}

// Founder ruling 2026-10-06: an upstream 401 or 403 never carries the station's raw body.
func TestUpstreamCredentialRefusalCarriesNoRawBody(t *testing.T) {
	secret := []byte(`{"error":"invalid api key key-abcd1234"}`)
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		for name, body := range map[string][]byte{
			"upstream error":    upstreamErrorBody(http.Header{}, status, "st-1", "m", 1, secret),
			"consumer rejected": consumerRejectedBody(http.Header{}, status, "st-1", secret),
		} {
			meta := errMeta(t, body)
			_, has := meta["raw"]
			require.False(t, has, "%d %s: the raw body is left out", status, name)
			require.NotContains(t, string(body), "key-abcd1234", "%d %s", status, name)
		}
	}
	require.Contains(t, errMeta(t, upstreamErrorBody(http.Header{}, http.StatusBadGateway, "st-1", "m", 1, []byte("down"))), "raw",
		"other statuses still carry the raw body for diagnosis")
}
