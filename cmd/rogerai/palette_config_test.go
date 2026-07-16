package main

// Increment 0 (TUI design overhaul): the `palette` config key - the persisted,
// env-overridable one-switch that points the color layer at `full` (the lamp
// board) or `mono` (the mono+red escape hatch). Settings surface mirrors
// `webui-open`: set / get / bare-listing, junk errors, JSON round-trip. No mocks -
// real config file under a temp XDG_CONFIG_HOME.
//
// Spec approved 2026-07-15 (tui-design-overhaul-brief.md, increment 0, group A).

import (
	"strings"
	"testing"
)

// A1 - unset config defaults to the full lamp board.
func TestPaletteDefaultFull(t *testing.T) {
	if got := paletteFromConfig(config{}); got != "full" {
		t.Errorf("unset palette should default to full, got %q", got)
	}
}

// A2/A3 - set persists both modes; reload reads them back.
func TestPaletteSetGetPersist(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := cmdConfig([]string{"set", "palette", "mono"}); err != nil {
		t.Fatalf("set palette mono: %v", err)
	}
	if got := paletteFromConfig(loadConfig()); got != "mono" {
		t.Fatalf("set palette mono did not persist, got %q", got)
	}
	if err := cmdConfig([]string{"set", "palette", "full"}); err != nil {
		t.Fatalf("set palette full: %v", err)
	}
	if got := paletteFromConfig(loadConfig()); got != "full" {
		t.Fatalf("set palette full did not persist, got %q", got)
	}
}

// A4 - an invalid value errors and leaves the stored config untouched.
func TestPaletteSetInvalidErrorsAndKeeps(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := cmdConfig([]string{"set", "palette", "mono"}); err != nil {
		t.Fatalf("seed set palette mono: %v", err)
	}
	if err := cmdConfig([]string{"set", "palette", "neon"}); err == nil {
		t.Fatal("set palette neon should error (unknown mode)")
	}
	if got := paletteFromConfig(loadConfig()); got != "mono" {
		t.Fatalf("invalid set must not clobber stored value, got %q", got)
	}
}

// A5 - `config get palette` prints the effective mode.
func TestPaletteGetPrints(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := cmdConfig([]string{"set", "palette", "mono"}); err != nil {
		t.Fatalf("set palette mono: %v", err)
	}
	out := captureStdout(t, func() {
		if err := cmdConfig([]string{"get", "palette"}); err != nil {
			t.Fatalf("get palette: %v", err)
		}
	})
	if strings.TrimSpace(out) != "mono" {
		t.Fatalf("get palette printed %q, want mono", strings.TrimSpace(out))
	}
}

// A6 - the bare `config` listing shows the palette line.
func TestPaletteInBareListing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := cmdConfig([]string{"set", "palette", "mono"}); err != nil {
		t.Fatalf("set palette mono: %v", err)
	}
	out := captureStdout(t, func() {
		if err := cmdConfig(nil); err != nil {
			t.Fatalf("bare config: %v", err)
		}
	})
	if !strings.Contains(out, "palette = mono") {
		t.Fatalf("bare listing missing `palette = mono`:\n%s", out)
	}
}

// A7 - JSON round-trip through save/load with omitempty (default file stays clean).
func TestPaletteRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := loadConfig()
	c.Palette = "mono"
	if err := saveConfig(c); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := loadConfig().Palette; got != "mono" {
		t.Fatalf("round-trip lost palette, got %q", got)
	}
}

// A8 - ROGER_PALETTE env beats the persisted config for the run; a junk env value
// is ignored (falls back to the stored config) - the test-time flip the brief wants.
func TestPaletteEnvOverride(t *testing.T) {
	t.Setenv("ROGER_PALETTE", "mono")
	if got := paletteFromConfig(config{Palette: "full"}); got != "mono" {
		t.Fatalf("ROGER_PALETTE=mono should win over config full, got %q", got)
	}
	t.Setenv("ROGER_PALETTE", "full")
	if got := paletteFromConfig(config{Palette: "mono"}); got != "full" {
		t.Fatalf("ROGER_PALETTE=full should win over config mono, got %q", got)
	}
	t.Setenv("ROGER_PALETTE", "garbage")
	if got := paletteFromConfig(config{Palette: "mono"}); got != "mono" {
		t.Fatalf("junk ROGER_PALETTE must be ignored, fall back to config mono, got %q", got)
	}
}
