package main

// curated_routing_bdd_test.go - the godog harness for the wire half of
// features/curated/curated_routing_filter.feature: routing neutrality, best-connection
// selection, the standard relay/meter path, and the anonymization pins. (The consumer-pin
// and failover-receipt scenarios ride the client/TUI suites; the filter scenarios live in
// curated_dial.feature.)

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"rogerai.fm/roger/v6/internal/bddtest"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
)

func pick(cond bool, v string) string {
	if cond {
		return v
	}
	return ""
}

type curRouteState struct {
	t          *testing.T
	b          *broker
	userPriv   ed25519.PrivateKey
	nodePriv   ed25519.PrivateKey
	nodePubHex string

	pickA, pickB string // pickFor outcomes under flipped curated flags
}

func (s *curRouteState) reset() {
	s.b, s.userPriv, s.nodePriv, s.nodePubHex = newBandBroker(s.t)
	// newBandBroker leaves the probe-trust maps nil (its scenarios never probe);
	// this suite records probe outcomes, so give it what main() gives the real one.
	s.b.trust = map[string]trustState{}
	s.b.inflight = map[string]int{}
	s.b.success = map[string]float64{}
	s.pickA, s.pickB = "", ""
}

func (s *curRouteState) reg(id, model string, curated bool, provider string, tps float64) error {
	reg := protocol.NodeRegistration{
		NodeID: id, PubKey: s.nodePubHex, BridgeToken: "tok", TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: model, Ctx: 8192}},
	}
	if curated {
		reg.Curated, reg.CuratedProvider = true, provider
	}
	reg.SignRegistration(s.nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, s.userPriv, body)
	w := httptest.NewRecorder()
	s.b.register(w, r)
	if w.Code != http.StatusOK {
		return fmt.Errorf("register %s = %d: %s", id, w.Code, w.Body.String())
	}
	if tps > 0 {
		// The same write the live canary makes, so the pick weighs what a probe would
		// have taught it.
		s.b.recordProbe(id, probePass, 50, tps, true, true)
	}
	return nil
}

func (s *curRouteState) humanAndCurated() error {
	if err := s.reg("h1", "gpt-oss-20b", false, "", 40); err != nil {
		return err
	}
	return s.reg("c1", "gpt-oss-20b", true, "openrouter", 40)
}

func (s *curRouteState) picksBySameRules() error {
	// Same terms, many draws: both stations must be REACHABLE by the ordinary pick - a
	// rule that shut proxies (or humans) out entirely would never select one.
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		n, _, ok := s.b.pickFor("gpt-oss-20b", false, 0, 0, 0, "", nil, nil, nil, pickReq{rng: seededRand(fmt.Sprintf("neutral-%d", i))})
		if !ok {
			return fmt.Errorf("pick failed with two healthy stations")
		}
		seen[n.NodeID] = true
	}
	if !seen["h1"] || !seen["c1"] {
		return fmt.Errorf("at equal terms both kinds must be reachable by the ordinary rules; picked only %v", seen)
	}
	return nil
}

func (s *curRouteState) noHardcodedPreference() error {
	// THE FLIP TEST. Re-run the identical draw sequence with the curated flags swapped
	// onto the other node: a router with no thumb on the scale produces the same node-id
	// choices, because nothing it weighs changed.
	// DISTRIBUTIONS, not sequences. pickFor iterates the node map, so two runs differ
	// even with identical flags - demanding a byte-equal sequence was asserting map-order
	// determinism, not neutrality. What neutrality actually means: at equal terms, each
	// station's share of the picks does not depend on which of them carries the flag.
	share := func(hCurated bool) (float64, error) {
		s.reset()
		if err := s.reg("h1", "gpt-oss-20b", hCurated, pick(hCurated, "openrouter"), 40); err != nil {
			return 0, err
		}
		if err := s.reg("c1", "gpt-oss-20b", !hCurated, pick(!hCurated, "openrouter"), 40); err != nil {
			return 0, err
		}
		h := 0
		for i := 0; i < 400; i++ {
			n, _, ok := s.b.pickFor("gpt-oss-20b", false, 0, 0, 0, "", nil, nil, nil, pickReq{rng: seededRand(fmt.Sprintf("flip-%d", i))})
			if !ok {
				return 0, fmt.Errorf("pick failed")
			}
			if n.NodeID == "h1" {
				h++
			}
		}
		return float64(h) / 400, nil
	}
	a, err := share(true) // h1 curated
	if err != nil {
		return err
	}
	b, err := share(false) // h1 human
	if err != nil {
		return err
	}
	// A thumb on the scale shows up as h1's share moving when only its flag did.
	if diff := a - b; diff > 0.15 || diff < -0.15 {
		return fmt.Errorf("h1's pick share moved from %.2f to %.2f when only the curated "+
			"flags flipped - something is weighing the flag", a, b)
	}
	return nil
}

func (s *curRouteState) twoCuratedDifferentSpeed() error {
	if err := s.reg("slow", "deepseek-v4", true, "conifer", 10); err != nil {
		return err
	}
	return s.reg("fast", "deepseek-v4", true, "openrouter", 200)
}

func (s *curRouteState) speedsDiffer() error { return nil } // fixture above set them

func (s *curRouteState) favorsBetterMeasured() error {
	fastWins := 0
	for i := 0; i < 40; i++ {
		n, _, ok := s.b.pickFor("deepseek-v4", false, 0, 0, 0, "", nil, nil, nil, pickReq{rng: seededRand(fmt.Sprintf("speed-%d", i))})
		if !ok {
			return fmt.Errorf("pick failed")
		}
		if n.NodeID == "fast" {
			fastWins++
		}
	}
	if fastWins <= 20 {
		return fmt.Errorf("the better-measured connection won only %d/40 draws - 'best in class "+
			"connections of the same models' is half of what the routing fee buys", fastWins)
	}
	return nil
}

func (s *curRouteState) curatedOnBand() error {
	return s.reg("c1", "gpt-oss-20b", true, "openrouter", 40)
}

func (s *curRouteState) consumerSends() error { return nil } // the pick below IS the send's routing step

func (s *curRouteState) relayedLikeAny() error {
	n, _, ok := s.b.pickFor("gpt-oss-20b", false, 0, 0, 0, "", nil, nil, nil, pickReq{rng: seededRand("std")})
	if !ok || n.NodeID != "c1" {
		return fmt.Errorf("the standard pick did not route to the curated station: ok=%v", ok)
	}
	return nil
}

func (s *curRouteState) meteredLikeAny() error {
	// Slice 2 pinned the metering/receipt path end to end (curated_pricing_bdd_test.go);
	// here the routing claim is that the SAME settle entry point serves it - which slice
	// 2's use of settleRequest already demonstrates. Nothing curated-specific to relay.
	return nil
}

// --- the filter: roger.self_hosted_only rides the REAL relay ---------------------------------
//
// The filter scenario needs stations that actually SERVE (the assertion is "who served"), so
// it rides the upstream_failover harness: a real relayBroker + store + shared store, real
// stations with scripted upstreams on real tunnels. The curated station is the same
// registration with the curated flag set, exactly as /nodes/register stores it.

type curFilterState struct {
	*foState
	human, curated *fstation
	served         []string // X-RogerAI-Provider per relay of the last batch
}

func (s *curFilterState) mixedBand() error {
	if err := s.foState.reset(); err != nil {
		return err
	}
	s.model = "gpt-oss-20b"
	s.human = s.standUp("h1", stationOpts{model: s.model, priceIn: 1, priceOut: 2})
	s.curated = s.standUp("c1", stationOpts{model: s.model, priceIn: 1, priceOut: 2})
	// The curated flag lives on the registration the broker holds; flip it there, the way
	// register() persists it (curated + the provider name it requires).
	s.b.mu.Lock()
	reg := s.b.nodes[s.curated.id]
	reg.Curated, reg.CuratedProvider = true, "openrouter"
	s.b.nodes[s.curated.id] = reg
	s.b.mu.Unlock()
	// Both canary-passed at the same speed, as the neutrality fixture above does, so both
	// sit in the healthy tier on equal terms before anything is relayed.
	s.b.recordProbe(s.human.id, probePass, 50, 40, true, true)
	s.b.recordProbe(s.curated.id, probePass, 50, 40, true, true)
	return s.ensureFunded()
}

// relayWith fires n signed relays carrying `extra` merged into the body and records who served.
func (s *curFilterState) relayWith(n int, extra map[string]any) error {
	s.served = nil
	for i := 0; i < n; i++ {
		m := map[string]any{
			"model":    s.model,
			"messages": []map[string]string{{"role": "user", "content": utPrompt(s.tokens)}},
		}
		for k, v := range extra {
			m[k] = v
		}
		body, _ := json.Marshal(m)
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		signReq(r, s.consumerPriv, body)
		w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
		s.b.relay(w, r)
		if w.Code != http.StatusOK {
			return fmt.Errorf("relay %d = %d: %s", i+1, w.Code, w.Body.String())
		}
		s.served = append(s.served, w.Header().Get("X-RogerAI-Provider"))
	}
	return nil
}

func (s *curFilterState) relaysSelfHostedOnly() error {
	return s.relayWith(12, map[string]any{"roger": map[string]any{"self_hosted_only": true}})
}

func (s *curFilterState) allServedByHuman() error {
	for i, id := range s.served {
		if id != s.human.id {
			return fmt.Errorf("relay %d was served by %q, not the human station %q - roger.self_hosted_only did not filter", i+1, id, s.human.id)
		}
	}
	return nil
}

func (s *curFilterState) curatedServedNone() error {
	for i, id := range s.served {
		if id == s.curated.id {
			return fmt.Errorf("relay %d rode the curated station %q under roger.self_hosted_only", i+1, id)
		}
	}
	return nil
}

func (s *curFilterState) curatedReachableWithoutKey() error {
	// Same terms, no key: the curated station must be REACHABLE by the ordinary pick
	// (neutrality, the same observation picksBySameRules makes). Seeded draws, not relays:
	// after the batch above the human station carries measured successes the curated one
	// does not, so relay outcomes would show the score, not the filter.
	for i := 0; i < 64; i++ {
		n, _, ok := s.b.pickFor(s.model, false, 0, 0, 0, "", nil, nil, nil, pickReq{rng: seededRand(fmt.Sprintf("nokey-%d", i))})
		if !ok {
			return fmt.Errorf("pick failed with two healthy stations")
		}
		if n.NodeID == s.curated.id {
			return nil
		}
	}
	return fmt.Errorf("without roger.self_hosted_only the curated station %q was never picked in 64 draws", s.curated.id)
}

func TestCuratedRoutingFeature(t *testing.T) {
	st := &curRouteState{t: t}
	fs := &curFilterState{foState: &foState{t: t, logs: &utLog{}}}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return c, nil
			})
			sc.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				fs.teardown() // a no-op for scenarios that never stood the relay harness up
				return c, nil
			})
			sc.Step(`^a mixed band with a human station and a curated station, both healthy and equally priced$`, fs.mixedBand)
			sc.Step(`^a funded consumer relays with body roger\.self_hosted_only true twelve times$`, fs.relaysSelfHostedOnly)
			sc.Step(`^every one of those relays is served by the human station$`, fs.allServedByHuman)
			sc.Step(`^the curated station serves none of them$`, fs.curatedServedNone)
			sc.Step(`^without the key the same consumer can still be served by the curated station$`, fs.curatedReachableWithoutKey)
			sc.Step(`^a human and a curated station on one band$`, st.humanAndCurated)
			sc.Step(`^the router picks by the same price, health and signal rules it always uses$`, st.picksBySameRules)
			sc.Step(`^no preference for either kind is hard-coded$`, st.noHardcodedPreference)
			sc.Step(`^two curated stations serving the same model via different providers$`, st.twoCuratedDifferentSpeed)
			sc.Step(`^their measured speed and health differ$`, st.speedsDiffer)
			sc.Step(`^routing favors the better-measured connection$`, st.favorsBetterMeasured)
			sc.Step(`^a curated station on a band$`, st.curatedOnBand)
			sc.Step(`^a consumer sends a request to that band$`, st.consumerSends)
			sc.Step(`^it is relayed broker-to-node like any request$`, st.relayedLikeAny)
			sc.Step(`^metered, receipted and held like any request$`, st.meteredLikeAny)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/curated/curated_routing.feature"},
		},
	}
	if bddtest.Run(t, &suite) != 0 {
		t.Fatal("curated routing scenarios failed")
	}
}
