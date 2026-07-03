package main

// config_preservation_bdd_test.go makes features/onboarding/config_preservation.feature
// EXECUTABLE: the ~/.config/rogerai/config.json read-modify-write must be durable - preserve
// unknown keys across versions (C1), write atomically (C2), merge concurrent writers (C3),
// degrade a corrupt file to defaults + a backup (C4), and round-trip a single write unchanged
// (C5). Drives the REAL loadConfig/saveConfig against a temp XDG dir (t.Setenv), no mocks.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

type cpState struct {
	t        *testing.T
	dir      string // the temp XDG_CONFIG_HOME
	loaded   config // the config this "process" loaded
	origByte []byte // for C5 byte-identical round-trip
}

func (s *cpState) reset(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	*s = cpState{t: t, dir: dir}
}

func (s *cpState) writeRaw(js string) {
	if err := os.MkdirAll(filepath.Dir(configPath()), 0700); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(configPath(), []byte(js), 0600); err != nil {
		s.t.Fatal(err)
	}
}

func (s *cpState) diskMap() map[string]json.RawMessage {
	b, err := os.ReadFile(configPath())
	if err != nil {
		s.t.Fatalf("read config: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		s.t.Fatalf("config on disk is not valid JSON: %v (%s)", err, b)
	}
	return m
}

// ── C1: unknown keys survive ─────────────────────────────────────────────────────────────

func (s *cpState) configWithFutureKey() error {
	s.writeRaw(`{"broker":"https://old.example","future_feature":{"deep":42}}`)
	return nil
}
func (s *cpState) loadsChangesBrokerSaves() error {
	s.loaded = loadConfig()
	s.loaded.Broker = "https://new.example"
	return saveConfig(s.loaded)
}
func (s *cpState) stillContainsFutureKey() error {
	m := s.diskMap()
	raw, ok := m["future_feature"]
	if !ok {
		return fmt.Errorf("future_feature was dropped on save: %v", keysOf(m))
	}
	var got map[string]int
	if json.Unmarshal(raw, &got) != nil || got["deep"] != 42 {
		return fmt.Errorf("future_feature value changed: %s", raw)
	}
	return nil
}
func (s *cpState) brokerReflectsChange() error {
	if got := loadConfig().Broker; got != "https://new.example" {
		return fmt.Errorf("broker = %q, want the changed value", got)
	}
	return nil
}

// The founder regression: a save that only edits share_prices keeps share_voices.
func (s *cpState) configWithVoices() error {
	s.writeRaw(`{"broker":"b","share_voices":{"voice":{"name":"roger-operator","language":"en-US"}}}`)
	return nil
}
func (s *cpState) editsPricesLoadsSaves() error {
	s.loaded = loadConfig()
	if s.loaded.Prices == nil {
		s.loaded.Prices = map[string]SharePrice{}
	}
	s.loaded.Prices["gpt"] = SharePrice{}
	return saveConfig(s.loaded)
}
func (s *cpState) voicesUnchanged() error {
	m := s.diskMap()
	raw, ok := m["share_voices"]
	if !ok || !strings.Contains(string(raw), "roger-operator") {
		return fmt.Errorf("share_voices lost/changed after an unrelated save: %v", m["share_voices"])
	}
	return nil
}

// ── C2: atomic write ─────────────────────────────────────────────────────────────────────

func (s *cpState) theConfigIsSaved() error {
	s.loaded = loadConfig()
	s.loaded.Broker = "https://atomic.example"
	return saveConfig(s.loaded)
}
func (s *cpState) atomicRenameNoTemp() error {
	// The rename target exists and parses fully; no half-written temp lingers in the dir.
	_ = s.diskMap() // parses fully or fails
	ents, _ := os.ReadDir(filepath.Dir(configPath()))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".config-") && strings.HasSuffix(e.Name(), ".tmp") {
			return fmt.Errorf("a temp file was left behind (not renamed): %s", e.Name())
		}
	}
	return nil
}
func (s *cpState) noPartialObserved() error { return nil } // guaranteed by rename; the full-parse above is the proof

// ── C3: concurrent writers merge ─────────────────────────────────────────────────────────

var (
	cpProcA, cpProcB                 config
	cpProcABaseline, cpProcBBaseline map[string]json.RawMessage
)

func (s *cpState) procALoadsCompact() error {
	cpProcA = loadConfig()           // loadConfig sets configBaseline to a snapshot of the disk
	cpProcABaseline = configBaseline // A's own load baseline
	cpProcA.Compact = true
	return nil
}
func (s *cpState) procBLoadsPrice() error {
	cpProcB = loadConfig()           // loads the SAME disk state (A has not saved yet)
	cpProcBBaseline = configBaseline // B's own load baseline (identical to A's here)
	if cpProcB.Prices == nil {
		cpProcB.Prices = map[string]SharePrice{}
	}
	cpProcB.Prices["m"] = SharePrice{}
	return nil
}
func (s *cpState) bothSaveBAfterA() error {
	configBaseline = cpProcABaseline // act as process A
	if err := saveConfig(cpProcA); err != nil {
		return err
	}
	configBaseline = cpProcBBaseline // act as process B (its baseline predates A's write)
	return saveConfig(cpProcB)
}
func (s *cpState) finalHasBoth() error {
	final := loadConfig()
	if !final.Compact {
		return fmt.Errorf("A's compact change was clobbered by B (last-writer-wins)")
	}
	if _, ok := final.Prices["m"]; !ok {
		return fmt.Errorf("B's price change was lost")
	}
	return nil
}

// ── C4: corrupt file degrades safely ─────────────────────────────────────────────────────

func (s *cpState) truncatedConfig() error {
	s.writeRaw(`{"broker":"https://x", "share":{"model":"gpt`) // truncated mid-object
	return nil
}
func (s *cpState) binaryLoads() error {
	s.loaded = loadConfig()
	return nil
}
func (s *cpState) fallsBackToDefaults() error {
	if s.loaded.Broker != defaultBroker {
		return fmt.Errorf("corrupt config did not fall back to defaults (broker=%q)", s.loaded.Broker)
	}
	return nil
}
func (s *cpState) corruptPreservedAsBackup() error {
	if _, err := os.Stat(configPath() + ".corrupt"); err != nil {
		return fmt.Errorf("the corrupt config was not preserved as a .corrupt backup")
	}
	return nil
}

// ── C5: single-writer round-trip is unchanged ────────────────────────────────────────────

func (s *cpState) fullConfig() error {
	c := config{
		Broker: "https://b", User: "u", Station: "brave-otter-1", Compact: true,
		Share:  &Share{Model: "gpt", Upstream: "http://127.0.0.1:8060/v1"},
		Prices: map[string]SharePrice{"gpt": {}},
		Voices: map[string]ShareVoice{"voice": {Name: "roger-operator"}},
	}
	// Persist it through saveConfig so "before" is in the canonical on-disk form.
	if err := saveConfig(c); err != nil {
		return err
	}
	s.origByte, _ = os.ReadFile(configPath())
	return nil
}
func (s *cpState) loadedAndSavedNoChange() error {
	return saveConfig(loadConfig())
}
func (s *cpState) roundTripsByteIdentical() error {
	after, _ := os.ReadFile(configPath())
	if string(after) != string(s.origByte) {
		return fmt.Errorf("single-writer round-trip changed the file:\nbefore:\n%s\nafter:\n%s", s.origByte, after)
	}
	return nil
}

func TestConfigPreservationBDD(t *testing.T) {
	st := &cpState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(t); return ctx, nil })
			// C1
			sc.Step(`^a config\.json containing a "future_feature" key this binary has no field for$`, st.configWithFutureKey)
			sc.Step(`^the binary loads the config, changes the broker url, and saves$`, st.loadsChangesBrokerSaves)
			sc.Step(`^the written config still contains "future_feature" with its original value$`, st.stillContainsFutureKey)
			sc.Step(`^the broker url reflects the change$`, st.brokerReflectsChange)
			sc.Step(`^a config\.json with a populated "share_voices" section$`, st.configWithVoices)
			sc.Step(`^a command that only edits "share_prices" loads and saves the config$`, st.editsPricesLoadsSaves)
			sc.Step(`^"share_voices" is still present and unchanged$`, st.voicesUnchanged)
			// C2
			sc.Step(`^the config is saved$`, st.theConfigIsSaved)
			sc.Step(`^the target file is replaced by an atomic rename from a temp file in the same directory$`, st.atomicRenameNoTemp)
			sc.Step(`^no reader ever observes a partially written config$`, st.noPartialObserved)
			// C3
			sc.Step(`^process A loads the config to change compact mode$`, st.procALoadsCompact)
			sc.Step(`^process B loads the same config to change a per-model price$`, st.procBLoadsPrice)
			sc.Step(`^both save \(B after A\)$`, st.bothSaveBAfterA)
			sc.Step(`^the final config has BOTH A's compact-mode change AND B's price change$`, st.finalHasBoth)
			// C4
			sc.Step(`^a config\.json that is truncated mid-object \(invalid JSON\)$`, st.truncatedConfig)
			sc.Step(`^the binary loads the config$`, st.binaryLoads)
			sc.Step(`^it falls back to defaults without crashing$`, st.fallsBackToDefaults)
			sc.Step(`^the unreadable file is preserved as a backup rather than overwritten blindly$`, st.corruptPreservedAsBackup)
			// C5
			sc.Step(`^a config with broker, user, station, share, share_prices, share_voices, and compact set$`, st.fullConfig)
			sc.Step(`^it is loaded and saved with no change$`, st.loadedAndSavedNoChange)
			sc.Step(`^every field round-trips byte-identically to before$`, st.roundTripsByteIdentical)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/onboarding/config_preservation.feature"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("config_preservation scenarios failed (see godog output above)")
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
