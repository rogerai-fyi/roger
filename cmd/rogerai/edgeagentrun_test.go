package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
)

// The spawn wiring launches the headless entrypoint, not a no-arg (TUI) roger.
func TestSpawnUsesTheHeadlessAgentArg(t *testing.T) {
	require.Equal(t, "__edge-agent", edgeAgentRunArg)
}

// A headless agent that cannot ARM - discovery disabled, or its network cannot be bound - must
// return an error and exit, not sit forever pretending to run (audit 2026-09-24).
func TestRunEdgeAgentErrorsWhenItCannotArm(t *testing.T) {
	useTempConfig(t)
	t.Setenv(edge.EnvDiscovery, "0") // nothing to serve or discover

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runEdgeAgentCtx(ctx, loadConfig()) }()

	select {
	case err := <-done:
		require.Error(t, err, "an agent that arms nothing must not run silently")
	case <-time.After(2 * time.Second):
		t.Fatal("runEdgeAgentCtx hung instead of erroring when it could not arm")
	}
}

// When it DOES arm, it stays up until its context is canceled, then stops cleanly.
func TestRunEdgeAgentStaysUpThenStopsCleanly(t *testing.T) {
	useTempConfig(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runEdgeAgentCtx(ctx, loadConfig()) }()

	select {
	case err := <-done:
		// Some CI hosts have no multicast-capable interface, so arming legitimately fails there; that
		// path is the error case above. Only a nil (armed-and-exited-early) return is wrong here.
		require.Error(t, err, "if it exited on its own it must be because it could not arm")
		cancel()
		return
	case <-time.After(200 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "an armed agent stops cleanly when canceled")
	case <-time.After(3 * time.Second):
		t.Fatal("the headless agent did not stop when canceled")
	}
}
