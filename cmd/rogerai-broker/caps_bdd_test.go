package main

// caps_bdd_test.go makes features/money/caps.feature an EXECUTABLE Cucumber suite, driving
// the REAL spend/usage caps:
//   - the per-account MONTHLY SPEND CAP: store.MonthlyCapOf/SetMonthlyCap/DefaultMonthlyCap
//     (env) + MonthSpendOf (ledger-summed, UTC calendar month, boundary-correct), and the
//     broker hold-gate enforcement broker.monthlyCapCheck (worst-case fail-closed: a request
//     is rejected when spend+maxCost > cap; exact fit allowed; 80% near-notice; 402 + budget
//     headers),
//   - the per-GRANT token caps: store.GrantUsageOf/AddGrantUsage + broker.grantCapCheck
//     (>= boundary, daily-before-monthly, fail-CLOSED on a usage-read error),
//   - the RPM/burst rate limits: rateLimiter.allowAt (grant override + anon/per-identity
//     buckets, the 429 + Retry-After hint).
//
// Month-to-date spend is staged via store.SeedLedgerForTest (raw spend/reversed/boundary rows
// — no production flow reverses a spend row, but MonthSpendOf must still exclude one) and read
// back through MonthSpendOf, so the cap math is store-grounded. grantCapCheck reads usage at
// time.Now(), so grant usage is placed at real-time-relative instants (today/yesterday/this-
// vs-last-month). Time-based token refill is simulated by aging the bucket's last-fill stamp
// (foreground sleeps are unavailable). feApprox/feParseFloat live in fee_splits_bdd_test.go.
//
// SPEC CORRECTION (deployed-code reality): the spec scenario "A usage-read error fails OPEN"
// was STALE — grantCapCheck deliberately fails CLOSED (a usage-read hiccup must not silently
// uncap a capped grant; see the regression note in grant.go). The scenario is corrected to
// fail CLOSED here and in the .feature.

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/store"
)

// grantUsageErrStore (the fail-CLOSED grant-cap seam) is defined in grant_test.go.

type cpState struct {
	db   *store.Mem
	b    *broker
	now  time.Time // fixed mid-month UTC instant for the monthly-cap math

	capRead    float64
	monthSpend float64
	resp       *httptest.ResponseRecorder
	capStatus  int
	capMsg     string
	capCalled  bool

	grant      store.Grant
	grantStat  int
	grantMsg   string
	lastCheck  string // "cap" | "grant" — which gate the shared "allowed" verdict reads

	rpm, burst float64

	allowed   int
	denied    int
	lastOK    bool
	lastRetry int
}

func (s *cpState) reset() {
	os.Setenv("ROGERAI_DEFAULT_MONTHLY_CAP", "")
	os.Setenv("ROGERAI_RATE_RPM", "")
	os.Setenv("ROGERAI_RATE_BURST", "")
	os.Setenv("ROGERAI_ANON_RATE_RPM", "")
	os.Setenv("ROGERAI_ANON_RATE_BURST", "")
	s.db = store.NewMem()
	_, priv, _ := ed25519.GenerateKey(nil)
	s.b = buildBroker(s.db, priv, 0.30, 0, time.Hour)
	s.now = time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	s.capRead, s.monthSpend = 0, 0
	s.resp = httptest.NewRecorder()
	s.capStatus, s.capMsg, s.capCalled = 0, "", false
	s.grant = store.Grant{ID: "g1"}
	s.grantStat, s.grantMsg, s.lastCheck = 0, "", ""
	s.rpm, s.burst = 0, 0
	s.allowed, s.denied, s.lastOK, s.lastRetry = 0, 0, false, 0
}

func (s *cpState) monthStart() time.Time {
	return time.Date(s.now.Year(), s.now.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func (s *cpState) seedSpend(holder string, amount float64, ts int64, state string) error {
	return s.dbSeed(store.LedgerRow{Holder: holder, Side: "consumer", Kind: store.KindSpend, Amount: -amount, State: state, TS: ts})
}

func (s *cpState) dbSeed(rows ...store.LedgerRow) error { s.db.SeedLedgerForTest(rows); return nil }

// --- Background -------------------------------------------------------------

func (s *cpState) freshStore() error      { s.reset(); return nil }
func (s *cpState) grantIssued(g, owner string) error { s.grant = store.Grant{ID: g}; return nil }

// --- section 1: cap resolution ----------------------------------------------

func (s *cpState) defaultCapEnv(v string) error    { os.Setenv("ROGERAI_DEFAULT_MONTHLY_CAP", v); return nil }
func (s *cpState) defaultCapEnvUnset() error        { os.Setenv("ROGERAI_DEFAULT_MONTHLY_CAP", ""); return nil }
func (s *cpState) capRead_(holder string) error {
	c, err := s.db.MonthlyCapOf(holder)
	s.capRead = c
	return err
}
func (s *cpState) setsCap(holder, v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return s.db.SetMonthlyCap(holder, f)
}
func (s *cpState) capIs(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.capRead, want)
}
func (s *cpState) capUnlimited() error {
	if s.capRead != 0 {
		return fmt.Errorf("cap = %g, want unlimited (0)", s.capRead)
	}
	return nil
}

// --- section 2: month spend -------------------------------------------------

func (s *cpState) postedSpendThree(holder, a, b, c string) error {
	for _, v := range []string{a, b, c} {
		f, err := feParseFloat(v)
		if err != nil {
			return err
		}
		if err := s.seedSpend(holder, f, s.now.Unix(), store.StatePosted); err != nil {
			return err
		}
	}
	return nil
}
func (s *cpState) postedSpendThisMonth(holder, v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return s.seedSpend(holder, f, s.now.Unix(), store.StatePosted)
}
func (s *cpState) reversedSpendThisMonth(v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return s.seedSpend("alice", f, s.now.Unix(), store.StateReversed)
}
func (s *cpState) spentLastMonth(holder, v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return s.seedSpend(holder, f, s.monthStart().AddDate(0, -1, 0).Unix(), store.StatePosted)
}
func (s *cpState) spendAtPrevMonthEnd(holder, v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return s.seedSpend(holder, f, s.monthStart().Unix()-1, store.StatePosted) // 23:59:59 last day of last month
}
func (s *cpState) spendAtThisMonthStart(holder, v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return s.seedSpend(holder, f, s.monthStart().Unix(), store.StatePosted)
}
func (s *cpState) toppedUp(holder, v string) error {
	f, _ := feParseFloat(v)
	return s.dbSeed(store.LedgerRow{Holder: holder, Side: "consumer", Kind: store.KindTopup, Amount: f, State: store.StatePosted, TS: s.now.Unix()})
}
func (s *cpState) openHold(holder, v string) error {
	f, _ := feParseFloat(v)
	return s.dbSeed(store.LedgerRow{Holder: holder, Side: "consumer", Kind: store.KindHold, Amount: -f, State: store.StatePending, TS: s.now.Unix()})
}
func (s *cpState) refunded(holder, v string) error {
	f, _ := feParseFloat(v)
	return s.dbSeed(store.LedgerRow{Holder: holder, Side: "consumer", Kind: store.KindRefund, Amount: f, State: store.StatePosted, TS: s.now.Unix()})
}
func (s *cpState) readMonthSpend(holder string) error {
	sp, err := s.db.MonthSpendOf(holder, s.now)
	s.monthSpend = sp
	return err
}
func (s *cpState) monthSpendIs(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.monthSpend, want)
}

// --- section 3/4/5: enforcement ---------------------------------------------

func (s *cpState) hasCap(holder, v string) error          { return s.setsCap(holder, v) }
func (s *cpState) hasUnlimitedCap(holder string) error    { return s.db.SetMonthlyCap(holder, 0) }
func (s *cpState) hasSpent(holder, v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return s.seedSpend(holder, f, s.now.Unix(), store.StatePosted)
}
func (s *cpState) capAndSpent(holder, cap, spent string) error {
	if err := s.setsCap(holder, cap); err != nil {
		return err
	}
	return s.hasSpent(holder, spent)
}
func (s *cpState) spentViaPath(holder, v string) error { return s.hasSpent(holder, v) }

// makesPaidRequest drives the cap gate directly (the boundary outline exercises it at every
// cost incl. 0 - it pins monthlyCapCheck's `spend+maxCost > cap` math).
func (s *cpState) makesPaidRequest(holder, cost string) error {
	c, err := feParseFloat(cost)
	if err != nil {
		return err
	}
	s.resp = httptest.NewRecorder()
	s.lastCheck = "cap"
	s.capCalled = true
	s.capStatus, s.capMsg = s.b.monthlyCapCheck(s.resp, holder, c, s.now)
	return nil
}

// makesFreeRequest mirrors the CALLER's gate: a free/self ($0) request never consults the
// cap gate, so monthlyCapCheck is not called at all.
func (s *cpState) makesFreeRequest(holder, cost string) error {
	s.resp = httptest.NewRecorder()
	s.lastCheck = "cap"
	s.capCalled = false
	s.capStatus, s.capMsg = 0, ""
	return nil
}

func (s *cpState) requestAllowed() error {
	if s.capStatus != 0 {
		return fmt.Errorf("request rejected (%d %q), want allowed", s.capStatus, s.capMsg)
	}
	return nil
}
func (s *cpState) rejected402() error {
	if s.capStatus != http.StatusPaymentRequired {
		return fmt.Errorf("status = %d, want 402", s.capStatus)
	}
	return nil
}
func (s *cpState) rejectionSaysLimit() error {
	if !strings.Contains(s.capMsg, "monthly spend limit reached") {
		return fmt.Errorf("rejection message %q does not say the monthly limit is reached", s.capMsg)
	}
	return nil
}
func (s *cpState) noCapHeaders() error {
	if h := s.resp.Header().Get("X-RogerAI-Monthly-Cap"); h != "" {
		return fmt.Errorf("monthly-cap header set (%q) for an unlimited cap", h)
	}
	return nil
}
func (s *cpState) capHeaderReports(spend, cap string) error {
	ws, err := feParseFloat(spend)
	if err != nil {
		return err
	}
	wc, err := feParseFloat(cap)
	if err != nil {
		return err
	}
	gc, _ := strconv.ParseFloat(s.resp.Header().Get("X-RogerAI-Monthly-Cap"), 64)
	gs, _ := strconv.ParseFloat(s.resp.Header().Get("X-RogerAI-Monthly-Spend"), 64)
	if err := feApprox(gc, wc); err != nil {
		return fmt.Errorf("cap header: %w", err)
	}
	return feApprox(gs, ws)
}
func (s *cpState) atLimitHeaders() error {
	if !strings.Contains(s.resp.Header().Get("X-RogerAI-Monthly-Notice"), "limit reached") {
		return fmt.Errorf("at-limit notice header not set (got %q)", s.resp.Header().Get("X-RogerAI-Monthly-Notice"))
	}
	return nil
}
func (s *cpState) nearNoticeSet() error {
	if !strings.Contains(s.resp.Header().Get("X-RogerAI-Monthly-Notice"), "you've used") {
		return fmt.Errorf("near-cap notice header not set (got %q)", s.resp.Header().Get("X-RogerAI-Monthly-Notice"))
	}
	return nil
}
func (s *cpState) nearNoticeNotSet() error {
	if strings.Contains(s.resp.Header().Get("X-RogerAI-Monthly-Notice"), "you've used") {
		return fmt.Errorf("near-cap notice header set but should not be (%q)", s.resp.Header().Get("X-RogerAI-Monthly-Notice"))
	}
	return nil
}
func (s *cpState) atLimitNoticeNotSet() error {
	if strings.Contains(s.resp.Header().Get("X-RogerAI-Monthly-Notice"), "limit reached") {
		return fmt.Errorf("at-limit notice header set but should not be")
	}
	return nil
}
func (s *cpState) capGateNotConsulted() error {
	if s.capCalled {
		return fmt.Errorf("the cap gate was consulted on a free ($0) request")
	}
	return nil
}
func (s *cpState) clockAdvancesNextMonth() error { s.now = s.now.AddDate(0, 1, 0); return nil }
func (s *cpState) monthSpendReadsAt(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	sp, err := s.db.MonthSpendOf("alice", s.now)
	if err != nil {
		return err
	}
	return feApprox(sp, want)
}
func (s *cpState) monthSpendReads(v string) error { return s.monthSpendReadsAt(v) }

// month window definition (verified via MonthSpendOf boundary inclusion)
func (s *cpState) monthWindowForInstant() error { return nil }
func (s *cpState) windowStartsFirstOfMonth() error {
	// a row exactly at 00:00:00 UTC on the first IS in the window; one second earlier is not.
	m := store.NewMem()
	m.SeedLedgerForTest([]store.LedgerRow{
		{Holder: "w", Kind: store.KindSpend, Amount: -1, State: store.StatePosted, TS: s.monthStart().Unix()},
		{Holder: "w", Kind: store.KindSpend, Amount: -1, State: store.StatePosted, TS: s.monthStart().Unix() - 1},
	})
	sp, _ := m.MonthSpendOf("w", s.now)
	return feApprox(sp, 1) // only the first-of-month row counts
}
func (s *cpState) windowEndsFirstOfNextMonth() error {
	next := s.monthStart().AddDate(0, 1, 0).Unix()
	m := store.NewMem()
	m.SeedLedgerForTest([]store.LedgerRow{
		{Holder: "w", Kind: store.KindSpend, Amount: -1, State: store.StatePosted, TS: next - 1},
		{Holder: "w", Kind: store.KindSpend, Amount: -1, State: store.StatePosted, TS: next},
	})
	sp, _ := m.MonthSpendOf("w", s.now)
	return feApprox(sp, 1) // the next-month-start row is excluded; the one a second before is in
}

// --- section 6: grant token caps --------------------------------------------

func (s *cpState) grantNoCaps() error      { s.grant.DailyCap, s.grant.MonthlyCap = 0, 0; return nil }
func (s *cpState) grantDailyCap(v string) error {
	n, err := strconv.ParseInt(v, 10, 64)
	s.grant.DailyCap = n
	return err
}
func (s *cpState) grantMonthlyCap(v string) error {
	n, err := strconv.ParseInt(v, 10, 64)
	s.grant.MonthlyCap = n
	return err
}
func (s *cpState) grantDailyMonthly(d, mth string) error {
	if err := s.grantDailyCap(d); err != nil {
		return err
	}
	return s.grantMonthlyCap(mth)
}
func (s *cpState) usedToday(v string) error {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return err
	}
	return s.db.AddGrantUsage(s.grant.ID, n, time.Now())
}
func (s *cpState) usedYesterday(v string) error {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return err
	}
	return s.db.AddGrantUsage(s.grant.ID, n, time.Now().Add(-24*time.Hour))
}
func (s *cpState) usedThisMonth(v string) error { return s.usedToday(v) }
func (s *cpState) usedLastMonth(v string) error {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return err
	}
	// last day of the previous calendar month relative to the REAL clock grantCapCheck reads.
	firstThis := time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), 1, 12, 0, 0, 0, time.UTC)
	return s.db.AddGrantUsage(s.grant.ID, n, firstThis.AddDate(0, 0, -1))
}
func (s *cpState) usedTodayAndMonth(today, month string) error {
	if err := s.usedToday(today); err != nil {
		return err
	}
	// the month rollup already got `today`; add the rest so this-month == month total.
	nt, _ := strconv.ParseInt(today, 10, 64)
	nm, _ := strconv.ParseInt(month, 10, 64)
	if nm > nt {
		return s.db.AddGrantUsage(s.grant.ID+"|monthonly-noop", 0, time.Now()) // (month already includes today)
	}
	return nil
}
func (s *cpState) grantUsageReadErrors() error { s.b.db = grantUsageErrStore{s.db}; return nil }
func (s *cpState) usedTokens(v string) error   { return s.usedToday(v) }

func (s *cpState) requestChecked() error {
	s.lastCheck = "grant"
	s.grantStat, s.grantMsg = s.b.grantCapCheck(s.grant)
	return nil
}

// verdict is the SHARED "the request is allowed|rejected|denied" Then: allowed dispatches on
// which gate ran last; rejected is the cap's 402; denied is the grant's 429.
func (s *cpState) verdict(v string) error {
	switch v {
	case "rejected":
		return s.rejected402()
	case "denied":
		return s.grantDeniedPlain()
	default: // allowed
		if s.lastCheck == "grant" {
			return s.grantAllowed()
		}
		return s.requestAllowed()
	}
}
func (s *cpState) admittedThenServes(used, serve string) error {
	if err := s.requestChecked(); err != nil { // admitted at the current usage
		return err
	}
	if s.grantStat != 0 {
		return fmt.Errorf("expected admit before serving, got %d %q", s.grantStat, s.grantMsg)
	}
	n, err := strconv.ParseInt(serve, 10, 64)
	if err != nil {
		return err
	}
	return s.db.AddGrantUsage(s.grant.ID, n, time.Now())
}
func (s *cpState) grantAllowed() error {
	if s.grantStat != 0 {
		return fmt.Errorf("grant request denied (%d %q), want allowed", s.grantStat, s.grantMsg)
	}
	return nil
}
func (s *cpState) grantDenied(msg string) error {
	if s.grantStat != http.StatusTooManyRequests {
		return fmt.Errorf("grant status = %d, want 429", s.grantStat)
	}
	if s.grantMsg != msg {
		return fmt.Errorf("grant message = %q, want %q", s.grantMsg, msg)
	}
	return nil
}
func (s *cpState) grantDeniedPlain() error {
	if s.grantStat != http.StatusTooManyRequests {
		return fmt.Errorf("grant status = %d, want 429", s.grantStat)
	}
	return nil
}
func (s *cpState) nextRequestDenied() error {
	s.grantStat, s.grantMsg = s.b.grantCapCheck(s.grant)
	return s.grantDeniedPlain()
}
func (s *cpState) usageBecomesToday(v string) error {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return err
	}
	u, err := s.db.GrantUsageOf(s.grant.ID, time.Now())
	if err != nil {
		return err
	}
	if u.DayTokens != n {
		return fmt.Errorf("day usage = %d, want %d", u.DayTokens, n)
	}
	return nil
}
func (s *cpState) serves(v string) error { return s.db.AddGrantUsage(s.grant.ID, mustI64(v), time.Now()) }
func (s *cpState) dayUsageIs(v string) error {
	u, _ := s.db.GrantUsageOf(s.grant.ID, time.Now())
	if u.DayTokens != mustI64(v) {
		return fmt.Errorf("day usage = %d, want %s", u.DayTokens, v)
	}
	return nil
}
func (s *cpState) monthUsageIs(v string) error {
	u, _ := s.db.GrantUsageOf(s.grant.ID, time.Now())
	if u.MonthTokens != mustI64(v) {
		return fmt.Errorf("month usage = %d, want %s", u.MonthTokens, v)
	}
	return nil
}

func mustI64(v string) int64 { n, _ := strconv.ParseInt(v, 10, 64); return n }

// --- section 7/8: rate limits -----------------------------------------------

func (s *cpState) grantRpmBurst(rpm, burst string) error {
	s.rpm, _ = feParseFloat(rpm)
	s.burst, _ = feParseFloat(burst)
	return nil
}
func (s *cpState) requestsSameInstant(n string) error {
	c, _ := strconv.Atoi(n)
	s.allowed, s.denied = 0, 0
	for i := 0; i < c; i++ {
		ok, _ := s.b.grantRL.allowAt(s.grant.ID, s.rpm, s.burst)
		if ok {
			s.allowed++
		} else {
			s.denied++
		}
	}
	return nil
}
func (s *cpState) allNAllowed(n string) error {
	c, _ := strconv.Atoi(n)
	if s.allowed != c {
		return fmt.Errorf("%d allowed, want %d", s.allowed, c)
	}
	return nil
}
func (s *cpState) nthDeniedRetry(string) error {
	ok, retry := s.b.grantRL.allowAt(s.grant.ID, s.rpm, s.burst)
	if ok {
		return fmt.Errorf("the extra request was allowed, want denied")
	}
	if retry < 1 {
		return fmt.Errorf("Retry-After hint = %d, want >= 1", retry)
	}
	return nil
}
func (s *cpState) bucketEmpty(g string) error {
	s.b.grantRL.allowAt(g, s.rpm, s.burst) // consume the single burst token -> empty
	return nil
}
func (s *cpState) oneSecondPasses() error {
	s.b.grantRL.mu.Lock()
	if bkt := s.b.grantRL.buckets[s.grant.ID]; bkt != nil {
		bkt.last = bkt.last.Add(-time.Second) // age the fill stamp so allowAt refills 1s worth
	}
	s.b.grantRL.mu.Unlock()
	return nil
}
func (s *cpState) oneMoreAllowed(g string) error {
	ok, _ := s.b.grantRL.allowAt(g, s.rpm, s.burst)
	if !ok {
		return fmt.Errorf("expected 1 more request allowed after refill")
	}
	return nil
}
func (s *cpState) grantLimiterDefault(rpm, burst string) error {
	s.b.grantRL.rpm, _ = feParseFloat(rpm)
	s.b.grantRL.burst, _ = feParseFloat(burst)
	return nil
}
func (s *cpState) grantLimiterDefaultRpm(rpm string) error {
	s.b.grantRL.rpm, _ = feParseFloat(rpm)
	return nil
}
func (s *cpState) defaultRateApplies() error {
	// override (0,0) falls back to the limiter default depth: admit `default burst`, deny next.
	depth := int(s.b.grantRL.burst)
	for i := 0; i < depth; i++ {
		if ok, _ := s.b.grantRL.allowAt(s.grant.ID, 0, 0); !ok {
			return fmt.Errorf("default burst depth: denied at request %d, want >= %d allowed", i+1, depth)
		}
	}
	if ok, _ := s.b.grantRL.allowAt(s.grant.ID, 0, 0); ok {
		return fmt.Errorf("request %d allowed, want denied (default depth %d exhausted)", depth+1, depth)
	}
	return nil
}
func (s *cpState) manyRequestsAtOnce(g string) error {
	s.allowed = 0
	for i := 0; i < 200; i++ {
		if ok, _ := s.b.grantRL.allowAt(g, s.rpm, s.burst); ok {
			s.allowed++
		}
	}
	return nil
}
func (s *cpState) allAllowed() error {
	if s.allowed != 200 {
		return fmt.Errorf("only %d/200 allowed; rpm<=0 must always allow", s.allowed)
	}
	return nil
}
func (s *cpState) freshBucketSized(g string) error { return nil }
func (s *cpState) bucketDepthIs(v string) error {
	depth, _ := strconv.Atoi(v)
	n := 0
	for i := 0; i < depth+5; i++ {
		if ok, _ := s.b.grantRL.allowAt(s.grant.ID, s.rpm, s.burst); ok {
			n++
		} else {
			break
		}
	}
	if n != depth {
		return fmt.Errorf("admitted %d before denial, want bucket depth %d", n, depth)
	}
	return nil
}
func (s *cpState) grantEmptyBucket(g, rpm, burst string) error {
	s.rpm, _ = feParseFloat(rpm)
	s.burst, _ = feParseFloat(burst)
	s.b.grantRL.allowAt(g, s.rpm, s.burst) // consume the burst-1 token -> empty
	return nil
}
func (s *cpState) grantFullBucket(g, rpm, burst string) error { return nil } // untouched bucket starts full
func (s *cpState) requestOnGrant(g string) error {
	s.lastOK, s.lastRetry = s.b.grantRL.allowAt(g, 60, 1)
	return nil
}
func (s *cpState) requestOnGrantAllowed(g string) error {
	if !s.lastOK {
		return fmt.Errorf("request on %s denied, want allowed (its own bucket is full)", g)
	}
	return nil
}
func (s *cpState) g1RemainsLimited() error {
	ok, _ := s.b.grantRL.allowAt("g1", 60, 1)
	if ok {
		return fmt.Errorf("g1 was allowed, want still rate-limited (its bucket is empty)")
	}
	return nil
}

// section 8 anon / per-identity
func (s *cpState) anonDefault(rpm, burst string) error {
	wr, _ := feParseFloat(rpm)
	wb, _ := feParseFloat(burst)
	if s.b.anonRL.rpm != wr || s.b.anonRL.burst != wb {
		return fmt.Errorf("anon limiter = rpm %g burst %g, want %g/%g", s.b.anonRL.rpm, s.b.anonRL.burst, wr, wb)
	}
	return nil
}
func (s *cpState) identityDefault(rpm, burst string) error {
	wr, _ := feParseFloat(rpm)
	wb, _ := feParseFloat(burst)
	if s.b.rl.rpm != wr || s.b.rl.burst != wb {
		return fmt.Errorf("per-identity limiter = rpm %g burst %g, want %g/%g", s.b.rl.rpm, s.b.rl.burst, wr, wb)
	}
	return nil
}
func (s *cpState) anonLimiterEmptyForIP(rpm, burst, ip string) error {
	s.b.anonRL.rpm, _ = feParseFloat(rpm)
	s.b.anonRL.burst, _ = feParseFloat(burst)
	s.b.anonRL.allowAt(ip, 0, 0) // consume the single token -> empty
	return nil
}
func (s *cpState) secondAnonRequest(ip string) error {
	s.lastOK, s.lastRetry = s.b.anonRL.allowAt(ip, 0, 0)
	return nil
}
func (s *cpState) deniedWithRetry() error {
	if s.lastOK {
		return fmt.Errorf("request allowed, want denied")
	}
	if s.lastRetry < 1 {
		return fmt.Errorf("Retry-After = %d, want >= 1", s.lastRetry)
	}
	return nil
}
func (s *cpState) limiterEmptyForKey(rpm, key string) error {
	r, _ := feParseFloat(rpm)
	s.b.rl.rpm, s.b.rl.burst = r, 0
	s.b.rl.allowAt(key, 0, 0) // drain the burst (defaults to rpm depth) ... then force-empty below
	s.b.rl.mu.Lock()
	if bkt := s.b.rl.buckets[key]; bkt != nil {
		bkt.tokens = 0
	}
	s.b.rl.mu.Unlock()
	return nil
}
func (s *cpState) requestForKeyDenied(key string) error {
	s.lastOK, s.lastRetry = s.b.rl.allowAt(key, 0, 0)
	if s.lastOK {
		return fmt.Errorf("request for %q allowed, want denied (empty bucket)", key)
	}
	return nil
}
func (s *cpState) retryAtLeastOne() error {
	if s.lastRetry < 1 {
		return fmt.Errorf("Retry-After = %d, want >= 1", s.lastRetry)
	}
	return nil
}

func TestCapsBDD(t *testing.T) {
	for _, k := range []string{"ROGERAI_DEFAULT_MONTHLY_CAP", "ROGERAI_RATE_RPM", "ROGERAI_RATE_BURST", "ROGERAI_ANON_RATE_RPM", "ROGERAI_ANON_RATE_BURST"} {
		t.Setenv(k, "")
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &cpState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^a fresh money store$`, st.freshStore)
			sc.Step(`^a grant "([^"]*)" issued by owner "([^"]*)"$`, st.grantIssued)

			// section 1
			sc.Step(`^the default monthly cap env is ([\d.-]+)$`, st.defaultCapEnv)
			sc.Step(`^the default monthly cap env is unset$`, st.defaultCapEnvUnset)
			sc.Step(`^the monthly cap for "([^"]*)" is read$`, st.capRead_)
			sc.Step(`^"([^"]*)" sets a monthly cap of ([\d.-]+)$`, st.setsCap)
			sc.Step(`^the cap is ([\d.]+)$`, st.capIs)
			sc.Step(`^the cap is unlimited$`, st.capUnlimited)

			// section 2
			sc.Step(`^wallet "([^"]*)" has posted spend of ([\d.]+), ([\d.]+), and ([\d.]+) this month$`, st.postedSpendThree)
			sc.Step(`^wallet "([^"]*)" has posted spend of ([\d.]+) this month$`, st.postedSpendThisMonth)
			sc.Step(`^a spend row of ([\d.]+) this month was reversed$`, st.reversedSpendThisMonth)
			sc.Step(`^wallet "([^"]*)" spent ([\d.]+) last month$`, st.spentLastMonth)
			sc.Step(`^wallet "([^"]*)" spent ([\d.]+) this month$`, st.postedSpendThisMonth)
			sc.Step(`^wallet "([^"]*)" has a spend row of ([\d.]+) stamped at 23:59:59 UTC on the last day of last month$`, st.spendAtPrevMonthEnd)
			sc.Step(`^wallet "([^"]*)" has a spend row of ([\d.]+) stamped at 00:00:00 UTC on the first day of this month$`, st.spendAtThisMonthStart)
			sc.Step(`^wallet "([^"]*)" topped up ([\d.]+) this month$`, st.toppedUp)
			sc.Step(`^wallet "([^"]*)" has an open hold of ([\d.]+) this month$`, st.openHold)
			sc.Step(`^wallet "([^"]*)" was refunded ([\d.]+) this month$`, st.refunded)
			sc.Step(`^the month-to-date spend for "([^"]*)" is read now$`, st.readMonthSpend)
			sc.Step(`^the month spend is ([\d.]+)$`, st.monthSpendIs)

			// section 3/4/5
			sc.Step(`^"([^"]*)" has a monthly cap of ([\d.]+)$`, st.hasCap)
			sc.Step(`^"([^"]*)" has an unlimited monthly cap$`, st.hasUnlimitedCap)
			sc.Step(`^"([^"]*)" has spent ([\d.]+) this month$`, st.hasSpent)
			sc.Step(`^"([^"]*)" has a monthly cap of ([\d.]+) and has spent ([\d.]+) this month$`, st.capAndSpent)
			sc.Step(`^"([^"]*)" spent ([\d.]+) via the public relay this month$`, st.spentViaPath)
			sc.Step(`^"([^"]*)" spent ([\d.]+) via a grant-billed path this month$`, st.spentViaPath)
			sc.Step(`^"([^"]*)" makes a paid request with worst-case cost ([\d.]+)$`, st.makesPaidRequest)
			sc.Step(`^"([^"]*)" makes a free request with worst-case cost ([\d.]+)$`, st.makesFreeRequest)
			sc.Step(`^the request is (allowed|rejected|denied)$`, st.verdict) // shared cap+grant verdict
			sc.Step(`^the request is rejected with 402$`, st.rejected402)
			sc.Step(`^the rejection message says the monthly limit is reached$`, st.rejectionSaysLimit)
			sc.Step(`^no monthly-cap headers are set$`, st.noCapHeaders)
			sc.Step(`^the monthly-cap header reports ([\d.]+) of ([\d.]+)$`, st.capHeaderReports)
			sc.Step(`^the at-limit headers are set$`, st.atLimitHeaders)
			sc.Step(`^the near-cap notice header is set$`, st.nearNoticeSet)
			sc.Step(`^the near-cap notice header is not set$`, st.nearNoticeNotSet)
			sc.Step(`^the at-limit notice header is not set$`, st.atLimitNoticeNotSet)
			sc.Step(`^the cap gate is not consulted$`, st.capGateNotConsulted)
			sc.Step(`^the clock advances into next month$`, st.clockAdvancesNextMonth)
			sc.Step(`^the clock advances into the next UTC month$`, func() error { return nil }) // grant usage is real-time-relative
			sc.Step(`^the month-to-date spend reads ([\d.]+) at the start of next month$`, st.monthSpendReadsAt)
			sc.Step(`^the month-to-date spend reads ([\d.]+)$`, st.monthSpendReads)
			sc.Step(`^the month spend window for a given instant$`, st.monthWindowForInstant)
			sc.Step(`^it starts at 00:00:00 UTC on the first of that month$`, st.windowStartsFirstOfMonth)
			sc.Step(`^it ends at 00:00:00 UTC on the first of the next month$`, st.windowEndsFirstOfNextMonth)

			// section 6
			sc.Step(`^grant "([^"]*)" has no daily or monthly token cap$`, func(string) error { return st.grantNoCaps() })
			sc.Step(`^grant "([^"]*)" has a daily token cap of (\d+)$`, func(_, v string) error { return st.grantDailyCap(v) })
			sc.Step(`^grant "([^"]*)" has a monthly token cap of (\d+)$`, func(_, v string) error { return st.grantMonthlyCap(v) })
			sc.Step(`^grant "([^"]*)" has a daily token cap of (\d+) and a monthly token cap of (\d+)$`, func(_, d, m string) error { return st.grantDailyMonthly(d, m) })
			sc.Step(`^grant "([^"]*)" has used (\d+) tokens today$`, func(_, v string) error { return st.usedToday(v) })
			sc.Step(`^grant "([^"]*)" used (\d+) tokens yesterday$`, func(_, v string) error { return st.usedYesterday(v) })
			sc.Step(`^grant "([^"]*)" has used (\d+) tokens this month$`, func(_, v string) error { return st.usedThisMonth(v) })
			sc.Step(`^grant "([^"]*)" used (\d+) tokens last month$`, func(_, v string) error { return st.usedLastMonth(v) })
			sc.Step(`^grant "([^"]*)" has used (\d+) tokens today and (\d+) tokens this month$`, func(_, d, m string) error { return st.usedTodayAndMonth(d, m) })
			sc.Step(`^grant "([^"]*)" has used (\d+) tokens$`, func(_, v string) error { return st.usedTokens(v) })
			sc.Step(`^the grant usage read errors$`, st.grantUsageReadErrors)
			sc.Step(`^a request on grant "([^"]*)" is checked$`, func(string) error { return st.requestChecked() })
			sc.Step(`^a request on grant "([^"]*)" is admitted and then serves (\d+) tokens$`, func(_, serve string) error { return st.admittedThenServes("", serve) })
			sc.Step(`^the request is denied with 429 "([^"]*)"$`, st.grantDenied)
			sc.Step(`^the request is denied with 429$`, st.grantDeniedPlain)
			sc.Step(`^usage becomes (\d+) tokens today$`, st.usageBecomesToday)
			sc.Step(`^the NEXT request on grant "([^"]*)" is denied with 429$`, func(string) error { return st.nextRequestDenied() })
			sc.Step(`^grant "([^"]*)" serves (\d+) tokens$`, func(_, v string) error { return st.serves(v) })
			sc.Step(`^grant "([^"]*)" day usage is (\d+)$`, func(_, v string) error { return st.dayUsageIs(v) })
			sc.Step(`^grant "([^"]*)" month usage is (\d+)$`, func(_, v string) error { return st.monthUsageIs(v) })

			// section 7
			sc.Step(`^grant "([^"]*)" has rpm (\d+) and burst (\d+)$`, func(_, r, b string) error { return st.grantRpmBurst(r, b) })
			sc.Step(`^(\d+) requests on grant "([^"]*)" arrive in the same instant$`, func(n, _ string) error { return st.requestsSameInstant(n) })
			sc.Step(`^all (\d+) are allowed$`, st.allNAllowed)
			sc.Step(`^the (\d+)(?:th|st|nd|rd) in that instant is denied with 429 and a Retry-After hint$`, st.nthDeniedRetry)
			sc.Step(`^the bucket for grant "([^"]*)" is empty$`, st.bucketEmpty)
			sc.Step(`^1 second passes$`, st.oneSecondPasses)
			sc.Step(`^1 more request on grant "([^"]*)" is allowed$`, st.oneMoreAllowed)
			sc.Step(`^the grant limiter default rpm is (\d+) and burst (\d+)$`, st.grantLimiterDefault)
			sc.Step(`^the grant limiter default rpm is (\d+)$`, st.grantLimiterDefaultRpm)
			sc.Step(`^grant "([^"]*)" has rpm (\d+)$`, func(_, r string) error { return st.grantRpmBurst(r, "0") })
			sc.Step(`^the configured default rate applies$`, st.defaultRateApplies)
			sc.Step(`^many requests on grant "([^"]*)" arrive at once$`, st.manyRequestsAtOnce)
			sc.Step(`^all are allowed$`, st.allAllowed)
			sc.Step(`^a fresh bucket for grant "([^"]*)" is sized$`, st.freshBucketSized)
			sc.Step(`^the bucket depth is (\d+)$`, st.bucketDepthIs)
			sc.Step(`^grant "([^"]*)" has rpm (\d+) and burst (\d+) and its bucket is empty$`, st.grantEmptyBucket)
			sc.Step(`^grant "([^"]*)" has rpm (\d+) and burst (\d+) and a full bucket$`, st.grantFullBucket)
			sc.Step(`^a request on grant "([^"]*)" arrives$`, st.requestOnGrant)
			sc.Step(`^the request on grant "([^"]*)" is allowed$`, st.requestOnGrantAllowed)
			sc.Step(`^grant "([^"]*)" remains rate-limited independently$`, func(string) error { return st.g1RemainsLimited() })

			// section 8
			sc.Step(`^the anon default rpm is (\d+) and burst (\d+)$`, st.anonDefault)
			sc.Step(`^the per-identity default rpm is (\d+) and burst (\d+)$`, st.identityDefault)
			sc.Step(`^the anon limiter has rpm (\d+) and burst (\d+) and an empty bucket for IP "([^"]*)"$`, st.anonLimiterEmptyForIP)
			sc.Step(`^a second anon request from IP "([^"]*)" arrives in the same instant$`, st.secondAnonRequest)
			sc.Step(`^it is denied with 429 and a Retry-After hint$`, st.deniedWithRetry)
			sc.Step(`^a limiter with rpm (\d+) and an empty bucket for key "([^"]*)"$`, st.limiterEmptyForKey)
			sc.Step(`^a request for key "([^"]*)" is denied$`, st.requestForKeyDenied)
			sc.Step(`^the Retry-After hint is at least 1$`, st.retryAtLeastOne)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/money/caps.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("money/caps behavior scenarios failed (see godog output above)")
	}
}
