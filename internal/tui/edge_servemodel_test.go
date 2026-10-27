package tui

// features/edge/remote_model.feature (@tui): the owner switches a member's model from the fleet
// screen. The member answers in its own words; this screen only names the model and asks first.

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
)

func jetsonModel(t *testing.T, declares bool) (model, *[]string) {
	t.Helper()
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "desk", AuthorityLocal: true, AuthorityHere: true,
		AuthorityAddr: "http://192.168.1.9:8791", Enrolled: true, Name: "desk", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	var caps []store.EdgeCap
	if declares {
		caps = append(caps, store.EdgeCap{Name: string(edge.ServeControl), State: string(edge.Claimed)})
	}
	j := nodeOf("j", "jetson", "host", caps...)
	j.Instances = []store.EdgeInstance{{Name: "jetson", Bands: []string{"qwen3-4b"}, ServeProven: []string{"qwen3-4b"}, Model: &store.EdgeModel{Band: "qwen3-4b", ID: "unsloth/Qwen3-4B-GGUF:Q4_K_M", Ctx: 8192,
		Have:  []string{"unsloth/Qwen3-4B-GGUF:Q4_K_M", "unsloth/Qwen3-8B-GGUF:Q4_K_M"},
		Fetch: &store.EdgeFetch{File: "Qwen3-14B-Q4_K_M.gguf", Done: 1 << 30, Total: 8 << 30, State: "fetching"}}}}
	m := networkModel(t, self, j)
	asked := &[]string{}
	m.hooks.EdgeServeModel = func(node, id string) (string, error) {
		*asked = append(*asked, node+"|"+id)
		if strings.HasPrefix(id, "nope/") {
			return "", errors.New("jetson refused: this machine's own arithmetic: will not fit")
		}
		return "starting; it serves once proven, and unsloth/Qwen3-4B-GGUF:Q4_K_M keeps serving until then", nil
	}
	m.enterEdge()
	for i, it := range m.edgeNetItems() {
		if it.member && it.name == "jetson" {
			m.edge.netSel, m.edge.netSelSet = i, true
		}
	}
	return m, asked
}

func edgeKey(m model, s string) (model, tea.Cmd) {
	var k tea.KeyMsg
	switch s {
	case "enter":
		k = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		k = tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		k = tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		k = tea.KeyMsg{Type: tea.KeyDown}
	case "backspace":
		k = tea.KeyMsg{Type: tea.KeyBackspace}
	default:
		k = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	tm, cmd := m.onEdgeKey(k)
	switch v := tm.(type) {
	case model:
		return v, cmd
	case *model:
		return *v, cmd
	}
	return m, cmd
}

func TestTheNodePageSaysWhatAMemberServesAndFetches(t *testing.T) {
	m, _ := jetsonModel(t, true)
	m.edge.netDetail = true
	out := stripANSI(m.edgeView(160))
	if !strings.Contains(out, "MODEL") || !strings.Contains(out, `serving "qwen3-4b"`) || !strings.Contains(out, "fetching Qwen3-14B-Q4_K_M.gguf: 1.0 GiB of 8.0 GiB") {
		t.Errorf("the node page does not say what it serves now and what it fetches:\n%s", out)
	}
}

func TestTheOwnerSwitchesAMembersModelFromTheFleetScreen(t *testing.T) {
	m, asked := jetsonModel(t, true)
	var offered bool
	for _, a := range m.edgeActions() {
		if a.key == "M" {
			offered = true
		}
	}
	if !offered {
		t.Fatalf("Serve Model is not offered on a member that turned serve-control on")
	}
	m, _ = edgeKey(m, "M")
	out := stripANSI(m.edgeView(116))
	if !strings.Contains(out, "WHAT SHOULD JETSON SERVE?") || !strings.Contains(out, "unsloth/Qwen3-8B-GGUF:Q4_K_M  on its disk") {
		t.Fatalf("the picker does not list what the member has:\n%s", out)
	}
	// Pick the second model it has; Enter asks; Enter alone is no.
	m, _ = edgeKey(m, "down")
	m, _ = edgeKey(m, "enter")
	if !strings.Contains(stripANSI(m.edgeView(116)), "ASK JETSON TO SERVE unsloth/Qwen3-8B-GGUF:Q4_K_M?") {
		t.Fatalf("the switch was not asked")
	}
	m, _ = edgeKey(m, "enter")
	if len(*asked) != 0 {
		t.Fatalf("Enter alone asked the member: %v", *asked)
	}
	m, _ = edgeKey(m, "enter")
	m, cmd := edgeKey(m, "y")
	if cmd == nil {
		t.Fatalf("y sent nothing")
	}
	msg := cmd()
	tm, _ := m.Update(msg)
	m = tm.(model)
	if len(*asked) != 1 || !strings.HasSuffix((*asked)[0], "|unsloth/Qwen3-8B-GGUF:Q4_K_M") {
		t.Fatalf("the member was not asked for the picked model: %v", *asked)
	}
	if !strings.Contains(stripANSI(m.status), "keeps serving until then") {
		t.Errorf("the member's own answer is not shown: %q", stripANSI(m.status))
	}

	// Typing an id: only an exact owner/repo:QUANT is sent; the member's refusal reaches the owner.
	m, _ = edgeKey(m, "M")
	m, _ = edgeKey(m, "down")
	m, _ = edgeKey(m, "down")
	for _, r := range "qwen3" {
		m, _ = edgeKey(m, string(r))
	}
	m, _ = edgeKey(m, "enter")
	if !strings.Contains(stripANSI(m.status), "owner/repo:QUANT") {
		t.Errorf("a bare name was not refused before sending: %q", stripANSI(m.status))
	}
	for range "qwen3" {
		m, _ = edgeKey(m, "backspace")
	}
	for _, r := range "nope/Big-GGUF:Q8_0" {
		m, _ = edgeKey(m, string(r))
	}
	m, _ = edgeKey(m, "enter")
	m, cmd = edgeKey(m, "y")
	tm, _ = m.Update(cmd())
	m = tm.(model)
	if !strings.Contains(stripANSI(m.status), "own arithmetic") {
		t.Errorf("the member's refusal did not reach the owner: %q", stripANSI(m.status))
	}
	m, _ = edgeKey(m, "M")
	m, _ = edgeKey(m, "esc")
	if m.edge.serveModel.open {
		t.Errorf("esc did not close the picker")
	}
}

func TestAMemberThatNeverTurnedServeControlOnSaysWhereToTurnItOn(t *testing.T) {
	m, asked := jetsonModel(t, false)
	for _, a := range m.edgeActions() {
		if a.key == "M" {
			t.Fatalf("Serve Model offered on a member that never turned it on")
		}
	}
	m, _ = edgeKey(m, "M")
	if !strings.Contains(stripANSI(m.status), "roger edge serve-control on") || m.edge.serveModel.open || len(*asked) != 0 {
		t.Errorf("pressing M did not explain the switch: %q", stripANSI(m.status))
	}
	m.hooks.EdgeServeModel = nil
	m, _ = edgeKey(m, "M")
	if !strings.Contains(stripANSI(m.status), "--on jetson") {
		t.Errorf("a build without the hook does not name the command: %q", stripANSI(m.status))
	}
}

func TestServeModelOnThisMachineIsTheModelsScreen(t *testing.T) {
	m, _ := jetsonModel(t, true)
	f := &modelsFake{view: baseView()}
	m.hooks.Models = f.hooks()
	for i, it := range m.edgeNetItems() {
		if it.machine {
			m.edge.netSel, m.edge.netSelSet = i, true
		}
	}
	m, _ = edgeKey(m, "M")
	if m.mode != modeModels {
		t.Errorf("M on this machine did not open MODELS")
	}
}

// fleet_window.feature §6: an action that cannot work now is shown unavailable, saying why.
func TestServeModelOnAUnitWithNoFaceSaysWhy(t *testing.T) {
	m, asked := jetsonModel(t, true)
	for i := range m.edge.rows {
		m.edge.rows[i].n.Transports = nil
	}
	var label string
	for _, a := range m.edgeActions() {
		if a.key == "M" {
			label = a.sub
		}
	}
	if label != "unavailable: no face to dial" {
		t.Fatalf("Serve Model on a unit with no face reads %q", label)
	}
	m, _ = edgeKey(m, "M")
	if m.edge.serveModel.open || len(*asked) != 0 || !strings.Contains(stripANSI(m.status), "can only be heard") {
		t.Errorf("M on a unit with no face did not say why: %q", stripANSI(m.status))
	}
}
