package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestExchangeCode covers the OAuth code->token exchange against a fake GitHub: a token
// in the response succeeds; an empty/absent token fails closed.
func TestExchangeCode(t *testing.T) {
	t.Setenv("GITHUB_OAUTH_CLIENT_ID", "cid")
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "sec")

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"gho_abc"}`))
	}))
	defer ok.Close()
	old := ghAccessTokenURL
	ghAccessTokenURL = ok.URL
	defer func() { ghAccessTokenURL = old }()

	if tok, vok := exchangeCode("code123"); !vok || tok != "gho_abc" {
		t.Fatalf("exchangeCode = %q / %v, want gho_abc/true", tok, vok)
	}

	// Empty token in the response -> fail closed.
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`))
	}))
	defer empty.Close()
	ghAccessTokenURL = empty.URL
	if _, vok := exchangeCode("bad"); vok {
		t.Error("exchangeCode with no access_token should fail")
	}
}

// TestProbeAccessors covers the probe trust accessors: ttftMs read-through and the
// consecutive-failure deprioritize threshold.
func TestProbeAccessors(t *testing.T) {
	b := &broker{trust: map[string]trustState{
		"fast":  {ttftMs: 120},
		"flaky": {probeFails: 4},
		"ok":    {probeFails: 1},
	}}
	if got := b.probeTTFT("fast"); got != 120 {
		t.Errorf("probeTTFT(fast) = %v, want 120", got)
	}
	if b.probeTTFT("unknown") != 0 {
		t.Errorf("probeTTFT(unknown) should be 0")
	}
	if !b.probeFailing("flaky") {
		t.Error("a node with 4 consecutive probe fails should be probeFailing")
	}
	if b.probeFailing("ok") {
		t.Error("a node with 1 fail should not be probeFailing")
	}
}
