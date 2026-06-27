package main

import "testing"

// TestCmdAppeal covers the appeal command: login gate, the required --reason, and a
// successful file against a fake broker.
func TestCmdAppeal(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: fakeBroker(t), User: "u"}

	// Not logged in -> error (file path needs an account).
	if err := cmdAppeal(cfg, []string{"--reason", "mistake"}); err == nil {
		t.Error("cmdAppeal without login should error")
	}

	writeAuth(t) // now logged in
	// Missing --reason -> error.
	if err := cmdAppeal(cfg, nil); err == nil {
		t.Error("cmdAppeal without --reason should error")
	}
	// Valid file -> nil.
	if err := cmdAppeal(cfg, []string{"--node", "n1", "--reason", "false positive"}); err != nil {
		t.Errorf("cmdAppeal(file) = %v, want nil", err)
	}
}

// TestCmdAccountSubcommands covers cmdAccount's whoami/logout dispatch + the unknown
// subcommand error.
func TestCmdAccountSubcommands(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: "https://b", User: "u"}

	if err := cmdAccount(cfg, []string{"whoami"}); err != nil {
		t.Errorf("account whoami = %v", err)
	}
	if err := cmdAccount(cfg, []string{"show"}); err != nil {
		t.Errorf("account show = %v", err)
	}
	if err := cmdAccount(cfg, []string{"logout"}); err != nil {
		t.Errorf("account logout = %v", err)
	}
	if err := cmdAccount(cfg, []string{"bogus"}); err == nil {
		t.Error("account bogus should error with usage")
	}
}
