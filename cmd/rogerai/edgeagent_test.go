package main

// Tests for declaring a roger an agent on its Edge (features/edge/agents.feature): the config
// role, the ROGER_EDGE_AGENT env (which wins per-run), and the `roger edge agent` command.

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
)

func TestEdgeIsAgentRole(t *testing.T) {
	// neither env nor config: not an agent.
	require.False(t, edgeIsAgentRole(config{}))
	// config role on: an agent.
	require.True(t, edgeIsAgentRole(config{EdgeAgent: true}))
	// env wins per-run, even over an off config role.
	t.Setenv("ROGER_EDGE_AGENT", "1")
	require.True(t, edgeIsAgentRole(config{}))
	require.True(t, edgeIsAgentRole(config{EdgeAgent: false}))
	t.Setenv("ROGER_EDGE_AGENT", "off")
	require.False(t, edgeIsAgentRole(config{EdgeAgent: true}), "env off overrides config on")
}

func TestCmdEdgeAgentOnOff(t *testing.T) {
	useTempConfig(t)
	out, err := captureEdgeStdout(func() error { return cmdEdgeAgent(loadConfig(), []string{"on"}) })
	require.NoError(t, err)
	require.Contains(t, out, "now a persistent agent")
	require.True(t, loadConfig().EdgeAgent, "on persists the config role")

	out, err = captureEdgeStdout(func() error { return cmdEdgeAgent(loadConfig(), []string{"off"}) })
	require.NoError(t, err)
	require.Contains(t, out, "no longer an agent")
	require.False(t, loadConfig().EdgeAgent)
}

func TestCmdEdgeAgentShow(t *testing.T) {
	useTempConfig(t)
	out, err := captureEdgeStdout(func() error { return cmdEdgeAgent(config{}, nil) })
	require.NoError(t, err)
	require.Contains(t, out, "not an agent")

	out, err = captureEdgeStdout(func() error { return cmdEdgeAgent(config{EdgeAgent: true}, nil) })
	require.NoError(t, err)
	require.Contains(t, out, "is an agent")
}

func TestCmdEdgeAgentBadArg(t *testing.T) {
	useTempConfig(t)
	_, err := captureEdgeStdout(func() error { return cmdEdgeAgent(loadConfig(), []string{"maybe"}) })
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "none of them")
}

func TestEdgeAgentPersistLifecycle(t *testing.T) {
	useTempConfig(t)
	// on marks a durable persistent record for this roger's agent name.
	_, err := captureEdgeStdout(func() error { return cmdEdgeAgent(loadConfig(), []string{"on"}) })
	require.NoError(t, err)
	pers := edge.PersistentAgents(edgePersistDir())
	require.Len(t, pers, 1, "on writes one durable persistent-agent record")
	name := pers[0].Name
	require.True(t, edge.IsPersistent(edgePersistDir(), name))

	// resume of a not-running persistent agent launches a plain roger with the role + name.
	prev := edgeSpawnRoger
	t.Cleanup(func() { edgeSpawnRoger = prev })
	var gotEnv map[string]string
	edgeSpawnRoger = func(env map[string]string) error { gotEnv = env; return nil }
	out, err := captureEdgeStdout(func() error { return cmdEdgeAgent(loadConfig(), []string{"resume", name}) })
	require.NoError(t, err)
	require.Contains(t, out, "launched a roger")
	require.Equal(t, "1", gotEnv["ROGER_EDGE_AGENT"])
	require.Equal(t, name, gotEnv["ROGER_EDGE_INSTANCE"])

	// resume of an unknown agent launches nothing and says so.
	gotEnv = nil
	_, err = captureEdgeStdout(func() error { return cmdEdgeAgent(loadConfig(), []string{"resume", "ghost"}) })
	require.Error(t, err)
	require.Nil(t, gotEnv, "no launch for a non-existent persistent agent")

	// remove drops the durable record; a second remove is a clean no-op.
	out, err = captureEdgeStdout(func() error { return cmdEdgeAgent(loadConfig(), []string{"remove", name}) })
	require.NoError(t, err)
	require.Contains(t, out, "removed")
	require.False(t, edge.IsPersistent(edgePersistDir(), name))
	out, err = captureEdgeStdout(func() error { return cmdEdgeAgent(loadConfig(), []string{"remove", name}) })
	require.NoError(t, err)
	require.Contains(t, out, "nothing to remove")
}

func TestEdgeAgentOffUnmarks(t *testing.T) {
	useTempConfig(t)
	_, _ = captureEdgeStdout(func() error { return cmdEdgeAgent(loadConfig(), []string{"on"}) })
	require.NotEmpty(t, edge.PersistentAgents(edgePersistDir()))
	_, err := captureEdgeStdout(func() error { return cmdEdgeAgent(loadConfig(), []string{"off"}) })
	require.NoError(t, err)
	require.Empty(t, edge.PersistentAgents(edgePersistDir()), "off unmarks the persistent record")
	require.False(t, loadConfig().EdgeAgent)
}

func TestEdgeAgentResumeAlreadyRunning(t *testing.T) {
	useTempConfig(t)
	// Enroll a fake identity so Register works, and make "scout" both persistent and running.
	_, err := edgeDesignateCore(edgeAuthDir(), "desk")
	require.NoError(t, err)
	_, err = edgeEnrollCore(loadConfig(), "desk", "", "")
	require.NoError(t, err)
	now := timeNowForTest()
	require.NoError(t, edge.MarkPersistent(edgePersistDir(), edgeThisNodeID(), "scout", now))
	_, err = edge.Register(edgeInstancesDir(), edgeThisNodeID(), edge.Instance{Name: "scout", Agent: true}, now)
	require.NoError(t, err)

	prev := edgeSpawnRoger
	t.Cleanup(func() { edgeSpawnRoger = prev })
	launched := false
	edgeSpawnRoger = func(map[string]string) error { launched = true; return nil }

	out, err := captureEdgeStdout(func() error { return cmdEdgeAgent(loadConfig(), []string{"resume", "scout"}) })
	require.NoError(t, err)
	require.Contains(t, out, "already running")
	require.False(t, launched, "a running agent is not relaunched")
}

func TestEdgeAgentShowListsPersistent(t *testing.T) {
	useTempConfig(t)
	_, err := edgeDesignateCore(edgeAuthDir(), "desk")
	require.NoError(t, err)
	_, err = edgeEnrollCore(loadConfig(), "desk", "", "")
	require.NoError(t, err)
	now := timeNowForTest()
	require.NoError(t, edge.MarkPersistent(edgePersistDir(), edgeThisNodeID(), "alice", now)) // will be running
	require.NoError(t, edge.MarkPersistent(edgePersistDir(), edgeThisNodeID(), "scout", now)) // not running
	_, err = edge.Register(edgeInstancesDir(), edgeThisNodeID(), edge.Instance{Name: "alice", Agent: true}, now)
	require.NoError(t, err)

	out, err := captureEdgeStdout(func() error { return cmdEdgeAgent(loadConfig(), nil) })
	require.NoError(t, err)
	require.Contains(t, out, "persistent agents")
	require.Contains(t, out, "alice")
	require.Contains(t, out, "running")
	require.Contains(t, out, "resume scout") // the not-running one shows its resume command
}

func timeNowForTest() time.Time { return time.Unix(100000, 0) }
