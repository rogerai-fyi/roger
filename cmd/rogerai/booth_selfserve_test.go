package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/localmodel"
)

// Outside LAN mode the booth (the TUI chat, `roger webui`, `roger resume`) took the Edge-then-
// market ladder even while this machine served the band itself: only `roger use` had the own
// serve first. The front-end ladder carries the own serve as its first rung now, so a turn on a
// band `roger model serve` has on the air is answered here, free, and the market is never asked.
// A band nothing here serves goes on to the market as before.
func TestTheBoothTakesTheOwnServeFirstOutsideLANMode(t *testing.T) {
	useTempConfig(t)
	var mu sync.Mutex
	var ownHits int
	own := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer serve-key", r.Header.Get("Authorization"), "the own serve is asked with its runtime key")
		mu.Lock()
		ownHits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","object":"chat.completion","model":"wave-nano-1p45b","choices":[{"index":0,"message":{"role":"assistant","content":"hello from this machine"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":4,"total_tokens":5}}`)
	}))
	t.Cleanup(own.Close)
	h := modelHome()
	require.NoError(t, os.MkdirAll(h.Dir, 0o700))
	rec, _ := json.Marshal(localmodel.Serving{Supervisor: os.Getpid(), Port: 43167, BaseURL: own.URL + "/v1", Key: "serve-key",
		Band: "wave-nano-1p45b", ID: "wave-nano-1p45b-Q4_K_M", File: "wave-nano-1p45b-Q4_K_M.gguf", Quant: "Q4_K_M", Started: time.Now()})
	require.NoError(t, os.WriteFile(filepath.Join(h.Dir, "serving-43167.json"), rec, 0o600))

	var asked []string
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/discover" {
			io.WriteString(w, `{"offers":[]}`)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(broker.Close)
	cfg := config{Broker: broker.URL, User: "u"} // not LAN mode, not enrolled, no Edge preference

	turn := func(band, freq string) (*http.Response, string) {
		opts := client.ProxyOptions{Broker: cfg.Broker, User: cfg.User, Model: band, SessionKey: "k", Freq: freq}
		frontEndLadder(cfg)(&opts) // what liveProxyOpts does for the tuned band
		srv := httptest.NewServer(client.ProxyHandler(opts))
		t.Cleanup(srv.Close)
		body := `{"model":"` + band + `","messages":[{"role":"user","content":"hi"}]}`
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer k")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}

	resp, body := turn("wave-nano-1p45b", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.Contains(t, body, "hello from this machine")
	require.Equal(t, "0", resp.Header.Get("X-RogerAI-Cost"), "a turn on this machine is free and says so")
	mu.Lock()
	require.Empty(t, asked, "the market was never asked for a band this machine serves")
	mu.Unlock()

	// A band nothing here serves: the ladder goes on to the market, as before.
	resp, _ = turn("some-other-model", "")
	require.NotEqual(t, http.StatusOK, resp.StatusCode)
	mu.Lock()
	require.NotEmpty(t, asked, "the market is still the rung for what this machine does not serve")
	asked = nil
	mu.Unlock()

	// A booth tuned to a PRIVATE band is asking for that band's stations, by name or not: the
	// own serve stands aside, as it does for `roger use --freq`.
	resp, _ = turn("wave-nano-1p45b", "7.7")
	require.NotEqual(t, http.StatusOK, resp.StatusCode, "the own serve did not answer a private-band turn")
	mu.Lock()
	require.NotEmpty(t, asked, "a private band is the market's to answer")
	require.Equal(t, 1, ownHits, "the own serve answered the open-band turn only")
	mu.Unlock()
}
