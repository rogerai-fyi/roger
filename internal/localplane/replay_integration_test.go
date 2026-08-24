package localplane

// Contract: features/tower/standalone_consumer_plane.feature - the replay-defense scenarios,
// through the real handlers, now that the plane binds a per-request nonce.

import (
	"bytes"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/protocol"
)

// nonceReq builds a request signed WITH a nonce, the way a roger client pointed at a local
// Tower signs. Reusing the same nonce+headers is exactly what a replay does.
func nonceReq(priv ed25519.PrivateKey, method, path string, body []byte, nonce string) *http.Request {
	pubHex, ts, sig := protocol.SignRequestNonce(priv, method, path, body, nonce)
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
	}
	r.Header.Set(protocol.HeaderPubkey, pubHex)
	r.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	r.Header.Set(protocol.HeaderSig, sig)
	r.Header.Set(protocol.HeaderNonce, nonce)
	return r
}

func TestACapturedRequestCannotBeReplayed(t *testing.T) {
	st := standaloneState(t)
	client := admitClient(t, st)
	srv := New(st)

	nonce := protocol.NewNonce()
	pubHex, ts, sig := protocol.SignRequestNonce(client, http.MethodGet, "/discover", nil, nonce)
	mk := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/discover", nil)
		r.Header.Set(protocol.HeaderPubkey, pubHex)
		r.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
		r.Header.Set(protocol.HeaderSig, sig)
		r.Header.Set(protocol.HeaderNonce, nonce)
		return r
	}
	rec1 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec1, mk())
	require.Equal(t, http.StatusOK, rec1.Code, "the first use is accepted")

	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, mk())
	require.Equal(t, http.StatusUnauthorized, rec2.Code, "the verbatim replay is refused")
	require.Equal(t, `{"error":"unauthorized"}`, rec2.Body.String(), "and refused as the uniform 401")
}

func TestTwoHonestSameSecondRequestsBothSucceed(t *testing.T) {
	st := standaloneState(t)
	client := admitClient(t, st)
	srv := New(st)

	// The exact case a signature-only guard broke: two identical requests in the same second.
	// With a per-request nonce each has a distinct signature, so BOTH are accepted.
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, nonceReq(client, http.MethodGet, "/discover", nil, protocol.NewNonce()))
		require.Equal(t, http.StatusOK, rec.Code, "honest duplicate %d must succeed (distinct nonce)", i)
	}
}

func TestStationPollReplayIsRefused(t *testing.T) {
	st := standaloneState(t)
	admitClient(t, st) // operator, so a station can attach
	station := attachKeyedStation(t, st, "st-1", []string{"m"})
	srv := New(st)

	nonce := protocol.NewNonce()
	pubHex, ts, sig := protocol.SignRequestNonce(station, http.MethodPost, "/local/poll", nil, nonce)
	mk := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/local/poll", nil)
		r.Header.Set(protocol.HeaderPubkey, pubHex)
		r.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
		r.Header.Set(protocol.HeaderSig, sig)
		r.Header.Set(protocol.HeaderNonce, nonce)
		return r
	}
	srv.pollTimeout = 50 * 1000 * 1000 // 50ms; no job pending so it returns 204 fast

	rec1 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec1, mk())
	require.Equal(t, http.StatusNoContent, rec1.Code, "the first poll authenticates (no work waiting)")

	// A captured poll replayed verbatim would, with a pending job, be handed the consumer's
	// prompt. It is refused: same nonce, seen before.
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, mk())
	require.Equal(t, http.StatusUnauthorized, rec2.Code, "a replayed station poll is refused")
}

func TestFreshNonceStationPollsKeepWorking(t *testing.T) {
	st := standaloneState(t)
	admitClient(t, st)
	station := attachKeyedStation(t, st, "st-1", []string{"m"})
	srv := New(st)
	srv.pollTimeout = 50 * 1000 * 1000 // 50ms

	// A station re-polling with a fresh nonce each time is never mistaken for a replay.
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, nonceReq(station, http.MethodPost, "/local/poll", nil, protocol.NewNonce()))
		require.Equal(t, http.StatusNoContent, rec.Code, "re-poll %d with a fresh nonce works", i)
	}
}
