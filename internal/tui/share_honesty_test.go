package tui

// The SHARE table's two money-truth rules.
//
// Both of these were wrong at once on a real machine, and together they made an
// earnings figure impossible to reconcile: the table showed "$0.01/1M out" beside
// "$0.27", while the price actually driving that number - $0.20/1M IN, twenty times
// larger - was not on screen at all, and the $0.27 was not money in the first place.

import (
	"rogerai.fm/roger/v7/internal/agent"
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

// The column is Core's credit, and says so: EARNED is the owner share Core recorded for this
// share's settled requests (probe_accounting.feature), not a node-local tally at list price,
// which is what the old AT LIST header warned about.
func TestShareColumnIsCoresCreditedShare(t *testing.T) {
	srv := fakeBroker(t)
	m := NewWithHooks(srv.URL, "tester", nil, Hooks{})
	m.setShareRows(freeRows(2))
	m.width, m.height = 140, 30
	v := stripANSI(m.shareView(140))
	if !strings.Contains(v, "EARNED") || strings.Contains(v, "AT LIST") {
		t.Errorf("the SHARE table does not head Core's credit EARNED:\n%s", v)
	}
}

// And the operator is told what the number is and where the money is, beside the number.
func TestShareSaysWhereRealMoneyLives(t *testing.T) {
	srv := fakeBroker(t)
	m := NewWithHooks(srv.URL, "tester", nil, Hooks{})
	m.setShareRows(freeRows(2))
	m.width, m.height = 140, 30
	// one row live, so the EARNED column is populated and the note applies
	m.shares = map[string]*agent.Session{m.shareRows[0].model: {}}
	v := stripANSI(m.shareView(140))
	for _, want := range []string{"Core credited", "after the hold", "roger payout"} {
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
	if strings.Contains(stripANSI(m.shareView(140)), "Core credited") {
		t.Error("the settlement note shows with nothing on air")
	}
}

// HIDDEN IS NOT DISCARDED.
//
// Keeping broker canaries out of SERVED / OUT TOK / EARNED is right - they are unbilled
// work nobody asked for. But removing them WITHOUT SAYING SO trades one confusion for
// another: a rig visibly busy while reporting almost nothing served, and no way to learn
// where the work went. The numbers stay clean; the account of them appears beside.
func TestTheShareViewSaysWhatTheUnbilledWorkWas(t *testing.T) {
	srv := fakeBroker(t)
	m := NewWithHooks(srv.URL, "tester", nil, Hooks{})
	m.setShareRows(freeRows(2))
	m.width, m.height = 140, 30

	sess := &agent.Session{}
	for i := 0; i < 3; i++ {
		sess.RecordHandledForTest(17)
	}
	sess.SetCoreForTest(0, 0, 0, true) // Core's record: nothing served
	m.shares = map[string]*agent.Session{m.shareRows[0].model: sess}

	v := stripANSI(m.shareView(140))
	if !strings.Contains(v, "3 network checks") {
		t.Errorf("the canary work is invisible: a busy rig reporting nothing served has no "+
			"way to explain itself.\n%s", v)
	}
	if !strings.Contains(v, "unbilled") {
		t.Error("the line does not say the work was unbilled, which is the point of separating it")
	}
}

// A station nobody has probed says NOTHING about probes. A printed zero reads as a
// measurement, which is the rail the rest of this file exists to hold.
func TestNoProbeLineWhenNothingHasBeenProbed(t *testing.T) {
	srv := fakeBroker(t)
	m := NewWithHooks(srv.URL, "tester", nil, Hooks{})
	m.setShareRows(freeRows(2))
	m.width, m.height = 140, 30
	m.shares = map[string]*agent.Session{m.shareRows[0].model: {}}

	if strings.Contains(stripANSI(m.shareView(140)), "network checks") {
		t.Error("an unprobed station still reported broker checks")
	}
}

// The probe line must never re-add probes to the operator's own figures - the two tallies
// are separate in agent.Session precisely so this cannot happen by accident.
func TestProbesAreReportedBesideTheNumbersNotInsideThem(t *testing.T) {
	sess := &agent.Session{}
	sess.RecordHandledForTest(17)
	sess.RecordHandledForTest(17)
	sess.RecordHandledForTest(10)
	sess.SetCoreForTest(1, 10, 0.5, true) // Core: one request served

	if reqs, toks := sess.Served(); reqs != 1 || toks != 10 {
		t.Fatalf("SERVED/OUT TOK = %d/%d, want Core's 1/10", reqs, toks)
	}
	if sess.Earnings() != 0.5 {
		t.Fatalf("earned %v, want Core's 0.5", sess.Earnings())
	}
	if pr, pt := sess.ProbeStats(); pr != 2 || pt != 34 {
		t.Fatalf("network checks = %d/%d, want 2/34", pr, pt)
	}
}

// Until Core answers, the table shows "…", never the rig's own count; a record Core could not
// refresh is said to be not current, and claims no checks.
func TestTheShareViewShowsCoresRecordOnly(t *testing.T) {
	srv := fakeBroker(t)
	m := NewWithHooks(srv.URL, "tester", nil, Hooks{})
	m.setShareRows(freeRows(2))
	m.width, m.height = 140, 30
	sess := &agent.Session{}
	for i := 0; i < 4; i++ {
		sess.RecordHandledForTest(17)
	}
	m.shares = map[string]*agent.Session{m.shareRows[0].model: sess}
	v := stripANSI(m.shareView(140))
	if !strings.Contains(v, "…") || strings.Contains(v, "network checks") {
		t.Errorf("before Core answered the table must show … and no checks:\n%s", v)
	}
	if d := stripANSI(m.shareRowDetail(m.shareRows[0], sess)); !strings.Contains(d, "waiting for Core") {
		t.Errorf("the detail line does not say it is waiting for Core: %s", d)
	}
	sess.SetCoreForTest(2, 20, 0, false)
	v = stripANSI(m.shareView(140))
	if !strings.Contains(v, "not current") || strings.Contains(v, "network checks") {
		t.Errorf("a stale record must be said to be not current and claim no checks:\n%s", v)
	}
}
