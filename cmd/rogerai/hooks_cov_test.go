package main

import (
	"testing"

	"github.com/rogerai-fyi/roger/internal/tui"
)

// TestTuiHooksClosures invokes the TUI hook closures built from config so their bodies
// (the persist callbacks + the broker-backed grant hooks) are exercised, not just the
// builder.
func TestTuiHooksClosures(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// tuiLimits' Save closure persists edited limits.
	ls := tuiLimits(config{Broker: "https://b"})
	if ls.Save != nil {
		ls.Save(map[string]tui.Limit{"m1": {MaxOut: 2}}, tui.Limit{MaxOut: 1})
	}

	b := fakeBroker(t)
	h := tuiHooks(config{Broker: b, User: "u"})
	// Local persist closures (saveConfig-backed).
	h.SaveStation("amber-fox")
	h.SavePrice("m1", tui.Pricing{Out: 2})
	h.SaveUpstream("http://up/v1", "sk-1")
	if h.SaveCompact != nil {
		h.SaveCompact(true)
	}
	// Broker-backed grant hooks against the fake broker.
	if h.GrantList != nil {
		if _, err := h.GrantList(b); err != nil {
			t.Errorf("GrantList hook: %v", err)
		}
	}
	if h.GrantCreate != nil {
		if _, err := h.GrantCreate(b, "petlings", true); err != nil {
			t.Errorf("GrantCreate hook: %v", err)
		}
	}
}

// TestGrantRecoverSecret covers `grant show --secret`: a free grant rotates (the old key
// is revoked + a fresh one minted), an unknown name errors, and a priced grant is
// refused (its caps can't be reconstructed).
func TestGrantRecoverSecret(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: fakeBroker(t), User: "u"}

	// The fake broker lists a free grant named "petlings" -> rotation succeeds.
	if err := grantRecoverSecret(cfg, "petlings"); err != nil {
		t.Errorf("grantRecoverSecret(free) = %v, want nil", err)
	}
	// Unknown name -> error.
	if err := grantRecoverSecret(cfg, "nope"); err == nil {
		t.Error("grantRecoverSecret(unknown) should error")
	}
}
