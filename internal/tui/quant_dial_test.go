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

// ── THE STANDING RULE ([3] CONFIG preference) ────────────────────────────────
//
// The dial's Q filter is a VIEW. This is the RULE: it binds routing, so it also governs
// the agent and `roger use` - turns nobody is watching, which is the case a filter can
// never help with.

func TestAnAcceptedQuantSetBindsRouting(t *testing.T) {
	m := browseSeed(100)
	m.bands = groupBands(quantOffers(), nil)
	m.limits = &LimitStore{Models: map[string]Limit{
		"qwen3.8-27b": {Quants: []string{"Q4_K_M"}},
	}}
	skip := strings.Join(m.prefExcludes("qwen3.8-27b"), ",")
	if !strings.Contains(skip, "c-qwen") {
		t.Errorf("the bf16 station was not excluded by the rule: %q", skip)
	}
	for _, keep := range []string{"a-qwen", "b-qwen"} {
		if strings.Contains(skip, keep) {
			t.Errorf("%s runs the accepted quant and must stay available: %q", keep, skip)
		}
	}
}

// AN UNSTATED QUANT IS ACCEPTED BY ANY RULE. A station that said nothing has not said the
// WRONG thing - refusing it would narrow routing on missing metadata rather than on a
// station's actual claim, and would quietly shrink the market for anyone who set a rule.
func TestAnUnstatedQuantIsNotRefusedByARule(t *testing.T) {
	lim := Limit{Quants: []string{"Q4_K_M"}}
	if !lim.acceptsQuant("") {
		t.Error("a station that stated no quant was refused by a quant rule")
	}
	if !lim.acceptsQuant("q4_k_m") {
		t.Error("the rule is case-sensitive - a label is a name, not a token")
	}
	if lim.acceptsQuant("BF16") {
		t.Error("a rule that accepts everything is not a rule")
	}
	// No rule accepts everything.
	if !(Limit{}).acceptsQuant("ANYTHING") {
		t.Error("an empty rule must accept any quant - that is the default")
	}
}

// BOTH CONSTRAINTS TRAVEL TOGETHER. The tuned row and the standing rule answer different
// questions - "the row I am on" and "what I will ever accept" - and an operator who set a
// rule and then tuned a row means both at once.
func TestTheRowAndTheRuleAreUnioned(t *testing.T) {
	m := browseSeed(100)
	m.bands = groupBands(quantOffers(), nil)
	m.limits = &LimitStore{Models: map[string]Limit{
		"qwen3.8-27b": {Quants: []string{"Q4_K_M", "BF16"}},
	}}
	var q4 band
	for _, b := range m.bands {
		if b.quant == "Q4_K_M" {
			q4 = b
		}
	}
	skip := strings.Join(m.routeExcludes(q4), ",")
	// The row excludes BF16; the rule excludes the unstated stations. Both must appear.
	if !strings.Contains(skip, "c-qwen") {
		t.Errorf("the row's constraint was dropped: %q", skip)
	}
	if !strings.Contains(skip, "d-qwen") {
		t.Errorf("the unstated stations were not excluded by the row: %q", skip)
	}
	// And no duplicates, or the header repeats a station.
	if strings.Count(skip, "c-qwen") != 1 {
		t.Errorf("a station is named twice: %q", skip)
	}
}

// What the operator types becomes the rule: upper-cased (a quant is a NAME and must match
// the label a station advertises), deduped, and tolerant of commas or spaces because
// people write lists both ways.
func TestParsingAnAcceptedQuantList(t *testing.T) {
	for in, want := range map[string]string{
		"q4_k_m":              "Q4_K_M",
		"Q4_K_M IQ4_XS":       "Q4_K_M IQ4_XS",
		"q4_k_m, iq4_xs":      "Q4_K_M IQ4_XS",
		"  Q4_K_M   q4_k_m  ": "Q4_K_M",
		"":                    "",
		"   ":                 "",
	} {
		if got := strings.Join(parseQuantList(in), " "); got != want {
			t.Errorf("parseQuantList(%q) = %q, want %q", in, got, want)
		}
	}
}

// The card shows the rule, and states "any" as a WORD - a blank cell would read as
// "nothing allowed" on the one row where that misreading would be catastrophic.
func TestTheCardStatesTheQuantRule(t *testing.T) {
	m := privateTab(t)
	m.limits = &LimitStore{Models: map[string]Limit{}}
	m.bands = []band{{model: "grok-4.6", online: true, cheapest: &offer{Model: "grok-4.6"}}}
	mm, _ := m.openBandConfig("grok-4.6", modeBrowse)
	if out := stripANSI(asModel(mm).bandConfigView(100)); !strings.Contains(out, "any") {
		t.Errorf("an unset quant rule must say \"any\":\n%s", out)
	}
	gm := asModel(mm)
	gm.limits.Set("grok-4.6", Limit{Quants: []string{"Q4_K_M"}})
	if out := stripANSI(gm.bandConfigView(100)); !strings.Contains(out, "Q4_K_M") {
		t.Errorf("a set rule is not shown:\n%s", out)
	}
}

// A QUANT-ONLY RULE MUST PERSIST. Set treats an all-zero limit as "unset" and clears it;
// before the Quants check was added there, saving a quant rule with no price cap deleted
// it on the spot - the operator set a rule and the store threw it away silently.
func TestAQuantOnlyRuleSurvivesBeingSaved(t *testing.T) {
	s := &LimitStore{Models: map[string]Limit{}}
	s.Set("m", Limit{Quants: []string{"Q4_K_M"}})
	if got := s.resolve("m"); len(got.Quants) != 1 || got.Quants[0] != "Q4_K_M" {
		t.Fatalf("a quant-only rule did not survive: %+v", got)
	}
	// And a limit that says NOTHING is still cleared, or the store fills with empties.
	s.Set("m", Limit{})
	if got := s.resolve("m"); len(got.Quants) != 0 || got.MaxOut != 0 {
		t.Errorf("an empty limit was kept: %+v", got)
	}
}

// THE INPUT that sets the rule: Q opens it seeded with the current rule, enter saves,
// esc discards, and an EMPTY value clears the rule rather than meaning "allow nothing".
func TestTheQuantRuleInput(t *testing.T) {
	base := func() model {
		m := privateTab(t)
		m.limits = &LimitStore{Models: map[string]Limit{"grok-4.6": {Quants: []string{"Q4_K_M"}}}}
		m.bands = []band{{model: "grok-4.6", online: true, quant: "Q4_K_M",
			cheapest: &offer{Model: "grok-4.6"}}}
		mm, _ := m.openBandConfig("grok-4.6", modeBrowse)
		return asModel(mm)
	}

	// Q opens it, SEEDED with the rule in force - so it edits rather than silently
	// replacing what is already there.
	opened, _ := base().onBandConfigKey(keyMsg("Q"))
	gm := asModel(opened)
	if gm.mode != modeBandQuants {
		t.Fatalf("Q did not open the rule input (mode %v)", gm.mode)
	}
	if gm.cfgLabelIn.Value() != "Q4_K_M" {
		t.Errorf("the input was not seeded with the current rule: %q", gm.cfgLabelIn.Value())
	}
	view := stripANSI(gm.bandQuantsView(100))
	if !strings.Contains(view, "RULE") || !strings.Contains(view, "any quant") {
		t.Errorf("the input must say it is a rule and what empty means:\n%s", view)
	}

	// esc discards.
	esc, _ := gm.onBandQuantsKey(keyMsg2(tea.KeyEsc))
	em := asModel(esc)
	if em.mode != modeBandConfig {
		t.Error("esc did not return to the card")
	}
	if len(em.limits.resolve("grok-4.6").Quants) != 1 {
		t.Error("esc changed the rule")
	}

	// enter saves what was typed.
	gm.cfgLabelIn.SetValue("iq4_xs bf16")
	saved, _ := gm.onBandQuantsKey(keyMsg2(tea.KeyEnter))
	sm := asModel(saved)
	if got := strings.Join(sm.limits.resolve("grok-4.6").Quants, " "); got != "IQ4_XS BF16" {
		t.Errorf("the saved rule is %q, want IQ4_XS BF16", got)
	}

	// EMPTY clears the rule - it means "any", never "none".
	sm.mode = modeBandQuants
	sm.cfgLabelIn.SetValue("")
	cleared, _ := sm.onBandQuantsKey(keyMsg2(tea.KeyEnter))
	cm := asModel(cleared)
	if len(cm.limits.resolve("grok-4.6").Quants) != 0 {
		t.Errorf("an empty value did not clear the rule: %+v", cm.limits.resolve("grok-4.6"))
	}
	if !strings.Contains(stripANSI(cm.status), "any quant") {
		t.Errorf("clearing must say every quant is accepted now, got %q", stripANSI(cm.status))
	}
}

// Typing into the input edits it rather than being swallowed.
func TestTheQuantRuleInputAcceptsTyping(t *testing.T) {
	m := privateTab(t)
	m.limits = &LimitStore{Models: map[string]Limit{}}
	mm, _ := m.openBandConfig("grok-4.6", modeBrowse)
	opened, _ := asModel(mm).onBandConfigKey(keyMsg("Q"))
	gm := asModel(opened)
	typed, _ := gm.onBandQuantsKey(keyMsg("x"))
	if asModel(typed).cfgLabelIn.Value() == "" {
		t.Error("a keystroke did not reach the input")
	}
}

// routeExcludes must not repeat a station named by BOTH the row and the rule, and must
// return nothing when neither constrains anything.
func TestRouteExcludesIsEmptyWithNoConstraint(t *testing.T) {
	m := browseSeed(100)
	m.bands = groupBands(quantOffers(), nil)
	m.limits = &LimitStore{Models: map[string]Limit{}}
	var blank band
	for _, b := range m.bands {
		if b.quant == "" {
			blank = b
		}
	}
	if got := m.routeExcludes(blank); len(got) != 0 {
		t.Errorf("an unconstrained turn excluded %v", got)
	}
}

// The cycle is a no-op with nothing to cycle, and SAYS so rather than appearing broken.
func TestCyclingWithNoQuantsOnTheDialSaysSo(t *testing.T) {
	m := browseSeed(100)
	m.bands = []band{{model: "plain", online: true, cheapest: &offer{Model: "plain"}}}
	got := m.cycleQuantFilter()
	if got.fQuant != "" {
		t.Errorf("the filter turned on with no quants to filter by: %q", got.fQuant)
	}
	if !strings.Contains(stripANSI(got.status), "no band") {
		t.Errorf("it must say why nothing happened, got %q", stripANSI(got.status))
	}
}

// The name cell degrades gracefully at widths too small for both halves.
func TestTheNameCellAtImpossibleWidths(t *testing.T) {
	bd := band{model: "qwen3.8-27b", quant: "Q4_K_M"}
	for _, w := range []int{0, 1, 5, 10} {
		cell := bandNameCell(bd, w)
		if w > 0 && len([]rune(cell)) != w {
			t.Errorf("w=%d produced %d cells: %q", w, len([]rune(cell)), cell)
		}
	}
}

// pad() with a NON-POSITIVE width must be empty, not a panic. Every column width in the
// browse table is derived from the terminal width, so a window narrow enough to drive one
// to zero took r[:n-1] to r[:-1] and crashed the app - on the one input an operator
// produces by dragging a window edge.
func TestPadSurvivesAZeroWidth(t *testing.T) {
	for _, n := range []int{0, -1, -50} {
		if got := pad("qwen3.8-27b", n); got != "" {
			t.Errorf("pad(_, %d) = %q, want empty", n, got)
		}
	}
	// And it still truncates and pads normally.
	if got := pad("abcdef", 4); len([]rune(got)) != 4 {
		t.Errorf("pad truncation broke: %q", got)
	}
	if got := pad("ab", 5); len([]rune(got)) != 5 {
		t.Errorf("pad padding broke: %q", got)
	}
}
