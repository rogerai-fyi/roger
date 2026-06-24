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

// TestEmptyStates: before any scan, the browse view shows the "tuning in"
// loading line; after a scan returns empty (broker serializes offers as null),
// it must flip to the idle "band is quiet" standing-by line, NOT stay on the
// loading pose; a broker drop shows the "...static" line.
func TestEmptyStates(t *testing.T) {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 92, Height: 30})
	if !strings.Contains(m.View(), "tuning in") {
		t.Errorf("pre-scan should show the loading line:\n%s", m.View())
	}
	// An empty scan (offers null -> nil slice) must reach the idle line.
	m, _ = m.Update(offersMsg(nil))
	if !strings.Contains(m.View(), "the band is quiet") {
		t.Errorf("empty scan should show the standing-by line, not loading:\n%s", m.View())
	}
	// A broker drop shows the static line.
	d, _ := New("http://broker.local", "tester").Update(tea.WindowSizeMsg{Width: 92, Height: 30})
	d, _ = d.Update(errMsg("broker unreachable: x"))
	if !strings.Contains(d.View(), "static") {
		t.Errorf("broker drop should show the static line:\n%s", d.View())
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
	if !strings.Contains(cv, "open channel") || !strings.Contains(cv, "/ reply") {
		t.Errorf("confirm screen not shown:\n%s", cv)
	}
	if strings.Contains(cv, "127.0.0.1:") {
		t.Error("endpoint should NOT bind before the user accepts")
	}

	// accept (enter) -> endpoint binds AND we auto-switch to CHANNEL mode with a
	// compact header (compact-on-connect). The endpoint is revealed via /endpoint.
	om, _ := cm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ov := om.View()
	if !strings.Contains(ov, "CHANNEL") || !strings.Contains(ov, "on channel nyx-home") {
		t.Errorf("expected compact CHANNEL view after accept:\n%s", ov)
	}
	// /endpoint in-session surfaces the bound 127.0.0.1 endpoint.
	em := om
	em, _ = em.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "endpoint" {
		em, _ = em.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	em, _ = em.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(em.View(), "127.0.0.1:") {
		t.Errorf("/endpoint should surface the bound endpoint:\n%s", em.View())
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

// TestDollarsAdaptivePrecision: balances at 2dp, tiny costs keep significant
// digits and never collapse to $0.00.
func TestDollarsAdaptivePrecision(t *testing.T) {
	cases := map[float64]string{
		0:         "$0.00",
		12.34:     "$12.34",
		0.01:      "$0.01",
		0.000123:  "$0.000123",
		0.0000005: "$0.0000005",
	}
	for in, want := range cases {
		if got := dollars(in); got != want {
			t.Errorf("dollars(%v) = %q, want %q", in, got, want)
		}
	}
	// a real sub-cent cost must never read as $0.00
	if dollars(0.0004) == "$0.00" {
		t.Error("sub-cent cost collapsed to $0.00")
	}
}

// TestLiveInputEcho proves the input-bug fix: a typed command echoes into the
// view LIVE, before Enter, in a clearly labeled `rog ›` prompt; likewise the
// channel `you ›` prompt echoes live before send. Regression guard for the bug
// where the user saw nothing until pressing Enter.
func TestLiveInputEcho(t *testing.T) {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	// browse always shows the labeled prompt + the press-/ hint.
	if !strings.Contains(m.View(), "rog ›") {
		t.Fatalf("browse view missing the rog prompt:\n%s", m.View())
	}
	// enter command mode and type, WITHOUT pressing enter - it must echo live.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "search" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if !strings.Contains(m.View(), "search") {
		t.Errorf("command input did not echo live before Enter:\n%s", m.View())
	}

	// channel prompt echoes live too. Connect first.
	cm := New("http://broker.local", "tester")
	cm.proxyAddr = "127.0.0.1:0"
	var c tea.Model = cm
	c, _ = c.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	c, _ = c.Update(offersMsg{{NodeID: "n", Model: "m", PriceOut: 0.1, Online: true}})
	c, _ = c.Update(tea.KeyMsg{Type: tea.KeyEnter}) // confirm
	c, _ = c.Update(tea.KeyMsg{Type: tea.KeyEnter}) // accept -> connected
	c, _ = c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	for _, r := range "hello" {
		c, _ = c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	cv := c.View()
	if !strings.Contains(cv, "you ›") || !strings.Contains(cv, "hello") {
		t.Errorf("channel input did not echo live before send:\n%s", cv)
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
	if !strings.Contains(m.View(), "open channel") {
		t.Errorf("after raising the max, expected the confirm screen:\n%s", m.View())
	}
	// and the new limit was persisted into the store
	if store.Models["m"].MaxOut < 0.34 {
		t.Errorf("limit not saved/raised, got %v", store.Models["m"].MaxOut)
	}
}
