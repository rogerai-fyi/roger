package main

// The auto-start hooks, exercised THROUGH DISK rather than in the abstract.
//
// tuiHooks builds the closures the TUI calls; the config round-trip tests beside this file
// pin the shape of what gets written, but only these prove the wiring - that pressing the
// key in the terminal actually lands a decision in config.json, and that a decision in
// config.json actually reaches the controller on the next launch. A shape test passes
// happily while the closure is never attached.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rogerai.fm/roger/v6/internal/tui"
)

// SaveAutoStart must persist a decision, and must persist it BESIDE the model's price
// rather than on top of it - share_prices is one store with two writers.
func TestSaveAutoStartPersistsBesideThePrice(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// A model that already has a price card from the pricing editor.
	c := loadConfig()
	c.Prices = map[string]SharePrice{"qwen3.8-27b": {PriceIn: 0.2, PriceOut: 0.01}}
	if err := saveConfig(c); err != nil {
		t.Fatal(err)
	}

	h := tuiHooks(loadConfig())
	if h.SaveAutoStart == nil {
		t.Fatal("the TUI was handed no SaveAutoStart hook, so pressing s would change " +
			"nothing across a restart")
	}
	h.SaveAutoStart("qwen3.8-27b", true)

	got := loadConfig().Prices["qwen3.8-27b"]
	if got.AutoStart == nil || !*got.AutoStart {
		t.Fatalf("the decision did not reach config.json: %+v", got)
	}
	if got.PriceIn != 0.2 || got.PriceOut != 0.01 {
		t.Fatalf("saving auto-start destroyed the price card: %+v", got)
	}
}

// An explicit "no" has to survive the write, because absent and false mean different things
// on the next launch: absent re-arms on the next share, false does not.
func TestSaveAutoStartPersistsAnExplicitNo(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	h := tuiHooks(loadConfig())
	h.SaveAutoStart("m", false)

	got := loadConfig().Prices["m"]
	if got.AutoStart == nil {
		t.Fatal("an explicit 'do not auto-start' was written as an ABSENCE; the next launch " +
			"reads that as undecided and sharing re-arms the model")
	}
	if *got.AutoStart {
		t.Fatalf("the decision flipped on the way to disk: %v", *got.AutoStart)
	}
}

// The other direction: a decision on disk must reach the TUI at launch, and ONLY for models
// that actually carry one.
func TestSavedAutoStartIsSeededFromConfig(t *testing.T) {
	on, off := true, false
	cfg := config{Prices: map[string]SharePrice{
		"armed":     {AutoStart: &on},
		"disarmed":  {AutoStart: &off},
		"undecided": {PriceOut: 0.01},
	}}

	h := tuiHooks(cfg)

	if len(h.SavedAutoStart) != 2 {
		t.Fatalf("seeded %d decisions, want 2 - a model with no auto_start is UNDECIDED and "+
			"must not appear at all: %v", len(h.SavedAutoStart), h.SavedAutoStart)
	}
	if !h.SavedAutoStart["armed"] {
		t.Error("an armed model was not seeded")
	}
	if v, ok := h.SavedAutoStart["disarmed"]; !ok || v {
		t.Error("an explicitly disarmed model must be seeded as false, not dropped - " +
			"dropping it is what re-arms it")
	}
	if _, ok := h.SavedAutoStart["undecided"]; ok {
		t.Error("a model with no recorded decision was seeded, which turns 'never decided' " +
			"into a decision")
	}
}

// SavePrice is the other writer on share_prices and must not clobber the auto-start bit.
func TestSavePriceKeepsAnAutoStartDecision(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	h := tuiHooks(loadConfig())
	h.SaveAutoStart("m", true)
	h.SavePrice("m", tui.Pricing{In: 0.5, Out: 0.05})

	got := loadConfig().Prices["m"]
	if got.AutoStart == nil || !*got.AutoStart {
		t.Fatal("re-pricing a model destroyed its auto-start decision - it would silently " +
			"stop coming back on air, with nothing to say why")
	}
	if got.PriceIn != 0.5 || got.PriceOut != 0.05 {
		t.Fatalf("the price did not land: %+v", got)
	}
}

// And the file on disk is real JSON in the expected place - the hooks own a config write,
// so a test that only reads back through loadConfig could pass on an in-memory illusion.
func TestAutoStartReachesTheConfigFileOnDisk(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	tuiHooks(loadConfig()).SaveAutoStart("m", true)

	b, err := os.ReadFile(filepath.Join(dir, "rogerai", "config.json"))
	if err != nil {
		t.Fatalf("no config.json was written: %v", err)
	}
	if !strings.Contains(string(b), `"auto_start"`) {
		t.Errorf("config.json carries no auto_start key:\n%s", b)
	}
}
