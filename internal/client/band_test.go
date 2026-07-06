package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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
	// The exported helper (used by the agent harness) must agree with the internal one.
	if EffectiveMaxOut(0) != ConsumerDefaultMaxOut || EffectiveMaxOut(7) != 7 {
		t.Errorf("EffectiveMaxOut diverged from effectiveMaxOut")
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
	relayWithFailover(context.Background(), w, opts, Criteria{Model: "m"}, []byte(`{"model":"m"}`), srv.Client(), defaultPolicy(), nil)
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
	relayWithFailover(context.Background(), w, opts, Criteria{Model: "m"}, []byte(`{"model":"m"}`), srv.Client(), defaultPolicy(), nil)
	if gotCap != "3.5" {
		t.Errorf("explicit cap header = %q, want %q", gotCap, "3.5")
	}
}

// TestRelaySendsFreqHeader: a private-band tune-in carries X-Roger-Freq so the broker
// admits only the resolved station - AND still carries the consumer out-cap, so the
// PRIVATE (--freq) path is bounded against overpay exactly like the public market.
func TestRelaySendsFreqHeader(t *testing.T) {
	var gotFreq, gotCap string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFreq = r.Header.Get("X-Roger-Freq")
		gotCap = r.Header.Get("X-Roger-Max-Price-Out")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	w := httptest.NewRecorder()
	// --freq tune-in with NO explicit cap: the broker default cap must ride along.
	opts := ProxyOptions{Broker: srv.URL, User: "u", Freq: "147.520 MHz 8F3K-9M2Q"}
	relayWithFailover(context.Background(), w, opts, Criteria{Model: "m"}, []byte(`{"model":"m"}`), srv.Client(), defaultPolicy(), nil)
	if gotFreq != "147.520 MHz 8F3K-9M2Q" {
		t.Errorf("freq header = %q, want the code", gotFreq)
	}
	if gotCap != "10" {
		t.Errorf("--freq path cap header = %q, want the $10 default (private path must be bounded too)", gotCap)
	}
}

// bandResolveServer stands up a broker stub whose /bands/resolve returns one online
// private station for "m" at priceOut, so the private-band confirm path can be driven.
func bandResolveServer(t *testing.T, priceOut float64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bands/resolve" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"band":   map[string]string{"display": "147.520 MHz 8F3K-9M2Q"},
				"offers": []Offer{{NodeID: "n", Model: "m", PriceOut: priceOut, Online: true}},
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
}

// TestPrivateBandTypeThePriceConfirm: on a PRIVATE (--freq) band whose out-price is above
// the type-the-price confirm threshold, a wrong typed price aborts (no channel opens).
// This proves the high-price confirm fires on the private path, not only the public use.
func TestPrivateBandTypeThePriceConfirm(t *testing.T) {
	srv := bandResolveServer(t, ConsumerConfirmThreshold+5) // above the confirm line
	defer srv.Close()

	// Feed a WRONG typed price on stdin; the confirm must reject and return without
	// binding a channel (so the call returns promptly with nil and never ListenAndServe).
	pr, pw, _ := os.Pipe()
	_, _ = pw.WriteString("0.01\n") // not the shown price -> abort
	_ = pw.Close()

	opt := UseOptions{Freq: "147.520 MHz 8F3K-9M2Q", Port: 0}
	err := useOnFreq(srv.URL, "u", "m", opt, ConsumerConfirmThreshold+5, 800, false, pr)
	if err != nil {
		t.Fatalf("useOnFreq returned error on aborted confirm: %v", err)
	}
	// (A correct path would block in ListenAndServe; the abort returns nil immediately,
	// which is exactly the type-the-price guard doing its job on the private band.)
}

// TestChatCarriesDefaultCap: the in-channel chat relay (client.ChatDetailed) carries the
// consumer out-cap header so the TUI channel is bounded against overpay like `use`. A 0
// maxOut applies the default; an explicit cap is honored (opt-in to pay more).
func TestChatCarriesDefaultCap(t *testing.T) {
	var gotCap string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCap = r.Header.Get("X-Roger-Max-Price-Out")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	}))
	defer srv.Close()

	if _, err := ChatDetailed(srv.URL, "u", "m", "hello", false, 0); err != nil {
		t.Fatalf("ChatDetailed error: %v", err)
	}
	if gotCap != "10" {
		t.Errorf("in-channel chat cap header = %q, want the $10 default", gotCap)
	}
	if _, err := ChatDetailed(srv.URL, "u", "m", "hello", false, 42); err != nil {
		t.Fatalf("ChatDetailed error: %v", err)
	}
	if gotCap != "42" {
		t.Errorf("in-channel chat explicit cap header = %q, want 42 (opt-in to pay more)", gotCap)
	}
}
