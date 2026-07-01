package main

import (
	"testing"
)

// TestShareVoiceFlagFlowsToConfig: `roger share --voice <id-or-blend>` sets the offer's DEFAULT
// voice on the --upstream (tts) path, so a headless voice share carries the operator's picked voice
// (single id OR blend string) — the node injects it into a /v1/audio/speech request that omits
// `voice`. An absent flag leaves it empty (unchanged). Speed rides via --voice-speed.
func TestShareVoiceFlagFlowsToConfig(t *testing.T) {
	cases := []struct {
		name      string
		flag      []string
		wantVoice string
		wantSpeed float64
	}{
		{"no flag leaves voice empty", nil, "", 0},
		{"single id", []string{"--voice", "am_onyx"}, "am_onyx", 0},
		{"blend string", []string{"--voice", "af_heart:0.5+af_aoede:0.5"}, "af_heart:0.5+af_aoede:0.5", 0},
		{"voice + speed", []string{"--voice", "am_onyx", "--voice-speed", "1.25"}, "am_onyx", 1.25},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			got := captureShareConfig(t)

			args := append([]string{"roger-operator-voice", "--upstream", "http://127.0.0.1:1234/v1", "--modality", "tts"}, tc.flag...)
			if err := runShare(t, config{Broker: "https://b", User: "u"}, args); err != nil {
				t.Fatalf("cmdShare(%v) = %v, want nil", tc.flag, err)
			}
			if got.Voice != tc.wantVoice {
				t.Errorf("cfgRun.Voice = %q, want %q", got.Voice, tc.wantVoice)
			}
			if got.Speed != tc.wantSpeed {
				t.Errorf("cfgRun.Speed = %v, want %v", got.Speed, tc.wantSpeed)
			}
		})
	}
}
