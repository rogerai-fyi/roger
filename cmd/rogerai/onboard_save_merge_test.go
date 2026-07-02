package main

// onboard_save_merge_test.go specs the CONCURRENT-WRITER contract for the onboarding
// wizard's config save (the 2026-07-02 07:58 live loss: a rogerai process exiting
// saved config.json from the in-memory snapshot it loaded at startup, deleting the
// share_voices section another writer had added to the file in between).
//
// The codebase's merge convention: every config writer RE-READS config.json and
// mutates only the section it owns before saving (saveStation, tuiLimits.Save,
// SavePrice, SaveUpstream, SaveCompact, cmdShare's upstream save, drphil's broker
// fix all do `c := loadConfig(); c.<owned> = ...; saveConfig(c)`). The wizard save
// (maybeOnboard / cmdOnboard) was the one writer that instead persisted the WHOLE
// startup snapshot - stale by however long the operator sat in the interactive form -
// so any section that landed on disk after this process loaded (share_voices,
// share_prices, station, ...) was silently deleted.
//
// Contract pinned here:
//   - a wizard save persists ONLY the sections the wizard owns (onboarded + share)
//     onto a FRESH read of the file; every other section survives verbatim;
//   - the wizard's own outcome still persists exactly as before (keep-path still
//     saves; --free still records share.model/port + onboarded);
//   - the non-interactive maybeOnboard path still never writes at all.
//
// Real files in an isolated config dir (useTempConfig), real detection stub seam;
// no mocks.

import (
	"os"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/detect"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// addSectionsBehindOurBack simulates the OTHER writer: after the process under test
// has loaded its startup config, a second rogerai (TUI SavePrice hook, a share run,
// an operator edit) re-reads the FILE and adds the voice/pricing/station sections.
func addSectionsBehindOurBack(t *testing.T) {
	t.Helper()
	c := loadConfig()
	c.Voices = map[string]ShareVoice{"voice": {
		Name: "roger-operator", Language: "en-US",
		SampleURL: "https://rogerai.fyi/assets/voice-samples/roger-operator-1950s.mp3"}}
	c.Prices = map[string]SharePrice{"voice": {PriceOut: 0.30}}
	c.Station = "eager-puma-54"
	if err := saveConfig(c); err != nil {
		t.Fatal(err)
	}
}

// assertSectionsSurvived asserts the concurrently-added sections are still on disk
// after the writer under test saved.
func assertSectionsSurvived(t *testing.T, when string) {
	t.Helper()
	c := loadConfig()
	wantVoice := ShareVoice{Name: "roger-operator", Language: "en-US",
		SampleURL: "https://rogerai.fyi/assets/voice-samples/roger-operator-1950s.mp3"}
	if got := c.Voices["voice"]; got != wantVoice {
		t.Errorf("%s: share_voices[voice] = %+v, want %+v (the save clobbered a section it does not own)", when, got, wantVoice)
	}
	if got := c.Prices["voice"]; got.PriceOut != 0.30 {
		t.Errorf("%s: share_prices[voice].price_out = %v, want 0.30 (the save clobbered a section it does not own)", when, got.PriceOut)
	}
	if c.Station != "eager-puma-54" {
		t.Errorf("%s: station = %q, want eager-puma-54 (the save clobbered a section it does not own)", when, c.Station)
	}
	// Belt and braces on the raw bytes: the keys must still be present in the file.
	b, err := os.ReadFile(configPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"share_voices"`, `"share_prices"`, `"station"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("%s: saved config lost the %s section:\n%s", when, key, b)
		}
	}
}

// The exact live shape: `roger onboard` opened on a config snapshot, the operator
// left it on "keep" (the non-interactive default), and the exiting process saved -
// share_voices/share_prices/station added to the FILE in between must SURVIVE.
func TestOnboardKeepSaveDoesNotClobberConcurrentSections(t *testing.T) {
	useTempConfig(t)
	if err := saveConfig(config{Broker: "https://b.local", User: "op", Onboarded: true}); err != nil {
		t.Fatal(err)
	}
	cfg := loadConfig() // the process's startup snapshot

	addSectionsBehindOurBack(t)

	// Non-interactive run resolves to the keep path, which STILL saves (pinned
	// behavior: cmdOnboard persists even when the wizard made no changes).
	if err := cmdOnboard(cfg, nil); err != nil {
		t.Fatalf("cmdOnboard(keep) = %v, want nil", err)
	}
	assertSectionsSurvived(t, "after cmdOnboard keep-path save")
	if !loadConfig().Onboarded {
		t.Error("cmdOnboard(keep) must keep onboarded=true persisted (existing save behavior)")
	}
}

// The wizard-completed shape: `roger onboard --free` runs detection (seconds of
// stale window in production; minutes when the form is interactive) and saves its
// outcome. The outcome must land WITHOUT deleting sections added in between.
func TestOnboardFreeSaveMergesWizardOutcomeOntoFreshConfig(t *testing.T) {
	useTempConfig(t)
	if err := saveConfig(config{Broker: "https://b.local", User: "op"}); err != nil {
		t.Fatal(err)
	}
	cfg := loadConfig() // startup snapshot: not onboarded, no share
	stubDetectFull(t, []detect.Found{{
		Name: "ollama", BaseURL: "http://127.0.0.1:11434/v1",
		Chat:   "http://127.0.0.1:11434/v1/chat/completions",
		Models: []string{"llama3.2"}, Modality: map[string]string{"llama3.2": protocol.ModalityChat},
	}}, nil)

	addSectionsBehindOurBack(t)

	if err := cmdOnboard(cfg, []string{"--free"}); err != nil {
		t.Fatalf("cmdOnboard(--free) = %v, want nil", err)
	}
	assertSectionsSurvived(t, "after cmdOnboard --free save")

	// The wizard's OWN sections still persist exactly as before (pinned behavior).
	got := loadConfig()
	if !got.Onboarded {
		t.Error("cmdOnboard(--free) must persist onboarded=true")
	}
	if got.Share == nil || got.Share.Model != "llama3.2" || got.Share.Upstream != "http://127.0.0.1:11434/v1" {
		t.Errorf("cmdOnboard(--free) must persist the wizard's share outcome, got %+v", got.Share)
	}
}

// The launch path guard: a non-interactive maybeOnboard must not write the file at
// all (so "fixing" the clobber by saving more often would fail here).
func TestMaybeOnboardNonInteractiveNeverWrites(t *testing.T) {
	useTempConfig(t)
	if err := saveConfig(config{Broker: "https://b.local", User: "op"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(configPath())
	if err != nil {
		t.Fatal(err)
	}
	_ = maybeOnboard(loadConfig())
	after, err := os.ReadFile(configPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("maybeOnboard(non-interactive) wrote the config file:\nbefore: %s\nafter:  %s", before, after)
	}
}
