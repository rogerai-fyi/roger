package tui

import (
	"strings"
	"testing"
)

// A context-window overflow is the ONE failure shape where the standard "[2] put one on
// air, [1] tune in" advice is actively wrong. The founder hit it on an Apple on-device
// `foundation` band (8K window): a web_fetch returned ~10KB, the turn overflowed, and the
// TUI told them to go put a station on air - sending them to fix a node that was serving
// correctly the whole time. The band is healthy; the conversation simply outgrew it.
func TestContextOverflowGetsTheRemedyThatActuallyHelps(t *testing.T) {
	// Every phrasing we have seen from a real server must land on the same remedy.
	raws := []string{
		"Exceeded model context window size",                                // Apple foundation
		"the station returned status 400: context length exceeded",          // llama.cpp
		"error: This model's maximum context length is 8192 tokens",         // OpenAI-compatible
		`{"error":{"code":"context_length_exceeded","message":"too long"}}`, // vLLM / gateways
		"failed to allocate kv cache slot",                                  // llama.cpp KV pressure
	}
	for _, raw := range raws {
		t.Run(raw[:min(len(raw), 40)], func(t *testing.T) {
			lines := failureHint(raw, "foundation", false)
			if len(lines) != 2 {
				t.Fatalf("want a phrase + a remedy, got %#v", lines)
			}
			first, remedy := stripANSI(lines[0]), stripANSI(lines[1])

			// The remedy must be the two moves that resolve an overflow...
			if !strings.Contains(remedy, "/clear") || !strings.Contains(remedy, "/model") {
				t.Errorf("remedy should offer /clear and /model, got %q", remedy)
			}
			// ...and must NOT send the operator to fix a node that is not broken.
			if strings.Contains(remedy, "[2]") || strings.Contains(remedy, "[1]") ||
				strings.Contains(strings.ToLower(remedy), "on air") ||
				strings.Contains(strings.ToLower(remedy), "tune in") {
				t.Errorf("an overflow must not advise going on air / tuning in, got %q", remedy)
			}
			// The first line must explain the real cause, naming the window that filled.
			if !strings.Contains(first, "context window") || !strings.Contains(first, "foundation") {
				t.Errorf("first line should name the model whose window filled, got %q", first)
			}
			// It must never be mistaken for an outage.
			if strings.Contains(strings.ToLower(first), "no station") {
				t.Errorf("an overflow is NOT a missing station, got %q", first)
			}
		})
	}
}

// The ordinary relay failures must keep the [2]/[1] hint - this fix narrows the copy for
// one shape, it does not change the default.
func TestOrdinaryFailuresKeepTheOnAirHint(t *testing.T) {
	for _, raw := range []string{"status 504 with no reply", "no station is serving it", "the request timed out"} {
		remedy := stripANSI(failureHint(raw, "m", false)[1])
		if !strings.Contains(remedy, "[2]") || !strings.Contains(remedy, "[1]") {
			t.Errorf("failureHint(%q) remedy = %q, want the [2]/[1] hint", raw, remedy)
		}
	}
}

// Narrow terminals still get a remedy that fits, and it is still the RIGHT one.
func TestContextOverflowRemedyNarrow(t *testing.T) {
	remedy := stripANSI(remedyFor("Exceeded model context window size", true))
	if !strings.Contains(remedy, "/clear") || !strings.Contains(remedy, "/model") {
		t.Errorf("narrow remedy lost its commands: %q", remedy)
	}
	if len(remedy) > 40 {
		t.Errorf("narrow remedy is %d chars, too wide: %q", len(remedy), remedy)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
