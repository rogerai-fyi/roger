package main

import (
	"encoding/hex"
	"testing"
)

// TestEnvConfigGetters covers the env-backed config accessors: each returns the env
// value when set and the documented default (or empty) when not.
func TestEnvConfigGetters(t *testing.T) {
	// String getters with defaults.
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "shh")
	if githubSecret() != "shh" {
		t.Errorf("githubSecret = %q, want shh", githubSecret())
	}

	t.Setenv("GITHUB_OAUTH_REDIRECT", "")
	if got := webRedirectURI(); got != "https://rogerai.fyi/auth/github/callback" {
		t.Errorf("webRedirectURI default = %q", got)
	}
	t.Setenv("GITHUB_OAUTH_REDIRECT", "https://x/cb")
	if got := webRedirectURI(); got != "https://x/cb" {
		t.Errorf("webRedirectURI override = %q", got)
	}

	t.Setenv("ROGERAI_DASHBOARD_URL", "")
	if got := dashboardURL(); got != "https://rogerai.fyi/dashboard" {
		t.Errorf("dashboardURL default = %q", got)
	}
	t.Setenv("ROGERAI_LOGIN_URL", "https://login")
	if got := loginURL(); got != "https://login" {
		t.Errorf("loginURL override = %q", got)
	}

	if got := envOr("ROGERAI_DOES_NOT_EXIST_xyz", "fallback"); got != "fallback" {
		t.Errorf("envOr default = %q", got)
	}
}

// TestIntEnvGetters covers the integer env getters: a valid value wins, an invalid or
// unset value falls back to the constant default, and the validation bounds hold.
func TestIntEnvGetters(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		fn      func() int
		set     string
		want    int
		def     int
		badKeep bool // when set to garbage, expect the default
	}{
		{"recountHoldDays", "ROGERAI_RECOUNT_HOLD_DAYS", recountHoldDays, "14", 14, defaultRecountHoldDays, true},
		{"reportEjectThreshold", "ROGERAI_REPORT_EJECT_AT", reportEjectThreshold, "9", 9, defaultReportEjectAt, true},
		{"reportDecayDays", "ROGERAI_REPORT_DECAY_DAYS", reportDecayDays, "45", 45, defaultReportDecayDays, true},
		{"nodeBanDays", "ROGERAI_NODE_BAN_DAYS", nodeBanDays, "5", 5, defaultNodeBanDays, true},
		{"strikeDecayDays", "ROGERAI_STRIKE_DECAY_DAYS", strikeDecayDays, "21", 21, defaultStrikeDecayDays, true},
		{"strikeCorroborateKinds", "ROGERAI_STRIKE_CORROBORATE_KINDS", strikeCorroborateKinds, "3", 3, defaultStrikeCorroborateKinds, true},
		{"maxNodesPerOwnerLimit", "ROGERAI_MAX_NODES_PER_OWNER", maxNodesPerOwnerLimit, "8", 8, defaultMaxNodesPerOwner, true},
		{"freeRegPerIPLimit", "ROGERAI_FREE_REG_PER_IP", freeRegPerIPLimit, "2", 2, defaultFreeRegPerIP, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(c.env, c.set)
			if got := c.fn(); got != c.want {
				t.Errorf("%s(%q) = %d, want %d", c.name, c.set, got, c.want)
			}
			t.Setenv(c.env, "not-a-number")
			if got := c.fn(); got != c.def {
				t.Errorf("%s(garbage) = %d, want default %d", c.name, got, c.def)
			}
		})
	}
}

// TestFreeRegWindowDur covers the per-IP free-registration window: a positive seconds
// value wins, garbage/non-positive falls back to the default.
func TestFreeRegWindowDur(t *testing.T) {
	t.Setenv("ROGERAI_FREE_REG_WINDOW_SEC", "120")
	if got := freeRegWindowDur(); got != 120*1e9 {
		t.Errorf("freeRegWindowDur(120) = %v, want 2m", got)
	}
	t.Setenv("ROGERAI_FREE_REG_WINDOW_SEC", "x")
	if got := freeRegWindowDur(); got != defaultFreeRegWindow {
		t.Errorf("freeRegWindowDur(garbage) = %v, want default", got)
	}
}

// TestAdminGitHubID covers the admin-id parse: a positive int wins, garbage/unset -> 0.
func TestAdminGitHubID(t *testing.T) {
	t.Setenv("ADMIN_GITHUB_ID", "12345")
	if adminGitHubID() != 12345 {
		t.Errorf("adminGitHubID = %d, want 12345", adminGitHubID())
	}
	t.Setenv("ADMIN_GITHUB_ID", "nope")
	if adminGitHubID() != 0 {
		t.Errorf("adminGitHubID(garbage) = %d, want 0", adminGitHubID())
	}
	t.Setenv("ADMIN_GITHUB_ID", "-3")
	if adminGitHubID() != 0 {
		t.Errorf("adminGitHubID(negative) = %d, want 0", adminGitHubID())
	}
}

// TestMultiInstanceEnabled covers the truthy-string parse for the multi-instance flag.
func TestMultiInstanceEnabled(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv("ROGERAI_MULTI_INSTANCE", v)
		if !multiInstanceEnabled() {
			t.Errorf("multiInstanceEnabled(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "0", "off", "nope"} {
		t.Setenv("ROGERAI_MULTI_INSTANCE", v)
		if multiInstanceEnabled() {
			t.Errorf("multiInstanceEnabled(%q) = true, want false", v)
		}
	}
}

// TestValidAdminKey covers the admin-key gate: only a real 32-byte ed25519 seed hex is
// accepted; empty and malformed inputs return "" (admin surface stays closed).
func TestValidAdminKey(t *testing.T) {
	seed := hex.EncodeToString(make([]byte, 32)) // ed25519.SeedSize
	if validAdminKey(seed) != seed {
		t.Errorf("a 32-byte seed hex should be accepted")
	}
	for _, bad := range []string{"", "zz", hex.EncodeToString(make([]byte, 8))} {
		if validAdminKey(bad) != "" {
			t.Errorf("validAdminKey(%q) should be rejected (empty)", bad)
		}
	}
}

// TestSmallHelpers covers firstNonEmpty, sortedKeys, failMode, randState, and
// loadCSAMCategories (env override + default fallback).
func TestSmallHelpers(t *testing.T) {
	if firstNonEmpty("", "  ", "x", "y") != "x" {
		t.Errorf("firstNonEmpty should return the first non-blank")
	}
	if firstNonEmpty("", "   ") != "" {
		t.Errorf("firstNonEmpty(all blank) should be empty")
	}

	ks := sortedKeys(map[string]bool{"b": true, "a": true, "c": true})
	if len(ks) != 3 || ks[0] != "a" || ks[2] != "c" {
		t.Errorf("sortedKeys = %v, want [a b c]", ks)
	}

	if failMode(true) != "CLOSED (rejected)" || failMode(false) != "OPEN (served)" {
		t.Errorf("failMode strings wrong")
	}

	if s := randState(); len(s) != 32 { // 16 bytes hex
		t.Errorf("randState len = %d, want 32 hex chars", len(s))
	}
	if randState() == randState() {
		t.Errorf("randState should be random")
	}

	// loadCSAMCategories: env override is parsed + case-folded; empty falls back to default.
	cats := loadCSAMCategories("S4, Csae ,, ")
	if !cats["s4"] || !cats["csae"] {
		t.Errorf("loadCSAMCategories env override = %v", cats)
	}
	if def := loadCSAMCategories(""); len(def) == 0 {
		t.Errorf("loadCSAMCategories(empty) should fall back to defaults")
	}
}
