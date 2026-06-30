package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestTuneInRendersMultipleSameHostBands: after the node_id collision fix, one host
// serving several models registers each as a DISTINCT node id (e.g.
// demo-mac-gpt-oss-20b + demo-mac-qwen3-vl-8b). The tune-in band list must render
// ALL of them, not collapse same-host bands. Each distinct model is its own band row.
func TestTuneInRendersMultipleSameHostBands(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	// Two DISTINCT node ids on the SAME host, each serving a different model.
	m, _ = m.Update(offersMsg{
		{NodeID: "demo-mac-gpt-oss-20b", Model: "gpt-oss-20b", PriceOut: 0.2, Online: true, TPS: 40},
		{NodeID: "demo-mac-qwen3-vl-8b", Model: "qwen3-vl-8b", PriceOut: 0.3, Online: true, TPS: 30},
	})
	m, _ = m.Update(tickMsg{})

	vis := m.(model).visibleBands()
	if len(vis) != 2 {
		t.Fatalf("two same-host bands should both render, got %d: %v", len(vis), names(vis))
	}
	got := map[string]bool{}
	for _, bd := range vis {
		got[bd.model] = true
	}
	if !got["gpt-oss-20b"] || !got["qwen3-vl-8b"] {
		t.Errorf("both same-host models must be present, got %v", names(vis))
	}
	// And both render in the browse view.
	v := stripANSI(m.(model).browseView(120))
	if !strings.Contains(v, "gpt-oss-20b") || !strings.Contains(v, "qwen3-vl-8b") {
		t.Errorf("browse view should show both same-host bands:\n%s", v)
	}
}

// TestTuneInSameModelTwoSameHostStations: when one host runs the SAME model on two
// servers (two distinct node ids), the band carries BOTH stations (so the user sees
// the redundancy / cheapest-of-two), not a single collapsed station.
func TestTuneInSameModelTwoSameHostStations(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = m.Update(offersMsg{
		{NodeID: "demo-mac-gpt-oss-20b-8060", Model: "gpt-oss-20b", PriceOut: 0.5, Online: true, TPS: 20},
		{NodeID: "demo-mac-gpt-oss-20b-8061", Model: "gpt-oss-20b", PriceOut: 0.2, Online: true, TPS: 50},
	})
	m, _ = m.Update(tickMsg{})

	vis := m.(model).visibleBands()
	if len(vis) != 1 {
		t.Fatalf("same model -> one band, got %d: %v", len(vis), names(vis))
	}
	bd := vis[0]
	if bd.stations != 2 {
		t.Errorf("the band should carry BOTH same-host stations, stations=%d", bd.stations)
	}
	if len(bd.all) != 2 {
		t.Errorf("band.all should hold both distinct node ids, got %d", len(bd.all))
	}
	// cheapest-of-two wins the headline route.
	if bd.cheapest == nil || bd.cheapest.NodeID != "demo-mac-gpt-oss-20b-8061" {
		t.Errorf("cheaper same-host station should be the headline route, got %+v", bd.cheapest)
	}
}
