package main

// price_floor_bdd_test.go makes features/pricing/price_floor.feature EXECUTABLE. It drives the
// REAL paths that a negative price / negative token count would exploit:
//   - node registration via registerWith (the public #1 sibling of the grant-price mint),
//   - the settle cost clamp via clampSettleCost (the extracted relay chokepoint),
//   - the recorded token counts via a real Finalize read back with EntriesByUser (the
//     billedTokens floor).
// Every assertion reads broker/store state back. parseF lives in grant_price_safety_bdd_test.go
// (same package).

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type pfState struct {
	t *testing.T

	b          *broker
	userPriv   ed25519.PrivateKey
	nodePriv   ed25519.PrivateKey
	nodePubHex string

	offer    protocol.ModelOffer
	signUser bool
	code     int
	msg      string

	// settle / token axis
	payer       string
	startBal    float64
	endBal      float64
	appliedCost float64
	maxCost     float64
	rec         protocol.UsageReceipt
	entryProm   int
	entryComp   int
}

func (s *pfState) reset() {
	s.b, s.userPriv, s.nodePriv, s.nodePubHex = newBandBroker(s.t)
	s.offer = protocol.ModelOffer{Model: "m", Ctx: 4096}
	s.signUser = true
	s.code, s.msg = 0, ""
	s.payer = "cons"
	s.startBal, s.endBal, s.appliedCost = 0, 0, 0
	s.maxCost = 1000
	s.rec = protocol.UsageReceipt{}
	s.entryProm, s.entryComp = 0, 0
}

// fund gives the consumer wallet a real starting balance.
func (s *pfState) fund(v float64) error {
	s.startBal = v
	_, err := s.b.db.(*store.Mem).AddCredits(s.payer, v)
	return err
}

// doRegister drives the real register handler with the current offer + signUser flag.
func (s *pfState) doRegister() error {
	s.code, s.msg = registerWith(s.t, s.b, "n1", s.nodePriv, s.nodePubHex, s.userPriv, s.signUser, s.offer, false, false)
	return nil
}

// settleReceipt models the relay: cost = clampSettleCost(CostWith2(claimed tokens)), then a real
// Finalize captures it. Uses held=0 to isolate the cost SIGN (endBal = startBal - appliedCost),
// so a negative cost that escaped the floor would show up as a raised balance.
func (s *pfState) settleReceipt() error {
	s.rec.RequestID = "req_pf"
	s.appliedCost = clampSettleCost(s.rec.CostWith2(s.rec.PromptTokens, s.rec.CompletionTokens), s.maxCost)
	bal, err := s.b.db.Finalize(s.payer, "n1", 0, s.appliedCost, 0, s.rec)
	if err != nil {
		return err
	}
	s.endBal = bal
	// Read the recorded entry back (records billedTokens) to assert the counts are floored.
	es, err := s.b.db.EntriesByUser(s.payer, 0, 1<<62)
	if err != nil {
		return err
	}
	for _, e := range es {
		if e.RequestID == s.rec.RequestID {
			s.entryProm, s.entryComp = e.PromptTokens, e.CompletionTokens
		}
	}
	return nil
}

// --- Given ------------------------------------------------------------------

func (s *pfState) ownerRegOutPrice(v string) error {
	s.offer.PriceOut = parseF(v)
	s.signUser = true
	return nil
}
func (s *pfState) ownerRegInPrice(v string) error {
	s.offer.PriceIn = parseF(v)
	s.signUser = true
	return nil
}

func (s *pfState) ownerRegWindowPrice(v string) error {
	s.offer.Schedule = []protocol.PriceWindow{{
		Start: "00:00", End: "23:59", Days: []int{0, 1, 2, 3, 4, 5, 6}, Out: parseF(v),
	}}
	s.signUser = true
	return nil
}

func (s *pfState) anonRegOutPrice(v string) error {
	s.offer.PriceOut = parseF(v)
	s.signUser = false
	return nil
}
func (s *pfState) anonRegFree(v string) error {
	s.offer.PriceOut = parseF(v)
	s.signUser = false
	return nil
}

func (s *pfState) pricedOfferAndConsumer(v string) error {
	s.rec.PriceIn, s.rec.PriceOut = 1000, 1000
	return s.fund(parseF(v))
}

func (s *pfState) pricedOfferAndRecount(n string) error {
	s.rec.PriceIn, s.rec.PriceOut = 1000, 1000
	s.rec.BrokerCompletionTokens = int(parseF(n))
	return s.fund(10)
}

func (s *pfState) consumerBalance(v string) error { return s.fund(parseF(v)) }

func (s *pfState) requestCostNotFinite() error {
	s.rec.RequestID = "req_inf"
	return nil
}

// --- When -------------------------------------------------------------------

func (s *pfState) registeredPublic() error { return s.doRegister() }

func (s *pfState) returnsCompletionTokens(n string) error {
	s.rec.CompletionTokens = int(parseF(n))
	return s.settleReceipt()
}
func (s *pfState) returnsPromptTokens(n string) error {
	s.rec.PromptTokens = int(parseF(n))
	return s.settleReceipt()
}
func (s *pfState) returnsBothTokens(p, c string) error {
	s.rec.PromptTokens = int(parseF(p))
	s.rec.CompletionTokens = int(parseF(c))
	return s.settleReceipt()
}

func (s *pfState) nodeClaimsCompletion(n string) error {
	s.rec.CompletionTokens = int(parseF(n))
	s.rec.RequestID = "req_recount"
	if _, err := s.b.db.Finalize(s.payer, "n1", 0, 0, 0, s.rec); err != nil {
		return err
	}
	es, _ := s.b.db.EntriesByUser(s.payer, 0, 1<<62)
	for _, e := range es {
		if e.RequestID == s.rec.RequestID {
			s.entryComp = e.CompletionTokens
		}
	}
	return nil
}

func (s *pfState) settlesAtCost(v string) error {
	s.rec.RequestID = "req_floor"
	s.appliedCost = clampSettleCost(parseF(v), s.maxCost)
	bal, err := s.b.db.Finalize(s.payer, "n1", 0, s.appliedCost, 0, s.rec)
	if err != nil {
		return err
	}
	s.endBal = bal
	return nil
}

func (s *pfState) costPreparedForSettlement() error {
	s.appliedCost = clampSettleCost(math.Inf(1), s.maxCost)
	return nil
}

func (s *pfState) recountCompletion(n string) error {
	s.entryComp = s.b.settleRecount("n1", "req_r", "m", "some completion text", int(parseF(n)))
	return nil
}
func (s *pfState) recountPrompt(n string) error {
	s.entryProm = s.b.settleRecountPrompt("n1", "req_r", "m", "some prompt text", int(parseF(n)), 100)
	return nil
}

func (s *pfState) persistedNegativeNode() error {
	reg := protocol.NodeRegistration{
		NodeID: "n1", PubKey: s.nodePubHex, BridgeToken: "tok", TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: "m", Ctx: 4096, PriceOut: -1000}},
	}
	return s.b.db.(*store.Mem).UpsertNode(store.NodeRecord{NodeID: "n1", Reg: reg, LastSeen: time.Now().Unix()})
}

func (s *pfState) rehydrates() error { s.b.rehydrateNodes(); return nil }

// --- Then -------------------------------------------------------------------

func (s *pfState) rejectedWith(msg string) error {
	if s.code != 400 {
		return fmt.Errorf("register = %d, want 400 (%q); msg=%q", s.code, msg, s.msg)
	}
	if !strings.Contains(s.msg, msg) {
		return fmt.Errorf("rejection %q does not contain %q", s.msg, msg)
	}
	return nil
}

func (s *pfState) notOnMarket() error {
	if p := s.b.liveModelProviders()["m"]; p != 0 {
		return fmt.Errorf("model m has %d providers on the market, want 0 (rejected node must not appear)", p)
	}
	return nil
}

func (s *pfState) accepted() error {
	if s.code != 200 {
		return fmt.Errorf("register = %d, want 200; msg=%q", s.code, s.msg)
	}
	return nil
}

func (s *pfState) costNotNegative() error {
	if s.appliedCost < 0 {
		return fmt.Errorf("settled cost = %v, want >= 0", s.appliedCost)
	}
	return nil
}

func (s *pfState) balanceNotIncreased() error {
	if s.endBal > s.startBal+1e-9 {
		return fmt.Errorf("balance rose from %v to %v through the settle (credit minted)", s.startBal, s.endBal)
	}
	return nil
}

func (s *pfState) recordedCompletionNotNegative() error {
	if s.entryComp < 0 {
		return fmt.Errorf("recorded completion tokens = %d, want >= 0", s.entryComp)
	}
	return nil
}
func (s *pfState) recordedPromptNotNegative() error {
	if s.entryProm < 0 {
		return fmt.Errorf("recorded prompt tokens = %d, want >= 0", s.entryProm)
	}
	return nil
}

func (s *pfState) billedCompletionIs(n string) error {
	want := int(parseF(n))
	if s.entryComp != want {
		return fmt.Errorf("billed completion tokens = %d, want %d", s.entryComp, want)
	}
	return nil
}
func (s *pfState) billedPromptIs(n string) error {
	want := int(parseF(n))
	if s.entryProm != want {
		return fmt.Errorf("billed prompt tokens = %d, want %d", s.entryProm, want)
	}
	return nil
}

func (s *pfState) costAppliedIs(v string) error {
	if s.appliedCost != parseF(v) {
		return fmt.Errorf("applied cost = %v, want %s", s.appliedCost, v)
	}
	return nil
}

func TestPriceFloorBDD(t *testing.T) {
	st := &pfState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^an owner registers a node with output price (\S+) per 1M$`, st.ownerRegOutPrice)
			sc.Step(`^an owner registers a node with input price (\S+) per 1M$`, st.ownerRegInPrice)
			sc.Step(`^an owner registers a node whose scheduled window price is (\S+) per 1M$`, st.ownerRegWindowPrice)
			sc.Step(`^an anonymous \(unsigned\) node registers with output price (\S+) per 1M$`, st.anonRegOutPrice)
			sc.Step(`^an anonymous \(unsigned\) node registers free at (\S+) per 1M$`, st.anonRegFree)
			sc.Step(`^a priced offer and a consumer with a starting balance of (\S+) credits$`, st.pricedOfferAndConsumer)
			sc.Step(`^a priced offer and a broker re-count of (\S+) completion tokens$`, st.pricedOfferAndRecount)
			sc.Step(`^a consumer with a starting balance of (\S+) credits$`, st.consumerBalance)
			sc.Step(`^a request whose computed cost is not finite$`, st.requestCostNotFinite)

			sc.Step(`^the node is registered as a public station$`, st.registeredPublic)
			sc.Step(`^the node returns a receipt claiming (\S+) completion tokens$`, st.returnsCompletionTokens)
			sc.Step(`^the node returns a receipt claiming (\S+) prompt tokens$`, st.returnsPromptTokens)
			sc.Step(`^the node returns a receipt claiming (\S+) prompt and (\S+) completion tokens$`, st.returnsBothTokens)
			sc.Step(`^the relay re-counts a claim of (\S+) completion tokens$`, st.recountCompletion)
			sc.Step(`^the relay re-counts a claim of (\S+) prompt tokens$`, st.recountPrompt)
			sc.Step(`^a persisted node registration carrying a negative output price$`, st.persistedNegativeNode)
			sc.Step(`^the broker re-hydrates its node registry$`, st.rehydrates)
			sc.Step(`^the node claims (\S+) completion tokens$`, st.nodeClaimsCompletion)
			sc.Step(`^a request settles at a computed cost of (\S+) credits$`, st.settlesAtCost)
			sc.Step(`^the cost is prepared for settlement$`, st.costPreparedForSettlement)

			sc.Step(`^the registration is rejected with "([^"]*)"$`, st.rejectedWith)
			sc.Step(`^the node does not appear on the market$`, st.notOnMarket)
			sc.Step(`^no anonymous negative-priced node is on the market$`, st.notOnMarket)
			sc.Step(`^the registration is accepted$`, st.accepted)
			sc.Step(`^the settled cost is not negative$`, st.costNotNegative)
			sc.Step(`^the consumer balance is not increased by the settle$`, st.balanceNotIncreased)
			sc.Step(`^the recorded completion tokens are not negative$`, st.recordedCompletionNotNegative)
			sc.Step(`^the recorded prompt tokens are not negative$`, st.recordedPromptNotNegative)
			sc.Step(`^the billed completion tokens are (\S+)$`, st.billedCompletionIs)
			sc.Step(`^the billed completion count is (\S+)$`, st.billedCompletionIs)
			sc.Step(`^the billed prompt count is (\S+)$`, st.billedPromptIs)
			sc.Step(`^the settled cost applied is (\S+)$`, st.costAppliedIs)
			sc.Step(`^the cost applied is (\S+)$`, st.costAppliedIs)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/pricing/price_floor.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("price-floor scenarios failed (see godog output above)")
	}
}
