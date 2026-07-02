package node

// voice_sample_url_test.go is the spec for the voice sample_url share plumbing (the iOS
// EXTERNAL-READINESS gap: a shared voice could never carry a hosted sample clip because
// VoiceConfig had no SampleURL and nothing mapped one into agent.Config). Table-driven over
// the two ways a voice identity reaches the controller - seeded from the host's saved config
// (Config.Voices, the config.json share_voices block) or set live by the BOOTH
// (SetVoiceConfig) - plus the empty/unset corners: the built agent.Config must carry the
// SampleURL exactly (agent.Start already forwards Config.SampleURL onto the registration
// offer), and an unset sample must stay EMPTY so protocol omitempty keeps it off the wire.
// The node does NOT validate the URL - the broker owns voice-metadata validation/moderation
// (VOICE-SHARING-SPEC: never pre-reject what the broker accepts).
//
// Same REAL Controller + startAgent process-edge seam as voice_config_test.go; no mocks.

import (
	"testing"

	"github.com/rogerai-fyi/roger/internal/agent"
)

func TestVoiceSampleURLPlumbing(t *testing.T) {
	cases := []struct {
		name       string
		seed       map[string]VoiceConfig // node.Config.Voices - the host's saved share_voices block
		set        *VoiceConfig           // SetVoiceConfig (the BOOTH path); nil = not called
		wantName   string
		wantSample string
	}{
		{
			name: "BOOTH-set sample rides onto agent.Config",
			set: &VoiceConfig{Name: "1950s Operator", Voice: "af_heart", Speed: 1.0, Language: "en-US",
				SampleURL: "https://cdn.example/operator-sample.mp3"},
			wantName:   "1950s Operator",
			wantSample: "https://cdn.example/operator-sample.mp3",
		},
		{
			name: "config-seeded Voices entry rides with no BOOTH pass",
			seed: map[string]VoiceConfig{"kokoro": {Name: "Night DJ", Language: "en-GB",
				SampleURL: "https://cdn.example/night.mp3"}},
			wantName:   "Night DJ",
			wantSample: "https://cdn.example/night.mp3",
		},
		{
			name:       "a BOOTH save replaces the seeded identity wholesale",
			seed:       map[string]VoiceConfig{"kokoro": {Name: "Old DJ", SampleURL: "https://cdn.example/old.mp3"}},
			set:        &VoiceConfig{Name: "New DJ", SampleURL: "https://cdn.example/new.mp3"},
			wantName:   "New DJ",
			wantSample: "https://cdn.example/new.mp3",
		},
		{
			name:       "no sample stays empty (protocol omitempty keeps it off the wire)",
			set:        &VoiceConfig{Name: "Plain DJ", Voice: "am_onyx"},
			wantName:   "Plain DJ",
			wantSample: "",
		},
		{
			name:       "an unconfigured model carries no voice metadata at all",
			wantName:   "",
			wantSample: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got agent.Config
			restore := swapStartAgent(func(cfg agent.Config) (*agent.Session, error) {
				got = cfg
				return &agent.Session{}, nil
			})
			defer restore()

			c := New(Config{Broker: "http://broker.local", Station: "swift-fox", HW: "cpu", Voices: tc.seed})
			c.SetLoggedIn(true)
			c.SetRows([]ShareRow{{Model: "kokoro", Modality: "tts", Ctx: 4096, Upstream: "http://127.0.0.1:8880/v1/chat/completions"}})
			if tc.set != nil {
				c.SetVoiceConfig("kokoro", *tc.set)
			}
			if res := c.ToggleOnAir("kokoro"); res.Err != nil {
				t.Fatalf("ToggleOnAir errored: %v", res.Err)
			}
			if got.Name != tc.wantName {
				t.Errorf("agent Config Name = %q, want %q", got.Name, tc.wantName)
			}
			if got.SampleURL != tc.wantSample {
				t.Errorf("agent Config SampleURL = %q, want %q", got.SampleURL, tc.wantSample)
			}
		})
	}
}

// A host-seeded Config.Voices entry must be visible to VoiceConfigFor, so the BOOTH reopens
// pre-filled with the saved identity (its save can then preserve the config-set sample - the
// BOOTH itself has no sample field; see the tui commit spec). Seeding is a COPY: a later
// SetVoiceConfig must never write back into the host's map.
func TestConfigVoicesSeedVoiceConfigFor(t *testing.T) {
	hostMap := map[string]VoiceConfig{"kokoro": {Name: "1950s Operator", Voice: "af_heart",
		Speed: 1.25, Language: "en-US", SampleURL: "https://cdn.example/op.mp3"}}
	c := New(Config{Broker: "http://broker.local", Station: "swift-fox", Voices: hostMap})

	want := VoiceConfig{Name: "1950s Operator", Voice: "af_heart", Speed: 1.25, Language: "en-US",
		SampleURL: "https://cdn.example/op.mp3"}
	if vc := c.VoiceConfigFor("kokoro"); vc != want {
		t.Errorf("VoiceConfigFor(kokoro) = %+v, want the seeded entry %+v", vc, want)
	}
	if vc := c.VoiceConfigFor("gpt-oss-20b"); vc != (VoiceConfig{}) {
		t.Errorf("VoiceConfigFor an unseeded model = %+v, want zero", vc)
	}

	c.SetVoiceConfig("kokoro", VoiceConfig{Name: "Replaced"})
	if hostMap["kokoro"] != want {
		t.Errorf("SetVoiceConfig mutated the host's seed map to %+v - New must copy, not alias", hostMap["kokoro"])
	}
}
