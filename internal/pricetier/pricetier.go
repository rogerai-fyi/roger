// Package pricetier renders the broker's neutral, buyer-facing price-tier (0..4) into the
// SAME display glyphs on every surface (CLI band table, TUI, web companion), so a band reads
// identically everywhere. The tier is CLASSIFIED upstream (the broker, carried on each offer
// as PriceTier); this package only INTERPRETS it for display. It is the single source of the
// "$ … $$$$" render that the broker, TUI, and client previously each reimplemented.
package pricetier

import "strings"

// Render maps a tier (0..4) + the active OUT-price to display glyphs + an optional FAVORABLE
// chip. The rules (favorable-only, never negative):
//
//	priceOut <= 0      -> ("FREE", "")          FREE wins over any tier.
//	tier 1             -> ("$", "good price")   only the cheapest tier is editorialized.
//	tier 2/3/4         -> ("$$".."$$$$", "")    neutral bars, no chip.
//	tier 0 / out-range -> ("", "")              priced-but-unclassifiable: nothing (the raw
//	                                            price renders elsewhere).
func Render(tier int, priceOut float64) (bars, chip string) {
	if priceOut <= 0 {
		return "FREE", ""
	}
	if tier < 1 || tier > 4 {
		return "", ""
	}
	bars = strings.Repeat("$", tier)
	if tier == 1 {
		chip = "good price"
	}
	return bars, chip
}

// Label is Render flattened to one plain-text cell for the CLI band table: "FREE", "" (tier
// 0 / out-of-range), or the bars with the chip appended ("$ good price", "$$", …). No color:
// the glyphs + the one favorable word carry the read under NO_COLOR or a pipe.
func Label(tier int, priceOut float64) string {
	bars, chip := Render(tier, priceOut)
	if chip != "" {
		return bars + " " + chip
	}
	return bars
}
