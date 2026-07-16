package tui

// Increment 6 of the TUI design overhaul: the USER-turn + input TINT BANDS. Your echoed
// asks and the prompt get the red ▌ left bar and a FAINT neutral warm wash (a Background()
// tint band, the first real tint-band use beyond the tube-glow), so your turns separate
// from the assistant's bare-paper prose. Gated to ANSI256+ via canTint; mono / dumb
// terminals drop to the bare red ▌ bar (the §9 fallback / the escape hatch).

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// B1 - with tint capability the user line carries the red ▌ bar AND the faint band bg.
func TestBandUserTinted(t *testing.T) {
	colorOn(t, true)
	rq := quiet
	quiet = false
	t.Cleanup(func() { quiet = rq })

	out := bandUser("hello")
	if !strings.Contains(out, lipgloss.NewStyle().Background(cBand).Render("hello")) {
		t.Error("B1: the user line should sit on the faint neutral band")
	}
	if !strings.Contains(stripANSI(out), "▌ hello") {
		t.Errorf("B1: the user line keeps the ▌ bar + text: %q", stripANSI(out))
	}
}

// B2 - the escape hatch: palette mono drops the band to the bare red ▌ bar (no bg wash).
func TestBandUserMonoFallback(t *testing.T) {
	colorOn(t, true)
	restore := paletteMono
	t.Cleanup(func() { paletteMono = restore })
	paletteMono = true
	rq := quiet
	quiet = false
	t.Cleanup(func() { quiet = rq })

	out := bandUser("hello")
	if strings.Contains(out, lipgloss.NewStyle().Background(cBand).Render("hello")) {
		t.Error("B2: mono must not paint the tint band")
	}
	if out != stSelBar.Render("▌ ")+"hello" {
		t.Errorf("B2: mono falls back to the bare red ▌ bar: %q", out)
	}
}

// B3 - a dumb terminal (no canTint, e.g. quiet/NO_COLOR) also drops to the bare bar.
func TestBandUserQuietFallback(t *testing.T) {
	// quiet stays true (the default under a non-TTY test) => canTint false.
	if bandUser("x") != stSelBar.Render("▌ ")+"x" {
		t.Error("B3: under quiet the band drops to the bare ▌ bar")
	}
}

// B4 - the echoed AGENT ask uses the band (the red ▌ bar), replacing the old ▸ marker.
func TestAgentAskUsesBand(t *testing.T) {
	m := agentAt(t, permConfirm)
	m.agentLines = nil // first ask: no time rule
	first := m.agentAskLines("commit what's staged")
	flat := stripANSI(first[len(first)-1])
	if !strings.Contains(flat, "▌ commit what's staged") {
		t.Errorf("B4: the ask should echo on the ▌ band: %q", flat)
	}
	if strings.Contains(flat, "▸") {
		t.Error("B4: the old ▸ ask marker is replaced by the ▌ bar")
	}
}
