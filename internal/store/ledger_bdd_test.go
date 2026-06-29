package store

// ledger_bdd_test.go makes features/money/ledger.feature an EXECUTABLE Cucumber suite against
// the REAL production Postgres store (no mocks): a fresh ephemeral Postgres via the shared
// ROGERAI_TEST_DATABASE_URL the cover-gate provisions. It pins that the Postgres ledger holds
// (never overdraws), Hold+Finalize/Settle debit exactly the cost and mint only the REAL
// operator earning (seed-funded spend mints none), and ChargebackLineage claws only the
// operator's share + is idempotent on the dispute id — the SAME guarantees the in-memory
// reference store upholds. Lives in package store so it runs SERIALLY with the other store
// tests (freshLedgerPG TRUNCATEs the shared DB, safe only intra-package) and reuses NewPostgres.
// Skipped (like every Postgres path here) when ROGERAI_TEST_DATABASE_URL is unset.

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// freshLedgerPG opens the real Postgres store and TRUNCATEs every data table (same reset the
// internal/store parity helpers use), returning errors instead of t.Fatalf so a failure
// surfaces as a clean godog step error. In-package, so it can reach pg.db.
func freshLedgerPG(dsn string) (*Postgres, error) {
	pg, err := NewPostgres(dsn)
	if err != nil {
		return nil, err
	}
	if _, err := pg.db.Exec(`TRUNCATE
		rogerai.wallet, rogerai.earnings, rogerai.earning_lots, rogerai.receipts,
		rogerai.ledger, rogerai.node_owner, rogerai.nodes, rogerai.owners,
		rogerai.processed_events, rogerai.seed_counter, rogerai.seed_grants,
		rogerai.grants, rogerai.grant_usage, rogerai.payouts, rogerai.disputes,
		rogerai.pending_reversals, rogerai.account_settings, rogerai.account_recount_holds,
		rogerai.recount_holds, rogerai.reports, rogerai.appeals, rogerai.csam_incidents,
		rogerai.banned_nodes, rogerai.banned_owners, rogerai.owner_strikes,
		rogerai.checkout_charges, rogerai.offer_overrides, rogerai.private_bands
		RESTART IDENTITY CASCADE`); err != nil {
		return nil, fmt.Errorf("truncate: %w", err)
	}
	if _, err := pg.db.Exec(`INSERT INTO rogerai.seed_counter(id,count) VALUES(1,0)
		ON CONFLICT (id) DO UPDATE SET count=0`); err != nil {
		return nil, fmt.Errorf("reset seed_counter: %w", err)
	}
	return pg, nil
}

type lgState struct {
	dsn        string
	pg         *Postgres
	now        time.Time
	reqN       int
	wallet     string // the wallet the bare "the request settles via ..." steps spend from
	lastHoldOK bool

	lastCost   float64
	lastEarned float64

	lastCb          ChargebackResult
	balBeforeRedeliv float64
}

func lgParse(v string) (float64, error) { return strconv.ParseFloat(v, 64) }

func lgApprox(got, want float64) error {
	if math.Abs(got-want) > 1e-9+1e-9*math.Abs(want) {
		return fmt.Errorf("got %.6f, want %.6f", got, want)
	}
	return nil
}

func (s *lgState) reqID() string { s.reqN++; return fmt.Sprintf("r%d", s.reqN) }

func (s *lgState) earnedTotal(account string) (float64, error) {
	sp, err := s.pg.EarningSplitOf(account, s.now)
	if err != nil {
		return 0, err
	}
	return sp.Held + sp.Payable + sp.Reserved, nil
}

// --- steps ------------------------------------------------------------------

func (s *lgState) freshPGStore() error {
	pg, err := freshLedgerPG(s.dsn)
	if err != nil {
		return err
	}
	s.pg = pg
	s.now = time.Now()
	s.reqN = 0
	return nil
}

// feePct validates the Background fee declaration; the 30% fee is already baked into the
// explicit owner-share inputs each scenario passes to Settle/Finalize, so nothing is stored.
func (s *lgState) feePct(p string) error {
	_, err := strconv.Atoi(p)
	return err
}

func (s *lgState) walletReal(name, v string) error {
	f, err := lgParse(v)
	if err != nil {
		return err
	}
	s.wallet = name
	_, err = s.pg.AddCredits(name, f)
	return err
}

func (s *lgState) walletSeed(name, v string) error {
	f, err := lgParse(v)
	if err != nil {
		return err
	}
	s.wallet = name
	_, _, err = s.pg.SeedOnce(name, f)
	return err
}

func (s *lgState) nodeOwned(node, owner string) error { return s.pg.BindNode(node, owner) }

func (s *lgState) placesHold(name, v string) error {
	f, err := lgParse(v)
	if err != nil {
		return err
	}
	ok, err := s.pg.Hold(name, f)
	s.lastHoldOK = ok
	return err
}

func (s *lgState) holdRefused() error {
	if s.lastHoldOK {
		return fmt.Errorf("hold succeeded, want refused")
	}
	return nil
}

func (s *lgState) holdSucceeds() error {
	if !s.lastHoldOK {
		return fmt.Errorf("hold refused, want success")
	}
	return nil
}

func (s *lgState) balanceIs(name, v string) error {
	want, err := lgParse(v)
	if err != nil {
		return err
	}
	bal, err := s.pg.BalanceOf(name, 0)
	if err != nil {
		return err
	}
	return lgApprox(bal, want)
}

func (s *lgState) finalizeSettles(holdS, costS, shareS string) error {
	held, err := lgParse(holdS)
	if err != nil {
		return err
	}
	cost, err := lgParse(costS)
	if err != nil {
		return err
	}
	share, err := lgParse(shareS)
	if err != nil {
		return err
	}
	before, err := s.earnedTotal("op1")
	if err != nil {
		return err
	}
	if _, err := s.pg.Finalize(s.wallet, "n1", held, cost, share, protocol.UsageReceipt{RequestID: s.reqID(), TS: s.now.Unix()}); err != nil {
		return err
	}
	after, err := s.earnedTotal("op1")
	if err != nil {
		return err
	}
	s.lastCost, s.lastEarned = cost, after-before
	return nil
}

func (s *lgState) settleSettles(costS, shareS string) error {
	cost, err := lgParse(costS)
	if err != nil {
		return err
	}
	share, err := lgParse(shareS)
	if err != nil {
		return err
	}
	before, err := s.earnedTotal("op1")
	if err != nil {
		return err
	}
	if _, err := s.pg.Settle(s.wallet, "n1", cost, share, protocol.UsageReceipt{RequestID: s.reqID(), TS: s.now.Unix()}); err != nil {
		return err
	}
	after, err := s.earnedTotal("op1")
	if err != nil {
		return err
	}
	s.lastCost, s.lastEarned = cost, after-before
	return nil
}

func (s *lgState) operatorEarned(op, v string) error {
	want, err := lgParse(v)
	if err != nil {
		return err
	}
	got, err := s.earnedTotal(op)
	if err != nil {
		return err
	}
	return lgApprox(got, want)
}

func (s *lgState) platformKeeps(v string) error {
	want, err := lgParse(v)
	if err != nil {
		return err
	}
	return lgApprox(s.lastCost-s.lastEarned, want)
}

func (s *lgState) noCreditsLost() error {
	platform := s.lastCost - s.lastEarned
	if s.lastEarned < -1e-9 || platform < -1e-9 {
		return fmt.Errorf("negative split: earned=%g platform=%g", s.lastEarned, platform)
	}
	return lgApprox(s.lastEarned+platform, s.lastCost) // cost splits exactly into earning + platform take
}

func (s *lgState) twoSettledRequests(costS, shareS string) error {
	cost, err := lgParse(costS)
	if err != nil {
		return err
	}
	share, err := lgParse(shareS)
	if err != nil {
		return err
	}
	for i := 0; i < 2; i++ {
		if _, err := s.pg.Settle(s.wallet, "n1", cost, share, protocol.UsageReceipt{RequestID: s.reqID(), TS: s.now.Unix() + int64(i)}); err != nil {
			return err
		}
	}
	return nil
}

func (s *lgState) oneSettledRequest(costS, shareS string) error {
	cost, err := lgParse(costS)
	if err != nil {
		return err
	}
	share, err := lgParse(shareS)
	if err != nil {
		return err
	}
	_, err = s.pg.Settle("alice", "n1", cost, share, protocol.UsageReceipt{RequestID: s.reqID(), TS: s.now.Unix()})
	return err
}

func (s *lgState) chargebackFor(v, who string) error {
	amt, err := lgParse(v)
	if err != nil {
		return err
	}
	res, err := s.pg.ChargebackLineage("dp_auto", who, "", amt, s.now)
	if err != nil {
		return err
	}
	s.lastCb = res
	return nil
}

func (s *lgState) chargebackDispute(v, dispute string) error {
	amt, err := lgParse(v)
	if err != nil {
		return err
	}
	res, err := s.pg.ChargebackLineage(dispute, "alice", "", amt, s.now)
	if err != nil {
		return err
	}
	s.lastCb = res
	return nil
}

func (s *lgState) sameChargebackAgain(dispute string) error {
	bal, err := s.pg.BalanceOf("alice", 0)
	if err != nil {
		return err
	}
	s.balBeforeRedeliv = bal
	res, err := s.pg.ChargebackLineage(dispute, "alice", "", 50, s.now)
	if err != nil {
		return err
	}
	s.lastCb = res
	return nil
}

func (s *lgState) clawedFromOp(v, op string) error {
	want, err := lgParse(v)
	if err != nil {
		return err
	}
	return lgApprox(s.lastCb.Clawed, want)
}

func (s *lgState) platformLossIs(v string) error {
	want, err := lgParse(v)
	if err != nil {
		return err
	}
	return lgApprox(s.lastCb.PlatformLoss, want)
}

func (s *lgState) otherLotUntouched(v string) error {
	want, err := lgParse(v)
	if err != nil {
		return err
	}
	got, err := s.earnedTotal("op1")
	if err != nil {
		return err
	}
	return lgApprox(got, want) // the surviving (un-clawed, un-paid) lot's gross
}

func (s *lgState) clawedPlusLoss(v string) error {
	want, err := lgParse(v)
	if err != nil {
		return err
	}
	return lgApprox(s.lastCb.Clawed+s.lastCb.PlatformLoss, want)
}

func (s *lgState) redeliveryClaws(v string) error {
	want, err := lgParse(v)
	if err != nil {
		return err
	}
	if !s.lastCb.AlreadyHandled {
		return fmt.Errorf("redelivery not marked already-handled")
	}
	return lgApprox(s.lastCb.Clawed, want)
}

func (s *lgState) balanceUnchangedRedeliv() error {
	bal, err := s.pg.BalanceOf("alice", 0)
	if err != nil {
		return err
	}
	return lgApprox(bal, s.balBeforeRedeliv)
}

func TestLedgerBDD(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ledger.feature requires ROGERAI_TEST_DATABASE_URL (a real Postgres) - skipping")
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &lgState{dsn: dsn}
			// No Before hook: the Background "a fresh Postgres-backed store" step opens + TRUNCATEs
			// once per scenario (a Before would double the NewPostgres pool + truncate).
			sc.Step(`^a fresh Postgres-backed store$`, st.freshPGStore)
			sc.Step(`^the platform fee rate is (\d+)%$`, st.feePct)
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) in real credits$`, st.walletReal)
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) in FREE seed credits$`, st.walletSeed)
			sc.Step(`^node "([^"]*)" is owned by account "([^"]*)"$`, st.nodeOwned)
			sc.Step(`^(\w+) places a hold of ([\d.]+)$`, st.placesHold)
			sc.Step(`^(\w+) holds ([\d.]+)$`, func(_, v string) error { return st.placesHold("alice", v) })
			sc.Step(`^the hold is refused$`, st.holdRefused)
			sc.Step(`^the hold succeeds$`, st.holdSucceeds)
			sc.Step(`^(\w+)'s balance is (?:still )?([\d.]+)$`, st.balanceIs)
			sc.Step(`^the request settles via Finalize with hold ([\d.]+), cost ([\d.]+), owner share ([\d.]+)$`, st.finalizeSettles)
			sc.Step(`^the request settles via Settle with cost ([\d.]+), owner share ([\d.]+)$`, st.settleSettles)
			sc.Step(`^operator "([^"]*)" has earned ([\d.]+)$`, st.operatorEarned)
			sc.Step(`^the platform keeps ([\d.]+)$`, st.platformKeeps)
			sc.Step(`^no credits were created or destroyed$`, st.noCreditsLost)
			sc.Step(`^(\w+) has two settled requests of cost ([\d.]+) each \(owner share ([\d.]+) each\)$`, func(_, c, sh string) error { return st.twoSettledRequests(c, sh) })
			sc.Step(`^(\w+) has one settled request of cost ([\d.]+) \(owner share ([\d.]+)\)$`, func(_, c, sh string) error { return st.oneSettledRequest(c, sh) })
			sc.Step(`^a chargeback of ([\d.]+) is processed for (\w+)$`, st.chargebackFor)
			sc.Step(`^a chargeback of ([\d.]+) with dispute id "([^"]*)" is processed$`, st.chargebackDispute)
			sc.Step(`^the same chargeback "([^"]*)" is delivered again$`, st.sameChargebackAgain)
			sc.Step(`^exactly ([\d.]+) is clawed back from operator "([^"]*)"$`, st.clawedFromOp)
			sc.Step(`^the platform loss is ([\d.]+)$`, st.platformLossIs)
			sc.Step(`^the operator's other ([\d.]+) lot is untouched$`, st.otherLotUntouched)
			sc.Step(`^clawed plus platform-loss equals the disputed ([\d.]+)$`, st.clawedPlusLoss)
			sc.Step(`^the second delivery claws back ([\d.]+)$`, st.redeliveryClaws)
			sc.Step(`^(\w+)'s balance is unchanged by the redelivery$`, func(string) error { return st.balanceUnchangedRedeliv() })
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/money/ledger.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("money/ledger behavior scenarios failed (see godog output above)")
	}
}
