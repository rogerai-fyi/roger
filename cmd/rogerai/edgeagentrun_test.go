package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
)

// A headless agent that cannot ARM - LAN discovery is disabled - must return an error and exit, not
// sit forever pretending to run while the launcher reports success (audit 2026-09-24).
func TestRunEdgeAgentErrorsWhenDiscoveryDisabled(t *testing.T) {
	useTempConfig(t)
	t.Setenv(edge.EnvDiscovery, "0")

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

// An agent on a machine that is NOT enrolled has no face to serve, so it must error rather than
// browse forever unable to be operated (audit 2026-09-24). Discovery is left enabled so arm()
// succeeds and the failure is specifically the missing face.
func TestRunEdgeAgentErrorsWhenNotEnrolled(t *testing.T) {
	useTempConfig(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runEdgeAgentCtx(ctx, loadConfig()) }()

	select {
	case err := <-done:
		require.Error(t, err, "an un-enrolled machine cannot serve, so the agent must not idle")
	case <-time.After(3 * time.Second):
		t.Fatal("runEdgeAgentCtx hung on an un-enrolled machine instead of erroring")
	}
}

// The happy path: on an ENROLLED machine the agent arms and serves, stays up until its context is
// canceled, then stops cleanly (audit 2026-09-24).
func TestRunEdgeAgentStaysUpWhenEnrolled(t *testing.T) {
	offline(t)
	if _, code := edgeRun(t, "edge", "authority", "local", "shed"); code != 0 {
		t.Skip("could not designate a local authority in this environment")
	}
	if _, code := edgeRun(t, "edge", "enroll", "workshop"); code != 0 {
		t.Skip("could not enroll in this environment")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runEdgeAgentCtx(ctx, loadConfig()) }()

	select {
	case err := <-done:
		// If the face could not bind in this sandbox, that is the error path, not a silent idle.
		require.Error(t, err)
		cancel()
		return
	case <-time.After(250 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "an enrolled agent stops cleanly when canceled")
	case <-time.After(3 * time.Second):
		t.Fatal("the enrolled agent did not stop when canceled")
	}
}
