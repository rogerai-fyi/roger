package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// edge_device_runtime.feature §3: outside LAN mode the own serve is the first rung; when it does
// not answer the turn goes on, and a session pinned to this machine with no Edge says so in this
// machine's words rather than an Edge's.
func TestOwnServeIsTheFirstRungAndItsFailureIsNamed(t *testing.T) {
	var hits atomic.Int32
	self := lanUpstream(t, "self", &hits, nil)
	body := []byte(`{"model":"wave-nano-1p45b","messages":[{"role":"user","content":"hi"}]}`)

	rec := httptest.NewRecorder()
	served, failed := relaySelfFirst(context.Background(), rec, ProxyOptions{
		LANSelf: func(string) (string, string) { return self.URL + "/v1/chat/completions", "k" }}, "wave-nano-1p45b", body, nil)
	require.True(t, served)
	require.Empty(t, failed)
	require.Contains(t, rec.Body.String(), "from self")
	require.Equal(t, "0", rec.Header().Get("X-RogerAI-Cost"))

	served, failed = relaySelfFirst(context.Background(), httptest.NewRecorder(), ProxyOptions{
		LANSelf: func(string) (string, string) { return "", "" }}, "wave-nano-1p45b", body, nil)
	require.False(t, served)
	require.Empty(t, failed, "nothing served here is not a failure")

	served, failed = relaySelfFirst(context.Background(), httptest.NewRecorder(), ProxyOptions{
		LANSelf: func(string) (string, string) { return "http://127.0.0.1:1/v1/chat/completions", "k" }}, "wave-nano-1p45b", body, nil)
	require.False(t, served)
	require.Contains(t, failed, "this machine:")

	msg := selfDownMessage("wave-nano-1p45b", failed)
	require.Contains(t, msg, "this machine's own serve of wave-nano-1p45b did not answer")
	require.Contains(t, msg, "roger model status")
	require.Contains(t, msg, "--market")
	require.NotContains(t, msg, "Edge")

	// Through the ladder: a session pinned to this machine (local-only, no Edge) is refused in
	// those words, with status 503, and no broker is reached.
	rec = httptest.NewRecorder()
	relayWithFailover(context.Background(), rec, ProxyOptions{Broker: "http://127.0.0.1:1", EdgePrefer: "local-only",
		LANSelf: func(string) (string, string) { return "http://127.0.0.1:1/v1/chat/completions", "k" }},
		Criteria{Model: "wave-nano-1p45b"}, body, http.DefaultClient, failoverPolicy{}, nil, nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "this machine's own serve of wave-nano-1p45b did not answer")
}
