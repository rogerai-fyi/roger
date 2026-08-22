package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicVersionReportsReleaseAndExactCommit(t *testing.T) {
	commit := strings.Repeat("a", 40)
	t.Setenv("ROGERAI_BUILD_COMMIT", strings.ToUpper(commit))

	rec := httptest.NewRecorder()
	(&broker{}).routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /version = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var got struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode /version: %v", err)
	}
	// Compared against the package's own `version`, NOT a literal. What this endpoint has
	// to get right is that it reports THE BUILD IT IS - the value -ldflags stamps - and a
	// hardcoded string tests the opposite: it passes when the endpoint is wired to a
	// constant and fails on every release, naming a version instead of a defect. The
	// literal here was "5.7.1", and it failed the v6.0.0 bump for no reason.
	if got.Version != version || got.Commit != commit {
		t.Fatalf("/version = %+v, want version %s commit %s", got, version, commit)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store so a rolling deploy cannot serve stale identity", cc)
	}
}

func TestPublicVersionOmitsInvalidCommitAndRejectsWrites(t *testing.T) {
	t.Setenv("ROGERAI_BUILD_COMMIT", "not-a-commit")
	rec := httptest.NewRecorder()
	(&broker{}).routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /version = %d, want 200", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode /version: %v", err)
	}
	if _, exists := got["commit"]; exists {
		t.Fatalf("invalid build commit was asserted: %v", got)
	}

	rec = httptest.NewRecorder()
	(&broker{}).routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/version", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /version = %d, want 405", rec.Code)
	}
}

func TestMalformedBuildCommitIsVisibleWithoutLeakingItsValue(t *testing.T) {
	const malformed = "not-a-commit-secret"
	t.Setenv("ROGERAI_BUILD_COMMIT", malformed)
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(old) })

	logBrokerCommitStatus()
	if !strings.Contains(logs.String(), "omitting it from /version") {
		t.Fatalf("invalid build identity was silent: %q", logs.String())
	}
	if strings.Contains(logs.String(), malformed) {
		t.Fatalf("build metadata value leaked into logs: %q", logs.String())
	}
}
