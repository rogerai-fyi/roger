package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestOffersTransientEmptyKeepsList pins the band-list flicker fix: a single empty /discover
// (a rescan that load-balanced onto a still-syncing broker instance) must NOT blank a populated
// list; only a SUSTAINED empty does. The alternating-instance case (full, empty, full, empty)
// never blanks because each full scan resets the counter.
func TestOffersTransientEmptyKeepsList(t *testing.T) {
	full := []offer{{Model: "gpt-oss-20b", Online: true}}
	m := seedFor(120, modeBrowse, false)
	m.offers = full
	m.bands = m.mergeStickyBand(groupBands(m.offers, m.limits))
	m.loadedOnce = true

	out, _ := m.Update(offersMsg(nil)) // transient empty
	om := asModel(out)
	if len(om.offers) == 0 {
		t.Error("a single transient empty scan should KEEP the last-known offers (no flicker)")
	}
	if om.emptyScans != 1 {
		t.Errorf("emptyScans = %d, want 1", om.emptyScans)
	}

	out2, _ := om.Update(offersMsg(full)) // a full scan resets the counter
	if asModel(out2).emptyScans != 0 {
		t.Error("a non-empty scan should reset emptyScans (alternating-instance case never blanks)")
	}

	cur := asModel(out2) // now drive SUSTAINED empties -> eventually blanks (genuine empty)
	for i := 0; i < emptyScansToBlank; i++ {
		o, _ := cur.Update(offersMsg(nil))
		cur = asModel(o)
	}
	if len(cur.offers) != 0 {
		t.Errorf("after %d consecutive empty scans a genuine empty should finally show", emptyScansToBlank)
	}
}

// TestOffersFirstLoadEmptyAccepts: the FIRST scan (loadedOnce false) accepts an empty result -
// a genuinely empty market shows immediately on startup (the debounce only guards a populated list).
func TestOffersFirstLoadEmptyAccepts(t *testing.T) {
	m := seedFor(120, modeBrowse, false)
	m.offers = nil
	m.loadedOnce = false
	out, _ := m.Update(offersMsg(nil))
	om := asModel(out)
	if !om.loadedOnce {
		t.Error("first scan should set loadedOnce")
	}
	if len(om.offers) != 0 {
		t.Error("first-load empty should be accepted (genuinely no stations)")
	}
}

// GHOSTS, ROOT CAUSE (founder screenshot #3, 2026-09-02: the header + account line
// visible TWICE with different on-air counts). Once the alt buffer has ever scrolled
// (an oversized frame, a resize race), the renderer's model of the screen is offset
// from reality and every later diff-paint lands rows off - old and new frames
// interleave. A resize is exactly the desync moment, so every WindowSizeMsg must
// answer with tea.ClearScreen: the renderer repaints the whole screen from a known
// blank and the ghosts die.
func TestResizeClearsTheScreen(t *testing.T) {
	m := browseSeed(80)
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if cmd == nil {
		t.Fatal("a resize returned no command - the renderer keeps its stale screen model")
	}
	if got := fmt.Sprintf("%T", cmd()); !strings.Contains(got, "clearScreenMsg") {
		t.Fatalf("a resize must answer tea.ClearScreen, got %s", got)
	}
}

// The station log says what it is. The founder opened it with i and asked "what am
// I actually seeing" - the view now carries one dim line of purpose under its head.
func TestStationLogSaysWhatItIs(t *testing.T) {
	m := browseSeed(100)
	m.detailBand = m.bands[0]
	view := stripANSI(m.bandDetailView(100))
	if !strings.Contains(view, "every station carrying this band") {
		t.Fatalf("the station log does not say what it is:\n%s", view)
	}
}
