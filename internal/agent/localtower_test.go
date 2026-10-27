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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/localplane"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/tower"
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

	// The fake upstream model: a non-stream request reaches it as stream:false, and it answers with a
	// verbatim, non-streaming chat completion. (A streamed request keeps its flag: superseded
	// 2026-10-07 by features/tower/standalone_streaming.feature, covered in
	// localtower_stream_bdd_test.go.)
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
	prompt := []byte(`{"model":"local-llama","messages":[{"role":"user","content":"hi"}]}`)
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
	_, got, err := pollLocalJob(context.Background(), no.Client(), no.URL, priv, Config{})
	require.NoError(t, err)
	require.False(t, got)

	// 500: treated as no job, with an error.
	boom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer boom.Close()
	_, got, err = pollLocalJob(context.Background(), boom.Client(), boom.URL, priv, Config{})
	require.Error(t, err)
	require.False(t, got)

	// 200 with a job missing its id is rejected.
	noid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"model":"m"}`) }))
	defer noid.Close()
	_, got, err = pollLocalJob(context.Background(), noid.Client(), noid.URL, priv, Config{})
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

func TestServeLocalTowerAsksNobodyToCopyItsKey(t *testing.T) {
	// features/join/surfaces.feature: no screen asks the user to carry a key between machines. A
	// station a Tower already attached serves without printing its key (the invite is the way in;
	// an unattached station is told so, below).
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
	require.NotContains(t, s, nodeID, "an attached station does not print its key")
	require.NotContains(t, s, "--key", "nor ask anyone to copy it")
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

// A Tower that has not attached this station answers every poll 401. The node says so once,
// naming the Tower and the exact attach command, and backs off instead of spinning.
func TestServeLocalTowerReportsAnUnattachedStationAndBacksOff(t *testing.T) {
	prev := localPollBackoffBase
	localPollBackoffBase = 20 * time.Millisecond
	t.Cleanup(func() { localPollBackoffBase = prev })
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	var mu sync.Mutex
	var at []time.Time
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		at = append(at, time.Now())
		mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
	}))
	defer refusing.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	_ = ServeLocalTower(ctx, Config{Broker: refusing.URL, Upstream: "http://127.0.0.1:1", Model: "m"}, priv, &out)

	s := out.String()
	require.Contains(t, s, "the Tower at "+refusing.URL+" has not attached this station")
	require.Contains(t, s, "ask its owner for a station invite (roger tower invite --station), then run here: roger join <invite>",
		"the unattached station is told the simple way in")
	require.NotContains(t, s, "--key", "it asks nobody to copy a key")
	require.Contains(t, s, "a Tower older than invites attaches this station's key: "+protocol.UserIDFromPubkey(hexOf(pub)))
	require.Equal(t, 1, strings.Count(s, "has not attached this station"), "said once, not on every poll")

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(at), 3)
	require.Less(t, len(at), 10, "polls back off: a fixed 20ms spin would poll about 35 times")
	first, last := at[1].Sub(at[0]), at[len(at)-1].Sub(at[len(at)-2])
	require.Greater(t, last, 2*first, "each retry waits longer than the one before")
}

func TestLocalPollBackoffDoublesToItsCap(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want time.Duration
	}{{1, 500 * time.Millisecond}, {2, time.Second}, {3, 2 * time.Second}, {7, 30 * time.Second}, {50, 30 * time.Second}} {
		require.Equal(t, tc.want, localPollBackoff(tc.n), "failure %d", tc.n)
	}
}

// StartLAN serves every LAN Tower as a standalone station and registers nowhere; Stop ends it.
func TestStartLANPollsEveryTowerUntilStopped(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	var mu sync.Mutex
	polled := map[string]int{}
	tower := func(name string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			polled[name+r.URL.Path]++
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	a, b := tower("a"), tower("b")
	sess := StartLAN(Config{Model: "m", Upstream: "http://127.0.0.1:1"}, []string{a.URL, b.URL}, priv, io.Discard)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return polled["a/local/poll"] > 0 && polled["b/local/poll"] > 0
	}, 3*time.Second, 20*time.Millisecond, "both Towers are polled")
	require.Equal(t, LinkOnAir, sess.Link(), "a LAN share is on air the moment it serves")
	sess.Stop()
	mu.Lock()
	n := polled["a/local/poll"]
	mu.Unlock()
	time.Sleep(700 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	require.LessOrEqual(t, polled["a/local/poll"], n+1, "Stop ends the polling")
	for k := range polled {
		require.True(t, strings.HasSuffix(k, "/local/poll"), "only the local plane is asked: %s", k)
	}
}
