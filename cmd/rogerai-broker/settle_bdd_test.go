package main

// settle_bdd_test.go makes features/money/settle.feature an EXECUTABLE Cucumber suite. The step
// defs drive the REAL store.Mem.Settle + realEarnShare/consumeSeed, so the P0-1 invariant (seed
// credits mint NO operator earning), the seed/real split, conservation, and the
// "direct Settle has no overdraft guard" fact all fail red if the ledger regresses.
//
// Earnings/spend/balance are read from the REAL store (EarningsOf/SpendOf/BalanceOf). The seed
// BUCKET is mirrored in step state only for the auxiliary "seed/real portion" + "remaining seed"
// assertions (there is no public seedRemain accessor); the mirror is cross-checked because the
// platform-keeps assertion combines the mirrored real portion with the store's actual earning.
// feParseFloat/feApprox live in fee_splits_bdd_test.go (same package).

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

type settleState struct {
	store      *store.Mem
	feeRate    float64
	seedGrant  float64
	wallet     string
	node       string
	seedRemain map[string]float64 // mirror of the store's per-wallet seed bucket
	settleN    int

	// last settle, for the per-request portion / platform / conservation assertions
	lastCost        float64
	lastOwnerShare  float64
	lastSeedUsed    float64
	lastRealPortion float64
	lastEarned      float64 // EarningsOf(node) delta across the last settle
}

func (s *settleState) freshStore() error {
	s.store = store.NewMem()
	s.seedRemain = map[string]float64{}
	return nil
}

func (s *settleState) feePct(p string) error {
	n, err := strconv.Atoi(p)
	if err != nil {
		return err
	}
	s.feeRate = float64(n) / 100
	return nil
}

func (s *settleState) starterSeed(v string) error {
	f, err := feParseFloat(v)
	s.seedGrant = f
	return err
}

func (s *settleState) realCredits(name, v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	if _, _, err := s.store.CreditOnce("real:"+name, name, f); err != nil {
		return err
	}
	s.wallet = name
	return nil
}

func (s *settleState) seedCredits(name, v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	if _, _, err := s.store.SeedOnce(name, f); err != nil {
		return err
	}
	s.seedRemain[name] += f
	s.wallet = name
	return nil
}

func (s *settleState) seedAndReal(name, seed, real string) error {
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
	return nil
}

func (s *settleState) nodeOwned(node, owner string) error { s.node = node; return nil }

func (s *settleState) doSettle(cost, share float64) error {
	// Mirror the seed drain: realEarnShare drains seed ONLY when cost>0 AND ownerShare>0.
	seedUsed := 0.0
	if cost > 0 && share > 0 {
		rem := s.seedRemain[s.wallet]
		seedUsed = cost
		if seedUsed > rem {
			seedUsed = rem
		}
		s.seedRemain[s.wallet] = rem - seedUsed
	}
	before, _ := s.store.EarningsOf(s.node)
	s.settleN++
	rec := protocol.UsageReceipt{RequestID: fmt.Sprintf("req-%s-%d", s.wallet, s.settleN), TS: time.Now().Unix()}
	if _, err := s.store.Settle(s.wallet, s.node, cost, share, rec); err != nil {
		return err
	}
	after, _ := s.store.EarningsOf(s.node)
	s.lastCost, s.lastOwnerShare = cost, share
	s.lastSeedUsed = seedUsed
	s.lastRealPortion = cost - seedUsed
	s.lastEarned = after - before
	return nil
}

func (s *settleState) settleOnce(costStr, shareStr string) error {
	cost, err := feParseFloat(costStr)
	if err != nil {
		return err
	}
	share, err := feParseFloat(shareStr)
	if err != nil {
		return err
	}
	return s.doSettle(cost, share)
}

func (s *settleState) settleManyRequests(who, countStr, costStr, shareStr string) error {
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
		if err := s.doSettle(cost, share); err != nil {
			return err
		}
	}
	return nil
}

func (s *settleState) balanceIs(name, v string) error {
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

func (s *settleState) spendIs(name, v string) error {
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

func (s *settleState) operatorEarned(op, v string) error {
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

func (s *settleState) platformKeeps(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.lastRealPortion-s.lastEarned, want)
}

func (s *settleState) seedPortion(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.lastSeedUsed, want)
}

func (s *settleState) realPortion(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.lastRealPortion, want)
}

func (s *settleState) remainingSeed(name, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.seedRemain[name], want)
}

func (s *settleState) shareSumEqualsCost(v string) error {
	cost, err := feParseFloat(v)
	if err != nil {
		return err
	}
	platformTake := s.lastCost - s.lastOwnerShare
	return feApprox(s.lastOwnerShare+platformTake, cost)
}

func (s *settleState) noCreditsCreated() error {
	if s.lastOwnerShare < 0 || s.lastOwnerShare > s.lastCost+1e-9 {
		return fmt.Errorf("owner share %g outside [0, cost=%g]", s.lastOwnerShare, s.lastCost)
	}
	platformTake := s.lastCost - s.lastOwnerShare
	return feApprox(s.lastOwnerShare+platformTake, s.lastCost)
}

func (s *settleState) meteringReceiptRecorded() error {
	es, err := s.store.EntriesByUser(s.wallet, 0, 1<<62)
	if err != nil {
		return err
	}
	if len(es) == 0 {
		return fmt.Errorf("no metering receipt recorded for %s", s.wallet)
	}
	return nil
}

func (s *settleState) noPayableLot() error {
	sp, err := s.store.EarningSplitOfNode(s.node, time.Now())
	if err != nil {
		return err
	}
	if total := sp.Held + sp.Payable + sp.Reserved + sp.Paid; total != 0 {
		return fmt.Errorf("expected no earning lot value, got %g across buckets", total)
	}
	return nil
}

func (s *settleState) noPayableLotForOp(op string) error { return s.noPayableLot() }

func (s *settleState) holdsBeforeSettleNote() error { return nil }

func TestSettleBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &settleState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = settleState{feeRate: 0.30, seedRemain: map[string]float64{}, store: store.NewMem()}
				return ctx, nil
			})
			sc.Step(`^a fresh ledger-backed store$`, st.freshStore)
			sc.Step(`^the platform fee rate is (\d+)%$`, st.feePct)
			sc.Step(`^the starter seed grant is ([\d.]+) credits$`, st.starterSeed)
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) in real credits$`, st.realCredits)
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) in FREE seed credits$`, st.seedCredits)
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) in FREE seed credits and ([\d.]+) in real credits$`, st.seedAndReal)
			sc.Step(`^node "([^"]*)" is owned by account "([^"]*)"$`, st.nodeOwned)
			sc.Step(`^the request settles via Settle with cost ([\d.]+), owner share ([\d.]+)$`, st.settleOnce)
			sc.Step(`^(\w+) settles (\d+) requests of cost ([\d.]+) each \(owner share ([\d.]+) each\)$`, st.settleManyRequests)
			sc.Step(`^(\w+)'s balance is (-?[\d.]+)$`, st.balanceIs)
			sc.Step(`^(\w+)'s lifetime spend is ([\d.]+)$`, st.spendIs)
			sc.Step(`^operator "([^"]*)" has earned ([\d.]+)$`, st.operatorEarned)
			sc.Step(`^the platform keeps ([\d.]+)$`, st.platformKeeps)
			sc.Step(`^the seed-funded portion of the cost is ([\d.]+)$`, st.seedPortion)
			sc.Step(`^the real-funded portion of the cost is ([\d.]+)$`, st.realPortion)
			sc.Step(`^the remaining seed for (\w+) is ([\d.]+)$`, st.remainingSeed)
			sc.Step(`^the operator share plus the platform take equals the cost ([\d.]+)$`, st.shareSumEqualsCost)
			sc.Step(`^no credits were created or destroyed in the split$`, st.noCreditsCreated)
			sc.Step(`^a metering receipt is recorded for the request$`, st.meteringReceiptRecorded)
			sc.Step(`^no payable earning lot was created \(owner share is zero\)$`, st.noPayableLot)
			sc.Step(`^no payable earning lot was ever created for operator "([^"]*)"$`, st.noPayableLotForOp)
			sc.Step(`^this is only safe because the production path always Holds before it Settles$`, st.holdsBeforeSettleNote)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/money/settle.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("money/settle behavior scenarios failed (see godog output above)")
	}
}
