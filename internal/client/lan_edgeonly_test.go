package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEdgeOnlyRelayNeverUsesATowerItIsHanded pins the relay's own edge-only guard
// (features/lan/edge_only.feature): even a caller that hands the relay a LAN Tower serving the band
// gets a refusal saying edge-only is on, and the Tower is never contacted.
func TestEdgeOnlyRelayNeverUsesATowerItIsHanded(t *testing.T) {
	var hits atomic.Int32
	tower := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("X-Roger-Local", "1")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"from tower"}}]}`))
	}))
	defer tower.Close()
	addr := strings.TrimPrefix(tower.URL, "http://")
	opts := ProxyOptions{Model: "qwen3-8b", SessionKey: "k", LANMode: true, LANEdgeOnly: true,
		EdgePrefer: "local-only", LANTowers: func(string) []string { return []string{addr} }}
	require.False(t, lanServes(opts, "qwen3-8b"), "an edge-only relay counts no Tower as a source")
	srv := httptest.NewServer(ProxyHandler(opts))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"qwen3-8b","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer k")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Zero(t, hits.Load(), "the Tower was contacted under edge-only")
}

func TestLANNothingServesNamesEdgeOnly(t *testing.T) {
	require.Contains(t, lanNothingServes("m", true), "edge-only is on, so LAN Towers are not used")
	require.NotContains(t, lanNothingServes("m", false), "edge-only")
}
