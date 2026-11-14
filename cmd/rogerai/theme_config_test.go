package main

// The `theme` and `colorblind` config keys: set / get / bare listing, the per-run
// ROGER_THEME / ROGER_COLORBLIND overrides, and the TUI's save hooks. Real config file
// under a temp XDG_CONFIG_HOME, like palette_config_test.go.

import (
	"strings"
	"testing"
)

func TestThemeColorblindDefaults(t *testing.T) {
	t.Setenv("ROGER_THEME", "")
	t.Setenv("ROGER_COLORBLIND", "")
	if got := themeFromConfig(config{}); got != "auto" {
		t.Errorf("unset theme should be auto, got %q", got)
	}
	if got := colorblindFromConfig(config{}); got != "off" {
		t.Errorf("unset colorblind should be off, got %q", got)
	}
	// A saved value nobody recognizes (an old or hand-edited file) reads as the default.
	if got := themeFromConfig(config{Theme: "sepia"}); got != "auto" {
		t.Errorf("unknown saved theme should read as auto, got %q", got)
	}
}

// A valid env value wins for the run in any case; a value it does not recognize is
// ignored, so a typo never wipes the saved setting (the ROGER_DECK / ROGER_PALETTE rule).
func TestThemeColorblindEnvOverride(t *testing.T) {
	saved := config{Theme: "light", Colorblind: "deutan"}
	cases := []struct {
		env, theme, colorblind string
	}{
		{"", "light", "deutan"},
		{"Dark", "dark", "deutan"},
		{"sepia", "light", "deutan"},
	}
	for _, c := range cases {
		t.Setenv("ROGER_THEME", c.env)
		t.Setenv("ROGER_COLORBLIND", "")
		if got := themeFromConfig(saved); got != c.theme {
			t.Errorf("ROGER_THEME=%q: got %q, want %q", c.env, got, c.theme)
		}
	}
	t.Setenv("ROGER_THEME", "")
	for env, want := range map[string]string{"": "deutan", "Protan": "protan", " TRITAN ": "tritan", "protanopia": "deutan"} {
		t.Setenv("ROGER_COLORBLIND", env)
		if got := colorblindFromConfig(saved); got != want {
			t.Errorf("ROGER_COLORBLIND=%q: got %q, want %q", env, got, want)
		}
	}
}

// set accepts any case, stores the lower-case value, and clears the key for the default.
func TestThemeColorblindSetGet(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGER_THEME", "")
	t.Setenv("ROGER_COLORBLIND", "")

	if err := cmdConfig([]string{"set", "theme", "Light"}); err != nil {
		t.Fatalf("set theme Light: %v", err)
	}
	if err := cmdConfig([]string{"set", "colorblind", "TRITAN"}); err != nil {
		t.Fatalf("set colorblind TRITAN: %v", err)
	}
	c := loadConfig()
	if c.Theme != "light" || c.Colorblind != "tritan" {
		t.Fatalf("stored theme=%q colorblind=%q, want light / tritan", c.Theme, c.Colorblind)
	}
	out := captureStdout(t, func() {
		if err := cmdConfig([]string{"get", "theme"}); err != nil {
			t.Fatalf("get theme: %v", err)
		}
		if err := cmdConfig([]string{"get", "colorblind"}); err != nil {
			t.Fatalf("get colorblind: %v", err)
		}
	})
	if strings.Fields(out)[0] != "light" || strings.Fields(out)[1] != "tritan" {
		t.Errorf("get printed %q, want light then tritan", out)
	}
	bare := captureStdout(t, func() {
		if err := cmdConfig(nil); err != nil {
			t.Fatalf("bare config: %v", err)
		}
	})
	if !strings.Contains(bare, "theme = light") || !strings.Contains(bare, "colorblind = tritan") {
		t.Errorf("bare listing should show both settings:\n%s", bare)
	}

	// An unknown value errors with the full list and keeps what was stored.
	err := cmdConfig([]string{"set", "theme", "sepia"})
	if err == nil || !strings.Contains(err.Error(), "auto|light|dark") {
		t.Errorf("set theme sepia should be a usage error listing the modes, got %v", err)
	}
	err = cmdConfig([]string{"set", "colorblind", "red"})
	if err == nil || !strings.Contains(err.Error(), "off|protan|deutan|tritan") {
		t.Errorf("set colorblind red should be a usage error listing the modes, got %v", err)
	}
	if c := loadConfig(); c.Theme != "light" || c.Colorblind != "tritan" {
		t.Errorf("a rejected set must not change the config: theme=%q colorblind=%q", c.Theme, c.Colorblind)
	}

	// The default clears the key, so the file stays clean.
	if err := cmdConfig([]string{"set", "theme", "auto"}); err != nil {
		t.Fatalf("set theme auto: %v", err)
	}
	if err := cmdConfig([]string{"set", "colorblind", "Off"}); err != nil {
		t.Fatalf("set colorblind Off: %v", err)
	}
	if c := loadConfig(); c.Theme != "" || c.Colorblind != "" {
		t.Errorf("auto / off should clear the keys, got theme=%q colorblind=%q", c.Theme, c.Colorblind)
	}
}

// The TUI's /theme and /colorblind persist through these hooks.
func TestThemeColorblindSaveHooks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h := tuiHooks(loadConfig())
	h.SaveTheme("dark")
	h.SaveColorblind("protan")
	if c := loadConfig(); c.Theme != "dark" || c.Colorblind != "protan" {
		t.Fatalf("hooks should save dark / protan, got theme=%q colorblind=%q", c.Theme, c.Colorblind)
	}
	h.SaveTheme("bogus") // never written: the TUI only passes listed modes, and this guards it
	h.SaveColorblind("bogus")
	if c := loadConfig(); c.Theme != "dark" || c.Colorblind != "protan" {
		t.Errorf("an unknown value must not be saved, got theme=%q colorblind=%q", c.Theme, c.Colorblind)
	}
	h.SaveTheme("auto")
	h.SaveColorblind("off")
	if c := loadConfig(); c.Theme != "" || c.Colorblind != "" {
		t.Errorf("auto / off should clear the keys, got theme=%q colorblind=%q", c.Theme, c.Colorblind)
	}
}
