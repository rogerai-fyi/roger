package main

import (
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/agent"
)

// stubShareSeams points cmdShare's register+serve seams at a no-op stub session and a
// non-blocking shareBlock, restored on cleanup, so cmdShare's setup + go-live path runs
// to completion without registering with a broker or blocking forever.
func stubShareSeams(t *testing.T) {
	t.Helper()
	origStart, origBlock := agentStart, shareBlock
	agentStart = func(agent.Config) (*agent.Session, error) { return &agent.Session{}, nil }
	shareBlock = func() {} // do not block
	t.Cleanup(func() { agentStart, shareBlock = origStart, origBlock })
}

// TestCmdSharePrivate drives cmdShare's PRIVATE free-share path: a logged-in owner with
// an explicit --upstream (skips detection) reaches the band-code surface + go-live
// without registering or blocking. Covers the cfgRun build, the on-air lock, and the
// private serve tail.
func TestCmdSharePrivate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t) // logged in (private requires a GitHub-linked owner)
	stubShareSeams(t)

	cfg := config{Broker: "https://b", User: "u"}
	if err := cmdShare(cfg, []string{"m1", "--upstream", "http://127.0.0.1:1234/v1", "--private"}); err != nil {
		t.Fatalf("cmdShare(--private) = %v, want nil", err)
	}
}

// TestCmdShareFree drives the non-private FREE share path (no login needed): --upstream
// skips detection, the stub session goes "on air", and shareBlock returns. waitOnAir
// runs against the stub (briefly) - the test bounds it via the seam'd block returning.
func TestCmdShareFree(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubShareSeams(t)

	cfg := config{Broker: "https://b", User: "u"}
	done := make(chan error, 1)
	go func() { done <- cmdShare(cfg, []string{"m1", "--upstream", "http://127.0.0.1:1234/v1"}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cmdShare(free) = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cmdShare(free) did not return (waitOnAir/shareBlock seam?)")
	}
}
