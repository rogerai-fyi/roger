package main

// discover_liveness_bdd_test.go makes features/multinode/discover_liveness.feature EXECUTABLE:
// the RESIDUAL cross-instance /discover ONLINE flicker that the registry union (task #52 / PR
// #11) did NOT fix. Two REAL broker instances (A and B) share ONE miniredis + ONE store + ONE
// broker signing key; a node registers + heartbeats + polls on A; B mirrors it via the shared
// registry + liveness sync. The scenarios assert on computeDiscover/computeMarket DIRECTLY (not
// the serveCachedJSON HTTP path, whose shared response cache would mask a per-instance flip).
// Real signed registrations; no mocks.

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

	"github.com/alicebob/miniredis/v2"
	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

const flModel = "free-m"

type flState struct {
	mr       *miniredis.Miniredis
	db       store.Store
	priv     ed25519.PrivateKey // broker signing key (shared by A + B)
	a, b     *broker
	nodePriv ed25519.PrivateKey
	token    string
	offersA  []offerView
	offersB  []offerView
	marketA  []marketView
	marketB  []marketView
}

func (s *flState) reset(t *testing.T) {
	s.mr = miniredis.RunT(t)
	_, s.priv, _ = ed25519.GenerateKey(nil)
	s.db = store.NewMem()
	s.a = newMIBroker(t, s.priv, s.db, s.mr)
	s.b = newMIBroker(t, s.priv, s.db, s.mr)
	s.token = "tok-flick-1"
}

// --- helpers ---------------------------------------------------------------

func (s *flState) discover(b *broker) []offerView {
	res := b.computeDiscover().(map[string]any)
	out, _ := res["offers"].([]offerView)
	return out
}

func (s *flState) market(b *broker) []marketView {
	res := b.computeMarket().(map[string]any)
	out, _ := res["market"].([]marketView)
	return out
}

func onlineOf(offers []offerView, node string) (offerView, bool) {
	for _, o := range offers {
		if o.NodeID == node {
			return o, true
		}
	}
	return offerView{}, false
}

func providerFor(mkt []marketView, model string) (marketView, bool) {
	for _, m := range mkt {
		if m.Model == model {
			return m, true
		}
	}
	return marketView{}, false
}

func (s *flState) setProbeFails(b *broker, node string, n int) {
	b.metricsMu.Lock()
	tq := b.trust[node]
	tq.probed = true
	tq.probeFails = n
	b.trust[node] = tq
	b.metricsMu.Unlock()
}

// hostPoll drives ONE real /agent/poll on the instance with an already-canceled request
// context, so agentPoll authenticates, marks the node seen, records the LOCAL poll (the fix's
// poll-host signal), then returns 204 at once instead of long-polling for 25s. This makes the
// instance the node's authoritative poll host exactly as a live long-poll would.
func (s *flState) hostPoll(b *broker, node string) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // return immediately after the markSeen + poll-host stamp, before the 25s wait
	r := httptest.NewRequest(http.MethodGet, "/agent/poll?node="+node, nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+s.token)
	w := httptest.NewRecorder()
	b.agentPoll(w, r)
}

// --- Background ------------------------------------------------------------

func (s *flState) twoInstances() error { return nil } // pair built in reset()

func (s *flState) registersHeartbeatsPollsOnA(node string) error {
	_, s.nodePriv, _ = ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(s.nodePriv.Public().(ed25519.PublicKey))
	reg := protocol.NodeRegistration{
		NodeID: node, PubKey: pubHex, BridgeToken: s.token, HW: "test-hw",
		Offers: []protocol.ModelOffer{{Model: flModel, Ctx: 4096}},
		TS:     time.Now().Unix(),
	}
	reg.SignRegistration(s.nodePriv)
	body, _ := json.Marshal(reg)
	w := httptest.NewRecorder()
	s.a.register(w, httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body)))
	if w.Code != http.StatusOK {
		return fmt.Errorf("register on A: %d %s", w.Code, w.Body.String())
	}
	s.a.markSeen(node)    // heartbeat: write-through to the shared liveness
	s.hostPoll(s.a, node) // long-poll: A becomes the node's authoritative poll host
	return nil
}

func (s *flState) bMirrorsAndSyncs() error {
	s.b.syncLivenessOnce() // registry union + liveness merge from the shared backend
	return nil
}

// --- Scenario steps --------------------------------------------------------

func (s *flState) bStreakNoPoll(node string) error {
	s.setProbeFails(s.b, node, probeDeadStreak) // B's local probe streak crosses the dead bar
	// B never polled the node, so it does NOT host the node's poll (b.localPollAt unset).
	return nil
}

func (s *flState) getDiscoverB() error { s.offersB = s.discover(s.b); return nil }

func (s *flState) bListsOnline(node string) error {
	o, ok := onlineOf(s.offersB, node)
	if !ok {
		return fmt.Errorf("instance B must LIST node %q on /discover (registry union)", node)
	}
	if !o.Online {
		return fmt.Errorf("instance B must report node %q ONLINE (it heartbeats on A); got Online=false - the residual flicker", node)
	}
	return nil
}

func (s *flState) bAtLeastOneOnline() error {
	n := 0
	for _, o := range s.offersB {
		if o.Online {
			n++
		}
	}
	if n < 1 {
		return fmt.Errorf("instance B /discover collapsed to %d online offers (the ~20%% flicker)", n)
	}
	return nil
}

func (s *flState) bRepeatedAcrossThrottleWithProbe(node string) error {
	// Simulate B browsing /discover repeatedly across the 20s durable-write throttle window,
	// attempting a probe on B each round. B hosts no poller, so its bus probe finds no
	// subscriber and skips (errNoPoller) - probeFails stays at the streak we set. B must never
	// flip the live node to zero-online.
	for round := 0; round < 6; round++ {
		s.b.probeNode(s.b.nodes[node], flModel, canaryFingerprints[round%len(canaryFingerprints)])
		s.b.syncLivenessOnce()
		offers := s.discover(s.b)
		o, ok := onlineOf(offers, node)
		if !ok || !o.Online {
			return fmt.Errorf("round %d: instance B flipped node %q OFFLINE (listed=%v)", round, node, ok)
		}
	}
	return nil
}

func (s *flState) aStreak(node string) error {
	s.setProbeFails(s.a, node, probeDeadStreak) // A is the poll host: its streak IS authoritative
	return nil
}

func (s *flState) getDiscoverA() error { s.offersA = s.discover(s.a); return nil }

func (s *flState) aListsOffline(node string) error {
	o, ok := onlineOf(s.offersA, node)
	if !ok {
		return fmt.Errorf("instance A must still LIST node %q on /discover", node)
	}
	if o.Online {
		return fmt.Errorf("the poll-hosting instance A must surface a probe-dead node %q as OFFLINE", node)
	}
	return nil
}

func (s *flState) bLocalStalePastTTL(node string) error {
	s.b.mu.Lock()
	s.b.lastSeen[node] = time.Now().Add(-(nodeTTL + 10*time.Second))
	s.b.mu.Unlock()
	return nil
}

func (s *flState) sharedFreshFromA(node string) error {
	return s.a.shared.markSeen(node, time.Now()) // A's heartbeat write-through keeps shared fresh
}

func (s *flState) bSyncsThenDiscoverMarket() error {
	s.b.syncLivenessOnce()
	s.offersB = s.discover(s.b)
	s.marketB = s.market(s.b)
	return nil
}

func (s *flState) bMarketHasProvider(node string) error {
	m, ok := providerFor(s.marketB, flModel)
	if !ok || m.Providers < 1 {
		return fmt.Errorf("instance B /market must list a provider for %q (live via shared liveness); got ok=%v providers=%d", flModel, ok, m.Providers)
	}
	return nil
}

func (s *flState) staleBothInstances(node string) error {
	old := time.Now().Add(-2 * nodeTTL)
	s.a.mu.Lock()
	s.a.lastSeen[node] = old
	s.a.mu.Unlock()
	s.b.mu.Lock()
	s.b.lastSeen[node] = old
	s.b.mu.Unlock()
	// Age the SHARED liveness too, so a sync cannot refresh either instance back to live.
	_ = s.a.shared.markSeen(node, old)
	return nil
}

func (s *flState) getDiscoverMarketBoth() error {
	s.b.syncLivenessOnce()
	s.offersA = s.discover(s.a)
	s.marketA = s.market(s.a)
	s.offersB = s.discover(s.b)
	s.marketB = s.market(s.b)
	return nil
}

func (s *flState) bListsOfflineOf(node string) error {
	o, ok := onlineOf(s.offersB, node)
	if ok && o.Online {
		return fmt.Errorf("instance B must age node %q OUT (OFFLINE) once heartbeats stop", node)
	}
	return nil
}

func (s *flState) neitherMarketHasProvider(node string) error {
	if _, ok := providerFor(s.marketA, flModel); ok {
		return fmt.Errorf("instance A /market must drop a dead node's model %q", flModel)
	}
	if _, ok := providerFor(s.marketB, flModel); ok {
		return fmt.Errorf("instance B /market must drop a dead node's model %q", flModel)
	}
	return nil
}

func TestDiscoverLivenessBDD(t *testing.T) {
	st := &flState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset(t)
				return ctx, nil
			})
			sc.Step(`^two broker instances A and B share one store and one shared backend, multi-instance ON$`, st.twoInstances)
			sc.Step(`^node "([^"]*)" registers on instance A and heartbeats and polls there$`, st.registersHeartbeatsPollsOnA)
			sc.Step(`^instance B mirrors the registry and syncs liveness from the shared backend$`, st.bMirrorsAndSyncs)

			sc.Step(`^instance B has accumulated a sustained probe-fail streak for "([^"]*)" and does not host its poll$`, st.bStreakNoPoll)
			sc.Step(`^a consumer GETs /discover on instance B$`, st.getDiscoverB)
			sc.Step(`^instance B lists "([^"]*)" as ONLINE$`, st.bListsOnline)
			sc.Step(`^instance B reports at least one online offer$`, st.bAtLeastOneOnline)

			sc.Step(`^instance B computes /discover repeatedly across the shared-write throttle window with a probe attempted each round$`, func() error { return st.bRepeatedAcrossThrottleWithProbe("st-alpha") })
			sc.Step(`^every /discover on instance B lists "([^"]*)" as ONLINE$`, func(string) error { return nil }) // asserted inside the loop above

			sc.Step(`^instance A has accumulated a sustained probe-fail streak for "([^"]*)"$`, st.aStreak)
			sc.Step(`^a consumer GETs /discover on instance A$`, st.getDiscoverA)
			sc.Step(`^instance A lists "([^"]*)" as OFFLINE$`, st.aListsOffline)

			sc.Step(`^instance B's local last-seen for "([^"]*)" has aged past the node TTL$`, st.bLocalStalePastTTL)
			sc.Step(`^the shared liveness for "([^"]*)" is fresh from instance A$`, st.sharedFreshFromA)
			sc.Step(`^instance B syncs liveness and a consumer GETs /discover and /market on instance B$`, st.bSyncsThenDiscoverMarket)
			sc.Step(`^instance B's /market lists a provider for "([^"]*)"'s model$`, st.bMarketHasProvider)

			sc.Step(`^"([^"]*)"'s last-seen has aged past the node TTL on both instances$`, st.staleBothInstances)
			sc.Step(`^a consumer GETs /discover and /market on instance A and instance B$`, st.getDiscoverMarketBoth)
			sc.Step(`^instance B lists "([^"]*)" as OFFLINE$`, st.bListsOfflineOf)
			sc.Step(`^neither instance's /market lists a provider for "([^"]*)"'s model$`, st.neitherMarketHasProvider)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/multinode/discover_liveness.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("discover_liveness.feature scenarios failed")
	}
}
