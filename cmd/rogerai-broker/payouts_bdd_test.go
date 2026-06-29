package main

// payouts_bdd_test.go makes features/money/payouts.feature an EXECUTABLE Cucumber suite,
// driving the REAL operator money-out rail end to end:
//   - the 120-day hold + sweep-on-read promotion (store.promoteLocked via EarningSplitOf),
//     incl. the node + owner-account recount holds (SetNodeRecountHold / SetAccountRecountHold),
//   - store.RequestPayout (minimum gate, payable-only, exact debit) / SettlePayout / FailPayout,
//   - the reserve coupling (reserve releases WITH the lot, never stranded),
//   - the broker POST /payouts/request flow (KYC gate, debit->transfer->settle/rollback ordering,
//     concurrency single-flight) with an injected conn.transfer + a failStore for the settle-fail
//     path, and the dev-stub / ROGERAI_REQUIRE_LIVE fail-closed transfer guards,
//   - payoutTransfer cents conversion + refreshConnectStatus status mapping (via a Stripe httptest
//     stub on stripeAPIBase).
//
// Lots with precise release/reserve/state are staged via store.SeedLotsForTest (the documented
// test seam); "earned just now" uses the real Settle path so the live 120-day policy is pinned.
// Assertions read STORE state back (EarningSplit, PayoutsOf, PayoutLots, the KindPayout ledger
// row + its reversed state/transfer ref) so a regression in any payout invariant fails red.
// feApprox/feParseFloat live in fee_splits_bdd_test.go (same package).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

const poDay = int64(24 * 3600)

type poState struct {
	db        *store.Mem
	now       time.Time
	op        string
	minPayout float64
	lotID     int64
	lots      []store.EarningLot
	payerFund bool

	split        store.EarningSplit
	payout       store.Payout
	payoutOK     bool
	payoutReason string
	payout2      store.Payout

	// broker
	bk            *broker
	ownerBound    map[string]bool
	connectID     string
	resp          *httptest.ResponseRecorder
	respBody      string
	failSettle    bool
	requireLive   bool
	loadedConn    connect
	stripeSrv     *httptest.Server
	origBase      string
	statusOut     string
	devTransferID string
	concCodes     []int

	// transfer stub captures (guarded by mu for the concurrent scenario)
	mu            sync.Mutex
	transferErr   bool
	transferID    string
	transferCalls int
	gotCents      int64
	gotIdem       string
	totalCents    int64
}

func (s *poState) reset() {
	os.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	os.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "120")
	os.Setenv("ROGERAI_PAYOUT_MIN", "25")
	os.Unsetenv("ROGERAI_REQUIRE_LIVE")
	os.Setenv("STRIPE_SECRET_KEY", "")
	if s.stripeSrv != nil {
		s.stripeSrv.Close()
		s.stripeSrv = nil
	}
	stripeAPIBase = s.origBase
	s.db = store.NewMem()
	s.now = time.Now()
	s.op = "op1"
	s.minPayout = 25
	s.lotID = 0
	s.lots = nil
	s.payerFund = false
	s.split = store.EarningSplit{}
	s.payout = store.Payout{}
	s.payoutOK = false
	s.payoutReason = ""
	s.payout2 = store.Payout{}
	s.bk = nil
	s.ownerBound = map[string]bool{}
	s.connectID = ""
	s.resp = nil
	s.respBody = ""
	s.failSettle = false
	s.requireLive = false
	s.statusOut = ""
	s.devTransferID = ""
	s.concCodes = nil
	s.transferErr = false
	s.transferID = ""
	s.transferCalls = 0
	s.gotCents = 0
	s.gotIdem = ""
	s.totalCents = 0
}

// --- shared setup -----------------------------------------------------------

func (s *poState) freshStore() error { s.reset(); return nil }
func (s *poState) feePct(string) error { return nil }

func (s *poState) policyBackground(_, _, minimum string) error {
	m, err := feParseFloat(minimum)
	if err != nil {
		return err
	}
	s.minPayout = m
	return nil
}

func (s *poState) nodeOwned(node, owner string) error {
	s.op = owner
	return s.db.BindNode(node, owner)
}

func (s *poState) ensureOwner(op string) {
	s.op = op
	if s.ownerBound[op] {
		return
	}
	s.ownerBound[op] = true
	_ = s.db.BindOwner(store.Owner{GitHubID: 1, Login: op, Pubkey: op})
	_ = s.db.BindNode("n1", op)
}

func (s *poState) applyLots() { s.db.SeedLotsForTest(s.lots) }

func (s *poState) stage(op, node string, gross, reserve float64, state string, releaseAt, reserveReleaseAt, createdAt int64) {
	s.lotID++
	s.lots = append(s.lots, store.EarningLot{
		ID: s.lotID, Node: node, AccountID: op, RequestID: fmt.Sprintf("r%d", s.lotID),
		Gross: gross, Reserve: reserve, State: state,
		ReleaseAt: releaseAt, ReserveReleaseAt: reserveReleaseAt, CreatedAt: createdAt,
	})
	s.applyLots()
}

func (s *poState) ensurePayer() {
	if !s.payerFund {
		s.payerFund = true
		_, _ = s.db.AddCredits("_payer", 1_000_000)
	}
}

// settleNow earns op a real lot via the live Settle path (lot release = now + policy hold).
func (s *poState) settleNow(op, node string, gross float64) error {
	s.ensurePayer()
	if err := s.db.BindNode(node, op); err != nil {
		return err
	}
	rec := protocol.UsageReceipt{RequestID: fmt.Sprintf("rs%d", len(s.lots)+1), TS: s.now.Unix()}
	_, err := s.db.Settle("_payer", node, gross, gross, rec)
	return err
}

// --- section 1: hold window / promotion -------------------------------------

func (s *poState) earnedJustNow(op, gross, node string) error {
	g, err := feParseFloat(gross)
	if err != nil {
		return err
	}
	s.op = op
	return s.settleNow(op, node, g)
}

func (s *poState) earnsLotOwnerShare(op, gross string) error {
	g, err := feParseFloat(gross)
	if err != nil {
		return err
	}
	s.op = op
	return s.settleNow(op, "n1", g)
}

func (s *poState) heldLot(op, gross string) error {
	g, err := feParseFloat(gross)
	if err != nil {
		return err
	}
	s.op = op
	s.stage(op, "n1", g, 0, store.LotHeld, s.now.Unix()+200*poDay, s.now.Unix()+200*poDay, s.now.Unix())
	return nil
}

func (s *poState) heldLotAgeNode(op, gross, age, node string) error {
	g, err := feParseFloat(gross)
	if err != nil {
		return err
	}
	a, err := strconv.Atoi(age)
	if err != nil {
		return err
	}
	s.op = op
	rel := s.now.Unix() + (120-int64(a))*poDay
	s.stage(op, node, g, 0, store.LotHeld, rel, rel, s.now.Unix()-int64(a)*poDay)
	return nil
}

func (s *poState) heldLotAge(op, gross, age string) error {
	return s.heldLotAgeNode(op, gross, age, "n1")
}

func (s *poState) heldLotReleasingIn(op, gross, secs string) error {
	g, err := feParseFloat(gross)
	if err != nil {
		return err
	}
	sec, err := strconv.Atoi(secs)
	if err != nil {
		return err
	}
	s.op = op
	rel := s.now.Unix() + int64(sec)
	s.stage(op, "n1", g, 0, store.LotHeld, rel, rel, s.now.Unix())
	return nil
}

func (s *poState) heldLotReleaseExactlyNow(op, gross string) error {
	g, err := feParseFloat(gross)
	if err != nil {
		return err
	}
	s.op = op
	s.stage(op, "n1", g, 0, store.LotHeld, s.now.Unix(), s.now.Unix(), s.now.Unix()-120*poDay)
	return nil
}

func (s *poState) nodeRecountHold(node string) error  { return s.db.SetNodeRecountHold(node, true) }
func (s *poState) acctRecountHold(acct string) error  { return s.db.SetAccountRecountHold(acct, true) }
func (s *poState) clearNodeHold(node string) error    { return s.db.SetNodeRecountHold(node, false) }

func (s *poState) readSplitNow(op string) error {
	s.op = op
	sp, err := s.db.EarningSplitOf(op, s.now)
	if err != nil {
		return err
	}
	s.split = sp
	return nil
}

func (s *poState) freshSplit() store.EarningSplit {
	sp, _ := s.db.EarningSplitOf(s.op, s.now)
	s.split = sp
	return sp
}

func (s *poState) heldBalanceIs(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.freshSplit().Held, want)
}

func (s *poState) payableBalanceIs(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.freshSplit().Payable, want)
}

func (s *poState) reservedBalanceIs(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.freshSplit().Reserved, want)
}

func (s *poState) nextReleaseDays(d string) error {
	days, err := strconv.Atoi(d)
	if err != nil {
		return err
	}
	got := s.freshSplit().NextRelease - s.now.Unix()
	want := int64(days) * poDay
	if got < want-3600 || got > want+3600 { // ±1h tolerance around the live Settle clock
		return fmt.Errorf("next release in %ds, want ~%ds (%d days)", got, want, days)
	}
	return nil
}

func (s *poState) lotReserveIs(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.freshSplit().Reserved, want) // a freshly earned held lot's reserve shows as Reserved
}

func (s *poState) reserveReleaseCoupled() error {
	// Reading PAST the release shows the reserve fully payable (not stranded on a later
	// tail) — i.e. reserve_release_at is coupled to release_at.
	sp, err := s.db.EarningSplitOf(s.op, s.now.Add(200*24*time.Hour))
	if err != nil {
		return err
	}
	if sp.Reserved != 0 {
		return fmt.Errorf("reserve still %g after release (reserve_release_at not coupled to release_at)", sp.Reserved)
	}
	return feApprox(sp.Payable, 100)
}

// policyReserveIs recreates the store under a configured reserve fraction (LoadPayoutPolicy
// reads the env at NewMem); it runs right after the Background, before any lot is staged.
func (s *poState) policyReserveIs(v string) error {
	os.Setenv("ROGERAI_PAYOUT_RESERVE", v)
	s.db = store.NewMem()
	s.lots = nil
	s.lotID = 0
	s.payerFund = false
	s.ownerBound = map[string]bool{}
	return nil
}

func (s *poState) reservedLot(op, gross, reserve string) error {
	g, err := feParseFloat(gross)
	if err != nil {
		return err
	}
	rv, err := feParseFloat(reserve)
	if err != nil {
		return err
	}
	s.op = op
	rel := s.now.Unix() + 200*poDay
	s.stage(op, "n1", g, rv, store.LotHeld, rel, rel, s.now.Unix())
	return nil
}

func (s *poState) reservedLotAged(op, gross, reserve, age string) error {
	g, err := feParseFloat(gross)
	if err != nil {
		return err
	}
	rv, err := feParseFloat(reserve)
	if err != nil {
		return err
	}
	a, err := strconv.Atoi(age)
	if err != nil {
		return err
	}
	s.op = op
	rel := s.now.Unix() + (120-int64(a))*poDay
	s.stage(op, "n1", g, rv, store.LotHeld, rel, rel, s.now.Unix()-int64(a)*poDay)
	return nil
}

// --- section 2/3: RequestPayout / Settle / Fail (store) ----------------------

func (s *poState) payableLot(op, v string) error {
	g, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.ensureOwner(op)
	rel := s.now.Unix() - poDay
	s.stage(op, "n1", g, 0, store.LotPayable, rel, rel, s.now.Unix()-200*poDay)
	return nil
}

func (s *poState) payableBalance(op, v string) error { return s.payableLot(op, v) }

func (s *poState) clawedLot(op, v string) error {
	g, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.op = op
	rel := s.now.Unix() - poDay
	s.stage(op, "n1", g, 0, store.LotClawed, rel, rel, s.now.Unix()-200*poDay)
	return nil
}

func (s *poState) payableLotsThree(op, a, b, c string) error {
	for _, v := range []string{a, b, c} {
		if err := s.payableLot(op, v); err != nil {
			return err
		}
	}
	return nil
}

func (s *poState) onlyHeldTotaling(op, v string) error {
	g, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.op = op
	rel := s.now.Unix() + 200*poDay
	s.stage(op, "n1", g, 0, store.LotHeld, rel, rel, s.now.Unix())
	return nil
}

func (s *poState) requestPayoutStore(op string) error {
	s.op = op
	pay, ok, reason, err := s.db.RequestPayout(op, s.now, s.minPayout)
	if err != nil {
		return err
	}
	s.payout, s.payoutOK, s.payoutReason = pay, ok, reason
	return nil
}

func (s *poState) requestPayoutAgain(op string) error {
	s.op = op
	pay, ok, _, err := s.db.RequestPayout(op, s.now, s.minPayout)
	if err != nil {
		return err
	}
	s.payout2 = pay
	_ = ok
	return nil
}

func (s *poState) pendingPayout(op, v string) error {
	if err := s.payableLot(op, v); err != nil {
		return err
	}
	return s.requestPayoutStore(op)
}

func (s *poState) pendingPayoutCovering(op, a, b, c string) error {
	if err := s.payableLotsThree(op, a, b, c); err != nil {
		return err
	}
	return s.requestPayoutStore(op)
}

func (s *poState) payoutAlreadySettled(op, transfer string) error {
	if err := s.pendingPayout(op, "60.00"); err != nil {
		return err
	}
	return s.db.SettlePayout(s.payout.ID, transfer)
}

func (s *poState) settledWithTransfer(transfer string) error {
	return s.db.SettlePayout(s.payout.ID, transfer)
}

func (s *poState) settledAgainWithTransfer(transfer string) error {
	return s.db.SettlePayout(s.payout.ID, transfer)
}

func (s *poState) failPayoutWhen() error { return s.db.FailPayout(s.payout.ID) }

func (s *poState) payoutOf(op string) (store.Payout, error) {
	pays, err := s.db.PayoutsOf(op, 100)
	if err != nil {
		return store.Payout{}, err
	}
	for _, p := range pays {
		if p.ID == s.payout.ID {
			return p, nil
		}
	}
	return store.Payout{}, fmt.Errorf("payout %d not found", s.payout.ID)
}

func (s *poState) refusedBelowMin() error {
	if s.payoutOK {
		return fmt.Errorf("payout was allowed, want refused below minimum")
	}
	if !strings.Contains(strings.ToLower(s.payoutReason), "minimum") {
		return fmt.Errorf("refusal reason = %q, want a below-minimum reason", s.payoutReason)
	}
	return nil
}

func (s *poState) noPayoutRow() error {
	pays, err := s.db.PayoutsOf(s.op, 100)
	if err != nil {
		return err
	}
	if len(pays) != 0 {
		return fmt.Errorf("expected no payout row, got %d", len(pays))
	}
	return nil
}

func (s *poState) payoutSucceeds() error {
	if !s.payoutOK {
		return fmt.Errorf("payout refused (%q), want success", s.payoutReason)
	}
	return nil
}

func (s *poState) payoutAmountIs(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	if !s.payoutOK {
		return fmt.Errorf("payout not successful, reason %q", s.payoutReason)
	}
	return feApprox(s.payout.Amount, want)
}

func (s *poState) newPayoutAmountIs(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.payout2.Amount, want)
}

func (s *poState) onlyPayableLotPaid() error { return feApprox(s.freshSplit().Paid, s.payout.Amount) }

func (s *poState) heldBalanceStill(v string) error { return s.heldBalanceIs(v) }

func (s *poState) clawedLotStaysClawed() error {
	sp := s.freshSplit()
	if sp.Held != 0 || sp.Payable != 0 {
		return fmt.Errorf("a clawed lot resurfaced (held=%g payable=%g)", sp.Held, sp.Payable)
	}
	return nil
}

func (s *poState) lotMarkedPaid() error {
	if !s.payoutOK {
		return fmt.Errorf("payout not successful")
	}
	return feApprox(s.freshSplit().Paid, s.payout.Amount)
}

func (s *poState) lotPaidInFull() error    { return feApprox(s.freshSplit().Paid, 100) }
func (s *poState) noReserveRemains() error { return feApprox(s.freshSplit().Reserved, 0) }
func (s *poState) noReserveLeftBehind() error {
	sp := s.freshSplit()
	if sp.Reserved != 0 {
		return fmt.Errorf("reserve %g left behind", sp.Reserved)
	}
	return nil
}

func (s *poState) singlePayoutLedgerRow(v, op string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	rows, err := s.db.LedgerOf(op, []string{store.KindPayout}, 100)
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return fmt.Errorf("expected exactly 1 payout ledger row, got %d", len(rows))
	}
	return feApprox(rows[0].Amount, want)
}

func (s *poState) threeLotsSamePayoutID() error {
	lots, ok, err := s.db.PayoutLots(s.op, s.payout.ID)
	if err != nil {
		return err
	}
	if !ok || len(lots) != 3 {
		return fmt.Errorf("payout %d covers %d lots, want 3 (ok=%v)", s.payout.ID, len(lots), ok)
	}
	return feApprox(s.freshSplit().Paid, 60)
}

func (s *poState) payoutStatePaid() error {
	p, err := s.payoutOf(s.op)
	if err != nil {
		return err
	}
	if p.State != store.PayoutPaid {
		return fmt.Errorf("payout state = %q, want paid", p.State)
	}
	return nil
}

func (s *poState) payoutStateFailed() error {
	p, err := s.payoutOf(s.op)
	if err != nil {
		return err
	}
	if p.State != store.PayoutFailed {
		return fmt.Errorf("payout state = %q, want failed", p.State)
	}
	return nil
}

func (s *poState) payoutStateStillPaid() error { return s.payoutStatePaid() }

func (s *poState) payoutRecordsTransfer(t string) error {
	p, err := s.payoutOf(s.op)
	if err != nil {
		return err
	}
	if p.StripeTransferID != t {
		return fmt.Errorf("payout transfer = %q, want %q", p.StripeTransferID, t)
	}
	return nil
}

func (s *poState) payoutStillRecordsTransfer(t string) error { return s.payoutRecordsTransfer(t) }

func (s *poState) ledgerRowReferencesTransfer(t string) error {
	rows, err := s.db.LedgerOf(s.op, []string{store.KindPayout}, 100)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Ref == t {
			return nil
		}
	}
	return fmt.Errorf("no payout ledger row references transfer %q", t)
}

func (s *poState) ledgerRowReversed() error {
	rows, err := s.db.LedgerOf(s.op, []string{store.KindPayout}, 100)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.State == store.StateReversed {
			return nil
		}
	}
	return fmt.Errorf("payout ledger row not reversed")
}

func (s *poState) lotsReturnPayablePlain() error { return s.payableBalanceIs("60.00") }
func (s *poState) payoutIDCleared() error {
	lots, _, err := s.db.PayoutLots(s.op, s.payout.ID)
	if err != nil {
		return err
	}
	if len(lots) != 0 {
		return fmt.Errorf("%d lots still reference the rolled-back payout id (not cleared)", len(lots))
	}
	return nil
}

func (s *poState) lotsStayPaid() error { return feApprox(s.freshSplit().Paid, 60) }

// --- section 4/7: broker payout flow ----------------------------------------

func (s *poState) ensureBroker() {
	if s.bk != nil {
		return
	}
	b := buildPayoutBrokerForTest(s.db)
	b.conn.transfer = func(dest string, cents int64, idem string) (string, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.transferCalls++
		s.gotCents = cents
		s.gotIdem = idem
		s.totalCents += cents
		if s.transferErr {
			return "", errStripeTransfer
		}
		if s.transferID != "" {
			return s.transferID, nil
		}
		return "tr_dflt", nil
	}
	s.bk = b
}

func (s *poState) kycActive(op string) error {
	s.ensureOwner(op)
	return s.db.SetConnect(op, "acct_dev_stub", "active")
}

func (s *poState) notOnboarded(op string) error { s.ensureOwner(op); return nil }

func (s *poState) transferWillFail() error    { s.ensureBroker(); s.transferErr = true; return nil }
func (s *poState) transferWillSucceed(t string) error {
	s.ensureBroker()
	s.transferID = t
	return nil
}
func (s *poState) settleWriteWillFail() error { s.failSettle = true; return nil }

func (s *poState) callsPayoutRequest(op string) error {
	s.ensureOwner(op)
	s.ensureBroker()
	if s.failSettle {
		s.bk.db = &failStore{Store: s.db, failSettlePay: true}
	}
	w := httptest.NewRecorder()
	s.bk.payoutsRequest(w, sessionReq(s.bk, http.MethodPost, "/payouts/request", op, 1))
	s.bk.db = s.db
	s.resp = w
	s.respBody = w.Body.String()
	return nil
}

func (s *poState) respCodeIs(code int) error {
	if s.resp == nil || s.resp.Code != code {
		got := 0
		if s.resp != nil {
			got = s.resp.Code
		}
		return fmt.Errorf("response = %d, want %d (body=%s)", got, code, s.respBody)
	}
	return nil
}

func (s *poState) responseIs(code string) error {
	c, err := strconv.Atoi(code)
	if err != nil {
		return err
	}
	return s.respCodeIs(c)
}

func (s *poState) rejectedWith(code string) error { return s.responseIs(code) }
func (s *poState) responseBelowMinimum(code string) error {
	if err := s.responseIs(code); err != nil {
		return err
	}
	if !strings.Contains(strings.ToLower(s.respBody), "minimum") {
		return fmt.Errorf("body %q does not mention minimum", s.respBody)
	}
	return nil
}

func (s *poState) responseMentionsTransfer(code, t string) error {
	if err := s.responseIs(code); err != nil {
		return err
	}
	if !strings.Contains(s.respBody, t) {
		return fmt.Errorf("body %q does not mention transfer %q", s.respBody, t)
	}
	return nil
}

func (s *poState) transferCreatedForExactly(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	wantCents := int64(want*s.bk.bill.creditUSD*100 + 0.5)
	if s.gotCents != wantCents {
		return fmt.Errorf("transfer cents = %d, want %d", s.gotCents, wantCents)
	}
	return nil
}

func (s *poState) respPayout() (store.Payout, error) {
	var out struct {
		Payout store.Payout `json:"payout"`
	}
	if err := json.Unmarshal([]byte(s.respBody), &out); err != nil {
		return store.Payout{}, err
	}
	return out.Payout, nil
}

func (s *poState) idemKeyIsPayoutID() error {
	p, err := s.respPayout()
	if err != nil {
		return err
	}
	want := "payout:" + strconv.FormatInt(p.ID, 10)
	if s.gotIdem != want {
		return fmt.Errorf("idempotency key = %q, want %q", s.gotIdem, want)
	}
	return nil
}

func (s *poState) settledReturnedPaid() error {
	p, err := s.respPayout()
	if err != nil {
		return err
	}
	if p.State != store.PayoutPaid {
		return fmt.Errorf("returned payout state = %q, want paid", p.State)
	}
	return nil
}

func (s *poState) noStripeTransfer() error {
	if s.transferCalls != 0 {
		return fmt.Errorf("a transfer was attempted (%d calls), want none", s.transferCalls)
	}
	return nil
}

func (s *poState) rolledBackViaFail() error {
	pays, err := s.db.PayoutsOf(s.op, 100)
	if err != nil {
		return err
	}
	for _, p := range pays {
		if p.State == store.PayoutFailed {
			return nil
		}
	}
	return fmt.Errorf("no failed (rolled-back) payout found")
}

func (s *poState) noCompletedTransferOnPayable() error {
	pays, err := s.db.PayoutsOf(s.op, 100)
	if err != nil {
		return err
	}
	for _, p := range pays {
		if p.State == store.PayoutPaid {
			return fmt.Errorf("a completed (paid) payout %+v is left while lots are payable", p)
		}
	}
	return s.payableBalanceIs("60.00")
}

func (s *poState) lotsNotRolledBack() error {
	if sp := s.freshSplit(); sp.Payable > 1e-9 {
		return fmt.Errorf("lots were rolled back to payable (%g) but should NOT be (money moved)", sp.Payable)
	}
	return nil
}

func (s *poState) concurrentRequests(op string) error {
	s.ensureOwner(op)
	s.ensureBroker()
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			s.bk.payoutsRequest(w, sessionReq(s.bk, http.MethodPost, "/payouts/request", op, 1))
			codes[idx] = w.Code
		}(i)
	}
	wg.Wait()
	s.concCodes = codes
	return nil
}

func (s *poState) exactlyOnePayoutDebits(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	ok := 0
	for _, c := range s.concCodes {
		if c == http.StatusOK {
			ok++
		}
	}
	if ok != 1 {
		return fmt.Errorf("%d concurrent requests succeeded, want exactly 1", ok)
	}
	pays, _ := s.db.PayoutsOf(s.op, 100)
	paid := 0
	for _, p := range pays {
		if p.State == store.PayoutPaid {
			paid++
			if err := feApprox(p.Amount, want); err != nil {
				return fmt.Errorf("paid payout amount: %w", err)
			}
		}
	}
	if paid != 1 {
		return fmt.Errorf("%d paid payouts, want 1", paid)
	}
	return nil
}

func (s *poState) otherSeesNothing() error {
	below := 0
	for _, c := range s.concCodes {
		if c == http.StatusBadRequest {
			below++
		}
	}
	if below != 1 {
		return fmt.Errorf("expected 1 below-minimum/empty response, got %d (codes=%v)", below, s.concCodes)
	}
	return nil
}

func (s *poState) totalTransferred(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	wantCents := int64(want*s.bk.bill.creditUSD*100 + 0.5)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.totalCents != wantCents {
		return fmt.Errorf("total transferred = %d cents, want %d", s.totalCents, wantCents)
	}
	return nil
}

// --- section 6: payoutTransfer cents + refreshConnectStatus ------------------

func (s *poState) creditUSDRate(v string) error {
	r, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.ensureBroker()
	s.bk.bill.creditUSD = r
	return nil
}

func (s *poState) transferOfCredits(v string) error {
	amt, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.ensureBroker()
	_, err = s.bk.payoutTransfer("acct_x", "op1", amt, "idem_x")
	return err
}

func (s *poState) stripeAmountCents(v string) error {
	want, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return err
	}
	if s.gotCents != want {
		return fmt.Errorf("Stripe amount = %d cents, want %d", s.gotCents, want)
	}
	return nil
}

func (s *poState) hasConnectAccount(op string) error {
	s.ensureOwner(op)
	s.connectID = "acct_" + op
	return s.db.SetConnect(op, s.connectID, "onboarding")
}

func (s *poState) connectAcctAccount(op, acct string) error {
	s.ensureOwner(op)
	s.connectID = acct
	return s.db.SetConnect(op, acct, "active")
}

func (s *poState) storedStatus(op, status string) error {
	s.ensureOwner(op)
	s.connectID = "acct_" + op
	return s.db.SetConnect(op, s.connectID, status)
}

// startStripeStub points stripeAPIBase at a stub returning the given account JSON.
func (s *poState) startStripeStub(transfers, reason string) {
	s.ensureBroker()
	s.bk.conn.secretKey = "sk_test_stub"
	s.stripeSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{
			"capabilities": map[string]any{"transfers": transfers},
			"requirements": map[string]any{"disabled_reason": reason},
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	stripeAPIBase = s.stripeSrv.URL
}

func (s *poState) reportsTransfersCapability(cap string) error {
	s.startStripeStub(cap, "")
	s.statusOut = s.bk.refreshConnectStatus("op1", s.connectID)
	return nil
}

func (s *poState) reportsTransfersReason(transfers, reason string) error {
	s.startStripeStub(transfers, reason)
	s.statusOut = s.bk.refreshConnectStatus("op1", s.connectID)
	return nil
}

func (s *poState) refreshTransportError() error {
	s.ensureBroker()
	s.bk.conn.secretKey = "sk_test_stub"
	// Point at a closed server so the HTTP round trip errors (transport error).
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	stripeAPIBase = url
	s.statusOut = s.bk.refreshConnectStatus("op1", s.connectID)
	return nil
}

func (s *poState) storedStatusBecomes(status string) error {
	if s.statusOut != status {
		return fmt.Errorf("refresh returned %q, want %q", s.statusOut, status)
	}
	o, found, _ := s.db.OwnerByLogin("op1")
	if !found || o.ConnectStatus != status {
		return fmt.Errorf("persisted status = %q, want %q", o.ConnectStatus, status)
	}
	return nil
}

func (s *poState) operatorCanRequest() error {
	o, _, _ := s.db.OwnerByLogin("op1")
	if o.ConnectStatus != "active" {
		return fmt.Errorf("operator status = %q, cannot request payout", o.ConnectStatus)
	}
	return nil
}

func (s *poState) statusFallsBackTo(status string) error {
	if s.statusOut != "" {
		return fmt.Errorf("refresh returned %q on transport error, want \"\"", s.statusOut)
	}
	o, _, _ := s.db.OwnerByLogin("op1")
	if o.ConnectStatus != status {
		return fmt.Errorf("status changed to %q on transport error, want stored %q", o.ConnectStatus, status)
	}
	return nil
}

// --- section 7: fail-closed env guards --------------------------------------

func (s *poState) requireLiveSet() error      { os.Setenv("ROGERAI_REQUIRE_LIVE", "1"); s.requireLive = true; return nil }
func (s *poState) requireLiveNotSet() error   { os.Unsetenv("ROGERAI_REQUIRE_LIVE"); s.requireLive = false; return nil }
func (s *poState) keyNotLive() error          { os.Setenv("STRIPE_SECRET_KEY", "sk_test_devkey"); return nil }
func (s *poState) noStripeKey() error         { os.Setenv("STRIPE_SECRET_KEY", ""); return nil }
func (s *poState) railLoads() error           { s.loadedConn = loadConnect(); return nil }
func (s *poState) payoutsDisabled() error {
	if s.loadedConn.secretKey != "" {
		return fmt.Errorf("payouts NOT disabled (secretKey=%q)", s.loadedConn.secretKey)
	}
	return nil
}

func (s *poState) noDevStubTransferIssued() error {
	b := buildPayoutBrokerForTest(s.db)
	b.conn = s.loadedConn
	id, err := b.payoutTransfer("", "op1", 60, "idem")
	if err == nil {
		return fmt.Errorf("transfer returned id %q with no error, want a refusal under REQUIRE_LIVE", id)
	}
	if strings.HasPrefix(id, "tr_dev_stub_") {
		return fmt.Errorf("a dev-stub transfer id was issued: %q", id)
	}
	return nil
}

func (s *poState) connectAccountStub(op, acct string) error {
	s.ensureOwner(op)
	s.connectID = acct
	return s.db.SetConnect(op, acct, "active")
}

func (s *poState) transferAttempted(op string) error {
	s.ensureOwner(op)
	if s.requireLive {
		// Full request flow: the transfer must be refused and rolled back.
		b := buildPayoutBrokerForTest(s.db) // loadConnect reads REQUIRE_LIVE now (key blanked)
		s.bk = b
		w := httptest.NewRecorder()
		b.payoutsRequest(w, sessionReq(b, http.MethodPost, "/payouts/request", op, 1))
		s.resp = w
		s.respBody = w.Body.String()
		return nil
	}
	// Dev (no key, not REQUIRE_LIVE): the transfer stubs to a tr_dev_stub_ id that can
	// still be settled. Drive the store + transfer primitives directly (KYC is not set,
	// so the full handler would 403 before the stub — this exercises the stub itself). Use
	// a broker with NO conn.transfer hook so payoutTransfer takes the real dev-stub branch.
	b := buildPayoutBrokerForTest(s.db)
	pay, ok, _, err := s.db.RequestPayout(op, s.now, 0)
	if err != nil || !ok {
		return fmt.Errorf("RequestPayout ok=%v err=%v", ok, err)
	}
	s.payout = pay
	id, err := b.payoutTransfer("", op, pay.Amount, "payout:"+strconv.FormatInt(pay.ID, 10))
	if err != nil {
		return err
	}
	s.devTransferID = id
	return nil
}

func (s *poState) transferRefused() error { return s.respCodeIs(http.StatusBadGateway) }

func (s *poState) settleNeverFakeID() error {
	pays, err := s.db.PayoutsOf(s.op, 100)
	if err != nil {
		return err
	}
	for _, p := range pays {
		if p.State == store.PayoutPaid {
			return fmt.Errorf("a payout was settled paid (%+v) under REQUIRE_LIVE — fake id reached SettlePayout", p)
		}
	}
	return nil
}

func (s *poState) rollsBackViaFailPayout() error {
	if err := s.rolledBackViaFail(); err != nil {
		return err
	}
	return s.payableBalanceIs("60.00")
}

func (s *poState) devStubReturned(prefix string) error {
	if !strings.HasPrefix(s.devTransferID, prefix) {
		return fmt.Errorf("transfer id = %q, want prefix %q", s.devTransferID, prefix)
	}
	return nil
}

func (s *poState) settledInDev() error {
	if err := s.db.SettlePayout(s.payout.ID, s.devTransferID); err != nil {
		return err
	}
	return s.payoutStatePaid()
}

// concCodes lives on the state for the concurrency scenario.

func TestPayoutsBDD(t *testing.T) {
	// Claim the env vars the scenarios mutate via os.Setenv: t.Setenv snapshots their prior
	// values and restores them when this test ends, so nothing leaks to sibling tests.
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "120")
	t.Setenv("ROGERAI_PAYOUT_MIN", "25")
	t.Setenv("ROGERAI_REQUIRE_LIVE", "")
	t.Setenv("STRIPE_SECRET_KEY", "")
	orig := stripeAPIBase
	t.Cleanup(func() { stripeAPIBase = orig })
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &poState{origBase: orig}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.stripeSrv != nil {
					st.stripeSrv.Close()
					st.stripeSrv = nil
				}
				stripeAPIBase = orig
				return ctx, nil
			})

			// Background
			sc.Step(`^a fresh money store$`, st.freshStore)
			sc.Step(`^the platform fee rate is (\d+)%$`, st.feePct)
			sc.Step(`^the payout policy is hold (\d+) days, reserve ([\d.]+), minimum ([\d.]+), schedule monthly$`, st.policyBackground)
			sc.Step(`^node "([^"]*)" is owned by account "([^"]*)"$`, st.nodeOwned)

			// section 1
			sc.Step(`^operator "([^"]*)" earned a lot of ([\d.]+) just now on node "([^"]*)"$`, st.earnedJustNow)
			sc.Step(`^operator "([^"]*)" earns a lot whose owner share is ([\d.]+) just now$`, st.earnsLotOwnerShare)
			sc.Step(`^operator "([^"]*)" has a held lot of ([\d.]+)$`, st.heldLot)
			sc.Step(`^operator "([^"]*)" has a held lot of ([\d.]+) created (\d+) days ago on node "([^"]*)"$`, st.heldLotAgeNode)
			sc.Step(`^operator "([^"]*)" has a held lot of ([\d.]+) created (\d+) days ago$`, st.heldLotAge)
			sc.Step(`^operator "([^"]*)" has a held lot of ([\d.]+) releasing in (\d+) second$`, st.heldLotReleasingIn)
			sc.Step(`^operator "([^"]*)" has a held lot of ([\d.]+) with release time exactly now$`, st.heldLotReleaseExactlyNow)
			sc.Step(`^node "([^"]*)" has an open recount hold$`, st.nodeRecountHold)
			sc.Step(`^account "([^"]*)" has an open recount hold$`, st.acctRecountHold)
			sc.Step(`^the hold on node "([^"]*)" is cleared$`, st.clearNodeHold)
			sc.Step(`^the earnings split for "([^"]*)" is read now$`, st.readSplitNow)
			sc.Step(`^the held balance is ([\d.]+)$`, st.heldBalanceIs)
			sc.Step(`^the held balance is still ([\d.]+)$`, st.heldBalanceStill)
			sc.Step(`^the payable balance is ([\d.]+)$`, st.payableBalanceIs)
			sc.Step(`^the payable balance is still ([\d.]+)$`, st.payableBalanceIs)
			sc.Step(`^the reserved balance is ([\d.]+)$`, st.reservedBalanceIs)
			sc.Step(`^the next release is (\d+) days from now$`, st.nextReleaseDays)

			// section 2/3 setup + assertions
			sc.Step(`^operator "([^"]*)" has a payable balance of ([\d.]+)$`, st.payableBalance)
			sc.Step(`^operator "([^"]*)" has a payable lot of ([\d.]+)$`, st.payableLot)
			sc.Step(`^operator "([^"]*)" has a clawed lot of ([\d.]+)$`, st.clawedLot)
			sc.Step(`^operator "([^"]*)" has payable lots of ([\d.]+), ([\d.]+), and ([\d.]+)$`, st.payableLotsThree)
			sc.Step(`^operator "([^"]*)" has only held lots totaling ([\d.]+)$`, st.onlyHeldTotaling)
			sc.Step(`^operator "([^"]*)" has a pending payout of ([\d.]+)$`, st.pendingPayout)
			sc.Step(`^operator "([^"]*)" has a pending payout of ([\d.]+) covering lots of ([\d.]+), ([\d.]+), and ([\d.]+)$`, func(op, _, a, b, c string) error { return st.pendingPayoutCovering(op, a, b, c) })
			sc.Step(`^operator "([^"]*)" has a payout already settled with transfer "([^"]*)"$`, st.payoutAlreadySettled)
			sc.Step(`^operator "([^"]*)" requests a payout$`, st.requestPayoutStore)
			sc.Step(`^operator "([^"]*)" requests a payout again$`, st.requestPayoutAgain)
			sc.Step(`^the payout is settled with transfer "([^"]*)"$`, st.settledWithTransfer)
			sc.Step(`^the payout is settled again with transfer "([^"]*)"$`, st.settledAgainWithTransfer)
			sc.Step(`^the payout is failed$`, st.failPayoutWhen)
			sc.Step(`^the payout is refused for being below the minimum$`, st.refusedBelowMin)
			sc.Step(`^no payout row is created$`, st.noPayoutRow)
			sc.Step(`^the payout succeeds$`, st.payoutSucceeds)
			sc.Step(`^the payout amount is ([\d.]+)$`, st.payoutAmountIs)
			sc.Step(`^the new payout amount is ([\d.]+)$`, st.newPayoutAmountIs)
			sc.Step(`^only the payable lot is marked paid$`, st.onlyPayableLotPaid)
			sc.Step(`^the clawed lot stays clawed$`, st.clawedLotStaysClawed)
			sc.Step(`^the lot is marked paid$`, st.lotMarkedPaid)
			sc.Step(`^the lot is marked paid in full$`, st.lotPaidInFull)
			sc.Step(`^no reserve remains attributable to the lot$`, st.noReserveRemains)
			sc.Step(`^no reserve is left behind$`, st.noReserveLeftBehind)
			sc.Step(`^a single payout ledger row of (-?[\d.]+) is written for "([^"]*)"$`, st.singlePayoutLedgerRow)
			sc.Step(`^all three lots are marked paid with the same payout id$`, st.threeLotsSamePayoutID)
			sc.Step(`^the payout state is paid$`, st.payoutStatePaid)
			sc.Step(`^the payout state is failed$`, st.payoutStateFailed)
			sc.Step(`^the payout state is still paid$`, st.payoutStateStillPaid)
			sc.Step(`^the payout records transfer "([^"]*)"$`, st.payoutRecordsTransfer)
			sc.Step(`^the payout still records transfer "([^"]*)"$`, st.payoutStillRecordsTransfer)
			sc.Step(`^the payout ledger row references transfer "([^"]*)"$`, st.ledgerRowReferencesTransfer)
			sc.Step(`^the payout ledger row is reversed$`, st.ledgerRowReversed)
			sc.Step(`^all three lots return to payable$`, st.lotsReturnPayablePlain)
			sc.Step(`^the lots return to payable$`, st.lotsReturnPayablePlain)
			sc.Step(`^each rolled-back lot has its payout id cleared$`, st.payoutIDCleared)
			sc.Step(`^the payable balance is ([\d.]+) again$`, st.payableBalanceIs)
			sc.Step(`^the lots stay paid$`, st.lotsStayPaid)

			// section 5 reserve
			sc.Step(`^the payout policy reserve is ([\d.]+)$`, st.policyReserveIs)
			sc.Step(`^operator "([^"]*)" has a held lot with gross ([\d.]+) and reserve ([\d.]+) releasing in the future$`, st.reservedLot)
			sc.Step(`^operator "([^"]*)" has a lot with gross ([\d.]+) and reserve ([\d.]+) created (\d+) days ago$`, st.reservedLotAged)
			sc.Step(`^the lot reserve is ([\d.]+)$`, st.lotReserveIs)
			sc.Step(`^the lot reserve_release_at equals its release_at$`, st.reserveReleaseCoupled)

			// section 4 broker flow
			sc.Step(`^operator "([^"]*)" has completed Connect KYC \(transfers active\)$`, st.kycActive)
			sc.Step(`^operator "([^"]*)" has not completed Connect onboarding$`, st.notOnboarded)
			sc.Step(`^the Stripe transfer will fail$`, st.transferWillFail)
			sc.Step(`^the Stripe transfer will succeed with "([^"]*)"$`, st.transferWillSucceed)
			sc.Step(`^the settle write will fail$`, st.settleWriteWillFail)
			sc.Step(`^operator "([^"]*)" calls POST /payouts/request$`, st.callsPayoutRequest)
			sc.Step(`^a Stripe transfer is created for exactly ([\d.]+)$`, st.transferCreatedForExactly)
			sc.Step(`^the transfer idempotency key is the payout id$`, st.idemKeyIsPayoutID)
			sc.Step(`^the payout is settled and returned as paid$`, st.settledReturnedPaid)
			sc.Step(`^the request is rejected with (\d+)$`, st.rejectedWith)
			sc.Step(`^no Stripe transfer is created$`, st.noStripeTransfer)
			sc.Step(`^the payout is rolled back via FailPayout$`, st.rolledBackViaFail)
			sc.Step(`^the response is a (\d+)$`, st.responseIs)
			sc.Step(`^the response is a (\d+) below-minimum$`, st.responseBelowMinimum)
			sc.Step(`^the response is a (\d+) mentioning transfer "([^"]*)"$`, st.responseMentionsTransfer)
			sc.Step(`^no completed transfer is left associated with payable lots$`, st.noCompletedTransferOnPayable)
			sc.Step(`^the lots are NOT rolled back$`, st.lotsNotRolledBack)
			sc.Step(`^two payout requests for "([^"]*)" run concurrently$`, st.concurrentRequests)
			sc.Step(`^exactly one payout debits ([\d.]+)$`, st.exactlyOnePayoutDebits)
			sc.Step(`^the other sees nothing payable or is below minimum$`, st.otherSeesNothing)
			sc.Step(`^the total transferred is ([\d.]+)$`, st.totalTransferred)

			// section 6
			sc.Step(`^the credit-to-USD rate is ([\d.]+)$`, st.creditUSDRate)
			sc.Step(`^a payout transfer of ([\d.]+) credits is created$`, st.transferOfCredits)
			sc.Step(`^the Stripe amount is (\d+) cents$`, st.stripeAmountCents)
			sc.Step(`^operator "([^"]*)" has a Connect account$`, st.hasConnectAccount)
			sc.Step(`^the Connect account reports transfers capability "([^"]*)"$`, st.reportsTransfersCapability)
			sc.Step(`^the Connect account reports transfers "([^"]*)" and disabled reason "([^"]*)"$`, st.reportsTransfersReason)
			sc.Step(`^operator "([^"]*)" has a stored status of "([^"]*)"$`, st.storedStatus)
			sc.Step(`^the Connect status refresh hits a transport error$`, st.refreshTransportError)
			sc.Step(`^the stored status becomes "([^"]*)"$`, st.storedStatusBecomes)
			sc.Step(`^the operator can request a payout$`, st.operatorCanRequest)
			sc.Step(`^the status read falls back to the stored "([^"]*)"$`, st.statusFallsBackTo)

			// section 7
			sc.Step(`^ROGERAI_REQUIRE_LIVE is set$`, st.requireLiveSet)
			sc.Step(`^REQUIRE_LIVE is not set$`, st.requireLiveNotSet)
			sc.Step(`^the Stripe secret key is not an sk_live key$`, st.keyNotLive)
			sc.Step(`^no Stripe secret key is configured$`, st.noStripeKey)
			sc.Step(`^the payout rail loads$`, st.railLoads)
			sc.Step(`^payouts are disabled$`, st.payoutsDisabled)
			sc.Step(`^no transfer is ever issued with a dev-stub transfer id$`, st.noDevStubTransferIssued)
			sc.Step(`^operator "([^"]*)" has connect account "([^"]*)"$`, st.connectAccountStub)
			sc.Step(`^a payout transfer is attempted for "([^"]*)"$`, st.transferAttempted)
			sc.Step(`^the transfer is refused$`, st.transferRefused)
			sc.Step(`^SettlePayout is never reached with a fake transfer id$`, st.settleNeverFakeID)
			sc.Step(`^the payout rolls back via FailPayout$`, st.rollsBackViaFailPayout)
			sc.Step(`^a dev-stub transfer id prefixed "([^"]*)" is returned$`, st.devStubReturned)
			sc.Step(`^the payout can still be settled in dev$`, st.settledInDev)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/money/payouts.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("money/payouts behavior scenarios failed (see godog output above)")
	}
}
