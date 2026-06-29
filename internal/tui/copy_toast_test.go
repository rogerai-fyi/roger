package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestCopiedToast: the shared clipboard confirmation is the clear, prominent
// "✓ Copied to clipboard" (opencode #927 style), with an optional detail suffix.
func TestCopiedToast(t *testing.T) {
	bare := stripANSI(copiedToast(""))
	if bare != "✓ Copied to clipboard" {
		t.Errorf("bare copy toast should be '✓ Copied to clipboard', got %q", bare)
	}
	detailed := stripANSI(copiedToast("the transcript"))
	if !strings.Contains(detailed, "✓ Copied to clipboard") || !strings.Contains(detailed, "the transcript") {
		t.Errorf("detailed copy toast should name what was copied, got %q", detailed)
	}
}

// TestChatCtrlYCopyToast: ctrl+y in the tuned-in chat copies the last reply and shows the
// prominent toast + a clipboard write cmd.
func TestChatCtrlYCopyToast(t *testing.T) {
	m := seedFor(120, modeChat, false)
	m.lastReply = "the station said hello"
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if cmd == nil {
		t.Error("ctrl+y with a reply should return a clipboard write cmd")
	}
	if !strings.Contains(stripANSI(asModel(nm).status), "Copied to clipboard") {
		t.Errorf("ctrl+y should show the prominent copy toast, got status %q", stripANSI(asModel(nm).status))
	}
}

// TestAgentCtrlYCopyToast: ctrl+y in the AGENT copies the transcript with the same toast;
// an empty transcript says "nothing to copy".
func TestAgentCtrlYCopyToast(t *testing.T) {
	base := browseSeed(120)
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	nm, _ := base.enterAgent()
	am := asModel(nm)
	am.agentLines = append(am.agentLines, "◂ here is your answer")

	cm, cmd := am.onAgentKey(tea.KeyMsg{Type: tea.KeyCtrlY})
	if cmd == nil {
		t.Error("agent ctrl+y with a transcript should return a clipboard write cmd")
	}
	if !strings.Contains(stripANSI(asModel(cm).status), "Copied to clipboard") {
		t.Errorf("agent ctrl+y should show the prominent copy toast, got %q", stripANSI(asModel(cm).status))
	}

	// Empty transcript -> nothing to copy (no cmd).
	empty := asModel(nm)
	empty.agentLines = nil
	em, ecmd := empty.onAgentKey(tea.KeyMsg{Type: tea.KeyCtrlY})
	if ecmd != nil {
		t.Error("agent ctrl+y with nothing to copy should not write the clipboard")
	}
	if !strings.Contains(stripANSI(asModel(em).status), "nothing to copy") {
		t.Errorf("agent ctrl+y with an empty transcript should say nothing to copy, got %q", stripANSI(asModel(em).status))
	}
}

// TestAgentFooterAdvertisesCopy: copy is discoverable in the AGENT footer (idle), narrow
// and compact alike - the founder's "discoverable in both chat footers".
func TestAgentFooterAdvertisesCopy(t *testing.T) {
	base := browseSeed(120)
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	nm, _ := base.enterAgent()
	am := asModel(nm)

	for _, w := range []int{120, 44} { // wide + narrow
		am.width = w
		out := stripANSI(am.View())
		if !strings.Contains(out, "copy") {
			t.Errorf("the AGENT footer (w=%d) should advertise copy:\n%s", w, out)
		}
	}
	// Compact too.
	am.width = 120
	am.compact = true
	if !strings.Contains(stripANSI(am.View()), "copy") {
		t.Errorf("the compact AGENT footer should advertise copy:\n%s", stripANSI(am.View()))
	}
}
