package webui

// The console's inline forms (rename, bind model / job) survive the poll while the operator is
// typing, and are put away the moment they act. Audit 2026-09-27: a guard on "focus anywhere in the
// card" also swallowed the re-render after Save/Cancel/Enter/Esc, because the button or input the
// operator had just used still held focus, so the form stayed on screen after it had acted.

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConsoleEdgeFormGuardBlocksOnlyTypingAndActionsBlurFirst(t *testing.T) {
	b, err := os.ReadFile("assets/console.js")
	require.NoError(t, err)
	js := string(b)

	// The guard names the controls an operator types into, and nothing else.
	guard := regexp.MustCompile(`function edgeFormBusy\(card\) \{[\s\S]*?\n  \}`).FindString(js)
	require.NotEmpty(t, guard, "edgeFormBusy is the one guard the detail re-render consults")
	for _, tag := range []string{`"INPUT"`, `"SELECT"`, `"TEXTAREA"`} {
		require.Contains(t, guard, tag)
	}
	require.NotContains(t, guard, "BUTTON", "a focused button never blocks a redraw")
	detail := regexp.MustCompile(`function edgeRenderDetail\(d\) \{[\s\S]*?\n  \}`).FindString(js)
	require.Contains(t, detail, "if (edgeFormBusy(card)) return;")
	require.NotContains(t, detail, "card.contains(document.activeElement)", "the old whole-card guard is gone")

	// Enter and Escape in the rename input act from INSIDE the input: they release it first, so the
	// re-render that follows is not refused by the guard they just satisfied.
	rename := regexp.MustCompile(`function edgeRenamePrompt\(card, n\) \{[\s\S]*?\n  \}`).FindString(js)
	require.NotEmpty(t, rename)
	require.Contains(t, rename, `if (e.key === "Enter") { e.preventDefault(); input.blur(); commit(); }`)
	require.Contains(t, rename, `else if (e.key === "Escape") { e.preventDefault(); input.blur(); edgeTick(); }`)
	require.True(t, strings.Count(rename, "input.blur()") >= 2)
}
