package tui

// Rendering guards for the onboarding cabinet (features/edge/onboard.feature). The godog @tui
// suite drives the flow; these pin the pure-render invariants across EVERY step and width - no
// line ever exceeds the terminal, each scene still carries its load-bearing words - and cover the
// branches the scripted flow does not stop on (the new-Edge handshake, the ON AIR lock strip).

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func renderWizModel(t *testing.T) model {
	t.Helper()
	fake := &fakeSetupBackend{defName: "workstation", userKey: strings.Repeat("ab", 32)}
	hooks := Hooks{Station: "gentle-mongoose-93"}
	hooks.EdgeFleet = edge.NewFleet(store.NewMem(), "acct-1")
	hooks.EdgeCandidates = func() []store.EdgeNode { return nil }
	hooks.EdgeStatus = func() edge.SelfStatus {
		return edge.SelfStatus{Authority: "Core", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	}
	setup := fake.hooks()
	hooks.EdgeSetup = &setup
	hooks.EdgeSetupOf = func() EdgeSetupState { return EdgeSetupState{AuthorityAddr: "http://192.168.1.20:8791"} }
	var tm tea.Model = NewWithHooks("http://broker.local", "tester", nil, hooks)
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m := tm.(model)
	m.edgeClock = func() time.Time { return time.Unix(0, 0) }
	m.enterEdge()
	return m
}

// TestWizardNoLineExceedsWidth sweeps every step at every supported width and asserts the
// cabinet never draws past the terminal - the art is sized, never clipped.
func TestWizardNoLineExceedsWidth(t *testing.T) {
	m := renderWizModel(t)
	uk := strings.Repeat("ab", 32)
	steps := []*edgeSetup{
		{open: true, step: setupChoose, name: "workstation", choice: 0},
		{open: true, step: setupChoose, name: "workstation", choice: 1},
		{open: true, step: setupName, name: "workstation"},
		{open: true, step: setupName, join: true, name: "bench"},
		{open: true, step: setupAddr, join: true, name: "bench", addr: "http://192.168.1.10:8791"},
		{open: true, step: setupRunning, join: true, anim: true, frame: 5},
		{open: true, step: setupRunning, join: false, anim: true, frame: 5},
		{open: true, step: setupAllow, join: true, userKey: uk},
		{open: true, step: setupDone, join: false},
		{open: true, step: setupDone, join: true},
		{open: true, step: setupFailed, err: "designate: home is read-only"},
	}
	for _, w := range []int{120, 100, 80, 72, 60, 48} {
		for _, st := range steps {
			m.edge.setup = st
			out := stripANSI(m.edgeView(w))
			for i, ln := range strings.Split(out, "\n") {
				if lipgloss.Width(ln) > w {
					t.Fatalf("step %d at w=%d: line %d is %d wide: %q", st.step, w, i, lipgloss.Width(ln), ln)
				}
			}
		}
	}
}

// TestWizardNewEdgeHandshake covers the new-Edge branch of the handshake scene (the scripted
// flow settles the new-Edge enroll instantly, so it never stops on this frame).
func TestWizardNewEdgeHandshake(t *testing.T) {
	m := renderWizModel(t)
	m.edge.setup = &edgeSetup{open: true, step: setupRunning, join: false, anim: true, frame: 3}
	out := stripANSI(m.edgeView(90))
	if !strings.Contains(out, "raising this machine's Edge") {
		t.Fatalf("new-Edge handshake phase missing:\n%s", out)
	}
	if !strings.Contains(out, "╱█╲") {
		t.Fatalf("new-Edge handshake drew no tower scene:\n%s", out)
	}
}

// TestWizardDoneLockStrip covers the ON AIR payoff: the locked carrier strip and the seal.
func TestWizardDoneLockStrip(t *testing.T) {
	m := renderWizModel(t)
	for _, join := range []bool{false, true} {
		m.edge.setup = &edgeSetup{open: true, step: setupDone, join: join}
		out := stripANSI(m.edgeView(90))
		if !strings.Contains(out, "O N   A I R") {
			t.Fatalf("join=%v: no ON AIR crown:\n%s", join, out)
		}
		if !strings.Contains(out, "CARRIER LOCKED") {
			t.Fatalf("join=%v: no carrier-lock payoff:\n%s", join, out)
		}
		if !strings.Contains(out, "on your Edge") {
			t.Fatalf("join=%v: lost the load-bearing 'on your Edge':\n%s", join, out)
		}
	}
}
