package tui

// features/edge/health.feature §5: "the alert shows on the fleet surfaces". A member with a firing
// rule wears it on its card line and in its readout; this machine's own alerts show on its card.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
)

func TestFiringAlertsShowOnTheFleetSurfaces(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "desk", AuthorityLocal: true, AuthorityHere: true,
		Enrolled: true, Name: "desk", NodeID: "n_desk", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	nas := store.EdgeNode{ID: "n_nas", Name: "nas", Kind: "host", Presence: string(edge.PresenceVerified),
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: "192.168.1.9:1", Fingerprint: "abab"}}}
	fl := edge.NewFleet(store.NewMem(), "acct-1")
	if _, err := fl.Enroll(nas); err != nil {
		t.Fatal(err)
	}
	h := Hooks{Station: "gm-93", EdgeFleet: fl, EdgeSelf: self.Name,
		EdgeCandidates: func() []store.EdgeNode { return nil },
		EdgeStatus:     func() edge.SelfStatus { return self },
		EdgeAlerts: func() map[string][]string {
			return map[string][]string{"n_nas": {"disk > 75"}, "n_desk": {"cpu_temp > 85"}}
		}}
	var tm tea.Model = NewWithHooks("http://b", "t", nil, h)
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 104, Height: 44})
	m := tm.(model)
	m.enterEdge()

	out := stripANSI(m.edgeView(100))
	if !strings.Contains(out, "1 alert") {
		t.Errorf("the map should count the member's firing alert on its card:\n%s", out)
	}
	for i, it := range m.edgeNetItems() {
		if it.member && it.name == "nas" {
			m.edge.netSel, m.edge.netSelSet = i, true
		}
	}
	m.edge.netDetail = true // open the readout, as enter does
	out = stripANSI(m.edgeView(100))
	if !strings.Contains(out, "ALERT") || !strings.Contains(out, "disk > 75") {
		t.Errorf("the member readout should name the firing rule:\n%s", out)
	}
	for i, it := range m.edgeNetItems() {
		if it.machine {
			m.edge.netSel, m.edge.netSelSet = i, true
		}
	}
	out = stripANSI(m.edgeView(100))
	if !strings.Contains(out, "cpu_temp > 85") {
		t.Errorf("this machine's readout should name its own firing rule:\n%s", out)
	}
	m.edge.netDetail = false
	out = stripANSI(m.edgeView(100))
	if !strings.Contains(out, "LOCAL · 1 alert") {
		t.Errorf("this machine's card should count its firing alert:\n%s", out)
	}
	// A dark member's card is narrow: its label is "DARK · N alert(s)", whole, never truncated - the
	// shared state word (features/edge/fleet_window.feature) where it used to say "offline".
	dark := edgeNetItem{member: true, dark: true, alerts: []string{"offline > 5"}}
	if got := dark.sub(); got != "DARK · 1 alert" {
		t.Errorf("dark member label = %q", got)
	}
}

// The lettered actions the panel advertises on its key caps must work from the keyboard,
// not only from their buttons: f arms the forget question on a member.
func TestForgetKeyWorksFromTheKeyboard(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "desk", AuthorityLocal: true, AuthorityHere: true,
		Enrolled: true, Name: "desk", NodeID: "n_desk", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	fl := edge.NewFleet(store.NewMem(), "acct-1")
	if _, err := fl.Enroll(store.EdgeNode{ID: "n_nas", Name: "nas", Kind: "host", Presence: string(edge.PresenceVerified)}); err != nil {
		t.Fatal(err)
	}
	h := Hooks{Station: "gm-93", EdgeFleet: fl, EdgeSelf: self.Name,
		EdgeCandidates: func() []store.EdgeNode { return nil },
		EdgeStatus:     func() edge.SelfStatus { return self },
		EdgeForget:     func(id string) error { return nil }}
	var tm tea.Model = NewWithHooks("http://b", "t", nil, h)
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 104, Height: 44})
	m := tm.(model)
	m.enterEdge()
	for i, it := range m.edgeNetItems() {
		if it.member && it.name == "nas" {
			m.edge.netSel, m.edge.netSelSet = i, true
		}
	}
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	switch got := tm.(type) { // the edge key path returns a pointer, other paths a value
	case *model:
		m = *got
	case model:
		m = got
	}
	if m.edge.forgetID != "n_nas" {
		t.Fatalf("pressing f must arm the forget question for nas, got forgetID=%q", m.edge.forgetID)
	}
}
