package client

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRetryable(t *testing.T) {
	cases := []struct {
		status int
		err    error
		want   bool
	}{
		{200, nil, false},
		{400, nil, false}, // bad request - caller's fault, surface it
		{402, nil, false}, // insufficient credits - don't retry
		{500, nil, true},
		{502, nil, true},
		{503, nil, true},
		{504, nil, true},
		{0, errors.New("dial timeout"), true},
	}
	for _, c := range cases {
		if got := retryable(c.status, c.err); got != c.want {
			t.Errorf("retryable(%d,%v)=%v want %v", c.status, c.err, got, c.want)
		}
	}
}

func TestPickAlternative(t *testing.T) {
	offers := []Offer{
		{NodeID: "a", Model: "m", PriceIn: 0.5, Online: true, TPS: 100},
		{NodeID: "b", Model: "m", PriceIn: 0.2, Online: true, TPS: 100}, // same tps, cheaper
		{NodeID: "c", Model: "m", PriceIn: 0.1, Online: true, TPS: 300}, // fastest
		{NodeID: "d", Model: "m", PriceIn: 0.1, Online: false, TPS: 500},
		{NodeID: "e", Model: "other", PriceIn: 0.1, Online: true, TPS: 999},
	}

	// fastest eligible wins
	if id, ok := pickAlternative(offers, Criteria{Model: "m"}, nil); !ok || id != "c" {
		t.Errorf("got %q,%v want c", id, ok)
	}
	// exclude the fastest → next is cheaper of the 100-tps pair (b)
	if id, ok := pickAlternative(offers, Criteria{Model: "m"}, map[string]bool{"c": true}); !ok || id != "b" {
		t.Errorf("with c excluded got %q,%v want b", id, ok)
	}
	// max-in filters c and a? c=0.1 ok, but cap 0.15 keeps c
	if id, ok := pickAlternative(offers, Criteria{Model: "m", MaxPriceIn: 0.15}, nil); !ok || id != "c" {
		t.Errorf("max-in got %q,%v want c", id, ok)
	}
	// min-tps floor excludes the 100-tps nodes once c is gone
	if id, ok := pickAlternative(offers, Criteria{Model: "m", MinTPS: 200}, map[string]bool{"c": true}); ok {
		t.Errorf("min-tps should leave nothing, got %q", id)
	}
	// confidential required, none confidential → nothing
	if _, ok := pickAlternative(offers, Criteria{Model: "m", Confidential: true}, nil); ok {
		t.Error("confidential filter should exclude all")
	}
	// unknown model → nothing
	if _, ok := pickAlternative(offers, Criteria{Model: "zzz"}, nil); ok {
		t.Error("unknown model should yield nothing")
	}
}

func TestPickAlternativeUnmeasuredGetsChance(t *testing.T) {
	// A node with tps==0 (never measured) must not be filtered out by MinTPS,
	// otherwise new providers are permanently locked out.
	offers := []Offer{{NodeID: "new", Model: "m", PriceIn: 0.2, Online: true, TPS: 0}}
	if id, ok := pickAlternative(offers, Criteria{Model: "m", MinTPS: 100}, nil); !ok || id != "new" {
		t.Errorf("unmeasured node got %q,%v want new", id, ok)
	}
}

func TestBackoffCaps(t *testing.T) {
	p := failoverPolicy{baseBackoff: 100 * time.Millisecond, maxBackoff: 400 * time.Millisecond}
	got := []time.Duration{p.backoff(1), p.backoff(2), p.backoff(3), p.backoff(4)}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 400 * time.Millisecond}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("backoff(%d)=%v want %v", i+1, got[i], want[i])
		}
	}
}

// TestProxyFailoverEndToEnd stands up a fake broker whose first provider always
// 503s and a second one succeeds; the proxy must transparently re-route and the
// client must see a 200 with the healthy provider's body.
func TestProxyFailoverEndToEnd(t *testing.T) {
	var hits []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discover":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"offers":[
				{"node_id":"bad","model":"m","price_in":0.1,"online":true,"tps":500},
				{"node_id":"good","model":"m","price_in":0.2,"online":true,"tps":100}
			]}`))
		case "/v1/chat/completions":
			pin := r.Header.Get("X-Roger-Node")
			mu.Lock()
			hits = append(hits, pin)
			mu.Unlock()
			// First call (no pin) routes to "bad" and fails; failover pins "good".
			if pin == "good" {
				w.Header().Set("X-RogerAI-Provider", "good")
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"ok":true}`))
				return
			}
			w.Header().Set("X-RogerAI-Provider", "bad")
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()

	var alerted string
	opts := ProxyOptions{Broker: srv.URL, User: "u", Alert: func(s string) { alerted = s }}
	h := ProxyHandler(opts)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Errorf("body=%s, expected the healthy provider response", rec.Body.String())
	}
	if rec.Header().Get("X-RogerAI-Provider") != "good" {
		t.Errorf("provider header=%q want good", rec.Header().Get("X-RogerAI-Provider"))
	}
	if !strings.Contains(alerted, "recovered") {
		t.Errorf("expected a recovery alert, got %q", alerted)
	}
}

// TestProxyFailoverExhausted: every provider 503s → clear 502 + alert.
func TestProxyFailoverExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/discover" {
			w.Write([]byte(`{"offers":[{"node_id":"only","model":"m","price_in":0.1,"online":true,"tps":100}]}`))
			return
		}
		w.Header().Set("X-RogerAI-Provider", "only")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	var alerted string
	opts := ProxyOptions{Broker: srv.URL, User: "u", MinTPS: 50, Alert: func(s string) { alerted = s }}
	// Speed up the test: tiny policy via the handler's default is fine (200ms base),
	// but only one provider exists so it exhausts after the first failover lookup.
	h := ProxyHandler(opts)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d want 502", rec.Code)
	}
	if !strings.Contains(alerted, "no provider available") {
		t.Errorf("expected exhaustion alert, got %q", alerted)
	}
	if !strings.Contains(alerted, "min-tps") {
		t.Errorf("alert should mention the active constraints, got %q", alerted)
	}
}

// TestProxyNonRetryablePassthrough: a 402 (no credits) must NOT trigger failover;
// it's surfaced to the caller verbatim.
func TestProxyNonRetryablePassthrough(t *testing.T) {
	var discoverCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/discover" {
			discoverCalls++
			w.Write([]byte(`{"offers":[]}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"error":{"message":"insufficient credits"}}`))
	}))
	defer srv.Close()

	h := ProxyHandler(ProxyOptions{Broker: srv.URL, User: "u"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status=%d want 402", rec.Code)
	}
	if discoverCalls != 0 {
		t.Errorf("a 402 should not trigger failover (discover called %d times)", discoverCalls)
	}
}

// TestBandRange verifies the live cross-station out-price range: min/max of the
// active out-price across ONLINE stations of the exact model, the cheapest node,
// and station count. Offline + other-model offers don't count; a single station
// yields Min==Max (no faked spread).
func TestBandRange(t *testing.T) {
	offers := []Offer{
		{NodeID: "a", Model: "m", PriceIn: 0.18, PriceOut: 0.22, Online: true, TPS: 58},
		{NodeID: "b", Model: "m", PriceIn: 0.30, PriceOut: 0.41, Online: true, TPS: 40},
		{NodeID: "c", Model: "m", PriceIn: 0.25, PriceOut: 0.34, Online: true, TPS: 47},
		{NodeID: "d", Model: "m", PriceIn: 0.10, PriceOut: 0.05, Online: false}, // offline: ignored
		{NodeID: "e", Model: "other", PriceOut: 0.01, Online: true},             // other band: ignored
	}
	br, ok := bandRange(offers, "m")
	if !ok {
		t.Fatal("expected a range for m")
	}
	if br.Stations != 3 {
		t.Errorf("stations = %d, want 3", br.Stations)
	}
	if br.Min != 0.22 || br.Max != 0.41 {
		t.Errorf("range = %.2f ~ %.2f, want 0.22 ~ 0.41", br.Min, br.Max)
	}
	if br.CheapNode != "a" || br.CheapTPS != 58 || br.CheapIn != 0.18 {
		t.Errorf("cheapest = %q tps=%v in=%v, want a/58/0.18", br.CheapNode, br.CheapTPS, br.CheapIn)
	}

	// Single station: Min==Max, no spread.
	one := []Offer{{NodeID: "z", Model: "solo", PriceOut: 0.55, Online: true}}
	br2, ok := bandRange(one, "solo")
	if !ok || br2.Stations != 1 || br2.Min != br2.Max || br2.Min != 0.55 {
		t.Errorf("single-station = %+v, want one point 0.55", br2)
	}

	// No station on air for the band.
	if _, ok := bandRange(offers, "ghost"); ok {
		t.Error("expected no range for an absent band")
	}
}

// TestEstReplyCost verifies the per-reply est-cost math and its default token count.
func TestEstReplyCost(t *testing.T) {
	if got := estReplyCost(0.22, 800); got != 0.22*800/1e6 {
		t.Errorf("estReplyCost = %v, want %v", got, 0.22*800/1e6)
	}
	if got := estReplyCost(0.22, 0); got != 0.22*800/1e6 {
		t.Errorf("estReplyCost default tokens = %v, want 800-token cost", got)
	}
}

// TestIsYes verifies the connect confirm defaults to DENY: only y/yes accept.
func TestIsYes(t *testing.T) {
	for _, s := range []string{"y", "Y", "yes", "  yes  ", "YES"} {
		if !isYes(s) {
			t.Errorf("isYes(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "n", "no", "nope", "1", "true", " "} {
		if isYes(s) {
			t.Errorf("isYes(%q) = true, want false (default deny)", s)
		}
	}
}
