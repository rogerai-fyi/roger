package main

// Increment 10 of the TUI design overhaul: the tube WARM-UP BOOT, gated ONCE PER VERSION
// (founder ruling §5.6). It plays on the first-ever run and after an update/upgrade, never
// on an ordinary re-launch - tracked by last_seen_version in config.

import "testing"

func TestBootShouldPlay(t *testing.T) {
	cases := []struct {
		name     string
		lastSeen string
		version  string
		want     bool
	}{
		{"first-ever run (nothing seen)", "", "5.3.9", true},
		{"same version re-launch", "5.3.9", "5.3.9", false},
		{"after an upgrade", "5.3.8", "5.3.9", true},
		{"after a downgrade still differs", "5.4.0", "5.3.9", true},
	}
	for _, c := range cases {
		if got := bootShouldPlay(c.lastSeen, c.version); got != c.want {
			t.Errorf("%s: bootShouldPlay(%q,%q) = %v, want %v", c.name, c.lastSeen, c.version, got, c.want)
		}
	}
}

// The config round-trips last_seen_version (omitempty; a fresh config has it empty).
func TestLastSeenVersionRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if loadConfig().LastSeenVersion != "" {
		t.Error("a fresh config should have no last_seen_version")
	}
	c := loadConfig()
	c.LastSeenVersion = "5.3.9"
	if err := saveConfig(c); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := loadConfig().LastSeenVersion; got != "5.3.9" {
		t.Errorf("round-trip lost last_seen_version, got %q", got)
	}
}
