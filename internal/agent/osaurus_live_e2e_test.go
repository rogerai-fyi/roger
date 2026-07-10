package agent

// osaurus_live_e2e_test.go is the Mac-only LIVE integration check for the Osaurus supply
// hardenings, run against a REAL Osaurus server on 127.0.0.1:1337. It is gated behind
// OSAURUS_E2E=1 so it NEVER runs in CI or a normal `go test` (no external dependency in the
// default suite); it is the reproducible evidence the founder brief (section 3) asks for.
//
//   OSAURUS_E2E=1 go test ./internal/agent/ -run TestOsaurusLiveE2E -v
//
// It proves, against the actual server, what the httptest BDD proves in isolation:
//   1. detection brands the :1337 server "osaurus" (not "jan");
//   2. the model-pin makes a tuner naming an UNINSTALLED model still serve the offer;
//   3. the path allowlist refuses /agents (a route real Osaurus DOES expose, unauthenticated
//      on loopback) with a clean 404 and never relays it.

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/detect"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

const osaLiveBase = "http://127.0.0.1:1337/v1"

func TestOsaurusLiveE2E(t *testing.T) {
	if os.Getenv("OSAURUS_E2E") != "1" {
		t.Skip("live Osaurus E2E: set OSAURUS_E2E=1 with a local Osaurus serving on :1337")
	}
	if !detect.IsOsaurus(osaLiveBase) {
		t.Fatalf("no Osaurus detected at %s (start it: osaurus serve)", osaLiveBase)
	}

	// The offered model = whatever Osaurus is actually serving (first from /v1/models).
	found, st := detect.ProbeKey(osaLiveBase, "")
	if st != detect.Reachable || len(found.Models) == 0 {
		t.Fatalf("Osaurus not reachable / no models: status=%v models=%v", st, found.Models)
	}
	offered := found.Models[0]
	if found.Name != "osaurus" {
		t.Errorf("detection should brand the server osaurus, got %q", found.Name)
	}
	t.Logf("live Osaurus offered model = %q (branded %q)", offered, found.Name)

	_, priv, _ := ed25519.GenerateKey(nil)
	client := &http.Client{Timeout: 90 * time.Second}
	cfg := Config{
		NodeID:   "osaurus-e2e",
		Model:    offered,
		Upstream: "http://127.0.0.1:1337/v1/chat/completions",
		Osaurus:  true,
	}

	// 1. MODEL-PIN: a tuner naming a model Osaurus does NOT have still gets served, because the
	//    node pins body.model to the offer before relaying. Without the pin this 404s upstream.
	t.Run("model pin serves the offer despite a bogus model name", func(t *testing.T) {
		job := protocol.Job{ID: "e2e-pin", User: "u", Body: []byte(
			`{"model":"totally-not-installed-9000","messages":[{"role":"user","content":"Reply with the single word OK"}],"max_tokens":8}`)}
		res := serve(cfg, protocol.ModelOffer{Model: offered}, priv, client, job)
		if res.Status != http.StatusOK {
			t.Fatalf("pinned relay status = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
		}
		var d struct {
			Model   string `json:"model"`
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(res.Body, &d); err != nil {
			t.Fatalf("upstream response not JSON: %s", truncate(res.Body))
		}
		if len(d.Choices) == 0 {
			t.Fatalf("no completion returned: %s", truncate(res.Body))
		}
		t.Logf("pin OK: bogus model served as %q, reply=%q", d.Model, strings.TrimSpace(d.Choices[0].Message.Content))

		// Control: with the pin OFF, the same bogus model is rejected upstream (proves the pin
		// is what saved it, not that Osaurus ignores the field).
		noPinCfg := cfg
		noPinCfg.Osaurus = false
		ctl := serve(noPinCfg, protocol.ModelOffer{Model: offered}, priv, client, job)
		if ctl.Status == http.StatusOK {
			t.Logf("note: Osaurus accepted the bogus model even unpinned (status 200) - pin still normalizes it, but this build does not reject unknown models")
		} else {
			t.Logf("control OK: unpinned bogus model rejected upstream with status %d (the pin is load-bearing)", ctl.Status)
		}
	})

	// 2. ALLOWLIST: /agents is a REAL Osaurus route (200, unauthenticated on loopback), yet the
	//    node refuses to relay it - 404 "unsupported path", the backend is never contacted.
	t.Run("allowlist refuses the agents route", func(t *testing.T) {
		// Sanity: the dangerous route really is reachable, so the refusal below is meaningful.
		if resp, err := http.Get("http://127.0.0.1:1337/agents"); err == nil {
			t.Logf("real Osaurus GET /agents -> %d (reachable unauthenticated; this is the surface the allowlist guards)", resp.StatusCode)
			resp.Body.Close()
		}
		job := protocol.Job{ID: "e2e-agents", User: "u", Path: "/agents/abc-123/run",
			Body: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}
		res := serve(cfg, protocol.ModelOffer{Model: offered}, priv, client, job)
		if res.Status != http.StatusNotFound {
			t.Fatalf("agents-route relay status = %d, want 404 (the node must refuse it)", res.Status)
		}
		if !strings.Contains(strings.ToLower(string(res.Body)), "unsupported path") {
			t.Errorf("refusal body = %s, want an 'unsupported path' error", truncate(res.Body))
		}
		t.Logf("allowlist OK: /agents/{id}/run refused with %d %s", res.Status, truncate(res.Body))
	})
}

func truncate(b []byte) string {
	s := string(b)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
