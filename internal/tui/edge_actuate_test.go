package tui

// THE ACTUATE CEREMONY on the [3] screen (features/edge/node_identity.feature). Confirming actuate
// licenses a machine to change the physical world, so the screen says what it licenses, asks first,
// and records nothing itself - the host records who.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func actuateModel(t *testing.T, state edge.CapState) (model, *[]string) {
	t.Helper()
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "desk", AuthorityLocal: true, AuthorityHere: true,
		AuthorityAddr: "http://192.168.1.9:8791", Enrolled: true, Name: "desk",
		Discovery: edge.DiscoveryScanning, IntervalS: 30}
	door := nodeOf("d", "shed-door", "board", store.EdgeCap{Name: string(edge.Actuate), State: string(state)})
	m := networkModel(t, self, door)
	confirmed := []string{}
	m.hooks.EdgeConfirmActuate = func(id string) error { confirmed = append(confirmed, id); return nil }
	m.hooks.EdgeHousehold = func() []edge.Instance { return []edge.Instance{{Name: "desk", Agent: true}} }
	m.hooks.EdgeSelfInstance = func() string { return "desk" }
	m.enterEdge()
	for i, it := range m.edgeNetItems() {
		if it.member && it.name == "shed-door" {
			m.edge.netSel, m.edge.netSelSet = i, true
		}
	}
	return m, &confirmed
}

func TestThePanelOffersTheCeremonyOnlyWhileItIsPending(t *testing.T) {
	m, _ := actuateModel(t, edge.Claimed)
	if !strings.Contains(stripANSI(m.edgeView(116)), "Confirm actuate") {
		t.Errorf("a node pending actuate is not offered the ceremony:\n%s", stripANSI(m.edgeView(116)))
	}
	// Already confirmed: nothing to do, so nothing is offered.
	done, _ := actuateModel(t, edge.Verified)
	if strings.Contains(stripANSI(done.edgeView(116)), "Confirm actuate") {
		t.Errorf("a confirmed node is still offered the ceremony")
	}
	// A build that cannot perform it draws no button for it.
	m.hooks.EdgeConfirmActuate = nil
	if strings.Contains(stripANSI(m.edgeView(116)), "Confirm actuate") {
		t.Errorf("a build with no ceremony wired drew the button anyway")
	}
}

func TestTheCeremonyAsksFirstAndSaysWhatItLicenses(t *testing.T) {
	m, confirmed := actuateModel(t, edge.Claimed)
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = asModel(tm)
	if m.edge.actuateID == "" {
		t.Fatalf("pressing c did not arm the ceremony")
	}
	// The QUESTION is the status line, which is where every confirmation on this screen is asked.
	asked := stripANSI(m.status)
	for _, want := range []string{"ACTUATE?", "physical world", "[y/N]", "Your name goes on it"} {
		if !strings.Contains(asked, want) {
			t.Errorf("the ceremony does not say %q before it acts: %q", want, asked)
		}
	}
	if len(*confirmed) != 0 {
		t.Fatalf("arming the ceremony already confirmed it: %v", *confirmed)
	}
	// Anything but y leaves it pending.
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = asModel(tm)
	if len(*confirmed) != 0 {
		t.Fatalf("no confirmed it anyway: %v", *confirmed)
	}
	if !strings.Contains(stripANSI(m.status), "left pending") {
		t.Errorf("declining does not say the node is still pending: %q", stripANSI(m.status))
	}
	// y confirms, through the host.
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = asModel(tm)
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = asModel(tm)
	if len(*confirmed) != 1 {
		t.Fatalf("y did not confirm through the host: %v", *confirmed)
	}
}

// The node page says what is missing, not a bare state word, and names who confirmed once one has.
func TestTheNodePageSaysActuateIsWaitingOnYou(t *testing.T) {
	m, _ := actuateModel(t, edge.Claimed)
	m.edge.netDetail = true
	out := stripANSI(m.edgeView(116))
	for _, want := range []string{"actuate", "awaiting your confirmation", "physical world"} {
		if !strings.Contains(out, want) {
			t.Errorf("the node page does not say actuate is waiting on the owner (%q):\n%s", want, out)
		}
	}
}
