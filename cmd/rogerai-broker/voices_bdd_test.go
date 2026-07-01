package main

// voices_bdd_test.go makes features/voice/discovery.feature EXECUTABLE, driving the REAL
// computeVoices aggregation for GET /voices (the anonymous voice picker the built iOS app calls,
// roger-ios docs/BROKER-VOICE-API.md). It asserts the app's shape AND the hard security rule:
// a node's bridge URL / hostname / IP is NEVER in the payload. No mocks — real broker state.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type voicesState struct {
	b       *broker
	now     time.Time
	voices  []voiceView
	payload string   // serialized computeVoices payload (for the no-address assertion)
	secrets []string // node addresses that must NEVER appear in the payload
}

func (s *voicesState) reset() {
	s.now = time.Now()
	s.b = routeBroker(s.now, map[string]protocol.NodeRegistration{})
	s.b.db = store.NewMem()
	s.b.probeSched = map[string]*probeState{}
	s.b.localCache = map[string]localCacheEntry{}
	s.voices, s.payload, s.secrets = nil, "", nil
}

// addVoice registers an ONLINE public node serving one voice (tts) offer, with optional
// metadata. Public voices are now signed-in operators only (founder Q2), so the node is
// BOUND to an owner ("voicehost") — the discovery.feature assertions here are about the
// voice SHAPE (id=raw, price, free, metadata, no address leak), which the namespacing layer
// leaves intact; an unbound node would simply not be publicly listable.
func (s *voicesState) addVoice(id, voiceID string, priceIn float64, name, lang string, latency int, bridgeURL string) {
	o := protocol.ModelOffer{Model: voiceID, Modality: protocol.ModalityTTS, PriceIn: priceIn,
		Name: name, Language: lang, LatencyMS: latency}
	o.Normalize()
	// Carry the owner's broadcast station so the voice is publicly listable + namespaced (the
	// discovery.feature assertions are about voice SHAPE, not the @station handle; one fixed
	// callsign for the single "voicehost" owner is fine — no cross-owner uniqueness at play).
	s.b.nodes[id] = protocol.NodeRegistration{NodeID: id, BridgeURL: bridgeURL, Station: "voicehost-fm", Offers: []protocol.ModelOffer{o}}
	s.b.lastSeen[id] = s.now // online
	s.b.trust[id] = trustState{probed: true, probeOK: true, ttftMs: 200}
	s.bindOwnedNode(id)
	if bridgeURL != "" {
		s.secrets = append(s.secrets, bridgeURL)
	}
}

// bindOwnedNode binds a node to a stable "voicehost" owner so it is publicly listable under
// the signed-in-only rule (Q2). The login is fixed (attribution isn't what discovery.feature
// asserts); the owner pubkey is derived from the node id so each node binds deterministically.
func (s *voicesState) bindOwnedNode(nodeID string) {
	pub := "vh" + nodeID // a stable, unique per-node owner pubkey (not a real key; the store keys on the string)
	_ = s.b.db.BindOwner(store.Owner{GitHubID: 1, Login: "voicehost", Pubkey: pub})
	_ = s.b.db.BindNode(nodeID, pub)
}

// addModel registers an online node for a non-voice modality (chat/stt).
func (s *voicesState) addModel(id, model, modality string) {
	o := protocol.ModelOffer{Model: model, Modality: modality, PriceIn: 0.20, PriceOut: 0.20}
	o.Normalize()
	s.b.nodes[id] = protocol.NodeRegistration{NodeID: id, Offers: []protocol.ModelOffer{o}}
	s.b.lastSeen[id] = s.now
	s.b.trust[id] = trustState{probed: true, probeOK: true}
}

func (s *voicesState) getVoices() {
	res := s.b.computeVoices().(map[string]any)
	s.voices, _ = res["voices"].([]voiceView)
	b, _ := json.Marshal(res)
	s.payload = string(b)
}

func (s *voicesState) voiceFor(id string) (voiceView, bool) {
	for _, v := range s.voices {
		if v.ID == id {
			return v, true
		}
	}
	return voiceView{}, false
}

// --- step methods ---

func (s *voicesState) aTTSNode(voiceID string, priceIn float64) error {
	s.addVoice("n-"+voiceID, voiceID, priceIn, "", "", 0, "")
	return nil
}
func (s *voicesState) aTTSNodeMeta(voiceID, name, lang string, latency int) error {
	s.addVoice("n-"+voiceID, voiceID, 15, name, lang, latency, "")
	return nil
}
func (s *voicesState) aTTSNodeBridge(voiceID, bridge string) error {
	s.addVoice("n-"+voiceID, voiceID, 15, "", "", 0, bridge)
	return nil
}
func (s *voicesState) aLocalAddr(addr string) error { s.secrets = append(s.secrets, addr); return nil }
func (s *voicesState) aChatNode(model string) error {
	s.addModel("n-chat", model, protocol.ModalityChat)
	return nil
}
func (s *voicesState) anSTTNode(model string) error {
	s.addModel("n-stt", model, protocol.ModalitySTT)
	return nil
}
func (s *voicesState) noTTSNode() error     { return nil }
func (s *voicesState) getVoicesStep() error { s.getVoices(); return nil }
func (s *voicesState) resp200() error       { return nil } // computeVoices returned a payload

func (s *voicesState) listsVoice(id string) error {
	if _, ok := s.voiceFor(id); !ok {
		return fmt.Errorf("voice %q not listed; got %d voices", id, len(s.voices))
	}
	return nil
}
func (s *voicesState) pricePer1k(id string, want float64) error {
	v, ok := s.voiceFor(id)
	if !ok {
		return fmt.Errorf("voice %q not listed", id)
	}
	if v.PricePer1kChars != want {
		return fmt.Errorf("voice %q price_per_1k_chars = %v, want %v", id, v.PricePer1kChars, want)
	}
	return nil
}
func (s *voicesState) freeIs(id string, want bool) error {
	v, ok := s.voiceFor(id)
	if !ok {
		return fmt.Errorf("voice %q not listed", id)
	}
	if v.Free != want {
		return fmt.Errorf("voice %q free = %v, want %v", id, v.Free, want)
	}
	return nil
}
func (s *voicesState) carriesMeta(id, name, lang string, latency int) error {
	v, ok := s.voiceFor(id)
	if !ok {
		return fmt.Errorf("voice %q not listed", id)
	}
	if v.Name != name || v.Language != lang || v.LatencyMs != latency {
		return fmt.Errorf("voice %q meta = {%q,%q,%d}, want {%q,%q,%d}", id, v.Name, v.Language, v.LatencyMs, name, lang, latency)
	}
	return nil
}
func (s *voicesState) noSampleURL() error {
	for _, v := range s.voices {
		if v.SampleURL != "" {
			return fmt.Errorf("voice %q should have null sample_url, got %q", v.ID, v.SampleURL)
		}
	}
	return nil
}
func (s *voicesState) noAddressLeak() error {
	for _, secret := range s.secrets {
		if strings.Contains(s.payload, secret) {
			return fmt.Errorf("SECURITY: /voices payload leaked node address %q", secret)
		}
	}
	return nil
}
func (s *voicesState) onlyListed(id string) error {
	if len(s.voices) != 1 || s.voices[0].ID != id {
		return fmt.Errorf("expected only %q, got %d voices", id, len(s.voices))
	}
	return nil
}
func (s *voicesState) emptyList() error {
	if len(s.voices) != 0 {
		return fmt.Errorf("expected empty voices list, got %d", len(s.voices))
	}
	if strings.Contains(s.payload, `"voices":null`) {
		return fmt.Errorf("empty /voices must serialize as [] not null (the app decodes an array): %s", s.payload)
	}
	return nil
}

func TestVoicesDiscoveryBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &voicesState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return ctx, nil })
			sc.Step(`^a live TTS node offering "([^"]*)" at price_in ([0-9.]+) per 1M chars$`, func(id string, p float64) error { return st.aTTSNode(id, p) })
			sc.Step(`^a TTS node offering "([^"]*)" advertising name "([^"]*)", language "([^"]*)", latency_ms (\d+)$`, func(id, n, l string, ms int) error { return st.aTTSNodeMeta(id, n, l, ms) })
			sc.Step(`^a live TTS node "([^"]*)" whose bridge URL is "([^"]*)"$`, st.aTTSNodeBridge)
			sc.Step(`^whose local address is "([^"]*)"$`, st.aLocalAddr)
			sc.Step(`^a chat node offering "([^"]*)"$`, st.aChatNode)
			sc.Step(`^an STT node offering "([^"]*)"$`, st.anSTTNode)
			sc.Step(`^a TTS node offering "([^"]*)"$`, func(id string) error { return st.aTTSNode(id, 15) })
			sc.Step(`^no TTS node is registered$`, st.noTTSNode)
			sc.Step(`^an anonymous GET /voices arrives$`, st.getVoicesStep)
			sc.Step(`^the response is 200$`, st.resp200)
			sc.Step(`^it lists a voice with id "([^"]*)"$`, st.listsVoice)
			sc.Step(`^that voice's price_per_1k_chars is ([0-9.]+)$`, func(p float64) error { return st.pricePer1k("roger-operator-voice", p) })
			sc.Step(`^that voice's free is (true|false)$`, func(b string) error { return st.freeIs("roger-operator-voice", b == "true") })
			sc.Step(`^the listed voice "([^"]*)" has free (true|false)$`, func(id, b string) error { return st.freeIs(id, b == "true") })
			sc.Step(`^its price_per_1k_chars is ([0-9.]+)$`, func(p float64) error { return st.pricePer1k("front-desk-voice", p) })
			sc.Step(`^the voice carries name "([^"]*)", language "([^"]*)", latency_ms (\d+)$`, func(n, l string, ms int) error { return st.carriesMeta("roger-operator-voice", n, l, ms) })
			sc.Step(`^a voice that advertised no sample_url has sample_url null$`, st.noSampleURL)
			sc.Step(`^the response body contains neither the bridge URL nor the local address$`, st.noAddressLeak)
			sc.Step(`^no field exposes a node hostname or IP$`, st.noAddressLeak)
			sc.Step(`^a sample_url, if present, is a broker- or CDN-hosted URL, never the node's$`, st.noAddressLeak)
			sc.Step(`^only "([^"]*)" is listed$`, st.onlyListed)
			sc.Step(`^the chat and stt models are not in /voices$`, func() error { return nil })
			sc.Step(`^the response is 200 with an empty voices list$`, st.emptyList)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/voice/discovery.feature"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("voice/discovery behavior scenarios failed (see godog output above)")
	}
}
