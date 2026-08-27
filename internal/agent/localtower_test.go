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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
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

func TestRunLocalJobWrapsANonJSONUpstreamReply(t *testing.T) {
	// An upstream that returns plain text (an error page) must not produce an unmarshalable
	// answer that strands the consumer - the node wraps it as valid JSON.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "502 Bad Gateway (not json)")
	}))
	defer bad.Close()
	ans := runLocalJob(&http.Client{Timeout: time.Second}, Config{Upstream: bad.URL}, []byte(`{"model":"m"}`))
	require.True(t, json.Valid(ans), "the answer the node returns is always valid JSON")
	require.Contains(t, string(ans), "unreadable")
}

func TestUnstreamLocalToleratesNullBody(t *testing.T) {
	require.NotPanics(t, func() {
		out := unstreamLocal([]byte("null"))
		require.Equal(t, []byte("null"), out, "a literal null is left untouched, not dereferenced")
	})
}

func TestServeLocalTowerAnnouncesItsNodeIDForAttach(t *testing.T) {
	// The operator needs the node's tower client id to run `roger-tower attach --key <id>`, and
	// the node is the only place that key hash is derivable from - so serving MUST print it, with
	// the exact command that consumes it. Without this the documented attach flow cannot be followed.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	nodeID := protocol.UserIDFromPubkey(hexOf(pub))
	quiet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer quiet.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	_ = ServeLocalTower(ctx, Config{Broker: quiet.URL, Upstream: "http://127.0.0.1:1"}, priv, &out)
	s := out.String()
	require.Contains(t, s, nodeID, "the node prints its own tower client id so the operator can attach it")
	require.Contains(t, s, "roger-tower attach", "and prints the exact command that consumes it")
}

func TestIDFromPrivFollowsTheCanonicalRule(t *testing.T) {
	// The id a node advertises must be the SAME string the Tower admits - both go through
	// protocol.UserIDFromPubkey over the hex pubkey, or a correctly-attached node is refused.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	require.Equal(t, protocol.UserIDFromPubkey(hexOf(pub)), idFromPriv(priv))
}

func TestNodeIDIsStableAndCanonical(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME steers os.UserConfigDir only on Linux; elsewhere this would touch the real node.key")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // isolate the node key file from the real config dir
	id1 := NodeID()
	require.Equal(t, "u_", id1[:2], "the node id is a tower client id")
	require.Equal(t, id1, NodeID(), "it is stable across calls - the same persisted node key")
	require.Equal(t, idFromPriv(NodeKey()), id1, "and it is exactly what a share will sign with")
}
