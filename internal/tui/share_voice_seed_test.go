package tui

// share_voice_seed_test.go specs the host-config side of the voice sample_url plumbing in
// the TUI: (1) Hooks.SavedVoices (the config.json share_voices block) seeds the shared node
// controller, so a saved voice identity arms the model before any BOOTH pass; (2) a BOOTH
// save must NOT clobber the config-set sample_url - the BOOTH has no sample field, so
// commitVoiceBooth preserves the stored SampleURL while updating name/voice/speed/language.
// Real model + real controller (vbSeed), no mocks.

import (
	"testing"

	"github.com/rogerai-fyi/roger/internal/node"
)

func TestSavedVoicesSeedController(t *testing.T) {
	ctrl := NewController("http://broker.local", Hooks{Station: "swift-fox",
		SavedVoices: map[string]VoiceConfig{
			"kokoro": {Name: "1950s Operator", Voice: "af_heart", Speed: 1.25, Language: "en-US",
				SampleURL: "https://cdn.example/op.mp3"},
		}})
	want := node.VoiceConfig{Name: "1950s Operator", Voice: "af_heart", Speed: 1.25, Language: "en-US",
		SampleURL: "https://cdn.example/op.mp3"}
	if vc := ctrl.VoiceConfigFor("kokoro"); vc != want {
		t.Errorf("VoiceConfigFor(kokoro) = %+v, want the SavedVoices entry %+v", vc, want)
	}
	if vc := ctrl.VoiceConfigFor("gpt-oss-20b"); vc != (node.VoiceConfig{}) {
		t.Errorf("VoiceConfigFor an unsaved model = %+v, want zero", vc)
	}
}

func TestBoothCommitPreservesSampleURL(t *testing.T) {
	m := vbSeed(t)
	// The operator set a sample clip in config.json (share_voices); the session seeded it.
	m.ctrl.SetVoiceConfig("kokoro", node.VoiceConfig{Name: "Old DJ", SampleURL: "https://cdn.example/op.mp3"})
	m.vbName = "DJ Test"
	m.vbVoice = "am_onyx"
	m.vbSpeed = 1.1
	m.vbLang = "en-US"
	m.vbPrice = "0.02"
	if !m.commitVoiceBooth() {
		t.Fatalf("commit failed: %q", m.vbErr)
	}
	vc := m.ctrl.VoiceConfigFor("kokoro")
	if vc.Name != "DJ Test" || vc.Voice != "am_onyx" || vc.Speed != 1.1 || vc.Language != "en-US" {
		t.Errorf("commit stored %+v, want the BOOTH-edited name/voice/speed/language", vc)
	}
	if vc.SampleURL != "https://cdn.example/op.mp3" {
		t.Errorf("commit clobbered SampleURL = %q, want the config-set sample preserved (the BOOTH has no sample field)", vc.SampleURL)
	}
}
