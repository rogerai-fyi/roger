package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// HELP is a pager at common terminal heights; its scroll keys must reach the end, where the
// "more keys" block sits since the welcome card took the top (welcome_card.feature).
func TestHelpPagerScrollsToTheKeys(t *testing.T) {
	m := New("http://broker.local", "tester")
	m.width, m.height = 100, 30
	m.mode = modeHelp
	first := stripANSI(m.View())
	require.Contains(t, first, "── 0%", "HELP is paged at 30 rows")
	require.NotContains(t, first, "switch section", "the keys block is below the fold at 30 rows")

	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = tm.(model)
	require.NotContains(t, stripANSI(m.View()), "── 0%", "page down moves the pager")

	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = tm.(model)
	last := stripANSI(m.View())
	require.Contains(t, last, "── 100%", "end reaches the bottom")
	require.True(t, strings.Contains(last, "switch section"), "the keys block is readable at the end:\n%s", last)
}
