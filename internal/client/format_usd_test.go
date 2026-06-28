package client

import "testing"

// TestFormatUSD locks the ONE canonical money renderer shared by the TUI (dollars()
// delegates here) and the CLI (the reply-footer Status uses it), so a cost reads identically
// everywhere: 0 -> "$0.00", a sub-cent charge at ~3 significant figures (never $0.00 for a real
// charge), and >= $0.01 at two decimals. (features/money/cost_precision.feature.)
func TestFormatUSD(t *testing.T) {
	cases := map[float64]string{
		0:          "$0.00",
		-1:         "-",
		12.34:      "$12.34",
		0.01:       "$0.01",
		0.12:       "$0.12",
		0.000123:   "$0.000123",
		0.0000005:  "$0.0000005",
		0.00000036: "$0.00000036", // a few output tokens at $0.01/1M - never "$0.00"
		0.00000285: "$0.00000285",
	}
	for in, want := range cases {
		if got := FormatUSD(in); got != want {
			t.Errorf("FormatUSD(%v) = %q, want %q", in, got, want)
		}
	}
	// A real sub-cent charge must never collapse to "$0.00" (the bug this whole change fixes).
	if FormatUSD(0.0004) == "$0.00" {
		t.Error("sub-cent cost collapsed to $0.00")
	}
}
