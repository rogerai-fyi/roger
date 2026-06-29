package main

// routing_bdd_test.go makes features/routing/eligibility.feature EXECUTABLE, driving the
// REAL per-relay routing pass (broker.pickFor in tunnel.go) against an in-memory registry
// + store. It pins the HARD eligibility filters that gate a node IN or OUT before any
// scoring: liveness (nodeTTL), node ban, the DURABLE owner ban (anti-rotation - a banned
// operator's fresh node id is still refused) AND its zero-cost fast path (no AccountOfNode
// lookup when no owner is banned), private-band visibility, pin/exclude/allow caller
// constraints, confidential-only (TEE) eligibility, the dead-probe NOT-SERVING gate (and
// recovery), the min-tps speed floor, model match, the output price cap, and that a free
// offer never moves the eligible price range. Zero surviving candidates => a clean
// not-found (the relay answers "no station serving" instead of dispatching into a failure).
//
// Candidacy is observed faithfully through the real pickFor: a node X is a candidate under
// a request R iff pickFor, run against a registry reduced to {X} while carrying R's exact
// constraints (the same shared per-node metric maps, owner bindings, ban sets), returns
// found && node==X. Per-node eligibility is independent of the other nodes (rangeMin/max
// only feed the SCORING pass), so this is exact, not an approximation.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// This suite reuses the package-local countingStore (cacheaccel_test.go), which wraps a
// store.Store and counts AccountOfNode lookups - exactly the signal the owner-ban fast path
// needs: pickFor resolves a node's owner ONLY when bannedOwners is non-empty, so the common
// (no-banned-owner) case must perform zero AccountOfNode lookups.

type routeState struct {
	b   *broker
	mem *store.Mem      // underlying store, for owner/node bindings
	cs  *countingStore  // AccountOfNode-counting wrapper installed as b.db
	now time.Time
	ids []string // every node id added this scenario (for "both nodes are candidates")

	// the request under test (the hard-filter inputs pickFor consumes)
	model                        string
	confidentialOnly             bool
	minTPS, maxIn, maxOut        float64
	pin                          string
	exclude, allow, privateAllow map[string]bool

	// last full-registry pick outcome
	found  bool
	picked string
}

// reset builds a broker with an empty registry, all per-node metric maps initialised, and a
// counting in-memory store. bannedOwners starts empty (the common case: the owner-ban
// lookup is skipped entirely).
func (s *routeState) reset() {
	s.now = time.Now()
	s.mem = store.NewMem()
	s.cs = &countingStore{Store: s.mem}
	s.b = routeBroker(s.now, map[string]protocol.NodeRegistration{})
	s.b.db = s.cs
	s.b.bannedOwners = map[string]bool{}
	s.b.successCount = map[string]int{}
	s.b.concurrentTPS = map[string]float64{}
	s.b.feeRate = 0.30
	s.ids = nil
	s.model, s.confidentialOnly = "", false
	s.minTPS, s.maxIn, s.maxOut = 0, 0, 0
	s.pin = ""
	s.exclude, s.allow, s.privateAllow = nil, nil, nil
	s.found, s.picked = false, ""
}

// addNode registers a node on air for one model at the given out-price, seen just now. It
// leaves trust + tps at the zero value (unprobed, unmeasured) - a fresh node passes every
// eligibility gate, so each scenario layers ONLY the one attribute it is testing on top.
func (s *routeState) addNode(id, model string, outPrice float64) {
	s.b.nodes[id] = protocol.NodeRegistration{
		NodeID: id,
		Offers: []protocol.ModelOffer{{Model: model, PriceIn: outPrice, PriceOut: outPrice}},
	}
	s.b.lastSeen[id] = s.now
	s.ids = append(s.ids, id)
}

// isCandidate runs the REAL pickFor against a registry reduced to {id}, carrying the
// request-under-test's exact constraints. found && node==id means id survived every hard
// gate for this request.
func (s *routeState) isCandidate(id string) bool {
	saved := s.b.nodes
	s.b.nodes = map[string]protocol.NodeRegistration{id: saved[id]}
	s.b.mu.Lock()
	n, _, ok := s.b.pickFor(s.model, s.confidentialOnly, s.minTPS, s.maxIn, s.maxOut, s.pin, s.exclude, s.allow, s.privateAllow, pickReq{})
	s.b.mu.Unlock()
	s.b.nodes = saved
	return ok && n.NodeID == id
}

// runPick runs the request against the FULL registry and records the outcome. The
// AccountOfNode counter is zeroed first so the assertion measures only routing-time lookups.
func (s *routeState) runPick() {
	s.cs.mu.Lock()
	s.cs.accountOfNode = 0
	s.cs.mu.Unlock()
	s.b.mu.Lock()
	n, _, ok := s.b.pickFor(s.model, s.confidentialOnly, s.minTPS, s.maxIn, s.maxOut, s.pin, s.exclude, s.allow, s.privateAllow, pickReq{})
	s.b.mu.Unlock()
	s.found, s.picked = ok, n.NodeID
}

// --- Given: registry + node attributes -------------------------------------

func (s *routeState) emptyRegistry() error { return nil } // built by reset()
func (s *routeState) feeRate30() error     { s.b.feeRate = 0.30; return nil }

func (s *routeState) onAirSeenNow(id, model string) error { s.addNode(id, model, 0); return nil }

func (s *routeState) staleNode(id, model string) error {
	s.addNode(id, model, 0)
	s.b.lastSeen[id] = s.now.Add(-2 * nodeTTL)
	return nil
}

func (s *routeState) nodeBanned(id string) error { s.b.banned[id] = true; return nil }

func (s *routeState) operatorBanned(owner string) error { s.b.bannedOwners[owner] = true; return nil }
func (s *routeState) noOperatorBanned() error           { return nil } // bannedOwners stays empty

func (s *routeState) ownedByOnAir(id, owner, model string) error {
	s.addNode(id, model, 0)
	_ = s.mem.BindOwner(store.Owner{GitHubID: 1, Login: owner, Pubkey: owner})
	return s.mem.BindNode(id, owner)
}

func (s *routeState) privateNode(id, model string) error {
	s.addNode(id, model, 0)
	s.b.private[id] = true
	return nil
}

func (s *routeState) twoNodes(a, b, model string) error {
	s.addNode(a, model, 0)
	s.addNode(b, model, 0)
	return nil
}

func (s *routeState) onAirNotTEE(id, model string) error {
	s.addNode(id, model, 0)
	s.b.confidential[id] = false
	return nil
}

func (s *routeState) onAirTEE(id, model string) error {
	s.addNode(id, model, 0)
	s.b.confidential[id] = true
	return nil
}

func (s *routeState) deadProbe(id string) error {
	tq := s.b.trust[id]
	tq.probeFails = probeDeadStreak
	s.b.trust[id] = tq
	return nil
}

func (s *routeState) onAirMeasured(id, model string, tps int) error {
	s.addNode(id, model, 0)
	s.b.tps[id] = float64(tps)
	return nil
}

func (s *routeState) onAirUnmeasured(id, model string) error { s.addNode(id, model, 0); return nil }

func (s *routeState) onlyModel(id, model string) error { s.addNode(id, model, 0); return nil }

// offersAtPrice / offersFree set a node healthy (probed canary-passed, live tps) so price is
// the ONLY differentiator - used by the "free offer never moves the price range" scenario
// (and harmless to the price-cap scenario, where the cap excludes the node regardless).
func (s *routeState) offersAtPrice(id, model string, price float64) error {
	s.addNode(id, model, price)
	s.b.trust[id] = trustState{probed: true, probeOK: true}
	s.b.tps[id] = 200
	return nil
}

func (s *routeState) offersFree(id, model string) error {
	s.addNode(id, model, 0)
	s.b.trust[id] = trustState{probed: true, probeOK: true}
	s.b.tps[id] = 200
	return nil
}

func (s *routeState) everyNodeExcluded(model string) error {
	s.addNode("n-x", model, 0)
	s.b.banned["n-x"] = true // some gate (a ban here) drops the only on-air node
	return nil
}

// --- When: the request routes ----------------------------------------------

func (s *routeState) requestRoutes(model string) error { s.model = model; s.runPick(); return nil }

func (s *routeState) publicRequestNoBand(model string) error {
	s.model = model
	s.privateAllow = nil
	s.runPick()
	return nil
}

func (s *routeState) requestPrivateAllow(model, id string) error {
	s.model = model
	s.privateAllow = map[string]bool{id: true}
	s.runPick()
	return nil
}

func (s *routeState) requestPinned(model, id string) error {
	s.model = model
	s.pin = id
	s.runPick()
	return nil
}

func (s *routeState) requestExcluding(model, id string) error {
	s.model = model
	s.exclude = map[string]bool{id: true}
	s.runPick()
	return nil
}

func (s *routeState) requestAllowingOnly(model, id string) error {
	s.model = model
	s.allow = map[string]bool{id: true}
	s.runPick()
	return nil
}

func (s *routeState) confidentialRequest(model string) error {
	s.model = model
	s.confidentialOnly = true
	s.runPick()
	return nil
}

func (s *routeState) requestMinTPS(model string, min int) error {
	s.model = model
	s.minTPS = float64(min)
	s.runPick()
	return nil
}

func (s *routeState) requestMaxOut(model string, max float64) error {
	s.model = model
	s.maxOut = max
	s.runPick()
	return nil
}

// --- Then ------------------------------------------------------------------

func (s *routeState) isCand(id string) error {
	if !s.isCandidate(id) {
		return fmt.Errorf("%q should be a candidate but pickFor excluded it", id)
	}
	return nil
}

func (s *routeState) isNotCand(id string) error {
	if s.isCandidate(id) {
		return fmt.Errorf("%q should NOT be a candidate but pickFor kept it eligible", id)
	}
	return nil
}

func (s *routeState) noStationServing() error {
	if s.found {
		return fmt.Errorf("expected no station serving, but pickFor chose %q", s.picked)
	}
	return nil
}

func (s *routeState) noAccountLookup() error {
	if _, _, _, accountOfNode, _ := s.cs.counts(); accountOfNode != 0 {
		return fmt.Errorf("AccountOfNode was called %d time(s); with no banned owner the lookup must be skipped entirely", accountOfNode)
	}
	return nil
}

func (s *routeState) recoveredProbeEligible(id string) error {
	// A single OK probe resets the consecutive-failure streak, so the node clears the
	// dead-probe NOT-SERVING gate again on the next pick.
	s.b.trust[id] = trustState{probed: true, probeOK: true, probeFails: 0}
	if !s.isCandidate(id) {
		return fmt.Errorf("%q should be eligible again after a recovered probe", id)
	}
	return nil
}

func (s *routeState) bothCandidates() error {
	if len(s.ids) < 2 {
		return fmt.Errorf("expected at least two nodes, have %d", len(s.ids))
	}
	for _, id := range s.ids {
		if !s.isCandidate(id) {
			return fmt.Errorf("%q should be a candidate", id)
		}
	}
	return nil
}

// rangeFromPaidOnly drives the REAL price-range derivation pickFor uses (extendOutRange)
// over the scenario's two offers: a free (out<=0) offer must leave the eligible window
// untouched, so only the paid offer sets rangeMin/rangeMax. (Win-share can't probe this:
// when the range is derived correctly the two nodes score EQUALLY, and P2C's equal-score
// tie-break collapses to map order - so the invariant is asserted on the exact derivation
// instead.) Order-independent: folding the free offer first, last, or twice never moves the
// [0.30,0.30] window the lone paid offer establishes.
func (s *routeState) rangeFromPaidOnly() error {
	rmin, rmax, have := 0.0, 0.0, false
	rmin, rmax, have = extendOutRange(0, rmin, rmax, have) // n-free, before any paid offer
	if have {
		return fmt.Errorf("a free offer moved the price range (haveRange became true with only a free offer)")
	}
	rmin, rmax, have = extendOutRange(0.30, rmin, rmax, have) // n-paid sets the window
	if !have || rmin != 0.30 || rmax != 0.30 {
		return fmt.Errorf("the paid offer should set the range to [0.30,0.30]; got [%v,%v] have=%v", rmin, rmax, have)
	}
	rmin, rmax, have = extendOutRange(0, rmin, rmax, have) // another free offer must not widen it
	if rmin != 0.30 || rmax != 0.30 {
		return fmt.Errorf("a free offer moved the range after a paid one; got [%v,%v]", rmin, rmax)
	}
	return nil
}

func (s *routeState) pickForFalse() error {
	if s.found {
		return fmt.Errorf("pickFor should report found=false, but chose %q", s.picked)
	}
	return nil
}

func (s *routeState) relayNoStation() error {
	// The relay dispatches only when pickFor returns ok AND a tunnel exists (tunnel.go: the
	// `if !ok || t == nil` guard answers a clean 503 "no node offers <model>"). found=false
	// guarantees that no-station answer rather than a dispatch into a failing upstream.
	if s.found {
		return fmt.Errorf("relay would dispatch (pickFor chose %q); expected a clean no-station answer", s.picked)
	}
	return nil
}

func TestRoutingEligibilityBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &routeState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			// Given
			sc.Step(`^a broker with an empty in-memory node registry$`, st.emptyRegistry)
			sc.Step(`^the fee rate is 30%$`, st.feeRate30)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" and was seen just now$`, st.onAirSeenNow)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)", seen just now$`, st.onAirSeenNow)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" but was last seen 2\*nodeTTL ago$`, st.staleNode)
			sc.Step(`^node "([^"]*)" is banned$`, st.nodeBanned)
			sc.Step(`^operator "([^"]*)" is banned$`, st.operatorBanned)
			sc.Step(`^no operator is banned$`, st.noOperatorBanned)
			sc.Step(`^node "([^"]*)" is owned by "([^"]*)", on air for "([^"]*)", seen just now$`, st.ownedByOnAir)
			sc.Step(`^node "([^"]*)" is private and on air for "([^"]*)", seen just now$`, st.privateNode)
			sc.Step(`^nodes "([^"]*)" and "([^"]*)" are on air for "([^"]*)", seen just now$`, st.twoNodes)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)", seen just now, NOT TEE-attested$`, st.onAirNotTEE)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)", seen just now, TEE-attested$`, st.onAirTEE)
			sc.Step(`^"([^"]*)" has probeFails >= probeDeadStreak \(its upstream is dead\)$`, st.deadProbe)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)", seen just now, measured at (\d+) t/s$`, st.onAirMeasured)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)", seen just now, with no tps reading yet$`, st.onAirUnmeasured)
			sc.Step(`^node "([^"]*)" is on air ONLY for "([^"]*)", seen just now$`, st.onlyModel)
			sc.Step(`^node "([^"]*)" offers "([^"]*)" at out-price \$([0-9.]+)/1M, seen just now$`, st.offersAtPrice)
			sc.Step(`^node "([^"]*)" offers "([^"]*)" free \(out-price 0\), seen just now$`, st.offersFree)
			sc.Step(`^every on-air node for "([^"]*)" is excluded by some gate$`, st.everyNodeExcluded)
			// When
			sc.Step(`^a request routes for "([^"]*)"$`, st.requestRoutes)
			sc.Step(`^a public request routes for "([^"]*)" with no band code$`, st.publicRequestNoBand)
			sc.Step(`^a request routes for "([^"]*)" with "([^"]*)" in the privateAllow set$`, st.requestPrivateAllow)
			sc.Step(`^a request routes for "([^"]*)" pinned to "([^"]*)"$`, st.requestPinned)
			sc.Step(`^a request routes for "([^"]*)" excluding "([^"]*)"$`, st.requestExcluding)
			sc.Step(`^a request routes for "([^"]*)" allowing only "([^"]*)"$`, st.requestAllowingOnly)
			sc.Step(`^a confidential-only request routes for "([^"]*)"$`, st.confidentialRequest)
			sc.Step(`^a request routes for "([^"]*)" with min-tps (\d+)$`, st.requestMinTPS)
			sc.Step(`^a request routes for "([^"]*)" with max-out \$([0-9.]+)/1M$`, st.requestMaxOut)
			// Then
			sc.Step(`^"([^"]*)" is a candidate$`, st.isCand)
			sc.Step(`^"([^"]*)" is NOT a candidate$`, st.isNotCand)
			sc.Step(`^the request finds no station serving$`, st.noStationServing)
			sc.Step(`^no AccountOfNode store lookup was performed$`, st.noAccountLookup)
			sc.Step(`^a single recovered probe makes "([^"]*)" eligible again on the next pick$`, st.recoveredProbeEligible)
			sc.Step(`^both nodes are candidates$`, st.bothCandidates)
			sc.Step(`^the price range min/max is derived from the paid offer only$`, st.rangeFromPaidOnly)
			sc.Step(`^pickFor returns found=false$`, st.pickForFalse)
			sc.Step(`^the relay answers "no station serving" rather than dispatching into a failure$`, st.relayNoStation)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/routing/eligibility.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("routing/eligibility behavior scenarios failed (see godog output above)")
	}
}
