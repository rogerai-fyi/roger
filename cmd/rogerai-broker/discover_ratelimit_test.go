package main

// discover_ratelimit_test.go pins the RELEASE-DAY root cause of the "dial flickers to
// empty" incident: GET /discover (and its voice twin GET /voices) must NEVER answer a
// public READ with HTTP 429. The old redundant per-IP anon-rate-limit gate on these two
// PUBLIC READ endpoints returned 429 {"error":...} under normal same-IP polling, and every
// client reads `.offers` / `.voices` off the body - a 429 body has neither, so the market
// rendered EMPTY (an on-air band blinked to "quiet"). The expensive full-market recompute
// the gate meant to bound is ALREADY collapsed to <=1 per publicMarketTTL by the shared
// read-through cache (serveCachedJSON), so the throttle only ever harmed availability.
//
// The fix removes the anon-RL gate from /discover + /voices, matching /market's cache-only
// posture. These tests assert the invariant on the REAL HTTP handlers (no mocks): repeated
// same-IP reads all return 200 with the full payload, even when the anon bucket is DRAINED.
// The anon limiter itself stays (relay/audio/tunnel still use it) - it just no longer gates
// the two public reads.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// rlTestBroker builds a broker with one live, probed public offer on air and a DELIBERATELY
// TINY anon bucket (rpm 1, burst 1) - so if any public-read handler still consulted the anon
// limiter, the 2nd same-IP request in the burst would trip 429. The cache-only posture must
// keep every read at 200 regardless.
func rlTestBroker(t *testing.T) *broker {
	t.Helper()
	now := time.Now()
	b := routeBroker(now, map[string]protocol.NodeRegistration{
		"n-live": {NodeID: "n-live", Offers: []protocol.ModelOffer{
			{Model: "gpt-oss-20b", PriceIn: 0.20, PriceOut: 0.20},
		}},
		"n-voice": {NodeID: "n-voice", Station: "bownux", Offers: []protocol.ModelOffer{
			{Model: "roger-operator-voice", Modality: protocol.ModalityTTS, Name: "Operator", PriceIn: 20},
		}},
	})
	b.db = store.NewMem()
	b.probeSched = map[string]*probeState{}
	b.localCache = map[string]localCacheEntry{}
	b.trust["n-live"] = trustState{probed: true, probeOK: true, ttftMs: 200}
	b.tps["n-live"] = 120
	// The smallest possible bucket: one token, then empty. Proves the public reads do NOT
	// consult it (they would 429 on the 2nd hit if they did).
	b.anonRL = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 1, burst: 1}
	return b
}

// getFrom issues a GET against the handler from a single fixed CF-Connecting-IP.
func getFrom(h func(http.ResponseWriter, *http.Request), path, ip string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("CF-Connecting-IP", ip)
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// TestDiscoverNeverRateLimitsToEmpty is the primary regression: 60 rapid GET /discover from
// ONE IP, with a live offer on air and the anon bucket drained to a single token, must ALL
// return 200 with the offers list present - never a 429 whose body a client would misread as
// an empty market.
func TestDiscoverNeverRateLimitsToEmpty(t *testing.T) {
	b := rlTestBroker(t)
	const n = 60
	for i := 0; i < n; i++ {
		w := getFrom(b.discover, "/discover", "203.0.113.7")
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("GET /discover #%d = 429 (rate-limited); a public read must never 429 - it blanks the market", i+1)
		}
		if w.Code != http.StatusOK {
			t.Fatalf("GET /discover #%d = %d, want 200", i+1, w.Code)
		}
		var resp struct {
			Offers []offerView `json:"offers"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("GET /discover #%d: bad JSON: %v", i+1, err)
		}
		if len(resp.Offers) == 0 {
			t.Fatalf("GET /discover #%d returned an EMPTY market while an offer is on air (the flicker bug)", i+1)
		}
	}
}

// TestVoicesNeverRateLimitsToEmpty is the same regression for the voice twin: repeated
// same-IP GET /voices with the anon bucket drained must all return 200 with the voices list,
// never a 429.
func TestVoicesNeverRateLimitsToEmpty(t *testing.T) {
	b := rlTestBroker(t)
	// Bind the voice node's owner so operatorStation lists it (Q2: attributable operators only).
	bindOwnerNode(t, b, "n-voice", "bownux")
	const n = 60
	for i := 0; i < n; i++ {
		w := getFrom(b.voices, "/voices", "203.0.113.7")
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("GET /voices #%d = 429 (rate-limited); a public read must never 429", i+1)
		}
		if w.Code != http.StatusOK {
			t.Fatalf("GET /voices #%d = %d, want 200", i+1, w.Code)
		}
		var resp struct {
			Voices []voiceView `json:"voices"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("GET /voices #%d: bad JSON: %v", i+1, err)
		}
		if len(resp.Voices) == 0 {
			t.Fatalf("GET /voices #%d returned an EMPTY voice list while a voice is on air", i+1)
		}
	}
}

// TestPublicReadsShareCacheOnlyThrottlePosture pins that /discover and /market carry the
// SAME throttle posture: neither anon-RL-gates the public read (the shared short-TTL cache is
// their only guard). With the anon bucket drained to empty, both handlers keep answering 200.
func TestPublicReadsShareCacheOnlyThrottlePosture(t *testing.T) {
	b := rlTestBroker(t)
	// Drain the anon bucket outright for this IP, so any residual gate would 429 immediately.
	b.anonRL.allow("203.0.113.7")

	for _, tc := range []struct {
		name string
		h    func(http.ResponseWriter, *http.Request)
		path string
	}{
		{"discover", b.discover, "/discover"},
		{"market", b.market, "/market"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < 5; i++ {
				w := getFrom(tc.h, tc.path, "203.0.113.7")
				if w.Code == http.StatusTooManyRequests {
					t.Fatalf("GET %s #%d = 429; the public reads must share a cache-only (no anon-gate) posture", tc.path, i+1)
				}
				if w.Code != http.StatusOK {
					t.Fatalf("GET %s #%d = %d, want 200", tc.path, i+1, w.Code)
				}
			}
		})
	}
}
