package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// TestBandCardWidthSafe: the one-time private-band code card renders without
// overflowing the terminal at narrow widths, reveals the FULL code (with the secret
// tail - this is the shown-once card, NOT the masked persisted display), shows the
// "shown once" notice + the c-copy affordance, and is plain-text (NO_COLOR) safe.
func TestBandCardWidthSafe(t *testing.T) {
	for _, w := range []int{40, 64, 80, 120} {
		m := New("http://broker.local", "tester")
		mm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
		m = mm.(model)
		m.mode = modeBandCard
		m.bandCardModel = "gpt-oss-20b"
		m.bandCardCode = "147.520 MHz · 8F3K-9M2Q" // the one-time FULL code (with the tail)
		m.bandCardDisp = "147.520 MHz · ••••-••••" // the MASKED persisted display
		out := m.View()
		for _, line := range strings.Split(out, "\n") {
			if vis := utf8.RuneCountInString(stripANSI(line)); vis > w {
				t.Errorf("width %d: band card line overflows (%d): %q", w, vis, stripANSI(line))
			}
		}
		if !strings.Contains(out, "147.520 MHz") {
			t.Errorf("width %d: band card missing the cosmetic frequency:\n%s", w, out)
		}
		// The one-time card at a readable width MUST reveal the secret tail (the owner saves
		// it now), and must NOT substitute the masked placeholder for it.
		if w >= 64 && !strings.Contains(out, "8F3K-9M2Q") {
			t.Errorf("width %d: one-time band card must reveal the secret tail:\n%s", w, out)
		}
		if strings.Contains(out, "••••") {
			t.Errorf("width %d: one-time band card must show the FULL code, not the mask:\n%s", w, out)
		}
		if !strings.Contains(stripANSI(out), "shown") {
			t.Errorf("width %d: band card missing the 'shown once' notice", w)
		}
	}
}

// TestFreqHeaderIndicator: the band browser header reads OPEN MARKET by default and
// FREQ <display> when a private frequency is tuned; neither overflows narrow widths.
func TestFreqHeaderIndicator(t *testing.T) {
	mk := func(freq, label string, w int) string {
		m := New("http://broker.local", "tester")
		mm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
		m = mm.(model)
		mm, _ = m.Update(offersMsg{{NodeID: "n", Model: "m", PriceOut: 0.3, Online: true}})
		m = mm.(model)
		m.tuneFreq, m.tuneFreqLabel = freq, label
		return m.View()
	}
	// Default: OPEN MARKET shown at a wide width.
	if out := mk("", "", 120); !strings.Contains(stripANSI(out), "OPEN MARKET") {
		t.Errorf("default header missing OPEN MARKET:\n%s", out)
	}
	// Tuned: FREQ <short label> shown.
	if out := mk("147.520 MHz 8F3K9M2Q", "147.520 MHz · 8F3K-9M2Q", 120); !strings.Contains(stripANSI(out), "FREQ 147.520 MHz") {
		t.Errorf("tuned header missing FREQ indicator:\n%s", out)
	}
	// Width safety at narrow widths in both states.
	for _, w := range []int{40, 64} {
		for _, out := range []string{mk("", "", w), mk("147.520 MHz 8F3K9M2Q", "147.520 MHz · 8F3K-9M2Q", w)} {
			for _, line := range strings.Split(out, "\n") {
				if vis := utf8.RuneCountInString(stripANSI(line)); vis > w {
					t.Errorf("width %d: header line overflows (%d): %q", w, vis, stripANSI(line))
				}
			}
		}
	}
}
