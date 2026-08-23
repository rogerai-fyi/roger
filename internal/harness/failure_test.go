package harness

import (
	"strings"
	"testing"
)

// failure_test.go - the shared "what went wrong, in words" mapping.
//
// It lives here because BOTH front-ends render failed turns now (the terminal TUI and the
// browser console), and the founder saw the raw "the station returned status 504 with no
// reply" in the browser precisely because the judgement used to be terminal-only.

// The founder's dead end, and the shapes around it. What each case locks is not a
// particular sentence but a property: the raw status string never survives, and the model
// is named wherever naming it turns a code into something actionable.
func TestShortFailureNamesTheCauseNotTheStatus(t *testing.T) {
	cases := []struct {
		name, raw, model string
		wantContains     []string
		wantAbsent       []string
	}{
		{
			name: "a relay 504 with no body is nobody on air",
			raw:  "the station returned status 504 with no reply", model: "grok-4.3",
			wantContains: []string{"no station is serving grok-4.3 right now", "(504)"},
			wantAbsent:   []string{"with no reply"},
		},
		{
			name: "...and says so generically when the band is unknown",
			raw:  "the station returned status 502 with no reply", model: "",
			wantContains: []string{"no station is on air right now", "(502)"},
		},
		{
			name: "a context overflow is a HEALTHY station refusing an oversized turn",
			raw:  "context length exceeded", model: "apple-on-device",
			wantContains: []string{"outgrew apple-on-device's context window"},
			// Reporting this as nobody being on air sends the operator to fix a node that
			// was never broken - it is checked before the no-station shapes for that reason.
			wantAbsent: []string{"no station"},
		},
		{
			name: "an unreachable broker is not a dead band",
			raw:  "could not reach the broker: connection refused", model: "m1",
			wantContains: []string{"could not reach the broker"},
			wantAbsent:   []string{"no station"},
		},
		{
			name: "a station-side inference crash usually recovers, and says so",
			raw:  "decode() failed", model: "m1",
			wantContains: []string{"internal error", "try again"},
			wantAbsent:   []string{"no station"},
		},
		{
			name: "a timeout is a timeout",
			raw:  "the request timed out", model: "m1",
			wantContains: []string{"timed out"},
		},
		{
			name: "an unrecognised cause is passed through, never hidden",
			raw:  "the upstream said something nobody has a phrase for", model: "m1",
			wantContains: []string{"nobody has a phrase for"},
		},
	}
	for _, c := range cases {
		got := ShortFailure(c.raw, c.model)
		for _, want := range c.wantContains {
			if !strings.Contains(got, want) {
				t.Errorf("%s: ShortFailure(%q) = %q, want it to contain %q", c.name, c.raw, got, want)
			}
		}
		for _, bad := range c.wantAbsent {
			if strings.Contains(got, bad) {
				t.Errorf("%s: ShortFailure(%q) = %q, must not contain %q", c.name, c.raw, got, bad)
			}
		}
	}
}

// An unrecognised cause is CLIPPED, so one runaway upstream message cannot swallow the
// line it is rendered on - but it is still passed through, because hiding the real cause
// is worse than a long line.
func TestShortFailureBoundsAnUnrecognisedCause(t *testing.T) {
	long := strings.Repeat("abcdefg ", 40)
	got := ShortFailure(long+"\nsecond line", "")
	if len(got) > 84 {
		t.Errorf("an unrecognised cause must be clipped, got %d chars", len(got))
	}
	if !strings.Contains(got, "…") {
		t.Errorf("a clipped cause must SAY it was clipped: %q", got)
	}
	// Newlines are flattened: this is rendered on one line in both front-ends.
	for _, r := range got {
		if r == '\n' {
			t.Errorf("the cause must be flattened to one line: %q", got)
		}
	}
}

// StatusSuffix keeps the one part of a raw error worth keeping when the rest is noise.
func TestStatusSuffix(t *testing.T) {
	cases := map[string]string{
		"the station returned status 504 with no reply": " (504)",
		"STATUS 200 ok":  " (200)",
		"no status here": "",
		"status ":        "", // a "status" with no code claims nothing
		"status abc":     "",
	}
	for raw, want := range cases {
		if got := StatusSuffix(raw); got != want {
			t.Errorf("StatusSuffix(%q) = %q, want %q", raw, got, want)
		}
	}
}
