package tui

// THE OWNER'S SERVING SETTINGS AS A ROW OF SHARE (features/serving/local_models.feature §11). Which
// build serves, the idle timeout and the disk budget are one visible row under roger's own rows,
// changeable in place: Enter opens a small chooser (which setting, then its value), and applying
// calls Hooks.Set, whose answer is the CLI's own sentence - what the change means on this machine
// and that it applies from the next serve or fetch.

import (
	"fmt"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"rogerai.fm/roger/v7/internal/modelview"
)

// settingsTerminal is where the settings are changed when this build cannot change them here.
const settingsTerminal = "`roger model runtime|idle|budget` in a terminal"

// settingsKinds are the chooser's settings, in the order the line says them, with Hooks.Set's kind.
var settingsKinds = []struct{ word, kind, title, prompt string }{
	{"build", "runtime", "BUILD - which llama.cpp build serves roger's models", ""},
	{"idle", "idle", "IDLE - stop a model after it sits unused this long", "off, or a duration like 30m or 2h"},
	{"disk", "budget", "DISK - the most roger's fetched models may take on this disk", "off, or a size like 20G"},
}

// settingsChooser is the chooser open on the settings row. which is the setting; picking is the
// first step (which setting); choice is the build under the cursor; input is a typed value.
type settingsChooser struct {
	which   int
	picking bool
	choice  int
	input   string
}

var hereBuild = regexp.MustCompile(`\bthe (\w+) build\b`)

// settingsHereWord is what the build choice means on THIS machine, in two words ("CUDA here"), from
// the CLI's own sentence; "" when the sentence names no build.
func settingsHereWord(here string) string {
	if strings.Contains(here, "cannot serve here") {
		return "cannot serve here"
	}
	if g := hereBuild.FindStringSubmatch(here); g != nil {
		return buildWord(g[1]) + " here"
	}
	return ""
}

// buildWord is a build as an owner reads it.
func buildWord(b string) string {
	switch strings.ToLower(b) {
	case "cpu":
		return "CPU"
	case "vulkan":
		return "Vulkan"
	case "cuda":
		return "CUDA"
	case "metal":
		return "Metal"
	}
	return b
}

// servingFacts is the build and the speed of one serve: measured once the runtime has counted its
// own tokens, else the estimate, marked as one. "" when neither is known.
func servingFacts(sv modelview.Serving) string {
	var parts []string
	if sv.Build != "" {
		parts = append(parts, buildWord(sv.Build))
	}
	if sv.Measured > 0 {
		parts = append(parts, fmt.Sprintf("%.1f tok/s measured", sv.Measured))
	} else if sv.Estimate != "" {
		parts = append(parts, sv.Estimate)
	}
	return strings.Join(parts, " · ")
}

// settingsValues are the three settings in words, in settingsKinds order.
func settingsValues(s modelview.ServingSettings, used int64) [3]string {
	build := modelsDash(s.Runtime)
	if w := settingsHereWord(s.Here); w != "" {
		build += ", " + w
	}
	disk := "no budget"
	if s.DiskBudget > 0 {
		disk = modelsBytes(s.DiskBudget) + " budget, " + modelsBytes(used) + " used"
	}
	return [3]string{build, modelsDash(s.Idle), disk}
}

// settingsLine is the row: "build: auto, CUDA here · idle: off · disk: no budget".
func settingsLine(v modelview.View) string {
	vals := settingsValues(v.Settings, v.DiskUsed)
	return "build: " + vals[0] + " · idle: " + vals[1] + " · disk: " + vals[2]
}

// settingsDetail is the detail banner for the settings row.
func (m model) settingsDetail() string {
	here := m.mdl.view.Settings.Here
	what := "⏎ change build, idle or disk"
	if m.hooks.Models.Set == nil {
		what = "change these with " + settingsTerminal
	}
	if here == "" {
		return what
	}
	return what + " · " + here // the action first, so a narrow terminal keeps it
}

// onSettingsEnter is Enter on the settings row: the chooser, or where to change them instead.
func (m *model) onSettingsEnter() (tea.Model, tea.Cmd) {
	if m.hooks.Models.Set == nil {
		m.status = stDim.Render("this build changes these with " + settingsTerminal)
		return m, nil
	}
	m.mdl.set = &settingsChooser{picking: true}
	return m, nil
}

// onSettingsKey drives the open chooser; it owns every key, so digits typed into a value never
// jump to another screen. ESC backs out one step, and out of the chooser, changing nothing.
func (m *model) onSettingsKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.mdl.set
	choices := m.mdl.view.Settings.Choices
	step := 0
	switch k.String() {
	case "right", "down", "l", "j", "tab", " ":
		step = 1
	case "left", "up", "h", "k", "shift+tab":
		step = -1
	}
	if k.Type == tea.KeyEsc {
		if c.picking {
			m.mdl.set = nil
		} else {
			c.picking, c.input = true, ""
		}
		return m, nil
	}
	switch {
	case c.picking:
		switch {
		case step != 0:
			c.which = (c.which + step + len(settingsKinds)) % len(settingsKinds)
		case k.Type == tea.KeyEnter:
			c.picking, c.input = false, ""
			if c.which == 0 {
				c.choice = 0
				for i, ch := range choices {
					if ch == m.mdl.view.Settings.Runtime {
						c.choice = i
					}
				}
			}
		}
		return m, nil
	case c.which == 0: // build: cycle through the CLI's choices
		switch {
		case step != 0 && len(choices) > 0:
			c.choice = (c.choice + step + len(choices)) % len(choices)
		case k.Type == tea.KeyEnter && c.choice < len(choices):
			return m.settingsApply(choices[c.choice])
		}
		return m, nil
	}
	// idle or disk: a typed value.
	switch k.Type {
	case tea.KeyEnter:
		return m.settingsApply(strings.TrimSpace(c.input))
	case tea.KeyBackspace, tea.KeyDelete:
		if r := []rune(c.input); len(r) > 0 {
			c.input = string(r[:len(r)-1])
		}
	case tea.KeyRunes:
		if len(c.input) < 24 {
			c.input += string(k.Runes)
		}
	}
	return m, nil
}

// settingsApply closes the chooser and asks Hooks.Set; its sentence or refusal reaches the status
// line through modelsDidMsg. An empty value changes nothing.
func (m *model) settingsApply(value string) (tea.Model, tea.Cmd) {
	kind := settingsKinds[m.mdl.set.which].kind
	m.mdl.set = nil
	if value == "" {
		m.status = stDim.Render("nothing changed")
		return m, nil
	}
	set := m.hooks.Models.Set
	return m, modelsDo(func() (string, error) { return set(kind, value) })
}

// settingsChooserView draws the open chooser under the detail banner. The selection is bracketed
// as well as coloured, so it survives without colour.
func (m model) settingsChooserView(w int) string {
	c := m.mdl.set
	var b strings.Builder
	ln := func(s string) { b.WriteString(truncVisible(s, w) + "\n") }
	mark := func(sel bool, s string) string {
		if sel {
			return stKey.Render("[" + s + "]")
		}
		return stDim.Render(" " + s + " ")
	}
	b.WriteString("\n")
	switch {
	case c.picking:
		st := m.mdl.view.Settings
		disk := "no budget"
		if st.DiskBudget > 0 {
			disk = modelsBytes(st.DiskBudget)
		}
		vals := []string{modelsDash(st.Runtime), modelsDash(st.Idle), disk}
		ln("  " + stKey.Render("CHANGE A SERVING SETTING"))
		var parts []string
		for i, s := range settingsKinds {
			parts = append(parts, mark(i == c.which, s.word+": "+vals[i]))
		}
		ln("   " + strings.Join(parts, " "))
		ln("  " + stKey.Render("←→") + stDim.Render(" choose · ") + stKey.Render("⏎") + stDim.Render(" change · ") +
			stKey.Render("esc") + stDim.Render(" back"))
	case c.which == 0:
		ln("  " + stKey.Render(settingsKinds[0].title))
		var parts []string
		for i, ch := range m.mdl.view.Settings.Choices {
			if i == c.choice {
				parts = append(parts, stKey.Render("["+ch+"]"))
			} else {
				parts = append(parts, stDim.Render(ch))
			}
		}
		ln("    " + strings.Join(parts, "  "))
		ln("  " + stKey.Render("←→") + stDim.Render(" choose · ") + stKey.Render("⏎") + stDim.Render(" apply from the next serve · ") +
			stKey.Render("esc") + stDim.Render(" back"))
	default:
		s := settingsKinds[c.which]
		ln("  " + stKey.Render(s.title))
		ln("    > " + c.input + "█")
		ln("  " + stDim.Render(s.prompt+" · ") + stKey.Render("⏎") + stDim.Render(" apply · ") + stKey.Render("esc") + stDim.Render(" back"))
	}
	return b.String()
}
