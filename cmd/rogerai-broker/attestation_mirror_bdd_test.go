package main

// attestation_mirror_bdd_test.go makes features/security/attestation_mirror.feature
// EXECUTABLE (P2-5): the cross-instance mirror must carry the broker's attestation
// VERDICT, never the node's raw signed CLAIM. Two REAL broker instances share one
// miniredis (the registry mirror) + one store, registrations go through the REAL
// register() handler with a mock attestation verifier forced to pass/fail, and the
// peer ingests via the REAL syncRegistry()/tunnelFor() paths. No mocks beyond the
// TEE verifier itself (the real one needs AMD hardware).

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

type attMirrorState struct {
	t        *testing.T
	mr       *miniredis.Miniredis
	db       *store.Mem
	a, b     *broker
	require  bool // ROGERAI_TEE_REQUIRE for instance A's registry
	lastCode int
	nodeKeys map[string]ed25519.PrivateKey
	ownerKey ed25519.PrivateKey
	seq      int
}

func (s *attMirrorState) reset(t *testing.T) {
	s.t = t
	s.mr, s.db, s.a, s.b = nil, nil, nil, nil
	s.require, s.lastCode = false, 0
	s.nodeKeys = map[string]ed25519.PrivateKey{}
}

func (s *attMirrorState) twoInstances() error {
	s.mr = miniredis.RunT(s.t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	s.db = store.NewMem()
	s.a = newMIBroker(s.t, brokerPriv, s.db, s.mr)
	s.b = newMIBroker(s.t, brokerPriv, s.db, s.mr)
	return nil
}

func (s *attMirrorState) teeRequire(v int) error {
	s.require = v == 1
	return nil
}

// register drives the REAL register() handler on instance A with a signed registration.
// claim: the node claims Confidential; verifierOK: the mock TEE verdict; private: a
// private-band register (owner-signed, required by the handler).
func (s *attMirrorState) register(node string, claim, verifierOK, private bool) error {
	reg := mockRegistry(verifierOK)
	reg.required = s.require
	s.a.attest = reg

	priv, ok := s.nodeKeys[node]
	if !ok {
		_, priv, _ = ed25519.GenerateKey(nil)
		s.nodeKeys[node] = priv
	}
	s.seq++
	nr := protocol.NodeRegistration{
		NodeID: node, TS: time.Now().Unix(),
		PubKey:      hex.EncodeToString(priv.Public().(ed25519.PublicKey)),
		BridgeToken: fmt.Sprintf("tok-%s-%d", node, s.seq),
		Offers:      []protocol.ModelOffer{{Model: "conf-m", Modality: protocol.ModalityChat}},
		Private:     private,
	}
	if claim {
		nr.Confidential = true
		nr.Attestation = "cXVvdGU=" // any base64; the mock verifier decides
		nr.AttestNonce = reg.issueNonce().Nonce
	}
	nr.SignRegistration(priv)
	body, _ := json.Marshal(nr)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	r.Header.Set("CF-Connecting-IP", "203.0.113.9")
	if private {
		if s.ownerKey == nil {
			_, s.ownerKey, _ = ed25519.GenerateKey(nil)
			opub := hex.EncodeToString(s.ownerKey.Public().(ed25519.PublicKey))
			if err := s.db.BindOwner(store.Owner{GitHubID: 42, Login: "op-att", Pubkey: opub}); err != nil {
				return err
			}
		}
		signReq(r, s.ownerKey, body)
	}
	w := httptest.NewRecorder()
	s.a.register(w, r)
	s.lastCode = w.Code
	return nil
}

func (s *attMirrorState) registersFailing(node string) error {
	return s.register(node, true, false, false)
}
func (s *attMirrorState) registersVerified(node string) error {
	if err := s.register(node, true, true, false); err != nil {
		return err
	}
	if s.lastCode != http.StatusOK {
		return fmt.Errorf("verified register should succeed, got %d", s.lastCode)
	}
	return nil
}
func (s *attMirrorState) registersNoClaim(node string) error {
	return s.register(node, false, true, false)
}
func (s *attMirrorState) registersFailingPrivate(node string) error {
	if err := s.register(node, true, false, true); err != nil {
		return err
	}
	if s.lastCode != http.StatusOK {
		return fmt.Errorf("private register should succeed under require=0, got %d", s.lastCode)
	}
	return nil
}

func (s *attMirrorState) admitted() error {
	if s.lastCode != http.StatusOK {
		return fmt.Errorf("expected the registration admitted (200), got %d", s.lastCode)
	}
	return nil
}

// sharedRecord reads a node's mirrored registration back out of the shared store
// (public namespace unless private is true).
func (s *attMirrorState) sharedRecord(node string, private bool) (protocol.NodeRegistration, bool, error) {
	var regs map[string][]byte
	var err error
	if private {
		regs, err = s.a.shared.allPrivateNodes()
	} else {
		regs, err = s.a.shared.allNodes()
	}
	if err != nil {
		return protocol.NodeRegistration{}, false, err
	}
	raw, ok := regs[node]
	if !ok {
		return protocol.NodeRegistration{}, false, nil
	}
	var reg protocol.NodeRegistration
	if err := json.Unmarshal(raw, &reg); err != nil {
		return protocol.NodeRegistration{}, false, err
	}
	return reg, true, nil
}

func (s *attMirrorState) sharedConfidential(node string, want bool) error {
	reg, ok, err := s.sharedRecord(node, false)
	if err != nil {
		return err
	}
	if !ok {
		// A private-band node lives in the private namespace only.
		if reg, ok, err = s.sharedRecord(node, true); err != nil || !ok {
			return fmt.Errorf("node %s not in the shared store (err=%v)", node, err)
		}
	}
	if reg.Confidential != want {
		return fmt.Errorf("shared-store Confidential for %s = %v, want %v (the mirror must carry the VERDICT)", node, reg.Confidential, want)
	}
	return nil
}

func (s *attMirrorState) sharedConfidentialFalse(node string) error {
	return s.sharedConfidential(node, false)
}
func (s *attMirrorState) sharedConfidentialTrue(node string) error {
	return s.sharedConfidential(node, true)
}

func (s *attMirrorState) bSyncs() {
	s.b.syncRegistry()
}

func (s *attMirrorState) bNeverGrantsTier(node string) error {
	s.bSyncs()
	s.b.mu.Lock()
	conf := s.b.confidential[node]
	s.b.mu.Unlock()
	if conf {
		return fmt.Errorf("instance B granted %s the confidential tier from the mirror", node)
	}
	return nil
}

func (s *attMirrorState) bNeverRoutesConfidential(node string) error {
	s.bSyncs()
	s.b.mu.Lock()
	_, _, ok := s.b.pickFor("conf-m", true, 0, 0, 0, "", nil, nil, nil, pickReq{rng: seededRand("att-mirror")})
	s.b.mu.Unlock()
	if ok {
		return fmt.Errorf("instance B routed confidential-only traffic to %s", node)
	}
	return nil
}

func (s *attMirrorState) bGrantsTier(node string) error {
	s.bSyncs()
	s.b.mu.Lock()
	conf := s.b.confidential[node]
	s.b.mu.Unlock()
	if !conf {
		return fmt.Errorf("instance B should grant verified node %s the confidential tier on sync", node)
	}
	return nil
}

func (s *attMirrorState) bSeedsReattestClock(node string) error {
	s.b.mu.Lock()
	_, ok := s.b.attestedAt[node]
	s.b.mu.Unlock()
	if !ok {
		return fmt.Errorf("instance B should seed the re-attestation clock for %s", node)
	}
	return nil
}

func (s *attMirrorState) onlyInSharedStore(node string) error {
	if err := s.registersFailing(node); err != nil {
		return err
	}
	// B must not know it locally yet (the lazy-learn scenario).
	s.b.mu.Lock()
	_, known := s.b.nodes[node]
	s.b.mu.Unlock()
	if known {
		return fmt.Errorf("precondition: B must not know %s before tunnelFor", node)
	}
	return nil
}

func (s *attMirrorState) bLearnsViaTunnelFor(node string) error {
	if t, _ := s.b.tunnelFor(node); t == nil {
		return fmt.Errorf("tunnelFor(%s) should learn the node from the shared store", node)
	}
	return nil
}

func (s *attMirrorState) bRecordsNotConfidential(node string) error {
	s.b.mu.Lock()
	conf := s.b.confidential[node]
	s.b.mu.Unlock()
	if conf {
		return fmt.Errorf("instance B recorded %s as confidential from a failed-verdict mirror", node)
	}
	return nil
}

func (s *attMirrorState) bSyncsPrivate() error {
	s.bSyncs() // syncRegistry ingests BOTH namespaces
	return nil
}

func (s *attMirrorState) poisonMirror(node string) error {
	if err := s.registersFailing(node); err != nil {
		return err
	}
	reg, ok, err := s.sharedRecord(node, false)
	if err != nil || !ok {
		return fmt.Errorf("node %s missing from shared store (err=%v)", node, err)
	}
	reg.Confidential = true // hand-poison the mirror
	raw, _ := json.Marshal(reg)
	return s.a.shared.putNode(node, raw, livenessTTL)
}

func (s *attMirrorState) reRegisters(node string) error {
	return s.registersFailing(node) // fresh nonce, still-failing verifier
}

func (s *attMirrorState) rejectedNothingMirrored(node string) error {
	if s.lastCode == http.StatusOK {
		return fmt.Errorf("require=1 must reject a failed confidential claim, got 200")
	}
	if _, ok, err := s.sharedRecord(node, false); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("a rejected registration must not be mirrored")
	}
	return nil
}

func (s *attMirrorState) wasMirroredConfidential(node string) error {
	if err := s.registersVerified(node); err != nil {
		return err
	}
	return s.sharedConfidentialTrue(node)
}

func (s *attMirrorState) reattestLapses(node string) error {
	// Drive the sweep with a now far past the TTL so the verified node lapses.
	s.a.expireStaleAttestations(time.Now().Add(48*time.Hour), time.Hour)
	return nil
}

func (s *attMirrorState) aDowngradesAndMirrorFlips(node string) error {
	s.a.mu.Lock()
	conf := s.a.confidential[node]
	s.a.mu.Unlock()
	if conf {
		return fmt.Errorf("A should have dropped %s's confidential status on lapse", node)
	}
	return s.sharedConfidentialFalse(node)
}

func (s *attMirrorState) bDropsTier(node string) error {
	return s.bNeverGrantsTier(node)
}

func TestAttestationMirrorFeature(t *testing.T) {
	s := &attMirrorState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset(t)
				return ctx, nil
			})
			sc.Step(`^a two-instance broker pair "A" and "B" sharing a store$`, s.twoInstances)
			sc.Step(`^ROGERAI_TEE_REQUIRE is (\d)$`, s.teeRequire)
			sc.Step(`^node "([^"]*)" registers on A claiming Confidential with a quote that fails verification$`, s.registersFailing)
			sc.Step(`^node "([^"]*)" registers on A claiming Confidential with a failing quote$`, s.registersFailing)
			sc.Step(`^node "([^"]*)" registers on A with a quote that verifies$`, s.registersVerified)
			sc.Step(`^node "([^"]*)" registers on A with no confidential claim$`, s.registersNoClaim)
			sc.Step(`^A admits it \(require=0 downgrades instead of rejecting\)$`, s.admitted)
			sc.Step(`^the shared-store record for "([^"]*)" has Confidential=false$`, s.sharedConfidentialFalse)
			sc.Step(`^the shared-store record for "([^"]*)" has Confidential=true$`, s.sharedConfidentialTrue)
			sc.Step(`^the shared-store record has Confidential=false$`, func() error { return s.sharedConfidentialFalse("plain") })
			sc.Step(`^B never grants "([^"]*)" the confidential tier$`, s.bNeverGrantsTier)
			sc.Step(`^B never routes confidential-only traffic to "([^"]*)"$`, s.bNeverRoutesConfidential)
			sc.Step(`^B grants "([^"]*)" the confidential tier on sync$`, s.bGrantsTier)
			sc.Step(`^B seeds its re-attestation clock$`, func() error { return s.bSeedsReattestClock("fortress") })
			sc.Step(`^node "([^"]*)" \(failed verdict\) is only in the shared store$`, s.onlyInSharedStore)
			sc.Step(`^B first learns of "([^"]*)" via tunnelFor$`, s.bLearnsViaTunnelFor)
			sc.Step(`^B records it as NOT confidential$`, func() error { return s.bRecordsNotConfidential("sneaky") })
			sc.Step(`^node "([^"]*)" \(failed verdict\) registered on a private band$`, s.registersFailingPrivate)
			sc.Step(`^B syncs private bands$`, s.bSyncsPrivate)
			sc.Step(`^the shared-store record for "([^"]*)" is hand-edited to Confidential=true but A's verdict for it was false$`, s.poisonMirror)
			sc.Step(`^A re-registers or re-attests "([^"]*)"$`, s.reRegisters)
			sc.Step(`^the shared-store record is overwritten back to the verdict \(false\)$`, func() error { return s.sharedConfidentialFalse("sneaky") })
			sc.Step(`^the registration is rejected and nothing is mirrored$`, func() error { return s.rejectedNothingMirrored("sneaky") })
			sc.Step(`^B treats it as non-confidential$`, func() error { return s.bNeverGrantsTier("plain") })
			sc.Step(`^node "([^"]*)" was mirrored Confidential=true$`, s.wasMirroredConfidential)
			sc.Step(`^its scheduled re-attestation on A fails$`, func() error { return s.reattestLapses("fortress") })
			sc.Step(`^A downgrades it AND the shared-store record flips to Confidential=false$`, func() error { return s.aDowngradesAndMirrorFlips("fortress") })
			sc.Step(`^B drops its confidential tier on the next sync$`, func() error { return s.bDropsTier("fortress") })
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"../../features/security/attestation_mirror.feature"},
			TestingT: t, Strict: true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("attestation_mirror.feature: scenarios failed")
	}
}
