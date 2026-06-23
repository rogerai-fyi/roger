package tui

// The `rogerai ping` easter egg: Ping does its 2-frame walk across the terminal
// width, then exits cleanly - in the oneko / nyancat spirit. Under NO_COLOR /
// non-TTY (quiet) we skip the animation entirely and print one static pose with
// a friendly radio line, so a plain pipe never sees cursor churn.

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	pingWalkW    = 9 // width of a walk frame
	pingWalkLaps = 2 // how many times Ping crosses before exiting
)

type pingWalkModel struct {
	width, height int
	x             int // current left column of Ping
	frame         int // tick counter (drives the 2-frame step)
	laps          int // crossings completed
	done          bool
}

type walkTickMsg struct{}

func walkTick() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(time.Time) tea.Msg { return walkTickMsg{} })
}

func (m pingWalkModel) Init() tea.Cmd { return walkTick() }

func (m pingWalkModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		// any key bails out early, cleanly
		return m, tea.Quit
	case walkTickMsg:
		m.frame++
		m.x += 2 // a brisk-but-readable stride
		if m.x+pingWalkW >= m.width {
			m.x = -pingWalkW // re-enter from the left edge
			m.laps++
			if m.laps >= pingWalkLaps {
				m.done = true
				return m, tea.Quit
			}
		}
		return m, walkTick()
	}
	return m, nil
}

func (m pingWalkModel) View() string {
	if m.width == 0 {
		return ""
	}
	// the 2-frame step; the eye stays the live-red on-air dot.
	pf := pingWalkFrames[m.frame%2]
	pad := m.x
	if pad < 0 {
		pad = 0
	}
	indent := strings.Repeat(" ", pad)
	var b strings.Builder
	// vertically center-ish: a couple of blank lines so it walks mid-screen.
	top := m.height/2 - 3
	for i := 0; i < top; i++ {
		b.WriteByte('\n')
	}
	for i, line := range pf.lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(indent + tintEyeLine(line, "•"))
	}
	return b.String()
}

// PingWalk runs the `rogerai ping` easter egg: Ping walks across the terminal a
// couple of times, then exits. Returns nil on a clean finish. Under NO_COLOR /
// non-TTY it prints a single static pose instead of animating.
func PingWalk() error {
	if quiet {
		// A plain pipe gets one friendly, static frame - no animation.
		art := renderPing(pingWalkFrames[0], "•")
		fmt.Println()
		fmt.Println(art)
		fmt.Println()
		fmt.Println(lipgloss.NewStyle().Foreground(cMist).Render("  ping. ((•)) roger that - standing by."))
		return nil
	}
	_, err := tea.NewProgram(pingWalkModel{}, tea.WithAltScreen()).Run()
	return err
}
