package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"rogerai.fm/roger/internal/session"
)

// ResumePickerModel is the small, standalone `roger resume` selector. It intentionally does
// not share the main radio model: selection happens before any controller, poller, or model
// connection is started.
type ResumePickerModel struct {
	all      []session.Snapshot
	filtered []session.Snapshot
	cwd      string
	now      time.Time
	search   string
	showAll  bool
	created  bool
	cursor   int
	width    int
	done     bool
	cancel   bool
}

func NewResumePicker(items []session.Snapshot, cwd string, now time.Time) ResumePickerModel {
	m := ResumePickerModel{
		all: append([]session.Snapshot(nil), items...), cwd: filepath.Clean(cwd), now: now, width: 100,
	}
	m.refilter()
	return m
}

func (m ResumePickerModel) Init() tea.Cmd { return nil }

func (m ResumePickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.done, m.cancel = true, true
			return m, tea.Quit
		case "enter":
			if len(m.filtered) > 0 {
				m.done = true
				return m, tea.Quit
			}
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor+1 < len(m.filtered) {
				m.cursor++
			}
		case "home":
			m.cursor = 0
		case "end":
			if len(m.filtered) > 0 {
				m.cursor = len(m.filtered) - 1
			}
		case "tab":
			m.showAll = !m.showAll
			m.refilter()
		case "ctrl+s":
			m.created = !m.created
			m.refilter()
		case "backspace":
			if len(m.search) > 0 {
				rs := []rune(m.search)
				m.search = string(rs[:len(rs)-1])
				m.refilter()
			}
		default:
			if msg.Type == tea.KeyRunes {
				m.search += string(msg.Runes)
				m.refilter()
			}
		}
	}
	return m, nil
}

func (m ResumePickerModel) View() string {
	var b strings.Builder
	b.WriteString(stKey.Render("Resume a previous session"))
	b.WriteString("\n\n")
	if m.search == "" {
		b.WriteString(stDim.Render("Type to search"))
	} else {
		b.WriteString(stDim.Render("Search: ") + stKey.Render(m.search))
	}
	b.WriteString("\n\n")
	if m.showAll {
		b.WriteString(stDim.Render("Filter: Cwd ") + stKey.Render("[All]"))
	} else {
		b.WriteString(stDim.Render("Filter: ") + stKey.Render("[Cwd]") + stDim.Render(" All"))
	}
	b.WriteString("    ")
	if m.created {
		b.WriteString(stDim.Render("Sort: Updated ") + stKey.Render("[Created]"))
	} else {
		b.WriteString(stDim.Render("Sort: ") + stKey.Render("[Updated]") + stDim.Render(" Created"))
	}
	b.WriteString("\n\n")

	if len(m.all) == 0 {
		b.WriteString(stDim.Render("No saved sessions. Complete an AGENT turn to create one."))
		b.WriteByte('\n')
		return b.String()
	}
	if len(m.filtered) == 0 {
		hint := "No matching sessions. Clear the search"
		if !m.showAll {
			hint += " or include All directories"
		}
		b.WriteString(stDim.Render(hint + "."))
		b.WriteByte('\n')
		return b.String()
	}
	for i, item := range m.filtered {
		cursor := "  "
		if i == m.cursor {
			cursor = stLive.Render("› ")
		}
		age := humanAge(m.now, item.UpdatedAt)
		titleWidth := max(16, m.width-34)
		title := ansi.Truncate(session.SafeLabel(item.Title), titleWidth, "…")
		if title == "" {
			title = "(untitled session)"
		}
		row := fmt.Sprintf("%-9s %-*s  %s", age, titleWidth, title, shortSessionID(item.ID))
		b.WriteString(cursor + row)
		if m.showAll && filepath.Clean(item.Workdir) != m.cwd {
			b.WriteString(stDim.Render("  " + ansi.Truncate(session.SafeLabel(item.Workdir), max(12, m.width/3), "…")))
		}
		b.WriteByte('\n')
	}
	b.WriteString("\n")
	b.WriteString(stDim.Render("↑↓/jk select · enter resume · tab Cwd/All · ctrl+s sort · esc cancel"))
	b.WriteByte('\n')
	return b.String()
}

func (m ResumePickerModel) Selected() session.Snapshot {
	if len(m.filtered) == 0 || m.cursor < 0 || m.cursor >= len(m.filtered) {
		return session.Snapshot{}
	}
	return m.filtered[m.cursor]
}

func (m ResumePickerModel) Done() bool      { return m.done }
func (m ResumePickerModel) Cancelled() bool { return m.cancel }

func (m *ResumePickerModel) refilter() {
	selected := m.Selected().ID
	query := strings.ToLower(strings.TrimSpace(m.search))
	m.filtered = m.filtered[:0]
	for _, item := range m.all {
		if !m.showAll && filepath.Clean(item.Workdir) != m.cwd {
			continue
		}
		haystack := strings.ToLower(item.Title + "\n" + item.ID + "\n" + item.Workdir)
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}
		m.filtered = append(m.filtered, item)
	}
	sort.Slice(m.filtered, func(i, j int) bool {
		a, b := m.filtered[i], m.filtered[j]
		at, bt := a.UpdatedAt, b.UpdatedAt
		if m.created {
			at, bt = a.CreatedAt, b.CreatedAt
		}
		if at.Equal(bt) {
			return a.ID < b.ID
		}
		return at.After(bt)
	})
	m.cursor = 0
	for i := range m.filtered {
		if m.filtered[i].ID == selected {
			m.cursor = i
			break
		}
	}
}

func humanAge(now, then time.Time) string {
	d := now.Sub(then)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func shortSessionID(id string) string {
	if len(id) <= 14 {
		return id
	}
	return id[:14] + "…"
}

// SelectResumeSession runs the standalone picker and returns (selection, cancelled, error).
func SelectResumeSession(items []session.Snapshot, cwd string) (session.Snapshot, bool, error) {
	final, err := tea.NewProgram(NewResumePicker(items, cwd, time.Now()), tea.WithAltScreen()).Run()
	if err != nil {
		return session.Snapshot{}, false, err
	}
	picker, ok := final.(ResumePickerModel)
	if !ok {
		return session.Snapshot{}, false, fmt.Errorf("resume picker returned unexpected model %T", final)
	}
	return picker.Selected(), picker.Cancelled(), nil
}
