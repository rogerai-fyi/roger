package edge

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// An agent's ASSIGNMENT (features/edge/jobs.feature) is a durable record: the model bound to it,
// the use/share posture on that model, and the job it runs with its targets. It follows the
// persistent-agent pattern (one small honest file per agent), so these pin the same guarantees:
// set is idempotent, read is non-nil-even-empty and sorted, clear reports whether it was there.

func TestAgentJobSetAndRead(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1000, 0)

	// serve job: a model bound and shared to the Edge.
	require.NoError(t, SetAgentJob(dir, AgentJob{
		Name: "worker-1", Node: "n_a", Model: "qwen3-30b", Share: true, Job: "serve",
	}, now))
	// use-only binding: a model the agent uses for itself, not shared.
	require.NoError(t, SetAgentJob(dir, AgentJob{
		Name: "use-1", Node: "n_a", Model: "pico", Use: true,
	}, now))
	// watch job with targets, no model.
	require.NoError(t, SetAgentJob(dir, AgentJob{
		Name: "sentry", Node: "n_a", Job: "watch", Targets: []string{"greenhouse", "pump"},
	}, now))

	got := AgentJobs(dir)
	require.NotNil(t, got)
	require.Len(t, got, 3)
	// Sorted by name: sentry, use-1, worker-1.
	require.Equal(t, "sentry", got[0].Name)
	require.Equal(t, "use-1", got[1].Name)
	require.Equal(t, "worker-1", got[2].Name)

	j, ok := AgentJobOf(dir, "worker-1")
	require.True(t, ok)
	require.Equal(t, "qwen3-30b", j.Model)
	require.True(t, j.Share)
	require.False(t, j.Use)
	require.Equal(t, "serve", j.Job)
	require.Equal(t, now.Unix(), j.SetAt)

	sentry, ok := AgentJobOf(dir, "sentry")
	require.True(t, ok)
	require.Equal(t, []string{"greenhouse", "pump"}, sentry.Targets)
	require.Empty(t, sentry.Model)

	_, ok = AgentJobOf(dir, "ghost")
	require.False(t, ok)
}

func TestAgentJobSetIsIdempotentAndReplaces(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1000, 0)
	require.NoError(t, SetAgentJob(dir, AgentJob{Name: "a", Node: "n", Model: "pico", Use: true}, now))
	// Re-set the same agent with a new posture: it replaces, not appends.
	require.NoError(t, SetAgentJob(dir, AgentJob{Name: "a", Node: "n", Model: "qwen3-30b", Share: true, Job: "serve"}, now))
	require.Len(t, AgentJobs(dir), 1)
	j, ok := AgentJobOf(dir, "a")
	require.True(t, ok)
	require.Equal(t, "qwen3-30b", j.Model)
	require.True(t, j.Share)
	require.False(t, j.Use)
}

func TestAgentJobClear(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1000, 0)
	require.NoError(t, SetAgentJob(dir, AgentJob{Name: "a", Node: "n", Model: "pico", Use: true}, now))
	had, err := ClearAgentJob(dir, "a")
	require.NoError(t, err)
	require.True(t, had)
	require.Empty(t, AgentJobs(dir))
	// Clearing a missing one is not an error and reports it was not there.
	had, err = ClearAgentJob(dir, "a")
	require.NoError(t, err)
	require.False(t, had)
}

func TestAgentJobEmptyNameNoOp(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, SetAgentJob(dir, AgentJob{Name: "  ", Model: "pico"}, time.Unix(1000, 0)))
	require.Empty(t, AgentJobs(dir))
}

// Posture helpers make the detail view honest: a serve job shares, a use posture uses, a bound
// model with neither is "assigned but idle", no model is "none".
func TestAgentJobPosture(t *testing.T) {
	require.Equal(t, "qwen3-30b", AgentJob{Model: "qwen3-30b", Share: true}.SharesModel())
	require.Equal(t, "", AgentJob{Model: "pico", Use: true}.SharesModel())
	require.Equal(t, "pico", AgentJob{Model: "pico", Use: true}.UsesModel())
	require.Equal(t, "", AgentJob{Model: "qwen3-30b", Share: true}.UsesModel())
	require.True(t, AgentJob{}.IsIdle())
	require.False(t, AgentJob{Job: "serve"}.IsIdle())
	require.False(t, AgentJob{Model: "pico", Use: true}.IsIdle())
}
