package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestOfferDecodesNewFields: the TUI offer struct now decodes the fields /discover
// already sends (ttft_ms, success, success_seen, verified, hw, ctx_estimated, terms),
// which were previously DROPPED on decode.
func TestOfferDecodesNewFields(t *testing.T) {
	raw := `{"offers":[{
		"node_id":"keqxz-qwen3-8b","region":"us-west-2","hw":"multi-gpu","model":"qwen3-8b",
		"price_in":0.10,"price_out":0.40,"ctx":131072,"ctx_estimated":false,"online":true,
		"confidential":false,"free_now":false,"tps":142,"ttft_ms":180,
		"success":0.98,"success_seen":true,"verified":true,"signal":71,
		"terms":{"supply":4,"speed":13,"latency":10,"verified":20,"success":14,"trust":10,"total":71}
	}]}`
	var d struct {
		Offers []offer `json:"offers"`
	}
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	o := d.Offers[0]
	if o.HW != "multi-gpu" {
		t.Errorf("hw not decoded: %q", o.HW)
	}
	if o.TTFTMs != 180 {
		t.Errorf("ttft_ms not decoded: %v", o.TTFTMs)
	}
	if o.SuccessRate != 0.98 || !o.SuccessSeen {
		t.Errorf("success/seen not decoded: %v / %v", o.SuccessRate, o.SuccessSeen)
	}
	if !o.Verified {
		t.Errorf("verified not decoded")
	}
	if o.CtxEstimated {
		t.Errorf("ctx_estimated wrong: a detected 131072 must NOT be estimated")
	}
	if o.Terms.Total != 71 || o.Terms.Verified != 20 {
		t.Errorf("terms not decoded: %+v", o.Terms)
	}
}

// detailModel builds a browse model on a single rich band and opens its expanded
// station view ([i]), returning the rendered view string.
func detailModel(t *testing.T, offers ...offer) string {
	t.Helper()
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(offersMsg(offers))
	m, _ = m.Update(tickMsg{})
	m = keyPress(m, "i") // open the expanded station log on the band under the cursor
	if m.(model).mode != modeBandDetail {
		t.Fatalf("i did not open modeBandDetail (mode=%d)", m.(model).mode)
	}
	return m.(model).View()
}

// TestBandDetailRendersRealFields: the expanded station view shows the real per-station
// metrics (ctx, ttft, success%, hw class) and the signal-term breakdown.
func TestBandDetailRendersRealFields(t *testing.T) {
	view := detailModel(t, offer{
		NodeID: "keqxz-qwen3-8b", Region: "us-west-2", HW: "multi-gpu", Model: "qwen3-8b",
		PriceIn: 0.10, PriceOut: 0.40, Ctx: 131072, CtxEstimated: false, Online: true,
		TPS: 142, TTFTMs: 180, SuccessRate: 0.98, SuccessSeen: true, Verified: true, Signal: 71,
		Terms: signalTerms{Supply: 4, Speed: 13, Latency: 10, Verified: 20, Success: 14, Trust: 10, Total: 71},
	})
	for _, want := range []string{"STATION LOG", "qwen3-8b", "US-W", "131k", "180ms", "98%", "multi-gpu"} {
		if !strings.Contains(view, want) {
			t.Errorf("expanded view missing %q\n---\n%s", want, view)
		}
	}
	// The signal-term breakdown line ("why is this a 71?").
	for _, want := range []string{"supply 4", "verified 20", "success 14", "trust 10"} {
		if !strings.Contains(view, want) {
			t.Errorf("signal-term breakdown missing %q\n---\n%s", want, view)
		}
	}
}

// TestBandDetailHonestEmpty: an unproven station renders "no data" for success (not a
// fabricated %), "~" + the estimated window for an estimated ctx, and a dim "-" for a
// missing region (never "??").
func TestBandDetailHonestEmpty(t *testing.T) {
	view := detailModel(t, offer{
		NodeID: "tuwm-llama", Region: "", HW: "apple", Model: "llama-3.1-8b",
		PriceIn: 0, PriceOut: 0, Ctx: 32768, CtxEstimated: true, Online: true, FreeNow: true,
		// no tps / ttft / success evidence, not verified
		SuccessSeen: false,
	})
	if !strings.Contains(view, "no data") {
		t.Errorf("unseen success must render 'no data', not a %%\n---\n%s", view)
	}
	if !strings.Contains(view, "~33k") && !strings.Contains(view, "~32k") {
		t.Errorf("estimated ctx must render with a leading ~\n---\n%s", view)
	}
	if strings.Contains(view, "??") {
		t.Errorf("missing region must never render '??'\n---\n%s", view)
	}
	if !strings.Contains(view, "apple") {
		t.Errorf("hw class chip missing\n---\n%s", view)
	}
}

// TestCoarseRegionTUI: the TUI's coarseRegion agrees with the web (buckets to a macro
// region; empty/unmatched -> "" so the cell renders a dim "-", never "??").
func TestCoarseRegionTUI(t *testing.T) {
	cases := map[string]string{
		"us-west-2": "US-W", "iad": "US-E", "frankfurt": "DE",
		"home": "", "": "", "mars-base-1": "",
	}
	for in, want := range cases {
		if got := coarseRegion(in); got != want {
			t.Errorf("coarseRegion(%q) = %q, want %q", in, got, want)
		}
	}
	if regionCell("") != "-" {
		t.Errorf("regionCell(\"\") = %q, want \"-\"", regionCell(""))
	}
}

// TestSuccessCellHonest: a SEEN rate renders "NN%"; an UNSEEN one renders "no data".
func TestSuccessCellHonest(t *testing.T) {
	if got := successCell(0.98, true); got != "98%" {
		t.Errorf("seen success cell = %q, want 98%%", got)
	}
	if got := successCell(0, false); got != "no data" {
		t.Errorf("unseen success cell = %q, want 'no data'", got)
	}
}

// TestBandVerifiedDistinctFromConfidential: a verified-serving station lights the
// browse badge with the lineage ✓, kept distinct from the confidential ◆.
func TestBandVerifiedDistinctFromConfidential(t *testing.T) {
	bands := groupBands([]offer{{
		NodeID: "a-m", Model: "m", Online: true, Verified: true, PriceOut: 1,
	}}, nil)
	if len(bands) != 1 || !bands[0].verified {
		t.Fatalf("band.verified not set from an online verified offer: %+v", bands)
	}
	if bands[0].lineage != 0 {
		t.Errorf("verified must NOT imply confidential lineage (got %d)", bands[0].lineage)
	}
}
