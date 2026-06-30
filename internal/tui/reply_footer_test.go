package tui

import (
	"strings"
	"testing"
	"time"
)

// TestReplyFooterRichMetrics: a reply with broker-reported metrics renders one calm line
// carrying tokens in/out, t/s, latency, and cost (the info the user asked to see), and
// /stats (verbose) adds a price line.
func TestReplyFooterRichMetrics(t *testing.T) {
	msg := chatMsg{
		status:    "node-x · $0.000123",
		cost:      0.000123,
		provider:  "node-x",
		tokensIn:  1253,
		tokensOut: 340,
		tps:       48.6,
		priceIn:   0.20,
		priceOut:  0.50,
		latency:   2100 * time.Millisecond,
	}
	compact := stripANSI(strings.Join(replyFooter(msg, false), "\n"))
	for _, want := range []string{"node-x", "↑1.3k", "↓340", "tok", "49 t/s", "2.1s", "$0.000123"} {
		if !strings.Contains(compact, want) {
			t.Errorf("compact footer missing %q\n got: %q", want, compact)
		}
	}
	if strings.Contains(compact, "price") {
		t.Errorf("compact footer must NOT include the price line: %q", compact)
	}
	verbose := stripANSI(strings.Join(replyFooter(msg, true), "\n"))
	for _, want := range []string{"price", "↑$0.20", "↓$0.50", "/1M"} {
		if !strings.Contains(verbose, want) {
			t.Errorf("verbose footer missing %q\n got: %q", want, verbose)
		}
	}
}

// TestReplyFooterFallback: with no broker metrics (a free turn with no receipt) the footer
// falls back to the legacy "provider · $cost" one-liner - never an empty footer.
func TestReplyFooterFallback(t *testing.T) {
	msg := chatMsg{status: "demo-rig · $0", cost: 0}
	got := stripANSI(strings.Join(replyFooter(msg, false), "\n"))
	if !strings.Contains(got, "demo-rig · $0") {
		t.Errorf("fallback footer = %q, want the legacy status line", got)
	}
}

func TestHumanTokensAndLatency(t *testing.T) {
	for _, c := range []struct {
		n    int
		want string
	}{{0, "0"}, {340, "340"}, {999, "999"}, {1000, "1.0k"}, {1253, "1.3k"}, {12000, "12.0k"}} {
		if got := humanTokens(c.n); got != c.want {
			t.Errorf("humanTokens(%d) = %q, want %q", c.n, got, c.want)
		}
	}
	for _, c := range []struct {
		d    time.Duration
		want string
	}{{0, ""}, {850 * time.Millisecond, "850ms"}, {time.Second, "1.0s"}, {2100 * time.Millisecond, "2.1s"}} {
		if got := humanLatency(c.d); got != c.want {
			t.Errorf("humanLatency(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
