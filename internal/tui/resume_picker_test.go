package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/session"
)

func pickerSessions() []session.Snapshot {
	base := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	return []session.Snapshot{
		{Version: 1, ID: "th_cwd_new", Title: "Fix the radio", Workdir: "/repo", CreatedAt: base, UpdatedAt: base.Add(3 * time.Hour)},
		{Version: 1, ID: "th_cwd_old", Title: "GPU enclosure", Workdir: "/repo", CreatedAt: base.Add(2 * time.Hour), UpdatedAt: base.Add(2 * time.Hour)},
		{Version: 1, ID: "th_elsewhere", Title: "Taxonomy pass", Workdir: "/other/private/project", CreatedAt: base.Add(4 * time.Hour), UpdatedAt: base.Add(time.Hour)},
	}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "home":
		return tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		return tea.KeyMsg{Type: tea.KeyEnd}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func updatePicker(m ResumePickerModel, k tea.KeyMsg) ResumePickerModel {
	next, _ := m.Update(k)
	return next.(ResumePickerModel)
}

func TestResumePickerDefaultsToCurrentDirectoryAndNewestUpdated(t *testing.T) {
	m := NewResumePicker(pickerSessions(), "/repo", time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC))
	view := m.View()
	require.Contains(t, view, "Resume a previous session")
	require.Contains(t, view, "Filter: [Cwd] All")
	require.Contains(t, view, "Sort: [Updated] Created")
	require.Contains(t, view, "Fix the radio")
	require.Contains(t, view, "GPU enclosure")
	require.NotContains(t, view, "Taxonomy pass")
	require.Equal(t, "th_cwd_new", m.Selected().ID)
}

func TestResumePickerFilterSearchSortAndStableSelection(t *testing.T) {
	m := NewResumePicker(pickerSessions(), "/repo", time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC))

	m = updatePicker(m, key("tab"))
	require.Contains(t, m.View(), "Filter: Cwd [All]")
	require.Contains(t, m.View(), "Taxonomy pass")
	require.Contains(t, m.View(), "/other/private/project")

	for _, r := range "TAXON" {
		m = updatePicker(m, key(string(r)))
	}
	require.Equal(t, "th_elsewhere", m.Selected().ID, "search is incremental and case-insensitive")

	for range 5 {
		m = updatePicker(m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m = updatePicker(m, key("ctrl+s"))
	require.Contains(t, m.View(), "Sort: Updated [Created]")
	require.Equal(t, "th_elsewhere", m.Selected().ID, "created sort is newest first")
}

func TestResumePickerNavigationSelectAndCancel(t *testing.T) {
	m := NewResumePicker(pickerSessions(), "/repo", time.Now())
	m = updatePicker(m, key("down"))
	require.Equal(t, "th_cwd_old", m.Selected().ID)
	m = updatePicker(m, key("up"))
	require.Equal(t, "th_cwd_new", m.Selected().ID)
	m = updatePicker(m, key("end"))
	require.Equal(t, "th_cwd_old", m.Selected().ID)
	m = updatePicker(m, key("home"))
	require.Equal(t, "th_cwd_new", m.Selected().ID)

	m = updatePicker(m, key("enter"))
	require.True(t, m.Done())
	require.False(t, m.Cancelled())

	cancelled := updatePicker(NewResumePicker(pickerSessions(), "/repo", time.Now()), key("esc"))
	require.True(t, cancelled.Done())
	require.True(t, cancelled.Cancelled())
}

func TestResumePickerEmptyAndNarrowViews(t *testing.T) {
	m := NewResumePicker(nil, "/repo", time.Now())
	require.Contains(t, strings.ToLower(m.View()), "no saved sessions")

	m = NewResumePicker(pickerSessions(), "/missing", time.Now())
	require.Contains(t, strings.ToLower(m.View()), "include all")
	m = updatePicker(m, key("tab"))
	for _, r := range "does-not-exist" {
		m = updatePicker(m, key(string(r)))
	}
	require.Contains(t, strings.ToLower(m.View()), "clear the search")
}
