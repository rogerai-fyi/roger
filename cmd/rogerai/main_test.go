package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeUpstream(t *testing.T) {
	const want = "http://127.0.0.1:8060/v1/chat/completions"
	cases := map[string]string{
		"http://127.0.0.1:8060":                      want, // base URL (the natural input)
		"http://127.0.0.1:8060/":                     want, // trailing slash
		"http://127.0.0.1:8060/v1":                   want, // /v1 base
		"http://127.0.0.1:8060/v1/":                  want, // /v1 with slash
		"http://127.0.0.1:8060/v1/chat/completions":  want, // already full (idempotent)
		"http://127.0.0.1:8060/v1/chat/completions/": want, // full + slash
	}
	for in, exp := range cases {
		if got := normalizeUpstream(in); got != exp {
			t.Errorf("normalizeUpstream(%q) = %q, want %q", in, got, exp)
		}
	}
	if got := normalizeUpstream(""); got != "" {
		t.Errorf("normalizeUpstream(\"\") = %q, want empty", got)
	}
}

// TestParseMonthlyCap verifies the `rogerai limit --monthly` value parsing: a dollar
// amount (with or without a leading $), the clear spellings (0/off/none/unlimited),
// and the invalid cases.
func TestParseMonthlyCap(t *testing.T) {
	ok := map[string]float64{
		"25": 25, "$25": 25, " 25 ": 25, "25.50": 25.5,
		"0": 0, "off": 0, "OFF": 0, "none": 0, "unlimited": 0, "$0": 0,
	}
	for in, want := range ok {
		got, err := parseMonthlyCap(in)
		if err != nil {
			t.Errorf("parseMonthlyCap(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseMonthlyCap(%q) = %v, want %v", in, got, want)
		}
	}
	for _, bad := range []string{"-5", "abc", "$-1", "12x"} {
		if _, err := parseMonthlyCap(bad); err == nil {
			t.Errorf("parseMonthlyCap(%q) should have errored", bad)
		}
	}
}

// TestLimitsLoadSaveBackCompat verifies the new limits section round-trips and
// that an OLD config with NO limits section still loads (back-compat) with no
// caps. Uses XDG_CONFIG_HOME so configPath() points at a temp dir.
// TestShareMaxOnAirDefault: the soft local on-air cap defaults to 4 and is read from
// share.max_on_air, surviving a save/reload. The default holds for an old config with
// no share section.
func TestShareMaxOnAirDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("ROGER_BROKER", "")
	t.Setenv("ROGER_USER", "")

	// No share section at all -> default 4.
	if err := os.MkdirAll(filepath.Dir(configPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath(), []byte(`{"broker":"https://b.example"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := loadConfig().shareMaxOnAir(); got != 4 {
		t.Fatalf("default share.max_on_air = %d, want 4", got)
	}
	if defaultShareMaxOnAir != 4 {
		t.Fatalf("defaultShareMaxOnAir = %d, want 4", defaultShareMaxOnAir)
	}

	// A configured share.max_on_air is read back.
	c := loadConfig()
	c.Share = &Share{Model: "m", MaxOnAir: 8}
	if err := saveConfig(c); err != nil {
		t.Fatal(err)
	}
	if got := loadConfig().shareMaxOnAir(); got != 8 {
		t.Errorf("configured share.max_on_air = %d, want 8", got)
	}
	// A non-positive value falls back to the default (it is a deliberate restart knob).
	c2 := loadConfig()
	c2.Share.MaxOnAir = 0
	_ = saveConfig(c2)
	if got := loadConfig().shareMaxOnAir(); got != 4 {
		t.Errorf("zero share.max_on_air should fall back to the default 4, got %d", got)
	}
}

func TestLimitsLoadSaveBackCompat(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("ROGER_BROKER", "")
	t.Setenv("ROGER_USER", "")

	// 1) Old config: no "limits" key at all - must load, no caps.
	old := `{"broker":"https://b.example","user":"luis"}`
	if err := os.MkdirAll(filepath.Dir(configPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath(), []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	c := loadConfig()
	if c.Broker != "https://b.example" || c.User != "luis" {
		t.Fatalf("old config lost fields: %+v", c)
	}
	lim, typ := c.resolve("anything")
	if lim != (Limit{}) || typ != 800 {
		t.Errorf("old config should resolve to no caps + default 800, got %+v typ=%d", lim, typ)
	}

	// 2) Set a per-model + default limit, save, reload.
	c.Limits.Default = Limit{MaxOut: 0.40}
	c.Limits.Models = map[string]Limit{"qwen3-coder-30b": {MaxOut: 0.30, MinTPS: 40}}
	c.Limits.TypicalOutTok = 1000
	if err := saveConfig(c); err != nil {
		t.Fatal(err)
	}
	c2 := loadConfig()
	if got, typ := c2.resolve("qwen3-coder-30b"); got.MaxOut != 0.30 || got.MinTPS != 40 || typ != 1000 {
		t.Errorf("per-model resolve = %+v typ=%d, want max-out 0.30 min-tps 40 typ 1000", got, typ)
	}
	if got, _ := c2.resolve("unpinned-model"); got.MaxOut != 0.40 {
		t.Errorf("default resolve = %+v, want max-out 0.40", got)
	}
}
