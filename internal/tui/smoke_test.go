package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRenderBrowse(t *testing.T) {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(offersMsg{{NodeID: "demo-node", Region: "home", Model: "gpt-oss-20b", PriceIn: 0.2, PriceOut: 0.3, Ctx: 32768, Online: true}})
	m, _ = m.Update(balanceMsg(100))
	m, _ = m.Update(tickMsg{})
	out := m.View()
	for _, want := range []string{"R O G E R", "demo-node", "gpt-oss-20b", "balance", "↑↓ tune"} {
		if !strings.Contains(out, want) {
			t.Errorf("browse view missing %q", want)
		}
	}
}

func TestConnectAndHelp(t *testing.T) {
	mm := New("http://broker.local", "tester")
	mm.proxyAddr = "127.0.0.1:0" // ephemeral port - no fixed-port conflict/leak in tests
	var m tea.Model = mm
	m, _ = m.Update(offersMsg{{NodeID: "nyx-home", Model: "llama-3.3-70b", Online: true}})
	// select + connect (enter)
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(tm.View(), "127.0.0.1:") {
		t.Error("endpoint panel not shown after connect")
	}
	// help command
	hm, _ := New("x", "y").run("help")
	if !strings.Contains(hm.View(), "commands") {
		t.Error("help view not shown")
	}
}
