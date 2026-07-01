package node

// voice_config_test.go is the spec for threading the SHARE VOICE BOOTH's result (dj-name +
// voice/blend string + speed + language) from the controller onto the agent.Config the node goes
// on air with. The BOOTH configures a voice model; when that model goes on air, its offer must
// carry the operator's chosen Name/Voice/Speed/Language so a consumer gets the picked voice.
//
// It uses a REAL Controller with the agent.Start seam captured (startAgent) so the built Config is
// inspected without a live broker — no mocks of the domain, just a boundary seam at the process
// edge (the same testability refactor CLAUDE.md endorses for hard-to-reach glue).

import (
	"testing"

	"github.com/rogerai-fyi/roger/internal/agent"
)

// swapStartAgent replaces the process-edge start seam with fn for the duration of a test and
// returns a restore closure, so a test inspects the built agent.Config without a live broker.
func swapStartAgent(fn func(agent.Config) (*agent.Session, error)) func() {
	prev := startAgent
	startAgent = fn
	return func() { startAgent = prev }
}

func TestVoiceConfigThreadsOntoAgentConfig(t *testing.T) {
	var got agent.Config
	restore := swapStartAgent(func(cfg agent.Config) (*agent.Session, error) {
		got = cfg
		return &agent.Session{}, nil // a non-nil session so ToggleOnAir records it on-air
	})
	defer restore()

	c := New(Config{Broker: "http://broker.local", Station: "swift-fox", HW: "cpu"})
	c.SetLoggedIn(true)
	c.SetRows([]ShareRow{{Model: "kokoro", Modality: "tts", Ctx: 4096, Upstream: "http://127.0.0.1:8880/v1/chat/completions"}})

	// The BOOTH result for this model: a named DJ on a weighted blend at a set speed.
	c.SetVoiceConfig("kokoro", VoiceConfig{
		Name:     "1950s Operator",
		Voice:    "af_heart:0.5+af_aoede:0.5",
		Speed:    1.25,
		Language: "en-US",
	})

	res := c.ToggleOnAir("kokoro")
	if res.Err != nil {
		t.Fatalf("ToggleOnAir errored: %v", res.Err)
	}
	if got.Modality != "tts" {
		t.Errorf("agent Config modality = %q, want tts", got.Modality)
	}
	if got.Name != "1950s Operator" {
		t.Errorf("agent Config Name = %q, want %q", got.Name, "1950s Operator")
	}
	if got.Voice != "af_heart:0.5+af_aoede:0.5" {
		t.Errorf("agent Config Voice = %q, want the blend string", got.Voice)
	}
	if got.Speed != 1.25 {
		t.Errorf("agent Config Speed = %v, want 1.25", got.Speed)
	}
	if got.Language != "en-US" {
		t.Errorf("agent Config Language = %q, want en-US", got.Language)
	}
}

// VoiceConfigFor returns the stored config, and is empty for a model that never went through the
// BOOTH (a plain chat model shares with no voice metadata).
func TestVoiceConfigForDefaultsEmpty(t *testing.T) {
	c := New(Config{Broker: "http://broker.local", Station: "swift-fox"})
	if vc := c.VoiceConfigFor("gpt-oss-20b"); vc != (VoiceConfig{}) {
		t.Errorf("VoiceConfigFor an unconfigured model = %+v, want zero", vc)
	}
	c.SetVoiceConfig("kokoro", VoiceConfig{Name: "DJ", Voice: "am_onyx"})
	if vc := c.VoiceConfigFor("kokoro"); vc.Name != "DJ" || vc.Voice != "am_onyx" {
		t.Errorf("VoiceConfigFor(kokoro) = %+v, want {Name:DJ Voice:am_onyx}", vc)
	}
}

// A model that never went through the BOOTH goes on air with NO voice metadata (no regression: a
// plain chat share is unchanged).
func TestNoVoiceConfigMeansNoVoiceMetadata(t *testing.T) {
	var got agent.Config
	restore := swapStartAgent(func(cfg agent.Config) (*agent.Session, error) {
		got = cfg
		return &agent.Session{}, nil
	})
	defer restore()

	c := New(Config{Broker: "http://broker.local", Station: "swift-fox"})
	c.SetLoggedIn(true)
	c.SetRows([]ShareRow{{Model: "gpt-oss-20b", Ctx: 4096, Upstream: "http://127.0.0.1:8080/v1/chat/completions"}})
	if res := c.ToggleOnAir("gpt-oss-20b"); res.Err != nil {
		t.Fatalf("ToggleOnAir errored: %v", res.Err)
	}
	if got.Voice != "" || got.Name != "" || got.Speed != 0 {
		t.Errorf("a non-voice share must carry NO voice metadata; got Voice=%q Name=%q Speed=%v", got.Voice, got.Name, got.Speed)
	}
}
