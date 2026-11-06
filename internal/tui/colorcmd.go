package tui

// colorcmd.go: the live /theme and /colorblind switches. Both retint the running
// TUI at once (the hue slots and the forced renderer arm are read at render time)
// and persist through the host hooks when they are wired.

import (
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// setThemeLive handles /theme [auto|light|dark]. Bare /theme steps to the next one.
func (m model) setThemeLive(args []string) (tea.Model, tea.Cmd) {
	next := cycleSetting(themeModes, themeMode)
	if len(args) > 0 {
		next = strings.ToLower(args[0])
		if !slices.Contains(themeModes, next) {
			m.status = stDim.Render("usage: ") + stKey.Render("/theme auto | light | dark")
			return m, nil
		}
	}
	SetTheme(next)
	applyTheme()
	if m.hooks.SaveTheme != nil {
		m.hooks.SaveTheme(next)
	}
	switch next {
	case "light":
		m.status = stKey.Render("theme light") + stDim.Render(": light page, dark text · /theme auto follows your terminal")
	case "dark":
		m.status = stKey.Render("theme dark") + stDim.Render(": dark page, light text · /theme auto follows your terminal")
	default:
		m.status = stKey.Render("theme auto") + stDim.Render(": follows your terminal · /theme light or dark to choose")
	}
	return m, nil
}

// setColorblindLive handles /colorblind [off|protan|deutan|tritan]. Bare
// /colorblind steps to the next one.
func (m model) setColorblindLive(args []string) (tea.Model, tea.Cmd) {
	next := cycleSetting(colorblindModes, colorblindMode)
	if len(args) > 0 {
		next = strings.ToLower(args[0])
		if !slices.Contains(colorblindModes, next) {
			m.status = stDim.Render("usage: ") + stKey.Render("/colorblind protan | deutan | tritan | off")
			return m, nil
		}
	}
	SetColorblind(next)
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
