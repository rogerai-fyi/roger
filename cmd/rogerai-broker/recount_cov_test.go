package main

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// TestSettleRecountPrompt covers the input re-count settle path: the zero-doubt byte
// floor (clamp to body length), the impossible-input ban past the margin, the
// recount-disabled passthrough, and the sidecar exact-recount settle on the smaller count.
func TestSettleRecountPrompt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGERAI_REDIS_URL", "")
	_, priv, _ := ed25519.GenerateKey(nil)

	t.Run("clamp + ban via the byte floor (no sidecar)", func(t *testing.T) {
		t.Setenv("TOKENIZER_URL", "") // recount disabled -> byte floor is the only defense
		b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)

		// bodyLen 0 -> no clamp; recount disabled -> claim passes through.
		if got := b.settleRecountPrompt("n1", "r1", "m", "", 100, 0); got != 100 {
			t.Errorf("no-body settle = %d, want 100", got)
		}
		// claimed > body but within the margin -> clamp to body, no ban.
		if got := b.settleRecountPrompt("n1", "r2", "m", "", 100, 50); got != 50 {
			t.Errorf("within-margin settle = %d, want clamp to 50", got)
		}
		if b.banned["n1"] {
			t.Error("a within-margin overage must NOT ban the node")
		}
		// claimed wildly over body (> margin 8192) -> impossible-input flag + clamp.
		if got := b.settleRecountPrompt("n-bad", "r3", "m", "", 20000, 10); got != 10 {
			t.Errorf("impossible-input settle = %d, want clamp to 10", got)
		}
	})

	t.Run("sidecar exact recount settles on the smaller count", func(t *testing.T) {
		sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"tokens":80,"exact":true}`))
		}))
		defer sidecar.Close()
		t.Setenv("TOKENIZER_URL", sidecar.URL) // recount enabled
		b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
		_ = b.db.BindNode("n2", "acct")

		// claim 100, sidecar exact-recounts 80 (< claim) -> settle on 80.
		if got := b.settleRecountPrompt("n2", "r4", "m", "a fairly long prompt", 100, 0); got != 80 {
			t.Errorf("sidecar settle = %d, want 80 (the smaller verified count)", got)
		}
	})
}

// TestLoadModerationProviders covers the moderation loader's provider resolution from env
// (groq key -> groq backend; url -> url backend; require flag).
func TestLoadModerationProviders(t *testing.T) {
	t.Setenv("MODERATION_GROQ_KEY", "gk")
	t.Setenv("ROGERAI_REQUIRE_MODERATION", "1")
	m := loadModeration()
	if !m.require || m.groqKey != "gk" || len(m.csamCats) == 0 {
		t.Errorf("loadModeration(groq) = %+v, want require + groq key + csam set", m)
	}
	t.Setenv("MODERATION_GROQ_KEY", "")
	t.Setenv("MODERATION_URL", "http://mod.local")
	if m2 := loadModeration(); m2.url != "http://mod.local" {
		t.Errorf("loadModeration(url) url = %q, want http://mod.local", m2.url)
	}
}
