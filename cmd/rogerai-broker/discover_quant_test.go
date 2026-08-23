package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

// /discover must carry the offer's quant, weights and variant.
//
// Without them every offer decodes with Quant=="" on the consumer side, and the whole
// quant feature - the dial split, the Q filter, the quant exclusions and the standing
// preference rule - silently no-ops against a real broker while passing every test that
// builds its offers synthetically. The protocol has carried these fields since the offer
// commit; this is the wire that was missing.
func TestDiscoverCarriesQuantWeightsAndVariant(t *testing.T) {
	now := time.Now()
	nodes := map[string]protocol.NodeRegistration{
		"quant-node": {
			NodeID: "quant-node", HW: "multi-gpu",
			Offers: []protocol.ModelOffer{{
				Model: "qwen3-8b", PriceOut: 1.0, Ctx: 32768,
				Quant: "Q4_K_M", Weights: "unsloth", Variant: "thinking",
			}},
		},
	}
	b := routeBroker(now, nodes)

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
	if o.Quant != "Q4_K_M" {
		t.Errorf("quant = %q, want Q4_K_M - the dial cannot split what the feed never sends", o.Quant)
	}
	if o.Weights != "unsloth" {
		t.Errorf("weights = %q, want unsloth", o.Weights)
	}
	if o.Variant != "thinking" {
		t.Errorf("variant = %q, want thinking", o.Variant)
	}
}

// An offer with no quant must omit the keys rather than send empty strings, so a consumer
// can tell "this station did not say" from "this station said nothing".
func TestDiscoverOmitsQuantWhenTheStationDidNotSayOne(t *testing.T) {
	now := time.Now()
	nodes := map[string]protocol.NodeRegistration{
		"plain-node": {
			NodeID: "plain-node", HW: "gpu",
			Offers: []protocol.ModelOffer{{Model: "qwen3-8b", PriceOut: 1.0, Ctx: 8192}},
		},
	}
	b := routeBroker(now, nodes)

	rec := httptest.NewRecorder()
	b.discover(rec, httptest.NewRequest(http.MethodGet, "/discover", nil))

	var raw struct {
		Offers []map[string]any `json:"offers"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw.Offers) != 1 {
		t.Fatalf("offers = %d, want 1", len(raw.Offers))
	}
	for _, k := range []string{"quant", "weights", "variant"} {
		if _, present := raw.Offers[0][k]; present {
			t.Errorf("key %q present on an offer that declared none; want omitted", k)
		}
	}
}

// A node's quant, weights and variant are node-SUPPLIED strings that land on a terminal
// row and in a browser table. Putting them on the public feed without sanitising them
// hands every station a way to repaint or wreck the dial for everyone looking at it.
//
// protocol.ModelOffer.Normalize bounds and strips these, and its own comment says the
// broker calls it on every registered offer - but nothing did, so the guarantee was a
// comment. It only became reachable when these fields reached the wire.
func TestHostileQuantLabelsAreSanitisedAtEmission(t *testing.T) {
	now := time.Now()
	hostile := "Q4" + strings.Repeat("A", 400)
	nodes := map[string]protocol.NodeRegistration{
		"hostile-node": {
			NodeID: "hostile-node", HW: "gpu",
			Offers: []protocol.ModelOffer{{
				Model: "qwen3-8b", PriceOut: 1.0, Ctx: 8192,
				Quant:   hostile,
				Weights: "uns\x1b[2Jloth\n\rrow-break",
				Variant: strings.Repeat("x", 500),
			}},
		},
	}
	b := routeBroker(now, nodes)

	rec := httptest.NewRecorder()
	b.discover(rec, httptest.NewRequest(http.MethodGet, "/discover", nil))
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

	for name, got := range map[string]string{"quant": o.Quant, "weights": o.Weights, "variant": o.Variant} {
		if len(got) > 40 {
			t.Errorf("%s is %d bytes on the public feed; an unbounded label is a layout weapon", name, len(got))
		}
		for _, r := range got {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				t.Errorf("%s carries control character %q - it can repaint another operator's terminal", name, r)
			}
		}
	}
}
