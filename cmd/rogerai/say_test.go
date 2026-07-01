package main

// say_test.go table-tests the PURE helpers behind `roger say` / `roger voices` (the cost line, the
// roster line, the char count) plus a couple of command-level branches the feature file does not
// pin down directly (the played-vs-saved status line). The end-to-end signed-POST / money-gate
// behavior is driven through godog in features/voice/say.feature (say_bdd_test.go).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/client"
)

// TestSayResultLine: the success line reads `spoke N chars · $X` (N = rune count), the billed cost
// via the canonical money renderer; a saved-file result reads the path instead.
func TestSayResultLine(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		res    client.SpeakResult
		played bool
		path   string
		want   string
	}{
		{"played paid", "hello", client.SpeakResult{Cost: 0.0008}, true, "", "spoke 5 chars · $0.0008"},
		{"played free", "hello", client.SpeakResult{Cost: 0}, true, "", "spoke 5 chars · $0.00"},
		{"unicode rune count", "héllo", client.SpeakResult{Cost: 0}, true, "", "spoke 5 chars"},
		{"saved to file", "hi", client.SpeakResult{Cost: 0.0001}, false, "/tmp/x.wav", "/tmp/x.wav"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sayResultLine(tc.text, tc.res, tc.played, tc.path)
			if !strings.Contains(got, tc.want) {
				t.Errorf("sayResultLine = %q, want to contain %q", got, tc.want)
			}
		})
	}
}

// TestVoiceRosterLine: each voice renders as `name · by @operator · language · $price/1k chars`,
// with FREE for a zero-price/free voice and the id to pass to --voice.
func TestVoiceRosterLine(t *testing.T) {
	paid := client.Voice{ID: "v-heart", NamespacedID: "@bownux/operator", Operator: "bownux", Name: "Operator", Language: "en-US", PricePer1kChars: 0.02}
	line := voiceRosterLine(paid)
	for _, want := range []string{"Operator", "@bownux", "en-US", "0.02"} {
		if !strings.Contains(line, want) {
			t.Errorf("paid roster line %q missing %q", line, want)
		}
	}
	free := client.Voice{ID: "v-free", Operator: "acme", Name: "Kiosk", Free: true}
	if fl := voiceRosterLine(free); !strings.Contains(fl, "Kiosk") || !strings.Contains(fl, "FREE") {
		t.Errorf("free roster line %q must name the voice and read FREE", fl)
	}
	// A voice with no display name falls back to the raw id (never a blank name), and a nameless
	// operator-less voice still renders its --voice handle.
	nameless := client.Voice{ID: "bare-voice", PricePer1kChars: 0.5}
	if nl := voiceRosterLine(nameless); !strings.Contains(nl, "bare-voice") || !strings.Contains(nl, "--voice bare-voice") {
		t.Errorf("nameless roster line %q must fall back to the id for both label and handle", nl)
	}
}

// TestVoiceHandle: the --voice id a consumer copies prefers the namespaced alias when present, else
// the raw id (both route at the broker; the namespaced one is the human-friendly copy).
func TestVoiceHandle(t *testing.T) {
	if h := voiceHandle(client.Voice{ID: "raw", NamespacedID: "@op/name"}); h != "@op/name" {
		t.Errorf("voiceHandle prefers the namespaced id, got %q", h)
	}
	if h := voiceHandle(client.Voice{ID: "raw"}); h != "raw" {
		t.Errorf("voiceHandle falls back to the raw id, got %q", h)
	}
}

// TestSayMissingVoiceHint: with no --voice the command errors WITHOUT touching the broker, and the
// message points at `roger voices` and the website (so a consumer knows where to find a voice id).
func TestSayMissingVoiceHint(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	err := cmdSay(config{Broker: "http://127.0.0.1:1", User: "u"}, []string{"hello", "there"})
	if err == nil {
		t.Fatal("cmdSay with no --voice must error")
	}
	if !strings.Contains(err.Error(), "roger voices") || !strings.Contains(err.Error(), "rogerai.fyi/voices") {
		t.Errorf("missing-voice error should name `roger voices` + the site, got %q", err)
	}
}

// TestSayNoTextUsage: --voice but no text is a usage error, again without any broker call.
func TestSayNoTextUsage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	err := cmdSay(config{Broker: "http://127.0.0.1:1", User: "u"}, []string{"--voice", "af_heart"})
	if err == nil {
		t.Fatal("cmdSay with no text must error")
	}
}

// TestSayBadFlag: an unknown flag is a parse error the command surfaces (and, being a parse error,
// it never reaches the broker).
func TestSayBadFlag(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := cmdSay(config{Broker: "http://127.0.0.1:1", User: "u"}, []string{"--nope"}); err == nil {
		t.Fatal("cmdSay with an unknown flag must error")
	}
}

// TestSayPlayAndSaveBothFail: the ONE genuinely unhappy playback case — the player could neither
// play NOR save (no path, an error) — is surfaced rather than swallowed. Drives the real cmdSay
// against a live broker with a player stub that fails hard.
func TestSayPlayAndSaveBothFail(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t)
	orig := sayPlayer
	t.Cleanup(func() { sayPlayer = orig })
	sayPlayer = func([]byte) (string, bool, error) { return "", false, errPlayFail }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RogerAI-Cost", "0")
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFFwav"))
	}))
	defer srv.Close()

	err := cmdSay(config{Broker: srv.URL, User: "u"}, []string{"--voice", "af_heart", "hi"})
	if err == nil || !strings.Contains(err.Error(), "could not play or save") {
		t.Fatalf("cmdSay(play+save fail) = %v, want a 'could not play or save' error", err)
	}
}

// errPlayFail is a fixed error for the play-and-save-both-fail case.
var errPlayFail = errString("no audio device and disk is full")

type errString string

func (e errString) Error() string { return string(e) }
