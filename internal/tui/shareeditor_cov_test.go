package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// edKey maps the editor key names this test drives to their tea.KeyMsg types (k.String()
// yields exactly these names back inside onShareEditorKey).
func edKey(name string) tea.KeyMsg {
	switch name {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	return tea.KeyMsg{}
}

// seedShareEditor builds a logged-in model parked in the pricing/schedule editor for one
// model, with a single time-of-use window so the window sub-fields are reachable.
func seedShareEditor() *model {
	m := browseSeed(100)
	m.loggedIn = true
	m.mode = modeShareEditor
	m.edModel = "gpt-oss-20b"
	m.edPriceIn = "0.2"
	m.edPriceOut = "0.3"
	m.edWindows = []SchedWindow{{Start: "18:00", End: "22:00", In: 0.1, Out: 0.2}}
	m.edField = edFieldOut
	m.edWinSub = winSubStart
	return &m
}

// TestShareEditorKeyNavAndEdits drives onShareEditorKey across every branch: tab/shift+tab
// field cycling, add (a) and delete (d) of a window, free (f) toggle, right/left sub-field
// cycling within a window, typing digits/colon into each sub-field, backspace, and the
// digit edit on the headline out-price.
func TestShareEditorKeyNavAndEdits(t *testing.T) {
	// tab advances the focused field; shift+tab steps back.
	m := seedShareEditor()
	m.onShareEditorKey(edKey("tab"))
	if m.edField != edFieldAddWin {
		t.Errorf("tab from out should land add-window, got %d", m.edField)
	}
	m.onShareEditorKey(edKey("shift+tab"))
	if m.edField != edFieldOut {
		t.Errorf("shift+tab should return to out, got %d", m.edField)
	}

	// Type a digit + dot into the out-price.
	d := seedShareEditor()
	d.edPriceOut = ""
	d.onShareEditorKey(keyMsg("5"))
	d.onShareEditorKey(keyMsg("."))
	d.onShareEditorKey(keyMsg("7"))
	if d.edPriceOut != "5.7" {
		t.Errorf("digit/dot typing should build 5.7, got %q", d.edPriceOut)
	}
	// Backspace trims it.
	d.onShareEditorKey(edKey("backspace"))
	if d.edPriceOut != "5." {
		t.Errorf("backspace should trim to 5., got %q", d.edPriceOut)
	}

	// Add a window: focus jumps to the new (second) window row.
	a := seedShareEditor()
	a.onShareEditorKey(keyMsg("a"))
	if len(a.edWindows) != 2 || a.edField != edFieldFirstWin+1 {
		t.Errorf("a should add a window and focus it, len=%d field=%d", len(a.edWindows), a.edField)
	}

	// On a window row, right cycles the sub-field; typing edits End / In / Out / Start.
	w := seedShareEditor()
	w.edField = edFieldFirstWin
	w.edWinSub = winSubStart
	w.edWindows[0].In = 0 // start the price buffers empty so typed digits are the whole value
	w.edWindows[0].Out = 0
	// Start sub-field: type a colon+digits.
	w.edWindows[0].Start = ""
	w.onShareEditorKey(keyMsg("9"))
	if w.edWindows[0].Start != "9" {
		t.Errorf("typing on Start should edit it, got %q", w.edWindows[0].Start)
	}
	// right -> End.
	w.onShareEditorKey(edKey("right"))
	if w.edWinSub != winSubEnd {
		t.Errorf("right should advance to End sub-field, got %d", w.edWinSub)
	}
	w.edWindows[0].End = ""
	w.onShareEditorKey(keyMsg(":"))
	if !strings.Contains(w.edWindows[0].End, ":") {
		t.Errorf("colon should be accepted on the End time, got %q", w.edWindows[0].End)
	}
	// right -> In: typing reflects into the float.
	w.onShareEditorKey(edKey("right"))
	if w.edWinSub != winSubIn {
		t.Fatalf("right should advance to In sub-field, got %d", w.edWinSub)
	}
	w.onShareEditorKey(keyMsg("0"))
	w.onShareEditorKey(keyMsg("."))
	w.onShareEditorKey(keyMsg("5"))
	if w.edWindows[0].In != 0.5 {
		t.Errorf("typing 0.5 on In should set the float, got %v", w.edWindows[0].In)
	}
	// right -> Out.
	w.onShareEditorKey(edKey("right"))
	if w.edWinSub != winSubOut {
		t.Fatalf("right should advance to Out sub-field, got %d", w.edWinSub)
	}
	w.onShareEditorKey(keyMsg("9"))
	if w.edWindows[0].Out != 9 {
		t.Errorf("typing 9 on Out should set the float, got %v", w.edWindows[0].Out)
	}
	// left wraps back through the sub-fields.
	w.onShareEditorKey(edKey("left"))
	if w.edWinSub != winSubIn {
		t.Errorf("left should step back to In, got %d", w.edWinSub)
	}

	// f toggles the focused window FREE.
	f := seedShareEditor()
	f.edField = edFieldFirstWin
	was := f.edWindows[0].Free
	f.onShareEditorKey(keyMsg("f"))
	if f.edWindows[0].Free == was {
		t.Error("f should toggle the window FREE flag")
	}

	// d deletes the focused window and re-homes focus to the out-price.
	del := seedShareEditor()
	del.edField = edFieldFirstWin
	del.onShareEditorKey(keyMsg("d"))
	if len(del.edWindows) != 0 || del.edField != edFieldOut {
		t.Errorf("d should delete the window and re-home focus, len=%d field=%d", len(del.edWindows), del.edField)
	}

	// esc cancels back to the share table.
	e := seedShareEditor()
	em, _ := e.onShareEditorKey(edKey("esc"))
	if asModel(em).mode != modeShare {
		t.Errorf("esc should return to the share table, got %v", asModel(em).mode)
	}
}

// TestShareEditorEnterSavesOrBlocks: a clean editor commits and returns to the table; a
// bad price keeps the editor open with an inline error.
func TestShareEditorEnterSavesOrBlocks(t *testing.T) {
	// Clean save.
	ok := seedShareEditor()
	ok.edWindows = nil
	ok.edPriceOut = "0.5"
	ok.edPriceIn = "0.2"
	om, _ := ok.onShareEditorKey(edKey("enter"))
	if asModel(om).mode != modeShare {
		t.Errorf("a clean save should return to the share table, got %v", asModel(om).mode)
	}

	// Bad price blocks: stays in the editor.
	bad := seedShareEditor()
	bad.edWindows = nil
	bad.edPriceOut = "1.2.3"
	bm, _ := bad.onShareEditorKey(edKey("enter"))
	if asModel(bm).mode != modeShareEditor {
		t.Errorf("a bad price should keep the editor open, got %v", asModel(bm).mode)
	}
}
