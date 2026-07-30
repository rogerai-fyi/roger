package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rogerai-fyi/roger/internal/capsule"
	"github.com/rogerai-fyi/roger/internal/harness"
	"github.com/rogerai-fyi/roger/internal/node"
	"github.com/rogerai-fyi/roger/internal/session"
)

// NewResumedWithHooksController restores durable semantic state before the Bubble Tea
// program starts. Runtime state is rebuilt fresh; historical tools are display-only.
func NewResumedWithHooksController(
	broker, user string,
	limits *LimitStore,
	hooks Hooks,
	ctrl *node.Controller,
	item session.Snapshot,
) (model, error) {
	m := NewWithHooksController(broker, user, limits, hooks, ctrl)
	history, err := restoredHarnessMessages(item.Messages)
	if err != nil {
		return model{}, err
	}
	m.mode = modeAgent
	m.threadID = item.ID
	m.sessionTitle = item.Title
	m.sessionWorkdir = filepath.Clean(item.Workdir)
	m.sessionWorkdirAvailable = item.WorkdirAvailable
	m.sessionCreated = item.CreatedAt
	m.ring = append([]capsule.Message(nil), item.Messages...)
	for _, msg := range m.ring {
		if msg.XRoger.Turn >= m.ringTurn {
			m.ringTurn = msg.XRoger.Turn + 1
		}
	}
	m.agentLines = restoredAgentLines(m, item.Messages)
	if !item.WorkdirAvailable {
		m.agentLines = append(m.agentLines,
			stDim.Render("· tools are unavailable because the saved working directory no longer exists: ")+stKey.Render(item.Workdir))
		m.agentLandingLines = len(m.agentLines)
		m.agentIn.Focus()
		return m, nil
	}
	m.agent = m.newAgentRuntime()
	if item.Model != "" {
		m.agent.model = item.Model
		m.agentPicked = true
	}
	if err := m.agent.loop.RestoreConversation(history); err != nil {
		return model{}, err
	}
	m.agentMaxSteps = m.agent.loop.MaxSteps
	m.agentLandingLines = len(m.agentLines)
	m.agentIn.Focus()
	return m, nil
}

func restoredHarnessMessages(messages []capsule.Message) ([]harness.Message, error) {
	out := make([]harness.Message, 0, len(messages))
	for i, msg := range messages {
		if msg.XRoger.Agent != agentSurfaceUser && !strings.HasPrefix(msg.XRoger.Agent, agentSurfacePrefix) {
			continue
		}
		if msg.Role != "user" && msg.Role != "assistant" {
			return nil, fmt.Errorf("session message %d has unsupported role %q", i, msg.Role)
		}
		out = append(out, harness.Message{Role: msg.Role, Content: msg.Content})
	}
	return out, nil
}

func restoredAgentLines(m model, messages []capsule.Message) []string {
	var lines []string
	for _, msg := range messages {
		switch {
		case msg.Role == "user" && msg.XRoger.Agent == agentSurfaceUser:
			lines = append(lines, m.agentAskLines(msg.Content)...)
		case msg.Role == "assistant" && strings.HasPrefix(msg.XRoger.Agent, agentSurfacePrefix):
			var calls []capsule.ToolCall
			if len(msg.ToolCalls) > 0 && json.Unmarshal(msg.ToolCalls, &calls) == nil {
				for _, call := range calls {
					lines = append(lines, stDim.Render("  ◉ "+call.Name+" · historical · not rerun"))
				}
			}
			if strings.TrimSpace(msg.Content) != "" {
				lines = append(lines, agentAnswerMark+msg.Content)
			}
		}
	}
	return lines
}

// completedAgentMessages returns only answered AGENT pairs. A user prompt recorded at send
// time remains pending until its assistant answer completes, so crashes cannot commit it.
func completedAgentMessages(messages []capsule.Message) []capsule.Message {
	var out []capsule.Message
	var pending *capsule.Message
	for i := range messages {
		msg := messages[i]
		switch {
		case msg.Role == "user" && msg.XRoger.Agent == agentSurfaceUser:
			copy := msg
			pending = &copy
		case msg.Role == "assistant" && strings.HasPrefix(msg.XRoger.Agent, agentSurfacePrefix) && pending != nil:
			out = append(out, *pending, durableAssistantMessage(msg))
			pending = nil
		}
	}
	return out
}

// durableAssistantMessage keeps historical tool names/outcomes but drops arguments and
// results: those fields can contain command lines, fetched credentials, or environment
// output and are not needed to reconstruct model conversation or the resume transcript.
func durableAssistantMessage(msg capsule.Message) capsule.Message {
	if len(msg.ToolCalls) == 0 {
		return msg
	}
	var calls []capsule.ToolCall
	if json.Unmarshal(msg.ToolCalls, &calls) != nil {
		msg.ToolCalls = nil
		return msg
	}
	for i := range calls {
		calls[i].Arguments = ""
		calls[i].Result = nil
	}
	msg.ToolCalls = capsule.ToolCallsRaw(calls)
	return msg
}

func (m *model) saveCompletedSession() error {
	if m.hooks.SaveSession == nil {
		return nil
	}
	messages := completedAgentMessages(m.ring)
	if len(messages) == 0 {
		return nil
	}
	now := time.Now()
	if m.sessionCreated.IsZero() {
		m.sessionCreated = now
	}
	if m.sessionWorkdir == "" {
		m.sessionWorkdir = agentRoot()
		m.sessionWorkdirAvailable = true
	}
	if m.sessionTitle == "" {
		m.sessionTitle = strings.TrimSpace(messages[0].Content)
	}
	modelName := ""
	if m.agent != nil {
		modelName = m.agent.model
	}
	return m.hooks.SaveSession(session.Snapshot{
		Version: CurrentSessionVersion(), ID: m.contextThreadID(), Title: m.sessionTitle,
		Workdir: m.sessionWorkdir, WorkdirAvailable: m.sessionWorkdirAvailable,
		CreatedAt: m.sessionCreated, UpdatedAt: now, Model: modelName,
		Messages: append([]capsule.Message(nil), messages...),
	})
}

// CurrentSessionVersion keeps the TUI from duplicating the storage schema number.
func CurrentSessionVersion() int { return session.CurrentVersion }

func workdirAvailable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
