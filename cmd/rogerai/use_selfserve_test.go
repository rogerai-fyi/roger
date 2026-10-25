package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/localmodel"
)

// edge_device_runtime.feature §3: `roger use <model>` prefers the model this machine serves, in
// every mode. Before this, with the own serve on the air, `roger use wave-nano-1p45b` went to the
// market and said "no station on air" (live, 2026-10-24). --market asks the market on purpose.
func TestRogerUsePrefersWhatThisMachineServes(t *testing.T) {
	useTempConfig(t)
	var sawKey string
	own := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawKey = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","object":"chat.completion","model":"wave-nano-1p45b","choices":[{"index":0,"message":{"role":"assistant","content":"hello from this machine"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":4,"total_tokens":5}}`)
	}))
	t.Cleanup(own.Close)
	h := modelHome()
	require.NoError(t, os.MkdirAll(h.Dir, 0o700))
	_, portStr, _ := net.SplitHostPort(strings.TrimPrefix(own.URL, "http://"))
	rec, _ := json.Marshal(localmodel.Serving{Supervisor: os.Getpid(), Port: 43166, BaseURL: own.URL + "/v1", Key: "serve-key",
		Band: "wave-nano-1p45b", ID: "wave-nano-1p45b-Q4_K_M", File: "wave-nano-1p45b-Q4_K_M.gguf", Quant: "Q4_K_M", Started: time.Now()})
	require.NoError(t, os.WriteFile(filepath.Join(h.Dir, "serving-43166.json"), rec, 0o600))
	_ = portStr

	var handler http.Handler
	restore := client.SetUseServe(func(ln net.Listener, hd http.Handler) error { handler = hd; return ln.Close() })
	t.Cleanup(restore)
	cfg := config{Broker: fakeBrokerEmpty(t), User: "u"} // the market has NOTHING on this band

	out, err := captureEdgeStdout(func() error { return cmdUse(cfg, []string{"wave-nano-1p45b", "--yes"}) })
	require.NoError(t, err)
	require.NotNil(t, handler, "a session opened without a market lookup: %s", out)
	require.Contains(t, out, "CHANNEL OPEN wave-nano-1p45b · local · this machine")
	require.Contains(t, out, "free · local")
	require.Contains(t, out, "BASE URL  http://")
	key := ""
	for _, ln := range strings.Split(out, "\n") {
		if f := strings.Fields(ln); len(f) == 3 && f[0] == "API" && f[1] == "KEY" {
			key = f[2]
		}
	}
	require.NotEmpty(t, key)

	// A turn through the session reaches the own serve with its runtime key, and costs nothing.
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions",
		bytes.NewReader([]byte(`{"model":"wave-nano-1p45b","messages":[{"role":"user","content":"hi"}]}`)))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	require.Contains(t, string(b), "hello from this machine")
	require.Equal(t, "Bearer serve-key", sawKey)
	require.Equal(t, "0", resp.Header.Get("X-RogerAI-Cost"))

	// --market asks the market on purpose, and the market has nothing.
	handler = nil
	out, err = captureEdgeStdout(func() error { return cmdUse(cfg, []string{"wave-nano-1p45b", "--yes", "--market"}) })
	require.NoError(t, err)
	require.Nil(t, handler, "no local session was opened: %s", out)
	require.Contains(t, out, "no station on air")

	// A band this machine does not serve goes to the market as before.
	out, err = captureEdgeStdout(func() error { return cmdUse(cfg, []string{"something-else", "--yes"}) })
	require.NoError(t, err)
	require.Nil(t, handler)
	require.Contains(t, out, "no station on air")
}
