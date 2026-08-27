package tui

// The SHARE table's two money-truth rules.
//
// Both of these were wrong at once on a real machine, and together they made an
// earnings figure impossible to reconcile: the table showed "$0.01/1M out" beside
// "$0.27", while the price actually driving that number - $0.20/1M IN, twenty times
// larger - was not on screen at all, and the $0.27 was not money in the first place.

import (
	"rogerai.fm/roger/v6/internal/agent"
	"strings"
	"testing"
)

// Cost() bills BOTH axes:
//
//	(prompt x PriceIn + completion x PriceOut) / 1e6
//
// so a cell that prints only the output price hides the term that dominates on a
// chat workload, where prompts are far longer than completions.
func TestSharePriceCellShowsBothAxes(t *testing.T) {

	got := stripANSI(sharePriceText(0.20, 0.01))

	if !strings.Contains(got, "0.20") {
		t.Errorf("the INPUT price is missing from %q - it is what most of the bill comes from", got)
	}
	if !strings.Contains(got, "0.01") {
		t.Errorf("the output price is missing from %q", got)
	}
}

// An output-only price still reads as before: no input term means nothing to show.
func TestSharePriceCellStaysTerseWithoutAnInputPrice(t *testing.T) {
	got := stripANSI(sharePriceText(0, 0.01))
	if strings.Contains(got, "in") {
		t.Errorf("an unpriced input axis should not be mentioned: %q", got)
	}
	if !strings.Contains(got, "0.01") {
		t.Errorf("the output price is missing: %q", got)
	}
}

// The column must NOT claim to be earnings.
//
// Session.Earnings() is a node-local tally computed from the node's OWN price card;
// the node never learns what the broker charged. Consuming your own node is $0 by
// design, so a rig serving its owner accrues a number here while the ledger stays at
// zero - which is exactly what happened ($0.27 shown, $0.00 payable and $0.00 held).
func TestShareColumnDoesNotClaimToBeEarnings(t *testing.T) {
	srv := fakeBroker(t)
	m := NewWithHooks(srv.URL, "tester", nil, Hooks{})
	m.setShareRows(freeRows(2))
	m.width, m.height = 140, 30
	v := stripANSI(m.shareView(140))

	if strings.Contains(v, "EARNINGS") {
		t.Error("the SHARE table still heads this column EARNINGS - it is a node-local " +
			"tally at list price, and self-use traffic makes it diverge from the ledger")
	}
	if !strings.Contains(v, "AT LIST") {
		t.Error("the column has no honest header")
	}
}

// And the operator must be told where real money is, beside the number rather than
// buried in docs - otherwise the rename just moves the confusion.
func TestShareSaysWhereRealMoneyLives(t *testing.T) {
	srv := fakeBroker(t)
	m := NewWithHooks(srv.URL, "tester", nil, Hooks{})
	m.setShareRows(freeRows(2))
	m.width, m.height = 140, 30
	// one row live, so the AT LIST column is populated and the note applies
	m.shares = map[string]*agent.Session{m.shareRows[0].model: {}}
	v := stripANSI(m.shareView(140))
	for _, want := range []string{"not settled money", "roger payout"} {
		if !strings.Contains(v, want) {
			t.Errorf("the settlement note is missing %q", want)
		}
	}
	if !strings.Contains(v, "$0") {
		t.Error("the note does not say that serving your own traffic is free")
	}
}

// A rig with nothing on air gets no lecture: the note exists to explain a populated
// column, not to fill space.
func TestNoSettlementNoteWhenNothingIsOnAir(t *testing.T) {
	srv := fakeBroker(t)
	m := NewWithHooks(srv.URL, "tester", nil, Hooks{})
	m.setShareRows(freeRows(2))
	m.width, m.height = 140, 30
	if strings.Contains(stripANSI(m.shareView(140)), "not settled money") {
		t.Error("the settlement note shows with nothing on air")
	}
}
