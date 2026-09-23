package main

// Tests for binding a model and a job to an agent (features/edge/jobs.feature): the `roger edge
// model` and `roger edge job` commands, and their durable records. Lightweight - no testcontainers.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
)

func TestCmdEdgeModelServeShares(t *testing.T) {
	useTempConfig(t)
	out, err := captureEdgeStdout(func() error {
		return cmdEdgeModel(loadConfig(), []string{"worker-1", "qwen3-30b", "share"})
	})
	require.NoError(t, err)
	require.Contains(t, out, "worker-1")
	require.Contains(t, out, "qwen3-30b")

	j, ok := edge.AgentJobOf(edgeJobsDir(), "worker-1")
	require.True(t, ok)
	require.Equal(t, "qwen3-30b", j.Model)
	require.True(t, j.Share)
	require.False(t, j.Use)
}

func TestCmdEdgeModelUseOnly(t *testing.T) {
	useTempConfig(t)
	_, err := captureEdgeStdout(func() error {
		return cmdEdgeModel(loadConfig(), []string{"use-1", "pico", "use"})
	})
	require.NoError(t, err)
	j, ok := edge.AgentJobOf(edgeJobsDir(), "use-1")
	require.True(t, ok)
	require.True(t, j.Use)
	require.False(t, j.Share)
}

func TestCmdEdgeModelUseAndShare(t *testing.T) {
	useTempConfig(t)
	_, err := captureEdgeStdout(func() error {
		return cmdEdgeModel(loadConfig(), []string{"both-1", "qwen3-30b", "use,share"})
	})
	require.NoError(t, err)
	j, _ := edge.AgentJobOf(edgeJobsDir(), "both-1")
	require.True(t, j.Use)
	require.True(t, j.Share)
}

func TestCmdEdgeModelDefaultsToShare(t *testing.T) {
	useTempConfig(t)
	// No posture given: the common path is to put the model on air (share).
	_, err := captureEdgeStdout(func() error {
		return cmdEdgeModel(loadConfig(), []string{"w", "qwen3-30b"})
	})
	require.NoError(t, err)
	j, _ := edge.AgentJobOf(edgeJobsDir(), "w")
	require.True(t, j.Share)
}

func TestCmdEdgeModelBadPosture(t *testing.T) {
	useTempConfig(t)
	_, err := captureEdgeStdout(func() error {
		return cmdEdgeModel(loadConfig(), []string{"w", "qwen3-30b", "borrow"})
	})
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "use")
}

func TestCmdEdgeJobServe(t *testing.T) {
	useTempConfig(t)
	// A model bound first, then a serve job over it.
	_, err := captureEdgeStdout(func() error { return cmdEdgeModel(loadConfig(), []string{"w", "qwen3-30b", "share"}) })
	require.NoError(t, err)
	out, err := captureEdgeStdout(func() error { return cmdEdgeJob(loadConfig(), []string{"w", "serve"}) })
	require.NoError(t, err)
	require.Contains(t, out, "serve")
	j, _ := edge.AgentJobOf(edgeJobsDir(), "w")
	require.Equal(t, "serve", j.Job)
	require.Equal(t, "qwen3-30b", j.Model, "job serve keeps the bound model")
}

func TestCmdEdgeJobWatchTargets(t *testing.T) {
	useTempConfig(t)
	out, err := captureEdgeStdout(func() error {
		return cmdEdgeJob(loadConfig(), []string{"sentry", "watch", "greenhouse", "pump"})
	})
	require.NoError(t, err)
	require.Contains(t, out, "watch")
	j, _ := edge.AgentJobOf(edgeJobsDir(), "sentry")
	require.Equal(t, "watch", j.Job)
	require.Equal(t, []string{"greenhouse", "pump"}, j.Targets)
}

func TestCmdEdgeJobNoneClears(t *testing.T) {
	useTempConfig(t)
	_, err := captureEdgeStdout(func() error { return cmdEdgeJob(loadConfig(), []string{"w", "serve"}) })
	require.NoError(t, err)
	out, err := captureEdgeStdout(func() error { return cmdEdgeJob(loadConfig(), []string{"w", "none"}) })
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(out), "no job")
	_, ok := edge.AgentJobOf(edgeJobsDir(), "w")
	require.False(t, ok, "none clears the assignment")
}

func TestCmdEdgeJobBadName(t *testing.T) {
	useTempConfig(t)
	_, err := captureEdgeStdout(func() error { return cmdEdgeJob(loadConfig(), []string{"w", "juggle"}) })
	require.Error(t, err)
}

func TestCmdEdgeModelShowsWhenNoArgs(t *testing.T) {
	useTempConfig(t)
	_, err := captureEdgeStdout(func() error { return cmdEdgeModel(loadConfig(), nil) })
	require.Error(t, err) // model needs at least an agent and a model
}
