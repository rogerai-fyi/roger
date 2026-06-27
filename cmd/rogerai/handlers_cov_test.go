package main

import (
	"os"
	"testing"
	"time"
)

// TestMainVersion covers main()'s happy path: argv is a known subcommand, run() returns
// nil, and main() returns without os.Exit. (The error->os.Exit branch can't be exercised
// in-process without terminating the test binary.)
func TestMainVersion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	origArgs := os.Args
	os.Args = []string{"roger", "version"}
	t.Cleanup(func() { os.Args = origArgs })
	main() // prints "rogerai <ver>" and returns
}

// TestCmdShareFlagBranches covers cmdShare's post-detection flag handling on the explicit
// --upstream path: a bad --free-window and a bad --schedule error out; --private without a
// login errors; and a valid --confidential + --free-window + --schedule combo reaches
// go-live (async) through the stubbed seams.
func TestCmdShareFlagBranches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubShareSeams(t)
	const up = "http://127.0.0.1:1234/v1"
	cfg := config{Broker: "https://b", User: "u"}

	if err := cmdShare(cfg, []string{"m1", "--upstream", up, "--free-window", "bad"}); err == nil {
		t.Error("cmdShare bad --free-window should error")
	}
	if err := cmdShare(cfg, []string{"m1", "--upstream", up, "--schedule", "{not json}"}); err == nil {
		t.Error("cmdShare bad --schedule should error")
	}
	// --private with no auth.json -> fail-fast login requirement.
	if err := cmdShare(cfg, []string{"m1", "--upstream", up, "--private"}); err == nil {
		t.Error("cmdShare --private without login should error")
	}

	// Valid confidential + schedule combo reaches go-live.
	done := make(chan error, 1)
	go func() {
		done <- cmdShare(cfg, []string{
			"m1", "--upstream", up, "--advanced", "--confidential",
			"--free-window", "03:00-03:30",
			"--schedule", `[{"start":"18:00","end":"22:00","price_out":0.5}]`,
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cmdShare(confidential+schedule) = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cmdShare(confidential+schedule) did not return")
	}
}

// TestCmdPayoutSubBranches covers cmdPayout's sub-dispatch: help (no login needed), the
// not-logged-in gate, and the unknown-subcommand fallthrough.
func TestCmdPayoutSubBranches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: "https://b", User: "u"}

	// Help works without login.
	if err := cmdPayout(cfg, []string{"help"}); err != nil {
		t.Errorf("cmdPayout help = %v", err)
	}
	// No auth.json -> the login gate (friendly message, nil).
	if err := cmdPayout(cfg, []string{"status"}); err != nil {
		t.Errorf("cmdPayout(not logged in) = %v, want nil", err)
	}

	// With a login, an unknown subcommand falls through to usage (nil).
	writeAuth(t)
	if err := cmdPayout(cfg, []string{"bogus"}); err != nil {
		t.Errorf("cmdPayout(unknown) = %v, want nil", err)
	}
}

// TestCmdBalanceTopupAliasRuns covers cmdBalance's hidden topup-alias branch (the pure
// parser is unit-tested elsewhere; this drives the broker-facing alias path).
func TestCmdBalanceTopupAliasRuns(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: fakeBroker(t), User: "u_gh_1"}
	if err := cmdBalance(cfg, []string{"topup", "15"}); err != nil {
		t.Errorf("cmdBalance(topup alias) = %v", err)
	}
}

// TestCmdLimitClearAndSet covers cmdLimit's clear (off) and set paths against a broker
// that reports NO cap, hitting the "cleared / none" rendering branches the active-cap
// fake broker doesn't reach.
func TestCmdLimitClearAndSet(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	noCap := config{Broker: brokerRouting(t, map[string]string{
		"/account/limit": `{"monthly_cap":0,"monthly_spend":7}`,
	}), User: "u"}
	if err := cmdLimit(noCap, nil); err != nil { // read-only, cap 0 -> "none" branch
		t.Errorf("cmdLimit(read, no cap) = %v", err)
	}
	if err := cmdLimit(noCap, []string{"--monthly", "off"}); err != nil { // clear -> "cleared" branch
		t.Errorf("cmdLimit(--monthly off) = %v", err)
	}
	if err := cmdLimit(noCap, []string{"--monthly", "bogus"}); err == nil {
		t.Error("cmdLimit(--monthly bogus) should error")
	}
}

// TestCmdAccountBranches covers cmdAccount's whoami/show + the unknown-subcommand error.
func TestCmdAccountBranches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: fakeBroker(t), User: "u_gh_1"}
	if err := cmdAccount(cfg, []string{"show"}); err != nil {
		t.Errorf("cmdAccount show = %v", err)
	}
	if err := cmdAccount(cfg, []string{"bogus"}); err == nil {
		t.Error("cmdAccount(unknown) should error")
	}
}

// TestCmdConfigErrorBranches covers cmdConfig's argument-validation error branches and the
// get-user path the existing happy-path test does not reach.
func TestCmdConfigErrorBranches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := cmdConfig([]string{"clear-limit"}); err == nil {
		t.Error("config clear-limit (no model) should error")
	}
	if err := cmdConfig([]string{"set"}); err == nil {
		t.Error("config set (too few args) should error")
	}
	if err := cmdConfig([]string{"set", "bogus", "v"}); err == nil {
		t.Error("config set (unknown key) should error")
	}
	if err := cmdConfig([]string{"get", "user"}); err != nil {
		t.Errorf("config get user = %v", err)
	}
}

// TestCmdSetLimitBranches covers cmdSetLimit's default-model path and its missing-arg
// usage error.
func TestCmdSetLimitBranches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := cmdSetLimit(nil); err == nil {
		t.Error("set-limit (no model) should error")
	}
	if err := cmdSetLimit([]string{"default", "--max-out", "0.4"}); err != nil {
		t.Fatalf("set-limit default = %v", err)
	}
	if loadConfig().Limits.Default.MaxOut != 0.4 {
		t.Errorf("set-limit default did not persist: %+v", loadConfig().Limits.Default)
	}
}

// TestCmdGrantBranches covers cmdGrant's argument-validation errors + the create
// missing-name error + the show --secret recovery path (rotate a lost free key).
func TestCmdGrantBranches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: fakeBroker(t), User: "u_gh_1"}

	if err := cmdGrant(cfg, []string{"revoke"}); err == nil {
		t.Error("grant revoke (no name) should error")
	}
	if err := cmdGrant(cfg, []string{"show"}); err == nil {
		t.Error("grant show (no name) should error")
	}
	if err := cmdGrant(cfg, []string{"bogus"}); err == nil {
		t.Error("grant (unknown) should error")
	}
	if err := cmdGrantCreate(cfg, []string{"--free"}); err == nil {
		t.Error("grant create (no name) should error")
	}
	// Advanced create with an expiry + caps -> the GrantCreate success path.
	if err := cmdGrantCreate(cfg, []string{"--name", "bots", "--advanced", "--expires", "30d", "--daily-cap", "1000000"}); err != nil {
		t.Errorf("grant create (advanced) = %v", err)
	}
	// show --secret on the fake broker's free "petlings" grant -> rotate + reprint.
	if err := cmdGrant(cfg, []string{"show", "--secret", "petlings"}); err != nil {
		t.Errorf("grant show --secret = %v", err)
	}
	// Recovering a non-existent grant errors.
	if err := grantRecoverSecret(cfg, "no-such-grant"); err == nil {
		t.Error("grantRecoverSecret(missing) should error")
	}
}

// TestCmdUseFlagBranches covers cmdUse's flag surface (--advanced, an explicit --port,
// --freq, caps) against an empty market (returns before the blocking relay), plus the
// missing-model usage error.
func TestCmdUseFlagBranches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := cmdUse(config{Broker: "https://b"}, nil); err == nil {
		t.Error("cmdUse (no model) should error")
	}
	noStation := config{Broker: fakeBrokerEmpty(t), User: "u"}
	if err := cmdUse(noStation, []string{"m1", "--advanced", "--port", "0", "--max-out", "0.3", "--max-in", "0.1", "--min-tps", "5"}); err != nil {
		t.Errorf("cmdUse(advanced flags) = %v", err)
	}
	if err := cmdUse(noStation, []string{"m1", "--freq", "147.520 MHz 8F3K-9M2Q"}); err != nil {
		t.Errorf("cmdUse(--freq) = %v", err)
	}
}

// TestDispatchMoreVerbs covers the dispatch routes the existing routing test skips: the
// drphil/doctor alias, support aliases, and account/identity.
func TestDispatchMoreVerbs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: fakeBroker(t), User: "u_gh_1"}
	for _, args := range [][]string{
		{"doctor"}, {"diagnose"}, {"identity"}, {"community"}, {"discover"}, {"models"}, {"cashout", "status"},
	} {
		if err := dispatch(cfg, args); err != nil {
			t.Errorf("dispatch(%v) = %v, want nil", args, err)
		}
	}
}
