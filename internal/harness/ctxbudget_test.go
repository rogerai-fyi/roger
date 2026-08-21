package harness

import (
	"strings"
	"testing"
)

// TOOL OUTPUT MUST FIT THE MODEL IT IS FED TO.
//
// THE INCIDENT (2026-08-07, calm-lynx-53-foundation): the founder ran the agent on Apple's
// on-device `foundation` band - an 8192-token context - and a single web_fetch returned
// ~10KB. The station answered "Exceeded model context window size" and the turn died.
//
// The cause is that maxToolOutput is a flat 16 KiB regardless of the model. On a big band
// that is a rounding error; on an 8K band ONE tool result can consume half the window, and
// two of them plus the conversation cannot fit at all. The cap has to scale with the
// context window it is being fed into.

func TestToolBudgetScalesWithTheContextWindow(t *testing.T) {
	cases := []struct {
		name string
		ctx  int // the model's context window, in tokens
		want int // the byte cap for one tool result
	}{
		{
			// The incident: an 8K window must NOT allow a 10KB tool result.
			//
			// AMENDED 2026-08-21: a tight band gets a SMALLER share (1/8, not 1/4 -
			// smallwindow.go). Measured: on 8192 tokens the persona and tool schemas are
			// already 32% of the window, so a quarter more for one result left two calls
			// unable to fit - which is exactly the overflow the founder hit on a freshly
			// cleared session. The guarantee here is unchanged and is what the name says:
			// the cap leaves room for the conversation. It just has to leave more of it.
			name: "an 8K band gets a cap that leaves room for the conversation",
			ctx:  8192,
			want: 8192 * bytesPerToken / 8,
		},
		{
			// A big band is unchanged: the flat 16 KiB stays the ceiling, so this is not a
			// silent downgrade for everyone else.
			name: "a large window keeps the existing 16 KiB ceiling",
			ctx:  1_000_000,
			want: maxToolOutput,
		},
		{
			// Unknown context (the broker did not report one) must behave exactly as before.
			name: "an unknown window falls back to the old flat cap",
			ctx:  0,
			want: maxToolOutput,
		},
		{
			// A tiny window must still leave a tool usable - a 200-byte result is not worth
			// making the call for. The floor wins over the proportional share.
			name: "a tiny window is floored so tools stay useful",
			ctx:  512,
			want: minToolOutput,
		},
		{
			name: "a negative window is treated as unknown, never as a floor",
			ctx:  -1,
			want: maxToolOutput,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolOutputBudget(tc.ctx); got != tc.want {
				t.Errorf("toolOutputBudget(%d) = %d, want %d", tc.ctx, got, tc.want)
			}
		})
	}
}

// The 8K case is the one that actually failed, so pin the consequence directly rather than
// only the arithmetic: the founder's ~10KB fetch must be cut down to something an 8K model
// can accept alongside its conversation.
func TestEightKBandCannotBeHandedTenKilobytes(t *testing.T) {
	budget := toolOutputBudget(8192)
	if budget >= 10103 {
		t.Fatalf("an 8K band would still accept the %d-byte result that overflowed it (budget %d)", 10103, budget)
	}
	// And it must still be big enough to be worth calling a tool at all.
	if budget < minToolOutput {
		t.Errorf("budget %d is below the usable floor %d", budget, minToolOutput)
	}
}

// clipTo is the Loop's choke point: every tool result passes through it, so a tool that
// forgot to clip internally still cannot blow the window.
func TestClipToMarksTruncationAndRespectsTheBudget(t *testing.T) {
	long := strings.Repeat("x", 5000)

	got := clipTo(long, 1000)
	if len(got) < 1000 {
		t.Fatalf("clipped to %d bytes, want at least the 1000-byte budget", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("a truncated result must SAY it was truncated, or the model treats a partial file as complete")
	}
	if !strings.HasPrefix(got, strings.Repeat("x", 1000)) {
		t.Error("the kept prefix must be the start of the result")
	}

	// A result that fits is returned untouched, with no truncation marker.
	short := "fits fine"
	if got := clipTo(short, 1000); got != short {
		t.Errorf("a fitting result was altered: %q", got)
	}
	// A zero/negative budget must not truncate to nothing - it means "no budget known".
	if got := clipTo(short, 0); got != short {
		t.Errorf("a zero budget must mean unbounded, got %q", got)
	}
}

// Clipping must never split a multi-byte rune, which would hand the model invalid UTF-8.
func TestClipToNeverSplitsARune(t *testing.T) {
	// Three-byte runes, so a byte budget lands mid-rune.
	s := strings.Repeat("café☕", 500)
	got := clipTo(s, 1001)
	if !utf8Valid(got) {
		t.Error("clipping produced invalid UTF-8 (a rune was split)")
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}
