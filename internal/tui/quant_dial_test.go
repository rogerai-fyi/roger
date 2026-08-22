package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// THE DIAL SPLITS BY (MODEL, QUANT) — founder ruling, MODEL-VARIANTS-DESIGN-2026-08-22.

func quantOffers() []offer {
	return []offer{
		{NodeID: "a-qwen", Model: "qwen3.8-27b", Quant: "Q4_K_M", Online: true, PriceOut: 0.40},
		{NodeID: "b-qwen", Model: "qwen3.8-27b", Quant: "Q4_K_M", Online: true, PriceOut: 0.45},
		{NodeID: "c-qwen", Model: "qwen3.8-27b", Quant: "BF16", Online: true, PriceOut: 0.90},
		// Two stations that state NOTHING must collapse into ONE row: they are not a
		// quant, they are an absence, and splitting absences yields rows that differ in
		// no visible way.
		{NodeID: "d-qwen", Model: "qwen3.8-27b", Online: true, PriceOut: 0.35},
		{NodeID: "e-qwen", Model: "qwen3.8-27b", Online: true, PriceOut: 0.38},
	}
}

func TestTheDialSplitsOneModelIntoOneRowPerQuant(t *testing.T) {
	bands := groupBands(quantOffers(), nil)
	byQuant := map[string]band{}
	for _, b := range bands {
		if b.model == "qwen3.8-27b" {
			byQuant[b.quant] = b
		}
	}
	if len(byQuant) != 3 {
		t.Fatalf("want 3 rows (Q4_K_M, BF16, unstated), got %d: %v", len(byQuant), byQuant)
	}
	if got := byQuant["Q4_K_M"].stations; got != 2 {
		t.Errorf("Q4_K_M row has %d stations, want 2", got)
	}
	if got := byQuant[""].stations; got != 2 {
		t.Errorf("the unstated row has %d stations, want both collapsed into one row", got)
	}
	// Each row prices ITSELF - the whole point of splitting. The unstated row is cheapest
	// and must not lend its price to the Q4_K_M row.
	if byQuant["Q4_K_M"].minOut != 0.40 || byQuant["BF16"].minOut != 0.90 {
		t.Errorf("rows share a price: %+v", byQuant)
	}
}

// THE ROW MUST SAY WHICH ONE IT IS. Two rows carrying the same model name and no quant
// would be the exact failure splitting was meant to fix, so when space is short the MODEL
// NAME gives way, not the quant.
func TestTheRowShowsItsQuantEvenWhenNarrow(t *testing.T) {
	bd := band{model: "qwen3.8-27b-instruct", quant: "Q4_K_M"}
	for _, w := range []int{40, 30, 22, 16} {
		cell := bandNameCell(bd, w)
		if len([]rune(cell)) != w {
			t.Errorf("w=%d: cell is %d wide", w, len([]rune(cell)))
		}
		if !strings.Contains(cell, "Q4_K_M") {
			t.Errorf("w=%d: the quant was dropped, leaving %q", w, cell)
		}
	}
	// A band with NO quant renders as just the model - absent is absent, never a
	// placeholder that could be mistaken for a station's claim.
	plain := bandNameCell(band{model: "qwen3.8-27b"}, 24)
	if strings.Contains(plain, "unknown") || strings.Contains(plain, "-") && strings.Contains(plain, "—") {
		t.Errorf("an unstated quant produced a placeholder: %q", plain)
	}
}

// TUNING A ROW MUST BIND. The broker groups by model alone, so without this the dial's
// promise is decoration: a Q4_K_M row could still route to the bf16 station.
func TestTuningAQuantRowExcludesTheOtherQuants(t *testing.T) {
	m := browseSeed(100)
	m.bands = groupBands(quantOffers(), nil)
	var q4 band
	for _, b := range m.bands {
		if b.quant == "Q4_K_M" {
			q4 = b
		}
	}
	skip := m.quantExcludes(q4)
	joined := strings.Join(skip, ",")
	if !strings.Contains(joined, "c-qwen") {
		t.Errorf("the bf16 station was not excluded: %v", skip)
	}
	for _, keep := range []string{"a-qwen", "b-qwen"} {
		if strings.Contains(joined, keep) {
			t.Errorf("%s runs the CHOSEN quant and must stay available for failover: %v", keep, skip)
		}
	}
	// The unstated stations are a different row, so they are excluded too - the operator
	// picked known weights, and "unknown" is not those weights.
	if !strings.Contains(joined, "d-qwen") {
		t.Errorf("an unstated-quant station was not excluded from a stated-quant row: %v", skip)
	}
}

// A band with NO stated quant excludes NOTHING. Absence of information is not a
// preference: narrowing routing on the strength of missing metadata would turn "I do not
// know what this is" into "I insist on not knowing".
func TestAnUnstatedBandBindsNothing(t *testing.T) {
	m := browseSeed(100)
	m.bands = groupBands(quantOffers(), nil)
	var blank band
	for _, b := range m.bands {
		if b.quant == "" {
			blank = b
		}
	}
	if skip := m.quantExcludes(blank); len(skip) != 0 {
		t.Errorf("a band with no stated quant excluded %v", skip)
	}
}

// Q CYCLES the filter: off -> each quant on the dial -> off.
func TestQCyclesTheQuantFilter(t *testing.T) {
	m := browseSeed(100)
	m.bands = groupBands(quantOffers(), nil)
	m.scanned = true

	var tm tea.Model = m
	seen := []string{}
	for i := 0; i < 4; i++ {
		tm, _ = tm.Update(keyMsg("Q"))
		seen = append(seen, asModel(tm).fQuant)
	}
	// Two quants on the dial (BF16, Q4_K_M) + off, then it wraps.
	if seen[0] != "BF16" || seen[1] != "Q4_K_M" || seen[2] != "" || seen[3] != "BF16" {
		t.Errorf("the cycle was %v, want BF16 -> Q4_K_M -> off -> BF16", seen)
	}
}

// The filter actually narrows the list, and the strip NAMES which quant - an operator who
// cannot see which one cannot tell a narrowed dial from an empty one.
func TestTheQuantFilterNarrowsAndNamesItself(t *testing.T) {
	m := browseSeed(100)
	m.bands = groupBands(quantOffers(), nil)
	m.scanned = true
	m.fQuant = "Q4_K_M"

	vis := m.visibleBands()
	if len(vis) != 1 || vis[0].quant != "Q4_K_M" {
		t.Fatalf("the filter did not narrow to one row: %+v", vis)
	}
	if !m.filtersActive() {
		t.Error("a quant filter must count as an active filter, or the clear hint never shows")
	}
	if strip := stripANSI(m.filterLine(len(vis), len(m.bands))); !strings.Contains(strip, "Q4_K_M") {
		t.Errorf("the filter strip does not name the quant: %q", strip)
	}
}

// The NAME filter matches the quant too, so "q4_k_m" narrows without new syntax.
func TestTheNameFilterAlsoMatchesTheQuant(t *testing.T) {
	m := browseSeed(100)
	m.bands = groupBands(quantOffers(), nil)
	m.scanned = true
	m.filterApplied = "bf16"
	vis := m.visibleBands()
	if len(vis) != 1 || vis[0].quant != "BF16" {
		t.Errorf("the name filter did not match on quant: %+v", vis)
	}
}
