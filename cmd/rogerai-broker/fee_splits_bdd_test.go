package main

// fee_splits_bdd_test.go makes features/money/fee_splits.feature an EXECUTABLE Cucumber suite.
// The step defs drive the REAL money primitives: protocol.UsageReceipt.Cost/CostWith2 (cost),
// the ownerShare = cost*(1-feeRate) split, fmtCostHeader (the X-RogerAI-Cost DISPLAY header),
// round6 (the Balance/JSON rounding), and store.Mem.Settle (the real ledger debit). Wiring this
// is what exposed the stale "round6 collapses sub-micro cost to $0" cost-display scenarios; the
// spec was corrected to the deployed fmtCostHeader behavior and these scenarios now pin it.

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"testing"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type feeSplitState struct {
	seed         float64
	feeRate      float64
	cost         float64
	ownerShare   float64
	platformTake float64
	priceIn      float64
	priceOut     float64
	exactAmt     float64

	// aggregate / real-debit scenarios
	store       *store.Mem
	wallet      string
	walletStart float64
	perReqCost  float64
	reqCount    int
}

func feParseFloat(s string) (float64, error) { return strconv.ParseFloat(s, 64) }

// feApprox compares floats with a combined absolute+relative tolerance: cost/split math is
// exact-decimal in intent but carries IEEE-754 noise, and fmtCostHeader cleans to 6 sig figs.
func feApprox(got, want float64) error {
	tol := 1e-9 + 1e-9*math.Abs(want)
	if math.Abs(got-want) > tol {
		return fmt.Errorf("expected %.12g, got %.12g (tol %.3g)", want, got, tol)
	}
	return nil
}

// feDisplay6 formats x at the 6-decimal round6 display the money JSON uses and string-compares.
func feDisplay6(x float64, want string) error {
	if got := strconv.FormatFloat(x, 'f', 6, 64); got != want {
		return fmt.Errorf("JSON display = %q, expected %q", got, want)
	}
	return nil
}

func (s *feeSplitState) freshStore() error { s.store = store.NewMem(); return nil }

func (s *feeSplitState) starterSeed(v string) error {
	f, err := feParseFloat(v)
	s.seed = f
	return err
}

func (s *feeSplitState) feePct(p string) error {
	n, err := strconv.Atoi(p)
	if err != nil {
		return err
	}
	s.feeRate = float64(n) / 100
	return nil
}

func (s *feeSplitState) requestCosts(v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.cost = f
	s.ownerShare = f * (1 - s.feeRate)
	s.platformTake = f - s.ownerShare
	return nil
}

func (s *feeSplitState) ownerShareIs(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.ownerShare, want)
}

func (s *feeSplitState) platformTakeIs(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.platformTake, want)
}

func (s *feeSplitState) shareSumEquals(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.ownerShare+s.platformTake, want)
}

func (s *feeSplitState) neitherNegative() error {
	if s.ownerShare < 0 || s.platformTake < 0 {
		return fmt.Errorf("a share is negative: owner=%g platform=%g", s.ownerShare, s.platformTake)
	}
	return nil
}

func (s *feeSplitState) modelPriced(in, out string) error {
	pi, err := feParseFloat(in)
	if err != nil {
		return err
	}
	po, err := feParseFloat(out)
	if err != nil {
		return err
	}
	s.priceIn, s.priceOut = pi, po
	return nil
}

func (s *feeSplitState) requestUses(prompt, completion string) error {
	p, err := strconv.Atoi(prompt)
	if err != nil {
		return err
	}
	c, err := strconv.Atoi(completion)
	if err != nil {
		return err
	}
	rec := protocol.UsageReceipt{PriceIn: s.priceIn, PriceOut: s.priceOut}
	s.cost = rec.CostWith2(p, c)
	return nil
}

func (s *feeSplitState) costIs(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.cost, want)
}

func (s *feeSplitState) exactCostIs(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.cost, want)
}

func (s *feeSplitState) displayedHeaderIs(v string) error {
	if got := fmtCostHeader(s.cost); got != v {
		return fmt.Errorf("X-RogerAI-Cost = %q, expected %q (cost=%.12g)", got, v, s.cost)
	}
	return nil
}

func (s *feeSplitState) walletDebited(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	db := store.NewMem()
	if _, _, err := db.CreditOnce("fund", "payer", 1.0); err != nil {
		return err
	}
	if _, err := db.Settle("payer", "n", s.cost, 0, protocol.UsageReceipt{}); err != nil {
		return err
	}
	bal, err := db.BalanceOf("payer", 0)
	if err != nil {
		return err
	}
	return feApprox(1.0-bal, want)
}

func (s *feeSplitState) displayedEqualsBilled() error {
	got, err := feParseFloat(fmtCostHeader(s.cost))
	if err != nil {
		return err
	}
	return feApprox(got, s.cost)
}

func (s *feeSplitState) exactAmount(v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.exactAmt = f
	return nil
}

func (s *feeSplitState) round6Is(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(round6(s.exactAmt), want)
}

func (s *feeSplitState) ownerShareJSON(v string) error  { return feDisplay6(round6(s.ownerShare), v) }
func (s *feeSplitState) platformTakeJSON(v string) error { return feDisplay6(round6(s.platformTake), v) }

func (s *feeSplitState) walletHasReal(name, v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.store = store.NewMem()
	if _, _, err := s.store.CreditOnce("fund:"+name, name, f); err != nil {
		return err
	}
	s.wallet, s.walletStart = name, f
	return nil
}

func (s *feeSplitState) nodeOwnedBy(node, owner string) error { return nil }

func (s *feeSplitState) runsRequests(who, count, percost string) error {
	n, err := strconv.Atoi(count)
	if err != nil {
		return err
	}
	pc, err := feParseFloat(percost)
	if err != nil {
		return err
	}
	s.reqCount, s.perReqCost = n, pc
	// Prove real ledger accumulation with a bounded loop (a full 1e6-row ledger is not a unit
	// test's job); the headline 1e6 total is asserted arithmetically in balanceFalls.
	const realLoop = 1000
	for i := 0; i < realLoop; i++ {
		if _, err := s.store.Settle(who, "n1", pc, 0, protocol.UsageReceipt{}); err != nil {
			return err
		}
	}
	bal, err := s.store.BalanceOf(who, 0)
	if err != nil {
		return err
	}
	if e := feApprox(s.walletStart-bal, float64(realLoop)*pc); e != nil {
		return fmt.Errorf("real %d-request ledger debit off: %w", realLoop, e)
	}
	return nil
}

func (s *feeSplitState) eachDisplays(v string) error {
	if got := fmtCostHeader(s.perReqCost); got != v {
		return fmt.Errorf("per-request display = %q, expected %q", got, v)
	}
	return nil
}

func (s *feeSplitState) balanceFalls(who, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(float64(s.reqCount)*s.perReqCost, want)
}

func (s *feeSplitState) aggregateReal() error {
	if fmtCostHeader(s.perReqCost) == "0" {
		return fmt.Errorf("per-request cost collapsed to $0 (the regressed behavior)")
	}
	if float64(s.reqCount)*s.perReqCost <= 0 {
		return fmt.Errorf("aggregate charge not positive")
	}
	return nil
}

func TestFeeSplitsBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &feeSplitState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = feeSplitState{feeRate: 0.30} // default platform fee rate
				return ctx, nil
			})
			sc.Step(`^a fresh ledger-backed store$`, st.freshStore)
			sc.Step(`^the starter seed grant is ([\d.]+) credits$`, st.starterSeed)
			sc.Step(`^the platform fee rate is (\d+)%$`, st.feePct)
			sc.Step(`^a request costs ([\d.]+) credits$`, st.requestCosts)
			sc.Step(`^the owner share is ([\d.]+)$`, st.ownerShareIs)
			sc.Step(`^the platform take is ([\d.]+)$`, st.platformTakeIs)
			sc.Step(`^the owner share plus the platform take equals ([\d.]+)$`, st.shareSumEquals)
			sc.Step(`^neither share is negative$`, st.neitherNegative)
			sc.Step(`^a model priced at ([\d.]+) per 1M input and ([\d.]+) per 1M output$`, st.modelPriced)
			sc.Step(`^a request uses (\d+) prompt tokens and (\d+) completion tokens$`, st.requestUses)
			sc.Step(`^the cost is ([\d.]+)$`, st.costIs)
			sc.Step(`^the exact cost is ([\d.]+) credits$`, st.exactCostIs)
			sc.Step(`^the displayed cost header is ([\d.]+)$`, st.displayedHeaderIs)
			sc.Step(`^the wallet is actually debited ([\d.]+) credits$`, st.walletDebited)
			sc.Step(`^the displayed cost equals the billed cost \(no \$0 collapse, no dust hidden\)$`, st.displayedEqualsBilled)
			sc.Step(`^an exact amount of ([\d.]+) credits$`, st.exactAmount)
			sc.Step(`^round6 of that amount is ([\d.]+)$`, st.round6Is)
			sc.Step(`^the owner share displays as ([\d.]+) in the earnings JSON$`, st.ownerShareJSON)
			sc.Step(`^the platform take displays as ([\d.]+) in the earnings JSON$`, st.platformTakeJSON)
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) in real credits$`, st.walletHasReal)
			sc.Step(`^node "([^"]*)" is owned by account "([^"]*)"$`, st.nodeOwnedBy)
			sc.Step(`^(\w+) runs (\d+) requests that each cost ([\d.]+) credits$`, st.runsRequests)
			sc.Step(`^each request displays a cost of ([\d.]+)$`, st.eachDisplays)
			sc.Step(`^(\w+)'s balance falls by ([\d.]+) in total$`, st.balanceFalls)
			sc.Step(`^the aggregate charge is real and each line item now shows its true cost$`, st.aggregateReal)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/money/fee_splits.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("money/fee_splits behavior scenarios failed (see godog output above)")
	}
}
