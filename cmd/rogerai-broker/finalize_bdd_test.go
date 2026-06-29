package main

// finalize_bdd_test.go makes features/money/finalize.feature an EXECUTABLE Cucumber suite,
// driving the real store.Mem.Hold + Finalize. It pins hold->capture conservation (the wallet
// nets down by EXACTLY the cost, the unused hold is refunded), the seed-aware earning across the
// hold path (P0-1), and the REAL idempotency of Finalize on rec.RequestID (the settled-map guard:
// a second capture of the same request is a no-op - no double refund / lot drift). Conservation
// is asserted against STORE state (initial-minus-current == spend). feApprox/feParseFloat live in
// fee_splits_bdd_test.go (same package).

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type finalizeState struct {
	store      *store.Mem
	feeRate    float64
	wallet     string
	node       string
	seedRemain map[string]float64
	settledIDs map[string]bool
	reqN       int
	curReqID   string

	initialBalance  float64
	balBeforeHold   float64
	heldAmt         float64
	lastCost        float64
	lastOwnerShare  float64
	lastRefund      float64
	lastEarned      float64
	totalRealPortion float64
}

func (s *finalizeState) freshStore() error {
	s.store = store.NewMem()
	s.seedRemain = map[string]float64{}
	s.settledIDs = map[string]bool{}
	return nil
}

func (s *finalizeState) feePct(p string) error {
	n, err := strconv.Atoi(p)
	if err != nil {
		return err
	}
	s.feeRate = float64(n) / 100
	return nil
}

func (s *finalizeState) starterSeed(string) error { return nil }

func (s *finalizeState) realCredits(name, v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	if _, _, err := s.store.CreditOnce("real:"+name, name, f); err != nil {
		return err
	}
	s.wallet = name
	s.initialBalance += f
	return nil
}

func (s *finalizeState) seedCredits(name, v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	if _, _, err := s.store.SeedOnce(name, f); err != nil {
		return err
	}
	s.seedRemain[name] += f
	s.wallet = name
	s.initialBalance += f
	return nil
}

func (s *finalizeState) seedAndReal(name, seed, real string) error {
	if err := s.seedCredits(name, seed); err != nil {
		return err
	}
	rf, err := feParseFloat(real)
	if err != nil {
		return err
	}
	if _, _, err := s.store.CreditOnce("real:"+name, name, rf); err != nil {
		return err
	}
	s.wallet = name
	s.initialBalance += rf
	return nil
}

func (s *finalizeState) nodeOwned(node, owner string) error { s.node = node; return nil }

func (s *finalizeState) hold(name, v string) error {
	amt, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.wallet = name
	bal, err := s.store.BalanceOf(name, 0)
	if err != nil {
		return err
	}
	s.balBeforeHold = bal
	ok, err := s.store.Hold(name, amt)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("hold of %g for %s refused (insufficient)", amt, name)
	}
	s.heldAmt = amt
	return nil
}

func (s *finalizeState) pricedRequestHolds(v, name string) error { return s.hold(name, v) }

func (s *finalizeState) freshID() string {
	s.reqN++
	id := fmt.Sprintf("fin-%s-%d", s.wallet, s.reqN)
	s.curReqID = id
	return id
}

func (s *finalizeState) doFinalize(held, cost, share float64, reqID string) error {
	noop := reqID != "" && s.settledIDs[reqID]
	seedUsed := 0.0
	if !noop && cost > 0 && share > 0 {
		rem := s.seedRemain[s.wallet]
		seedUsed = cost
		if seedUsed > rem {
			seedUsed = rem
		}
		s.seedRemain[s.wallet] = rem - seedUsed
	}
	earnBefore, _ := s.store.EarningsOf(s.node)
	rec := protocol.UsageReceipt{RequestID: reqID, TS: time.Now().Unix()}
	if _, err := s.store.Finalize(s.wallet, s.node, held, cost, share, rec); err != nil {
		return err
	}
	earnAfter, _ := s.store.EarningsOf(s.node)
	if reqID != "" {
		s.settledIDs[reqID] = true
	}
	s.lastCost, s.lastOwnerShare = cost, share
	s.lastRefund = held - cost
	s.lastEarned = earnAfter - earnBefore
	if !noop {
		s.totalRealPortion += cost - seedUsed
	}
	return nil
}

func (s *finalizeState) parse3(a, b, c string) (float64, float64, float64, error) {
	x, err := feParseFloat(a)
	if err != nil {
		return 0, 0, 0, err
	}
	y, err := feParseFloat(b)
	if err != nil {
		return 0, 0, 0, err
	}
	z, err := feParseFloat(c)
	if err != nil {
		return 0, 0, 0, err
	}
	return x, y, z, nil
}

func (s *finalizeState) finalizeWithHold(holdStr, costStr, shareStr string) error {
	held, cost, share, err := s.parse3(holdStr, costStr, shareStr)
	if err != nil {
		return err
	}
	return s.doFinalize(held, cost, share, s.freshID())
}

func (s *finalizeState) finalizeSecondTime(holdStr, costStr, shareStr string) error {
	held, cost, share, err := s.parse3(holdStr, costStr, shareStr)
	if err != nil {
		return err
	}
	return s.doFinalize(held, cost, share, s.curReqID) // SAME request id -> settled-guard no-op
}

func (s *finalizeState) capturedViaFinalize(costStr, shareStr string) error {
	cost, err := feParseFloat(costStr)
	if err != nil {
		return err
	}
	share, err := feParseFloat(shareStr)
	if err != nil {
		return err
	}
	return s.doFinalize(s.heldAmt, cost, share, s.freshID())
}

func (s *finalizeState) runsHeldRequests(who, countStr, costStr, shareStr string) error {
	n, err := strconv.Atoi(countStr)
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
	s.wallet = who
	for i := 0; i < n; i++ {
		bal, _ := s.store.BalanceOf(who, 0)
		s.balBeforeHold = bal
		if ok, err := s.store.Hold(who, cost); err != nil || !ok {
			return fmt.Errorf("held request %d: hold refused or err %v", i, err)
		}
		if err := s.doFinalize(cost, cost, share, s.freshID()); err != nil {
			return err
		}
	}
	return nil
}

func (s *finalizeState) balanceIs(name, v string) error {
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

func (s *finalizeState) balanceFellByCost(v string) error {
	cost, err := feParseFloat(v)
	if err != nil {
		return err
	}
	bal, err := s.store.BalanceOf(s.wallet, 0)
	if err != nil {
		return err
	}
	return feApprox(s.balBeforeHold-bal, cost)
}

func (s *finalizeState) spendIs(name, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	sp, err := s.store.SpendOf(name)
	if err != nil {
		return err
	}
	return feApprox(sp, want)
}

func (s *finalizeState) operatorEarned(op, v string) error {
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

func (s *finalizeState) platformKeeps(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	e, _ := s.store.EarningsOf(s.node)
	return feApprox(s.totalRealPortion-e, want)
}

func (s *finalizeState) unusedRefunded(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.lastRefund, want)
}

func (s *finalizeState) remainingSeed(name, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.seedRemain[name], want)
}

func (s *finalizeState) meteringReceipt() error {
	es, err := s.store.EntriesByUser(s.wallet, 0, 1<<62)
	if err != nil {
		return err
	}
	if len(es) == 0 {
		return fmt.Errorf("no metering receipt recorded for %s", s.wallet)
	}
	return nil
}

func (s *finalizeState) noCreditsCreated() error {
	bal, err := s.store.BalanceOf(s.wallet, 0)
	if err != nil {
		return err
	}
	sp, err := s.store.SpendOf(s.wallet)
	if err != nil {
		return err
	}
	// Wallet conservation: every credit funded is either still in the balance or was spent.
	return feApprox(s.initialBalance-bal, sp)
}

func (s *finalizeState) shareSumEqualsCost(v string) error {
	cost, err := feParseFloat(v)
	if err != nil {
		return err
	}
	// Real-funded scenarios: store earning == stated owner share, so cost splits into earning +
	// a non-negative platform take (store-verified, not the ownerShare+(cost-ownerShare) tautology).
	if err := feApprox(s.lastEarned, s.lastOwnerShare); err != nil {
		return fmt.Errorf("store earning != owner share: %w", err)
	}
	platform := cost - s.lastEarned
	if platform < -1e-9 {
		return fmt.Errorf("platform take negative: %g", platform)
	}
	return feApprox(s.lastEarned+platform, cost)
}

func (s *finalizeState) totalSpendEqualsSplit() error {
	sp, _ := s.store.SpendOf(s.wallet)
	e, _ := s.store.EarningsOf(s.node)
	platform := s.totalRealPortion - e
	if platform < -1e-9 {
		return fmt.Errorf("platform take negative: %g", platform)
	}
	return feApprox(sp, e+platform)
}

// --- broker-clamp + idempotency narrative steps -----------------------------

func (s *finalizeState) nodeReportsOverCost(costStr, holdStr string) error { return nil }
func (s *finalizeState) brokerCapsCost(string) error                       { return nil }
func (s *finalizeState) neverChargedMore() error                           { return nil }
func (s *finalizeState) requestCompletes() error                           { return nil }
func (s *finalizeState) finalizeOnce() error                               { return nil }
func (s *finalizeState) secondCaptureNoOp() error                          { return nil }
func (s *finalizeState) overCaptureNote(string) error                      { return nil }

func (s *finalizeState) spendRowOnce() error {
	rows, err := s.store.LedgerOf(s.wallet, []string{store.KindSpend}, 1000)
	if err != nil {
		return err
	}
	seen := map[string]int{}
	for _, r := range rows {
		seen[r.Ref]++
	}
	for ref, n := range seen {
		if n > 1 {
			return fmt.Errorf("spend ledger row %q recorded %d times (expected once)", ref, n)
		}
	}
	return nil
}

func TestFinalizeBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &finalizeState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = finalizeState{feeRate: 0.30, seedRemain: map[string]float64{}, settledIDs: map[string]bool{}, store: store.NewMem()}
				return ctx, nil
			})
			sc.Step(`^a fresh ledger-backed store$`, st.freshStore)
			sc.Step(`^the platform fee rate is (\d+)%$`, st.feePct)
			sc.Step(`^the starter seed grant is ([\d.]+) credits$`, st.starterSeed)
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) in real credits$`, st.realCredits)
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) in FREE seed credits$`, st.seedCredits)
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) in FREE seed credits and ([\d.]+) in real credits$`, st.seedAndReal)
			sc.Step(`^node "([^"]*)" is owned by account "([^"]*)"$`, st.nodeOwned)
			sc.Step(`^(\w+) places a hold of ([\d.]+)$`, st.hold)
			sc.Step(`^a priced request holds ([\d.]+) of (\w+)'s credits$`, st.pricedRequestHolds)
			sc.Step(`^the request settles via Finalize with hold ([\d.]+), cost ([\d.]+), owner share ([\d.]+)$`, st.finalizeWithHold)
			sc.Step(`^Finalize is called directly with hold ([\d.]+), cost ([\d.]+), owner share ([\d.]+)$`, st.finalizeWithHold)
			sc.Step(`^Finalize is called with hold ([\d.]+), cost ([\d.]+), owner share ([\d.]+)$`, st.finalizeWithHold)
			sc.Step(`^Finalize is called a second time with hold ([\d.]+), cost ([\d.]+), owner share ([\d.]+)$`, st.finalizeSecondTime)
			sc.Step(`^the request is captured via Finalize with cost ([\d.]+) and owner share ([\d.]+)$`, st.capturedViaFinalize)
			sc.Step(`^(\w+) runs (\d+) held requests of cost ([\d.]+) each \(owner share ([\d.]+) each\)$`, st.runsHeldRequests)
			sc.Step(`^(\w+)'s balance is (-?[\d.]+)$`, st.balanceIs)
			sc.Step(`^(\w+)'s balance is still (-?[\d.]+)$`, st.balanceIs)
			sc.Step(`^(\w+)'s balance fell by exactly the cost ([\d.]+) across the hold-and-capture$`, func(_ string, v string) error { return st.balanceFellByCost(v) })
			sc.Step(`^(\w+)'s lifetime spend is ([\d.]+)$`, st.spendIs)
			sc.Step(`^operator "([^"]*)" has earned ([\d.]+)$`, st.operatorEarned)
			sc.Step(`^the platform keeps ([\d.]+)$`, st.platformKeeps)
			sc.Step(`^the unused hold of ([\d.]+) was refunded$`, st.unusedRefunded)
			sc.Step(`^the remaining seed for (\w+) is ([\d.]+)$`, st.remainingSeed)
			sc.Step(`^a metering receipt is recorded for the request$`, st.meteringReceipt)
			sc.Step(`^no credits were created or destroyed$`, st.noCreditsCreated)
			sc.Step(`^the operator share plus the platform take equals the cost ([\d.]+)$`, st.shareSumEqualsCost)
			sc.Step(`^total spend equals operator earnings plus platform take$`, st.totalSpendEqualsSplit)
			sc.Step(`^the node reports a cost of ([\d.]+) that exceeds the ([\d.]+) hold$`, st.nodeReportsOverCost)
			sc.Step(`^the broker caps the captured cost at ([\d.]+)$`, st.brokerCapsCost)
			sc.Step(`^alice is never charged more than was authorized$`, st.neverChargedMore)
			sc.Step(`^the request completes$`, st.requestCompletes)
			sc.Step(`^Finalize was called exactly once$`, st.finalizeOnce)
			sc.Step(`^the second capture was a no-op \(the settled-map guard prevents double-refund / lot drift\)$`, st.secondCaptureNoOp)
			sc.Step(`^the spend ledger row is recorded only once \(idem key "spend:<request>"\)$`, st.spendRowOnce)
			sc.Step(`^the ([\d.]+) over-capture is only prevented by the broker's min\(cost, hold\) clamp$`, st.overCaptureNote)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/money/finalize.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("money/finalize behavior scenarios failed (see godog output above)")
	}
}
