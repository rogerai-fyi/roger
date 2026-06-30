package main

// cost_precision_bdd_test.go makes features/money/cost_precision.feature EXECUTABLE, driving the
// real cost arithmetic (protocol.UsageReceipt.Cost/CostWith2), the fee split, round6 (the
// balance/quality grid), fmtCostHeader (the exact X-RogerAI-Cost header), client.FormatUSD (the
// canonical consumer renderer dollars() delegates to), and ModelOffer.ActivePrice (the free
// time-of-use window). feApprox/feParseFloat live in fee_splits_bdd_test.go (same package).

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type costPrecState struct {
	prompt, completion       int
	priceIn, priceOut        float64
	brokerPrompt, brokerComp int
	cost, costAlt            float64
	feeRate                  float64
	ownerShare, platformTake float64
	raw, round6Out           float64
	captured                 float64
	activeOut                float64
}

func (s *costPrecState) reset() { *s = costPrecState{} }

func (s *costPrecState) receiptTokens(p, c string) error {
	pp, err := strconv.Atoi(p)
	if err != nil {
		return err
	}
	cc, err := strconv.Atoi(c)
	if err != nil {
		return err
	}
	s.prompt, s.completion = pp, cc
	return nil
}

func (s *costPrecState) prices(in, out string) error {
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

func (s *costPrecState) receiptCompletionAtPrice(c, out string) error {
	cc, err := strconv.Atoi(c)
	if err != nil {
		return err
	}
	po, err := feParseFloat(out)
	if err != nil {
		return err
	}
	s.prompt, s.completion, s.priceIn, s.priceOut = 0, cc, 0, po
	return nil
}

func (s *costPrecState) receiptCompletionOnly(c string) error {
	cc, err := strconv.Atoi(c)
	if err != nil {
		return err
	}
	s.prompt, s.completion = 0, cc
	return nil
}

func (s *costPrecState) rec() protocol.UsageReceipt {
	return protocol.UsageReceipt{PromptTokens: s.prompt, CompletionTokens: s.completion, PriceIn: s.priceIn, PriceOut: s.priceOut}
}

func (s *costPrecState) computeCost() error {
	s.cost = s.rec().CostWith2(s.prompt, s.completion)
	return nil
}

func (s *costPrecState) computeBoth() error {
	r := s.rec()
	s.cost = r.Cost()
	s.costAlt = r.CostWith2(s.prompt, s.completion)
	return nil
}

func (s *costPrecState) claimTokens(p, c string) error { return s.receiptTokens(p, c) }

func (s *costPrecState) brokerRecounts(p, c string) error {
	pp, err := strconv.Atoi(p)
	if err != nil {
		return err
	}
	cc, err := strconv.Atoi(c)
	if err != nil {
		return err
	}
	s.brokerPrompt, s.brokerComp = pp, cc
	return nil
}

func (s *costPrecState) computeWithBrokerCounts() error {
	s.cost = s.rec().CostWith2(s.brokerPrompt, s.brokerComp)
	return nil
}

func (s *costPrecState) claimIgnored() error { return nil }

func (s *costPrecState) costInCreditsIs(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.cost, want)
}

func (s *costPrecState) bothEqual(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	if err := feApprox(s.cost, want); err != nil {
		return fmt.Errorf("Cost: %w", err)
	}
	if err := feApprox(s.costAlt, want); err != nil {
		return fmt.Errorf("CostWith2: %w", err)
	}
	return feApprox(s.cost, s.costAlt)
}

func (s *costPrecState) walletDebitedFull(v string) error {
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

func (s *costPrecState) headerSendsExact(v string) error {
	if got := fmtCostHeader(s.cost); got != v {
		return fmt.Errorf("X-RogerAI-Cost = %q, expected %q (cost=%.12g)", got, v, s.cost)
	}
	return nil
}

func (s *costPrecState) settledCost(v string) error {
	c, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.cost = c
	return nil
}

func (s *costPrecState) feeRateDecimal(v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.feeRate = f
	return nil
}

func (s *costPrecState) splitCost() error {
	s.ownerShare = s.cost * (1 - s.feeRate)
	s.platformTake = s.cost - s.ownerShare
	return nil
}

func (s *costPrecState) ownerShareTimes(v string) error {
	c, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.ownerShare, c*(1-s.feeRate))
}

func (s *costPrecState) platformTimes(v string) error {
	c, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.platformTake, c*s.feeRate)
}

func (s *costPrecState) splitSumEquals(v string) error {
	c, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.ownerShare+s.platformTake, c)
}

func (s *costPrecState) neitherNegative() error {
	if s.ownerShare < -1e-12 || s.platformTake < -1e-12 {
		return fmt.Errorf("negative share: owner=%g platform=%g", s.ownerShare, s.platformTake)
	}
	return nil
}

func (s *costPrecState) rawValue(v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.raw = f
	return nil
}

func (s *costPrecState) applyRound6() error { s.round6Out = round6(s.raw); return nil }

func (s *costPrecState) round6Returns(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.round6Out, want)
}

func (s *costPrecState) capturedCost(v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.captured = f
	return nil
}

func (s *costPrecState) renderForConsumer() error { return nil }

func (s *costPrecState) displayReads(want string) error {
	if got := client.FormatUSD(s.captured); got != want {
		return fmt.Errorf("FormatUSD(%.12g) = %q, expected %q", s.captured, got, want)
	}
	return nil
}

func (s *costPrecState) neverReadsZeroForCharge() error {
	if s.captured > 0 && client.FormatUSD(s.captured) == "$0.00" {
		return fmt.Errorf("nonzero charge %.12g rendered as $0.00", s.captured)
	}
	return nil
}

func (s *costPrecState) noHoldNoLedger() error { return nil }

func (s *costPrecState) offerFreeWindow(out string) error {
	po, err := feParseFloat(out)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	offer := protocol.ModelOffer{PriceOut: po, Schedule: []protocol.PriceWindow{{
		Start: now.Add(-time.Hour).Format("15:04"),
		End:   now.Add(time.Hour).Format("15:04"),
		Free:  true,
	}}}
	_, s.activeOut, _, _ = offer.ActivePrice(now)
	s.priceIn, s.priceOut = 0, s.activeOut
	return nil
}

func (s *costPrecState) resolveAndCompute() error {
	s.cost = s.rec().CostWith2(s.prompt, s.completion)
	return nil
}

func (s *costPrecState) activePriceIsZero() error { return feApprox(s.activeOut, 0) }

func TestCostPrecisionBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &costPrecState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^a receipt with (\d+) prompt tokens and (\d+) completion tokens$`, st.receiptTokens)
			sc.Step(`^price_in ([\d.]+) per 1M and price_out ([\d.]+) per 1M$`, st.prices)
			sc.Step(`^a receipt with (\d+) completion tokens at price_out ([\d.]+) per 1M$`, st.receiptCompletionAtPrice)
			sc.Step(`^a receipt with (\d+) completion tokens$`, st.receiptCompletionOnly)
			sc.Step(`^the cost is computed$`, st.computeCost)
			sc.Step(`^the cost is computed both via Cost and via CostWith2 with the same counts$`, st.computeBoth)
			sc.Step(`^a receipt claiming (\d+) prompt tokens and (\d+) completion tokens$`, st.claimTokens)
			sc.Step(`^the broker re-counts (\d+) prompt tokens and (\d+) completion tokens$`, st.brokerRecounts)
			sc.Step(`^the cost is computed via CostWith2 with the broker counts$`, st.computeWithBrokerCounts)
			sc.Step(`^the node's inflated claim is ignored$`, st.claimIgnored)
			sc.Step(`^the (?:captured )?cost in credits is ([\d.]+)$`, st.costInCreditsIs)
			sc.Step(`^both results are equal to ([\d.]+)$`, st.bothEqual)
			sc.Step(`^the wallet is debited the full ([\d.]+) credits$`, st.walletDebitedFull)
			sc.Step(`^the X-RogerAI-Cost header sends ([\d.]+) exactly(?: \(NOT round6'd to a bare 0\))?$`, st.headerSendsExact)
			sc.Step(`^a settled cost of ([\d.]+) credits$`, st.settledCost)
			sc.Step(`^a platform fee rate of ([\d.]+)$`, st.feeRateDecimal)
			sc.Step(`^the cost is split$`, st.splitCost)
			sc.Step(`^the owner share is ([\d.]+) times one-minus-fee$`, st.ownerShareTimes)
			sc.Step(`^the platform take is ([\d.]+) times fee$`, st.platformTimes)
			sc.Step(`^owner share plus platform take equals ([\d.]+)$`, st.splitSumEquals)
			sc.Step(`^neither share is negative$`, st.neitherNegative)
			sc.Step(`^a raw value of ([\d.]+) credits$`, st.rawValue)
			sc.Step(`^round6 is applied$`, st.applyRound6)
			sc.Step(`^round6 returns ([\d.]+)$`, st.round6Returns)
			sc.Step(`^a captured cost of ([\d.]+) credits$`, st.capturedCost)
			sc.Step(`^the cost is rendered for the consumer \(dollars\(\)\)$`, st.renderForConsumer)
			sc.Step(`^the displayed cost reads "([^"]*)"$`, st.displayReads)
			sc.Step(`^it never reads "\$0\.00" for a nonzero charge$`, st.neverReadsZeroForCharge)
			sc.Step(`^no hold is placed and no ledger money rows are written$`, st.noHoldNoLedger)
			sc.Step(`^an offer with base price_out ([\d.]+) per 1M and a FREE schedule window active now$`, st.offerFreeWindow)
			sc.Step(`^the active price is resolved and the cost computed$`, st.resolveAndCompute)
			sc.Step(`^the active price is 0$`, st.activePriceIsZero)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/money/cost_precision.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("money/cost_precision behavior scenarios failed (see godog output above)")
	}
}
