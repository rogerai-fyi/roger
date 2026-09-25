package main

import (
	"os"
	"testing"
)

// The authority port must be STABLE across restarts: a device presents to the URL it cached at
// enrollment and does not re-resolve it, so an ephemeral port that changes every restart silently
// cuts off every enrolled phone. These lock in that the port is remembered and reused.

func TestAuthorityPortPersistsAndIsReused(t *testing.T) {
	useTempConfig(t)
	t.Setenv(envAuthorityBind, "") // no env pin, so the saved port is what matters

	// First run: nothing saved yet, so the bind is ephemeral.
	if got := edgeAuthorityBind(); got != ":0" {
		t.Fatalf("first bind = %q, want :0 (ephemeral until one is remembered)", got)
	}

	// After a bind, the actual port is remembered.
	edgeSaveAuthorityPort(44122)
	if p := edgeSavedAuthorityPort(); p != 44122 {
		t.Fatalf("saved port = %d, want 44122", p)
	}

	// The next restart reuses it - the phone's cached URL keeps working.
	if got := edgeAuthorityBind(); got != ":44122" {
		t.Fatalf("bind after save = %q, want :44122", got)
	}
}

func TestAuthorityBindEnvWins(t *testing.T) {
	useTempConfig(t)
	edgeSaveAuthorityPort(44122)
	t.Setenv(envAuthorityBind, ":51000")
	if got := edgeAuthorityBind(); got != ":51000" {
		t.Fatalf("bind = %q, want the env :51000 to win over the saved port", got)
	}
}

func TestSavedAuthorityPortIgnoresGarbage(t *testing.T) {
	useTempConfig(t)
	t.Setenv(envAuthorityBind, "")
	_ = os.MkdirAll(edgeAuthDir(), 0o755)
	_ = os.WriteFile(edgeAuthorityPortFile(), []byte("not-a-port"), 0o644)
	if p := edgeSavedAuthorityPort(); p != 0 {
		t.Fatalf("garbage port file = %d, want 0", p)
	}
	if got := edgeAuthorityBind(); got != ":0" {
		t.Fatalf("bind with garbage saved = %q, want :0", got)
	}
}
