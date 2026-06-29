package main

// discovery_market_bdd_test.go makes features/discovery/market.feature EXECUTABLE, driving
// the REAL public marketplace computation: computeDiscover (the live per-offer /discover
// list), computeMarket (the per-model aggregate: providers, cheapest price, 0..100 signal),
// the PRIVATE-band exclusion from BOTH public views, the stale-node handling, the
// recency-driven signal, and the in-process short-TTL read cache (serveCachedJSON, the
// no-Redis local fallback). No mocks - it reads the real computed payloads.
//
// Spec correction (deployed code is source of truth): the prose said a stale node's offer is
// "omitted" from /discover. computeDiscover actually LISTS every public node with an Online
// flag (a consumer sees an offline station, greyed out); only the /market AGGREGATE
// (computeMarket) drops a stale node. The feature scenario is corrected to match.

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type dmState struct {
	b   *broker
	now time.Time

	offers []offerView  // last /discover payload
	market []marketView // last /market payload

	computeCalls int // counts compute() runs for the cache scenario
}

func (s *dmState) reset() {
	s.now = time.Now()
	s.b = routeBroker(s.now, map[string]protocol.NodeRegistration{})
	s.b.db = store.NewMem()
	s.b.successCount = map[string]int{}
	s.b.concurrentTPS = map[string]float64{}
	s.b.probeSched = map[string]*probeState{}
	s.b.localCache = map[string]localCacheEntry{}
	s.offers, s.market, s.computeCalls = nil, nil, 0
}

// addNode registers a public node on air for one model at input price `in`, last seen
// `age` ago, probed + canary-passed (so it carries a signal).
func (s *dmState) addNode(id, model string, in float64, age time.Duration) {
	s.b.nodes[id] = protocol.NodeRegistration{
		NodeID: id,
		Offers: []protocol.ModelOffer{{Model: model, PriceIn: in, PriceOut: in}},
	}
	s.b.lastSeen[id] = s.now.Add(-age)
	s.b.trust[id] = trustState{probed: true, probeOK: true, ttftMs: 200}
	s.b.tps[id] = 120
}

func (s *dmState) getDiscover() {
	res := s.b.computeDiscover().(map[string]any)
	s.offers, _ = res["offers"].([]offerView)
}

func (s *dmState) getMarket() {
	res := s.b.computeMarket().(map[string]any)
	s.market, _ = res["market"].([]marketView)
}

func (s *dmState) offerFor(node, model string) (offerView, bool) {
	for _, o := range s.offers {
		if o.NodeID == node && o.Model == model {
			return o, true
		}
	}
	return offerView{}, false
}

func (s *dmState) marketFor(model string) (marketView, bool) {
	for _, m := range s.market {
		if m.Model == model {
			return m, true
		}
	}
	return marketView{}, false
}

// --- Scenario 1: /discover lists live public offers ------------------------

func (s *dmState) nodesOnAirTwoModels(m1, m2 string) error {
	s.addNode("n-a", m1, 0.20, 0)
	s.addNode("n-b", m2, 0.30, 0)
	return nil
}

func (s *dmState) consumerGetsDiscover() error { s.getDiscover(); return nil }

func (s *dmState) bothOffersListed() error {
	a, okA := s.offerFor("n-a", "gpt-oss-20b")
	b, okB := s.offerFor("n-b", "qwen3-4b")
	if !okA || !okB {
		return fmt.Errorf("both public offers must be listed; got %d offers", len(s.offers))
	}
	if !a.Online || !b.Online || a.In != 0.20 || b.In != 0.30 {
		return fmt.Errorf("each offer must carry its node + active price online; a=%+v b=%+v", a, b)
	}
	return nil
}

// --- Scenario 2 (corrected): stale node OFFLINE on /discover, omitted from /market ---

func (s *dmState) staleNodeOnAir(model string) error {
	s.addNode("n-stale", model, 0.20, 2*nodeTTL) // last heartbeat older than nodeTTL
	return nil
}

func (s *dmState) staleOfflineOnDiscoverOmittedFromMarket() error {
	s.getDiscover()
	o, ok := s.offerFor("n-stale", "gpt-oss-20b")
	if !ok {
		return fmt.Errorf("/discover lists every public node; the stale node should appear (as OFFLINE)")
	}
	if o.Online {
		return fmt.Errorf("a stale node must read Online=false on /discover")
	}
	s.getMarket()
	if _, present := s.marketFor("gpt-oss-20b"); present {
		return fmt.Errorf("a stale node must drop out of the /market aggregate")
	}
	return nil
}

// --- Scenario 3: private node hidden from both public views ----------------

func (s *dmState) privateBandNode(model string) error {
	s.addNode("n-priv", model, 0.20, 0)
	s.b.private["n-priv"] = true
	return nil
}

func (s *dmState) consumerGetsDiscoverOrMarket() error {
	s.getDiscover()
	s.getMarket()
	return nil
}

func (s *dmState) privateNeverAppears() error {
	if _, ok := s.offerFor("n-priv", "gpt-oss-20b"); ok {
		return fmt.Errorf("a PRIVATE node must never appear on /discover")
	}
	if _, ok := s.marketFor("gpt-oss-20b"); ok {
		return fmt.Errorf("a PRIVATE node must never appear in the /market aggregate")
	}
	return nil
}

// --- Scenario 4: /market aggregates per-model -------------------------------

func (s *dmState) severalNodesDifferentPrices(model string) error {
	s.addNode("n-1", model, 0.20, 0)
	s.addNode("n-2", model, 0.50, 0)
	return nil
}

func (s *dmState) consumerGetsMarket() error { s.getMarket(); return nil }

func (s *dmState) marketShowsAggregate(model string) error {
	m, ok := s.marketFor(model)
	if !ok {
		return fmt.Errorf("%q must appear in the /market aggregate", model)
	}
	if m.Providers != 2 {
		return fmt.Errorf("aggregate offer count = %d, want 2", m.Providers)
	}
	if m.MinPrice != 0.20 {
		return fmt.Errorf("aggregate cheapest price = %v, want 0.20 (the price range floor)", m.MinPrice)
	}
	if m.Signal <= 0 {
		return fmt.Errorf("aggregate signal = %d, want a positive signal strength", m.Signal)
	}
	return nil
}

// --- Scenario 5: /market is cached for a short TTL --------------------------

func (s *dmState) marketJustComputed() error {
	s.addNode("n-1", "gpt-oss-20b", 0.20, 0)
	w := httptest.NewRecorder()
	s.b.serveCachedJSON(w, "market:test", publicMarketTTL, s.countingMarket)
	return nil
}

func (s *dmState) countingMarket() any {
	s.computeCalls++
	return s.b.computeMarket()
}

func (s *dmState) anotherGetWithinTTL() error {
	w := httptest.NewRecorder()
	s.b.serveCachedJSON(w, "market:test", publicMarketTTL, s.countingMarket)
	return nil
}

func (s *dmState) cachedServedNotRecomputed() error {
	if s.computeCalls != 1 {
		return fmt.Errorf("computeMarket ran %d times, want 1 (the second hit must serve the cached payload)", s.computeCalls)
	}
	return nil
}

// --- Scenario 6: signal reflects liveness/recency --------------------------

func (s *dmState) twoNodesFreshAndStaling(model string) error {
	s.addNode("n-fresh", model, 0.20, 0)                                     // recency ~1.0
	s.addNode("n-staling", model, 0.20, time.Duration(0.9*float64(nodeTTL))) // live but recency ~0.1
	return nil
}

func (s *dmState) marketComputed() error { s.getMarket(); return nil }

func (s *dmState) fresherContributesStronger() error {
	both, ok := s.marketFor("gpt-oss-20b")
	if !ok {
		return fmt.Errorf("the model must appear in the aggregate")
	}
	// The fresh node lifts the aggregate above what the staling node alone would yield: drop
	// the fresh node and recompute - the staling-only signal must be strictly weaker.
	delete(s.b.nodes, "n-fresh")
	s.getMarket()
	stalingOnly, ok := s.marketFor("gpt-oss-20b")
	if !ok {
		return fmt.Errorf("the staling node alone should still aggregate (it is live)")
	}
	if !(both.Signal > stalingOnly.Signal) {
		return fmt.Errorf("the fresher node must contribute a stronger signal: both=%d staling-only=%d", both.Signal, stalingOnly.Signal)
	}
	return nil
}

func TestDiscoveryMarketBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &dmState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^nodes are on air for "([^"]*)" and "([^"]*)"$`, st.nodesOnAirTwoModels)
			sc.Step(`^a consumer GETs /discover$`, st.consumerGetsDiscover)
			sc.Step(`^both models' public offers are listed with their node \+ price$`, st.bothOffersListed)
			sc.Step(`^a node on air for "([^"]*)" that has not heartbeat within nodeTTL$`, st.staleNodeOnAir)
			sc.Step(`^that node is shown OFFLINE on /discover and omitted from the /market aggregate$`, st.staleOfflineOnDiscoverOmittedFromMarket)
			sc.Step(`^a node sharing "([^"]*)" on a PRIVATE band$`, st.privateBandNode)
			sc.Step(`^a consumer GETs /discover or /market$`, st.consumerGetsDiscoverOrMarket)
			sc.Step(`^the private node never appears \(it is reachable only via its frequency code\)$`, st.privateNeverAppears)
			sc.Step(`^several nodes on air for "([^"]*)" at different prices$`, st.severalNodesDifferentPrices)
			sc.Step(`^a consumer GETs /market$`, st.consumerGetsMarket)
			sc.Step(`^"([^"]*)" shows the aggregated offer count, price range, and a signal strength$`, st.marketShowsAggregate)
			sc.Step(`^/market was just computed$`, st.marketJustComputed)
			sc.Step(`^another consumer GETs /market within the cache TTL$`, st.anotherGetWithinTTL)
			sc.Step(`^the cached payload is served \(computeMarket is not recomputed per hit\)$`, st.cachedServedNotRecomputed)
			sc.Step(`^two nodes for "([^"]*)": one freshly probed, one going stale$`, st.twoNodesFreshAndStaling)
			sc.Step(`^/market is computed$`, st.marketComputed)
			sc.Step(`^the fresher node contributes a stronger signal than the staling one$`, st.fresherContributesStronger)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/discovery/market.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("discovery/market behavior scenarios failed (see godog output above)")
	}
}
