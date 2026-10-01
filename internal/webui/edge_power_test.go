package webui

import (
	"fmt"
	"strings"
	"testing"

	"rogerai.fm/roger/v6/internal/edge"
)

// The console's rule form offers every metric the fleet can judge: a rule the CLI can set and the
// console cannot is a surface drifting from the others (the throttled metric arrived 2026-10-01).
func TestTheConsoleOffersEveryMetricARuleCanWatch(t *testing.T) {
	js := consoleJS(t)
	for _, m := range edge.Metrics {
		if !strings.Contains(js, fmt.Sprintf("%q", string(m))) {
			t.Errorf("the console's rule form does not offer %q", m)
		}
	}
	// A Pi's power is drawn on the node card in the same words as everywhere else.
	for _, w := range []string{"under-voltage", "frequency capped", "throttled", "soft temperature limit", "since boot", "pi_power"} {
		if !strings.Contains(js, w) {
			t.Errorf("the console does not word a Pi's power: missing %q", w)
		}
	}
}
