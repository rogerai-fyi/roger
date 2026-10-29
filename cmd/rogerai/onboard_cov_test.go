package main

import "testing"

// TestWizardNonInteractive covers the onboarding wizard's non-interactive branches
// (go test has no TTY, so interactive() is false): no welcome card, runWizard returns
// keep-as-is, cmdOnboard saves, and the guided-upstream / key prompts bail cleanly.
func TestWizardNonInteractive(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if interactive() {
		t.Skip("running attached to a TTY; the non-interactive branches aren't reachable")
	}

	// Non-interactive launch: no welcome card, config unchanged (launch never blocks).
	cfg := config{Broker: "https://b", User: "u"}
	if got, due := welcomeOnLaunch(cfg, false); due || got.Broker != "https://b" {
		t.Errorf("welcomeOnLaunch(non-interactive) = %+v due %v", got, due)
	}
	// Already onboarded: never the card, even on a terminal.
	if got, due := welcomeOnLaunch(config{Onboarded: true, Broker: "x"}, true); due || got.Broker != "x" || !got.WelcomeSeen {
		t.Errorf("welcomeOnLaunch(onboarded) = %+v due %v", got, due)
	}

	// runWizard with no force + non-interactive -> keep-as-is (ran=false, no error).
	if up, ran, err := runWizard(cfg, wizardOpts{}); err != nil || ran || up.Broker != "https://b" {
		t.Errorf("runWizard(non-interactive) = %+v / ran=%v / %v, want keep-as-is", up, ran, err)
	}

	// The guided fallbacks bail without a TTY (no prompt, no panic).
	if _, ok := guidedUpstream("https://b", nil); ok {
		t.Error("guidedUpstream(non-interactive) should return ok=false")
	}
	if _, ok := guidedUpstream("https://b", []string{"http://127.0.0.1:9/v1"}); ok {
		t.Error("guidedUpstream(non-interactive, needKey) should return ok=false")
	}
	if _, ok := promptUpstreamKey(nil); ok {
		t.Error("promptUpstreamKey(non-interactive) should return ok=false")
	}

	// cmdOnboard (keep path) saves + returns nil.
	if err := cmdOnboard(cfg, nil); err != nil {
		t.Errorf("cmdOnboard(keep) = %v, want nil", err)
	}
	// (runWizard's --free detect path is covered deterministically in
	// TestRunWizardForcePaths, which stubs detectFull and asserts the result.)
}
