package main

// context_stranger_test.go drives the CLI `roger context publish|resolve` verbs (Stage 3) end
// to end through a content-blind rendezvous stub: publish a summary capsule, scrape the
// printed one-time code, resolve it back. REAL seal/open crypto; the stub is only the store.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/rogerai-fyi/roger/internal/capsule"
)

func rendezvousServer(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	store := map[string]string{}
	mux := http.NewServeMux()
	mux.HandleFunc("/capsule", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct{ Lookup, Blob string }
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		store[req.Lookup] = req.Blob
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/capsule/resolve", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct{ Lookup string }
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		blob, ok := store[req.Lookup]
		delete(store, req.Lookup)
		mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such capsule"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"blob": blob})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// summaryCapsuleFile writes a signed summary-only capsule and returns its path.
func summaryCapsuleFile(t *testing.T) string {
	t.Helper()
	d := capsule.SummaryOnly(capsule.Draft{
		ID:        "cap_cli",
		Thread:    capsule.Thread{OriginThreadID: "t1", BaseWatermark: 1},
		Redaction: "full",
		Messages:  []capsule.Message{{Role: "user", Content: "current", XRoger: capsule.XRoger{Turn: 0, Agent: "user"}}},
	})
	c, err := capsule.Export(d, ephemeralKey(t), "roger-cli", func() int64 { return 100 })
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := c.Marshal()
	p := filepath.Join(t.TempDir(), "convo.rcap.json")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

var codeLine = regexp.MustCompile(`(?m)^\s{2}(\S.*\S)\s*$`)

func TestContextPublishResolveRoundTrip(t *testing.T) {
	srv := rendezvousServer(t)
	cfg := config{Broker: srv.URL}
	file := summaryCapsuleFile(t)

	out, err := captureCtxStdout(func() error { return cmdContext(cfg, []string{"publish", file}) })
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	// scrape the printed one-time code (the indented line).
	var code string
	for _, m := range codeLine.FindAllStringSubmatch(out, -1) {
		if strings.Contains(m[1], "MHz") {
			code = m[1]
			break
		}
	}
	if code == "" {
		t.Fatalf("publish did not print a code:\n%s", out)
	}

	sum, err := captureCtxStdout(func() error { return cmdContext(cfg, []string{"resolve", code}) })
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.Contains(sum, "verified capsule") || !strings.Contains(sum, "redaction=summary") {
		t.Fatalf("resolve summary unexpected:\n%s", sum)
	}
	// one-time: a second resolve of the same code is gone.
	if _, err := captureCtxStdout(func() error { return cmdContext(cfg, []string{"resolve", code}) }); err == nil {
		t.Fatal("second resolve must fail (one-time, delete-on-read)")
	}
}

func TestContextPublishRefusesFull(t *testing.T) {
	srv := rendezvousServer(t)
	cfg := config{Broker: srv.URL}
	// a FULL capsule file must be refused by publish (redaction floor).
	c, _ := capsule.Export(capsule.Draft{
		ID: "cap_full", Thread: capsule.Thread{BaseWatermark: 1}, Redaction: "full",
		Messages: []capsule.Message{{Role: "user", Content: "x", XRoger: capsule.XRoger{Turn: 0}}},
	}, ephemeralKey(t), "roger-cli", func() int64 { return 100 })
	raw, _ := c.Marshal()
	p := filepath.Join(t.TempDir(), "full.rcap.json")
	_ = os.WriteFile(p, raw, 0o600)
	if _, err := captureCtxStdout(func() error { return cmdContext(cfg, []string{"publish", p}) }); err == nil {
		t.Fatal("publishing a full capsule to a stranger must be refused")
	}
}

func TestContextPublishResolveNeedBroker(t *testing.T) {
	if err := cmdContext(config{}, []string{"publish", "x.json"}); err == nil {
		t.Error("publish without a broker must error")
	}
	if err := cmdContext(config{}, []string{"resolve", "somecode"}); err == nil {
		t.Error("resolve without a broker must error")
	}
	if err := cmdContext(config{Broker: "http://x"}, []string{"resolve"}); err == nil {
		t.Error("resolve without a code must error")
	}
}
