package main

// scoring_bdd_test.go makes features/routing/scoring.feature EXECUTABLE, driving the REAL
// smart-router v2 scoring + selection (broker.pickFor's second pass + router.go's pure
// pieces): the price modifier (cheaper favored; a user cap widens the reward ceiling; free
// offers are neutral and don't set the range), the multiplicative reliability spine, the
// request-size-aware speed-fit, the canary-GATED UCB exploration radius, capacity-aware
// load (local + cross-instance peer inflight), the two-tier health gate (healthy beats
// failing absolutely; Tier B only when Tier A is empty), and power-of-two-choices selection
// (spreads load instead of dogpiling; concentration tightens with beta).
//
// Where a winner is unambiguous the suite asserts it through the REAL pickFor over many
// seeded requests (win-share). Where the property is a single composite value that P2C's
// equal-score tie-break would hide (rmax, the UCB gate, the beta knob), it drives the exact
// pure helper pickFor uses (priceCeiling / explorationRadius / selectP2C). No mocks.

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type scoreState struct {
	b   *broker
	now time.Time

	model     string
	maxOut    float64
	rangeMax  float64 // dearest eligible out-price, derived via the real extendOutRange
	totalReqs int64   // broker-wide relay count feeding the UCB radius

	counts map[string]int // win-share result of the last "many requests route"
	total  int
	found  bool
	picked string
}

// reset builds a healthy two-tier-capable broker with an empty registry. Scenarios add
// their own named nodes; the Background's "two healthy Tier-A nodes, equal on every axis" is
// the DEFAULT each addBase node satisfies (probed + canary-passed, equal tps + zero load),
// so a scenario layers only the one axis it differentiates.
func (s *scoreState) reset() {
	s.now = time.Now()
	s.b = routeBroker(s.now, map[string]protocol.NodeRegistration{})
	s.b.db = store.NewMem()
	s.b.bannedOwners = map[string]bool{}
	s.model = "gpt-oss-20b"
	s.maxOut, s.rangeMax, s.totalReqs = 0, 0, 0
	s.counts, s.total = nil, 0
	s.found, s.picked = false, ""
}

// addBase registers a node on air for the model at out-price `price`, Tier A (probed +
// canary-passed, probeFails 0), with an equal speed baseline (120 t/s, 200ms TTFT) and zero
// load. The equal baseline is the Background's "equal reliability, speed, and load".
func (s *scoreState) addBase(id string, price float64) {
	s.b.nodes[id] = protocol.NodeRegistration{
		NodeID: id,
		Offers: []protocol.ModelOffer{{Model: s.model, PriceIn: price, PriceOut: price}},
	}
	s.b.lastSeen[id] = s.now
	s.b.trust[id] = trustState{probed: true, probeOK: true, probeFails: 0, ttftMs: 200}
	s.b.tps[id] = 120
	s.b.inflight[id] = 0
}

// winShares routes many seeded requests through the REAL pickFor and tallies the winners.
// Distinct per-request seeds drive power-of-two-choices spread (a nil rng is deterministic
// top-1). promptTokens feeds the size-aware speed-fit; pref selects the weight profile.
func (s *scoreState) winShares(promptTokens int, p pref) {
	const n = 900
	s.counts, s.total = map[string]int{}, n
	for i := 0; i < n; i++ {
		req := pickReq{pref: p, promptTokens: promptTokens, rng: rand.New(rand.NewSource(int64(i + 1)))}
		s.b.mu.Lock()
		node, _, ok := s.b.pickFor(s.model, false, 0, 0, 0, "", nil, nil, nil, req)
		s.b.mu.Unlock()
		if ok {
			s.counts[node.NodeID]++
		}
	}
}

func (s *scoreState) deterministicPick() {
	s.b.mu.Lock()
	node, _, ok := s.b.pickFor(s.model, false, 0, 0, 0, "", nil, nil, nil, pickReq{})
	s.b.mu.Unlock()
	s.found, s.picked = ok, node.NodeID
}

func (s *scoreState) wonMajority(id string) error {
	if s.counts[id]*2 <= s.total {
		return fmt.Errorf("%q won %d/%d picks, want the majority", id, s.counts[id], s.total)
	}
	return nil
}

// --- Background ------------------------------------------------------------

func (s *scoreState) twoEligibleNodes(model string) error { s.model = model; return nil }
func (s *scoreState) bothHealthyEqual() error             { return nil } // addBase's default

// --- price -----------------------------------------------------------------

func (s *scoreState) offersAt(id, model string, price float64) error {
	s.model = model
	s.addBase(id, price)
	return nil
}

func (s *scoreState) manyRequests(model string) error {
	s.model = model
	s.winShares(0, prefBalanced)
	return nil
}

func (s *scoreState) eligiblePrices(a, b float64) error {
	// Derive the dearest eligible out-price the way pickFor does: fold both through the real
	// extendOutRange (free offers excluded), giving rangeMax.
	rmin, rmax, have := 0.0, 0.0, false
	rmin, rmax, have = extendOutRange(a, rmin, rmax, have)
	rmin, rmax, have = extendOutRange(b, rmin, rmax, have)
	_ = rmin
	if !have {
		return fmt.Errorf("no paid offer set the range from %v/%v", a, b)
	}
	s.rangeMax = rmax
	return nil
}

func (s *scoreState) requestMaxOutScore(model string, max float64) error {
	s.model = model
	s.maxOut = max
	return nil
}

func (s *scoreState) ceilingIs(rmax, notMax float64) error {
	got := priceCeiling(s.rangeMax, s.maxOut) // the exact rmax pickFor computes
	if got != rmax {
		return fmt.Errorf("priceCeiling = %v, want %v (the user cap widens the ceiling)", got, rmax)
	}
	if got == notMax {
		return fmt.Errorf("priceCeiling = %v, but it must NOT collapse to the eligible max %v", got, notMax)
	}
	return nil
}

// --- free offers tie -------------------------------------------------------

func (s *scoreState) bothFree(a, b, model string) error {
	s.model = model
	s.addBase(a, 0)
	s.addBase(b, 0)
	return nil
}

func (s *scoreState) requestsRoute(model string) error {
	s.model = model
	s.winShares(0, prefBalanced)
	return nil
}

func (s *scoreState) freeTieBrokenByOther() error {
	// Neither free offer sets the price range (the real derivation ignores out<=0).
	_, _, have := extendOutRange(0, 0, 0, false)
	_, _, have = extendOutRange(0, 0, 0, have)
	if have {
		return fmt.Errorf("a free offer moved the eligible price range")
	}
	// And the pick is then decided by the OTHER factors: give n-free-a a clear speed edge and
	// confirm it now wins the majority (price, being tied at neutral, cannot be the decider).
	s.b.tps["n-free-a"] = 300
	s.b.tps["n-free-b"] = 30
	s.winShares(0, prefBalanced)
	return s.wonMajority("n-free-a")
}

// --- reliability spine -----------------------------------------------------

func (s *scoreState) cleanProbeSuccess(id string, rate float64) error {
	s.addBase(id, 0.30)
	s.b.success[id] = rate // a measured organic success rate
	return nil
}

func (s *scoreState) successRateSamePrice(id string, rate float64) error {
	s.addBase(id, 0.30)
	s.b.success[id] = rate
	return nil
}

// --- speed fit -------------------------------------------------------------

func (s *scoreState) runsAtFast(id string, tps int) error {
	s.addBase(id, 0.30)
	s.b.tps[id] = float64(tps)
	tq := s.b.trust[id]
	tq.ttftMs = 150 // low TTFT
	s.b.trust[id] = tq
	return nil
}

func (s *scoreState) runsAtSlow(id string, tps int) error {
	s.addBase(id, 0.30)
	s.b.tps[id] = float64(tps)
	tq := s.b.trust[id]
	tq.ttftMs = 400
	s.b.trust[id] = tq
	return nil
}

func (s *scoreState) largePromptRequest(model string) error {
	s.model = model
	s.winShares(30000, prefBalanced)
	return nil
}

func (s *scoreState) favoredBySpeedFit(id string) error { return s.wonMajority(id) }

// --- UCB exploration (canary-gated) ----------------------------------------

func (s *scoreState) provenProbed(id string) error {
	s.addBase(id, 0.30)
	tq := s.b.trust[id]
	tq.probed, tq.probeOK, tq.probes = true, true, 3
	s.b.trust[id] = tq
	return nil
}

func (s *scoreState) neverProbed(id string) error {
	s.addBase(id, 0.30)
	s.b.trust[id] = trustState{probed: false, probeOK: false} // never canary-passed
	return nil
}

func (s *scoreState) scoringRuns(model string) error {
	s.model = model
	s.totalReqs = 1000 // some traffic, so a canary-passed node's radius is non-zero
	return nil
}

func (s *scoreState) receivesRadius(id string) error {
	c := prefBalanced.weights().c
	if r := explorationRadius(s.b.trust[id], c, s.totalReqs, s.b.successCount[id]); r <= 0 {
		return fmt.Errorf("%q (canary-passed) got radius %v, want > 0", id, r)
	}
	return nil
}

func (s *scoreState) receivesZeroRadius(id string) error {
	c := prefBalanced.weights().c
	if r := explorationRadius(s.b.trust[id], c, s.totalReqs, s.b.successCount[id]); r != 0 {
		return fmt.Errorf("%q (unproven) got radius %v, want exactly 0 (gate must withhold exploration)", id, r)
	}
	return nil
}

// --- load (capacity-aware, cross-instance) ---------------------------------

func (s *scoreState) busierThan(busy, idle string) error {
	s.addBase(busy, 0.30)
	s.addBase(idle, 0.30)
	s.b.inflight[busy] = 8 // higher inflight at equal (hw-prior) capacity
	s.b.inflight[idle] = 0
	return nil
}

func (s *scoreState) peerLoaded(shared string) error {
	s.addBase(shared, 0.30)
	s.addBase("n-solo", 0.30) // an equal control with no peer load
	s.b.inflight[shared] = 0
	s.b.peerInflight = map[string]int{shared: 8, "n-solo": 0}
	return nil
}

func (s *scoreState) loadReflectsPeer() error {
	// The peer-loaded node must lose the deterministic pick to the equal control (peer load
	// counted), and the capacity-normalised factor with peer load must be strictly lower than
	// local-only - both via the REAL loadFactor pickFor sums into.
	if s.picked != "n-solo" {
		return fmt.Errorf("deterministic pick = %q, want n-solo (the peer-loaded node must be penalised)", s.picked)
	}
	cap := capacityOf(s.b.concurrentTPS["n-shared"], s.b.nodes["n-shared"].HW)
	withPeer := loadFactor(s.b.inflight["n-shared"]+s.b.peerInflight["n-shared"], cap)
	localOnly := loadFactor(s.b.inflight["n-shared"], cap)
	if !(withPeer < localOnly) {
		return fmt.Errorf("loadFactor with peer (%v) must be below local-only (%v)", withPeer, localOnly)
	}
	return nil
}

// --- two-tier health gate --------------------------------------------------

func (s *scoreState) tierAandTierB(healthy, probation string) error {
	s.addBase(healthy, 0.50) // Tier A, pricier/slower
	// n-probation: probeFails>=2 => Tier B, but cheaper AND faster so it WOULD outscore the
	// healthy node if the tier gate did not exclude it.
	s.addBase(probation, 0.05)
	s.b.tps[probation] = 400
	tq := s.b.trust[probation]
	tq.probeFails = 2
	s.b.trust[probation] = tq
	return nil
}

func (s *scoreState) tierAselected(healthy, _ string) error {
	if !s.found || s.picked != healthy {
		return fmt.Errorf("pick = %q (found=%v), want %q (Tier A is an absolute gate)", s.picked, s.found, healthy)
	}
	return nil
}

func (s *scoreState) onlyTierB(probation string) error {
	s.addBase(probation, 0.30)
	tq := s.b.trust[probation]
	tq.probeFails = 2 // Tier B (probation)
	s.b.trust[probation] = tq
	return nil
}

func (s *scoreState) tierBselected(probation string) error {
	if !s.found || s.picked != probation {
		return fmt.Errorf("pick = %q (found=%v), want %q (Tier B is the last-resort availability floor)", s.picked, s.found, probation)
	}
	return nil
}

// --- P2C selection ---------------------------------------------------------

func (s *scoreState) severalTierANearEqual(model string) error {
	s.model = model
	s.addBase("n-1", 0.30)
	s.addBase("n-2", 0.30)
	s.addBase("n-3", 0.30)
	return nil
}

func (s *scoreState) picksSpread() error {
	for id, c := range s.counts {
		if frac := float64(c) / float64(s.total); frac > 0.6 {
			return fmt.Errorf("node %q took %.0f%% of traffic, want spread (<60%%)", id, frac*100)
		}
	}
	if len(s.counts) < 3 {
		return fmt.Errorf("only %d of 3 nodes received traffic - load did not spread", len(s.counts))
	}
	return nil
}

// spreadTightensWithBeta drives the REAL selectP2C over a fixed near-equal band: a higher
// beta concentrates selection on the top node (the spread tightens), a lower beta loosens
// it. Distinct seeds per draw exercise the weighted sampling.
func (s *scoreState) spreadTightensWithBeta() error {
	band := []scoredCand{{idx: 0, score: 1.00}, {idx: 1, score: 0.95}, {idx: 2, score: 0.90}}
	topShare := func(beta float64) float64 {
		const n = 3000
		top := 0
		for i := 0; i < n; i++ {
			if selectP2C(band, beta, rand.New(rand.NewSource(int64(i+1)))) == 0 {
				top++
			}
		}
		return float64(top) / n
	}
	loose, tight := topShare(0.5), topShare(8.0)
	if !(tight > loose) {
		return fmt.Errorf("top-node share should rise with beta: loose(β=0.5)=%.2f tight(β=8)=%.2f", loose, tight)
	}
	return nil
}

func (s *scoreState) noCandidateSurvives() error {
	s.addBase("n-x", 0.30)
	s.b.banned["n-x"] = true // excluded -> zero candidates -> neither tier populated
	return nil
}

func (s *scoreState) selectP2CnegAndNotFound() error {
	if got := selectP2C(nil, 2.0, rand.New(rand.NewSource(1))); got >= 0 {
		return fmt.Errorf("selectP2C over an empty pool = %d, want < 0", got)
	}
	if s.found {
		return fmt.Errorf("pickFor should report found=false, but chose %q", s.picked)
	}
	return nil
}

func TestRoutingScoringBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &scoreState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			// Background
			sc.Step(`^a broker with two eligible nodes on air for "([^"]*)", both seen just now$`, st.twoEligibleNodes)
			sc.Step(`^both nodes are healthy \(Tier A\) with equal reliability, speed, and load$`, st.bothHealthyEqual)
			// price
			sc.Step(`^node "([^"]*)" offers "([^"]*)" at out-price \$([0-9.]+)/1M$`, st.offersAt)
			sc.Step(`^many requests route for "([^"]*)"$`, st.manyRequests)
			sc.Step(`^"([^"]*)" wins the majority of picks$`, st.wonMajority)
			sc.Step(`^the eligible out-prices are \$([0-9.]+)/1M and \$([0-9.]+)/1M$`, st.eligiblePrices)
			sc.Step(`^a request routes for "([^"]*)" with max-out \$([0-9.]+)/1M$`, st.requestMaxOutScore)
			sc.Step(`^the price-mod ceiling \(rmax\) is \$([0-9.]+)/1M, not the eligible max \$([0-9.]+)/1M$`, st.ceilingIs)
			// free tie
			sc.Step(`^nodes "([^"]*)" and "([^"]*)" both offer "([^"]*)" free$`, st.bothFree)
			sc.Step(`^requests route for "([^"]*)"$`, st.requestsRoute)
			sc.Step(`^neither node sets the price range, and the pick is decided by the other factors$`, st.freeTieBrokenByOther)
			// reliability
			sc.Step(`^node "([^"]*)" has a clean probe history and a ([0-9.]+) success rate$`, st.cleanProbeSuccess)
			sc.Step(`^node "([^"]*)" has a ([0-9.]+) success rate at the same price$`, st.successRateSamePrice)
			// speed
			sc.Step(`^node "([^"]*)" runs at (\d+) t/s with low TTFT$`, st.runsAtFast)
			sc.Step(`^node "([^"]*)" runs at (\d+) t/s at the same price and reliability$`, st.runsAtSlow)
			sc.Step(`^a large-prompt request routes for "([^"]*)"$`, st.largePromptRequest)
			sc.Step(`^"([^"]*)" is favored by the speed-fit term$`, st.favoredBySpeedFit)
			// UCB
			sc.Step(`^node "([^"]*)" has been probed and passed \(probed && probeOK\)$`, st.provenProbed)
			sc.Step(`^node "([^"]*)" has never passed a probe$`, st.neverProbed)
			sc.Step(`^scoring runs for "([^"]*)"$`, st.scoringRuns)
			sc.Step(`^"([^"]*)" receives a UCB exploration radius$`, st.receivesRadius)
			sc.Step(`^"([^"]*)" receives a zero radius \(we never explore unproven-flaky capacity\)$`, st.receivesZeroRadius)
			// load
			sc.Step(`^node "([^"]*)" has higher inflight relative to its capacity than "([^"]*)"$`, st.busierThan)
			sc.Step(`^node "([^"]*)" has 0 local inflight but high peer-instance inflight$`, st.peerLoaded)
			sc.Step(`^a request routes for "([^"]*)"$`, func(model string) error { st.model = model; st.deterministicPick(); return nil })
			sc.Step(`^the load factor reflects local \+ peer inflight, not just local$`, st.loadReflectsPeer)
			// two-tier
			sc.Step(`^node "([^"]*)" is Tier A and node "([^"]*)" is Tier B \(probeFails>=2\)$`, st.tierAandTierB)
			sc.Step(`^"([^"]*)" \(Tier A\) is selected even if "([^"]*)" would score higher$`, st.tierAselected)
			sc.Step(`^the only on-air node "([^"]*)" is Tier B \(probation\)$`, st.onlyTierB)
			sc.Step(`^"([^"]*)" is selected \(a transient blip never blanks a live model\)$`, st.tierBselected)
			// P2C
			sc.Step(`^several Tier-A nodes have near-equal scores for "([^"]*)"$`, st.severalTierANearEqual)
			sc.Step(`^picks spread across the top nodes \(load-aware\), not all onto one$`, st.picksSpread)
			sc.Step(`^the spread tightens or loosens with the beta knob$`, st.spreadTightensWithBeta)
			sc.Step(`^no candidate survives into either tier$`, st.noCandidateSurvives)
			sc.Step(`^selectP2C returns < 0 and pickFor reports found=false$`, st.selectP2CnegAndNotFound)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/routing/scoring.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("routing/scoring behavior scenarios failed (see godog output above)")
	}
}
