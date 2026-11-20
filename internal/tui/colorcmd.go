package tui

// colorcmd.go: the live /theme and /colorblind switches. Both retint the running
// TUI at once (the hue slots and the forced renderer arm are read at render time),
// carry the lines already in the transcripts across (recolor.go), and persist through
// the host hooks when they are wired.

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// setThemeLive handles /theme [auto|light|dark]. Bare /theme steps to the next one.
func (m model) setThemeLive(args []string) (tea.Model, tea.Cmd) {
	next := nextIn(ThemeModes, themeMode)
	if len(args) > 0 {
		var ok bool
		if next, ok = ParseTheme(args[0]); !ok {
			m.status = stDim.Render("usage: ") + stKey.Render("/theme "+modeChoices(ThemeModes))
			return m, nil
		}
	}
	wasDark := lipgloss.HasDarkBackground()
	SetTheme(next)
	applyTheme()
	if nowDark := lipgloss.HasDarkBackground(); nowDark != wasDark {
		m = m.recolorStored(sgrMap(armSwap(themeTokens(), wasDark, nowDark)))
	}
	if m.hooks.SaveTheme != nil {
		m.hooks.SaveTheme(next)
	}
	switch {
	case next != "auto" && paletteMono:
		m.status = stKey.Render("theme "+next) + stDim.Render(" is saved, and shows once mono is off (roger config set palette full)")
	case next == "light":
		m.status = stKey.Render("theme light") + stDim.Render(": light page, dark text · /theme auto follows your terminal")
	case next == "dark":
		m.status = stKey.Render("theme dark") + stDim.Render(": dark page, light text · /theme auto follows your terminal")
	default:
		m.status = stKey.Render("theme auto") + stDim.Render(": follows your terminal · /theme light or dark to choose")
	}
	return m, nil
}

// setColorblindLive handles /colorblind [off|protan|deutan|tritan]. Bare
// /colorblind steps to the next one.
func (m model) setColorblindLive(args []string) (tea.Model, tea.Cmd) {
	next := nextIn(ColorblindModes, colorblindMode)
	if len(args) > 0 {
		var ok bool
		if next, ok = ParseColorblind(args[0]); !ok {
			m.status = stDim.Render("usage: ") + stKey.Render("/colorblind "+modeChoices(ColorblindModes))
			return m, nil
		}
	}
	before := activeBoard()
	SetColorblind(next)
	m = m.recolorStored(sgrMap(boardSwap(before, activeBoard(), lipgloss.HasDarkBackground())))
	if m.hooks.SaveColorblind != nil {
		m.hooks.SaveColorblind(next)
	}
	switch next {
	case "protan", "deutan":
		m.status = stKey.Render("colorblind "+next) + stDim.Render(": red highlights are blue now, and green is cyan · /colorblind off restores")
	case "tritan":
		m.status = stKey.Render("colorblind tritan") + stDim.Render(": blue info is magenta now, and red stays · /colorblind off restores")
	default:
		m.status = stKey.Render("colorblind off") + stDim.Render(": the full color board")
	}
	return m, nil
}
