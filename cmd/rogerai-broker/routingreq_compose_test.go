package main

// routingreq_compose_test.go - the pure header/body composition rules of contract §1a: a
// LIMIT stated in both carriers composes to the stricter, in either direction, so a body
// can never loosen a header limit. The behaviour is pinned end to end by
// features/routing/request_shape.feature and regression_pins.feature; this table covers
// every combination of present / absent / zero / above-ceiling directly.

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStricterCap(t *testing.T) {
	_ = os.Unsetenv("ROGERAI_CONSUMER_DEFAULT_MAX_PRICE_OUT")
	_ = os.Unsetenv("ROGERAI_MAX_PRICE_OUT")
	for _, tc := range []struct {
		name         string
		header, body float64
		want         float64 // the composed cap as sent (0 = neither side stated one)
		effectiveOut float64 // what the relay applies as the out cap after default + ceiling
	}{
		{"both absent: the consumer default", 0, 0, 0, 10},
		{"header only", 2, 0, 2, 2},
		{"body only", 0, 0.5, 0.5, 0.5},
		{"both, header stricter: a body cannot raise the header cap", 2, 50, 2, 2},
		{"both, body stricter: a body may tighten", 50, 2, 2, 2},
		{"both equal", 3, 3, 3, 3},
		{"header zero states no cap: the body's applies", 0, 20, 20, 20},
		{"body zero states no cap: the header's stands", 20, 0, 20, 20},
		{"negative reads as absent on either side", -1, 4, 4, 4},
		{"negative body cannot erase the header cap", 4, -1, 4, 4},
		{"body above the ceiling, no header: clamped to the ceiling", 0, 1000, 1000, 100},
		{"both above the ceiling: the lower, then the ceiling", 250, 500, 250, 100},
		{"header under, body above the ceiling: the header", 42, 1e9, 42, 42},
		{"above the default but under the ceiling is honored", 60, 80, 60, 60},
	} {
		got := stricterCap(tc.header, tc.body)
		require.InDelta(t, tc.want, got, 1e-12, tc.name)
		require.InDelta(t, tc.effectiveOut, effectiveRelayMaxOut(got), 1e-12, tc.name+" (effective out cap)")
		if tc.header > 0 {
			// The invariant the rule exists for: once a header states a cap, no body value
			// yields a looser one. (With no header cap a body may state its own, above the
			// default, as a hand-rolled caller always could with the header.)
			require.LessOrEqual(t, effectiveRelayMaxOut(got), effectiveRelayMaxOut(tc.header)+1e-12,
				tc.name+": the body never yields a looser cap than the header alone")
		}
	}
}

func TestStricterFloor(t *testing.T) {
	for _, tc := range []struct {
		name               string
		header, body, want float64
	}{
		{"both absent", 0, 0, 0},
		{"header only", 30, 0, 30},
		{"body only", 0, 30, 30},
		{"header stricter: a body cannot lower the floor", 30, 5, 30},
		{"body stricter: a body may raise it", 5, 30, 30},
		{"equal", 20, 20, 20},
		{"a negative value on either side is no floor", -3, -8, 0},
		{"a negative body leaves the header floor", 12, -1, 12},
	} {
		require.InDelta(t, tc.want, stricterFloor(tc.header, tc.body), 1e-12, tc.name)
	}
}

func TestFreqConflict(t *testing.T) {
	const a, b = "147.520 MHz · 8F3K-9M2Q", "101.100 MHz · 7H2J-4N6P"
	for _, tc := range []struct {
		name         string
		header, body string
		want         bool
	}{
		{"neither", "", "", false},
		{"header only", a, "", false},
		{"body only", "", a, false},
		{"the same code", a, a, false},
		{"the same code, cosmetic part dropped and lower-cased", a, "8f3k9m2q", false},
		{"the same code typed with confusable letters", "8F3K-9M2Q", "8F3K-9M2Q ", false},
		{"a different band", a, b, true},
		{"a body with no valid code against a real header code", a, "nope", true},
		{"two unresolvable strings are not a conflict (both read as no code)", "x", "y", false},
	} {
		require.Equal(t, tc.want, freqConflict(tc.header, tc.body), tc.name)
	}
}
