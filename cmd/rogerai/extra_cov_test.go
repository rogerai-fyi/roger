package main

import (
	"fmt"
	"io"
	"testing"

	"github.com/rogerai-fyi/roger/internal/update"
)

// TestCmdUpgrade covers cmdUpgrade's --check (available + up-to-date) and the install
// path via the updateCheck / updateUpgrade seams, so no real GitHub call is made.
func TestCmdUpgrade(t *testing.T) {
	origCheck, origUp := updateCheck, updateUpgrade
	t.Cleanup(func() { updateCheck, updateUpgrade = origCheck, origUp })

	// --check, a newer version available -> Notice() branch.
	updateCheck = func(cur string) (update.CheckResult, error) {
		return update.CheckResult{Current: cur, Latest: "9.9.9", Available: true}, nil
	}
	if err := cmdUpgrade([]string{"--check"}); err != nil {
		t.Errorf("cmdUpgrade(--check, available) = %v", err)
	}
	// --check, up to date -> the "up to date" branch.
	updateCheck = func(cur string) (update.CheckResult, error) {
		return update.CheckResult{Current: cur, Available: false}, nil
	}
	if err := cmdUpgrade([]string{"--check"}); err != nil {
		t.Errorf("cmdUpgrade(--check, up to date) = %v", err)
	}
	// --check, network error -> swallowed (returns nil).
	updateCheck = func(string) (update.CheckResult, error) {
		return update.CheckResult{}, fmt.Errorf("offline")
	}
	if err := cmdUpgrade([]string{"--check"}); err != nil {
		t.Errorf("cmdUpgrade(--check, offline) = %v, want nil", err)
	}
	// No --check -> the install path delegates to updateUpgrade; surface its error.
	updateUpgrade = func(string, io.Writer) error { return fmt.Errorf("boom") }
	if err := cmdUpgrade(nil); err == nil {
		t.Error("cmdUpgrade(install) should surface the upgrade error")
	}
}

// TestDefaultUser covers both branches of defaultUser: a set $USER, and the "anon"
// fallback when it is empty.
func TestDefaultUser(t *testing.T) {
	t.Setenv("USER", "alice")
	if got := defaultUser(); got != "alice" {
		t.Errorf("defaultUser() = %q, want alice", got)
	}
	t.Setenv("USER", "")
	if got := defaultUser(); got != "anon" {
		t.Errorf("defaultUser() (empty) = %q, want anon", got)
	}
}

// TestLoadConfigEnvOverrides covers loadConfig's ROGER_BROKER / ROGER_USER env overrides.
func TestLoadConfigEnvOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGER_BROKER", "https://env-broker.test")
	t.Setenv("ROGER_USER", "env-user")
	c := loadConfig()
	if c.Broker != "https://env-broker.test" || c.User != "env-user" {
		t.Fatalf("loadConfig env overrides = %+v", c)
	}
}

// TestTuiHooksSeeded covers tuiHooks's seeding branches: a saved Share (model + upstream
// + key) and saved per-model Prices flow into the returned hooks.
func TestTuiHooksSeeded(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{
		Broker:  "https://b",
		Compact: true,
		Share: &Share{
			Model: "m1", PriceIn: 0.2, PriceOut: 0.3,
			Upstream: "http://127.0.0.1:11434/v1", UpstreamKey: "sk-x", MaxOnAir: 6,
		},
		Prices: map[string]SharePrice{
			"m1": {PriceIn: 0.2, PriceOut: 0.3, Windows: []SchedWindow{{Start: "03:00", End: "03:30", Free: true}}},
		},
	}
	h := tuiHooks(cfg)
	if h.ShareModel != "m1" || h.ShareUpstream != "http://127.0.0.1:11434/v1" || h.ShareUpstreamKey != "sk-x" {
		t.Fatalf("tuiHooks did not seed the saved share config: %+v", h)
	}
	if h.ShareMaxOnAir != 6 || !h.Compact {
		t.Errorf("tuiHooks max-on-air/compact = %d/%v, want 6/true", h.ShareMaxOnAir, h.Compact)
	}
	if p, ok := h.SavedPrices["m1"]; !ok || p.Out != 0.3 || len(p.Windows) != 1 {
		t.Errorf("tuiHooks did not seed saved prices: %+v", h.SavedPrices)
	}
}

// TestRunWizardNonInteractive covers runWizard's non-interactive, no-force-flag return
// (ran=false, config unchanged) and maybeOnboard's wrapper around it.
func TestRunWizardNonInteractive(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: "https://b"}
	got, ran, err := runWizard(cfg, wizardOpts{})
	if err != nil || ran {
		t.Fatalf("runWizard(non-interactive) = ran %v err %v, want ran=false nil", ran, err)
	}
	if got.Onboarded {
		t.Error("non-interactive runWizard should not mark onboarded")
	}
	// maybeOnboard on a not-yet-onboarded, non-interactive run returns the config as-is.
	if out := maybeOnboard(config{Broker: "https://b"}); out.Onboarded {
		t.Error("maybeOnboard(non-interactive) should not onboard")
	}
}

// TestDispatchEvenMore covers the dispatch routes that delegate to non-blocking handlers
// (limits via cmdConfig, and the use/connect/tune aliases against an empty market).
func TestDispatchEvenMore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	empty := config{Broker: fakeBrokerEmpty(t), User: "u"}
	if err := dispatch(empty, []string{"limits"}); err != nil {
		t.Errorf("dispatch(limits) = %v", err)
	}
	for _, verb := range []string{"use", "connect", "tune"} {
		if err := dispatch(empty, []string{verb, "ghost-model"}); err != nil {
			t.Errorf("dispatch(%s ghost) = %v", verb, err)
		}
	}
}
