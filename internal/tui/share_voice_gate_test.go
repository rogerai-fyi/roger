package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/node"
)

// The broker 400s a tts offer whose display name is empty ("voice name is empty after
// normalization", screenVoiceOffers → slugVoiceName). vc is the zero VoiceConfig until a
// voice goes through the VOICE BOOTH, so a bare `enter` on a tts row would fire a doomed
// register. Founder decision: the TUI BLOCKS a nameless voice on-air with a clear prompt
// (no broker round-trip). stt + chat rows are unaffected.

// TestToggleShareBlocksNamelessVoice: putting a nameless (or voiceless) tts row on air must
// NOT register - the row stays OFF air and the status prompts the VOICE BOOTH. Both on-air
// paths are gated: `toggleShareAt` (enter/a, the OPEN-MARKET toggle) AND `togglePrivateAt`
// (the PRIVATE-band toggle), which shares the same guard so a nameless voice can't slip on
// air the hidden-band way either. Both must block before any doomed broker register.
func TestToggleShareBlocksNamelessVoice(t *testing.T) {
	voices := []struct {
		name  string
		voice node.VoiceConfig // set on the controller before the toggle
	}{
		{"no name and no voice (zero VoiceConfig)", node.VoiceConfig{}},
		{"a voice but still no name", node.VoiceConfig{Voice: "af_heart", Speed: 1.0}},
		{"a name but no voice picked", node.VoiceConfig{Name: "Night Owl"}},
	}
	// Both on-air toggles carry the SAME nameless-voice guard. togglePrivateAt is login-gated,
	// so the harness logs the controller in first - proving the GUARD (not the login gate)
	// blocks the nameless row.
	toggles := []struct {
		name   string
		toggle func(m *model, i int)
	}{
		{"toggleShareAt", func(m *model, i int) { m.toggleShareAt(i) }},
		{"togglePrivateAt", func(m *model, i int) { m.togglePrivateAt(i) }},
	}
	for _, tg := range toggles {
		for _, tc := range voices {
			t.Run(tg.name+"/"+tc.name, func(t *testing.T) {
				// A broker that would 400 a nameless voice (and 200 anything else) - but the guard
				// must fire FIRST, so this handler should never see a register for this row.
				registered := false
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.Contains(r.URL.Path, "register") {
						registered = true
					}
					_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
				}))
				defer srv.Close()

				mm := New(srv.URL, "tester")
				mm.width, mm.height = 100, 30
				mm.mode = modeShare
				mm.loggedIn = true // so the private-band gate isn't the reason for the block
				mm.ctrl.SetLoggedIn(true)
				mm.setShareRows([]shareRow{{model: "kokoro-82m", modality: "tts", ctx: 4096}})
				if tc.voice.Name != "" || tc.voice.Voice != "" {
					mm.ctrl.SetVoiceConfig("kokoro-82m", tc.voice)
				}
				mm.syncShareCache()
				mm.shareCursor = 0

				tg.toggle(&mm, 0)
				mm.syncShareCache()

				if mm.shares["kokoro-82m"] != nil {
					t.Errorf("a nameless tts row must stay OFF air, not register")
				}
				if registered {
					t.Errorf("a nameless tts row must not fire a broker register (block before the round-trip)")
				}
				st := stripANSI(mm.status)
				if !strings.Contains(st, "needs a name") || !strings.Contains(strings.ToLower(st), "voice booth") {
					t.Errorf("status should prompt the VOICE BOOTH before going on air, got %q", st)
				}
				if !strings.Contains(st, "p") {
					t.Errorf("status should name the p affordance, got %q", st)
				}
			})
		}
	}
}

// TestToggleShareNamedVoiceGoesOnAir: a tts row WITH both a name and a voice still goes on
// air normally (the guard only blocks the nameless/voiceless case).
func TestToggleShareNamedVoiceGoesOnAir(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer srv.Close()

	mm := New(srv.URL, "tester")
	mm.width, mm.height = 100, 30
	mm.mode = modeShare
	mm.setShareRows([]shareRow{{model: "kokoro-82m", modality: "tts", ctx: 4096}})
	mm.ctrl.SetVoiceConfig("kokoro-82m", node.VoiceConfig{Name: "Night Owl", Voice: "af_heart", Speed: 1.0})
	mm.syncShareCache()
	mm.shareCursor = 0

	mm.toggleShareAt(0)
	mm.syncShareCache()
	t.Cleanup(func() {
		if s := mm.shares["kokoro-82m"]; s != nil {
			s.Stop()
		}
	})

	if mm.shares["kokoro-82m"] == nil {
		t.Errorf("a NAMED tts row should go on air, got status %q", stripANSI(mm.status))
	}
	if !strings.Contains(stripANSI(mm.status), "ON AIR") {
		t.Errorf("a named tts row on-air status should read ON AIR, got %q", stripANSI(mm.status))
	}
}

// TestToggleShareSTTAndChatUnaffected: stt and chat rows go on air with no name (they are
// not voices-with-a-DJ), so the nameless guard must NOT touch them.
func TestToggleShareSTTAndChatUnaffected(t *testing.T) {
	for _, modality := range []string{"stt", ""} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		}))
		mm := New(srv.URL, "tester")
		mm.width, mm.height = 100, 30
		mm.mode = modeShare
		mm.setShareRows([]shareRow{{model: "some-model", modality: modality, ctx: 4096}})
		mm.syncShareCache()
		mm.shareCursor = 0

		mm.toggleShareAt(0)
		mm.syncShareCache()
		if s := mm.shares["some-model"]; s != nil {
			s.Stop()
		} else {
			t.Errorf("modality %q should go on air (unaffected by the voice-name guard), status %q", modality, stripANSI(mm.status))
		}
		srv.Close()
	}
}
