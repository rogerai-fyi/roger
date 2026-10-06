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
