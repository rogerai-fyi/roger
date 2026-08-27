package webui

// ONE FORMAT ACROSS BOTH SURFACES. The terminal's band editor joins quant labels with a
// space (internal/tui/band_config.go), and its `roger config` output shows them the same
// way. An operator who copies "Q4_K_M IQ4_XS" out of the terminal and pastes it into the
// browser's quant field expects the two labels the terminal would read. A comma-only split
// turned that whole string into ONE label, which matches no station's quant - so the rule
// silently stopped narrowing anything, the same class of quiet failure the quant-carry fix
// closed. The console must accept the terminal's own separator.

import (
	"regexp"
	"testing"
)

func TestConsoleQuantFieldAcceptsSpacesNotJustCommas(t *testing.T) {
	js := consoleJS(t)

	commaOnly := regexp.MustCompile(`quants[\s\S]{0,80}?\.split\("\s*,\s*"\)`)
	if commaOnly.MatchString(js) {
		t.Errorf("the quant field splits on commas only; the terminal's editor is " +
			"space-separated, so a pasted `Q4_K_M IQ4_XS` becomes one bogus label. " +
			"Split on /[\\s,]+/ so both separators work.")
	}
	bothSeparators := regexp.MustCompile(`\.split\(/\[\\s,\]\+/\)`)
	if !bothSeparators.MatchString(js) {
		t.Errorf("the quant field does not split on /[\\s,]+/; the browser and terminal " +
			"must read the same space-or-comma format")
	}
}
