package client

import (
	"net/http"
	"os"
	"testing"
)

// raw_reasoning_test.go pins the `roger use --raw` wiring (PR #34 follow-up, finding 1): the
// documented "a caller that needs raw passthrough can disable it per session" toggle must have a
// real user-facing surface. --raw (UseOptions.Raw) and the ROGERAI_REASONING_RAW env var both
// thread into ProxyOptions.ReasoningFallbackOff so the per-session disable actually works; absent
// = fallback ON (the default). It is RED against origin/main because UseOptions has no Raw field
// and nothing sets ReasoningFallbackOff from a caller knob.
//
// The wiring is observed through newProxyHandler, the seam Use/useOnFreq build the proxy handler
// through (the twin of the useServe seam) - a test captures the ProxyOptions Use assembled.

// captureProxyOpts points newProxyHandler at a capture func so a test can read the ProxyOptions
// Use built without binding a real relay. Restored on cleanup. It still returns a real handler so
// the downstream useServe seam is unaffected.
func captureProxyOpts(t *testing.T, got *ProxyOptions) {
	t.Helper()
	old := newProxyHandler
	newProxyHandler = func(o ProxyOptions) http.Handler {
		*got = o
		return old(o)
	}
	t.Cleanup(func() { newProxyHandler = old })
}

// TestUseRawFlagDisablesReasoningFallback: `roger use --raw` (UseOptions.Raw) sets
// ProxyOptions.ReasoningFallbackOff on the open-market path, so the relay hands back the raw
// provider body. Default (Raw false, env unset) leaves the fallback ON.
func TestUseRawFlagDisablesReasoningFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGERAI_REASONING_RAW", "")
	b := fakeBroker(t)

	var addr string
	captureServe(t, &addr)
	var opts ProxyOptions
	captureProxyOpts(t, &opts)

	if err := Use(b, "u_gh_1", "m1", UseOptions{Raw: true, Yes: true, MaxOut: 5, Port: 7100}); err != nil {
		t.Fatalf("Use(--raw) = %v, want nil", err)
	}
	if !opts.ReasoningFallbackOff {
		t.Fatal("--raw did not set ProxyOptions.ReasoningFallbackOff (per-session disable not wired)")
	}
}

// TestUseDefaultKeepsReasoningFallbackOn: without --raw and with the env unset the fallback stays
// ON (ReasoningFallbackOff false) - the founder default.
func TestUseDefaultKeepsReasoningFallbackOn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGERAI_REASONING_RAW", "")
	b := fakeBroker(t)

	var addr string
	captureServe(t, &addr)
	var opts ProxyOptions
	captureProxyOpts(t, &opts)

	if err := Use(b, "u_gh_1", "m1", UseOptions{Yes: true, MaxOut: 5, Port: 7101}); err != nil {
		t.Fatalf("Use(default) = %v, want nil", err)
	}
	if opts.ReasoningFallbackOff {
		t.Fatal("default Use disabled the reasoning fallback (should default ON)")
	}
}

// TestUseRawEnvDisablesReasoningFallback: ROGERAI_REASONING_RAW=1 disables the fallback even
// without the flag, so a scripted caller can opt into raw passthrough via the environment.
func TestUseRawEnvDisablesReasoningFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGERAI_REASONING_RAW", "1")
	b := fakeBroker(t)

	var addr string
	captureServe(t, &addr)
	var opts ProxyOptions
	captureProxyOpts(t, &opts)

	if err := Use(b, "u_gh_1", "m1", UseOptions{Yes: true, MaxOut: 5, Port: 7102}); err != nil {
		t.Fatalf("Use(env raw) = %v, want nil", err)
	}
	if !opts.ReasoningFallbackOff {
		t.Fatal("ROGERAI_REASONING_RAW=1 did not disable the reasoning fallback")
	}
}

// TestUseOnFreqRawDisablesReasoningFallback: the private-band (--freq) path honors --raw too, so
// the toggle is not silently dropped when tuning a private frequency.
func TestUseOnFreqRawDisablesReasoningFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGERAI_REASONING_RAW", "")
	srv := bandResolveServer(t, 2.0) // below the confirm threshold -> plain (y/N)

	var addr string
	captureServe(t, &addr)
	var opts ProxyOptions
	captureProxyOpts(t, &opts)

	pr, pw, _ := os.Pipe()
	_, _ = pw.WriteString("y\n") // confirm the channel
	_ = pw.Close()

	opt := UseOptions{Freq: "147.520 MHz 8F3K-9M2Q", Port: 7103, Raw: true}
	if err := useOnFreq(srv.URL, "u", "m", opt, ConsumerDefaultMaxOut, 800, true, pr); err != nil {
		t.Fatalf("useOnFreq(--raw) = %v, want nil", err)
	}
	if !opts.ReasoningFallbackOff {
		t.Fatal("--raw did not set ReasoningFallbackOff on the private-band path")
	}
}
