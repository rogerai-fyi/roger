package tui

// THE GLOBAL KEY ROUTER, mode by mode: the keys each small modal owns and what each one does to the
// screen. Driven through Update with real key messages; assertions are on the mode, the status line,
// the input buffers and the rendered view.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

func keyModel(t *testing.T) model {
	t.Helper()
	var tm tea.Model = New("http://broker.local", "tester")
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m := asModel(tm)
	m.cmdHist, m.chatHist = &inputHistory{}, &inputHistory{} // in memory: no history file
	return m
}

func send(m model, msgs ...tea.KeyMsg) (model, tea.Cmd) {
	var tm tea.Model = m
	var cmd tea.Cmd
	for _, k := range msgs {
		tm, cmd = tm.Update(k)
	}
	return asModel(tm), cmd
}

func kt(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func TestAKeyWakesThePingWorldBackIntoTheInputItLeft(t *testing.T) {
	for _, prev := range []mode{modeChat, modeCommand, modeBrowse} {
		m := keyModel(t)
		m.mode, m.prevMode = modePingWorld, prev
		got, cmd := send(m, kt(tea.KeyCtrlC))
		require.Equal(t, prev, got.mode, "even ctrl+c only wakes the screensaver")
		require.Contains(t, stripANSI(got.status), "welcome back")
		require.NotNil(t, cmd, "the beat (and the cursor blink) is re-armed")
	}
}

func TestDecliningTheQuitGuardKeepsSharing(t *testing.T) {
	m := keyModel(t)
	m.mode, m.quitReturn = modeQuitConfirm, modeShare
	got, _ := send(m, keyRunes("n"))
	require.Equal(t, modeShare, got.mode)
	require.Contains(t, stripANSI(got.status), "still on air")
}

func TestTheCommandPaletteRecallsAndCancels(t *testing.T) {
	m := keyModel(t)
	m.cmdHist.add("help")
	m.cmdHist.add("log")
	m.mode = modeCommand
	got, _ := send(m, kt(tea.KeyUp))
	require.Equal(t, "log", got.cmd.Value())
	got, _ = send(got, kt(tea.KeyUp))
	require.Equal(t, "help", got.cmd.Value())
	got, _ = send(got, kt(tea.KeyDown))
	require.Equal(t, "log", got.cmd.Value())
	got, _ = send(got, kt(tea.KeyEsc))
	require.Equal(t, modeBrowse, got.mode)
	require.Equal(t, "", got.cmd.Value())

	// enter runs the line and leaves the palette.
	m.mode = modeCommand
	m.cmd.SetValue("log")
	got, _ = send(m, kt(tea.KeyEnter))
	require.Equal(t, modeLog, got.mode)
	// /log is read-only: any key closes it.
	got, _ = send(got, keyRunes("x"))
	require.Equal(t, modeBrowse, got.mode)
}

func TestChannelKeysScrollPeekCopyAndRecall(t *testing.T) {
	m := keyModel(t)
	m.mode = modeChat

	got, _ := send(m, kt(tea.KeyTab))
	require.Equal(t, modeBrowse, got.mode, "tab peeks at the band without disconnecting")
	require.Contains(t, stripANSI(got.status), "the channel stays open")

	for _, k := range []tea.KeyType{tea.KeyPgUp, tea.KeyPgDown, tea.KeyCtrlU, tea.KeyCtrlD, tea.KeyEnd} {
		got, _ = send(m, kt(k))
		require.Equal(t, modeChat, got.mode, k.String())
		require.False(t, got.chatUnstuck, "%s on an empty transcript stays at the bottom", k)
	}

	got, _ = send(m, kt(tea.KeyCtrlP))
	require.Contains(t, stripANSI(got.status), "tool approvals live in the AGENT")

	got, cmd := send(m, kt(tea.KeyCtrlY))
	require.Contains(t, stripANSI(got.status), "nothing to copy yet")
	require.Nil(t, cmd)
	m.lastReply = "roger that"
	got, cmd = send(m, kt(tea.KeyCtrlY))
	require.Contains(t, stripANSI(got.status), "the last reply")
	require.NotNil(t, cmd, "ctrl+y writes the reply to the clipboard")

	got, _ = send(m, kt(tea.KeyCtrlO))
	require.True(t, got.mouseOff)
	got, _ = send(got, kt(tea.KeyCtrlO))
	require.False(t, got.mouseOff)

	// Recall: up brings back the last sent line, ctrl+n / down walk forward to the draft.
	m.chatHist.add("first")
	m.chatHist.add("second")
	got, _ = send(m, kt(tea.KeyUp))
	require.Equal(t, "second", got.chatIn.Value())
	got, _ = send(got, kt(tea.KeyUp))
	require.Equal(t, "first", got.chatIn.Value())
	got, _ = send(got, kt(tea.KeyCtrlN))
	require.Equal(t, "second", got.chatIn.Value())
	got, _ = send(got, kt(tea.KeyDown))
	require.Equal(t, "", got.chatIn.Value(), "past the newest restores the (empty) draft")
	got, _ = send(got, kt(tea.KeyDown))
	require.Equal(t, modeChat, got.mode, "down with nothing newer scrolls instead")

	// enter on an empty line, or with no channel, sends nothing.
	got, cmd = send(m, kt(tea.KeyEnter))
	require.Nil(t, cmd)
	require.Empty(t, got.transcript)
}

func TestHelpIsAPager(t *testing.T) {
	m := keyModel(t)
	m.mode = modeHelp
	for _, k := range []tea.KeyMsg{kt(tea.KeyDown), keyRunes("j"), kt(tea.KeyPgDown), kt(tea.KeyEnd),
		kt(tea.KeyUp), keyRunes("k"), kt(tea.KeyPgUp), kt(tea.KeyHome), keyRunes("z")} {
		got, _ := send(m, k)
		require.Equal(t, modeHelp, got.mode, "%q pages, it does not leave help", k.String())
	}
	got, _ := send(m, kt(tea.KeyEnd))
	require.True(t, got.helpVP.AtBottom(), "end reaches the bottom")
	got, _ = send(got, kt(tea.KeyHome))
	require.True(t, got.helpVP.AtTop(), "home returns to the top")
	got, _ = send(m, keyRunes("?"))
	require.Equal(t, modeBrowse, got.mode)
}

func TestConnectConfirmDetailToggleAndConnectingCancel(t *testing.T) {
	m := keyModel(t)
	m.mode = modeConnectConfirm
	got, _ := send(m, keyRunes("d"))
	require.True(t, got.showDetail)
	require.Equal(t, modeConnectConfirm, got.mode)
	got, _ = send(got, keyRunes("d"))
	require.False(t, got.showDetail)
	got, _ = send(got, keyRunes("n"))
	require.Equal(t, modeBrowse, got.mode)
	require.Contains(t, stripANSI(got.status), "denied - no channel opened")

	m.mode = modeConnecting
	got, _ = send(m, kt(tea.KeyEsc))
	require.Equal(t, modeBrowse, got.mode)
	require.Contains(t, stripANSI(got.status), "no channel opened")
}

func TestTheStationLogClosesOrRescans(t *testing.T) {
	m := keyModel(t)
	for _, k := range []tea.KeyMsg{kt(tea.KeyEsc), kt(tea.KeyLeft), keyRunes("h"), keyRunes("i"), keyRunes("q")} {
		m.mode = modeBandDetail
		got, _ := send(m, k)
		require.Equal(t, modeBrowse, got.mode, k.String())
	}
	m.mode = modeBandDetail
	m.scanned = true
	got, cmd := send(m, keyRunes("r"))
	require.Equal(t, modeBandDetail, got.mode)
	require.False(t, got.scanned, "r forgets the stale scan")
	require.Contains(t, got.status, "re-scanning")
	require.NotNil(t, cmd)
	got, _ = send(got, keyRunes("x"))
	require.Equal(t, modeBandDetail, got.mode, "any other key leaves the log open")
}

func TestFrequencyEntryCancels(t *testing.T) {
	m := keyModel(t)
	m.mode = modeFreqEntry
	m.freqIn.Focus()
	got, _ := send(m, keyRunes("8F3K"))
	require.Equal(t, "8F3K", got.freqIn.Value())
	got, _ = send(got, kt(tea.KeyEsc))
	require.Equal(t, modeBrowse, got.mode)
	require.True(t, strings.Contains(stripANSI(got.status), "cancelled"))
}
