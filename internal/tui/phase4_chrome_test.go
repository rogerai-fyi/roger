package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestCaratSlideStampsOnMove: moving the browse cursor stamps caratFrame so the `>` can ease in.
func TestCaratSlideStampsOnMove(t *testing.T) {
	m := seedFor(120, modeBrowse, false)
	m.frame = 9
	if len(m.visibleBands()) < 2 {
		t.Skip("need >=2 bands to move the cursor")
	}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}) // down
	if om := asModel(out); om.caratFrame != 9 {
		t.Errorf("a cursor move should stamp caratFrame=frame (9), got %d", om.caratFrame)
	}
}

// TestCaratGutterEasesThenSettles: the gutter is always 2 cols; it shows " >" (eased in from the
// right) for the slide window after a move, then settles to "> "; a fresh model is settled.
func TestCaratGutterEasesThenSettles(t *testing.T) {
	settled := func(g string) bool { return strings.HasPrefix(stripANSI(g), ">") }
	width := func(g string) int { return len([]rune(stripANSI(g))) }

	fresh := seedFor(120, modeBrowse, false) // caratFrame 0 -> settled
	if g := fresh.caratGutter(); !settled(g) || width(g) != 2 {
		t.Errorf("fresh gutter = %q, want a 2-col settled \"> \"", stripANSI(g))
	}
	m := seedFor(120, modeBrowse, false)
	m.caratFrame, m.frame = 5, 5 // just moved -> sliding
	if g := m.caratGutter(); settled(g) || width(g) != 2 {
		t.Errorf("sliding gutter = %q, want a 2-col eased \" >\"", stripANSI(g))
	}
	m.frame = 5 + caratSlideFrames // window elapsed -> settled
	if g := m.caratGutter(); !settled(g) || width(g) != 2 {
		t.Errorf("post-slide gutter = %q, want settled \"> \"", stripANSI(g))
	}
}

// TestStatusToastStampsOnChange: a status-changing action stamps statusFrame (the toast clock).
func TestStatusToastStampsOnChange(t *testing.T) {
	m := seedFor(120, modeBrowse, false)
	m.frame = 12
	out, _ := m.Update(altM()) // alt+m toggles compact and sets a status line
	if om := asModel(out); om.status == "" || om.statusFrame != 12 {
		t.Errorf("a status change should stamp statusFrame=12; got status=%q frame=%d", stripANSI(om.status), om.statusFrame)
	}
}

// TestStatusToastAutoDismisses: in a MAIN view the tick clears a stale status after toastFrames;
// a fresh status stays; a MODAL screen never auto-clears (the status is its prompt).
func TestStatusToastAutoDismisses(t *testing.T) {
	mk := func(md mode, age int) model {
		m := seedFor(120, md, false)
		m.status = stDim.Render("did a thing")
		m.statusFrame = 5
		m.frame = 5 + age
		return m
	}
	// main view, well past the window -> cleared (the tick's frame++ counts toward elapsed).
	if out, _ := mk(modeBrowse, toastFrames+1).Update(tickMsg{}); asModel(out).status != "" {
		t.Error("a stale status in a main view should auto-dismiss on tick")
	}
	// main view, clearly fresh -> kept
	if out, _ := mk(modeBrowse, 2).Update(tickMsg{}); asModel(out).status == "" {
		t.Error("a fresh status should NOT be dismissed yet")
	}
	// modal screen, stale -> kept (it's the modal's prompt)
	if out, _ := mk(modeOverLimit, toastFrames+5).Update(tickMsg{}); asModel(out).status == "" {
		t.Error("a modal screen's status must NOT auto-dismiss")
	}
}
