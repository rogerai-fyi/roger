package tui

// [3] offers Health: h runs `roger edge health --live` as a child process (the Limoni screen),
// through the same exec seam the guest-operator handoff uses, and returns to the fleet after.

import (
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func TestHealthKeyOpensTheLiveHealthScreen(t *testing.T) {
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
	if out := stripANSI(m.edgeView(100)); !strings.Contains(out, "Health") {
		t.Fatalf("[3] should offer Health:\n%s", out)
	}
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	if ran == nil {
		t.Fatal("h should run the live health screen")
	}
	if got := strings.Join(ran.Args[1:], " "); got != "edge health --live" {
		t.Fatalf("args = %q", got)
	}
}
