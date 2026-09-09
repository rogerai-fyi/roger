package agent

import (
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
)

// TestServeCapturesRetryAfterOnlyOn429And503 pins features/routing/upstream_failover.feature
// "the station captures Retry-After only on 429 and 503": the JobResult a station posts carries
// the upstream's Retry-After (normalized to seconds) on a 429/503 and nothing on a 200, on both
// the non-stream (serve) and the streaming (serveStream) paths.
func TestServeCapturesRetryAfterOnlyOn429And503(t *testing.T) {
	cases := []struct {
		status int
		header string
		want   int
	}{
		{429, "7", 7},
		{503, "3", 3},
		{200, "99", 0},
		{429, "", 0},
		{429, "garbage", 0},
		{429, time.Now().Add(45 * time.Second).UTC().Format(http.TimeFormat), 45},
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	for _, tc := range cases {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tc.header != "" {
				w.Header().Set("Retry-After", tc.header)
			}
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"x"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
		}))
		cfg := Config{Upstream: up.URL, NodeID: "n", Model: "m"}
		res := serve(cfg, protocol.ModelOffer{}, priv, &http.Client{}, protocol.Job{ID: "j", Body: json.RawMessage(`{"model":"m"}`)})
		got := res.RetryAfterSec
		if tc.want == 45 && got == 44 { // an HTTP-date is second-granular; the tick may have passed
			got = 45
		}
		require.Equal(t, tc.want, got, "serve status=%d header=%q", tc.status, tc.header)
		wire, _ := json.Marshal(res)
		if tc.want == 0 {
			require.NotContains(t, string(wire), "retry_after_sec", "a zero hint must be omitted on the wire")
		}
		up.Close()
	}

	// Streaming: the result is POSTed to the broker; capture it there.
	for _, tc := range cases[:3] {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tc.header != "" {
				w.Header().Set("Retry-After", tc.header)
			}
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, "data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n")
		}))
		var mu sync.Mutex
		var posted protocol.JobResult
		broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/agent/result") {
				mu.Lock()
				_ = json.NewDecoder(r.Body).Decode(&posted)
				mu.Unlock()
			}
			w.WriteHeader(http.StatusOK)
		}))
		cfg := Config{Upstream: up.URL, Broker: broker.URL, NodeID: "n", Model: "m"}
		serveStream(cfg, protocol.ModelOffer{}, priv, "tok", protocol.Job{ID: "j", Body: json.RawMessage(`{"model":"m","stream":true}`)})
		mu.Lock()
		require.Equal(t, tc.status, posted.Status)
		require.Equal(t, tc.want, posted.RetryAfterSec, "serveStream status=%d header=%q", tc.status, tc.header)
		mu.Unlock()
		broker.Close()
		up.Close()
	}
}
