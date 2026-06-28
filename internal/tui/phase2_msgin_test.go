package tui

import (
	"reflect"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestRevealBlockDimsThenSettles pins the message-in "ink-settling": a freshly-appended block
// (entries [from:]) is re-styled dim for the first msgRevealFrames frames of its age, keeping
// the text, then settles to the original styling. Frozen under reduced motion; no-op when out
// of range. Pure in (lines, from, age, reduce).
func TestRevealBlockDimsThenSettles(t *testing.T) {
	lines := []string{"intro", stLive.Render("◂ hello there"), "footer · cost"}
	from := 1

	// At age 0 the block [from:] is re-styled to the dim ink (text preserved); entries before
	// `from` are untouched. We assert the exact transformation, not string inequality, because
	// under a NO_COLOR test profile dim and live both render plain (the effect is real on a
	// color TTY; this proves the right entries get the right restyle regardless).
	got := revealBlock(lines, from, 0, false)
	for i := 0; i < from; i++ {
		if got[i] != lines[i] {
			t.Errorf("entry %d before `from` must be untouched", i)
		}
	}
	for i := from; i < len(lines); i++ {
		if want := stDim.Render(ansi.Strip(lines[i])); got[i] != want {
			t.Errorf("entry %d should be dimmed+stripped: got %q want %q", i, got[i], want)
		}
		if ansi.Strip(got[i]) != ansi.Strip(lines[i]) {
			t.Errorf("entry %d reveal must preserve the text", i)
		}
	}

	if settled := revealBlock(lines, from, msgRevealFrames, false); !reflect.DeepEqual(settled, lines) {
		t.Error("after msgRevealFrames the block must settle to the original styling")
	}
	if r := revealBlock(lines, from, 0, true); !reflect.DeepEqual(r, lines) {
		t.Error("reduce=true must skip the reveal (reduced motion)")
	}
	if r := revealBlock(lines, 9, 0, false); !reflect.DeepEqual(r, lines) {
		t.Error("out-of-range from must be a no-op")
	}
	if r := revealBlock(lines, from, -1, false); !reflect.DeepEqual(r, lines) {
		t.Error("negative age must be a no-op")
	}
}

// TestChatMsgStampsMessageIn: an incoming reply stamps the message-in frame + the index where
// its block starts, so refreshScroll can settle exactly that block.
func TestChatMsgStampsMessageIn(t *testing.T) {
	m := seedFor(120, modeChat, false)
	m.frame = 7
	before := len(m.transcript)
	out, _ := m.Update(chatMsg{reply: "the answer", cost: 0.01})
	om := asModel(out)
	if om.msgInFrame != 7 {
		t.Errorf("chatMsg should stamp msgInFrame = current frame (7), got %d", om.msgInFrame)
	}
	if om.msgInFrom != before {
		t.Errorf("msgInFrom should mark where the new block starts (%d), got %d", before, om.msgInFrom)
	}
	if len(om.transcript) <= before {
		t.Error("chatMsg should append the reply block")
	}
}

// TestClearResetsMessageIn: clearing the transcript drops the pending reveal stamp (no stale
// reveal pointing into a wiped transcript).
func TestClearResetsMessageIn(t *testing.T) {
	m := seedFor(120, modeChat, false)
	m.msgInFrame = 5
	m.msgInFrom = 2
	out, _ := m.runSession("/clear")
	if om := asModel(out); om.msgInFrame != 0 {
		t.Errorf("/clear should reset msgInFrame, got %d", om.msgInFrame)
	}
}
