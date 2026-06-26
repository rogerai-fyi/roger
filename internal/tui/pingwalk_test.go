package tui

import (
	"strings"
	"testing"
)

// TestRenderPingDrawsMascot is the regression guard for the `roger ping` easter egg:
// renderPing must produce the recognizable Ping figure - the (( … )) on-air head over
// the │ R │ body. If the frames or renderer regress, the easter egg silently degrades.
// Covers the quiet/non-TTY path PingWalk prints (the animated TTY walk renders the
// same frames). Asserts on ASCII-stable parts (parens + the R marker) so it holds even
// where glyphs.Fold swaps box-drawing/bullet runes for ASCII stand-ins.
func TestRenderPingDrawsMascot(t *testing.T) {
	art := renderPing(pingWalkFrames[0], "•")
	if strings.TrimSpace(art) == "" {
		t.Fatal("renderPing returned empty art")
	}
	for _, want := range []string{"((", "))", "R"} {
		if !strings.Contains(art, want) {
			t.Fatalf("rendered Ping missing %q:\n%s", want, art)
		}
	}
	// A multi-line figure (head + arms + body + feet), not a one-liner.
	if lines := strings.Count(strings.TrimSpace(art), "\n"); lines < 3 {
		t.Fatalf("rendered Ping too small (%d newlines), want a multi-line figure:\n%s", lines, art)
	}
	// The two walk frames differ only in the feet (the step), so the figure animates.
	if a, b := renderPing(pingWalkFrames[0], "•"), renderPing(pingWalkFrames[1], "•"); a == b {
		t.Fatal("the two walk frames render identically - the step won't animate")
	}
}
