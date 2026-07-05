package main

// discovery_union_bdd_test.go makes features/multinode/discovery_union.feature
// EXECUTABLE: the PUBLIC marketplace on-air view (/discover, /market, liveMarket) must be
// the UNION across every instance behind the load balancer, so the TUNE-IN dial never
// flickers "N online <-> 0 online" as the round-robin LB hits an instance that lacks a
// given node's affine tunnel. Same real-deps harness as cross_instance_bdd_test.go
// (real broker instances over real HTTP, one shared store + one real Valkey/miniredis,
// real signed registrations, the real sync tick). No mocks. It reuses the xiState
// primitives (twoBrokers/singleBroker, register, sweepAll) and adds the multi-node
// registration + public-surface assertions this feature needs.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/cucumber/godog"
)

// duModel is the model every discovery-union node offers, so the /market + liveMarket
// aggregations key on one known model.
const duModel = "free-m"

// duRegisterOne registers ONE free node (unique id) on inst, heartbeats it there (so the
// shared liveness carries a fresh last_seen), and records it in s.duNodes. It does NOT
// sweep - the scenario's When runs the sync tick, exactly as the 5s production tick would.
func (s *xiState) duRegisterOne(specName, inst string) error {
	pub, priv, _ := ed25519.GenerateKey(nil)
	n := &xiNode{
		id:       specName + "-" + s.nonce,
		specName: specName,
		pub:      hex.EncodeToString(pub),
		priv:     priv,
		token:    "tok-0-" + specName + "-" + s.nonce,
		model:    duModel,
		priceOut: 0,
		hc:       &http.Client{Timeout: 10 * time.Second},
	}
	if err := n.register(s.t, s.inst[inst]); err != nil {
		return err
	}
	n.beat(s.t, s.inst[inst])
	s.duNodes = append(s.duNodes, n)
	s.node = n // keep the single-node steps (heartbeats instance X, absent-from-discover) usable
	return nil
}

func (s *xiState) duRegisterOneHB(name, inst string) error {
	return s.duRegisterOne(name, inst)
}

func (s *xiState) duRegisterN(count int, inst string) error {
	for i := 0; i < count; i++ {
		if err := s.duRegisterOne(fmt.Sprintf("du-%s-%d", inst, i), inst); err != nil {
			return err
		}
	}
	return nil
}

// duDiscoverOnline counts the offers instance inst reports ONLINE from computeDiscover -
// the exact function GET /discover computes on every request (and, crucially, on every
// shared-cache MISS: the TTL is ~2-3s, so this is what the round-robin dial actually
// re-derives from THIS instance's own registry moments apart). We assert on computeDiscover
// rather than the raw HTTP body ON PURPOSE: the shared RESPONSE cache (serveCachedJSON)
// makes the HTTP body cross-instance-consistent INCIDENTALLY (a peer can serve the affine
// instance's cached bytes), which MASKS a broken registry mirror. The bug the founder saw
// live is exactly the per-instance registry disagreeing whenever the cache misses; pinning
// computeDiscover pins the real fix (the syncRegistry union), not the accelerator that hides
// it. It is the SAME read path (public, no money mutation), just without the cache veneer.
func (s *xiState) duDiscoverOnline(inst string) (int, error) {
	res, ok := s.inst[inst].b.computeDiscover().(map[string]any)
	if !ok {
		return 0, fmt.Errorf("computeDiscover on %s returned %T", inst, s.inst[inst].b.computeDiscover())
	}
	offers, _ := res["offers"].([]offerView)
	n := 0
	for _, o := range offers {
		if o.Online {
			n++
		}
	}
	return n, nil
}

func (s *xiState) duDiscoverShows(inst string, want int) error {
	got, err := s.duDiscoverOnline(inst)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("instance %s public /discover shows %d online, want %d - the peer disagrees with the affine instance (the flicker)", inst, got, want)
	}
	return nil
}

func (s *xiState) duAgree() error {
	a, err := s.duDiscoverOnline("A")
	if err != nil {
		return err
	}
	b, err := s.duDiscoverOnline("B")
	if err != nil {
		return err
	}
	if a != b {
		return fmt.Errorf("instances disagree on the online count: A=%d B=%d - the round-robin dial flickers between them", a, b)
	}
	return nil
}

func (s *xiState) duStable(inst string, want int) error {
	for i := 0; i < 5; i++ {
		s.sweepAll()
		time.Sleep(10 * time.Millisecond)
		got, err := s.duDiscoverOnline(inst)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("round %d: instance %s shows %d online, want a stable %d (the dial oscillated)", i, inst, got, want)
		}
	}
	return nil
}

// duAgeOut simulates every registered node going dark: it moves last_seen PAST the on-air
// TTL on both the LOCAL registries and the SHARED liveness (so a following sync tick, which
// only merges freshness FORWARD, cannot resurrect them). The nodes stay in b.nodes (only a
// multi-day prune removes a registration) but must read OFFLINE.
func (s *xiState) duAgeOut() error {
	old := time.Now().Add(-2 * nodeTTL)
	for _, n := range s.duNodes {
		for _, inst := range s.inst {
			inst.b.mu.Lock()
			inst.b.lastSeen[n.id] = old
			inst.b.mu.Unlock()
			if inst.b.shared != nil {
				_ = inst.b.shared.markSeen(n.id, old)
			}
		}
	}
	return nil
}

// duReRegisterRotated re-registers the LAST node with a rotated bridge token on inst - the
// idempotent-by-node-id path: a re-register must overwrite the same registry key, never add
// a duplicate offer to the peer's dial.
func (s *xiState) duReRegisterRotated(inst string) error {
	n := s.duNodes[len(s.duNodes)-1]
	n.mu.Lock()
	n.token = "tok-rot-" + xiNonce()
	n.mu.Unlock()
	if err := n.register(s.t, s.inst[inst]); err != nil {
		return err
	}
	n.beat(s.t, s.inst[inst])
	// Steady state: the local-register grace has lapsed so the peer reconciles from shared.
	for _, i := range s.inst {
		i.b.mu.Lock()
		if i.b.localRegAt == nil {
			i.b.localRegAt = map[string]time.Time{}
		}
		i.b.localRegAt[n.id] = time.Now().Add(-2 * syncLocalRegisterGrace)
		i.b.mu.Unlock()
	}
	return nil
}

// duBreakShared points inst's shared store at a dead address so every shared op errors,
// exercising the fail-OPEN read: the instance must keep serving its LOCAL registry.
func (s *xiState) duBreakShared(inst string) error {
	dead, _ := newValkeyStore("redis://127.0.0.1:1") // connection-refused: returns the store, ping errs
	s.t.Cleanup(func() { _ = dead.Close() })
	s.inst[inst].b.shared = dead
	return nil
}

// duAbsentFromMarket asserts the private band never surfaces in inst's PUBLIC /market,
// while (per absentFromDiscover) the instance does hold it internally.
func (s *xiState) duAbsentFromMarket(inst string) error {
	resp, err := http.Get(s.inst[inst].url() + "/market")
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/market on %s = %d", inst, resp.StatusCode)
	}
	if bytes.Contains(body, []byte(s.node.id)) {
		return fmt.Errorf("PRIVATE band %s leaked into instance %s's public /market: %s", s.node.id, inst, body)
	}
	return nil
}

// duHoldsBandOffer asserts the peer LEARNED the private band internally with the SAME
// offer it registered - the "mirrored for routing, hidden from the public surface" split:
// the metrics are real and identical, only the public listing is suppressed.
func (s *xiState) duHoldsBandOffer(inst string) error {
	b := s.inst[inst].b
	b.mu.Lock()
	reg, known := b.nodes[s.node.id]
	priv := b.private[s.node.id]
	b.mu.Unlock()
	if !known {
		return fmt.Errorf("instance %s never learned the band internally - it could not route the freq", inst)
	}
	if !priv {
		return fmt.Errorf("instance %s holds the band but did not flag it private - it would leak into /discover", inst)
	}
	if len(reg.Offers) != 1 || reg.Offers[0].Model != s.node.model {
		return fmt.Errorf("instance %s holds a DIFFERENT offer than registered: got %+v, want model %q", inst, reg.Offers, s.node.model)
	}
	return nil
}

func (s *xiState) duMarketProviders(inst string, want int) error {
	res, ok := s.inst[inst].b.computeMarket().(map[string]any)
	if !ok {
		return fmt.Errorf("computeMarket on %s returned %T", inst, s.inst[inst].b.computeMarket())
	}
	rows, _ := res["market"].([]marketView)
	got := 0
	for _, m := range rows {
		if m.Model == duModel {
			got = m.Providers
		}
	}
	if got != want {
		return fmt.Errorf("instance %s computeMarket shows %d providers for %q, want the union %d", inst, got, duModel, want)
	}
	return nil
}

func (s *xiState) duLiveMarketOnAir(inst string, want int) error {
	lm := s.inst[inst].b.liveMarket(time.Now())
	got, _ := lm["on_air"].(int)
	if got != want {
		return fmt.Errorf("instance %s liveMarket on_air = %d, want the union %d", inst, got, want)
	}
	return nil
}

// duInitScenarios wires the discovery-union feature to the xiState harness.
func duInitScenarios(t *testing.T) func(sc *godog.ScenarioContext) {
	return func(sc *godog.ScenarioContext) {
		st := &xiState{}
		sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
			st.reset(t)
			return ctx, nil
		})
		sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
			st.cleanup()
			return ctx, nil
		})

		// topology (reused from the churn harness)
		sc.Step(`^a single broker with the shared backend wired and the multi-instance flag (ON|OFF)$`, st.singleBrokerAfterPersist)
		sc.Step(`^two broker (?:processes|instances) A and B with the multi-instance flag (ON|OFF) sharing one Valkey and one store$`, st.twoBrokers)

		// registration
		sc.Step(`^node "([^"]*)" registers over HTTP on instance ([AB]) and heartbeats$`, st.duRegisterOneHB)
		sc.Step(`^(\d+) nodes register over HTTP on instance ([AB]) and heartbeats?$`, st.duRegisterN)
		sc.Step(`^node "([^"]*)" registers over HTTP on instance ([AB]) as a private band$`, st.nodeRegistersPrivateOn)
		sc.Step(`^the node re-registers with a rotated token on instance ([AB])$`, st.duReRegisterRotated)

		// sync tick / liveness (reused)
		sc.Step(`^both instances run their liveness sync sweep$`, st.sweepBoth)
		sc.Step(`^the node heartbeats instance ([AB])$`, st.heartbeatsOn)
		sc.Step(`^the node stops heartbeating and its last-seen ages past the on-air TTL$`, st.duAgeOut)
		sc.Step(`^instance ([AB])'s shared store becomes unreachable$`, st.duBreakShared)

		// public-surface assertions
		sc.Step(`^instance ([AB])'s public discover shows (\d+) nodes? online$`, st.duDiscoverShows)
		sc.Step(`^instance ([AB])'s public discover still shows (\d+) nodes? online$`, st.duDiscoverShows)
		sc.Step(`^instance ([AB])'s public discover stays at (\d+) nodes? online across repeated sweeps$`, st.duStable)
		sc.Step(`^instance A and instance B agree on the online count$`, st.duAgree)
		sc.Step(`^the node is absent from instance ([AB])'s public discovery$`, st.absentFromDiscover)
		sc.Step(`^the node is absent from instance ([AB])'s public market$`, st.duAbsentFromMarket)
		sc.Step(`^instance ([AB]) holds the band internally with the same offer it registered$`, st.duHoldsBandOffer)
		sc.Step(`^instance ([AB])'s market shows (\d+) providers for the model$`, st.duMarketProviders)
		sc.Step(`^instance ([AB])'s live-market on-air count is (\d+)$`, st.duLiveMarketOnAir)
	}
}

// TestDiscoveryUnionBDD runs features/multinode/discovery_union.feature.
func TestDiscoveryUnionBDD(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	suite := godog.TestSuite{
		ScenarioInitializer: duInitScenarios(t),
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/multinode/discovery_union.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("multinode/discovery_union scenarios failed (see godog output above)")
	}
}
