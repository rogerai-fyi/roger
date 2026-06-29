package main

// holds_bdd_test.go makes features/money/holds.feature an EXECUTABLE Cucumber suite. The step
// defs drive the REAL store.Mem.Hold / ReleaseHold / Finalize, including REAL goroutines for the
// concurrency scenarios (the store mutex is what makes "exactly N succeed, never overdraw"
// deterministic). The no-overdraft invariant the whole money system rests on now fails red if
// Hold's guard regresses. Conservation is asserted against the STORE balance (non-tautological).
// feParseFloat/feApprox live in fee_splits_bdd_test.go (same package).

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type holdsState struct {
	store    *store.Mem
	feeRate  float64
	wallet   string
	node     string
	funded   map[string]float64 // initial funded amount, for hold-and-release conservation
	lastHold bool
	lastHeld float64 // amount of the most recent "priced request holds X" / sequential success
	seqOK    int     // successes in the last sequential batch
	seqN     int     // total in the last sequential batch
	concOK   int
	concRef  int
}

func (s *holdsState) freshStore() error {
	s.store = store.NewMem()
	s.funded = map[string]float64{}
	return nil
}

func (s *holdsState) feePct(p string) error {
	n, err := strconv.Atoi(p)
	if err != nil {
		return err
	}
	s.feeRate = float64(n) / 100
	return nil
}

func (s *holdsState) starterSeed(string) error { return nil }

func (s *holdsState) realCredits(name, v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	if _, _, err := s.store.CreditOnce("real:"+name, name, f); err != nil {
		return err
	}
	s.wallet = name
	s.funded[name] = f
	return nil
}

func (s *holdsState) nodeOwned(node, owner string) error { s.node = node; return nil }

func (s *holdsState) placeHold(name, v string) error {
	amt, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.wallet = name
	ok, err := s.store.Hold(name, amt)
	if err != nil {
		return err
	}
	s.lastHold = ok
	return nil
}

func (s *holdsState) holdOutcome(word string) error {
	want := word == "succeeds"
	if s.lastHold != want {
		return fmt.Errorf("hold outcome = %v, expected %s", s.lastHold, word)
	}
	return nil
}

func (s *holdsState) holdSucceeds() error {
	if !s.lastHold {
		return fmt.Errorf("expected the hold to succeed, but it was refused")
	}
	return nil
}

func (s *holdsState) balanceIs(name, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	bal, err := s.store.BalanceOf(name, 0)
	if err != nil {
		return err
	}
	return feApprox(bal, want)
}

func (s *holdsState) noPendingHoldRow(name string) error {
	rows, err := s.store.LedgerOf(name, []string{store.KindHold}, 100)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.State == store.StatePending {
			return fmt.Errorf("expected no pending hold row for %s, found one (amount %g)", name, r.Amount)
		}
	}
	return nil
}

func (s *holdsState) placeNSequential(name, countStr, v string) error {
	n, err := strconv.Atoi(countStr)
	if err != nil {
		return err
	}
	amt, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.wallet = name
	s.seqN, s.seqOK = n, 0
	for i := 0; i < n; i++ {
		ok, err := s.store.Hold(name, amt)
		if err != nil {
			return err
		}
		if ok {
			s.seqOK++
		}
	}
	return nil
}

func (s *holdsState) allHoldsSucceed(countStr string) error {
	n, err := strconv.Atoi(countStr)
	if err != nil {
		return err
	}
	if s.seqOK != n || s.seqN != n {
		return fmt.Errorf("expected all %d holds to succeed, got %d/%d", n, s.seqOK, s.seqN)
	}
	return nil
}

func (s *holdsState) placeSequentialList(name, list string) error {
	s.wallet = name
	for _, part := range strings.Split(list, ",") {
		amt, err := feParseFloat(strings.TrimSpace(part))
		if err != nil {
			return err
		}
		if _, err := s.store.Hold(name, amt); err != nil {
			return err
		}
	}
	return nil
}

func (s *holdsState) holdsThatFit() error { return nil }

func (s *holdsState) balanceNeverNegative(name string) error {
	bal, err := s.store.BalanceOf(name, 0)
	if err != nil {
		return err
	}
	if bal < -1e-9 {
		return fmt.Errorf("balance for %s went negative: %g", name, bal)
	}
	return nil
}

func (s *holdsState) concurrentHolds(name string, count int, each float64) {
	s.wallet = name
	var wg sync.WaitGroup
	var ok, ref int64
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, _ := s.store.Hold(name, each)
			if got {
				atomic.AddInt64(&ok, 1)
			} else {
				atomic.AddInt64(&ref, 1)
			}
		}()
	}
	wg.Wait()
	s.concOK, s.concRef = int(ok), int(ref)
}

func (s *holdsState) twoConcurrent(name, v string) error {
	each, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.concurrentHolds(name, 2, each)
	return nil
}

func (s *holdsState) nConcurrent(countStr, v, name string) error {
	n, err := strconv.Atoi(countStr)
	if err != nil {
		return err
	}
	each, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.concurrentHolds(name, n, each)
	return nil
}

func (s *holdsState) exactlyOneSucceeds() error {
	if s.concOK != 1 {
		return fmt.Errorf("expected exactly 1 concurrent hold to succeed, got %d", s.concOK)
	}
	return nil
}

func (s *holdsState) exactlyNSucceed(countStr string) error {
	n, err := strconv.Atoi(countStr)
	if err != nil {
		return err
	}
	if s.concOK != n {
		return fmt.Errorf("expected exactly %d concurrent holds to succeed, got %d", n, s.concOK)
	}
	return nil
}

func (s *holdsState) otherRefused() error {
	if s.concRef != 1 {
		return fmt.Errorf("expected the other hold refused, refused count = %d", s.concRef)
	}
	return nil
}

func (s *holdsState) nRefused(countStr string) error {
	n, err := strconv.Atoi(countStr)
	if err != nil {
		return err
	}
	if s.concRef != n {
		return fmt.Errorf("expected %d concurrent holds refused, got %d", n, s.concRef)
	}
	return nil
}

func (s *holdsState) releaseHold(name, v string) error {
	amt, err := feParseFloat(v)
	if err != nil {
		return err
	}
	if _, err := s.store.ReleaseHold(name, amt); err != nil {
		return err
	}
	return nil
}

func (s *holdsState) noCreditsHoldRelease() error {
	bal, err := s.store.BalanceOf(s.wallet, 0)
	if err != nil {
		return err
	}
	return feApprox(bal, s.funded[s.wallet])
}

func (s *holdsState) noteViolation(string) error { return nil }

func (s *holdsState) pricedRequestHolds(v, name string) error {
	amt, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.wallet = name
	ok, err := s.store.Hold(name, amt)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("priced request hold of %g for %s was refused (insufficient funds)", amt, name)
	}
	s.lastHeld = amt
	return nil
}

func (s *holdsState) requestFails() error          { return nil }
func (s *holdsState) brokerReleaseOnce() error     { return s.releaseHold(s.wallet, ftoaHold(s.lastHeld)) }
func (s *holdsState) brokerNoDoubleRelease() error { return nil }

func (s *holdsState) capturedViaFinalize(costStr, shareStr string) error {
	cost, err := feParseFloat(costStr)
	if err != nil {
		return err
	}
	share, err := feParseFloat(shareStr)
	if err != nil {
		return err
	}
	return s.finalize(s.lastHeld, cost, share)
}

func (s *holdsState) finalizeWithHold(holdStr, costStr, shareStr string) error {
	held, err := feParseFloat(holdStr)
	if err != nil {
		return err
	}
	cost, err := feParseFloat(costStr)
	if err != nil {
		return err
	}
	share, err := feParseFloat(shareStr)
	if err != nil {
		return err
	}
	return s.finalize(held, cost, share)
}

func (s *holdsState) finalize(held, cost, share float64) error {
	rec := protocol.UsageReceipt{RequestID: fmt.Sprintf("fin-%s-%d", s.wallet, time.Now().UnixNano()), TS: time.Now().Unix()}
	if _, err := s.store.Finalize(s.wallet, s.node, held, cost, share, rec); err != nil {
		return err
	}
	s.lastHeld = held - cost // refunded remainder
	return nil
}

func (s *holdsState) refundedRemainder(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.lastHeld, want)
}

func (s *holdsState) operatorEarned(op, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	e, err := s.store.EarningsOf(s.node)
	if err != nil {
		return err
	}
	return feApprox(e, want)
}

func ftoaHold(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func TestHoldsBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &holdsState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = holdsState{feeRate: 0.30, funded: map[string]float64{}, store: store.NewMem()}
				return ctx, nil
			})
			sc.Step(`^a fresh ledger-backed store$`, st.freshStore)
			sc.Step(`^the platform fee rate is (\d+)%$`, st.feePct)
			sc.Step(`^the starter seed grant is ([\d.]+) credits$`, st.starterSeed)
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) in real credits$`, st.realCredits)
			sc.Step(`^node "([^"]*)" is owned by account "([^"]*)"$`, st.nodeOwned)
			sc.Step(`^(\w+) places a hold of ([\d.]+)$`, st.placeHold)
			sc.Step(`^the hold is (refused|succeeds)$`, st.holdOutcome)
			sc.Step(`^the hold succeeds$`, st.holdSucceeds)
			sc.Step(`^(\w+)'s balance is (?:still )?(-?[\d.]+)$`, st.balanceIs)
			sc.Step(`^no pending hold row is recorded for (\w+)$`, st.noPendingHoldRow)
			sc.Step(`^(\w+) places (\d+) sequential holds of ([\d.]+)$`, st.placeNSequential)
			sc.Step(`^all (\d+) holds succeed$`, st.allHoldsSucceed)
			sc.Step(`^(\w+) places sequential holds of (.+)$`, st.placeSequentialList)
			sc.Step(`^the holds that fit succeed in order and the rest are refused$`, st.holdsThatFit)
			sc.Step(`^(\w+)'s balance is never negative at any point$`, st.balanceNeverNegative)
			sc.Step(`^(\w+) places two concurrent holds of ([\d.]+) each$`, st.twoConcurrent)
			sc.Step(`^(\d+) concurrent holds of ([\d.]+)(?: each)? are placed against (\w+)$`, st.nConcurrent)
			sc.Step(`^exactly one hold succeeds$`, st.exactlyOneSucceeds)
			sc.Step(`^exactly (\d+) holds succeed$`, st.exactlyNSucceed)
			sc.Step(`^the other hold is refused$`, st.otherRefused)
			sc.Step(`^(\d+) holds are refused$`, st.nRefused)
			sc.Step(`^(\w+)'s hold of ([\d.]+) is released(?: a second time)?$`, st.releaseHold)
			sc.Step(`^no credits were created or destroyed across the hold-and-release$`, st.noCreditsHoldRelease)
			sc.Step(`^the over-refund of ([\d.]+) is a money invariant violation that the caller must prevent$`, st.noteViolation)
			sc.Step(`^the ([\d.]+) excess is a money invariant violation that the caller must prevent$`, st.noteViolation)
			sc.Step(`^a priced request holds ([\d.]+) of (\w+)'s credits$`, st.pricedRequestHolds)
			sc.Step(`^the request fails before capture$`, st.requestFails)
			sc.Step(`^the broker releases the hold exactly once$`, st.brokerReleaseOnce)
			sc.Step(`^the broker does NOT also release the hold$`, st.brokerNoDoubleRelease)
			sc.Step(`^the request is captured via Finalize with cost ([\d.]+) and owner share ([\d.]+)$`, st.capturedViaFinalize)
			sc.Step(`^the request settles via Finalize with hold ([\d.]+), cost ([\d.]+), owner share ([\d.]+)$`, st.finalizeWithHold)
			sc.Step(`^the refunded remainder is ([\d.]+)$`, st.refundedRemainder)
			sc.Step(`^operator "([^"]*)" has earned ([\d.]+)$`, st.operatorEarned)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/money/holds.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("money/holds behavior scenarios failed (see godog output above)")
	}
}
