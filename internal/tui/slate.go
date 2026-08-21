package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// slate.go - THE TELEGRAM BLOCKS.
//
// FOUNDER 2026-08-21: "i want to see it in a box, and the You also in a box, it should
// look like a running telegram machine, so each receipt is visible, in an envelope
// slate block."
//
// So a turn is two blocks torn off the same machine: what you sent, and what came back.
// Each sits on its own face, lifted off the deck, with a shadow under it. The pair
// reads as one exchange because they share a shape; they read as different sides
// because the operator's face is brighter and wears the red bar, while the station's is
// quieter and wears an ink one.
//
// DEPTH WITHOUT GLYPHS. A terminal has no shadows, so lift is relative brightness: a
// face lighter than the ground reads as raised, one darker row beneath reads as the
// shadow it casts. Nothing depends on a font having ▔ or ▁, and there is no glyph to
// wrap - which is what broke the first version of this.
//
// THE WIDTH IS THE CONTENT WIDTH. transcriptContent wraps at width-2 and then indents
// by two, so a block built to the viewport width is two cells over: it wraps, and the
// overflow comes back as a stray fragment. Callers pass the content width.

// slateBlock encloses already-rendered rows in a lifted face plus a shadow.
//
// The rows arrive individually STYLED (an answer carries code fences, diff colours and
// bullets), and every one of those styles emits an SGR reset that would punch a hole
// straight through an outer background. solidBackground re-arms the face after each
// reset, which is the same helper the deck ground uses and for the same reason.
func slateBlock(rows []string, w int, face lipgloss.TerminalColor, shade lipgloss.TerminalColor) []string {
	if len(rows) == 0 {
		return nil
	}
	out := make([]string, 0, len(rows)+1)
	for _, r := range rows {
		if pad := w - lipgloss.Width(r); pad > 0 {
			r += strings.Repeat(" ", pad)
		}
		out = append(out, r)
	}
	painted := strings.Split(solidBackground(strings.Join(out, "\n"), face), "\n")
	// The shadow: one darker row the width of the block. It is what makes the face read
	// as lifted rather than merely tinted.
	return append(painted, lipgloss.NewStyle().Background(shade).Render(strings.Repeat(" ", w)))
}

// slatesOn reports whether the enclosure may be painted at all. Mono and any profile
// that cannot tint fall back to the bare gutters, which is the same escape hatch every
// other band on this screen has: the blocks are decoration over lines that already read.
func slatesOn() bool {
	return !paletteMono && canTint(lipgloss.DefaultRenderer().ColorProfile())
}
