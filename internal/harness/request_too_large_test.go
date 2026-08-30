package harness

// THE SAME WALL, MEASURED IN BYTES.
//
// A station can refuse an oversized conversation at the HTTP layer rather than the
// tokenizer, and then it never says "context" at all. The turn stopped dead and told the
// operator to "retry the turn or fix the error" - advice that cannot work: retrying sends
// the same oversized body, and there is nothing a human can fix. The cause is identical to
// a token overflow and so is the remedy.

import (
	"strings"
	"testing"
)

// The exact string a real station returned, from a real session.
const realBodyLimitError = "Maximum request body size 1048576 exceeded, actual body size 1050714 (status 413)"

func TestAnOversizedRequestBodyCompactsInsteadOfStopping(t *testing.T) {
	if !IsContextOverflow(realBodyLimitError) {
		t.Fatal("a 413 body-size refusal is not recognised as an overflow, so the turn asks " +
			"the operator to retry - which sends the same oversized body again")
	}
}

func TestTheOtherWaysAStationSaysTooBig(t *testing.T) {
	for _, s := range []string{
		"413 Request Entity Too Large",
		"Payload Too Large",
		"http: request body size exceeded",
	} {
		if !IsContextOverflow(s) {
			t.Errorf("%q is not recognised as an overflow", s)
		}
	}
}

// NARROW ON PURPOSE. A bare "413" can appear in a model's own answer or in a tool result
// being relayed back, and compacting on that would silently destroy context over a number.
func TestABare413IsNotTreatedAsAnOverflow(t *testing.T) {
	for _, s := range []string{
		"the build produced 413 warnings",
		"HTTP 413",
		"error code 413",
	} {
		if IsRequestTooLarge(s) {
			t.Errorf("%q was treated as an oversized request: compaction would drop the "+
				"operator's context over a number that merely looks like a status code", s)
		}
	}
}

// The remedy line has to stay TRUE. The model's context window is fine here; the station's
// per-request byte cap is not, and naming the wrong one sends an operator to the wrong knob.
func TestTheMessageNamesTheRequestCapNotTheContextWindow(t *testing.T) {
	got := ShortFailure(realBodyLimitError, "gpt-oss-120b")
	if got == "" {
		t.Fatal("no explanation produced")
	}
	if strings.Contains(got, "context window") {
		t.Errorf("the message blames the context window for a request-size refusal: %q", got)
	}
	if !strings.Contains(got, "one request") {
		t.Errorf("the message does not say the limit is per-request: %q", got)
	}
}
