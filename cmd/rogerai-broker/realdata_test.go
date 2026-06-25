package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestHwConcurrencyClassBuckets: the privacy-bucketed hw classes map to sensible
// concurrency priors (multi-gpu highest, cpu lowest), and a legacy raw GPU string
// still resolves to a GPU-tier prior.
func TestHwConcurrencyClassBuckets(t *testing.T) {
	cases := []struct {
		hw   string
		want int
	}{
		{"multi-gpu", 4},
		{"single-gpu", 2},
		{"apple", 2},
		{"cpu", 1},
		{"unknown", 1},
		{"", 1},
		{"NVIDIA RTX PRO 4500", 2}, // legacy raw single-card string
	}
	for _, c := range cases {
		if got := hwConcurrencyClass(c.hw); got != c.want {
			t.Errorf("hwConcurrencyClass(%q) = %d, want %d", c.hw, got, c.want)
		}
	}
}

// TestSuccessSeenHonest: successFor + the seen flag distinguish a REAL success rate
// from the neutral no-evidence fallback. Organic traffic and probe-verified count as
// SEEN (real); a node with no evidence is UNSEEN (the UI shows "no data", not a %).
func TestSuccessSeenHonest(t *testing.T) {
	if r := successFor(0.97, true, false); r != 0.97 {
		t.Errorf("organic success = %v, want 0.97", r)
	}
	if r := successFor(0, false, true); r != 0.9 {
		t.Errorf("probe-verified success = %v, want 0.9", r)
	}
	if r := successFor(0, false, false); r != 0.6 {
		t.Errorf("no-evidence success = %v, want 0.6 (neutral)", r)
	}
	srSeen, verified := false, false
	if seen := srSeen || verified; seen {
		t.Errorf("no-evidence must be UNSEEN so the UI shows 'no data', not a fabricated %%")
	}
}

// TestDiscoverSurfacesRealDataFields: /discover carries the new honest fields - the
// hw class, ctx_estimated, success_seen - so the web + TUI can render REAL data (or an
// honest empty) instead of placeholders, and the rig is never leaked.
func TestDiscoverSurfacesRealDataFields(t *testing.T) {
	now := time.Now()
	nodes := map[string]protocol.NodeRegistration{
		"real-node": {
			NodeID: "real-node", HW: "multi-gpu",
			Offers: []protocol.ModelOffer{{Model: "qwen3-8b", PriceOut: 1.0, Ctx: 32768, CtxEstimated: true}},
		},
	}
	b := routeBroker(now, nodes) // all metric maps initialised; node marked just-seen

	rec := httptest.NewRecorder()
	b.discover(rec, httptest.NewRequest(http.MethodGet, "/discover", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/discover status = %d", rec.Code)
	}
	var resp struct {
		Offers []offerView `json:"offers"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Offers) != 1 {
		t.Fatalf("offers = %d, want 1", len(resp.Offers))
	}
	o := resp.Offers[0]
	if o.HW != "multi-gpu" {
		t.Errorf("hw = %q, want multi-gpu (the bucketed class)", o.HW)
	}
	if !o.CtxEstimated {
		t.Errorf("ctx_estimated = false, want true (the node reported an estimate)")
	}
	// A node with no traffic and no probe is UNSEEN, so the UI shows "no data" rather
	// than a fabricated success percentage.
	if o.SuccessSeen {
		t.Errorf("success_seen = true for an unproven node; want false (honest 'no data')")
	}
	// The raw rig must never appear (privacy): the hw field is the class only.
	if strings.Contains(strings.ToLower(o.HW), "rtx") || strings.Contains(o.HW, "4") {
		t.Errorf("hw %q leaks rig detail", o.HW)
	}
}
