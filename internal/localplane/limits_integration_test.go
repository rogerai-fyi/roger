package localplane

// Contract: features/tower/standalone_consumer_plane.feature - the replay defense and the
// resource-safety scenarios, exercised through the real handlers.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/protocol"
)

func TestPerClientRateLimitReturns429(t *testing.T) {
	st := standaloneState(t)
	client := admitClient(t, st)
	attachKeyedStation(t, st, "st-1", []string{"m"})
	srv := New(st)
	srv.rl = newRateLimiter(time.Now, 0, 2) // burst 2, no refill within the test

	// Distinct bodies (distinct signatures, so not replays) for an UNOFFERED model: each
	// passes auth (spending a token) and gets a fast 404, until the bucket empties -> 429.
	codes := []int{}
	for i := 0; i < 4; i++ {
		body := []byte(`{"model":"nope","n":` + strconv.Itoa(i) + `}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		pubHex, ts, sig := protocol.SignRequest(client, http.MethodPost, "/v1/chat/completions", body)
		req.Header.Set(protocol.HeaderPubkey, pubHex)
		req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
		req.Header.Set(protocol.HeaderSig, sig)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	require.Equal(t, http.StatusNotFound, codes[0], "first request within burst passes auth, model unoffered -> 404")
	require.Equal(t, http.StatusNotFound, codes[1])
	require.Equal(t, http.StatusTooManyRequests, codes[2], "past the burst, the client is throttled")
	require.Equal(t, http.StatusTooManyRequests, codes[3])
}

func TestConcurrencyIsBounded(t *testing.T) {
	st := standaloneState(t)
	client := admitClient(t, st)
	attachKeyedStation(t, st, "st-1", []string{"m"})
	srv := New(st)
	srv.inflight = newSemaphore(1)
	srv.completionTimeout = time.Second // A will hold its slot while it waits (no station polls)

	// Request A occupies the single slot and blocks waiting for a station.
	occupied := make(chan struct{})
	go func() {
		rec := httptest.NewRecorder()
		req := signedBody(t, client, http.MethodPost, "/v1/chat/completions", []byte(`{"model":"m","a":1}`))
		close(occupied)
		srv.Handler().ServeHTTP(rec, req)
	}()
	<-occupied
	time.Sleep(50 * time.Millisecond) // let A acquire the slot

	// Request B cannot get a slot and is refused fast, not queued.
	recB := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recB, signedBody(t, client, http.MethodPost, "/v1/chat/completions", []byte(`{"model":"m","b":2}`)))
	require.Equal(t, http.StatusServiceUnavailable, recB.Code, "past the concurrency bound, a request is refused")
}

func TestPerClientConcurrencyIsFair(t *testing.T) {
	st := standaloneState(t)
	alice := admitClient(t, st)
	attachKeyedStation(t, st, "st-1", []string{"m"})
	srv := New(st)
	srv.perClient = newClientInflight(1) // alice may hold one at a time
	srv.completionTimeout = time.Second

	// Alice's first request occupies her one slot and blocks waiting for a station.
	occupied := make(chan struct{})
	go func() {
		close(occupied)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, signedBody(t, alice, http.MethodPost, "/v1/chat/completions", []byte(`{"model":"m","a":1}`)))
	}()
	<-occupied
	time.Sleep(50 * time.Millisecond)

	// Alice's second concurrent request is refused - she is at her per-client cap.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, signedBody(t, alice, http.MethodPost, "/v1/chat/completions", []byte(`{"model":"m","a":2}`)))
	require.Equal(t, http.StatusTooManyRequests, rec.Code, "one client cannot exceed its concurrent share")
}
