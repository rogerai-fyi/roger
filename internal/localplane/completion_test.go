package localplane

// Contract: features/tower/standalone_consumer_plane.feature
//
// The loop the first slice proves: an admitted client gets a completion through a local
// station, the station is reached WITHOUT the Tower dialing out (the station polls the Tower),
// a free local receipt is persisted, and an Open Market model is refused after auth with
// nothing dialed.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
)

// signedBody builds a POST signed by priv, carrying body both as the request body and as the
// bytes the signature covers - exactly as roger sends a prompt.
func signedBody(t *testing.T, priv ed25519.PrivateKey, method, path string, body []byte) *http.Request {
	t.Helper()
	pubHex, ts, sig := protocol.SignRequest(priv, method, path, body)
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set(protocol.HeaderPubkey, pubHex)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sig)
	return req
}

// attachKeyedStation generates a station key, attaches it under its canonical key hash, and
// returns the private key and station id - so the station can authenticate its own polls.
func attachKeyedStation(t *testing.T, st *tower.State, id string, models []string) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	keyHash := protocol.UserIDFromPubkey(hexPub(pub))
	_, err = st.AttachStation(id, keyHash, models)
	require.NoError(t, err)
	return priv
}

func TestFullLocalCompletionLoop(t *testing.T) {
	st := standaloneState(t)
	client := admitClient(t, st)
	station := attachKeyedStation(t, st, "st-llama", []string{"llama-8b"})
	srv := New(st)

	prompt := []byte(`{"model":"llama-8b","messages":[{"role":"user","content":"hi"}]}`)

	// The consumer posts and blocks until a station serves it.
	clientDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, signedBody(t, client, http.MethodPost, "/v1/chat/completions", prompt))
		clientDone <- rec
	}()

	// The station polls (blocks until the job arrives), runs it, and returns the answer.
	pollRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(pollRec, signedBody(t, station, http.MethodPost, "/local/poll", nil))
	require.Equal(t, http.StatusOK, pollRec.Code)
	var jobMsg struct {
		JobID   string          `json:"job_id"`
		Model   string          `json:"model"`
		Request json.RawMessage `json:"request"`
	}
	require.NoError(t, json.Unmarshal(pollRec.Body.Bytes(), &jobMsg))
	require.Equal(t, "llama-8b", jobMsg.Model)
	require.JSONEq(t, string(prompt), string(jobMsg.Request), "the station gets the consumer's request verbatim")

	answer := []byte(`{"choices":[{"message":{"content":"hello from the plant"}}]}`)
	completeBody, _ := json.Marshal(map[string]any{"job_id": jobMsg.JobID, "answer": json.RawMessage(answer)})
	completeRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(completeRec, signedBody(t, station, http.MethodPost, "/local/complete", completeBody))
	require.Equal(t, http.StatusOK, completeRec.Code)

	// The consumer gets the station's answer, free and local.
	select {
	case rec := <-clientDone:
		require.Equal(t, http.StatusOK, rec.Code)
		require.JSONEq(t, string(answer), rec.Body.String(), "the answer comes from the local station, verbatim")
		require.Equal(t, "0", rec.Header().Get("X-Roger-Cost"), "a free cost header, never a billing shape")
	case <-time.After(3 * time.Second):
		t.Fatal("the consumer never received the local station's answer")
	}

	// A local receipt was persisted for the served request.
	receipts, err := st.Receipts(0)
	require.NoError(t, err)
	require.Len(t, receipts, 1)
	require.Equal(t, "st-llama", receipts[0].StationID)
	require.Equal(t, "llama-8b", receipts[0].Model)
	require.Zero(t, receipts[0].Cost, "the receipt is free and locally accounted")
}

func TestOpenMarketModelIsRefusedAfterAuthWithNothingDialed(t *testing.T) {
	st := standaloneState(t)
	client := admitClient(t, st)
	attachKeyedStation(t, st, "st-1", []string{"llama-8b"}) // this Tower hosts only llama
	srv := New(st)

	// A model only the Open Market sells: refused, named to the already-authenticated client,
	// and nothing dialed (the source-scan gate proves no code here could dial).
	prompt := []byte(`{"model":"gpt-4o","messages":[]}`)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, signedBody(t, client, http.MethodPost, "/v1/chat/completions", prompt))
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "not offered by any local station")
	require.Contains(t, rec.Body.String(), "gpt-4o", "the model is named to the authenticated client")
}

func TestChatCompletionsRefusesUnauthedAndNonAdmitted(t *testing.T) {
	st := standaloneState(t)
	admitClient(t, st) // establishes the operator so a station can attach
	attachKeyedStation(t, st, "st-1", []string{"m"})
	srv := New(st)
	_, unadmitted, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	prompt := []byte(`{"model":"m"}`)
	// Unsigned and a valid-but-unadmitted signature both get the uniform 401.
	unsigned := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(prompt))
	rec1 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec1, unsigned)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, signedBody(t, unadmitted, http.MethodPost, "/v1/chat/completions", prompt))
	require.Equal(t, http.StatusUnauthorized, rec1.Code)
	require.Equal(t, http.StatusUnauthorized, rec2.Code)
	require.Equal(t, rec1.Body.String(), rec2.Body.String(), "both refusals are byte-identical")
}

func TestStationEndpointsRefuseNonStations(t *testing.T) {
	st := standaloneState(t)
	client := admitClient(t, st) // an admitted CLIENT is not an attached STATION
	srv := New(st)
	for _, path := range []string{"/local/poll", "/local/complete"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, signedBody(t, client, http.MethodPost, path, []byte(`{}`)))
		require.Equal(t, http.StatusUnauthorized, rec.Code, "%s must refuse a non-station", path)
	}
}

func TestCompletionTimesOutWhenNoStationServes(t *testing.T) {
	st := standaloneState(t)
	client := admitClient(t, st)
	attachKeyedStation(t, st, "st-1", []string{"m"})
	srv := New(st)
	srv.completionTimeout = 150 * time.Millisecond // no station will poll

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, signedBody(t, client, http.MethodPost, "/v1/chat/completions", []byte(`{"model":"m"}`)))
	require.Equal(t, http.StatusGatewayTimeout, rec.Code)
	require.Contains(t, rec.Body.String(), "in time")
}

func TestEndpointMethodAndBodyGuards(t *testing.T) {
	st := standaloneState(t)
	client := admitClient(t, st)
	station := attachKeyedStation(t, st, "st-1", []string{"m"})
	srv := New(st)
	srv.pollTimeout = 80 * time.Millisecond

	serve := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	// Wrong method on each endpoint, from an AUTHENTICATED caller, is a 405 (not a leak).
	require.Equal(t, http.StatusMethodNotAllowed,
		serve(signedBody(t, client, http.MethodGet, "/v1/chat/completions", nil)).Code)
	require.Equal(t, http.StatusMethodNotAllowed,
		serve(signedBody(t, station, http.MethodGet, "/local/poll", nil)).Code)
	require.Equal(t, http.StatusMethodNotAllowed,
		serve(signedBody(t, station, http.MethodGet, "/local/complete", nil)).Code)

	// A completion with no model is a 400.
	require.Equal(t, http.StatusBadRequest,
		serve(signedBody(t, client, http.MethodPost, "/v1/chat/completions", []byte(`{}`))).Code)

	// A complete with no job_id is a 400.
	require.Equal(t, http.StatusBadRequest,
		serve(signedBody(t, station, http.MethodPost, "/local/complete", []byte(`{"answer":{}}`))).Code)

	// A station polling with no work waiting gets 204 and polls again.
	require.Equal(t, http.StatusNoContent,
		serve(signedBody(t, station, http.MethodPost, "/local/poll", nil)).Code)

	// A completion for an unknown job is accepted and reports it delivered nothing.
	unknown, _ := json.Marshal(map[string]any{"job_id": "does-not-exist", "answer": json.RawMessage(`{}`)})
	rec := serve(signedBody(t, station, http.MethodPost, "/local/complete", unknown))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"delivered":false`)
}

func TestABadTimestampFailsAuthUniformly(t *testing.T) {
	st := standaloneState(t)
	client := admitClient(t, st)
	srv := New(st)
	req := signedBody(t, client, http.MethodGet, "/discover", nil)
	req.Header.Set(protocol.HeaderTS, "not-a-number") // parseTS -> 0 -> outside the freshness window
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestClientDisconnectAbandonsTheJob(t *testing.T) {
	st := standaloneState(t)
	client := admitClient(t, st)
	attachKeyedStation(t, st, "st-1", []string{"m"})
	srv := New(st)

	// A request whose context is already cancelled: the handler returns promptly and the job
	// is abandoned rather than pinned for the full completion timeout.
	req := signedBody(t, client, http.MethodPost, "/v1/chat/completions", []byte(`{"model":"m"}`))
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)

	done := make(chan struct{})
	go func() {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a disconnected client's handler did not return promptly")
	}
	// Nothing leaked into the queue.
	srv.q.mu.Lock()
	pending, inflight := len(srv.q.pending), len(srv.q.inflight)
	srv.q.mu.Unlock()
	require.Zero(t, pending)
	require.Zero(t, inflight)
}

func TestCompleteRequiresANonEmptyAnswer(t *testing.T) {
	st := standaloneState(t)
	admitClient(t, st)
	station := attachKeyedStation(t, st, "st-1", []string{"m"})
	srv := New(st)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, signedBody(t, station, http.MethodPost, "/local/complete", []byte(`{"job_id":"x"}`)))
	require.Equal(t, http.StatusBadRequest, rec.Code, "a completion with no answer is refused, not delivered empty")
}

func TestDrainTakesABufferedResultOnce(t *testing.T) {
	j := &job{result: make(chan jobResult, 1)}
	j.result <- jobResult{answer: []byte("hi"), stationID: "st-x"}
	res, ok := drain(j)
	require.True(t, ok)
	require.Equal(t, "st-x", res.stationID)
	_, ok2 := drain(j)
	require.False(t, ok2, "a second drain finds nothing")
}
