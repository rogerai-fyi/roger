package main

// voice_persist_test.go pins applyShareVoice's founder-approved (2026-07-02) sole-profile
// recovery: a headless voice share keeps its display name + sample_url across a model-id drift
// (the recurring "voice came back as raw 'voice', no sample" gap). The saved share_voices profile
// is adopted by exact key, else - for a VOICE offer with EXACTLY ONE saved profile - by that sole
// profile (only one identity in play, so nothing can bleed). A chat offer or an ambiguous
// multi-profile config is left untouched (never guess). Fast unit coverage of the guards; the
// full share path is covered by TestShareTTSProfileKeyIsTheSharedModelID.

import (
	"testing"

	"github.com/rogerai-fyi/roger/internal/agent"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

func TestApplyShareVoiceExactKey(t *testing.T) {
	cfg := config{Voices: map[string]ShareVoice{
		"roger-operator-voice": {Name: "1950s Operator", Language: "en", SampleURL: "https://x/s.mp3", Voice: "af_heart", Speed: 1.1},
	}}
	run := &agent.Config{Model: "roger-operator-voice", Modality: protocol.ModalityTTS}
	applyShareVoice(cfg, "roger-operator-voice", run)
	if run.Name != "1950s Operator" || run.SampleURL != "https://x/s.mp3" || run.Voice != "af_heart" || run.Speed != 1.1 {
		t.Fatalf("exact-key profile not applied: %+v", run)
	}
}

// The model id drifted to the default "voice" on restart, but the profile is keyed
// "roger-operator-voice". With ONE saved voice profile + a tts offer, adopt it.
func TestApplyShareVoiceSoleFallbackOnDrift(t *testing.T) {
	cfg := config{Voices: map[string]ShareVoice{
		"roger-operator-voice": {Name: "1950s Operator", SampleURL: "https://x/s.mp3", Voice: "af_heart"},
	}}
	run := &agent.Config{Model: "voice", Modality: protocol.ModalityTTS} // detected as the bare default
	applyShareVoice(cfg, "voice", run)
	if run.Name != "1950s Operator" || run.SampleURL != "https://x/s.mp3" || run.Voice != "af_heart" {
		t.Fatalf("sole-profile fallback did not restore the voice identity on id drift: %+v", run)
	}
}

// A CHAT offer must never inherit a voice profile via the sole fallback (it would mislabel a
// text model with a voice identity).
func TestApplyShareVoiceNoFallbackForChat(t *testing.T) {
	cfg := config{Voices: map[string]ShareVoice{
		"roger-operator-voice": {Name: "1950s Operator", SampleURL: "https://x/s.mp3"},
	}}
	run := &agent.Config{Model: "gpt-oss-20b", Modality: protocol.ModalityChat}
	applyShareVoice(cfg, "gpt-oss-20b", run)
	if run.Name != "" || run.SampleURL != "" {
		t.Fatalf("a chat offer must not inherit a voice profile: %+v", run)
	}
}

// Two saved profiles is ambiguous - the sole fallback must NOT guess; only an exact key applies.
func TestApplyShareVoiceAmbiguousNoFallback(t *testing.T) {
	cfg := config{Voices: map[string]ShareVoice{
		"voice-a": {Name: "A", SampleURL: "https://x/a.mp3"},
		"voice-b": {Name: "B", SampleURL: "https://x/b.mp3"},
	}}
	run := &agent.Config{Model: "voice-c", Modality: protocol.ModalityTTS}
	applyShareVoice(cfg, "voice-c", run)
	if run.Name != "" || run.SampleURL != "" {
		t.Fatalf("an ambiguous multi-profile config must not guess a voice identity: %+v", run)
	}
}
