package client

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestEffectiveMaxOut: the consumer default cap is applied when none is set, and a
// caller's explicit cap is honored. priceMatches tolerates a cent of float noise.
func TestEffectiveMaxOut(t *testing.T) {
	if got := effectiveMaxOut(0); got != ConsumerDefaultMaxOut {
		t.Errorf("no cap -> %v, want default %v", got, ConsumerDefaultMaxOut)
	}
	if got := effectiveMaxOut(2.5); got != 2.5 {
		t.Errorf("explicit cap clobbered: %v", got)
	}
	if !priceMatches(12.50, 12.50) || !priceMatches(12.505, 12.50) {
		t.Errorf("priceMatches should accept an exact / near-exact typed price")
	}
	if priceMatches(12.0, 12.50) {
		t.Errorf("priceMatches should reject a fat-fingered price")
	}
}

// TestRelayEnforcesDefaultCap: a request with NO out-price cap (the --yes / headless
// overpay path) must still carry the default consumer ceiling header to the broker,
// so the broker can refuse an over-cap station. This closes the accidental-overpay
// path even for callers that never saw the interactive confirm.
func TestRelayEnforcesDefaultCap(t *testing.T) {
	var gotCap string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCap = r.Header.Get("X-Roger-Max-Price-Out")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	w := httptest.NewRecorder()
	// opts.MaxPriceOut = 0 (no cap set) -> the relay must inject the default ceiling.
	opts := ProxyOptions{Broker: srv.URL, User: "u"}
	relayWithFailover(w, opts, Criteria{Model: "m"}, []byte(`{"model":"m"}`), srv.Client(), defaultPolicy())
	if gotCap == "" {
		t.Fatalf("relay sent no X-Roger-Max-Price-Out - default cap NOT enforced (overpay path open)")
	}
	// It should be the default ceiling (formatted with %g => "10").
	if gotCap != "10" {
		t.Errorf("default cap header = %q, want %q", gotCap, "10")
	}
}

// TestRelayHonorsExplicitCap: an explicit out-price cap is sent verbatim, not the
// default.
func TestRelayHonorsExplicitCap(t *testing.T) {
	var gotCap string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCap = r.Header.Get("X-Roger-Max-Price-Out")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	w := httptest.NewRecorder()
	opts := ProxyOptions{Broker: srv.URL, User: "u", MaxPriceOut: 3.5}
	relayWithFailover(w, opts, Criteria{Model: "m"}, []byte(`{"model":"m"}`), srv.Client(), defaultPolicy())
	if gotCap != "3.5" {
		t.Errorf("explicit cap header = %q, want %q", gotCap, "3.5")
	}
}

// TestRelaySendsFreqHeader: a private-band tune-in carries X-Roger-Freq so the broker
// admits only the resolved station.
func TestRelaySendsFreqHeader(t *testing.T) {
	var gotFreq string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFreq = r.Header.Get("X-Roger-Freq")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	w := httptest.NewRecorder()
	opts := ProxyOptions{Broker: srv.URL, User: "u", Freq: "147.520 MHz 8F3K-9M2Q"}
	relayWithFailover(w, opts, Criteria{Model: "m"}, []byte(`{"model":"m"}`), srv.Client(), defaultPolicy())
	if gotFreq != "147.520 MHz 8F3K-9M2Q" {
		t.Errorf("freq header = %q, want the code", gotFreq)
	}
}
