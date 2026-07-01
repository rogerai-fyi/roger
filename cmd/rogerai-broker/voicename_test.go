package main

// voicename_test.go: focused table-driven units for the voice-namespacing helpers and the
// GET /voices HTTP handler. The end-to-end behavior lives in the godog suites
// (voice_namespacing_bdd_test.go / voices_bdd_test.go); these cover the pure-function edge
// cases + the handler's CORS/method/rate-limit wrapper directly. Real Mem store, no mocks.
// Plain stdlib testing (matching the rest of the broker package — no new test dependency).

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/agent"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

func TestSlugVoiceName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"simple", "Operator", "operator", true},
		{"spaces collapse", "  Front  Desk  VOICE  ", "front-desk-voice", true},
		{"slash becomes dash", "acme/official", "acme-official", true},
		{"at becomes dash then trimmed", "@acme voice", "acme-voice", true},
		{"digits kept", "1950s Operator", "1950s-operator", true},
		{"fullwidth folds to ascii", "ｇｐｔ", "gpt", true},
		{"punctuation stripped", "Claude-3!!!", "claude-3", true},
		{"empty string", "", "", false},
		{"only dashes", "---", "", false},
		{"only spaces", "   ", "", false},
		{"non-ascii only folds empty", "我的声音", "", false},
		{"leading/trailing junk trimmed", "***Reception***", "reception", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := slugVoiceName(c.in)
			if ok != c.ok || got != c.want {
				t.Fatalf("slugVoiceName(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestSlugVoiceNameCapsAt64Runes(t *testing.T) {
	got, ok := slugVoiceName(stringOf('a', 200))
	if !ok {
		t.Fatal("an all-letter name should slug")
	}
	if n := len([]rune(got)); n != voiceSlugMaxRunes {
		t.Errorf("slug len = %d runes, want the cap %d", n, voiceSlugMaxRunes)
	}
}

func TestImpersonatesChatModel(t *testing.T) {
	os.Setenv("ROGERAI_VOICE_IMPERSONATION_DENYLIST", "")
	cases := []struct {
		slug string
		want bool
	}{
		{"qwen", true},
		{"qwen3-coder-next", true},
		{"gpt", true},
		{"gpt-oss-120b", true},
		{"llama3-2", true}, // llama3.2 -> slug llama3-2, prefix "llama"
		{"claude", true},
		{"claude-3", true},
		{"grok", true},
		{"mistral", true},
		{"deepseek", true},
		{"gemma", true},
		{"phi", true},
		{"1950s-operator", false},
		{"front-desk-voice", false},
		{"reception", false},
		{"operator", false},
	}
	for _, c := range cases {
		t.Run(c.slug, func(t *testing.T) {
			if got := impersonatesChatModel(c.slug); got != c.want {
				t.Errorf("impersonatesChatModel(%q) = %v, want %v", c.slug, got, c.want)
			}
		})
	}
}

func TestImpersonationDenylistEnvOverride(t *testing.T) {
	t.Cleanup(func() { os.Setenv("ROGERAI_VOICE_IMPERSONATION_DENYLIST", "") })
	// The env REPLACES the default: a former default root no longer blocks, the new one does.
	os.Setenv("ROGERAI_VOICE_IMPERSONATION_DENYLIST", "acme-brand, Foocorp")
	if !impersonatesChatModel("acme-brand-voice") {
		t.Error("env root should block acme-brand-voice")
	}
	if !impersonatesChatModel("foocorp") {
		t.Error("env root normalized should block foocorp")
	}
	if impersonatesChatModel("qwen") {
		t.Error("a default root must NOT block once the env replaces the list")
	}
	// A blank env falls back to the default set.
	os.Setenv("ROGERAI_VOICE_IMPERSONATION_DENYLIST", "")
	if !impersonatesChatModel("gpt") {
		t.Error("blank env should fall back to the default set (gpt blocked)")
	}
}

// nsUnitBroker is a minimal Mem-backed broker for the helper units.
func nsUnitBroker(t *testing.T) *broker {
	t.Helper()
	return &broker{
		db:           store.NewMem(),
		nodes:        map[string]protocol.NodeRegistration{},
		lastSeen:     map[string]time.Time{},
		private:      map[string]bool{},
		banned:       map[string]bool{},
		bannedOwners: map[string]bool{},
	}
}

// bindOwnerNode binds an owner (login) to a node id via a synthetic pubkey, returning it.
func bindOwnerNode(t *testing.T, b *broker, nodeID, login string) string {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	if err := b.db.BindOwner(store.Owner{GitHubID: 1, Login: login, Pubkey: pub}); err != nil {
		t.Fatalf("BindOwner: %v", err)
	}
	if err := b.db.BindNode(nodeID, pub); err != nil {
		t.Fatalf("BindNode: %v", err)
	}
	return pub
}

func ttsReg(nodeID, name string) protocol.NodeRegistration {
	return protocol.NodeRegistration{NodeID: nodeID, Offers: []protocol.ModelOffer{
		{Model: "m-" + nodeID, Modality: protocol.ModalityTTS, Name: name}}}
}

func TestDuplicateVoiceName(t *testing.T) {
	t.Run("same operator same slug is a duplicate", func(t *testing.T) {
		b := nsUnitBroker(t)
		owner := bindOwnerNode(t, b, "n1", "bownux")
		b.nodes["n1"] = ttsReg("n1", "Operator")
		b.lastSeen["n1"] = time.Now()
		msg := b.duplicateVoiceName(owner, "n2", []protocol.ModelOffer{{Modality: protocol.ModalityTTS, Name: "  operator "}})
		if !strings.Contains(msg, "already have a voice") {
			t.Errorf("want duplicate message, got %q", msg)
		}
	})

	t.Run("aged-out node is not a collision", func(t *testing.T) {
		b := nsUnitBroker(t)
		owner := bindOwnerNode(t, b, "n1", "bownux")
		b.nodes["n1"] = ttsReg("n1", "Operator")
		b.lastSeen["n1"] = time.Now().Add(-2 * nodeTTL) // off air
		if msg := b.duplicateVoiceName(owner, "n2", []protocol.ModelOffer{{Modality: protocol.ModalityTTS, Name: "Operator"}}); msg != "" {
			t.Errorf("aged-out node must not collide, got %q", msg)
		}
	})

	t.Run("different operator is namespaced away, not a dup", func(t *testing.T) {
		b := nsUnitBroker(t)
		bindOwnerNode(t, b, "n1", "acme")
		b.nodes["n1"] = ttsReg("n1", "Operator")
		b.lastSeen["n1"] = time.Now()
		other := bindOwnerNode(t, b, "n2", "bownux")
		if msg := b.duplicateVoiceName(other, "n2", []protocol.ModelOffer{{Modality: protocol.ModalityTTS, Name: "Operator"}}); msg != "" {
			t.Errorf("cross-operator must not collide, got %q", msg)
		}
	})

	t.Run("existing non-TTS + empty-name offers are skipped", func(t *testing.T) {
		b := nsUnitBroker(t)
		owner := bindOwnerNode(t, b, "n1", "bownux")
		b.nodes["n1"] = protocol.NodeRegistration{NodeID: "n1", Offers: []protocol.ModelOffer{
			{Model: "chat", Modality: protocol.ModalityChat},                 // skipped: not TTS
			{Model: "v", Modality: protocol.ModalityTTS, Name: ""},           // skipped: empty name -> no slug
			{Model: "v2", Modality: protocol.ModalityTTS, Name: "Reception"}, // the real one
		}}
		b.lastSeen["n1"] = time.Now()
		if msg := b.duplicateVoiceName(owner, "n2", []protocol.ModelOffer{{Modality: protocol.ModalityTTS, Name: "Operator"}}); msg != "" {
			t.Errorf("Operator must not collide with Reception, got %q", msg)
		}
		if msg := b.duplicateVoiceName(owner, "n2", []protocol.ModelOffer{{Modality: protocol.ModalityTTS, Name: "Reception"}}); !strings.Contains(msg, "already have") {
			t.Errorf("Reception must collide, got %q", msg)
		}
	})

	t.Run("no valid new slug -> never a duplicate", func(t *testing.T) {
		b := nsUnitBroker(t)
		owner := bindOwnerNode(t, b, "n1", "bownux")
		b.nodes["n1"] = ttsReg("n1", "Operator")
		b.lastSeen["n1"] = time.Now()
		// new offer has an empty-after-normalize name -> no slug -> nothing to collide.
		if msg := b.duplicateVoiceName(owner, "n2", []protocol.ModelOffer{{Modality: protocol.ModalityTTS, Name: "---"}}); msg != "" {
			t.Errorf("an empty new slug can't collide, got %q", msg)
		}
	})
}

// bindStationNode binds an owner + node AND plants the node in b.nodes carrying `station` (the
// signed reg.Station operatorStation reads), on air. login "" models an Apple-only owner. Returns
// the owner pubkey.
func bindStationNode(t *testing.T, b *broker, nodeID, login, station string) string {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	o := store.Owner{GitHubID: 1, Pubkey: pub}
	if login != "" {
		o.Login = login
	} else {
		o.AppleSub = "apple-" + nodeID
	}
	if err := b.db.BindOwner(o); err != nil {
		t.Fatalf("BindOwner: %v", err)
	}
	if err := b.db.BindNode(nodeID, pub); err != nil {
		t.Fatalf("BindNode: %v", err)
	}
	reg := ttsReg(nodeID, "Operator")
	reg.Station = station
	b.nodes[nodeID] = reg
	b.lastSeen[nodeID] = time.Now()
	return pub
}

func TestOperatorStation(t *testing.T) {
	t.Run("unbound node -> not listable", func(t *testing.T) {
		b := nsUnitBroker(t)
		if _, ok := b.operatorStation("nope"); ok {
			t.Error("an unbound node must not resolve an operator")
		}
	})

	t.Run("bound owner-signed node with a station -> station", func(t *testing.T) {
		b := nsUnitBroker(t)
		bindStationNode(t, b, "n1", "bownux", "brave-otter-37")
		st, ok := b.operatorStation("n1")
		if !ok || st != "brave-otter-37" {
			t.Errorf("operatorStation(n1) = (%q,%v), want (brave-otter-37,true)", st, ok)
		}
	})

	t.Run("APPLE-only owner (no login) with a station -> station (login not required)", func(t *testing.T) {
		b := nsUnitBroker(t)
		bindStationNode(t, b, "n1", "", "swift-lynx-8")
		st, ok := b.operatorStation("n1")
		if !ok || st != "swift-lynx-8" {
			t.Errorf("operatorStation(n1) = (%q,%v), want (swift-lynx-8,true) for an Apple-only owner", st, ok)
		}
	})

	t.Run("station is re-normalized broker-side", func(t *testing.T) {
		b := nsUnitBroker(t)
		bindStationNode(t, b, "n1", "bownux", "  Brave Otter 37 ") // unslugged
		st, ok := b.operatorStation("n1")
		if !ok || st != "brave-otter-37" {
			t.Errorf("operatorStation(n1) = (%q,%v), want (brave-otter-37,true)", st, ok)
		}
	})

	t.Run("banned owner -> not listable", func(t *testing.T) {
		b := nsUnitBroker(t)
		pub := bindStationNode(t, b, "n1", "bownux", "brave-otter-37")
		b.bannedOwners[pub] = true
		if _, ok := b.operatorStation("n1"); ok {
			t.Error("a banned operator's voice must not list")
		}
	})

	t.Run("bound node with NO station -> not listable", func(t *testing.T) {
		b := nsUnitBroker(t)
		bindStationNode(t, b, "n1", "bownux", "") // station-less (predates the field)
		if _, ok := b.operatorStation("n1"); ok {
			t.Error("a node carrying no station has no public namespace")
		}
	})
}

// TestVoicesHTTPHandler exercises the GET /voices HTTP wrapper directly (CORS preflight,
// method-not-allowed, rate-limit, and a happy 200 whose body carries the namespaced voice
// but NO node address) — the pure-aggregation path is covered by the godog suites.
func TestVoicesHTTPHandler(t *testing.T) {
	handlerBroker := func() *broker {
		b := nsUnitBroker(t)
		b.anonRL = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 600, burst: 600}
		return b
	}

	t.Run("CORS preflight short-circuits", func(t *testing.T) {
		b := handlerBroker()
		r := httptest.NewRequest(http.MethodOptions, "/voices", nil)
		r.Header.Set("Origin", "https://rogerai.fyi")
		r.Header.Set("Access-Control-Request-Method", "GET")
		w := httptest.NewRecorder()
		b.voices(w, r)
		if w.Code != http.StatusNoContent {
			t.Errorf("preflight = %d, want 204", w.Code)
		}
	})

	t.Run("non-GET is rejected", func(t *testing.T) {
		b := handlerBroker()
		w := httptest.NewRecorder()
		b.voices(w, httptest.NewRequest(http.MethodPost, "/voices", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST /voices = %d, want 405", w.Code)
		}
	})

	t.Run("rate limit returns 429 with Retry-After", func(t *testing.T) {
		b := nsUnitBroker(t)
		b.anonRL = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 1, burst: 1} // 1 token, then blocked
		w1 := httptest.NewRecorder()
		b.voices(w1, httptest.NewRequest(http.MethodGet, "/voices", nil))
		if w1.Code != http.StatusOK {
			t.Fatalf("first GET = %d, want 200", w1.Code)
		}
		w2 := httptest.NewRecorder()
		b.voices(w2, httptest.NewRequest(http.MethodGet, "/voices", nil))
		if w2.Code != http.StatusTooManyRequests {
			t.Errorf("second GET = %d, want 429", w2.Code)
		}
		if w2.Header().Get("Retry-After") == "" {
			t.Error("429 must carry a Retry-After header")
		}
	})

	t.Run("happy 200 lists a bound voice, no address", func(t *testing.T) {
		b := handlerBroker()
		bindOwnerNode(t, b, "n1", "bownux")
		// Station carries the public callsign (here == the login) so the voice is listable +
		// namespaced @<station>/<slug>; the raw id + no-address-leak invariants are unchanged.
		b.nodes["n1"] = protocol.NodeRegistration{NodeID: "n1", BridgeURL: "https://secret.trycloudflare.com", Station: "bownux",
			Offers: []protocol.ModelOffer{{Model: "roger-operator-voice", Modality: protocol.ModalityTTS, Name: "Operator", PriceIn: 20}}}
		b.lastSeen["n1"] = time.Now()
		w := httptest.NewRecorder()
		b.voices(w, httptest.NewRequest(http.MethodGet, "/voices", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET /voices = %d, want 200", w.Code)
		}
		body := w.Body.String()
		if strings.Contains(body, "secret.trycloudflare.com") {
			t.Error("SECURITY: bridge URL leaked into /voices")
		}
		if strings.Contains(body, "n1") {
			t.Error("SECURITY: node id leaked into /voices")
		}
		var out struct {
			Voices []voiceView `json:"voices"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(out.Voices) != 1 {
			t.Fatalf("got %d voices, want 1", len(out.Voices))
		}
		v := out.Voices[0]
		if v.ID != "roger-operator-voice" || v.NamespacedID != "@bownux/operator" || v.Operator != "bownux" {
			t.Errorf("voice = {id=%q ns=%q op=%q}, want {roger-operator-voice @bownux/operator bownux}", v.ID, v.NamespacedID, v.Operator)
		}
	})
}

func stringOf(r rune, n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = r
	}
	return string(b)
}

// TestParseNamespacedVoice covers every branch of the "@<station>/<slug>" parser: a RAW id (no
// "@", the back-compat routing key), a namespaced id with no "/", a forged deeper "/"-segment, an
// empty station, an empty-after-normalize slug, and the valid case (with both parts re-normalized
// to the ids /voices emits).
func TestParseNamespacedVoice(t *testing.T) {
	cases := []struct {
		in          string
		wantStation string
		wantSlug    string
		wantOK      bool
	}{
		{"af_heart", "", "", false},                                            // raw id: not namespaced
		{"", "", "", false},                                                    // empty: not namespaced
		{"@brave-otter", "", "", false},                                        // no "/": malformed
		{"@brave-otter/operator", "brave-otter", "operator", true},             // valid
		{"@brave-otter/1950s Operator", "brave-otter", "1950s-operator", true}, // slug re-normalized
		{"@Brave Otter/Operator", "brave-otter", "operator", true},             // station re-normalized (slug/case fold)
		{"@brave-otter/acme/official", "", "", false},                          // forged deeper segment
		{"@/operator", "", "", false},                                          // empty station
		{"@brave-otter/", "", "", false},                                       // empty slug
		{"@brave-otter/---", "", "", false},                                    // slug normalizes to empty
	}
	for _, c := range cases {
		st, sl, ok := parseNamespacedVoice(c.in)
		if ok != c.wantOK || st != c.wantStation || sl != c.wantSlug {
			t.Errorf("parseNamespacedVoice(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, st, sl, ok, c.wantStation, c.wantSlug, c.wantOK)
		}
	}
}

// TestSlugStationMatchesAgent PINS the broker's station-normalizer to internal/agent.SlugStation
// (the SAME rule ShareNodeID slugs the node-id prefix with). They MUST stay byte-identical: the
// station a caller sees in @<station>/<slug> from /voices, the station the relay resolves by, and
// the callsign the node embeds in its id all have to agree, so this guards against a silent drift
// if either implementation is edited. (The broker keeps its own tiny copy rather than importing
// the node-agent package into the SERVER binary — this test is the safety net for that choice.)
func TestSlugStationMatchesAgent(t *testing.T) {
	for _, s := range []string{
		"brave-otter-37", "  Brave Otter 37 ", "Swift/Lynx", "@acme", "UPPER_CASE",
		"multi   space", "trailing---", "---leading", "", "已经", "mixed-42_x",
	} {
		if got, want := slugStation(s), agent.SlugStation(s); got != want {
			t.Errorf("slugStation(%q)=%q but agent.SlugStation=%q — the station rule drifted", s, got, want)
		}
	}
}

// TestOffersTTS covers both arms: a slice containing a TTS offer (true) and one with only
// chat/stt offers (false, the reservation-doesn't-fire path).
func TestOffersTTS(t *testing.T) {
	if !offersTTS([]protocol.ModelOffer{{Modality: protocol.ModalityChat}, {Modality: protocol.ModalityTTS}}) {
		t.Error("a slice with a tts offer must report true")
	}
	if offersTTS([]protocol.ModelOffer{{Modality: protocol.ModalityChat}, {Modality: protocol.ModalitySTT}, {Modality: ""}}) {
		t.Error("a slice with no tts offer must report false")
	}
	if offersTTS(nil) {
		t.Error("an empty slice must report false")
	}
}

// TestResolveNamespacedVoice covers every filter branch of the namespaced-id -> node resolver:
// banned / private / off-air / wrong-modality / slug-mismatch (phase 1 skips), station-mismatch
// (phase 2 skip), and a successful match returning the raw model + node id.
func TestResolveNamespacedVoice(t *testing.T) {
	b := nsUnitBroker(t)
	// A helper to plant an on-air owner-bound TTS node under a station.
	plant := func(id, station, model, name, modality string) string {
		_, priv, _ := ed25519.GenerateKey(nil)
		pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
		_ = b.db.BindOwner(store.Owner{GitHubID: int64(len(id)), Login: "op-" + id, Pubkey: pub})
		_ = b.db.BindNode(id, pub)
		b.nodes[id] = protocol.NodeRegistration{NodeID: id, Station: station,
			Offers: []protocol.ModelOffer{{Model: model, Modality: modality, Name: name}}}
		b.lastSeen[id] = time.Now()
		return pub
	}

	// The MATCH: an on-air brave-otter tts node named "Operator" (slug "operator").
	plant("good", "brave-otter", "af_heart", "Operator", protocol.ModalityTTS)
	// Distractors that must all be SKIPPED for a @brave-otter/operator resolve:
	bpub := plant("banned", "brave-otter", "af_heart", "Operator", protocol.ModalityTTS) // banned owner
	b.bannedOwners[bpub] = true
	plant("priv", "brave-otter", "af_heart", "Operator", protocol.ModalityTTS)
	b.private["priv"] = true // private node
	plant("stale", "brave-otter", "af_heart", "Operator", protocol.ModalityTTS)
	b.lastSeen["stale"] = time.Now().Add(-2 * nodeTTL)                                  // off air
	plant("chatty", "brave-otter", "af_heart", "Operator", protocol.ModalityChat)       // wrong modality
	plant("otherslug", "brave-otter", "af_aoede", "Receptionist", protocol.ModalityTTS) // slug mismatch
	plant("otherstation", "swift-lynx", "af_heart", "Operator", protocol.ModalityTTS)   // station mismatch (phase 2)

	raw, node, ok := b.resolveNamespacedVoice("brave-otter", "operator", protocol.ModalityTTS)
	if !ok || node != "good" || raw != "af_heart" {
		t.Fatalf("resolve(@brave-otter/operator) = (%q,%q,%v), want (af_heart,good,true)", raw, node, ok)
	}

	// No match: unknown slug under a real station.
	if _, _, ok := b.resolveNamespacedVoice("brave-otter", "nonexistent", protocol.ModalityTTS); ok {
		t.Error("an unknown slug must not resolve")
	}
	// No match: unknown station for a real slug (all same-slug nodes are on other stations).
	if _, _, ok := b.resolveNamespacedVoice("ghost", "operator", protocol.ModalityTTS); ok {
		t.Error("an unknown station must not resolve")
	}
	// Modality isolation: the same station+slug on STT does not resolve to the tts node.
	if _, _, ok := b.resolveNamespacedVoice("brave-otter", "operator", protocol.ModalitySTT); ok {
		t.Error("an stt resolve must not reach the tts node")
	}
}

// TestStationClaimedByOther covers the cross-owner station-uniqueness backstop's branches: no
// claim (empty station / different station / off-air / non-tts / unbound / same-owner / banned)
// and a real DIFFERENT-owner claim.
func TestStationClaimedByOther(t *testing.T) {
	newBroker := func() *broker { return nsUnitBroker(t) }
	ownerPub := func(t *testing.T, b *broker, login string) string {
		t.Helper()
		_, priv, _ := ed25519.GenerateKey(nil)
		pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
		if err := b.db.BindOwner(store.Owner{GitHubID: int64(len(login)), Login: login, Pubkey: pub}); err != nil {
			t.Fatal(err)
		}
		return pub
	}
	// plantTTS puts an on-air TTS node under `station` bound to `ownerPub`.
	plantTTS := func(b *broker, id, station, ownerPub string) {
		b.nodes[id] = protocol.NodeRegistration{NodeID: id, Station: station,
			Offers: []protocol.ModelOffer{{Model: "m-" + id, Modality: protocol.ModalityTTS, Name: "Operator"}}}
		b.lastSeen[id] = time.Now()
		if ownerPub != "" {
			_ = b.db.BindNode(id, ownerPub)
		}
	}

	t.Run("empty station -> no claim", func(t *testing.T) {
		b := newBroker()
		if got := b.stationClaimedByOther("", "self"); got != "" {
			t.Errorf("empty station must never claim, got %q", got)
		}
	})

	t.Run("a DIFFERENT owner on the same station -> claimed", func(t *testing.T) {
		b := newBroker()
		other := ownerPub(t, b, "intruder")
		plantTTS(b, "n1", "brave-otter", other)
		self := ownerPub(t, b, "me")
		if got := b.stationClaimedByOther("brave-otter", self); got != "n1" {
			t.Errorf("a different owner's node under the station must be reported, got %q", got)
		}
	})

	t.Run("the SAME owner on the station -> not a claim", func(t *testing.T) {
		b := newBroker()
		self := ownerPub(t, b, "me")
		plantTTS(b, "n1", "brave-otter", self)
		if got := b.stationClaimedByOther("brave-otter", self); got != "" {
			t.Errorf("the same owner reusing their own station is not a collision, got %q", got)
		}
	})

	t.Run("a DIFFERENT station -> not a claim", func(t *testing.T) {
		b := newBroker()
		other := ownerPub(t, b, "intruder")
		plantTTS(b, "n1", "swift-lynx", other)
		self := ownerPub(t, b, "me")
		if got := b.stationClaimedByOther("brave-otter", self); got != "" {
			t.Errorf("a different station holds no claim, got %q", got)
		}
	})

	t.Run("an OFF-AIR node -> not a claim", func(t *testing.T) {
		b := newBroker()
		other := ownerPub(t, b, "intruder")
		plantTTS(b, "n1", "brave-otter", other)
		b.lastSeen["n1"] = time.Now().Add(-2 * nodeTTL) // aged out
		self := ownerPub(t, b, "me")
		if got := b.stationClaimedByOther("brave-otter", self); got != "" {
			t.Errorf("an off-air node holds no claim, got %q", got)
		}
	})

	t.Run("a CHAT-only node under the station -> not a claim", func(t *testing.T) {
		b := newBroker()
		other := ownerPub(t, b, "intruder")
		b.nodes["n1"] = protocol.NodeRegistration{NodeID: "n1", Station: "brave-otter",
			Offers: []protocol.ModelOffer{{Model: "llama3.2", Modality: protocol.ModalityChat}}}
		b.lastSeen["n1"] = time.Now()
		_ = b.db.BindNode("n1", other)
		self := ownerPub(t, b, "me")
		if got := b.stationClaimedByOther("brave-otter", self); got != "" {
			t.Errorf("a chat-only node reserves no public station, got %q", got)
		}
	})

	t.Run("an UNBOUND node under the station -> not a claim", func(t *testing.T) {
		b := newBroker()
		plantTTS(b, "n1", "brave-otter", "") // no owner binding
		self := ownerPub(t, b, "me")
		if got := b.stationClaimedByOther("brave-otter", self); got != "" {
			t.Errorf("an unbound node holds no owner claim, got %q", got)
		}
	})

	t.Run("a BANNED owner under the station -> not a claim", func(t *testing.T) {
		b := newBroker()
		other := ownerPub(t, b, "intruder")
		plantTTS(b, "n1", "brave-otter", other)
		b.bannedOwners[other] = true
		self := ownerPub(t, b, "me")
		if got := b.stationClaimedByOther("brave-otter", self); got != "" {
			t.Errorf("a banned owner's node holds no live public claim, got %q", got)
		}
	})
}
