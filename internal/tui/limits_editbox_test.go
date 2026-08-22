package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// THE [3] CONFIG EDIT BOX. The founder reported it "a bit skewed/off" twice.
//
// The border was never the problem. The plate was one cell too wide for the content area,
// lipgloss WRAPPED it, and the box grew a second row with "esc / cancel" split across the
// fold - which reads exactly like a box drawn wrong. Two off-by-somethings caused it:
// Style.Width() sets the TOTAL width INCLUDING padding (so passing the content width gives
// the content two cells less than it needs), and MaxWidth does not prevent a wrap - it
// clips a block that has already wrapped.
//
// The other hazard, fixed with it: ⏎ (U+23CE) and ▏ (U+258F) are East-Asian-Width
// AMBIGUOUS, so lipgloss counts them as one cell while a terminal may draw two. Outside a
// box that is harmless; inside one it makes the content row and the borders disagree.

// editBox returns the bordered plate's lines from a CONFIG view at width w.
func editBox(t *testing.T, w int, buf string) []string {
	t.Helper()
	m := browseSeed(w)
	m.mode = modeLimits
	m.limModels = []string{"grok-4.5"}
	m.limCursor, m.editField, m.editBuf = 0, 0, buf
	var box []string
	for _, ln := range strings.Split(stripANSI(m.limitsView(w)), "\n") {
		if strings.ContainsAny(ln, "┌│└") {
			box = append(box, ln)
		}
	}
	return box
}

// THE HEADLINE: exactly three lines (top, one content row, bottom), all the same width,
// at every terminal size. A fourth line means it wrapped.
func TestEditBoxIsOneRowAndSquareAtEveryWidth(t *testing.T) {
	for _, w := range []int{30, 40, 50, 60, 80, 100, 120, 200} {
		box := editBox(t, w, "1.25")
		if len(box) != 3 {
			t.Errorf("w=%d: the edit box has %d lines, want 3 (a 4th means the content wrapped):\n%s",
				w, len(box), strings.Join(box, "\n"))
			continue
		}
		// The top line carries the 2-space indent; the other two do not.
		top := lipgloss.Width(box[0]) - 2
		for i, ln := range box[1:] {
			if got := lipgloss.Width(ln); got != top {
				t.Errorf("w=%d: box line %d is %d wide, top border is %d - the box is skewed:\n%s",
					w, i+1, got, top, strings.Join(box, "\n"))
			}
		}
		if lipgloss.Width(box[0]) > w {
			t.Errorf("w=%d: the box is %d wide - it runs off the screen", w, lipgloss.Width(box[0]))
		}
	}
}

// THE VALUE NEVER GETS CLIPPED. What the fit ladder drops is the keys, then the model
// name, then the field label - never the number being typed. A clipped value is a value
// the operator cannot trust, and they are looking straight at it.
func TestEditBoxKeepsTheValueAtEveryWidth(t *testing.T) {
	for _, w := range []int{30, 40, 60, 100} {
		box := editBox(t, w, "12.75")
		joined := strings.Join(box, "\n")
		if !strings.Contains(joined, "[12.75]") {
			t.Errorf("w=%d: the value being edited was clipped out of the box:\n%s", w, joined)
		}
	}
}

// NO AMBIGUOUS-WIDTH GLYPHS INSIDE THE BORDER. lipgloss and the terminal can disagree
// about these by a cell each, which is precisely how a square box renders crooked.
func TestEditBoxUsesNoAmbiguousWidthGlyphs(t *testing.T) {
	joined := strings.Join(editBox(t, 100, "1.25"), "\n")
	for _, bad := range []string{"⏎", "▏"} {
		if strings.Contains(joined, bad) {
			t.Errorf("the edit box contains the ambiguous-width glyph %q - it will not line up on every terminal:\n%s",
				bad, joined)
		}
	}
	// And it still teaches the keys in words at a normal width.
	if !strings.Contains(joined, "enter save") {
		t.Errorf("the box stopped teaching its keys:\n%s", joined)
	}
}
