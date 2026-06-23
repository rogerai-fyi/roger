package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRenderBrowse(t *testing.T) {
	// The browse view is now a BAND list (offers grouped by model) showing a
	// cross-station out-price RANGE - not a flat per-station list.
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(offersMsg{
		{NodeID: "demo-node", Region: "home", Model: "gpt-oss-20b", PriceIn: 0.2, PriceOut: 0.3, Ctx: 32768, Online: true},
		{NodeID: "alt-node", Region: "us-w", Model: "gpt-oss-20b", PriceIn: 0.25, PriceOut: 0.41, Ctx: 32768, Online: true},
	})
	m, _ = m.Update(balanceMsg(100))
	m, _ = m.Update(tickMsg{})
	out := m.View()
	// model name, the range column header, the live range, the balance + footer.
	for _, want := range []string{"R O G E R", "gpt-oss-20b", "$/1M out (range)", "0.30 ~ 0.41", "balance", "↑↓ tune"} {
		if !strings.Contains(out, want) {
			t.Errorf("browse view missing %q\n---\n%s", want, out)
		}
	}
}

func TestConnectConfirmAndHelp(t *testing.T) {
	// Enter now opens the cost-confirmation screen FIRST (default DENY); the
	// endpoint binds only on accept.
	mm := New("http://broker.local", "tester")
	mm.proxyAddr = "127.0.0.1:0" // ephemeral port - no fixed-port conflict/leak in tests
	var m tea.Model = mm
	m, _ = m.Update(balanceMsg(42))
	m, _ = m.Update(offersMsg{{NodeID: "nyx-home", Model: "llama-3.3-70b", PriceIn: 0.2, PriceOut: 0.55, Online: true}})

	// select + connect (enter) -> confirmation screen, NOT yet bound
	cm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cv := cm.View()
	if !strings.Contains(cv, "tune in to") || !strings.Contains(cv, "est. cost") {
		t.Errorf("confirm screen not shown:\n%s", cv)
	}
	if strings.Contains(cv, "127.0.0.1:") {
		t.Error("endpoint should NOT bind before the user accepts")
	}

	// accept (enter) -> endpoint binds
	om, _ := cm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(om.View(), "127.0.0.1:") {
		t.Errorf("endpoint panel not shown after accept:\n%s", om.View())
	}

	// deny path: a fresh connect, then esc -> no endpoint, back to browse
	mm2 := New("http://broker.local", "tester")
	mm2.proxyAddr = "127.0.0.1:0"
	var d tea.Model = mm2
	d, _ = d.Update(offersMsg{{NodeID: "n", Model: "m", PriceOut: 0.1, Online: true}})
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEnter}) // -> confirm
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEsc})   // deny
	if strings.Contains(d.View(), "127.0.0.1:") {
		t.Error("deny should not bind an endpoint")
	}

	// help command
	hm, _ := New("x", "y").run("help")
	if !strings.Contains(hm.View(), "commands") {
		t.Error("help view not shown")
	}
}

// TestOverLimitFlow: a band priced over the per-model max enters the over-limit
// screen; raising the max inline (digits + enter) unblocks it into the confirm.
func TestOverLimitFlow(t *testing.T) {
	store := &LimitStore{Models: map[string]Limit{"m": {MaxOut: 0.20}}, TypicalOut: 800}
	mm := NewWith("http://broker.local", "tester", store)
	mm.proxyAddr = "127.0.0.1:0"
	var m tea.Model = mm
	m, _ = m.Update(offersMsg{{NodeID: "n", Model: "m", PriceOut: 0.34, Online: true}})

	// connect -> over-limit (0.34 > 0.20)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ov := m.View()
	if !strings.Contains(ov, "above your limit") {
		t.Fatalf("expected over-limit screen:\n%s", ov)
	}

	// The field is pre-filled to the cheapest price (the smallest unblocking
	// raise). nudge up twice (+0.02 -> 0.36) then save -> re-check into confirm.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.View(), "tune in to") {
		t.Errorf("after raising the max, expected the confirm screen:\n%s", m.View())
	}
	// and the new limit was persisted into the store
	if store.Models["m"].MaxOut < 0.34 {
		t.Errorf("limit not saved/raised, got %v", store.Models["m"].MaxOut)
	}
}
