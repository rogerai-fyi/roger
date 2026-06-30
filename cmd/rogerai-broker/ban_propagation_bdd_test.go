package main

// ban_propagation_bdd_test.go makes features/multinode/ban_propagation.feature EXECUTABLE,
// driving the REAL cross-instance ban path over TWO broker instances (A and B) that share
// ONE in-process Valkey (miniredis) + ONE store — exactly the production 2-instance shape
// (shared Valkey + shared Postgres behind a load balancer). No mocks: it reuses the proven
// multiinstance_test.go harness (newMIBroker / miRegisterNode / pickReq) so the scenarios
// exercise the same real banNode/banOwner/unbanNode/nodeBanSweepOnce + syncLivenessOnce +
// pickFor/computeDiscover/settleRequest code paths as the live broker.
//
// THE INVARIANT IT LOCKS IN: ban state is ALWAYS shared (Valkey rev counter + a Postgres
// re-pull on the sync tick), NEVER trapped in per-instance memory. Any future change that
// re-traps a ban in one instance's map fails this suite RED.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"

	"github.com/cucumber/godog"
)

type banPropState struct {
	t      *testing.T
	mr     *miniredis.Miniredis
	db     *store.Mem
	a, b   *broker // the two instances behind the (simulated) load balancer
	single *broker // for the single-instance no-shared-backend safety scenario
	payer  string
	cost   float64
}

func (s *banPropState) reset(t *testing.T) {
	s.t = t
	s.mr, s.db, s.a, s.b, s.single = nil, nil, nil, nil, nil
	s.payer, s.cost = "", 0
}

// --- Background ------------------------------------------------------------

func (s *banPropState) twoInstances() error {
	s.mr = miniredis.RunT(s.t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	s.db = store.NewMem()
	// Both instances share the SAME broker key (a receipt verifies on either), the SAME
	// durable store (the money/ban truth), and the SAME Valkey (the rev counter rendezvous).
	s.a = newMIBroker(s.t, brokerPriv, s.db, s.mr)
	s.b = newMIBroker(s.t, brokerPriv, s.db, s.mr)
	return nil
}

// --- Given -----------------------------------------------------------------

func (s *banPropState) nodeOnBoth(node, model string) error {
	nodePub, _, _ := ed25519.GenerateKey(nil)
	pub := hex.EncodeToString(nodePub)
	offers := []protocol.ModelOffer{{Model: model}}
	miRegisterNode(s.a, node, pub, "tok-"+node, offers)
	miRegisterNode(s.b, node, pub, "tok-"+node, offers)
	return nil
}

func (s *banPropState) ownedNodeOnBoth(node, owner, model string) error {
	if err := s.db.BindOwner(store.Owner{GitHubID: 1, Login: owner, Pubkey: owner}); err != nil {
		return err
	}
	if err := s.db.BindNode(node, owner); err != nil {
		return err
	}
	return s.nodeOnBoth(node, model)
}

func (s *banPropState) fundedConsumer() error {
	s.payer, s.cost = "u_payer", 1.0
	// Real (non-seed) top-up so the owner share actually mints an earning absent a ban
	// (seed-funded spend earns the operator nothing — see store.realEarnShare).
	if _, _, err := s.db.CreditOnce("topup:"+s.payer, s.payer, 10.0); err != nil {
		return err
	}
	return nil
}

func (s *banPropState) aBannedAndBSynced(node string) error {
	s.a.banNode(node, "abuse - test")
	s.b.syncLivenessOnce()
	if !s.b.isBanned(node) {
		return fmt.Errorf("precondition: instance B did not sync the ban for %q (propagation missing)", node)
	}
	return nil
}

func (s *banPropState) aReportBannedAndBSynced(node string) error {
	s.a.banNode(node, reportBanReasonPrefix+" (5 distinct reporters)")
	s.b.syncLivenessOnce()
	if !s.b.isBanned(node) {
		return fmt.Errorf("precondition: instance B did not sync the report-ban for %q (propagation missing)", node)
	}
	return nil
}

func (s *banPropState) singleInstanceNoShared() error {
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	s.single = newMIBroker(s.t, brokerPriv, store.NewMem(), nil) // nil mr => shared==nil
	if s.single.shared != nil {
		return fmt.Errorf("single-instance broker unexpectedly has a shared backend")
	}
	return nil
}

// --- When ------------------------------------------------------------------

func (s *banPropState) aBansNode(node string) error { s.a.banNode(node, "abuse - test"); return nil }

func (s *banPropState) aBansOwner(owner string) error {
	s.a.banOwner(owner, "abuse", `{"note":"test"}`)
	return nil
}

func (s *banPropState) aUnbansNode(node string) error { return s.a.unbanNode(node) }

func (s *banPropState) aSweepLifts() error {
	// Cutoff one hour in the FUTURE so the just-placed report-ban is "older" and auto-lifts.
	s.a.nodeBanSweepOnce(time.Now().Add(time.Hour))
	return nil
}

func (s *banPropState) bSyncs() error { s.b.syncLivenessOnce(); return nil }

func (s *banPropState) consumerSettlesOnB(node string) error {
	rec := protocol.UsageReceipt{
		RequestID: "req-settle-1", NodeID: node, Model: "m",
		PromptTokens: 5, CompletionTokens: 7, TS: time.Now().Unix(),
	}
	if ok, err := s.db.Hold(s.payer, s.cost); err != nil || !ok {
		return fmt.Errorf("hold for the paid request failed (ok=%v err=%v)", ok, err)
	}
	_, err := s.b.settleRequest(s.payer, node, s.cost, s.cost, rec, "", false)
	return err
}

func (s *banPropState) singleBansNode(node string) error { s.single.banNode(node, "abuse"); return nil }

// --- Then ------------------------------------------------------------------

func (s *banPropState) bDoesNotPick(node, model string) error {
	got, _, ok := s.b.pickFor(model, false, 0, 0, 0, "", nil, nil, nil, pickReq{})
	if ok && got.NodeID == node {
		return fmt.Errorf("instance B still picked banned node %q for %q — the ban did not propagate", node, model)
	}
	return nil
}

func (s *banPropState) bPicks(node, model string) error {
	got, _, ok := s.b.pickFor(model, false, 0, 0, 0, "", nil, nil, nil, pickReq{})
	if !ok || got.NodeID != node {
		return fmt.Errorf("instance B did not pick %q for %q (ok=%v got=%q)", node, model, ok, got.NodeID)
	}
	return nil
}

func (s *banPropState) nodeAbsentFromDiscoverB(node string) error {
	res, ok := s.b.computeDiscover().(map[string]any)
	if !ok {
		return fmt.Errorf("computeDiscover shape changed: %T", s.b.computeDiscover())
	}
	offers, _ := res["offers"].([]offerView)
	for _, o := range offers {
		if o.NodeID == node {
			return fmt.Errorf("node %q is still in instance B's /discover after the ban propagated", node)
		}
	}
	return nil
}

func (s *banPropState) nodeAccruesNoEarning(node string) error {
	got, err := s.db.EarningsOf(node)
	if err != nil {
		return err
	}
	if got != 0 {
		return fmt.Errorf("node %q accrued earning %.6f on instance B despite the owner ban — a banned owner is still earning", node, got)
	}
	return nil
}

func (s *banPropState) consumerStillBilled() error {
	spend, err := s.db.SpendOf(s.payer)
	if err != nil {
		return err
	}
	if spend <= 0 {
		return fmt.Errorf("consumer spend = %.6f, want > 0 (the consumer must still be billed for the output served)", spend)
	}
	return nil
}

func (s *banPropState) singleNodeBanned(node string) error {
	if !s.single.isBanned(node) {
		return fmt.Errorf("single-instance banNode did not flip the local set for %q (the guarded rev bump broke local banning)", node)
	}
	return nil
}

func TestMultinodeBanPropagationBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &banPropState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset(t)
				return ctx, nil
			})
			// Background / Given
			sc.Step(`^two broker instances A and B sharing one Valkey and one store$`, st.twoInstances)
			sc.Step(`^node "([^"]*)" offering model "([^"]*)" is registered on both instances$`, st.nodeOnBoth)
			sc.Step(`^node "([^"]*)" owned by operator "([^"]*)" offering model "([^"]*)" is registered on both instances$`, st.ownedNodeOnBoth)
			sc.Step(`^a funded consumer$`, st.fundedConsumer)
			sc.Step(`^instance A has banned node "([^"]*)" and instance B has synced the ban$`, st.aBannedAndBSynced)
			sc.Step(`^instance A has report-banned node "([^"]*)" and instance B has synced the ban$`, st.aReportBannedAndBSynced)
			sc.Step(`^a single-instance broker with no shared backend$`, st.singleInstanceNoShared)
			// When
			sc.Step(`^instance A bans node "([^"]*)"$`, st.aBansNode)
			sc.Step(`^instance A bans owner "([^"]*)"$`, st.aBansOwner)
			sc.Step(`^instance A unbans node "([^"]*)"$`, st.aUnbansNode)
			sc.Step(`^instance A's node-ban sweep auto-lifts the suspension$`, st.aSweepLifts)
			sc.Step(`^instance B runs its liveness sync tick$`, st.bSyncs)
			sc.Step(`^the consumer settles a paid request served by node "([^"]*)" on instance B$`, st.consumerSettlesOnB)
			sc.Step(`^the single-instance broker bans node "([^"]*)"$`, st.singleBansNode)
			// Then
			sc.Step(`^instance B no longer picks node "([^"]*)" for model "([^"]*)"$`, st.bDoesNotPick)
			sc.Step(`^node "([^"]*)" is absent from instance B's /discover$`, st.nodeAbsentFromDiscoverB)
			sc.Step(`^instance B picks node "([^"]*)" for model "([^"]*)" again$`, st.bPicks)
			sc.Step(`^instance B still picks node "([^"]*)" for model "([^"]*)" before its next sync tick$`, st.bPicks)
			sc.Step(`^node "([^"]*)" accrues no earning$`, st.nodeAccruesNoEarning)
			sc.Step(`^the consumer is still billed for the request$`, st.consumerStillBilled)
			sc.Step(`^node "([^"]*)" is banned on that broker with no error$`, st.singleNodeBanned)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/multinode/ban_propagation.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("multinode/ban_propagation behavior scenarios failed (see godog output above)")
	}
}
