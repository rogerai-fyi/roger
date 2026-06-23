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
	// max-price filters c and a? c=0.1 ok, but cap 0.15 keeps c
	if id, ok := pickAlternative(offers, Criteria{Model: "m", MaxPrice: 0.15}, nil); !ok || id != "c" {
		t.Errorf("maxprice got %q,%v want c", id, ok)
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
