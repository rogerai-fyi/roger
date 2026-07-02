package main

// share_voice_config_test.go specs the config.json half of the voice sample_url plumbing:
// a per-model `share_voices` block (the sibling of share_prices) that lets an operator set
// a display name + hosted sample clip URL (+ default voice/speed/language) on a voice
// share. Three surfaces:
//   - parse/save round-trip, with empty fields OMITTED (the "empty stays omitted" contract);
//   - tuiHooks seeds Hooks.SavedVoices from it (the TUI / web-console controller path);
//   - headless `roger share` seeds the offer identity from it, an explicit --voice /
//     --voice-speed flag winning (the seedSharePricing convention: set it in config, it
//     applies when you share headless).
//
// The CLI does NOT validate the URL - the broker owns voice-metadata validation/moderation
// (never pre-reject what the broker accepts). Real config files in an isolated dir + the
// existing agentStart capture seam; no mocks.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/tui"
)

func TestShareVoicesConfigRoundTrip(t *testing.T) {
	t.Run("full entry parses and survives a save/load round-trip", func(t *testing.T) {
		useTempConfig(t)
		raw := `{
  "broker": "https://b.local",
  "user": "op",
  "share_voices": {
    "kokoro": {
      "name": "1950s Operator",
      "voice": "af_heart:0.5+af_aoede:0.5",
      "speed": 1.25,
      "language": "en-US",
      "sample_url": "https://cdn.example/operator-sample.mp3"
    }
  }
}`
		if err := os.MkdirAll(filepath.Dir(configPath()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath(), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		c := loadConfig()
		want := ShareVoice{Name: "1950s Operator", Voice: "af_heart:0.5+af_aoede:0.5", Speed: 1.25,
			Language: "en-US", SampleURL: "https://cdn.example/operator-sample.mp3"}
		if got := c.Voices["kokoro"]; got != want {
			t.Fatalf("loadConfig share_voices[kokoro] = %+v, want %+v", got, want)
		}
		if err := saveConfig(c); err != nil {
			t.Fatal(err)
		}
		if got := loadConfig().Voices["kokoro"]; got != want {
			t.Errorf("round-tripped share_voices[kokoro] = %+v, want %+v", got, want)
		}
		b, err := os.ReadFile(configPath())
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{`"share_voices"`, `"name"`, `"sample_url"`} {
			if !strings.Contains(string(b), key) {
				t.Errorf("saved config is missing %s:\n%s", key, b)
			}
		}
	})

	t.Run("empty fields stay omitted within an entry", func(t *testing.T) {
		useTempConfig(t)
		if err := saveConfig(config{Broker: "https://b.local", User: "op",
			Voices: map[string]ShareVoice{"kokoro": {Name: "Plain DJ"}}}); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(configPath())
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{`"sample_url"`, `"voice"`, `"speed"`, `"language"`} {
			if strings.Contains(string(b), key) {
				t.Errorf("an unset field must be OMITTED from the saved entry, found %s in:\n%s", key, b)
			}
		}
	})

	t.Run("no share_voices block stays omitted and loads nil (old configs unchanged)", func(t *testing.T) {
		useTempConfig(t)
		if err := saveConfig(config{Broker: "https://b.local", User: "op"}); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(configPath())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "share_voices") {
			t.Errorf("a config with no voice profiles must not carry a share_voices key:\n%s", b)
		}
		if v := loadConfig().Voices; v != nil {
			t.Errorf("loadConfig().Voices = %+v, want nil for an old/absent block", v)
		}
	})
}

// tuiHooks threads the saved share_voices block into Hooks.SavedVoices (nil when absent),
// so NewController arms the identity on the shared node controller at TUI startup.
func TestTuiHooksSeedSavedVoices(t *testing.T) {
	useTempConfig(t)
	h := tuiHooks(config{Voices: map[string]ShareVoice{
		"kokoro": {Name: "1950s Operator", Voice: "af_heart", Speed: 1.25, Language: "en-US",
			SampleURL: "https://cdn.example/op.mp3"},
	}})
	want := tui.VoiceConfig{Name: "1950s Operator", Voice: "af_heart", Speed: 1.25, Language: "en-US",
		SampleURL: "https://cdn.example/op.mp3"}
	if got := h.SavedVoices["kokoro"]; got != want {
		t.Errorf("tuiHooks SavedVoices[kokoro] = %+v, want %+v", got, want)
	}
	if h2 := tuiHooks(config{}); h2.SavedVoices != nil {
		t.Errorf("tuiHooks with no share_voices should leave SavedVoices nil, got %+v", h2.SavedVoices)
	}
}

// Headless `roger share`: the saved share_voices profile rides cfgRun (name/language/
// sample_url have no flags and come only from the profile); an explicit --voice /
// --voice-speed always wins over the saved default.
func TestShareVoiceProfileFlowsToConfig(t *testing.T) {
	profile := map[string]ShareVoice{"roger-operator-voice": {
		Name: "1950s Operator", Voice: "af_heart:0.5+af_aoede:0.5", Speed: 1.25, Language: "en-US",
		SampleURL: "https://cdn.example/operator-sample.mp3"}}
	cases := []struct {
		name       string
		voices     map[string]ShareVoice
		flags      []string
		wantName   string
		wantVoice  string
		wantSpeed  float64
		wantLang   string
		wantSample string
	}{
		{
			name:       "saved profile rides the offer with no flags",
			voices:     profile,
			wantName:   "1950s Operator",
			wantVoice:  "af_heart:0.5+af_aoede:0.5",
			wantSpeed:  1.25,
			wantLang:   "en-US",
			wantSample: "https://cdn.example/operator-sample.mp3",
		},
		{
			name:       "an explicit --voice wins over the saved default voice",
			voices:     profile,
			flags:      []string{"--voice", "am_onyx"},
			wantName:   "1950s Operator",
			wantVoice:  "am_onyx",
			wantSpeed:  1.25,
			wantLang:   "en-US",
			wantSample: "https://cdn.example/operator-sample.mp3",
		},
		{
			name:       "an explicit --voice-speed wins over the saved default speed",
			voices:     profile,
			flags:      []string{"--voice-speed", "0.9"},
			wantName:   "1950s Operator",
			wantVoice:  "af_heart:0.5+af_aoede:0.5",
			wantSpeed:  0.9,
			wantLang:   "en-US",
			wantSample: "https://cdn.example/operator-sample.mp3",
		},
		{
			name: "no saved profile leaves the identity empty (omitted end to end)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTempConfig(t)
			got := captureShareConfig(t)

			args := []string{"roger-operator-voice", "--upstream", "http://127.0.0.1:1234/v1", "--modality", "tts"}
			args = append(args, tc.flags...)
			if err := runShare(t, config{Broker: "https://b", User: "u", Voices: tc.voices}, args); err != nil {
				t.Fatalf("cmdShare(%v) = %v, want nil", tc.flags, err)
			}
			if got.Name != tc.wantName {
				t.Errorf("cfgRun.Name = %q, want %q", got.Name, tc.wantName)
			}
			if got.Voice != tc.wantVoice {
				t.Errorf("cfgRun.Voice = %q, want %q", got.Voice, tc.wantVoice)
			}
			if got.Speed != tc.wantSpeed {
				t.Errorf("cfgRun.Speed = %v, want %v", got.Speed, tc.wantSpeed)
			}
			if got.Language != tc.wantLang {
				t.Errorf("cfgRun.Language = %q, want %q", got.Language, tc.wantLang)
			}
			if got.SampleURL != tc.wantSample {
				t.Errorf("cfgRun.SampleURL = %q, want %q", got.SampleURL, tc.wantSample)
			}
		})
	}
}
