package webui

// ZERO IS NOT A MEASUREMENT - the BROWSE table's half of a rail the rest of the product
// already follows.
//
// The wire contract states it twice, in the struct that carries these very fields:
// Offer.TTFTMs is "probe-measured TTFT (ms; 0 = unmeasured)" and CheapTPS is "that node's
// measured tok/s (0 = unmeasured)". So a station the prober has not reached arrives with
// tps 0 and ttft_ms 0, and BROWSE rendered both as figures - "0" and "0ms" - which reads
// as "measured, and infinitely fast". That is precisely backwards when the table exists to
// help someone choose between stations.
//
// The dial has always been right about this (tpsCell renders "- t/s" unless tps > 0). The
// tests below are written against the shipped asset because that is what the browser runs.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func consoleJS(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("assets/console.js")
	if err != nil {
		t.Fatalf("read console.js: %v", err)
	}
	return string(b)
}

// The specific trap: `x != null` is TRUE for 0, so a null-guard looks like it handles the
// unmeasured case and does not. The guard has to be a positivity test.
func TestBrowseTreatsZeroTpsAndTtftAsUnmeasured(t *testing.T) {
	js := consoleJS(t)

	for _, field := range []string{"o.tps", "o.ttft_ms"} {
		nullGuard := regexp.MustCompile(regexp.QuoteMeta(field) + `\s*!=\s*null`)
		if nullGuard.MatchString(js) {
			t.Errorf("%s is guarded with != null, which is TRUE for 0 - an unmeasured "+
				"station would render as a measured zero. Guard with > 0.", field)
		}
		posGuard := regexp.MustCompile(regexp.QuoteMeta(field) + `\s*>\s*0`)
		if !posGuard.MatchString(js) {
			t.Errorf("%s has no `> 0` guard; the wire contract says 0 means unmeasured", field)
		}
	}
}

// And the absent case must actually render the absence glyph rather than an empty cell -
// a blank cannot be told apart from a column that failed to render.
func TestBrowseRendersUnmeasuredAsTheAbsenceGlyph(t *testing.T) {
	js := consoleJS(t)
	i := strings.Index(js, "o.tps > 0")
	if i < 0 {
		t.Fatal("the tps cell is gone or renamed")
	}
	line := js[i:]
	if j := strings.Index(line, "\n"); j > 0 {
		line = line[:j]
	}
	if !strings.Contains(line, "—") {
		t.Errorf("unmeasured tps does not render the absence glyph: %s", line)
	}
}

// The balance placeholder is the same rail applied to money. $0.00 before the fetch lands
// reads as "you have no money"; the real figure on this machine was $120.96.
func TestBalancePlaceholderIsNotAFabricatedZero(t *testing.T) {
	b, err := os.ReadFile("assets/console.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)

	i := strings.Index(html, `id="acct-balance"`)
	if i < 0 {
		t.Fatal("the balance figure is gone or renamed")
	}
	cell := html[i:]
	if j := strings.Index(cell, "</div>"); j > 0 {
		cell = cell[:j]
	}
	if strings.Contains(cell, "$0.00") {
		t.Errorf("the balance placeholder is a fabricated $0.00 - use an absence glyph "+
			"until the account fetch lands: %s", cell)
	}
}

// ONE PRODUCT, TWO WINDOWS - the console must not contradict the terminal about money.
//
// The SHARE table's value column is a NODE-LOCAL tally at the operator's own price card,
// not settled money: serving your own traffic is $0 by design, so a rig serving its owner
// accrues a figure here while the ledger stays at zero. The terminal was corrected to say
// AT LIST; the console went on heading the same field EARNINGS, which is the same false
// claim in the surface more people look at.
func TestConsoleDoesNotHeadTheValueColumnEarnings(t *testing.T) {
	b, err := os.ReadFile("assets/console.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	if strings.Contains(html, ">EARNINGS<") {
		t.Error("the console still heads this column EARNINGS - it is a node-local tally at " +
			"list price, and the terminal already says AT LIST. Two windows on one store " +
			"must not disagree about what the number means.")
	}
	if !strings.Contains(html, ">AT LIST<") {
		t.Error("the console has no honest header for the value column")
	}
}

// And canary work is reported beside the operator's numbers, not folded into them or
// dropped - the console's half of the rail the terminal holds.
func TestConsoleReportsUnbilledBrokerChecks(t *testing.T) {
	js := consoleJS(t)
	if !strings.Contains(js, "unbilled broker checks") {
		t.Error("the console never mentions the broker's canary traffic, so a busy rig " +
			"reporting little served traffic cannot explain itself there")
	}
	// Guarded on presence, not rendered as a zero.
	if !strings.Contains(js, "t.probes > 0") {
		t.Error("the probe line is not guarded with `> 0`; an unprobed station would render " +
			"a zero, which reads as a measurement")
	}
}
