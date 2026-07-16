package tui

// Bug fix (founder, 2026-07-15, screenshots): in TUNE-IN chat AND AGENT, a streamed
// reply line longer than the terminal width was CLIPPED at the right edge by the
// viewport - text past the margin ("…a layered \"cak") was simply lost. The transcript
// must WRAP long lines to the width (reflowing on resize), not truncate them. Fixed at
// render time in transcriptContent (ANSI + wide-char aware via ansi.Wrap), so no reply
// text is ever dropped.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// A long, single-paragraph reply (as a station streams it) must reflow to fit the
// width: every rendered line within the budget, and NOT ONE WORD lost.
func TestTranscriptWrapsLongReply(t *testing.T) {
	long := "For a no-bake, refreshingly cold summer dessert, you can't beat a No-Bake " +
		"Lemon Icebox Cake. It's creamy, tart, sweet, and requires zero oven time. " +
		"It's essentially a layered cake."
	entry := stLive.Render("◂ ") + long

	const w = 48
	out := transcriptContent([]string{entry}, w)

	lines := strings.Split(out, "\n")
	if len(lines) < 3 {
		t.Fatalf("a %d-col reply at width %d must wrap to several lines, got %d:\n%s",
			lipgloss.Width(entry), w, len(lines), out)
	}
	for _, ln := range lines {
		if lipgloss.Width(ln) > w {
			t.Errorf("wrapped line exceeds width %d: %q (%d cols)", w, stripANSI(ln), lipgloss.Width(ln))
		}
	}
	// The cut-off word from the bug report must survive the wrap (no text dropped).
	flat := strings.ReplaceAll(stripANSI(out), "\n", " ")
	for _, word := range []string{"layered", "cake."} {
		if !strings.Contains(flat, word) {
			t.Errorf("wrapping dropped %q - the reply text must be preserved in full", word)
		}
	}
}

// The model's OWN newlines (markdown paragraphs / list items) are preserved as hard
// breaks, and short lines are returned untouched (no gratuitous rewrapping).
func TestTranscriptPreservesHardBreaks(t *testing.T) {
	out := transcriptContent([]string{"- one\n- two\n- three"}, 80)
	for _, want := range []string{"- one", "- two", "- three"} {
		if !strings.Contains(out, want) {
			t.Errorf("hard break %q lost:\n%s", want, out)
		}
	}
	if n := len(strings.Split(out, "\n")); n != 3 {
		t.Errorf("three short list items should stay three lines, got %d", n)
	}
}

// A degenerate tiny width must not panic or spin (guard the indent subtraction).
func TestTranscriptTinyWidthSafe(t *testing.T) {
	_ = transcriptContent([]string{stLive.Render("◂ ") + "hello world this is long"}, 1)
	_ = transcriptContent([]string{"x"}, 0)
}
