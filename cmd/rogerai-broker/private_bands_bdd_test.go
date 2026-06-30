package main

// private_bands_bdd_test.go makes features/multinode/private_bands.feature EXECUTABLE,
// driving the REAL private-band cross-instance path over TWO broker instances sharing ONE
// in-process Valkey (miniredis) + ONE store. No mocks: it calls the real register handler,
// the real syncLivenessOnce/syncRegistry mirror, the real bandOffers resolve, pickFor route,
// computeDiscover, and tunnelFor — so the scenarios exercise the same code paths as the live
// broker behind the load balancer.
//
// THE INVARIANT IT LOCKS IN: a private band registered on instance A is RESOLVABLE + ROUTABLE
// on instance B, yet NEVER appears in B's public /discover or the public shared registry.

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
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"

	"github.com/cucumber/godog"
)

type pbState struct {
	t        *testing.T
	mr       *miniredis.Miniredis
	db       *store.Mem
	a, b     *broker
	single   *broker
	nodePriv ed25519.PrivateKey
	token    string
}

func (s *pbState) reset(t *testing.T) {
	s.t = t
	s.mr, s.db, s.a, s.b, s.single = nil, nil, nil, nil, nil
	s.nodePriv, s.token = nil, ""
}

// registerPrivate posts an OWNER-SIGNED private registration (offers model "m") through the
// REAL register handler on broker bk — private bands require a GitHub-linked owner, so we
// bind an owner and sign the REQUEST with that owner key (register then mints the band). It
// then marks the node seen so its liveness reaches the shared store (a peer's
// syncLivenessOnce both merges liveness AND mirrors the registry).
func (s *pbState) registerPrivate(bk *broker, node string) error {
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	_, ownerPriv, _ := ed25519.GenerateKey(nil)
	ownerPubHex := hex.EncodeToString(ownerPriv.Public().(ed25519.PublicKey))
	if err := bk.db.BindOwner(store.Owner{GitHubID: 1, Login: "owner-" + node, Pubkey: ownerPubHex}); err != nil {
		return err
	}
	s.nodePriv, s.token = nodePriv, "tok-"+node
	reg := protocol.NodeRegistration{
		NodeID: node, PubKey: hex.EncodeToString(nodePub), BridgeToken: s.token, HW: "test-hw",
		Offers: []protocol.ModelOffer{{Model: "m", Ctx: 4096}}, TS: time.Now().Unix(), Private: true,
	}
	reg.SignRegistration(nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, ownerPriv, body) // owner-authenticate the request (private bands need login)
	w := httptest.NewRecorder()
	bk.register(w, r)
	if w.Code != http.StatusOK {
		return fmt.Errorf("register(private %s) = %d, want 200 (body=%s)", node, w.Code, w.Body.String())
	}
	bk.markSeen(node) // push liveness to the shared store so a peer's sync tick proceeds
	return nil
}

// --- Background / Given ----------------------------------------------------

func (s *pbState) twoInstances() error {
	s.mr = miniredis.RunT(s.t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	s.db = store.NewMem()
	s.a = newMIBroker(s.t, brokerPriv, s.db, s.mr)
	s.b = newMIBroker(s.t, brokerPriv, s.db, s.mr)
	return nil
}

func (s *pbState) privateOnA(node string) error { return s.registerPrivate(s.a, node) }

// privateWithBandOnA registers the owner-signed private node; the REAL register handler mints
// the routing band itself, so BandByNode resolves it on the resolvability assertion.
func (s *pbState) privateWithBandOnA(node string) error { return s.registerPrivate(s.a, node) }

func (s *pbState) bSynced() error { s.b.syncLivenessOnce(); return nil }

func (s *pbState) singleInstanceNoShared() error {
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	s.single = newMIBroker(s.t, brokerPriv, store.NewMem(), nil) // nil mr => shared==nil
	return nil
}

// --- When ------------------------------------------------------------------

func (s *pbState) bRunsRegistrySync() error { s.b.syncLivenessOnce(); return nil }

func (s *pbState) privateOnSingle(node string) error { return s.registerPrivate(s.single, node) }

// --- Then ------------------------------------------------------------------

func (s *pbState) knowsPrivate(bk *broker, node string) error {
	bk.mu.Lock()
	_, known := bk.nodes[node]
	priv := bk.private[node]
	tun := bk.tunnels[node]
	bk.mu.Unlock()
	if !known {
		return fmt.Errorf("broker does not know node %q (private registry not mirrored)", node)
	}
	if !priv {
		return fmt.Errorf("node %q is not flagged private on the broker (would leak into /discover)", node)
	}
	if tun == nil || tun.token != s.token {
		return fmt.Errorf("node %q tunnel/token wrong: tun=%v want token %q", node, tun, s.token)
	}
	return nil
}

func (s *pbState) bKnowsPrivate(node string) error      { return s.knowsPrivate(s.b, node) }
func (s *pbState) singleKnowsPrivate(node string) error { return s.knowsPrivate(s.single, node) }

func (s *pbState) bResolvesBand(node string) error {
	band, found, err := s.db.BandByNode(node)
	if err != nil || !found {
		return fmt.Errorf("band for node %q not found (found=%v err=%v)", node, found, err)
	}
	offers, ok := s.b.bandOffers(band, true, time.Now())
	if !ok || len(offers) == 0 {
		return fmt.Errorf("instance B could not resolve the band to node %q's offers (ok=%v n=%d)", node, ok, len(offers))
	}
	for _, o := range offers {
		if o.NodeID != node {
			return fmt.Errorf("band resolved to a foreign node %q, want %q", o.NodeID, node)
		}
	}
	return nil
}

func (s *pbState) bPicksOnFreq(node, model string) error {
	privateAllow := map[string]bool{node: true}
	got, _, ok := s.b.pickFor(model, false, 0, 0, 0, "", nil, nil, privateAllow, pickReq{})
	if !ok || got.NodeID != node {
		return fmt.Errorf("instance B did not route the band to %q for %q (ok=%v got=%q)", node, model, ok, got.NodeID)
	}
	return nil
}

func (s *pbState) absentFromDiscoverB(node string) error {
	res, _ := s.b.computeDiscover().(map[string]any)
	offers, _ := res["offers"].([]offerView)
	for _, o := range offers {
		if o.NodeID == node {
			return fmt.Errorf("private node %q LEAKED into instance B's public /discover", node)
		}
	}
	return nil
}

func (s *pbState) absentFromPublicRegistry(node string) error {
	regs, err := s.b.shared.allNodes() // the PUBLIC shared registry the /discover mirror reads
	if err != nil {
		return err
	}
	if _, ok := regs[node]; ok {
		return fmt.Errorf("private node %q LEAKED into the public shared registry (allNodes)", node)
	}
	return nil
}

func (s *pbState) bBuildsTunnelOnDemand(node string) error {
	// No sync yet: tunnelFor must lazily learn the private node from the shared private
	// namespace so the node's own poll/result authenticates on B (no re-register storm).
	tun := s.b.tunnelFor(node)
	if tun == nil || tun.token != s.token {
		return fmt.Errorf("instance B could not build node %q's tunnel on demand (tun=%v want token %q)", node, tun, s.token)
	}
	return nil
}

func TestMultinodePrivateBandsBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &pbState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset(t)
				return ctx, nil
			})
			sc.Step(`^two broker instances A and B sharing one Valkey and one store$`, st.twoInstances)
			sc.Step(`^a private node "([^"]*)" is registered on instance A$`, st.privateOnA)
			sc.Step(`^a private node "([^"]*)" with a band is registered on instance A$`, st.privateWithBandOnA)
			sc.Step(`^instance B has synced the registry$`, st.bSynced)
			sc.Step(`^instance B runs its registry sync$`, st.bRunsRegistrySync)
			sc.Step(`^a single-instance broker with no shared backend$`, st.singleInstanceNoShared)
			sc.Step(`^a private node "([^"]*)" is registered on it$`, st.privateOnSingle)
			sc.Step(`^instance B knows node "([^"]*)" as private with its bridge token$`, st.bKnowsPrivate)
			sc.Step(`^that broker knows node "([^"]*)" as private with its bridge token$`, st.singleKnowsPrivate)
			sc.Step(`^instance B resolves the band to node "([^"]*)"'s offers$`, st.bResolvesBand)
			sc.Step(`^instance B picks node "([^"]*)" for model "([^"]*)" on the band frequency$`, st.bPicksOnFreq)
			sc.Step(`^node "([^"]*)" is absent from instance B's /discover$`, st.absentFromDiscoverB)
			sc.Step(`^node "([^"]*)" is absent from the public shared registry$`, st.absentFromPublicRegistry)
			sc.Step(`^instance B builds node "([^"]*)"'s tunnel on demand with its bridge token$`, st.bBuildsTunnelOnDemand)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/multinode/private_bands.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("multinode/private_bands behavior scenarios failed (see godog output above)")
	}
}
