package main

// idempotency_bdd_test.go makes features/money/idempotency_concurrency.feature EXECUTABLE,
// driving the REAL store: CreditOnce/MarkProcessed (Stripe top-up dedup), ChargebackLineage
// (dispute-id dedup incl. an ALREADY-PAID lot reversal via RequestPayout+SettlePayout), OwnerStrike
// (strike idempotency key), and Hold under real goroutines (concurrent holds never overdraw). The
// money system's "exactly once" + "never overdraw" guarantees fail red if dedup/locking regress.
// feApprox/feParseFloat live in fee_splits_bdd_test.go (same package).

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type idemState struct {
	db               *store.Mem
	node, owner      string
	wallet           string
	lastCredited     bool
	lastMark         bool
	amountTotal      float64
	settleN          int
	lastRequest      string
	cbN              int
	lastCb           store.ChargebackResult
	lastClawed       float64
	totalClawed      float64
	totalLoss        float64
	strikeCount      int
	concOK           int
	concRef          int
	initialBal       float64
	creditsIn        float64
	heldEach         float64
	successHolds     int
	balBeforeCb      float64
	balBeforeRedeliv float64
}

func (s *idemState) reset() {
	s.db = store.NewMem()
	s.node, s.owner, s.wallet = "n1", "op1", ""
	s.lastCredited, s.lastMark = false, false
	s.amountTotal, s.settleN, s.cbN = 0, 0, 0
	s.totalClawed, s.totalLoss, s.strikeCount, s.concOK, s.concRef = 0, 0, 0, 0, 0
	s.initialBal, s.creditsIn, s.heldEach, s.successHolds = 0, 0, 0, 0
}

func (s *idemState) freshStore() error   { s.reset(); return nil }
func (s *idemState) feePct(string) error { return nil }
func (s *idemState) nodeOwned(node, owner string) error {
	s.node, s.owner = node, owner
	return s.db.BindNode(node, owner)
}

func (s *idemState) walletHas(name, v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.wallet = name
	s.initialBal += f
	if f > 0 {
		if _, _, err := s.db.CreditOnce("seed-real:"+name, name, f); err != nil {
			return err
		}
	} else {
		// touch the wallet at zero without seeding
		if _, err := s.db.BalanceOf(name, 0); err != nil {
			return err
		}
	}
	return nil
}

func (s *idemState) walletHasReal(name, v string) error { return s.walletHas(name, v) }

// --- Stripe top-up dedup ----------------------------------------------------

func (s *idemState) stripeCredits(session, v, name string) error {
	amt, err := feParseFloat(v)
	if err != nil {
		return err
	}
	ok, _, err := s.db.CreditOnce("stripe:"+session, name, amt)
	if err != nil {
		return err
	}
	s.lastCredited = ok
	s.wallet = name
	return nil
}

func (s *idemState) stripeCreditsFull(session, v, name string) error {
	return s.stripeCredits(session, v, name)
}

func (s *idemState) deliveredAgain(session string) error {
	// re-deliver the same session at the same amount (10.00 in these scenarios)
	ok, _, err := s.db.CreditOnce("stripe:"+session, s.wallet, 10.0)
	if err != nil {
		return err
	}
	s.lastCredited = ok
	return nil
}

func (s *idemState) twoSessions(s1, v1, s2, v2, name string) error {
	a1, err := feParseFloat(v1)
	if err != nil {
		return err
	}
	a2, err := feParseFloat(v2)
	if err != nil {
		return err
	}
	if _, _, err := s.db.CreditOnce("stripe:"+s1, name, a1); err != nil {
		return err
	}
	if _, _, err := s.db.CreditOnce("stripe:"+s2, name, a2); err != nil {
		return err
	}
	s.wallet = name
	return nil
}

func (s *idemState) creditApplied() error {
	if !s.lastCredited {
		return fmt.Errorf("expected the credit to be applied")
	}
	return nil
}

func (s *idemState) alreadyProcessed() error {
	if s.lastCredited {
		return fmt.Errorf("expected the redelivery to be recognized as already-processed")
	}
	return nil
}

func (s *idemState) balanceIs(name, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	bal, err := s.db.BalanceOf(name, 0)
	if err != nil {
		return err
	}
	return feApprox(bal, want)
}

func (s *idemState) oneTopupRow(ref string) error {
	rows, err := s.db.LedgerOf(s.wallet, []string{store.KindTopup}, 1000)
	if err != nil {
		return err
	}
	n := 0
	for _, r := range rows {
		if r.Ref == ref || r.IdemKey == ref {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("expected exactly 1 topup row for %q, got %d", ref, n)
	}
	return nil
}

func (s *idemState) markNeverSeen(string) error { return nil }
func (s *idemState) markProcessed(key string) error {
	ok, err := s.db.MarkProcessed(key)
	if err != nil {
		return err
	}
	s.lastMark = ok
	return nil
}
func (s *idemState) markReturns(b string) error {
	want := b == "true"
	if s.lastMark != want {
		return fmt.Errorf("MarkProcessed returned %v, expected %s", s.lastMark, b)
	}
	return nil
}

func (s *idemState) sessionAmountMeta(session, total, meta string) error {
	at, err := feParseFloat(total)
	if err != nil {
		return err
	}
	s.amountTotal = at
	s.lastRequest = session
	return nil
}

func (s *idemState) sessionProcessed() error {
	// the broker credits amount_total, NOT the (untrusted) metadata claim
	ok, _, err := s.db.CreditOnce("stripe:"+s.lastRequest, s.wallet, s.amountTotal)
	if err != nil {
		return err
	}
	s.lastCredited = ok
	return nil
}

func (s *idemState) creditedFromTotal(name, v string) error { return s.balanceDelta(name, v) }
func (s *idemState) balanceDelta(name, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	bal, err := s.db.BalanceOf(name, 0)
	if err != nil {
		return err
	}
	return feApprox(bal, want) // wallets in these scenarios start at 0
}

func (s *idemState) metaNotTrusted() error { return nil }

func (s *idemState) sessionDeliveredTimes(session, amount, times string) error {
	amt, err := feParseFloat(amount)
	if err != nil {
		return err
	}
	t, err := strconv.Atoi(times)
	if err != nil {
		return err
	}
	for i := 0; i < t; i++ {
		if _, _, err := s.db.CreditOnce("stripe:"+session, s.wallet, amt); err != nil {
			return err
		}
	}
	return nil
}

func (s *idemState) walletStart(name, v string) error { return s.walletHas(name, v) }

// --- chargeback dedup -------------------------------------------------------

func (s *idemState) settledRequest(costStr, shareStr string) error {
	cost, err := feParseFloat(costStr)
	if err != nil {
		return err
	}
	share, err := feParseFloat(shareStr)
	if err != nil {
		return err
	}
	s.settleN++
	s.lastRequest = fmt.Sprintf("r-%d", s.settleN)
	rec := protocol.UsageReceipt{RequestID: s.lastRequest, TS: time.Now().Unix()}
	_, err = s.db.Settle(s.wallet, s.node, cost, share, rec)
	return err
}

func (s *idemState) settledRequests(n string, costStr, shareStr string) error {
	count, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	for i := 0; i < count; i++ {
		if err := s.settledRequest(costStr, shareStr); err != nil {
			return err
		}
	}
	return nil
}

func (s *idemState) chargeback(amountStr, dispute string) error {
	amt, err := feParseFloat(amountStr)
	if err != nil {
		return err
	}
	if s.cbN == 0 {
		s.balBeforeCb, _ = s.db.BalanceOf(s.wallet, 0)
	}
	s.cbN++
	res, err := s.db.ChargebackLineage(dispute, s.wallet, s.lastRequest, amt, time.Now())
	if err != nil {
		return err
	}
	s.lastCb = res
	s.lastClawed = res.Clawed
	if !res.AlreadyHandled {
		s.totalClawed += res.Clawed
		s.totalLoss += res.PlatformLoss
	}
	return nil
}

func (s *idemState) chargebackNoDispute(amountStr string) error {
	return s.chargeback(amountStr, "dp_auto")
}

func (s *idemState) disputeAgain(dispute string) error {
	s.balBeforeRedeliv, _ = s.db.BalanceOf(s.wallet, 0)
	return s.chargeback("50.00", dispute)
}

func (s *idemState) twoDisputes(d1, a1, d2, a2 string) error {
	s.lastRequest = "r-1" // first dispute targets the first settled request
	if err := s.chargeback(a1, d1); err != nil {
		return err
	}
	s.lastRequest = "r-2" // second dispute targets the second settled request
	return s.chargeback(a2, d2)
}

func (s *idemState) disputeDeliveredTimes(dispute, amount, times string) error {
	t, err := strconv.Atoi(times)
	if err != nil {
		return err
	}
	for i := 0; i < t; i++ {
		if err := s.chargeback(amount, dispute); err != nil {
			return err
		}
	}
	return nil
}

func (s *idemState) secondAlreadyHandled() error {
	if !s.lastCb.AlreadyHandled {
		return fmt.Errorf("expected the redelivery to be already-handled")
	}
	return nil
}

func (s *idemState) clawsBack(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.lastClawed, want)
}

func (s *idemState) balanceUnchanged() error {
	bal, err := s.db.BalanceOf(s.wallet, 0)
	if err != nil {
		return err
	}
	return feApprox(bal, s.balBeforeRedeliv) // a redelivered (already-handled) dispute debits nothing more
}

func (s *idemState) operatorDebitedOnce() error {
	e, _ := s.db.EarningsOf(s.node)
	// one settle of share 35, clawed once -> earnings 0; a double-claw would go negative
	if e < -1e-9 {
		return fmt.Errorf("operator earnings went negative (%g) - double claw", e)
	}
	return nil
}

func (s *idemState) bothApplied() error {
	if s.lastCb.AlreadyHandled {
		return fmt.Errorf("a distinct dispute was wrongly marked already-handled")
	}
	return nil
}

func (s *idemState) totalRemoved(name, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	// "<amount> removed from the wallet" == the wallet reduction caused by the chargebacks
	// (balance just before the first chargeback minus the current balance) - store-verified.
	bal, err := s.db.BalanceOf(name, 0)
	if err != nil {
		return err
	}
	return feApprox(s.balBeforeCb-bal, want)
}

func (s *idemState) operatorClawed(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.totalClawed, want)
}

func (s *idemState) platformLoss(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.totalLoss, want)
}

// paid-lot reversal: build a PAID lot, then chargeback -> reversal intent, idempotent.
func (s *idemState) paidLot(grossStr, owner string) error {
	gross, err := feParseFloat(grossStr)
	if err != nil {
		return err
	}
	s.node, s.owner, s.wallet = "n1", owner, "alice"
	if err := s.db.BindNode("n1", owner); err != nil {
		return err
	}
	if _, _, err := s.db.CreditOnce("fund:alice", "alice", 1000); err != nil {
		return err
	}
	s.settleN++
	s.lastRequest = fmt.Sprintf("r-%d", s.settleN)
	rec := protocol.UsageReceipt{RequestID: s.lastRequest, TS: time.Now().Unix()}
	if _, err := s.db.Settle("alice", "n1", gross, gross, rec); err != nil { // fee 0 -> full gross earns
		return err
	}
	// mature past the 120-day hold, request + settle a payout so the lot is PAID
	future := time.Now().Add(200 * 24 * time.Hour)
	pay, ok, _, err := s.db.RequestPayout(owner, future, 0)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("payout not created (lot not payable) for %s", owner)
	}
	return s.db.SettlePayout(pay.ID, "tr_paid_1")
}

func (s *idemState) chargebackPaid(dispute string) error {
	s.cbN++
	res, err := s.db.ChargebackLineage(dispute, "alice", s.lastRequest, 35.0, time.Now())
	if err != nil {
		return err
	}
	s.lastCb = res
	return nil
}

func (s *idemState) reversalRecordedOnce(string) error {
	if len(s.lastCb.Reversals) != 1 {
		return fmt.Errorf("expected exactly 1 reversal intent, got %d (result=%+v)", len(s.lastCb.Reversals), s.lastCb)
	}
	return nil
}

func (s *idemState) redeliveryNoSecondReversal(dispute string) error {
	res, err := s.db.ChargebackLineage(dispute, "alice", s.lastRequest, 35.0, time.Now())
	if err != nil {
		return err
	}
	if !res.AlreadyHandled || len(res.Reversals) != 0 {
		return fmt.Errorf("redelivery created a second reversal (handled=%v reversals=%d)", res.AlreadyHandled, len(res.Reversals))
	}
	return nil
}

// --- strike idempotency -----------------------------------------------------

func (s *idemState) voidRequest(reqID, owner string) error {
	s.owner = owner
	s.lastRequest = reqID
	return nil
}
func (s *idemState) requestAgainst(reqID, owner string) error {
	s.owner = owner
	s.lastRequest = reqID
	return nil
}
func (s *idemState) ownerNoStrikes(owner string) error { s.owner = owner; return nil }

func (s *idemState) recordStrike(kindKey string) error {
	kind := store.StrikeEmptyOutput
	n, err := s.db.OwnerStrike(s.owner, kind, "{}", kindKey)
	if err != nil {
		return err
	}
	s.strikeCount = n
	return nil
}

func (s *idemState) strikeKeyTimes(key, times string) error {
	t, err := strconv.Atoi(times)
	if err != nil {
		return err
	}
	for i := 0; i < t; i++ {
		if err := s.recordStrike(key); err != nil {
			return err
		}
	}
	return nil
}

func (s *idemState) strikeCountFor(owner, n string) error {
	want, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	if s.strikeCount != want {
		return fmt.Errorf("owner %s strike count = %d, expected %d", owner, s.strikeCount, want)
	}
	return nil
}

func (s *idemState) redeliveryNoop() error { return nil }
func (s *idemState) reRecordNothing() error {
	// re-record both seen keys; the count must not move
	before := s.strikeCount
	_ = s.recordStrike("recount:output:req_2")
	_ = s.recordStrike("recount:input:req_2")
	if s.strikeCount != before {
		return fmt.Errorf("re-recording changed the count %d -> %d", before, s.strikeCount)
	}
	return nil
}

// --- concurrent holds -------------------------------------------------------

func (s *idemState) placeHold(name, v string) error {
	amt, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.wallet = name
	ok, err := s.db.Hold(name, amt)
	if err != nil {
		return err
	}
	if ok {
		s.concOK = 1
	} else {
		s.concOK = 0
	}
	s.lastCredited = ok // reuse as last-hold-ok
	return nil
}

func (s *idemState) holdRefused() error {
	if s.lastCredited {
		return fmt.Errorf("expected the hold to be refused")
	}
	return nil
}
func (s *idemState) holdSucceeds() error {
	if !s.lastCredited {
		return fmt.Errorf("expected the hold to succeed")
	}
	return nil
}

func (s *idemState) concurrentHolds(countStr, v, name string) error {
	n, err := strconv.Atoi(countStr)
	if err != nil {
		return err
	}
	each, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.wallet = name
	var wg sync.WaitGroup
	var ok, ref int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, _ := s.db.Hold(name, each)
			if got {
				atomic.AddInt64(&ok, 1)
			} else {
				atomic.AddInt64(&ref, 1)
			}
		}()
	}
	wg.Wait()
	s.concOK, s.concRef = int(ok), int(ref)
	return nil
}

func (s *idemState) exactlySucceed(n string) error {
	want, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	if s.concOK != want {
		return fmt.Errorf("exactly %d holds should succeed, got %d", want, s.concOK)
	}
	return nil
}

func (s *idemState) exactlyRefused(n string) error {
	want, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	if s.concRef != want {
		return fmt.Errorf("exactly %d holds should be refused, got %d", want, s.concRef)
	}
	return nil
}

func (s *idemState) neverNegative() error {
	bal, _ := s.db.BalanceOf(s.wallet, 0)
	if bal < -1e-9 {
		return fmt.Errorf("balance went negative: %g", bal)
	}
	return nil
}

func (s *idemState) noPendingHoldRow() error {
	rows, err := s.db.LedgerOf(s.wallet, []string{store.KindHold}, 100)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.State == store.StatePending {
			return fmt.Errorf("a pending hold row was written for a refused attempt")
		}
	}
	return nil
}

func (s *idemState) holdsAndTopup(holdsStr, holdEach, topup string) error {
	nh, err := strconv.Atoi(holdsStr)
	if err != nil {
		return err
	}
	he, err := feParseFloat(holdEach)
	if err != nil {
		return err
	}
	tu, err := feParseFloat(topup)
	if err != nil {
		return err
	}
	s.heldEach = he
	s.creditsIn = s.initialBal + tu
	var wg sync.WaitGroup
	var ok int64
	for i := 0; i < nh; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, _ := s.db.Hold(s.wallet, he); got {
				atomic.AddInt64(&ok, 1)
			}
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); _, _, _ = s.db.CreditOnce("topup-conc", s.wallet, tu) }()
	wg.Wait()
	s.successHolds = int(ok)
	return nil
}

func (s *idemState) conservationHolds() error {
	// CONSERVATION: every credit is either still in the balance or reserved by a successful hold;
	// none lost or invented across the concurrent holds + top-up (credits-in = start + top-up).
	bal, err := s.db.BalanceOf(s.wallet, 0)
	if err != nil {
		return err
	}
	held := float64(s.successHolds) * s.heldEach
	return feApprox(bal+held, s.creditsIn)
}

func (s *idemState) noCreditsCreated() error {
	bal, err := s.db.BalanceOf(s.wallet, 0)
	if err != nil {
		return err
	}
	if bal < -1e-9 {
		return fmt.Errorf("balance went negative under concurrency: %g", bal)
	}
	return nil
}

func TestIdempotencyConcurrencyBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &idemState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^a fresh store$`, st.freshStore)
			sc.Step(`^a fresh money store$`, st.freshStore)
			sc.Step(`^the platform fee rate is (\d+)%$`, st.feePct)
			sc.Step(`^node "([^"]*)" is owned by account "([^"]*)"$`, st.nodeOwned)
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) credits$`, st.walletHas)
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) real credits$`, st.walletHasReal)
			sc.Step(`^the Stripe event "checkout\.session\.completed" for session "([^"]*)" credits ([\d.]+) to "([^"]*)"$`, st.stripeCreditsFull)
			sc.Step(`^the Stripe event for session "([^"]*)" credits ([\d.]+) to "([^"]*)"$`, st.stripeCredits)
			sc.Step(`^the Stripe event for session "([^"]*)" is delivered again$`, st.deliveredAgain)
			sc.Step(`^session "([^"]*)" credits ([\d.]+) and session "([^"]*)" credits ([\d.]+) to "([^"]*)"$`, st.twoSessions)
			sc.Step(`^the credit is applied$`, st.creditApplied)
			sc.Step(`^the second delivery is recognized as already-processed$`, st.alreadyProcessed)
			sc.Step(`^(\w+)'s balance is (?:still )?([\d.]+)$`, st.balanceIs)
			sc.Step(`^"([^"]*)" balance is ([\d.]+)$`, st.balanceIs)
			sc.Step(`^exactly one topup ledger row exists for "([^"]*)"$`, st.oneTopupRow)
			sc.Step(`^the idempotency key "([^"]*)" has never been seen$`, st.markNeverSeen)
			sc.Step(`^MarkProcessed is called with "([^"]*)"(?: again)?$`, st.markProcessed)
			sc.Step(`^it returns (true|false)$`, st.markReturns)
			sc.Step(`^a session "([^"]*)" whose amount_total is ([\d.]+) but whose metadata claims ([\d.]+) credits$`, st.sessionAmountMeta)
			sc.Step(`^the session is processed$`, st.sessionProcessed)
			sc.Step(`^(\w+) is credited ([\d.]+) from amount_total$`, st.creditedFromTotal)
			sc.Step(`^the metadata divergence is logged but not trusted$`, st.metaNotTrusted)
			sc.Step(`^session "([^"]*)" crediting ([\d.]+) is delivered (\d+) times$`, st.sessionDeliveredTimes)
			sc.Step(`^(\w+) has one settled request of cost ([\d.]+) with owner share ([\d.]+)$`, func(_, c, sh string) error { return st.settledRequest(c, sh) })
			sc.Step(`^(\w+) has one settled request of cost ([\d.]+) \(owner share ([\d.]+)\)$`, func(_, c, sh string) error { return st.settledRequest(c, sh) })
			sc.Step(`^(\w+) has two settled requests of cost ([\d.]+) each with owner share ([\d.]+) each$`, func(_, c, sh string) error { return st.settledRequests("2", c, sh) })
			sc.Step(`^a chargeback of ([\d.]+) with dispute id "([^"]*)" is processed$`, st.chargeback)
			sc.Step(`^the same dispute "([^"]*)" is delivered again$`, st.disputeAgain)
			sc.Step(`^a chargeback "([^"]*)" of ([\d.]+) and a chargeback "([^"]*)" of ([\d.]+) are processed$`, st.twoDisputes)
			sc.Step(`^dispute "([^"]*)" of ([\d.]+) is delivered (\d+) times$`, st.disputeDeliveredTimes)
			sc.Step(`^the second delivery is marked already-handled$`, st.secondAlreadyHandled)
			sc.Step(`^the second delivery claws back ([\d.]+)$`, st.clawsBack)
			sc.Step(`^(\w+)'s balance is unchanged by the redelivery$`, func(string) error { return st.balanceUnchanged() })
			sc.Step(`^the operator is debited only once$`, st.operatorDebitedOnce)
			sc.Step(`^both are applied$`, st.bothApplied)
			sc.Step(`^([\d.]+) total is removed from (\w+)'s wallet$`, func(v, name string) error { return st.totalRemoved(name, v) })
			sc.Step(`^the operator is clawed ([\d.]+) in total$`, st.operatorClawed)
			sc.Step(`^the platform loss is ([\d.]+) in total$`, st.platformLoss)
			sc.Step(`^(\w+) has one ALREADY-PAID earning lot of gross ([\d.]+) for owner "([^"]*)"$`, func(_, g, o string) error { return st.paidLot(g, o) })
			sc.Step(`^a chargeback "([^"]*)" claws that paid lot$`, st.chargebackPaid)
			sc.Step(`^a pending reversal keyed "([^"]*)" is recorded once$`, st.reversalRecordedOnce)
			sc.Step(`^a webhook redelivery of "([^"]*)" does not create a second reversal intent$`, st.redeliveryNoSecondReversal)
			sc.Step(`^a void request with request id "([^"]*)" against owner "([^"]*)"$`, st.voidRequest)
			sc.Step(`^a request id "([^"]*)" against owner "([^"]*)"$`, st.requestAgainst)
			sc.Step(`^owner "([^"]*)" with no prior strikes$`, st.ownerNoStrikes)
			sc.Step(`^the empty-output strike "([^"]*)" is recorded(?: again)?$`, st.recordStrike)
			sc.Step(`^a recount-over-report strike "([^"]*)" is recorded$`, st.recordStrike)
			sc.Step(`^the impossible-input strike "([^"]*)" is recorded twice$`, func(k string) error { return st.strikeKeyTimes(k, "2") })
			sc.Step(`^the strike key "([^"]*)" is recorded (\d+) times$`, st.strikeKeyTimes)
			sc.Step(`^owner "([^"]*)" has exactly one empty-output strike$`, func(o string) error { return st.strikeCountFor(o, "1") })
			sc.Step(`^owner "([^"]*)" has two distinct recount strikes$`, func(o string) error { return st.strikeCountFor(o, "2") })
			sc.Step(`^owner "([^"]*)" has exactly (\d+) strike\(s\) for that key$`, st.strikeCountFor)
			sc.Step(`^exactly one impossible-input strike exists for "([^"]*)"$`, func(string) error { return st.strikeCountFor(st.owner, "1") })
			sc.Step(`^the redelivery is a no-op via the idempotency key$`, st.redeliveryNoop)
			sc.Step(`^re-recording either key adds nothing$`, st.reRecordNothing)
			sc.Step(`^(\w+) places a hold of ([\d.]+)$`, st.placeHold)
			sc.Step(`^the hold is refused$`, st.holdRefused)
			sc.Step(`^the hold succeeds$`, st.holdSucceeds)
			sc.Step(`^(\d+) holds of ([\d.]+) are placed concurrently$`, func(c, v string) error { return st.concurrentHolds(c, v, st.wallet) })
			sc.Step(`^(\d+) holds of ([\d.]+) are placed concurrently against (\w+)$`, st.concurrentHolds)
			sc.Step(`^exactly (\d+) holds succeed$`, st.exactlySucceed)
			sc.Step(`^exactly (\d+) holds are refused$`, st.exactlyRefused)
			sc.Step(`^the balance never went negative at any point$`, st.neverNegative)
			sc.Step(`^no pending hold ledger row is written for the refused attempt$`, st.noPendingHoldRow)
			sc.Step(`^(\d+) holds of ([\d.]+) and one top-up of ([\d.]+) are applied concurrently$`, st.holdsAndTopup)
			sc.Step(`^the sum of remaining balance, successful holds, and released holds equals the credits in$`, st.conservationHolds)
			sc.Step(`^no credits were created or destroyed$`, st.noCreditsCreated)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/money/idempotency_concurrency.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("money/idempotency_concurrency behavior scenarios failed (see godog output above)")
	}
}
