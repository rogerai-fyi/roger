package main

// ONE STORE, TWO WINDOWS - the console must not destroy what the terminal set.
//
// The manual promises "a spend cap set in the browser is the cap the terminal enforces,
// and the other way round". That promise is only true if each surface WRITES the whole
// limit. It stopped being true when Limit gained Quants (the standing quant rule, set on
// the band card with Q): the console's save path constructed a fresh tui.Limit from the
// two numbers its form knows about, so saving a price cap in the browser silently threw
// away a quant rule set in the terminal.
//
// Silent is the operative word. Nothing failed, nothing warned, and the rule simply
// stopped applying - which for a routing rule means traffic quietly going somewhere the
// operator had ruled out.

import (
	"testing"

	"rogerai.fm/roger/v6/internal/tui"
	"rogerai.fm/roger/v6/internal/webui"
)

func TestConsoleSaveKeepsAQuantRuleTheTerminalSet(t *testing.T) {
	limits := &tui.LimitStore{Models: map[string]tui.Limit{}}

	// The terminal sets a standing quant rule (band card -> Q).
	limits.Set("qwen3.8-27b", tui.Limit{Quants: []string{"Q4_K_M"}})

	// The browser then edits the SAME band's price cap, as SETTINGS does.
	writeLimit(limits)("qwen3.8-27b", webui.SpendLimit{MaxOut: 3})

	got := limits.Snapshot()["qwen3.8-27b"]
	if got.MaxOut != 3 {
		t.Fatalf("the console's own edit was lost: MaxOut = %v, want 3", got.MaxOut)
	}
	if len(got.Quants) != 1 || got.Quants[0] != "Q4_K_M" {
		t.Fatalf("the console destroyed the terminal's quant rule: Quants = %v, want [Q4_K_M].\n"+
			"one store, two windows means a write from either surface preserves the fields "+
			"that surface does not edit", got.Quants)
	}
}

// The console CAN clear a quant rule - it just has to be asked to, rather than doing it as
// a side effect of an unrelated edit. An explicit empty list is an instruction; an absent
// field is not.
func TestConsoleCanClearAQuantRuleWhenAsked(t *testing.T) {
	limits := &tui.LimitStore{Models: map[string]tui.Limit{}}
	limits.Set("m", tui.Limit{MaxOut: 2, Quants: []string{"Q4_K_M"}})

	empty := []string{}
	writeLimit(limits)("m", webui.SpendLimit{MaxOut: 2, Quants: &empty})

	if q := limits.Snapshot()["m"].Quants; len(q) != 0 {
		t.Fatalf("an explicit empty list should clear the rule, got %v", q)
	}
}

// And it can set one, so the browser is not a second-class editor of a field the terminal
// owns - the whole point of two windows on one store.
func TestConsoleCanSetAQuantRule(t *testing.T) {
	limits := &tui.LimitStore{Models: map[string]tui.Limit{}}
	q := []string{"IQ4_XS", "4bit"}
	writeLimit(limits)("m", webui.SpendLimit{MinTPS: 20, Quants: &q})

	got := limits.Snapshot()["m"]
	if len(got.Quants) != 2 || got.Quants[0] != "IQ4_XS" {
		t.Fatalf("Quants = %v, want [IQ4_XS 4bit]", got.Quants)
	}
	if got.MinTPS != 20 {
		t.Fatalf("MinTPS = %v, want 20", got.MinTPS)
	}
}
