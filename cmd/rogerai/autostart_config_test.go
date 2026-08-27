package main

// share_prices is ONE STORE WITH TWO WRITERS.
//
// The pricing editor owns price_in/price_out/windows; the auto-start toggle owns
// auto_start. Each save path loads the whole config, edits its own model's entry, and
// writes it back - so a path that REBUILDS the entry from the fields it knows about
// silently deletes the other's. That already happened once in this codebase: a price cap
// saved in the browser console erased a quant rule set in the terminal, with nothing
// failing and nothing warning. These lock the same rail for share_prices.

import "testing"

func TestSavingAPriceKeepsTheAutoStartDecision(t *testing.T) {
	on := true
	c := config{Prices: map[string]SharePrice{
		"qwen3.8-27b": {PriceIn: 0.2, PriceOut: 0.01, AutoStart: &on},
	}}

	// What SavePrice does to the entry.
	sp := c.Prices["qwen3.8-27b"]
	sp.PriceIn, sp.PriceOut, sp.Windows = 0.5, 0.05, nil
	c.Prices["qwen3.8-27b"] = sp

	got := c.Prices["qwen3.8-27b"]
	if got.AutoStart == nil {
		t.Fatal("re-pricing a model destroyed its auto-start decision - the model would " +
			"silently stop coming back on air at launch, and nothing would say so")
	}
	if !*got.AutoStart || got.PriceIn != 0.5 {
		t.Fatalf("entry = %+v, want the new price AND the kept decision", got)
	}
}

func TestSavingAutoStartKeepsThePriceCard(t *testing.T) {
	c := config{Prices: map[string]SharePrice{
		"qwen3.8-27b": {PriceIn: 0.2, PriceOut: 0.01, Windows: []SchedWindow{{Start: "22:00", End: "06:00"}}},
	}}

	on := false
	sp := c.Prices["qwen3.8-27b"]
	sp.AutoStart = &on
	c.Prices["qwen3.8-27b"] = sp

	got := c.Prices["qwen3.8-27b"]
	if got.PriceIn != 0.2 || got.PriceOut != 0.01 {
		t.Fatalf("the auto-start toggle destroyed the price card: %+v", got)
	}
	if len(got.Windows) != 1 {
		t.Fatal("the auto-start toggle destroyed the time-of-use schedule - the model " +
			"would start billing at its base price around the clock")
	}
}

// The pointer is the tri-state. A plain bool cannot tell "never decided" (arm it when
// shared) from "decided no" (leave it alone), and JSON omitempty would erase an explicit
// false into an absence - which reads back as undecided and re-arms the model.
func TestAnExplicitNoSurvivesARoundTrip(t *testing.T) {
	off := false
	c := config{Prices: map[string]SharePrice{"m": {AutoStart: &off}}}

	blob, err := jsonRoundTrip(c)
	if err != nil {
		t.Fatal(err)
	}
	got := blob.Prices["m"].AutoStart
	if got == nil {
		t.Fatal("an explicit 'do not auto-start' was erased by the round trip; on the " +
			"next launch it reads as undecided and sharing re-arms the model")
	}
	if *got {
		t.Fatalf("the decision flipped: got %v, want false", *got)
	}
}
