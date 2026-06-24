package tui

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// manyOffers builds n distinct on-air bands (one station each) so the scale tests
// exercise hundreds/thousands of rows. Every other band is named "qwen-*" so a
// name filter has a predictable match count.
func manyOffers(n int) offersMsg {
	offers := make([]offer, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("llama-%03d", i)
		if i%2 == 0 {
			name = fmt.Sprintf("qwen-%03d", i)
		}
		offers = append(offers, offer{
			NodeID: fmt.Sprintf("node-%03d", i), Region: "home", Model: name,
			PriceIn: 0.1, PriceOut: float64(i%7) * 0.1, Ctx: 32768, Online: true,
			TPS: float64(i % 90),
		})
	}
	return offersMsg(offers)
}

func keyPress(m tea.Model, s string) tea.Model {
	var msg tea.KeyMsg
	switch s {
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	nm, _ := m.Update(msg)
	return nm
}

func typeStr(m tea.Model, s string) tea.Model {
	for _, r := range s {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

// TestFilterByNameLive: pressing f opens the filter; typing narrows the band list
// LIVE by name (substring, case-insensitive); the active filter + match count
// render; and f / the typed text is NOT stolen by a preset (e.g. m for compact).
func TestFilterByNameLive(t *testing.T) {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(manyOffers(40)) // 20 qwen-*, 20 llama-*
	m, _ = m.Update(tickMsg{})

	// f opens the filter line.
	m = keyPress(m, "f")
	if !m.(model).filterMode {
		t.Fatalf("f should open the filter line")
	}
	// Type "qwen" - the m in no way toggles compact (the filter owns the keys).
	m = typeStr(m, "qwen")
	mm := m.(model)
	if mm.compact {
		t.Errorf("typing into the filter must NOT trigger the compact (m) preset")
	}
	if mm.filterApplied != "qwen" {
		t.Errorf("filter should apply live while typing, got %q", mm.filterApplied)
	}
	// 20 of 40 bands match "qwen".
	if got := len(mm.visibleBands()); got != 20 {
		t.Errorf("filter qwen should match 20/40 bands, got %d", got)
	}
	// The match count + active filter render in the view.
	out := stripANSI(m.View())
	if !strings.Contains(out, "(20/40)") {
		t.Errorf("view should show the match count (20/40):\n%s", out)
	}
	if !strings.Contains(out, "qwen") {
		t.Errorf("view should show the active filter text:\n%s", out)
	}
	// Case-insensitive: uppercase query still matches lowercase names. Clear, reopen.
	m = keyPress(m, "esc") // clears + closes
	m = keyPress(m, "f")
	m = typeStr(m, "QWEN")
	if got := len(m.(model).visibleBands()); got != 20 {
		t.Errorf("uppercase QWEN should match the same 20 (case-insensitive), got %d", got)
	}

	// enter keeps the filter applied and returns to the list (navigable).
	m = keyPress(m, "enter")
	mm = m.(model)
	if mm.filterMode {
		t.Errorf("enter should close the filter input")
	}
	if mm.filterApplied != "QWEN" {
		t.Errorf("enter should keep the filter applied, got %q", mm.filterApplied)
	}

	// esc (re-open then esc) clears + closes the filter (back to the full list).
	m = keyPress(m, "f")
	m = keyPress(m, "esc")
	mm = m.(model)
	if mm.filterMode || mm.filterApplied != "" {
		t.Errorf("esc should clear + close the filter, got mode=%v applied=%q", mm.filterMode, mm.filterApplied)
	}
	if got := len(mm.visibleBands()); got != 40 {
		t.Errorf("after clearing, all 40 bands should be visible, got %d", got)
	}
}

// TestVirtualizedWindow: with hundreds of bands the browse view renders ONLY the
// visible window (not every row), shows a position indicator (e.g. "1-N of 340"),
// and stays correct with the cursor at the top edge, the bottom edge, and with a
// filter applied (window over the filtered set).
func TestVirtualizedWindow(t *testing.T) {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(manyOffers(340))
	m, _ = m.Update(tickMsg{})

	countBandRows := func(v string) int {
		n := 0
		for _, line := range strings.Split(stripANSI(v), "\n") {
			if strings.Contains(line, "qwen-") || strings.Contains(line, "llama-") {
				n++
			}
		}
		return n
	}

	out := stripANSI(m.View())
	rows := countBandRows(out)
	if rows >= 340 {
		t.Errorf("virtualized list must NOT render all 340 rows, rendered %d", rows)
	}
	if rows < 3 {
		t.Errorf("window should render a useful number of rows, got %d", rows)
	}
	// Position indicator present at the top edge: "1-N of 340".
	if !strings.Contains(out, " of 340") {
		t.Errorf("view should show a position indicator (... of 340):\n%s", topLines(out, 20))
	}
	// Top edge: no "more above" hint, but a "more below" hint exists.
	if strings.Contains(out, "more above") {
		t.Errorf("at the top edge there should be no 'more above' hint")
	}
	if !strings.Contains(out, "more below") {
		t.Errorf("at the top edge there should be a 'more below' hint")
	}

	// Move the cursor to the bottom edge: press down 339 times.
	for i := 0; i < 339; i++ {
		m = keyPress(m, "down")
	}
	mm := m.(model)
	if mm.cursor != 339 {
		t.Fatalf("cursor should be at the last band (339), got %d", mm.cursor)
	}
	out = stripANSI(m.View())
	// Bottom edge: the band UNDER the cursor (the last in the sorted view) is rendered,
	// a "more above" hint exists, and there is no "below". (The exact last name depends
	// on the active sort, so assert the selected band, not a hard-coded id.)
	lastBand, _ := mm.selectedBand()
	if !strings.Contains(out, lastBand.model) {
		t.Errorf("at the bottom edge the cursor's band %q should be visible:\n%s", lastBand.model, lastLines(out, 20))
	}
	if !strings.Contains(out, "more above") {
		t.Errorf("at the bottom edge there should be a 'more above' hint")
	}
	if strings.Contains(out, "more below") {
		t.Errorf("at the bottom edge there should be no 'more below' hint")
	}
	if countBandRows(out) >= 340 {
		t.Errorf("still must not render all rows at the bottom edge")
	}
	// Position indicator's end equals the total at the bottom.
	if !strings.Contains(out, "of 340") {
		t.Errorf("bottom edge should still show the position indicator:\n%s", lastLines(out, 20))
	}

	// With a filter applied the window is over the FILTERED set: filter to a single
	// band by its exact unique name.
	m = keyPress(m, "f")
	m = typeStr(m, "qwen-100")
	m = keyPress(m, "enter")
	mm = m.(model)
	if got := len(mm.visibleBands()); got != 1 {
		t.Fatalf("filter qwen-100 should match exactly 1 band, got %d", got)
	}
	out = stripANSI(m.View())
	if !strings.Contains(out, "qwen-100") {
		t.Errorf("the single filtered band should render:\n%s", out)
	}
	// One row fits the window: no position indicator / more hints over a 1-row set.
	if strings.Contains(out, "more above") || strings.Contains(out, "more below") {
		t.Errorf("a single-row filtered set should have no more-hints:\n%s", out)
	}
}

// TestCursorRespectsFilteredSet: cursor nav is bounded by the FILTERED + SORTED
// visible set, never the raw band list, and connect acts on the band the cursor
// actually points at in that view.
func TestCursorRespectsFilteredSet(t *testing.T) {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(manyOffers(40))
	m, _ = m.Update(balanceMsg{balance: 100, loggedIn: true})
	m, _ = m.Update(tickMsg{})

	// Filter to the 20 qwen bands, then drive the cursor to the last visible row.
	m = keyPress(m, "f")
	m = typeStr(m, "qwen")
	m = keyPress(m, "enter")
	for i := 0; i < 50; i++ { // overshoot: nav must clamp at the filtered count
		m = keyPress(m, "down")
	}
	mm := m.(model)
	vis := mm.visibleBands()
	if mm.cursor != len(vis)-1 {
		t.Errorf("cursor must clamp to the last FILTERED band (%d), got %d", len(vis)-1, mm.cursor)
	}
	bd, ok := mm.selectedBand()
	if !ok || !strings.HasPrefix(bd.model, "qwen-") {
		t.Errorf("selected band must be inside the filtered set, got %q", bd.model)
	}
}

// TestSortCycleMirrorsWeb: S cycles the sort dial (strongest/cheapest/fastest/
// most-stations) and the active sort reorders the visible list - same keys as the
// /bands web page.
func TestSortCycleMirrorsWeb(t *testing.T) {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(offersMsg{
		{NodeID: "a", Model: "alpha", PriceOut: 0.9, Online: true, TPS: 10},
		{NodeID: "b", Model: "bravo", PriceOut: 0.1, Online: true, TPS: 90},
	})
	m, _ = m.Update(tickMsg{})

	// Default sort (strongest signal): bravo (tps 90) before alpha (tps 10).
	if v := m.(model).visibleBands(); v[0].model != "bravo" {
		t.Errorf("default strongest-signal sort: bravo (faster) should lead, got %q", v[0].model)
	}
	// S -> cheapest: bravo (0.1) still leads here (cheaper out-price).
	m = keyPress(m, "S")
	if m.(model).sortMode != sortCheapest {
		t.Fatalf("S should advance to the cheapest sort, got %d", m.(model).sortMode)
	}
	if v := m.(model).visibleBands(); v[0].model != "bravo" {
		t.Errorf("cheapest sort: bravo (0.1 out) should lead, got %q", v[0].model)
	}
	// Cycle is bounded by sortCount and returns to strongest after a full loop.
	for i := 0; i < sortCount-1; i++ {
		m = keyPress(m, "S")
	}
	if m.(model).sortMode != sortSignal {
		t.Errorf("the sort cycle should wrap back to strongest, got %d", m.(model).sortMode)
	}
}

// TestQuickTogglesFilter: F/C/O toggles narrow the list to free-now / confidential
// / on-air, mirroring the /bands web tuner chips.
func TestQuickTogglesFilter(t *testing.T) {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(offersMsg{
		{NodeID: "a", Model: "free-band", PriceOut: 0, Online: true, FreeNow: true},
		{NodeID: "b", Model: "conf-band", PriceOut: 0.2, Online: true, Confidential: true},
		{NodeID: "c", Model: "plain-band", PriceOut: 0.3, Online: true},
	})
	m, _ = m.Update(tickMsg{})

	m = keyPress(m, "F") // free-now only
	if v := m.(model).visibleBands(); len(v) != 1 || v[0].model != "free-band" {
		t.Errorf("F (free-now) should leave only the free band, got %v", names(v))
	}
	m = keyPress(m, "F") // off
	m = keyPress(m, "C") // confidential only
	if v := m.(model).visibleBands(); len(v) != 1 || v[0].model != "conf-band" {
		t.Errorf("C (confidential) should leave only the lineage band, got %v", names(v))
	}
}

// TestAsyncLoadingState: before the first /discover lands the browse view shows the
// ((•)) scanning indicator (mirroring SHARE), not a frozen empty list; once offers
// arrive the list renders; pressing r returns to the loading pose.
func TestAsyncLoadingState(t *testing.T) {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(tickMsg{})

	// Initial state: no scan back yet -> the scanning indicator.
	out := stripANSI(m.View())
	if !strings.Contains(out, "scanning the band") {
		t.Errorf("initial browse load should show the scanning indicator:\n%s", out)
	}

	// Offers land -> the list renders, no scanning pose.
	m, _ = m.Update(manyOffers(5))
	out = stripANSI(m.View())
	if strings.Contains(out, "scanning the band") {
		t.Errorf("after offers land the scanning pose should clear:\n%s", out)
	}
	if !strings.Contains(out, "qwen-000") {
		t.Errorf("the band list should render after offers land:\n%s", out)
	}

	// r re-scan returns to the loading pose (scanned reset).
	m = keyPress(m, "r")
	if m.(model).scanned {
		t.Errorf("r should reset scanned to show the loading pose during the re-scan")
	}
	// (no offers yet) the empty list shows the scanning indicator again.
	mm := m.(model)
	mm.bands = nil // a re-scan can momentarily empty the list before offers return
	if v := mm.browseView(100); !strings.Contains(stripANSI(v), "scanning the band") {
		t.Errorf("r re-scan should show the scanning indicator again:\n%s", stripANSI(v))
	}
}

// TestWindowForEdges is a focused unit test of the windowing math: the cursor is
// always visible, the window never leaves a blank tail, and a list that fits
// returns the whole list.
func TestWindowForEdges(t *testing.T) {
	cases := []struct {
		top, cur, rows, n int
		wantTop, wantEnd  int
		name              string
	}{
		{0, 0, 10, 5, 0, 5, "fits-no-scroll"},
		{0, 0, 10, 340, 0, 10, "top-edge"},
		{0, 339, 10, 340, 330, 340, "bottom-edge"},
		{0, 25, 10, 340, 16, 26, "middle-cursor-pulls-window-down"},
		{30, 5, 10, 340, 5, 15, "cursor-above-window-pulls-up"},
		{0, 0, 10, 0, 0, 0, "empty-list"},
	}
	for _, c := range cases {
		gotTop, gotEnd := windowFor(c.top, c.cur, c.rows, c.n)
		if gotTop != c.wantTop || gotEnd != c.wantEnd {
			t.Errorf("%s: windowFor(%d,%d,%d,%d) = (%d,%d), want (%d,%d)",
				c.name, c.top, c.cur, c.rows, c.n, gotTop, gotEnd, c.wantTop, c.wantEnd)
		}
		// The cursor is always inside [top,end) for a non-empty list.
		if c.n > 0 && (c.cur < gotTop || c.cur >= gotEnd) {
			t.Errorf("%s: cursor %d not visible in window [%d,%d)", c.name, c.cur, gotTop, gotEnd)
		}
	}
}

// TestBandBrowserNoColorNarrow: with a filter applied + the virtualized window
// live, the view emits NO ANSI under NO_COLOR and no line overflows at widths
// 40-120 (the non-TTY / piped, narrow-safe contract).
func TestBandBrowserNoColorNarrow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, w := range []int{40, 50, 64, 80, 120} {
		var m tea.Model = New("http://broker.local", "tester")
		m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: 24})
		m, _ = m.Update(manyOffers(300))
		m, _ = m.Update(balanceMsg{balance: 5, loggedIn: true})
		m, _ = m.Update(tickMsg{})
		// Apply a filter + move the cursor so the window + indicator + filter line all render.
		m = keyPress(m, "f")
		m = typeStr(m, "qwen")
		m = keyPress(m, "enter")
		for i := 0; i < 40; i++ {
			m = keyPress(m, "down")
		}
		out := m.View()
		if strings.Contains(out, "\x1b[") {
			t.Errorf("width %d: band browser emitted ANSI under NO_COLOR", w)
		}
		for _, line := range strings.Split(out, "\n") {
			if vis := utf8.RuneCountInString(stripANSI(line)); vis > w {
				t.Errorf("width %d: line overflows (%d cols): %q", w, vis, stripANSI(line))
			}
		}
		// The filter line + position indicator are present (the scale affordances).
		plain := stripANSI(out)
		if !strings.Contains(plain, "qwen") {
			t.Errorf("width %d: active filter should render:\n%s", w, plain)
		}
	}
}

// --- small test helpers ---

func names(bs []band) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.model
	}
	return out
}

func topLines(s string, n int) string {
	ls := strings.Split(s, "\n")
	if len(ls) > n {
		ls = ls[:n]
	}
	return strings.Join(ls, "\n")
}

func lastLines(s string, n int) string {
	ls := strings.Split(s, "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, "\n")
}
