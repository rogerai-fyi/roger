package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
)

// A spawned agent must run HEADLESS and STAY ALIVE (registering this machine's Edge) until stopped -
// not launch the TUI and die at once as a no-arg roger did (audit 2026-09-24).
func TestRunEdgeAgentStaysUpThenStopsCleanly(t *testing.T) {
	useTempConfig(t)
	t.Setenv(edge.EnvDiscovery, "0") // no real multicast sockets in the test

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runEdgeAgentCtx(ctx, loadConfig()) }()

	// It does NOT return on its own: it stays up.
	select {
	case <-done:
		t.Fatal("the headless agent exited immediately instead of staying up")
	case <-time.After(150 * time.Millisecond):
	}

	// It stops cleanly when its context is canceled (the signal in production).
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("the headless agent did not stop when signaled")
	}
}

// The spawn wiring launches the headless entrypoint, not a no-arg (TUI) roger.
func TestSpawnUsesTheHeadlessAgentArg(t *testing.T) {
	require.Equal(t, "__edge-agent", edgeAgentRunArg)
}
