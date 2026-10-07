package main

// idem_charged_test.go: an Idempotency-Key whose request was charged keeps its claim even when
// nothing reached the consumer (it left between the settle and the first byte): giving the key
// back would let the retry charge again (slice-6 audit 2026-10-06).

import (
	"crypto/ed25519"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
)

func idemReq(key string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	r.Header.Set("Idempotency-Key", key)
	return r
}

func TestIdemKeyKeptWhenChargedBeforeAnyWrite(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	body := []byte(`{"model":"m"}`)

	for _, tc := range []struct {
		name    string
		charged bool
		wantRun bool // the retry runs fresh
	}{
		{"charged, nothing written: the key stays claimed", true, false},
		{"not charged, nothing written: the key is given back", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := "k-" + strings.ReplaceAll(tc.name[:12], " ", "-")
			rw := &relayWriter{ResponseWriter: httptest.NewRecorder(), attempts: func() int { return 1 }}
			finish, done := b.idemBegin(rw, idemReq(key), rw, body, "payer-1", "req-first", false)
			require.False(t, done)
			require.NotNil(t, finish)
			if tc.charged {
				noteCharged(rw)
			}
			finish() // the consumer left: nothing was written

			rw2 := &relayWriter{ResponseWriter: httptest.NewRecorder(), attempts: func() int { return 1 }}
			_, done = b.idemBegin(rw2, idemReq(key), rw2, body, "payer-1", "req-retry", false)
			require.Equal(t, tc.wantRun, !done, "a retry runs fresh only when nothing was charged")
		})
	}
}

// slice-6 audit 2026-10-06: an in-flight claim past its deadline is taken over, so a stream's
// claim carries the whole window as its deadline (a stream may run far past the non-stream wait)
// and a non-stream claim the non-stream wait.
func TestIdemClaimDeadlineFollowsTheRequestShape(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	b := buildBroker(db, priv, 0.30, 100, time.Hour)
	now := b.now()
	for _, tc := range []struct {
		stream bool
		want   time.Duration
	}{{false, nonStreamRelayWait}, {true, idemTTL()}} {
		key := fmt.Sprintf("k-shape-%v", tc.stream)
		rw := &relayWriter{ResponseWriter: httptest.NewRecorder(), attempts: func() int { return 1 }}
		_, done := b.idemBegin(rw, idemReq(key), rw, []byte(`{"model":"m"}`), "payer-2", "req-"+key, tc.stream)
		require.False(t, done)
		probe := store.IdemClaim{Payer: "payer-2", Key: key, Fingerprint: "other", RequestID: "probe", State: store.IdemInFlight, Created: now.UnixMilli()}
		cur, claimed, err := db.ClaimIdempotency(probe, 0, 1000)
		require.NoError(t, err)
		require.False(t, claimed)
		require.InDelta(t, now.Add(tc.want).UnixMilli(), cur.Deadline, 2000, "stream=%v", tc.stream)
	}
}
