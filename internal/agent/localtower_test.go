package agent

// A true end-to-end for the standalone plane: a share node running ServeLocalTower polls a REAL
// localplane, runs jobs against a fake upstream model, and returns answers - and a consumer
// posting to the plane's /v1/chat/completions gets the model's answer, having only pointed at
// the Tower. The Tower dials nobody; the node connects in and polls.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
)

func TestServeLocalTowerServesAConsumerEndToEnd(t *testing.T) {
	// A standalone Tower with an admitted consumer and an attached station.
	dir := t.TempDir()
	_, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	st, err := tower.Open(dir)
	require.NoError(t, err)

	consumerPub, consumerPriv, _ := ed25519.GenerateKey(rand.Reader)
	consumerID := protocol.UserIDFromPubkey(hexOf(consumerPub))
	inv, code, err := st.CreateInvitation(consumerID, time.Hour, 5)
	require.NoError(t, err)
	_, err = st.ConsumeInvitation(inv.ID, code, consumerID)
	require.NoError(t, err)

	nodePub, nodePriv, _ := ed25519.GenerateKey(rand.Reader)
	nodeID := protocol.UserIDFromPubkey(hexOf(nodePub))
	_, err = st.AttachStation("st-node", nodeID, []string{"local-llama"})
	require.NoError(t, err)

	// The fake upstream model: it must SEE stream:false (the node forces it) and answers with a
	// verbatim, non-streaming chat completion.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		require.Contains(t, string(body), `"stream":false`, "the node must force non-streaming")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"answer from the plant model"}}]}`)
	}))
	defer upstream.Close()

	// The real plane, on a real HTTP listener so the node can poll it over http.
	plane := httptest.NewServer(localplane.New(st).Handler())
	defer plane.Close()

	// The share node polls the plane and serves.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = ServeLocalTower(ctx, Config{Broker: plane.URL, Upstream: upstream.URL, Model: "local-llama"}, nodePriv, io.Discard)
	}()

	// The consumer posts a prompt and gets the model's answer, having only pointed at the Tower.
	prompt := []byte(`{"model":"local-llama","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	req, _ := http.NewRequest(http.MethodPost, plane.URL+"/v1/chat/completions", bytes.NewReader(prompt))
	pubHex, ts, sig := protocol.SignRequest(consumerPriv, http.MethodPost, "/v1/chat/completions", prompt)
	req.Header.Set(protocol.HeaderPubkey, pubHex)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sig)

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "0", resp.Header.Get("X-Roger-Cost"), "the answer is free and locally accounted")
	ans, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(ans), "answer from the plant model", "the consumer gets the local model's answer through the whole loop")

	// A local receipt was persisted for the served request.
	receipts, err := st.Receipts(0)
	require.NoError(t, err)
	require.Len(t, receipts, 1)
	require.Equal(t, "st-node", receipts[0].StationID)
}

func hexOf(b []byte) string { return hex.EncodeToString(b) }

func TestRunLocalJobReturnsAReadableErrorWhenTheModelFails(t *testing.T) {
	// A dead upstream: the node returns an OpenAI-shaped error, not an empty answer.
	ans := runLocalJob(&http.Client{Timeout: time.Second}, Config{Upstream: "http://127.0.0.1:1"}, []byte(`{"model":"m"}`))
	require.Contains(t, string(ans), "local_station_error")
	require.Contains(t, string(ans), "did not answer")

	// An unbuildable upstream URL is also a readable error, not a panic.
	ans2 := runLocalJob(&http.Client{Timeout: time.Second}, Config{Upstream: "://bad"}, []byte(`{"model":"m"}`))
	require.Contains(t, string(ans2), "local_station_error")
}

func TestPollLocalJobHandlesNonOKAndNoContent(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	// 204: no work, no error.
	no := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer no.Close()
	_, got, err := pollLocalJob(context.Background(), no.Client(), no.URL, priv)
	require.NoError(t, err)
	require.False(t, got)

	// 500: treated as no job, with an error.
	boom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer boom.Close()
	_, got, err = pollLocalJob(context.Background(), boom.Client(), boom.URL, priv)
	require.Error(t, err)
	require.False(t, got)

	// 200 with a job missing its id is rejected.
	noid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"model":"m"}`) }))
	defer noid.Close()
	_, got, err = pollLocalJob(context.Background(), noid.Client(), noid.URL, priv)
	require.Error(t, err)
	require.False(t, got)
}

func TestUnstreamLocalForcesNonStreamingAndToleratesGarbage(t *testing.T) {
	out := unstreamLocal([]byte(`{"model":"m","stream":true,"stream_options":{"include_usage":true}}`))
	require.Contains(t, string(out), `"stream":false`)
	require.NotContains(t, string(out), "stream_options")
	// Non-JSON is returned untouched.
	require.Equal(t, []byte("not json"), unstreamLocal([]byte("not json")))
}

func TestCompleteLocalJobIsBestEffort(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	// A dead broker must not panic - completion is best-effort.
	require.NotPanics(t, func() {
		completeLocalJob(&http.Client{Timeout: time.Second}, "http://127.0.0.1:1", priv, "job-1", []byte(`{}`))
	})
}

func TestServeLocalTowerStopsOnContextCancel(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	// A plane that always says "no work": the loop should keep polling and then stop on cancel.
	quiet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer quiet.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := ServeLocalTower(ctx, Config{Broker: quiet.URL, Upstream: "http://127.0.0.1:1"}, priv, io.Discard)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
