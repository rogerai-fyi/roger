package tui

// THE [0] AGENT'S KEYBOARD outside the happy path: the transcript pane's scroll and fold keys, a
// question that owns the keys until it is answered, the return leg to a channel that may not exist,
// and the one-line summary a confirm modal shows. Driven through Update with real key messages.

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

func agentKeysModel(t *testing.T) model {
	t.Helper()
	base := browseSeed(120)
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	nm, _ := base.enterAgent()
	am := asModel(nm)
	am.agentHist = &inputHistory{}
	for i := 0; i < 120; i++ {
		am.agentLines = append(am.agentLines, "LINE")
	}
	am.agentIn.Focus()
	return am
}

func TestTheTranscriptPaneScrollsAndFolds(t *testing.T) {
	m := agentKeysModel(t)
	m, _ = send(m, kt(tea.KeyTab))
	require.True(t, m.agentPaneFocus)

	m, _ = send(m, kt(tea.KeyHome))
	require.True(t, m.agentUnstuck, "home leaves the live tail")
	require.True(t, m.agentVP.AtTop())
	for _, k := range []tea.KeyMsg{kt(tea.KeyPgDown), kt(tea.KeyCtrlD), kt(tea.KeyDown), keyRunes("j")} {
		before := m.agentVP.YOffset
		m, _ = send(m, k)
		require.Greater(t, m.agentVP.YOffset, before, "%s scrolls down", k.String())
	}
	for _, k := range []tea.KeyMsg{kt(tea.KeyPgUp), kt(tea.KeyCtrlU), kt(tea.KeyUp)} {
		before := m.agentVP.YOffset
		m, _ = send(m, k)
		require.Less(t, m.agentVP.YOffset, before, "%s scrolls up", k.String())
	}
	m, _ = send(m, kt(tea.KeyEnd))
	require.False(t, m.agentUnstuck)
	require.True(t, m.agentVP.AtBottom())

	m, _ = send(m, keyRunes("d"))
	require.True(t, m.showToolOutput)
	require.Contains(t, stripANSI(m.status), "tool output shown")
	m, _ = send(m, keyRunes("d"))
	require.False(t, m.showToolOutput)
	require.Contains(t, stripANSI(m.status), "tool output hidden")

	// A control key that means nothing here keeps the pane focused and types nothing.
	m, _ = send(m, kt(tea.KeyF5))
	require.True(t, m.agentPaneFocus)
	require.Equal(t, "", m.agentIn.Value())

	m, _ = send(m, kt(tea.KeyEnter))
	require.False(t, m.agentPaneFocus)
	require.Contains(t, stripANSI(m.status), "the mic is yours")
}

func TestTheInputSideKeys(t *testing.T) {
	m := agentKeysModel(t)

	m, _ = send(m, kt(tea.KeyCtrlO))
	require.True(t, m.showToolCalls)
	require.Contains(t, stripANSI(m.status), "tool machinery open")
	m, _ = send(m, kt(tea.KeyCtrlO))
	require.False(t, m.showToolCalls)
	require.Contains(t, stripANSI(m.status), "tool machinery folded")

	m.agentHist.add("first ask")
	m.agentHist.add("second ask")
	m, _ = send(m, kt(tea.KeyUp))
	require.Equal(t, "second ask", m.agentIn.Value())
	m, _ = send(m, kt(tea.KeyUp))
	require.Equal(t, "first ask", m.agentIn.Value())
	m, _ = send(m, kt(tea.KeyCtrlN))
	require.Equal(t, "second ask", m.agentIn.Value())
	m, _ = send(m, kt(tea.KeyDown))
	require.Equal(t, "", m.agentIn.Value(), "past the newest restores the empty draft")

	m.agentUnstuck = true
	m, _ = send(m, kt(tea.KeyEnd))
	require.False(t, m.agentUnstuck, "end jumps back to the live tail")

	// shift+tab goes back to the channel it came from.
	back, _ := send(m, kt(tea.KeyShiftTab))
	require.Equal(t, modeChat, back.mode)
	require.Contains(t, stripANSI(back.status), "back on the channel")
	// With no channel open it says so and stays.
	m.connected = nil
	stay, _ := send(m, kt(tea.KeyShiftTab))
	require.Equal(t, modeAgent, stay.mode)
	require.Contains(t, stripANSI(stay.status), "no channel open to go back to")
}

func TestAQuestionOwnsTheKeysUntilAnswered(t *testing.T) {
	m := agentKeysModel(t)
	resp := make(chan string, 1)
	m.agentPendingAsk = &agentAsk{question: "which file?", options: []string{"a.go", "b.go"}, resp: resp}

	got, _ := send(m, kt(tea.KeyEnter))
	require.NotNil(t, got.agentPendingAsk, "an empty enter does not answer")
	require.Contains(t, stripANSI(got.status), "type an answer, or esc")

	// A digit beyond the options types into the composer instead of answering.
	got, _ = send(got, keyRunes("9"))
	require.NotNil(t, got.agentPendingAsk)
	require.Equal(t, "9", got.agentIn.Value())

	// esc declines - and the agent is TOLD, not left waiting.
	got.agentIn.SetValue("")
	got, _ = send(got, kt(tea.KeyEsc))
	require.Nil(t, got.agentPendingAsk)
	require.Equal(t, "", <-resp)
	require.Contains(t, stripANSI(got.agentLines[len(got.agentLines)-1]), "(not answered)")

	// A typed answer is sent as words.
	got.agentPendingAsk = &agentAsk{question: "which file?", resp: resp}
	got, _ = send(got, keyRunes("c.go"), kt(tea.KeyEnter))
	require.Equal(t, "c.go", <-resp)
	require.Nil(t, got.agentPendingAsk)
}

func TestAConfirmNamesWhatItWillTouch(t *testing.T) {
	for _, tc := range []struct {
		c    agentConfirm
		want string
	}{
		{agentConfirm{tool: "run_shell", args: map[string]any{"cmd": "rm -rf build"}}, "run_shell: rm -rf build"},
		{agentConfirm{tool: "write_file", args: map[string]any{"path": "a.txt", "content": "hello"}}, "write_file: a.txt (5 bytes)"},
		{agentConfirm{tool: "edit_file", args: map[string]any{"path": "main.go"}}, "edit_file: main.go"},
		{agentConfirm{tool: "edit_file", args: map[string]any{"path": "main.go", "old_string": "foo", "new_string": "bar"}}, "edit_file: main.go  foo -> bar"},
		{agentConfirm{tool: "fetch_url"}, "fetch_url"},
	} {
		require.Equal(t, tc.want, tc.c.summary())
	}
}

func TestAToolFailureCardReadsOneLine(t *testing.T) {
	require.Equal(t, "exit 1", shortToolFailure("exit 1\nstack trace here"))
	require.Equal(t, "refused · writing outside the workdir",
		shortToolFailure("refused: writing outside the workdir. Ask the operator first."))
	require.Equal(t, "refused · see https://rogerai.fyi/docs",
		shortToolFailure("refused: see https://rogerai.fyi/docs."), "a URL is never cut at its dots")
}
