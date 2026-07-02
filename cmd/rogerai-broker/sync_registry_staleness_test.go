package main

// sync_registry_staleness_test.go pins the ACTUAL drop site of the live-verified (2026-07-02)
// /voices sample_url loss: the MULTI-INSTANCE registry mirror. Every single-instance leg of the
// trip is proven lossless by voices_sample_url_bdd_test.go (real register -> Mem AND Postgres
// store -> re-register -> restart re-hydrate -> /voices). What CAN drop a freshly-registered
// field on the deployed 2-instance topology is syncRegistry: it adopts WHATEVER the shared
// (Valkey) registry holds with no registration ordering, so once the 15s localRegAt grace
// lapses, a STALE mirrored registration (e.g. left behind when a register-time putNode write
// was lost to a Valkey blip/failover - putNode is deliberately best-effort) REPLACES the
// fresher local one: the offers regress to the previous register (Name+Language survive, the
// newly-added SampleURL vanishes - the exact live symptom), and nothing ever re-publishes the
// fresh copy, so the whole fleet converges STALE until the node happens to re-register.
//
// The invariant these tests pin: a node's signed registration TS totally orders its
// registrations - an instance NEVER adopts a mirrored registration STRICTLY OLDER than the one
// it holds, and on detecting one it RE-PUBLISHES its fresher copy so the shared registry (and
// every peer) heals to the newest registration. Equal-or-newer mirrored registrations are
// still adopted unchanged - the bridge-token reconvergence fix (token rotated on a peer) must
// keep working, so the guard blocks ONLY strictly-older regressions.
//
// NO mocks: two real broker instances over one real (miniredis) Valkey and one shared store,
// registrations through the REAL signed b.register, /voices via the REAL computeVoices.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

const (
	stalenessSample  = "https://rogerai.fyi/samples/roger-operator.mp3"
	stalenessVoiceID = "roger-operator-voice"
)

// freshTTSReg builds the node's CURRENT registration: a tts offer carrying the full display
// metadata including sample_url, signed at ts.
func freshTTSReg(nodeID, pubHex, token string, ts int64) protocol.NodeRegistration {
	return protocol.NodeRegistration{
		NodeID: nodeID, PubKey: pubHex, BridgeToken: token, TS: ts, Station: "mirror-fm",
		Offers: []protocol.ModelOffer{{
			Model: stalenessVoiceID, Modality: protocol.ModalityTTS,
			Name: "Roger Operator", Language: "en-US", SampleURL: stalenessSample,
		}},
	}
}

// staleTTSReg is the node's PREVIOUS registration: same voice, name and language present,
// but from before the operator added a sample_url - and an OLDER signed TS.
func staleTTSReg(nodeID, pubHex, token string, ts int64) protocol.NodeRegistration {
	r := freshTTSReg(nodeID, pubHex, token, ts)
	r.Offers[0].SampleURL = ""
	return r
}

// registerOwnedTTS drives the REAL signed register path on instance b for the given
// registration, owner-signed so the voice is publicly listable on /voices (owner-bound +
// station). Fails the test on a non-200.
func registerOwnedTTS(t *testing.T, b *broker, reg protocol.NodeRegistration, nodePriv ed25519.PrivateKey, ownerPriv ed25519.PrivateKey) {
	t.Helper()
	reg.SignRegistration(nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, ownerPriv, body)
	w := httptest.NewRecorder()
	b.register(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("owned tts register = %d (%s), want 200", w.Code, w.Body.String())
	}
}

// voicesSampleURL returns the /voices row for stalenessVoiceID on instance b.
func voicesSampleURL(t *testing.T, b *broker) voiceView {
	t.Helper()
	res := b.computeVoices().(map[string]any)
	voices, _ := res["voices"].([]voiceView)
	for _, v := range voices {
		if v.ID == stalenessVoiceID {
			return v
		}
	}
	t.Fatalf("voice %q not listed; got %+v", stalenessVoiceID, voices)
	return voiceView{}
}

// ageLocalRegGrace expires instance b's own-register grace for node so the next
// syncRegistry tick reconciles it from the shared registry (production: >=15s elapsed).
func ageLocalRegGrace(b *broker, node string) {
	b.mu.Lock()
	b.localRegAt[node] = time.Now().Add(-syncLocalRegisterGrace - time.Second)
	b.mu.Unlock()
}

// bindVoiceOwner binds a GitHub-linked owner in the shared store and returns its keypair
// (the register requests are signed with it so BindNode owner-binds the voice).
func bindVoiceOwner(t *testing.T, db store.Store) ed25519.PrivateKey {
	t.Helper()
	_, ownerPriv, _ := ed25519.GenerateKey(nil)
	ownerPub := hex.EncodeToString(ownerPriv.Public().(ed25519.PublicKey))
	if err := db.BindOwner(store.Owner{GitHubID: 7, Login: "mirrorop", Pubkey: ownerPub}); err != nil {
		t.Fatal(err)
	}
	return ownerPriv
}

// TestSyncRegistryStaleMirrorNeverDropsFreshOffer is the live-symptom regression: the node
// (re-)registered WITH sample_url on THIS instance, but the shared registry still holds the
// PREVIOUS (older-TS, sample-less) registration - the state a lost register-time putNode
// (best-effort, Valkey blip) leaves behind, kept alive indefinitely by heartbeat markSeen.
// After the local-register grace lapses, syncRegistry must NOT regress the fresh local
// registration to the stale mirror (today it does: sample_url vanishes from /voices while
// name+language survive), and it must HEAL the shared registry to the fresher copy so peers
// converge fresh instead of stale.
func TestSyncRegistryStaleMirrorNeverDropsFreshOffer(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a := newMIBroker(t, brokerPriv, db, mr)
	a.localRegAt = map[string]time.Time{}
	ownerPriv := bindVoiceOwner(t, db)

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(nodePub)
	const nodeID, token = "mirror-n1", "tok-fresh"
	freshTS := time.Now().Unix()

	// The node's CURRENT registration (with sample_url) lands through the REAL register
	// path on instance A: in-memory fresh, persisted fresh, and published fresh.
	registerOwnedTTS(t, a, freshTTSReg(nodeID, pubHex, token, freshTS), nodePriv, ownerPriv)
	if v := voicesSampleURL(t, a); v.SampleURL != stalenessSample {
		t.Fatalf("precondition: fresh register must list sample_url %q, got %q", stalenessSample, v.SampleURL)
	}

	// The shared registry holds the node's PREVIOUS registration (older signed TS, no
	// sample_url): the state a lost/failed register-time putNode leaves behind (putNode is
	// best-effort by design; markSeen keeps the stale key alive as long as the node
	// heartbeats). Written through the real shared-store client, as register() writes it.
	stale := staleTTSReg(nodeID, pubHex, token, freshTS-60)
	stale.SignRegistration(nodePriv)
	rawStale, _ := json.Marshal(stale)
	if err := a.shared.putNode(nodeID, rawStale, livenessTTL); err != nil {
		t.Fatal(err)
	}

	// Production time passes: A's own-register grace lapses, the 5s sync tick fires.
	ageLocalRegGrace(a, nodeID)
	a.syncRegistry()

	// The live symptom: name+language survive, sample_url is gone. The fresh registration
	// must win - a strictly-older mirrored registration is never adopted.
	v := voicesSampleURL(t, a)
	if v.Name != "Roger Operator" || v.Language != "en-US" {
		t.Errorf("sibling fields regressed: name/language = %q/%q, want Roger Operator/en-US", v.Name, v.Language)
	}
	if v.SampleURL != stalenessSample {
		t.Errorf("DROP SITE: after a stale-mirror sync, /voices sample_url = %q, want %q (syncRegistry regressed the fresh registration to the older mirrored one)", v.SampleURL, stalenessSample)
	}

	// And the shared registry must have been HEALED to the fresher local registration
	// (re-published), so peer instances converge FRESH instead of adopting the stale copy.
	rawShared, ok, err := a.shared.getNode(nodeID)
	if err != nil || !ok {
		t.Fatalf("shared registry read after sync: ok=%v err=%v", ok, err)
	}
	var sharedReg protocol.NodeRegistration
	if err := json.Unmarshal(rawShared, &sharedReg); err != nil {
		t.Fatal(err)
	}
	if sharedReg.TS != freshTS || sharedReg.Offers[0].SampleURL != stalenessSample {
		t.Errorf("shared registry not healed: TS=%d sample_url=%q, want TS=%d sample_url=%q", sharedReg.TS, sharedReg.Offers[0].SampleURL, freshTS, stalenessSample)
	}

	// A peer that knows nothing fresher now adopts the HEALED registration: /voices on B
	// carries the sample_url (liveness seeded as a synced heartbeat would).
	b := newMIBroker(t, brokerPriv, db, mr)
	b.syncRegistry()
	b.mu.Lock()
	b.lastSeen[nodeID] = time.Now()
	b.mu.Unlock()
	if v := voicesSampleURL(t, b); v.SampleURL != stalenessSample {
		t.Errorf("peer adopted a stale registration: /voices on B sample_url = %q, want %q", v.SampleURL, stalenessSample)
	}
}

// TestSyncRegistryStaleMirrorNeverDropsPrivateOffer covers the SAME regression on the
// private-band mirror loop: a --private node's fresher local registration (offers carry the
// operator's current price and metadata) must never regress to a strictly-older preg mirror,
// and the private namespace heals the same way. Seeded directly in the private namespace (the
// state register() leaves for a private band), then reconciled by the real syncRegistry.
func TestSyncRegistryStaleMirrorNeverDropsPrivateOffer(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a := newMIBroker(t, brokerPriv, db, mr)
	a.localRegAt = map[string]time.Time{}

	nodePub, _, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(nodePub)
	const nodeID, token = "mirror-p1", "tok-priv"
	freshTS := time.Now().Unix()

	fresh := freshTTSReg(nodeID, pubHex, token, freshTS)
	fresh.Private = true
	a.mu.Lock()
	a.nodes[nodeID] = fresh
	a.private[nodeID] = true
	a.lastSeen[nodeID] = time.Now()
	a.mu.Unlock()

	stale := staleTTSReg(nodeID, pubHex, token, freshTS-60)
	stale.Private = true
	rawStale, _ := json.Marshal(stale)
	if err := a.shared.putPrivateNode(nodeID, rawStale, livenessTTL); err != nil {
		t.Fatal(err)
	}

	a.syncRegistry()

	a.mu.Lock()
	got := a.nodes[nodeID]
	a.mu.Unlock()
	if got.Offers[0].SampleURL != stalenessSample || got.TS != freshTS {
		t.Errorf("private mirror regressed the fresh registration: TS=%d sample_url=%q, want TS=%d sample_url=%q", got.TS, got.Offers[0].SampleURL, freshTS, stalenessSample)
	}

	rawShared, ok, err := a.shared.getPrivateNode(nodeID)
	if err != nil || !ok {
		t.Fatalf("private shared registry read after sync: ok=%v err=%v", ok, err)
	}
	var sharedReg protocol.NodeRegistration
	if err := json.Unmarshal(rawShared, &sharedReg); err != nil {
		t.Fatal(err)
	}
	if sharedReg.TS != freshTS || sharedReg.Offers[0].SampleURL != stalenessSample {
		t.Errorf("private shared registry not healed: TS=%d sample_url=%q, want TS=%d sample_url=%q", sharedReg.TS, sharedReg.Offers[0].SampleURL, freshTS, stalenessSample)
	}
}

// TestSyncRegistryEqualAndNewerMirrorsStillAdopted pins that the staleness guard blocks ONLY
// strictly-older mirrors - the multi-instance bridge-token reconvergence fix depends on
// adopting an EQUAL-TS mirror (a same-second re-register that landed on a peer) and a
// NEWER-TS mirror (the node re-registered on a peer, possibly with a rotated token and
// changed offers). Both must keep flowing through unchanged, offers included.
func TestSyncRegistryEqualAndNewerMirrorsStillAdopted(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a := newMIBroker(t, brokerPriv, db, mr)
	a.localRegAt = map[string]time.Time{}

	nodePub, _, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(nodePub)
	const nodeID = "mirror-n2"
	baseTS := time.Now().Unix()

	seed := staleTTSReg(nodeID, pubHex, "tok-old", baseTS)
	a.mu.Lock()
	a.nodes[nodeID] = seed
	a.lastSeen[nodeID] = time.Now()
	a.mu.Unlock()

	// EQUAL TS, rotated token (a same-second peer re-register): adopted - the shared
	// registry stays the tie-break source of truth for the bridge token.
	equal := staleTTSReg(nodeID, pubHex, "tok-equal", baseTS)
	rawEqual, _ := json.Marshal(equal)
	if err := a.shared.putNode(nodeID, rawEqual, livenessTTL); err != nil {
		t.Fatal(err)
	}
	a.syncRegistry()
	a.mu.Lock()
	tok := a.tunnels[nodeID].token
	a.mu.Unlock()
	if tok != "tok-equal" {
		t.Errorf("equal-TS mirror not adopted: tunnel token = %q, want tok-equal (token reconvergence must keep working)", tok)
	}

	// NEWER TS with a rotated token AND changed offers (the node re-registered on a peer,
	// now carrying a sample_url): adopted fully - offers update when genuinely newer.
	newer := freshTTSReg(nodeID, pubHex, "tok-newer", baseTS+30)
	rawNewer, _ := json.Marshal(newer)
	if err := a.shared.putNode(nodeID, rawNewer, livenessTTL); err != nil {
		t.Fatal(err)
	}
	a.syncRegistry()
	a.mu.Lock()
	got := a.nodes[nodeID]
	tok = a.tunnels[nodeID].token
	a.mu.Unlock()
	if tok != "tok-newer" || got.TS != baseTS+30 || got.Offers[0].SampleURL != stalenessSample {
		t.Errorf("newer mirror not adopted: token=%q TS=%d sample_url=%q, want tok-newer/%d/%q", tok, got.TS, got.Offers[0].SampleURL, baseTS+30, stalenessSample)
	}
}
