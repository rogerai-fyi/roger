package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/glyphs"
)

// TestChatSendsRaisedMaxTokens: the in-channel chat must request the shared raised
// answer budget (MaxAnswerTokens), not the old 256 that truncated answers and emptied
// reasoning-model turns.
func TestChatSendsRaisedMaxTokens(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var gotMaxTokens float64
	var seen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if v, ok := body["max_tokens"].(float64); ok {
			gotMaxTokens, seen = v, true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	}))
	defer srv.Close()

	if _, err := ChatDetailed(srv.URL, "tester", "gpt-oss-20b", "hello", false, 0); err != nil {
		t.Fatalf("ChatDetailed error: %v", err)
	}
	if !seen {
		t.Fatalf("chat request did not carry a max_tokens field")
	}
	if int(gotMaxTokens) != MaxAnswerTokens {
		t.Errorf("channel chat requested max_tokens=%d, want %d (shared raised budget)", int(gotMaxTokens), MaxAnswerTokens)
	}
	if MaxAnswerTokens <= 256 {
		t.Errorf("MaxAnswerTokens=%d must exceed the old 256 ceiling that truncated answers", MaxAnswerTokens)
	}
}

// TestChatFailsOverPastABadStation: a station that returns a retryable 5xx must not
// dead-end the channel - Chat retries, asking the broker to EXCLUDE the failed node,
// and succeeds on the next station. This is the fix for "I said hi and got a 504".
func TestChatFailsOverPastABadStation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var calls int
	var excludeOnRetry string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			// First station times out: a retryable 504, naming itself as the provider.
			w.Header().Set("X-RogerAI-Provider", "zombie-node")
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		// Retry: the broker should have been told to skip the failed node.
		excludeOnRetry = r.Header.Get("X-Roger-Exclude-Nodes")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RogerAI-Provider", "good-node")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi back"}}]}`))
	}))
	defer srv.Close()

	r, err := ChatDetailed(srv.URL, "tester", "gpt-oss-120b", "hi", false, 0)
	if err != nil {
		t.Fatalf("ChatDetailed should have failed over to the good station, got: %v", err)
	}
	if r.Reply != "hi back" {
		t.Fatalf("reply = %q, want the second station's answer", r.Reply)
	}
	if calls < 2 {
		t.Fatalf("Chat made %d calls, want >=2 (it must retry past the 504)", calls)
	}
	if excludeOnRetry != "zombie-node" {
		t.Fatalf("retry should exclude the failed node, X-Roger-Exclude-Nodes=%q want zombie-node", excludeOnRetry)
	}
}

// TestChatDoesNotRetryA4xx: a non-retryable 4xx (e.g. 402 no credits) returns at once,
// without burning failover attempts on the user's own error.
func TestChatDoesNotRetryA4xx(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"message":"insufficient balance"}}`))
	}))
	defer srv.Close()

	if _, err := ChatDetailed(srv.URL, "tester", "m", "hi", false, 0); err == nil {
		t.Fatal("a 402 should return an error")
	}
	if calls != 1 {
		t.Fatalf("a 402 must NOT be retried, got %d calls", calls)
	}
}

// TestGetJSONDistinctErrorOnNon2xx: a broker 500 must surface as ErrBrokerUnreachable
// (the "couldn't reach the broker" class), NOT a silent empty/zero decode - so balance
// and search can tell broker-down apart from a real empty result.
func TestGetJSONDistinctErrorOnNon2xx(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	var out struct {
		Balance float64 `json:"balance"`
	}
	err := getJSON(srv.URL, "/balance", "tester", &out)
	if err == nil {
		t.Fatalf("getJSON on a 500 returned nil error (would masquerade as $0 / no offers)")
	}
	if !errors.Is(err, ErrBrokerUnreachable) {
		t.Errorf("getJSON 500 error = %v, want wrapped ErrBrokerUnreachable", err)
	}
}

// TestGetJSONDistinctErrorOnUnreachable: a dead broker (refused connection) is also the
// ErrBrokerUnreachable class, distinct from an empty body.
func TestGetJSONDistinctErrorOnUnreachable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// A server we immediately close, so the address refuses connections.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead := srv.URL
	srv.Close()

	var out struct{}
	err := getJSON(dead, "/balance", "tester", &out)
	if err == nil {
		t.Fatalf("getJSON to a dead broker returned nil error")
	}
	if !errors.Is(err, ErrBrokerUnreachable) {
		t.Errorf("getJSON unreachable error = %v, want wrapped ErrBrokerUnreachable", err)
	}
}

// TestGetJSON2xxDecodesNoError: a real 200 with an empty/zero body is NOT an error - the
// caller validates the fields (a genuine $0 stays a genuine $0).
func TestGetJSON2xxDecodesNoError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance":0,"logged_in":true}`))
	}))
	defer srv.Close()

	var out struct {
		Balance  float64 `json:"balance"`
		LoggedIn bool    `json:"logged_in"`
	}
	if err := getJSON(srv.URL, "/balance", "tester", &out); err != nil {
		t.Fatalf("getJSON on a real 200 returned error: %v", err)
	}
	if !out.LoggedIn {
		t.Errorf("logged_in not decoded from a real 200 body")
	}
}

// TestWithTopupHint: a 402 maps to the actionable topup hint; other statuses pass msg
// through unchanged.
func TestWithTopupHint(t *testing.T) {
	got := WithTopupHint(http.StatusPaymentRequired, "insufficient balance - add funds")
	if !strings.Contains(got, TopupHint) {
		t.Errorf("402 hint = %q, want it to contain %q", got, TopupHint)
	}
	if !strings.Contains(got, "topup") {
		t.Errorf("402 hint = %q, want an actionable topup step", got)
	}
	if blank := WithTopupHint(http.StatusPaymentRequired, ""); !strings.Contains(blank, TopupHint) {
		t.Errorf("402 with blank msg = %q, want the topup hint", blank)
	}
	if other := WithTopupHint(http.StatusServiceUnavailable, "no station"); other != "no station" {
		t.Errorf("non-402 should pass through unchanged, got %q", other)
	}
}

// TestChat402MapsToTopupHint: a broker 402 (insufficient balance) surfaces to the chat
// caller with the actionable topup next step appended, never a dead end.
func TestChat402MapsToTopupHint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"message":"insufficient balance - add funds"}}`))
	}))
	defer srv.Close()

	_, err := ChatDetailed(srv.URL, "tester", "gpt-oss-20b", "hello", false, 0)
	if err == nil {
		t.Fatalf("ChatDetailed on a 402 returned nil error")
	}
	if !strings.Contains(err.Error(), TopupHint) {
		t.Errorf("402 chat error = %q, want it to contain the topup hint %q", err.Error(), TopupHint)
	}
}

// TestSignalTowerUsesBrokerSignal verifies the band-list fix in the plain CLI: an
// online node with NO traffic (tps==0) but a broker signal (43) renders a NON-blank
// tower (it would have been blank when driven by tps alone). Offline renders the flat
// "no signal" tower. The glyph heights carry the reading (NO_COLOR / pipe safe).
func TestSignalTowerUsesBrokerSignal(t *testing.T) {
	off := glyphs.Current().SigOff

	// Online, signal 43, zero tps -> non-blank, mid-tower (the regression case).
	got := signalTower(43, 0, true)
	if got == off {
		t.Fatalf("online signal=43 tps=0 rendered the blank tower %q; the on-air band must meter", got)
	}
	if len([]rune(got)) != 5 {
		t.Errorf("tower = %q, want 5 cells", got)
	}

	// Offline -> blank tower regardless of signal.
	if got := signalTower(43, 0, false); got != off {
		t.Errorf("offline tower = %q, want the flat no-signal tower %q", got, off)
	}

	// No broker signal but measured tps still meters (legacy fallback path).
	if got := signalTower(0, 120, true); got == off {
		t.Errorf("online tps=120 with no broker signal should still meter, got blank")
	}

	// Online with neither signal nor tps -> at least one bar, never fully blank.
	if got := signalTower(0, 0, true); got == off {
		t.Errorf("online with no reading should show a faint carrier, got the blank tower")
	}
}

// TestSignalLevelMapping checks the 0..100 -> lit-bar COUNT (0..5): 0 yields the "no
// signal" sentinel (0), any positive signal is >= 1 bar (online never fully blank),
// ~43 lands mid-meter at 3 bars, and 100 lights the full staircase. Lock-step with
// the TUI's mapping.
func TestSignalLevelMapping(t *testing.T) {
	if signalLevel(0) != 0 {
		t.Errorf("signalLevel(0) = %d want 0 (no signal sentinel)", signalLevel(0))
	}
	if l := signalLevel(1); l != 1 {
		t.Errorf("signalLevel(1) = %d want 1 (online never blank)", l)
	}
	if l := signalLevel(43); l != 3 {
		t.Errorf("signalLevel(43) = %d want 3 (mid-meter)", l)
	}
	if l := signalLevel(100); l != 5 {
		t.Errorf("signalLevel(100) = %d want 5 (full staircase)", l)
	}
	// Monotonic: stronger signal never reads shorter.
	if signalLevel(20) > signalLevel(80) {
		t.Error("signalLevel should be non-decreasing in signal")
	}
}
