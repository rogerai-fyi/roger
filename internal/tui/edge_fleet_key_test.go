package tui

import (
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// [3] offers the live map: o runs `roger edge fleet` (the Limoni fleet screen) as a child process.
func TestLiveMapKeyOpensTheFleetScreen(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "desk", AuthorityLocal: true, AuthorityHere: true,
		Enrolled: true, Name: "desk", NodeID: "n_desk", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	fl := edge.NewFleet(store.NewMem(), "acct-1")
	var ran *exec.Cmd
	prev := operatorExec
	operatorExec = func(c *exec.Cmd, fn func(error) tea.Msg) tea.Cmd { ran = c; return nil }
	t.Cleanup(func() { operatorExec = prev })
	h := Hooks{Station: "gm-93", EdgeFleet: fl, EdgeSelf: self.Name,
		EdgeCandidates: func() []store.EdgeNode { return nil },
		EdgeStatus:     func() edge.SelfStatus { return self },
		EdgeHealthLive: true}
	var tm tea.Model = NewWithHooks("http://b", "t", nil, h)
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 104, Height: 44})
	m := tm.(model)
	m.enterEdge()
	if out := stripANSI(m.edgeView(100)); !strings.Contains(out, "Live map") {
		t.Fatalf("[3] should offer the live map:\n%s", out)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if ran == nil || strings.Join(ran.Args[1:], " ") != "edge fleet" {
		t.Fatalf("o should run `edge fleet`, ran = %v", ran)
	}
}
