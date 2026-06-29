package main

// on_air_bdd_test.go makes features/sharing/on_air.feature EXECUTABLE, driving the TWO
// REAL halves of "going on air" (no business logic mocked):
//
//   - the node side: a real internal/node.Controller (ToggleOnAir / MaxOnAir / the soft
//     on-air cap / the priced-share login gate). Its only network peer is a tiny httptest
//     "ok" broker so a session can really start/heartbeat - the SAME stand-in the committed
//     internal/node/controller_test.go uses; the controller's own logic is exercised for real.
//   - the broker side: a real *broker (register / heartbeat / pick / discover / the per-owner
//     on-air cap via ownerOnAirCount) on a Mem store, driven exactly like band_test.go.
//
// A station that toggles off (or stops heartbeating) ages out of routing within nodeTTL; the
// broker has no explicit deregister, so off-air == stale, which scenarios 2 and 3 exercise.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/node"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

type onAirState struct {
	t       *testing.T
	fakeURL string // tiny "ok" broker the controller heartbeats against

	// node-controller half
	ctrl  *node.Controller
	model string
	res   node.ToggleResult

	// broker half
	b          *broker
	userPriv   ed25519.PrivateKey
	nodePriv   ed25519.PrivateKey
	nodePubHex string
	regCode    int
	regMsg     string
}

func (s *onAirState) reset() {
	s.b, s.userPriv, s.nodePriv, s.nodePubHex = newBandBroker(s.t)
	s.ctrl = nil
	s.model = ""
	s.res = node.ToggleResult{}
	s.regCode, s.regMsg = 0, ""
}

// newController builds a real Controller wired to the "ok" fake broker, with one free
// ShareRow per model so a free toggle goes on air without a login.
func (s *onAirState) newController(maxOnAir int, models ...string) *node.Controller {
	c := node.New(node.Config{Broker: s.fakeURL, Station: "amber-fox", MaxOnAir: maxOnAir})
	rows := make([]node.ShareRow, 0, len(models))
	for _, m := range models {
		rows = append(rows, node.ShareRow{Model: m, Ctx: 8192, Upstream: "http://127.0.0.1:0/v1/chat/completions"})
	}
	c.SetRows(rows)
	return c
}

// --- broker-side helpers ----------------------------------------------------

func (s *onAirState) registerFree(nodeID, model string) (int, string) {
	return registerWith(s.t, s.b, nodeID, s.nodePriv, s.nodePubHex, s.userPriv, false,
		protocol.ModelOffer{Model: model, Ctx: 8192}, false, false)
}

func (s *onAirState) sendHeartbeat(nodeID, token string) int {
	body, _ := json.Marshal(map[string]string{"node_id": nodeID})
	r := httptest.NewRequest(http.MethodPost, "/nodes/heartbeat", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.b.heartbeat(w, r)
	return w.Code
}

// discoverOffer reports whether nodeID's offer for model is present in a /discover body
// and, if so, its live Online flag. /discover never DROPS a stale node - it lists it with
// Online=false - so "dropped from discovery" means "no longer an ONLINE station".
func discoverOffer(body, nodeID, model string) (found, online bool) {
	var d struct {
		Offers []offerView `json:"offers"`
	}
	_ = json.Unmarshal([]byte(body), &d)
	for _, o := range d.Offers {
		if o.NodeID == nodeID && o.Model == model {
			return true, o.Online
		}
	}
	return false, false
}

// discoverBody returns the LIVE /discover payload via the real computeDiscover (the function
// GET /discover serves), bypassing the 3s in-process HTTP TTL cache so a before/after liveness
// check within one scenario reflects the current state rather than cached bytes.
func (s *onAirState) discoverBody() string {
	blob, _ := json.Marshal(s.b.computeDiscover())
	return string(blob)
}

func (s *onAirState) assertRoutable(model, nodeID string) error {
	found, online := discoverOffer(s.discoverBody(), nodeID, model)
	if !found || !online {
		return fmt.Errorf("/discover: %s for %q found=%v online=%v, want a listed ONLINE station", nodeID, model, found, online)
	}
	s.b.mu.Lock()
	n, _, ok := s.b.pickFor(model, false, 0, 0, 50, "", nil, nil, nil, pickReq{})
	s.b.mu.Unlock()
	if !ok || n.NodeID != nodeID {
		return fmt.Errorf("pick(%q) = %+v ok=%v, want it to route to %s", model, n, ok, nodeID)
	}
	return nil
}

func (s *onAirState) assertNotRoutable(model, nodeID string) error {
	// routing: pick must not select the stale node.
	s.b.mu.Lock()
	n, _, ok := s.b.pickFor(model, false, 0, 0, 50, "", nil, nil, nil, pickReq{})
	s.b.mu.Unlock()
	if ok && n.NodeID == nodeID {
		return fmt.Errorf("pick still routes %q to off-air %s", model, nodeID)
	}
	// discovery: it is no longer a LIVE station (absent, or listed with Online=false).
	if found, online := discoverOffer(s.discoverBody(), nodeID, model); found && online {
		return fmt.Errorf("/discover still lists %s for %q as ONLINE after it went stale", nodeID, model)
	}
	return nil
}

func (s *onAirState) ageOut(nodeID string) {
	s.b.mu.Lock()
	s.b.lastSeen[nodeID] = time.Now().Add(-2 * nodeTTL)
	s.b.mu.Unlock()
}

// --- scenario 1: toggle on air -> routable ----------------------------------

func (s *onAirState) operatorDetectedModel(model string) error {
	s.model = model
	s.ctrl = s.newController(0, model)
	return nil
}

func (s *onAirState) togglesOnAir(model string) error {
	s.res = s.ctrl.ToggleOnAir(model)
	if s.res.Err != nil || s.res.AtLimit || s.res.LoginNeeded || s.res.WentOff {
		return fmt.Errorf("toggle %q on air = %+v, want a clean on-air", model, s.res)
	}
	if s.ctrl.OnAirCount() != 1 {
		return fmt.Errorf("on-air count = %d, want 1", s.ctrl.OnAirCount())
	}
	return nil
}

func (s *onAirState) nodeRegisters() error {
	if c, msg := s.registerFree("n1", s.model); c != http.StatusOK {
		return fmt.Errorf("free register = %d, want 200; msg=%q", c, msg)
	}
	return nil
}

func (s *onAirState) appearsLiveRoutable(model string) error { return s.assertRoutable(model, "n1") }

// --- scenario 2: toggle off air -> no longer routed -------------------------

func (s *onAirState) operatorOnAirFor(model string) error {
	s.model = model
	s.ctrl = s.newController(0, model)
	if r := s.ctrl.ToggleOnAir(model); r.Err != nil || !(s.ctrl.OnAirCount() == 1) {
		return fmt.Errorf("setup on air = %+v, count=%d", r, s.ctrl.OnAirCount())
	}
	if c, msg := s.registerFree("n1", model); c != http.StatusOK {
		return fmt.Errorf("register = %d msg=%q", c, msg)
	}
	return s.assertRoutable(model, "n1") // it WAS routable before going off air
}

func (s *onAirState) togglesItOffAir() error {
	s.res = s.ctrl.ToggleOnAir(s.model)
	if !s.res.WentOff || s.ctrl.OnAirCount() != 0 {
		return fmt.Errorf("toggle off = %+v, on-air=%d, want WentOff + 0", s.res, s.ctrl.OnAirCount())
	}
	s.ageOut("n1") // an off-air node stops heartbeating -> it ages out of routing
	return nil
}

func (s *onAirState) brokerNoLongerRoutes(model string) error {
	return s.assertNotRoutable(model, "n1")
}

// --- scenario 3: stale TTL drop + heartbeat recovery ------------------------

func (s *onAirState) nodeOnAirSeenJustNow(model string) error {
	s.model = model
	if c, msg := s.registerFree("n1", model); c != http.StatusOK {
		return fmt.Errorf("register = %d msg=%q", c, msg)
	}
	return s.assertRoutable(model, "n1")
}

func (s *onAirState) ttlPassesNoHeartbeat() error { s.ageOut("n1"); return nil }

func (s *onAirState) droppedStale() error { return s.assertNotRoutable(s.model, "n1") }

func (s *onAirState) freshHeartbeatBringsBack() error {
	if c := s.sendHeartbeat("n1", "tok"); c != http.StatusOK {
		return fmt.Errorf("heartbeat = %d, want 200 (a fresh beat must revive the station)", c)
	}
	return s.assertRoutable(s.model, "n1")
}

// --- scenario 4: the soft MaxOnAir cap (node controller) --------------------

func (s *onAirState) nodeAtMaxOnAir(cap int, model string) error {
	s.model = model
	s.ctrl = s.newController(cap, model, "second-model")
	if r := s.ctrl.ToggleOnAir(model); r.Err != nil || r.AtLimit || r.LoginNeeded {
		return fmt.Errorf("first on air = %+v", r)
	}
	if s.ctrl.OnAirCount() != 1 {
		return fmt.Errorf("after first toggle, on-air = %d, want 1", s.ctrl.OnAirCount())
	}
	return nil
}

func (s *onAirState) togglesSecondModel() error {
	s.res = s.ctrl.ToggleOnAir("second-model")
	return nil
}

func (s *onAirState) toggleRefusedAtLimit() error {
	if !s.res.AtLimit {
		return fmt.Errorf("second toggle at the cap = %+v, want AtLimit", s.res)
	}
	return nil
}

func (s *onAirState) firstStaysOnAir() error {
	if s.ctrl.OnAirCount() != 1 {
		return fmt.Errorf("on-air count = %d, want 1 (the cap held; the first model stays on air)", s.ctrl.OnAirCount())
	}
	if _, on := s.ctrl.Headline(); !on {
		return fmt.Errorf("headline reports off air; the first model should still be on air")
	}
	return nil
}

// --- scenario 5: the per-owner on-air cap (broker) --------------------------

func (s *onAirState) operatorAtPerOwnerCap() error {
	s.b.maxNodesPerOwner = 1 // one band on air per account, for the test
	code, msg := registerWith(s.t, s.b, "n1", s.nodePriv, s.nodePubHex, s.userPriv, true,
		protocol.ModelOffer{Model: "m1", Ctx: 8192, PriceOut: 5}, false, false)
	if code != http.StatusOK {
		return fmt.Errorf("first owner-bound node register = %d, want 200; msg=%q", code, msg)
	}
	return nil
}

func (s *onAirState) bringsAnotherModelOnAir() error {
	s.regCode, s.regMsg = registerWith(s.t, s.b, "n2", s.nodePriv, s.nodePubHex, s.userPriv, true,
		protocol.ModelOffer{Model: "m2", Ctx: 8192, PriceOut: 5}, false, false)
	return nil
}

func (s *onAirState) ownerOnAirCountBlocks() error {
	if s.regCode != http.StatusTooManyRequests {
		return fmt.Errorf("over-cap register = %d, want 429; msg=%q", s.regCode, s.regMsg)
	}
	owner := hex.EncodeToString(s.userPriv.Public().(ed25519.PublicKey))
	s.b.mu.Lock()
	cnt := s.b.ownerOnAirCount(owner, "n2")
	s.b.mu.Unlock()
	if cnt < s.b.maxNodesPerOwner {
		return fmt.Errorf("ownerOnAirCount = %d, want >= cap %d (the cap that blocked the new band)", cnt, s.b.maxNodesPerOwner)
	}
	return nil
}

// --- scenario 6: registration admits priced offers --------------------------

func (s *onAirState) registersPricedOffer(model string) error {
	s.model = model
	s.regCode, s.regMsg = registerWith(s.t, s.b, "n1", s.nodePriv, s.nodePubHex, s.userPriv, true,
		protocol.ModelOffer{Model: model, Ctx: 8192, PriceIn: 0.20, PriceOut: 0.30}, false, false)
	return nil
}

func (s *onAirState) brokerRecordsPricedOffer() error {
	if s.regCode != http.StatusOK {
		return fmt.Errorf("priced register = %d, want 200; msg=%q", s.regCode, s.regMsg)
	}
	s.b.mu.Lock()
	reg, ok := s.b.nodes["n1"]
	s.b.mu.Unlock()
	if !ok {
		return fmt.Errorf("broker did not record node n1")
	}
	if len(reg.Offers) != 1 || reg.Offers[0].Model != s.model || reg.Offers[0].PriceIn != 0.20 || reg.Offers[0].PriceOut != 0.30 {
		return fmt.Errorf("recorded offer = %+v, want %s in 0.20 out 0.30", reg.Offers, s.model)
	}
	return nil
}

func (s *onAirState) offerEligibleForRouting() error { return s.assertRoutable(s.model, "n1") }

// --- scenario 7: free needs no login; earning requires an account -----------

func (s *onAirState) sharesFreeNoLogin() error {
	s.model = "gpt-oss-20b"
	s.regCode, s.regMsg = s.registerFree("free1", s.model)
	return nil
}

func (s *onAirState) canGoOnAirAndServe() error {
	if s.regCode != http.StatusOK {
		return fmt.Errorf("free register = %d, want 200 (free sharing needs no login); msg=%q", s.regCode, s.regMsg)
	}
	return s.assertRoutable(s.model, "free1")
}

func (s *onAirState) earningRequiresLinkedAccount() error {
	code, msg := registerWith(s.t, s.b, "earn1", s.nodePriv, s.nodePubHex, s.userPriv, false,
		protocol.ModelOffer{Model: "earn-model", Ctx: 8192, PriceOut: 5}, false, false)
	if code == http.StatusOK {
		return fmt.Errorf("priced (earning) register WITHOUT login was accepted (200) - earning must require a linked account")
	}
	if code != http.StatusUnauthorized && code != http.StatusForbidden {
		return fmt.Errorf("earning-without-login register = %d, want 401/403; msg=%q", code, msg)
	}
	return nil
}

func TestOnAirBDD(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer fake.Close()

	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &onAirState{t: t, fakeURL: fake.URL}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				if st.ctrl != nil {
					st.ctrl.StopAll()
				}
				return ctx, err
			})
			// scenario 1
			sc.Step(`^an operator with model "([^"]*)" detected locally$`, st.operatorDetectedModel)
			sc.Step(`^the operator toggles "([^"]*)" on air$`, st.togglesOnAir)
			sc.Step(`^the node registers with the broker$`, st.nodeRegisters)
			sc.Step(`^"([^"]*)" appears as a live station the broker can route to$`, st.appearsLiveRoutable)
			// scenario 2
			sc.Step(`^an operator is on air for "([^"]*)"$`, st.operatorOnAirFor)
			sc.Step(`^the operator toggles it off air$`, st.togglesItOffAir)
			sc.Step(`^the broker no longer routes "([^"]*)" to that node$`, st.brokerNoLongerRoutes)
			// scenario 3
			sc.Step(`^a node on air for "([^"]*)" that was last seen just now$`, st.nodeOnAirSeenJustNow)
			sc.Step(`^more than nodeTTL passes with no heartbeat$`, st.ttlPassesNoHeartbeat)
			sc.Step(`^the node is dropped from routing and discovery \(stale\)$`, st.droppedStale)
			sc.Step(`^a fresh heartbeat brings it back$`, st.freshHeartbeatBringsBack)
			// scenario 4
			sc.Step(`^a node whose MaxOnAir is (\d+) and is already on air for "([^"]*)"$`, st.nodeAtMaxOnAir)
			sc.Step(`^the operator tries to toggle a second model on air$`, st.togglesSecondModel)
			sc.Step(`^the toggle is refused \(at the on-air limit\)$`, st.toggleRefusedAtLimit)
			sc.Step(`^the first model stays on air$`, st.firstStaysOnAir)
			// scenario 5
			sc.Step(`^an operator already at the per-owner on-air cap across their nodes$`, st.operatorAtPerOwnerCap)
			sc.Step(`^they bring another model on air$`, st.bringsAnotherModelOnAir)
			sc.Step(`^the broker's ownerOnAirCount blocks it \(one operator can't monopolize the dial\)$`, st.ownerOnAirCountBlocks)
			// scenario 6
			sc.Step(`^a node registers offering "([^"]*)" at in \$0\.20/1M, out \$0\.30/1M$`, st.registersPricedOffer)
			sc.Step(`^the broker records the node \+ its priced offer$`, st.brokerRecordsPricedOffer)
			sc.Step(`^the offer is eligible for routing \(subject to the routing gates\)$`, st.offerEligibleForRouting)
			// scenario 7
			sc.Step(`^an operator shares FREE \(no login\)$`, st.sharesFreeNoLogin)
			sc.Step(`^they can go on air and serve traffic$`, st.canGoOnAirAndServe)
			sc.Step(`^to EARN they must link a GitHub account \(the payout path\)$`, st.earningRequiresLinkedAccount)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/sharing/on_air.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("sharing / on-air behavior scenarios failed (see godog output above)")
	}
}
