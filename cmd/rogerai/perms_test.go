package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNormalizePerms locks the accepted spellings onto the canonical trio.
func TestNormalizePerms(t *testing.T) {
	for in, want := range map[string]string{
		"confirm": "confirm", "ask": "confirm", "default": "confirm",
		"edits": "edits", "auto-edits": "edits", "EDIT": "edits",
		"all": "all", "YOLO": "all", "bypass": "all",
	} {
		if got, ok := normalizePerms(in); !ok || got != want {
			t.Errorf("normalizePerms(%q) = %q,%v want %q,true", in, got, ok, want)
		}
	}
	if _, ok := normalizePerms("junk"); ok {
		t.Error("junk should not normalize")
	}
}

// TestStripPermsFlags covers the three flag shapes, last-wins, pass-through of other
// args, and the clear error on a bad value.
func TestStripPermsFlags(t *testing.T) {
	rest, mode, err := stripPermsFlags([]string{"--yolo"})
	if err != nil || mode != "all" || len(rest) != 0 {
		t.Fatalf("--yolo = %v/%q/%v", rest, mode, err)
	}
	rest, mode, err = stripPermsFlags([]string{"--perms", "edits", "search"})
	if err != nil || mode != "edits" || len(rest) != 1 || rest[0] != "search" {
		t.Fatalf("--perms edits search = %v/%q/%v", rest, mode, err)
	}
	rest, mode, err = stripPermsFlags([]string{"--perms=confirm", "--yolo"})
	if err != nil || mode != "all" || len(rest) != 0 {
		t.Fatalf("last flag should win, got %v/%q/%v", rest, mode, err)
	}
	if _, _, err = stripPermsFlags([]string{"--perms", "junk"}); err == nil {
		t.Error("bad --perms value should error")
	}
	rest, mode, err = stripPermsFlags([]string{"share", "gpt"})
	if err != nil || mode != "" || len(rest) != 2 {
		t.Fatalf("non-flag args must pass through, got %v/%q/%v", rest, mode, err)
	}
}

// TestApplyPermsDefault locks the precedence: flag > existing env > config.
func TestApplyPermsDefault(t *testing.T) {
	t.Setenv("ROGERAI_AGENT_PERMS", "")
	os.Unsetenv("ROGERAI_AGENT_PERMS")
	applyPermsDefault("", "edits") // config seeds an empty env
	if got := os.Getenv("ROGERAI_AGENT_PERMS"); got != "edits" {
		t.Fatalf("config seed = %q want edits", got)
	}
	applyPermsDefault("", "all") // an already-set env stands
	if got := os.Getenv("ROGERAI_AGENT_PERMS"); got != "edits" {
		t.Fatalf("existing env must stand, got %q", got)
	}
	applyPermsDefault("all", "confirm") // the flag wins over everything
	if got := os.Getenv("ROGERAI_AGENT_PERMS"); got != "all" {
		t.Fatalf("flag must win, got %q", got)
	}
}

// TestCmdPermsPersists: `roger perms edits` writes config.json; `confirm` clears the
// entry (the default needs no config); a bad mode errors.
func TestCmdPermsPersists(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	os.Unsetenv("ROGERAI_AGENT_PERMS")
	if err := cmdPerms(loadConfig(), []string{"edits"}); err != nil {
		t.Fatalf("perms edits: %v", err)
	}
	if got := loadConfig().AgentPerms; got != "edits" {
		t.Fatalf("persisted = %q want edits", got)
	}
	if _, err := os.Stat(filepath.Dir(configPath())); err != nil {
		t.Fatalf("config dir missing: %v", err)
	}
	if err := cmdPerms(loadConfig(), []string{"confirm"}); err != nil {
		t.Fatalf("perms confirm: %v", err)
	}
	if got := loadConfig().AgentPerms; got != "" {
		t.Fatalf("confirm should clear the entry, got %q", got)
	}
	if err := cmdPerms(loadConfig(), []string{"junk"}); err == nil {
		t.Error("bad mode should error")
	}
}

// `roger perms` is where an operator reads what will and will not ask before it runs. Its
// wording is a safety claim, so the modes and their descriptions are asserted rather than
// left to drift from what the loop actually gates.
func TestPermsReportsAndPersistsEachMode(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// The bare read names the effective default and where it came from.
	out := captureStdout(t, func() {
		if err := cmdPerms(config{}, nil); err != nil {
			t.Fatalf("perms: %v", err)
		}
	})
	if !strings.Contains(out, "web_fetch") || !strings.Contains(out, "run_shell") {
		t.Errorf("the default must name every gated tool:\n%s", out)
	}

	for _, tc := range []struct{ mode, want string }{
		{"edits", "run_shell still asks"},
		{"all", "nothing asks"},
		{"confirm", "ask y/N"},
	} {
		got := captureStdout(t, func() {
			if err := cmdPerms(config{}, []string{tc.mode}); err != nil {
				t.Fatalf("perms %s: %v", tc.mode, err)
			}
		})
		if !strings.Contains(got, tc.want) {
			t.Errorf("perms %s must say %q:\n%s", tc.mode, tc.want, got)
		}
	}

	// An unknown mode is refused rather than silently persisted - a typo'd mode that
	// quietly became "all" would remove every gate without saying so.
	if err := cmdPerms(config{}, []string{"yolol"}); err == nil {
		t.Error("an unknown perms mode was accepted")
	}
}
