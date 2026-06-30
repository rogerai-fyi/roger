package main

// funding_bdd_test.go makes features/money/funding.feature EXECUTABLE, driving the real seed/real
// accounting: SeedOnce / BalanceOf (seed at most once, keyed idem), PeekBalance (never seeds),
// SetSeedLimit + SeedStatus (the free-credit liability cap, incl. concurrent first-seeds via real
// goroutines), AddCredits / CreditOnce (real top-ups that do NOT add to the free bucket), and
// Settle (seed drained before real; P0-1 seed never mints earnings). The seed bucket is mirrored
// in step state (no public seedRemain accessor) and kept honest by deriving each grant from the
// actual balance change. feApprox/feParseFloat live in fee_splits_bdd_test.go (same package).

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

type fundingState struct {
	store        *store.Mem
	feeRate      float64
	seedGrant    float64
	wallet       string
	node         string
	seedRemain   map[string]float64
	lastSeeded   bool
	lastCredited bool
	settleN      int
	lastSeedUsed float64
	lastReal     float64
	created      []string
}

func (s *fundingState) freshStore() error {
	s.store = store.NewMem()
	s.seedRemain = map[string]float64{}
	s.created = nil
	return nil
}

func (s *fundingState) feePct(p string) error {
	n, err := strconv.Atoi(p)
	if err != nil {
		return err
	}
	s.feeRate = float64(n) / 100
	return nil
}

func (s *fundingState) starterSeed(v string) error {
	f, err := feParseFloat(v)
	s.seedGrant = f
	return err
}

func (s *fundingState) unseen(name string) error { s.wallet = name; return nil }

func (s *fundingState) realCredits(name, v string) error {
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

func (s *fundingState) seedCredits(name, v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.seedGrantMirror(name, f)
	s.wallet = name
	return nil
}

func (s *fundingState) seedAndReal(name, seed, real string) error {
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

// seedGrantMirror calls SeedOnce and updates the mirror only by the ACTUAL balance change, so a
// cap-blocked grant (balance unchanged) does not inflate the mirrored free bucket.
func (s *fundingState) seedGrantMirror(name string, amount float64) bool {
	before, _ := s.store.PeekBalance(name)
	_, seeded, _ := s.store.SeedOnce(name, amount)
	after, _ := s.store.PeekBalance(name)
	if after-before > 1e-9 {
		s.seedRemain[name] += amount
	}
	s.lastSeeded = seeded
	return seeded
}

func (s *fundingState) nodeOwned(node, owner string) error { s.node = node; return nil }

func (s *fundingState) balanceReadWithSeed(name, v string) error {
	amt, err := feParseFloat(v)
	if err != nil {
		return err
	}
	before, _ := s.store.PeekBalance(name)
	if _, err := s.store.BalanceOf(name, amt); err != nil {
		return err
	}
	after, _ := s.store.PeekBalance(name)
	if after-before > 1e-9 {
		s.seedRemain[name] += amt
	}
	s.wallet = name
	return nil
}

func (s *fundingState) seedOnceGrant(name, v string) error {
	amt, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.seedGrantMirror(name, amt)
	s.wallet = name
	return nil
}

func (s *fundingState) walletSeededWith(name, v string) error { return s.seedOnceGrant(name, v) }

func (s *fundingState) balanceIs(name, v string) error {
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

func (s *fundingState) seedBucketIs(name, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.seedRemain[name], want)
}

func (s *fundingState) seededCountIs(v string) error {
	want, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	got, _, _, err := s.store.SeedStatus()
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("seeded count = %d, expected %d", got, want)
	}
	return nil
}

func (s *fundingState) grantSeededIs(b string) error {
	want := b == "true"
	if s.lastSeeded != want {
		return fmt.Errorf("grant seeded = %v, expected %s", s.lastSeeded, b)
	}
	return nil
}

func (s *fundingState) grantForReports(name, b string) error { return s.grantSeededIs(b) }

func (s *fundingState) peekBalance(name string) error { s.wallet = name; return nil }

func (s *fundingState) notSeeded(name string) error {
	bal, err := s.store.PeekBalance(name)
	if err != nil {
		return err
	}
	if bal != 0 {
		return fmt.Errorf("%s appears seeded (balance %g)", name, bal)
	}
	return nil
}

func (s *fundingState) settleOnce(costStr, shareStr string) error {
	cost, err := feParseFloat(costStr)
	if err != nil {
		return err
	}
	share, err := feParseFloat(shareStr)
	if err != nil {
		return err
	}
	return s.doSettle(s.wallet, cost, share)
}

func (s *fundingState) doSettle(who string, cost, share float64) error {
	seedUsed := 0.0
	if cost > 0 && share > 0 {
		rem := s.seedRemain[who]
		seedUsed = cost
		if seedUsed > rem {
			seedUsed = rem
		}
		s.seedRemain[who] = rem - seedUsed
	}
	s.lastSeedUsed = seedUsed
	s.lastReal = cost - seedUsed
	s.settleN++
	rec := protocol.UsageReceipt{RequestID: fmt.Sprintf("f-%s-%d", who, s.settleN), TS: time.Now().Unix()}
	_, err := s.store.Settle(who, s.node, cost, share, rec)
	return err
}

func (s *fundingState) drainSeed(name, v, node string) error {
	amt, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.wallet, s.node = name, node
	return s.doSettle(name, amt, amt*(1-s.feeRate))
}

func (s *fundingState) operatorEarned(op, v string) error {
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

func (s *fundingState) seedPortion(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.lastSeedUsed, want)
}

func (s *fundingState) realPortion(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.lastReal, want)
}

func (s *fundingState) noEarningLot(op string) error { return s.noPayableLot(op) }
func (s *fundingState) noPayableLot(op string) error {
	sp, err := s.store.EarningSplitOfNode(s.node, time.Now())
	if err != nil {
		return err
	}
	if total := sp.Held + sp.Payable + sp.Reserved + sp.Paid; total != 0 {
		return fmt.Errorf("expected no earning lot for %s, got %g", op, total)
	}
	return nil
}

func (s *fundingState) seedLimitIs(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	s.store.SetSeedLimit(n)
	return nil
}

func (s *fundingState) seedStatusIs(seededStr, limitStr, remStr string) error {
	wSeeded, _ := strconv.Atoi(seededStr)
	wLimit, _ := strconv.Atoi(limitStr)
	wRem, _ := strconv.Atoi(remStr)
	seeded, limit, rem, err := s.store.SeedStatus()
	if err != nil {
		return err
	}
	if seeded != wSeeded || limit != wLimit || rem != wRem {
		return fmt.Errorf("seed status = (%d,%d,%d), expected (%d,%d,%d)", seeded, limit, rem, wSeeded, wLimit, wRem)
	}
	return nil
}

func (s *fundingState) nWalletsSeeded(countStr, v string) error {
	n, err := strconv.Atoi(countStr)
	if err != nil {
		return err
	}
	amt, err := feParseFloat(v)
	if err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("seq%d", i)
		s.created = append(s.created, name)
		s.seedGrantMirror(name, amt)
	}
	return nil
}

func (s *fundingState) allNBalance(countStr, v string) error {
	n, err := strconv.Atoi(countStr)
	if err != nil {
		return err
	}
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	ok := 0
	for _, name := range s.created {
		bal, _ := s.store.PeekBalance(name)
		if err := feApprox(bal, want); err == nil {
			ok++
		}
	}
	if ok < n {
		return fmt.Errorf("only %d of %d wallets have balance %g", ok, n, want)
	}
	return nil
}

func (s *fundingState) seedUnlimited() error {
	_, _, rem, err := s.store.SeedStatus()
	if err != nil {
		return err
	}
	if rem != -1 {
		return fmt.Errorf("expected unlimited (remaining -1), got %d", rem)
	}
	return nil
}

func (s *fundingState) nConcurrentSeed(countStr, v string) error {
	n, err := strconv.Atoi(countStr)
	if err != nil {
		return err
	}
	amt, err := feParseFloat(v)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("conc%d", i)
		s.created = append(s.created, name)
		wg.Add(1)
		go func(nm string) {
			defer wg.Done()
			_, _, _ = s.store.SeedOnce(nm, amt)
		}(name)
	}
	wg.Wait()
	return nil
}

func (s *fundingState) exactlyNSeeded(countStr, v string) error {
	n, err := strconv.Atoi(countStr)
	if err != nil {
		return err
	}
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	var got int64
	for _, name := range s.created {
		bal, _ := s.store.PeekBalance(name)
		if feApprox(bal, want) == nil {
			atomic.AddInt64(&got, 1)
		}
	}
	if int(got) != n {
		return fmt.Errorf("exactly %d wallets seeded with %g expected, got %d", n, want, got)
	}
	return nil
}

func (s *fundingState) nWalletsZero(countStr string) error {
	n, err := strconv.Atoi(countStr)
	if err != nil {
		return err
	}
	zero := 0
	for _, name := range s.created {
		bal, _ := s.store.PeekBalance(name)
		if bal == 0 {
			zero++
		}
	}
	if zero < n {
		return fmt.Errorf("only %d of %d wallets are zero", zero, n)
	}
	return nil
}

func (s *fundingState) addRealCredits(v, name string) error {
	amt, err := feParseFloat(v)
	if err != nil {
		return err
	}
	if _, err := s.store.AddCredits(name, amt); err != nil {
		return err
	}
	s.wallet = name
	return nil
}

func (s *fundingState) topupWithKey(v, key, name string) error {
	amt, err := feParseFloat(v)
	if err != nil {
		return err
	}
	ok, _, err := s.store.CreditOnce(key, name, amt)
	if err != nil {
		return err
	}
	s.lastCredited = ok
	s.wallet = name
	return nil
}

func (s *fundingState) topupCreditedIs(b string) error {
	want := b == "true"
	if s.lastCredited != want {
		return fmt.Errorf("top-up credited = %v, expected %s", s.lastCredited, b)
	}
	return nil
}

func TestFundingBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &fundingState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = fundingState{feeRate: 0.30, seedRemain: map[string]float64{}, store: store.NewMem()}
				return ctx, nil
			})
			sc.Step(`^a fresh ledger-backed store$`, st.freshStore)
			sc.Step(`^the platform fee rate is (\d+)%$`, st.feePct)
			sc.Step(`^the starter seed grant is ([\d.]+) credits$`, st.starterSeed)
			sc.Step(`^wallet "([^"]*)" has never been seen$`, st.unseen)
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) in real credits$`, st.realCredits)
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) in FREE seed credits$`, st.seedCredits)
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) in FREE seed credits and ([\d.]+) in real credits$`, st.seedAndReal)
			sc.Step(`^node "([^"]*)" is owned by account "([^"]*)"$`, st.nodeOwned)
			sc.Step(`^(\w+)'s balance is read with a ([\d.]+) seed(?: again)?$`, st.balanceReadWithSeed)
			sc.Step(`^SeedOnce grants (\w+) a ([\d.]+) seed(?: again)?$`, st.seedOnceGrant)
			sc.Step(`^wallet "([^"]*)" is seeded with ([\d.]+)$`, st.walletSeededWith)
			sc.Step(`^(\w+)'s balance is (?:still )?(-?[\d.]+)$`, st.balanceIs)
			sc.Step(`^(\w+)'s free seed bucket is ([\d.]+)$`, st.seedBucketIs)
			sc.Step(`^the seeded-wallet count is (?:exactly )?(\d+)$`, st.seededCountIs)
			sc.Step(`^the grant reports seeded = (true|false)$`, st.grantSeededIs)
			sc.Step(`^the grant for (\w+) reports seeded = (true|false)$`, st.grantForReports)
			sc.Step(`^(\w+)'s balance is peeked$`, st.peekBalance)
			sc.Step(`^(\w+) has not been seeded$`, st.notSeeded)
			sc.Step(`^the request settles via Settle with cost ([\d.]+), owner share ([\d.]+)$`, st.settleOnce)
			sc.Step(`^(\w+) drains the full ([\d.]+) seed against node "([^"]*)"$`, st.drainSeed)
			sc.Step(`^operator "([^"]*)" has earned ([\d.]+)$`, st.operatorEarned)
			sc.Step(`^the seed-funded portion of the cost is ([\d.]+)$`, st.seedPortion)
			sc.Step(`^the real-funded portion of the cost is ([\d.]+)$`, st.realPortion)
			sc.Step(`^no earning lot was created for operator "([^"]*)"$`, st.noEarningLot)
			sc.Step(`^no payable earning lot exists for account "([^"]*)"$`, st.noPayableLot)
			sc.Step(`^the seed limit is (\d+)$`, st.seedLimitIs)
			sc.Step(`^the seed status is (\d+) seeded of (\d+), with (-?\d+) remaining$`, st.seedStatusIs)
			sc.Step(`^(\d+) distinct wallets are each seeded with ([\d.]+)$`, st.nWalletsSeeded)
			sc.Step(`^all (\d+) wallets have a balance of ([\d.]+)$`, st.allNBalance)
			sc.Step(`^the seed status reports unlimited with remaining -1$`, st.seedUnlimited)
			sc.Step(`^(\d+) distinct wallets are concurrently seeded with ([\d.]+) each$`, st.nConcurrentSeed)
			sc.Step(`^exactly (\d+) wallets were seeded with ([\d.]+)$`, st.exactlyNSeeded)
			sc.Step(`^(\d+) wallets have a balance of ([\d.]+)$`, st.nWalletsZero)
			sc.Step(`^([\d.]+) in real credits are added to (\w+)$`, st.addRealCredits)
			sc.Step(`^a ([\d.]+) top-up with key "([^"]*)" is applied to (\w+)$`, st.topupWithKey)
			sc.Step(`^the same ([\d.]+) top-up with key "([^"]*)" is delivered again$`, func(v, key string) error { return st.topupWithKey(v, key, st.wallet) })
			sc.Step(`^the top-up reports credited = (true|false)$`, st.topupCreditedIs)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/money/funding.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("money/funding behavior scenarios failed (see godog output above)")
	}
}
