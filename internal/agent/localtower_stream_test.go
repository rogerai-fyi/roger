package agent

// The station's streamed-job failure paths, end to end against the REAL plane: each upstream
// misbehaves a different way, and the consumer must get what was sent plus a local error
// (never a hang, never a second reply). The busy-retry loop is checked on the wire separately,
// because the real plane only answers 503 when a consumer is 64 events behind.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/localplane"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/tower"
)

// planeWithStation stands up a real Tower and plane with an admitted client and an attached
// station key, and returns a function that posts one streamed request and reads it whole.
func planeWithStation(t *testing.T) (planeURL string, nodePv ed25519.PrivateKey, ask func() (string, http.Header)) {
	t.Helper()
	dir := t.TempDir()
	_, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	st, err := tower.Open(dir)
	require.NoError(t, err)
	_, clientPv, _ := ed25519.GenerateKey(nil)
	inv, code, err := st.CreateInvitation(idFromPriv(clientPv), time.Hour, 5)
	require.NoError(t, err)
	_, err = st.ConsumeInvitation(inv.ID, code, idFromPriv(clientPv))
	require.NoError(t, err)
	_, nodePv, _ = ed25519.GenerateKey(nil)
	_, err = st.AttachStation("shed", idFromPriv(nodePv), []string{"m"})
	require.NoError(t, err)
	plane := httptest.NewServer(localplane.New(st).Handler())
	t.Cleanup(plane.Close)
	ask = func() (string, http.Header) {
		body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)
		req, _ := http.NewRequest(http.MethodPost, plane.URL+"/v1/chat/completions", bytes.NewReader(body))
		nonce := protocol.NewNonce()
		pub, ts, sig := protocol.SignRequestNonce(clientPv, http.MethodPost, "/v1/chat/completions", body, nonce)
		req.Header.Set(protocol.HeaderPubkey, pub)
		req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
		req.Header.Set(protocol.HeaderSig, sig)
		req.Header.Set(protocol.HeaderNonce, nonce)
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b), resp.Header
	}
	return plane.URL, nodePv, ask
}

func serveStation(t *testing.T, cfg Config, priv ed25519.PrivateKey) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = ServeLocalTower(ctx, cfg, priv, io.Discard) }()
}

func TestStreamedJobUpstreamFailuresEndTheStreamHonestly(t *testing.T) {
	sse := func(w http.ResponseWriter, tokens ...string) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, tok := range tokens {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": tok}}}})
			_, _ = io.WriteString(w, "data: "+string(b)+"\n\n")
			w.(http.Flusher).Flush()
		}
	}
	cases := []struct {
		name     string
		upstream http.HandlerFunc // nil = nothing listening
		osaurus  bool
		want     []string // all must appear in the consumer's stream
		not      []string
	}{
		{name: "the upstream is not listening", want: []string{"a local upstream failure ended the reply", "[DONE]"}},
		{name: "the upstream answers an error status", upstream: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}, want: []string{"a local upstream failure ended the reply"}},
		{name: "the upstream answers whole JSON to a stream", upstream: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"whole reply"}}]}`)
		}, want: []string{`"content":"whole reply"`, "[DONE]"}, not: []string{"error"}},
		{name: "the upstream answers an unreadable body", upstream: func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "<html>oops</html>")
		}, want: []string{"a local upstream failure ended the reply"}, not: []string{"oops"}},
		{name: "the upstream stops without [DONE]", upstream: func(w http.ResponseWriter, r *http.Request) {
			sse(w, "partial")
		}, want: []string{`"content":"partial"`, "a local upstream failure ended the reply"}},
		{name: "an Osaurus upstream is pinned and told not to persist", osaurus: true, upstream: func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			if r.Header.Get("X-Persist") != "false" || !strings.Contains(string(b), `"model":"m"`) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			sse(w, "pinned")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}, want: []string{`"content":"pinned"`, "[DONE]"}, not: []string{"error"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			planeURL, nodePv, ask := planeWithStation(t)
			upURL := "http://127.0.0.1:1/v1/chat/completions" // nothing listens on port 1
			if tc.upstream != nil {
				up := httptest.NewServer(tc.upstream)
				t.Cleanup(up.Close)
				upURL = up.URL
			}
			serveStation(t, Config{Broker: planeURL, Upstream: upURL, Model: "m", Osaurus: tc.osaurus}, nodePv)
			got, hdr := ask()
			require.Contains(t, hdr.Get("Content-Type"), "text/event-stream", got)
			for _, w := range tc.want {
				require.Contains(t, got, w)
			}
			for _, n := range tc.not {
				require.NotContains(t, got, n)
			}
		})
	}
}

// TestChunkPosterResendsWhenTheTowerIsBusy pins the retry loop on the wire: a 503 resends the
// SAME sequence number, a 200 advances it, and any other refusal stops the job.
func TestChunkPosterResendsWhenTheTowerIsBusy(t *testing.T) {
	var calls atomic.Int32
	var seqs []int
	tower := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var c struct {
			Seq int `json:"seq"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &c)
		seqs = append(seqs, c.Seq)
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable) // the consumer is behind
		case 2, 3:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusConflict)
		}
	}))
	defer tower.Close()
	_, priv, _ := ed25519.GenerateKey(nil)
	p := &chunkPoster{ctx: context.Background(), c: tower.Client(), broker: tower.URL, priv: priv, jobID: "j"}
	require.True(t, p.send([]byte("a"), false, ""), "a 503 then a 200 goes on")
	require.True(t, p.send([]byte("b"), false, ""))
	require.False(t, p.send([]byte("c"), false, ""), "a 409 stops the job")
	require.False(t, p.send([]byte("d"), false, ""), "a stopped job sends nothing more")
	require.Equal(t, []int{0, 0, 1, 2}, seqs, "a busy chunk is resent with the same sequence")

	// A Tower that stays busy is given up on rather than retried forever.
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer busy.Close()
	p = &chunkPoster{ctx: context.Background(), c: busy.Client(), broker: busy.URL, priv: priv, jobID: "j"}
	require.False(t, p.send([]byte("x"), false, ""))

	// A cancelled job stops while waiting to resend, and an unreachable Tower stops at once.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p = &chunkPoster{ctx: ctx, c: busy.Client(), broker: busy.URL, priv: priv, jobID: "j"}
	require.False(t, p.send([]byte("x"), false, ""))
	p = &chunkPoster{ctx: context.Background(), c: busy.Client(), broker: "http://127.0.0.1:1", priv: priv, jobID: "j"}
	require.False(t, p.send([]byte("x"), false, ""))
}
