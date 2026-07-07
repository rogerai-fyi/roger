package client

// rc_enrichment_test.go - RED unit table for the deferred OPERATOR FRAME ENRICHMENT
// follow-up (features/operator/rc_enrichment.feature). Pins the ONE shared enriched
// constructor + the additive omitempty wire fields on protocol.RCFrame:
//
//	Model string  `json:"model,omitempty"`  the tuned band's public model identity
//	Spend float64 `json:"spend,omitempty"`  the HOST's own session spend, in dollars
//
// Founder ruling 2 (2026-07-07): the drafted Band field is DROPPED for v1 - RCFrame has
// NO band field at all (the model conveys the station), and the private-band frequency
// secret (ProxyOptions.Freq) must NEVER appear on any frame field. Pinned below.
//
// This test is RED by design: neither the enriched OperatorStatusFrame signature nor the
// RCFrame.{Model,Spend} fields exist yet, so the package fails to COMPILE - the honest
// "fields absent" red for the approval gate. Stdlib testing + encoding/json, no mocks.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestOperatorStatusFrameEnriched pins the enriched constructor: it sets Operator/Model/
// Spend, keeps Kind "status" and the exact "guest has the mic" template, and NEVER
// interpolates enrichment (or any guest content) into Text.
func TestOperatorStatusFrameEnriched(t *testing.T) {
	cases := []struct {
		name     string
		operator string
		model    string
		spend    float64
	}{
		{"full enrichment", "opencode", "gpt-oss-120b", 0.19},
		{"start frame, zero spend", "opencode", "gpt-oss-120b", 0},
		{"open market", "aider", "qwen3-32b-fp8", 1.05},
		{"no model yet", "hermes", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// RED: this 3-arg form does not exist yet (today: OperatorStatusFrame(operator)).
			f := OperatorStatusFrame(tc.operator, tc.model, tc.spend)

			if f.Kind != protocol.RCKindStatus {
				t.Fatalf("kind = %q, want status", f.Kind)
			}
			if f.Operator != tc.operator {
				t.Fatalf("Operator = %q, want %q", f.Operator, tc.operator)
			}
			if f.Model != tc.model {
				t.Fatalf("Model = %q, want %q", f.Model, tc.model)
			}
			if f.Spend != tc.spend {
				t.Fatalf("Spend = %v, want %v", f.Spend, tc.spend)
			}
			// The text is the FIXED template naming the operator only - enrichment (and any
			// guest content) must never be interpolated into it.
			wantText := "guest has the mic: " + tc.operator +
				" - the DJ answers when the handoff ends"
			if f.Text != wantText {
				t.Fatalf("Text = %q, want the fixed operator-only template %q", f.Text, wantText)
			}
			if tc.model != "" && strings.Contains(f.Text, tc.model) {
				t.Fatalf("Text leaked the model into the fixed template: %q", f.Text)
			}
		})
	}
}

// TestOperatorStatusFrameOmitempty pins the ADDITIVE/degrade-clean wire contract: an empty
// model and zero spend are OMITTED from the JSON (an old viewer / un-tuned state sees no
// bogus keys), while operator + kind always ride.
func TestOperatorStatusFrameOmitempty(t *testing.T) {
	// Empty model, zero spend -> the enrichment keys must be absent.
	bare, err := json.Marshal(OperatorStatusFrame("opencode", "", 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"model"`, `"spend"`, `"band"`} {
		if strings.Contains(string(bare), k) {
			t.Fatalf("bare frame wire JSON must omit %s: %s", k, bare)
		}
	}
	for _, k := range []string{`"operator"`, `"kind"`} {
		if !strings.Contains(string(bare), k) {
			t.Fatalf("bare frame wire JSON must carry %s: %s", k, bare)
		}
	}

	// Fully enriched -> model + spend present; a band key still never exists (ruling 2).
	full, err := json.Marshal(OperatorStatusFrame("opencode", "gpt-oss-120b", 0.19))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"model"`, `"spend"`} {
		if !strings.Contains(string(full), k) {
			t.Fatalf("enriched frame wire JSON must carry %s: %s", k, full)
		}
	}
	if strings.Contains(string(full), `"band"`) {
		t.Fatalf("no frame may ever carry a band key (ruling 2): %s", full)
	}
}

// TestRCFrameHasNoBandOrFreqField pins founder ruling 2 structurally: protocol.RCFrame has
// NO Band field (the model conveys the station) and no field whose name or JSON tag could
// carry the private-band frequency secret (ProxyOptions.Freq is hash-at-rest; putting it on
// a frame would leak a private-band secret to every paired device and the relay ring).
func TestRCFrameHasNoBandOrFreqField(t *testing.T) {
	rt := reflect.TypeOf(protocol.RCFrame{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name := strings.ToLower(f.Name)
		tag := strings.ToLower(f.Tag.Get("json"))
		if strings.Contains(name, "band") || strings.Contains(tag, "band") {
			t.Fatalf("RCFrame carries a band field (%s / json:%q) - dropped for v1 by founder ruling 2", f.Name, tag)
		}
		if strings.Contains(name, "freq") || strings.Contains(tag, "freq") {
			t.Fatalf("RCFrame carries a frequency field (%s / json:%q) - the Freq secret must never ride a frame", f.Name, tag)
		}
	}
}

// TestDJBackFrameCarriesNoEnrichment pins that the DJ-back status frame (the corrective
// frame the desk emits on return) carries NO operator/model/spend - nothing runs, so
// the enrichment keys are all omitted, exactly as today.
func TestDJBackFrameCarriesNoEnrichment(t *testing.T) {
	// The DJ-back frame is constructed inline in the TUI (rcEmitDJBack): a plain status
	// frame with only Text. Pin the wire contract of that shape here.
	djBack := protocol.RCFrame{Kind: protocol.RCKindStatus, Text: "the DJ is back at the desk"}
	raw, err := json.Marshal(djBack)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"operator"`, `"band"`, `"model"`, `"spend"`} {
		if strings.Contains(string(raw), k) {
			t.Fatalf("DJ-back frame must omit %s: %s", k, raw)
		}
	}
}
