package harness

import (
	"strings"
	"testing"
)

// TestShortFailureKeepsTheRoutingConstraint: a no_match that names the routing key which
// excluded every station keeps the broker's words, because "no station is serving X" sends
// the operator to the market when the fix is their own rule (U, a quant rule, a region).
func TestShortFailureKeepsTheRoutingConstraint(t *testing.T) {
	// The broker's own wording (cmd/rogerai-broker tunnel.go no_match): filter names after
	// "under", the curated hint, the capability phrase.
	for _, raw := range []string{
		"no node offers mistral-large under self_hosted_only (a curated station is available) (status 503)",
		"no node offers qwen3-32b with the tools capability",
		"no node offers m under quantizations",
		"no node offers m under min_tps",
	} {
		got := ShortFailure(raw, "m")
		if strings.Contains(got, NoStationServing("m")) {
			t.Errorf("%q collapsed into %q", raw, got)
		}
	}
	if got := ShortFailure("no node offers m", "m"); got != NoStationServing("m") {
		t.Errorf("a plain no-node stays the no-station phrase, got %q", got)
	}
}
