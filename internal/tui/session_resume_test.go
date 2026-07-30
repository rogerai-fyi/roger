package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/capsule"
	"github.com/rogerai-fyi/roger/internal/session"
	"github.com/stretchr/testify/require"
)

func resumedFixture() session.Snapshot {
	model := "model-a"
	result := "old result"
	return session.Snapshot{
		Version: session.CurrentVersion, ID: "th_restore", Title: "Inspect the GPU",
		Workdir: "/work/repo", WorkdirAvailable: true,
		CreatedAt: time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 7, 29, 11, 0, 0, 0, time.UTC),
		Model:     model,
		Messages: []capsule.Message{
			{Role: "user", Content: "Inspect the GPU", XRoger: capsule.XRoger{Turn: 0, Agent: agentSurfaceUser}},
			{Role: "assistant", Content: "It is connected.", XRoger: capsule.XRoger{Turn: 1, Agent: agentSurfacePrefix + ":" + model},
				ToolCalls: capsule.ToolCallsRaw([]capsule.ToolCall{{ID: "call_1", Name: "read_file", Arguments: `{"path":"gpu.txt"}`, Result: &result}})},
		},
	}
}

func TestNewResumedModelRestoresSemanticTranscriptAndIdentity(t *testing.T) {
	item := resumedFixture()
	m, err := NewResumedWithHooksController("broker", "user", nil, Hooks{}, NewController("broker", Hooks{}), item)
	require.NoError(t, err)
	require.Equal(t, modeAgent, m.mode)
	require.Equal(t, item.ID, m.threadID)
	require.Equal(t, item.Workdir, m.sessionWorkdir)
	require.Equal(t, item.Messages, m.ring)
	require.Equal(t, 2, m.ringTurn)

	text := m.agentTranscriptText()
	require.Contains(t, text, "Inspect the GPU")
	require.Contains(t, text, "read_file")
	require.Contains(t, text, "historical")
	require.Contains(t, text, "It is connected.")
}

func TestResumedHarnessHistoryContainsOnlyCompletedUserAssistantText(t *testing.T) {
	got, err := restoredHarnessMessages(resumedFixture().Messages)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "user", got[0].Role)
	require.Equal(t, "Inspect the GPU", got[0].Content)
	require.Equal(t, "assistant", got[1].Role)
	require.Equal(t, "It is connected.", got[1].Content)
	require.Empty(t, got[1].ToolCalls, "historical tools are display-only and never replayed")
}

func TestCompletedSessionSnapshotUsesStableIDAndExcludesPendingTurn(t *testing.T) {
	item := resumedFixture()
	var saved session.Snapshot
	h := Hooks{SaveSession: func(s session.Snapshot) error {
		saved = s
		return nil
	}}
	m, err := NewResumedWithHooksController("broker", "user", nil, h, NewController("broker", h), item)
	require.NoError(t, err)
	m.recordAgentPrompt("unfinished prompt")

	require.NoError(t, m.saveCompletedSession())
	require.Equal(t, item.ID, saved.ID)
	require.Equal(t, item.CreatedAt, saved.CreatedAt)
	require.Len(t, saved.Messages, 2, "unanswered prompt is not committed")
	require.Equal(t, "Inspect the GPU", saved.Title)
	require.NotContains(t, string(saved.Messages[1].ToolCalls), "old result")
	require.NotContains(t, string(saved.Messages[1].ToolCalls), "gpu.txt")
}

func TestMissingWorkdirRestoresTranscriptButDisablesAgentRuntime(t *testing.T) {
	item := resumedFixture()
	item.WorkdirAvailable = false
	m, err := NewResumedWithHooksController("broker", "user", nil, Hooks{}, NewController("broker", Hooks{}), item)
	require.NoError(t, err)
	require.Nil(t, m.agent)
	require.Contains(t, m.agentTranscriptText(), "tools are unavailable")
	require.Contains(t, m.agentTranscriptText(), item.Workdir)
}

func TestMissingWorkdirCanBeExplicitlyReboundIncludingSpaces(t *testing.T) {
	item := resumedFixture()
	item.WorkdirAvailable = false
	m, err := NewResumedWithHooksController("broker", "user", nil, Hooks{}, NewController("broker", Hooks{}), item)
	require.NoError(t, err)
	root := filepath.Join(t.TempDir(), "work with spaces")
	require.NoError(t, os.MkdirAll(root, 0o700))

	next, _ := m.runAgentCommand("/cwd " + root)
	got := next.(model)
	require.NotNil(t, got.agent)
	require.Equal(t, root, got.agent.loop.Root)
	require.Contains(t, got.agentTranscriptText(), "tools now use")
}
