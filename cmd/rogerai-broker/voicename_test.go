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

func TestOperatorLogin(t *testing.T) {
	t.Run("unbound node -> not listable", func(t *testing.T) {
		b := nsUnitBroker(t)
		if _, ok := b.operatorLogin("nope"); ok {
			t.Error("an unbound node must not resolve an operator")
		}
	})

	t.Run("bound node -> login", func(t *testing.T) {
		b := nsUnitBroker(t)
		bindOwnerNode(t, b, "n1", "bownux")
		login, ok := b.operatorLogin("n1")
		if !ok || login != "bownux" {
			t.Errorf("operatorLogin(n1) = (%q,%v), want (bownux,true)", login, ok)
		}
	})

	t.Run("banned owner -> not listable", func(t *testing.T) {
		b := nsUnitBroker(t)
		pub := bindOwnerNode(t, b, "n1", "bownux")
		b.bannedOwners[pub] = true
		if _, ok := b.operatorLogin("n1"); ok {
			t.Error("a banned operator's voice must not list")
		}
	})

	t.Run("owner row missing -> not listable", func(t *testing.T) {
		b := nsUnitBroker(t)
		// Bind the node to a pubkey that has NO owner row (BindNode without BindOwner).
		if err := b.db.BindNode("n1", "orphan-pubkey"); err != nil {
			t.Fatal(err)
		}
		if _, ok := b.operatorLogin("n1"); ok {
			t.Error("a node bound to an owner-less pubkey must not list")
		}
	})

	t.Run("empty login -> not listable", func(t *testing.T) {
		b := nsUnitBroker(t)
		if err := b.db.BindOwner(store.Owner{GitHubID: 1, Login: "", Pubkey: "p-empty"}); err != nil {
			t.Fatal(err)
		}
		if err := b.db.BindNode("n1", "p-empty"); err != nil {
			t.Fatal(err)
		}
		if _, ok := b.operatorLogin("n1"); ok {
			t.Error("an empty login must not list (never emit a placeholder operator)")
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
		b.nodes["n1"] = protocol.NodeRegistration{NodeID: "n1", BridgeURL: "https://secret.trycloudflare.com",
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
