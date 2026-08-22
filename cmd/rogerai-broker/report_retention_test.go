package main

import (
	"net/http"
	"testing"
	"time"

	"rogerai.fm/roger/v5/internal/store"
)

// TestReportRetentionIsDerivedNotChosen pins the horizon to the windows it is made of. The
// failure this guards against is not "the number is wrong today" - it is somebody widening
// ROGERAI_REPORT_DECAY_DAYS and a fixed retention constant silently starting to eat the
// evidence inside the window the ban decision reads.
func TestReportRetentionIsDerivedNotChosen(t *testing.T) {
	day := 24 * time.Hour
	b := testBrokerWithDB(store.NewMem())

	b.reportDecayDays, b.nodeBanDays = 30, 3
	if got, want := b.reportRetention(), 30*day+3*day+reportRetentionGrace; got != want {
		t.Errorf("reportRetention() = %s, want decay+suspension+grace = %s", got, want)
	}
	// It must COMFORTABLY exceed the window the corroboration count reads. Equal would be a
	// bug: the sweep would be deleting rows the very next eject decision still counts.
	if b.reportRetention() <= 30*day {
		t.Errorf("retention %s does not exceed the %s decay window it must outlive", b.reportRetention(), 30*day)
	}

	// Widening the decay window widens retention by the same amount.
	before := b.reportRetention()
	b.reportDecayDays = 60
	if got, want := b.reportRetention()-before, 30*day; got != want {
		t.Errorf("doubling the decay window moved retention by %s, want %s", got, want)
	}
	// Lengthening the suspension does too - an appeal against a longer-standing ban needs
	// the evidence to still be there.
	before = b.reportRetention()
	b.nodeBanDays = 10
	if got, want := b.reportRetention()-before, 7*day; got != want {
		t.Errorf("lengthening the suspension moved retention by %s, want %s", got, want)
	}

	// nodeBanDays<=0 disables auto-lift. The term must clamp to zero rather than SHRINK the
	// horizon below the decay window - a negative here would delete evidence the live count
	// is still reading.
	b.reportDecayDays, b.nodeBanDays = 30, -5
	if got, want := b.reportRetention(), 30*day+reportRetentionGrace; got != want {
		t.Errorf("with auto-lift disabled retention = %s, want %s (suspension term clamped to 0)", got, want)
	}
	if b.reportRetention() <= 30*day {
		t.Error("a disabled auto-lift must never pull retention under the decay window")
	}
}

// TestCSAMReportRetentionClearsTheStatutoryFloorAndTheOrdinaryHorizon pins the second
// horizon. A csam-category report is written by POST /report and NOT by preserveCSAM, so
// rogerai.reports is the only copy of it; it gets the 18 USC 2258A(h) period, and it can
// never come out shorter than the ordinary horizon however the decay window is tuned.
func TestCSAMReportRetentionClearsTheStatutoryFloorAndTheOrdinaryHorizon(t *testing.T) {
	day := 24 * time.Hour
	b := testBrokerWithDB(store.NewMem())

	b.reportDecayDays, b.nodeBanDays = 30, 3
	if got := b.csamReportRetention(); got < csamPreservationDays*day {
		t.Errorf("csamReportRetention() = %s, must not fall under the %d-day 2258A(h) period", got, csamPreservationDays)
	}
	if b.csamReportRetention() <= b.reportRetention() {
		t.Error("a csam report must outlive an ordinary one, never be swept first")
	}

	// A deployment with a very wide decay window pushes the ordinary horizon past 90 days.
	// The csam horizon must follow it up, not stay at the statutory floor and become the
	// SHORTER of the two.
	b.reportDecayDays = 400
	if b.csamReportRetention() < b.reportRetention() {
		t.Errorf("csam horizon %s fell under the ordinary horizon %s", b.csamReportRetention(), b.reportRetention())
	}
}

// TestReportRetentionSweepKeepsWhatIsReadAndDropsWhatIsNot is the both-arms test on the
// sweep itself. It asserts rows SURVIVE when something can still read them and VANISH when
// nothing can, and - the invariant that actually matters - that the corroboration count the
// auto-eject and the appeal both read is BYTE-IDENTICAL either side of a sweep.
func TestReportRetentionSweepKeepsWhatIsReadAndDropsWhatIsNot(t *testing.T) {
	day := 24 * time.Hour
	db := store.NewMem()
	b := testBrokerWithDB(db)
	b.reportDecayDays, b.nodeBanDays = 30, 3
	now := time.Now()
	at := func(age time.Duration) int64 { return now.Add(-age).Unix() }

	// inWindow  : inside the decay window - the eject/appeal count reads this one.
	// inAppeal  : past the decay window but inside the suspension it could have caused, so
	//             it is the evidence an operator contesting that suspension is owed.
	// expired   : past the whole horizon; nothing can read it and it is only storage.
	// csamAged  : past the ordinary horizon, kept on the 2258A(h) horizon instead.
	for _, r := range []store.Report{
		{Category: "abuse", NodeID: "n", Detail: "inWindow", IP: "1.1.1.1", CreatedAt: at(10 * day)},
		{Category: "abuse", NodeID: "n", Detail: "inAppeal", IP: "2.2.2.2", CreatedAt: at(32 * day)},
		{Category: "abuse", NodeID: "n", Detail: "expired", IP: "3.3.3.3", CreatedAt: at(60 * day)},
		{Category: store.ReportCategoryCSAM, NodeID: "n", Detail: "csamAged", IP: "4.4.4.4", CreatedAt: at(60 * day)},
	} {
		if _, err := db.AddReport(r); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := db.ReportsByNode("n", 0); len(got) != 4 {
		t.Fatalf("setup wrote %d rows, want 4 - the rest of this test would pass vacuously", len(got))
	}

	since := now.Add(-30 * day).Unix()
	countBefore, err := db.DistinctReporterCountByNode("n", since)
	if err != nil {
		t.Fatal(err)
	}
	if countBefore != 1 {
		t.Fatalf("corroboration count before the sweep = %d, want 1 (only inWindow is in the decay window)", countBefore)
	}

	b.reportRetentionSweepOnce(now)

	have := map[string]bool{}
	got, err := db.ReportsByNode("n", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		have[r.Detail] = true
	}
	for _, keep := range []string{"inWindow", "inAppeal", "csamAged"} {
		if !have[keep] {
			t.Errorf("%q should have SURVIVED the sweep", keep)
		}
	}
	if have["expired"] {
		t.Error(`"expired" should have been REAPED - nothing can read a report past the whole horizon`)
	}
	if len(got) != 3 {
		t.Errorf("sweep left %d rows, want 3", len(got))
	}

	// The whole point: the ban decision and the appeal's auto-exoneration must not be able
	// to tell that a sweep happened.
	if countAfter, _ := db.DistinctReporterCountByNode("n", since); countAfter != countBefore {
		t.Errorf("the sweep changed the corroboration count: %d -> %d", countBefore, countAfter)
	}
}

// TestReportUsesTheAnonLimiterNotTheRelayOne locks which bucket the public write endpoint
// draws from. Both arms, because either alone is satisfiable by the wrong wiring: a tight
// anon limiter proves nothing if the handler is still reading b.rl and b.rl happens to be
// tight too.
func TestReportUsesTheAnonLimiterNotTheRelayOne(t *testing.T) {
	burst := func(b *broker) bool {
		for i := 0; i < 8; i++ {
			if postReport(b, `{"category":"spam","node_id":"n"}`).Code == http.StatusTooManyRequests {
				return true
			}
		}
		return false
	}

	// Tight ANON bucket, generous relay bucket -> the burst must be refused.
	b := testBrokerWithDB(store.NewMem())
	b.rl = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 6000, burst: 1000}
	b.anonRL = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 60, burst: 2}
	if !burst(b) {
		t.Error("a tight ANON limiter must 429 the report burst - /report is reading the wrong bucket")
	}

	// Tight RELAY bucket, generous anon bucket -> the burst must be allowed. This is the arm
	// that fails if the handler goes back to b.rl.
	b = testBrokerWithDB(store.NewMem())
	b.rl = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 60, burst: 2}
	b.anonRL = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 6000, burst: 1000}
	if burst(b) {
		t.Error("the per-identity relay limiter must NOT gate /report - an unauthenticated surface was drawing on the authenticated allowance")
	}
}
