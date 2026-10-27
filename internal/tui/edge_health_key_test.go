package tui

// [3] offers Health: h opens the fleet window (`roger edge fleet`, the Limoni rack) with the
// selected unit's health open, as a child process through the same exec seam the guest-operator
// handoff uses, and returns to [3] after. HEALTH is folded into the window's FLEET tab
// (features/edge/fleet_window.feature, WHERE IT LIVES), so h never opens a second list of the same
// units; `roger edge health` stays a CLI command of its own.

import (
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
)

func TestHealthKeyOpensTheFleetWindowWithHealthOpen(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "desk", AuthorityLocal: true, AuthorityHere: true,
		Enrolled: true, Name: "desk", NodeID: "n_desk", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	for _, c := range []struct {
		name     string
		onMember bool
	}{
		{"on this machine", false},
		{"on another unit", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			fl := edge.NewFleet(store.NewMem(), "acct-1")
			nas := edgeAddNode(t, fl, "nas", nil)
			var ran *exec.Cmd
			prev := operatorExec
			operatorExec = func(cmd *exec.Cmd, fn func(error) tea.Msg) tea.Cmd { ran = cmd; return nil }
			t.Cleanup(func() { operatorExec = prev })
			h := Hooks{Station: "gm-93", EdgeFleet: fl, EdgeSelf: self.Name,
				EdgeCandidates: func() []store.EdgeNode { return nil },
				EdgeStatus:     func() edge.SelfStatus { return self },
				EdgeHealthLive: true}
			var tm tea.Model = NewWithHooks("http://b", "t", nil, h)
			tm, _ = tm.Update(tea.WindowSizeMsg{Width: 104, Height: 44})
			m := tm.(model)
			m.enterEdge()
			want := "edge fleet --health"
			for i, it := range m.edgeNetItems() {
				if (c.onMember && it.member) || (!c.onMember && it.machine) {
					m.edge.netSel, m.edge.netSelSet = i, true
				}
			}
			if c.onMember {
				want = "edge fleet --select " + nas.ID + " --health" // the window opens on the unit the owner was on
			}
			require.Contains(t, stripANSI(m.edgeView(100)), "Health", "[3] should offer Health here")
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
			require.NotNil(t, ran, "h should open the fleet window")
			require.Equal(t, want, strings.Join(ran.Args[1:], " "), "never the standalone `edge health --live`")
		})
	}
}
