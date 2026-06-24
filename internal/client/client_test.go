package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

	if _, _, _, err := Chat(srv.URL, "tester", "gpt-oss-20b", "hello", false, 0); err != nil {
		t.Fatalf("Chat error: %v", err)
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

	_, _, _, err := Chat(srv.URL, "tester", "gpt-oss-20b", "hello", false, 0)
	if err == nil {
		t.Fatalf("Chat on a 402 returned nil error")
	}
	if !strings.Contains(err.Error(), TopupHint) {
		t.Errorf("402 chat error = %q, want it to contain the topup hint %q", err.Error(), TopupHint)
	}
}
