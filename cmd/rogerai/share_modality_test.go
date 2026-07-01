package main

import (
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/agent"
	"github.com/rogerai-fyi/roger/internal/detect"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// captureShareConfig points cmdShare's register seam at a stub that records the agent.Config it
// was handed (so we can assert the modality that flowed through), keeps the go-live non-blocking,
// and restores both on cleanup. Returns a pointer whose target is filled once cmdShare registers.
func captureShareConfig(t *testing.T) *agent.Config {
	t.Helper()
	var got agent.Config
	origStart, origBlock := agentStart, shareBlock
	agentStart = func(c agent.Config) (*agent.Session, error) { got = c; return &agent.Session{}, nil }
	shareBlock = func() {}
	t.Cleanup(func() { agentStart, shareBlock = origStart, origBlock })
	return &got
}

// runShare drives cmdShare in a goroutine (the free path calls waitOnAir + shareBlock, seam'd to
// return) and returns its error, bounded so a hang fails loudly instead of stalling the suite.
func runShare(t *testing.T, cfg config, args []string) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmdShare(cfg, args) }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("cmdShare did not return (waitOnAir/shareBlock seam?)")
		return nil
	}
}

// TestShareModalityFlagOverridesUpstreamPath: on the explicit --upstream path detection is
// skipped, so the modality would default to chat. A --modality tts|stt flag is the operator's
// override and must flow into agent.Config.Modality (which becomes the registered offer's
// modality). Chat (or an absent flag) stays the back-compat default.
func TestShareModalityFlagOverridesUpstreamPath(t *testing.T) {
	cases := []struct {
		name string
		flag []string
		want string
	}{
		{"no flag defaults to chat/back-compat", nil, ""},
		{"--modality tts", []string{"--modality", "tts"}, protocol.ModalityTTS},
		{"--modality stt", []string{"--modality", "stt"}, protocol.ModalitySTT},
		{"--modality chat is explicit chat", []string{"--modality", "chat"}, protocol.ModalityChat},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			got := captureShareConfig(t)

			args := append([]string{"m1", "--upstream", "http://127.0.0.1:1234/v1"}, tc.flag...)
			if err := runShare(t, config{Broker: "https://b", User: "u"}, args); err != nil {
				t.Fatalf("cmdShare(%v) = %v, want nil", tc.flag, err)
			}
			if got.Modality != tc.want {
				t.Errorf("cfgRun.Modality = %q, want %q", got.Modality, tc.want)
			}
		})
	}
}

// TestShareModalityFlagDoesNotOverrideAutoDetection: on the AUTO path (no --upstream) detection
// is authoritative — it read the endpoint and classified the server. An explicit --modality must
// NOT clobber that (e.g. a detected chat server must stay chat, not be mislabeled tts), matching
// the flag's documented "for --upstream" scope. Validation still runs (see the rejects-unknown
// test), but the override only applies on the explicit --upstream path.
func TestShareModalityFlagDoesNotOverrideAutoDetection(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	got := captureShareConfig(t)
	// Auto-detect returns a normal chat server.
	stubDetectFull(t, []detect.Found{{
		Name: "ollama", BaseURL: "http://127.0.0.1:11434/v1", Chat: "http://127.0.0.1:11434/v1/chat/completions",
		Models: []string{"llama3.2"}, Modality: map[string]string{"llama3.2": protocol.ModalityChat},
	}}, nil)

	// --modality tts on the AUTO path (no --upstream) must be ignored for the offer modality.
	if err := runShare(t, config{Broker: "https://b", User: "u"}, []string{"--modality", "tts"}); err != nil {
		t.Fatalf("cmdShare(auto + --modality tts) = %v, want nil", err)
	}
	if got.Modality != protocol.ModalityChat {
		t.Errorf("cfgRun.Modality = %q, want chat (auto-detection is authoritative; --modality is for --upstream)", got.Modality)
	}
}

// TestShareRenamedBareVoiceKeepsModality: the founder's real workflow — a bare voice server
// (auto-detected, synthesized model id "voice"/tts) shared under a custom name via `--model
// roger-operator-voice`. The rename must NOT drop the detected modality: the offer must still go
// on air as tts, or it would be billed as chat/token and mis-routed. Guards the synthesized-offer
// rename path (a bare voice Found carries one modality that applies whatever the operator names it).
func TestShareRenamedBareVoiceKeepsModality(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	got := captureShareConfig(t)
	// Auto-detect returns a synthesized bare-TTS Found (no /v1/models; id defaulted to "voice").
	stubDetectFull(t, []detect.Found{{
		Name: "kokoro", BaseURL: "http://127.0.0.1:8095/v1", Chat: "http://127.0.0.1:8095/v1/chat/completions",
		Models: []string{"voice"}, Modality: map[string]string{"voice": protocol.ModalityTTS},
	}}, nil)

	// No --upstream, so detection runs; --model renames the synthesized offer.
	if err := runShare(t, config{Broker: "https://b", User: "u"}, []string{"--model", "roger-operator-voice"}); err != nil {
		t.Fatalf("cmdShare(rename bare voice) = %v, want nil", err)
	}
	if got.Model != "roger-operator-voice" {
		t.Errorf("cfgRun.Model = %q, want roger-operator-voice", got.Model)
	}
	if got.Modality != protocol.ModalityTTS {
		t.Errorf("cfgRun.Modality = %q, want tts (a rename must not drop the detected modality)", got.Modality)
	}
}

// TestSoleModality: one modality across all entries => that modality; empty or disagreeing
// entries => "" (a mixed server must NOT be collapsed to a single guessed modality).
func TestSoleModality(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]string
		want string
	}{
		{"empty", nil, ""},
		{"single tts", map[string]string{"voice": "tts"}, "tts"},
		{"two agree stt", map[string]string{"a": "stt", "b": "stt"}, "stt"},
		{"disagree -> empty", map[string]string{"a": "tts", "b": "chat"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := soleModality(tc.in); got != tc.want {
				t.Errorf("soleModality(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestShareModalityFlagRejectsUnknown: the modality enum is CLOSED (protocol.ValidModality). An
// unknown --modality is a fat-finger the operator must see BEFORE going on air, not a silently
// mislabeled offer at the broker.
func TestShareModalityFlagRejectsUnknown(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	captureShareConfig(t) // never reached, but keep the go-live seam'd

	err := cmdShare(config{Broker: "https://b", User: "u"},
		[]string{"m1", "--upstream", "http://127.0.0.1:1234/v1", "--modality", "video"})
	if err == nil || !strings.Contains(err.Error(), "modality") {
		t.Fatalf("cmdShare(--modality video) = %v, want a modality-rejection error", err)
	}
}
