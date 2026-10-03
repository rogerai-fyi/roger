package tui

// features/serving/local_models.feature §11 (@tui): the owner's serving settings are one visible,
// changeable row of SHARE, and what roger serves says which build it runs on and how fast it was
// measured answering (or the estimate, marked as one, before it has answered).

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/modelview"
)

func settingsView() modelview.View {
	v := baseView()
	v.Settings = modelview.ServingSettings{Runtime: "auto", Idle: "off",
		Here:    "on this machine: the cuda build of llama.cpp b6000 (412.0 MiB, installed)",
		Choices: []string{"auto", "cpu", "gpu", "vulkan", "cuda"}}
	return v
}

// sKey presses keys on SHARE, the way the app routes them.
func sKey(t *testing.T, m model, keys ...string) (model, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "left":
			msg = tea.KeyMsg{Type: tea.KeyLeft}
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		var tm tea.Model
		tm, cmd = m.onShareKey(msg)
		m = asModel(tm)
	}
	return m, cmd
}

// toSettings moves the SHARE cursor onto the settings row.
func toSettings(t *testing.T, m model) model {
	t.Helper()
	for i := 0; i < m.shareRowCount(); i++ {
		if e, ok := m.shareExtraAt(m.shareCursor); ok && e.kind == extraSettings {
			return m
		}
		m, _ = sKey(t, m, "down")
	}
	t.Fatalf("the cursor never reached the settings row")
	return m
}

func TestSettingsHereWord(t *testing.T) {
	for _, c := range []struct{ here, want string }{
		{"on this machine: the cuda build of llama.cpp b6000 (412.0 MiB, installed)", "CUDA here"},
		{"on this machine: the vulkan build of llama.cpp b6000 (30.0 MiB, not fetched yet)", "Vulkan here"},
		{"on this machine: the cpu build of llama.cpp b6000 (20.0 MiB, installed)", "CPU here"},
		{"on this machine: the metal build of llama.cpp b6000 (20.0 MiB, installed)", "Metal here"},
		{"on this machine it cannot serve here: no CUDA driver", "cannot serve here"},
		{"", ""},
		{"something the CLI said that names no build", ""},
	} {
		require.Equal(t, c.want, settingsHereWord(c.here), c.here)
	}
}

func TestTheServingSettingsAreAVisibleRowOfShare(t *testing.T) {
	f := &modelsFake{view: settingsView()}
	m := shareWithModels(t, f)
	out := stripANSI(m.shareView(130))
	require.Contains(t, out, "build: auto, CUDA here · idle: off · disk: no budget")
	// Under roger's own rows, and getting a model stays the last row.
	i, j, k := strings.Index(out, "ON DISK"), strings.Index(out, "build: auto"), strings.Index(out, "+ Get a model")
	require.True(t, i >= 0 && i < j && j < k, "settings are not between roger's rows and the last row:\n%s", out)
	require.Equal(t, 2+4, m.shareRowCount())

	// A real row the cursor reaches, with a detail that says what the choice means here.
	m = toSettings(t, m)
	out = stripANSI(m.shareView(130))
	require.Contains(t, out, "on this machine: the cuda build of llama.cpp b6000")
	require.Contains(t, out, "⏎ change build, idle or disk")

	// A budget says itself and what is on disk; a timeout says itself.
	f.view.Settings.DiskBudget, f.view.Settings.Idle, f.view.Settings.Runtime = 20<<30, "30m0s", "cpu"
	f.view.Settings.Here = "on this machine: the cpu build of llama.cpp b6000 (20.0 MiB, installed)"
	m = feed(t, m, m.modelsLoad())
	require.Contains(t, stripANSI(m.shareView(130)), "build: cpu, CPU here · idle: 30m0s · disk: 20.0 GiB budget, 2.0 GiB used")

	// Before the settings are read, the row still shows, without inventing a value.
	f.view.Settings = modelview.ServingSettings{}
	m = feed(t, m, m.modelsLoad())
	require.Contains(t, stripANSI(m.shareView(130)), "build: - · idle: - · disk: no budget")
}

func TestTheBuildIsChangedFromTheSettingsLine(t *testing.T) {
	f := &modelsFake{view: settingsView()}
	m := toSettings(t, shareWithModels(t, f))
	m, _ = sKey(t, m, "enter")
	out := stripANSI(m.shareView(130))
	require.Contains(t, out, "CHANGE A SERVING SETTING")
	require.Contains(t, out, "[build: auto]", "the chosen setting is marked without colour")
	require.Contains(t, out, "idle: off")
	require.Contains(t, out, "disk: no budget")

	m, _ = sKey(t, m, "enter") // build
	out = stripANSI(m.shareView(130))
	require.Contains(t, out, "BUILD")
	require.Contains(t, out, "[auto]  cpu  gpu  vulkan  cuda", "the current choice starts selected")
	m, _ = sKey(t, m, "right", "right", "right", "right", "right") // wraps round to auto again
	require.Contains(t, stripANSI(m.shareView(130)), "[auto]")
	m, _ = sKey(t, m, "left") // and back the other way, to cuda
	require.Contains(t, stripANSI(m.shareView(130)), "[cuda]")
	m, cmd := sKey(t, m, "enter")
	require.NotNil(t, cmd)
	require.Nil(t, m.mdl.set, "applying closes the chooser")
	nm, _, ok := m.onModelsMsg(cmd())
	require.True(t, ok)
	require.Equal(t, []string{"runtime|cuda"}, f.sets)
	require.Contains(t, stripANSI(nm.status), "runtime is now cuda - it applies from the next serve")
	require.Equal(t, modeShare, nm.mode)
}

func TestIdleAndDiskTakeTypedInputAndARefusalIsShown(t *testing.T) {
	f := &modelsFake{view: settingsView()}
	m := toSettings(t, shareWithModels(t, f))
	m, _ = sKey(t, m, "enter", "right", "enter") // idle
	out := stripANSI(m.shareView(130))
	require.Contains(t, out, "IDLE")
	require.Contains(t, out, "off, or a duration like 30m or 2h")
	m, _ = sKey(t, m, "3", "0", "x", "backspace", "m")
	require.Contains(t, stripANSI(m.shareView(130)), "> 30m")
	m, cmd := sKey(t, m, "enter")
	nm, _, _ := m.onModelsMsg(cmd())
	require.Equal(t, []string{"idle|30m"}, f.sets)
	require.Contains(t, stripANSI(nm.status), "idle is now 30m")

	// Digits typed into the budget are the budget, never a jump to another screen.
	m = nm
	m, _ = sKey(t, m, "enter", "down", "down", "enter") // disk
	out = stripANSI(m.shareView(130))
	require.Contains(t, out, "DISK")
	require.Contains(t, out, "off, or a size like 20G")
	m, cmd = sKey(t, m, "2", "0", "G", "enter")
	require.Equal(t, modeShare, m.mode)
	nm, _, _ = m.onModelsMsg(cmd())
	require.Equal(t, []string{"idle|30m", "budget|20G"}, f.sets)
	require.Contains(t, stripANSI(nm.status), "budget is now 20G")

	// A refusal is the CLI's own words, shown as a refusal.
	m, cmd = sKey(t, nm, "enter", "up", "enter", "l", "o", "t", "s", "enter")
	nm, _, _ = m.onModelsMsg(cmd())
	require.Equal(t, "budget|lots", f.sets[len(f.sets)-1])
	require.Contains(t, stripANSI(nm.status), `"lots" is not a size`)

	// Enter on an empty value changes nothing.
	m, cmd = sKey(t, nm, "enter", "right", "enter", "enter")
	require.Nil(t, cmd)
	require.Len(t, f.sets, 3)
	require.Nil(t, m.mdl.set)
}

func TestEscBacksOutOfTheSettingsChooserWithoutChange(t *testing.T) {
	f := &modelsFake{view: settingsView()}
	m := toSettings(t, shareWithModels(t, f))
	m, _ = sKey(t, m, "enter", "enter", "right", "esc") // out of the build values, back to the pick
	require.NotNil(t, m.mdl.set)
	require.Contains(t, stripANSI(m.shareView(130)), "CHANGE A SERVING SETTING")
	m, _ = sKey(t, m, "right", "enter", "4", "esc") // out of a typed idle
	require.Contains(t, stripANSI(m.shareView(130)), "CHANGE A SERVING SETTING")
	m, _ = sKey(t, m, "esc") // and out of the chooser, still on SHARE, on the same row
	require.Nil(t, m.mdl.set)
	require.Equal(t, modeShare, m.mode)
	e, ok := m.shareExtraAt(m.shareCursor)
	require.True(t, ok)
	require.Equal(t, extraSettings, e.kind)
	require.Empty(t, f.sets)
	require.NotContains(t, stripANSI(m.shareView(130)), "CHANGE A SERVING SETTING")
}

func TestSettingsWithoutASetHookShowButSayTheTerminalChangesThem(t *testing.T) {
	f := &modelsFake{view: settingsView()}
	h := f.hooks()
	h.Set = nil
	var tm tea.Model = NewWithHooks("http://b", "t", nil, Hooks{Models: h})
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 130, Height: 44})
	m := tm.(model)
	m.mode = modeShare
	m = feed(t, m, m.modelsLoad())
	m = toSettings(t, m)
	out := stripANSI(m.shareView(130))
	require.Contains(t, out, "build: auto, CUDA here")
	require.Contains(t, out, "`roger model runtime|idle|budget` in a terminal")
	m, cmd := sKey(t, m, "enter")
	require.Nil(t, cmd)
	require.Nil(t, m.mdl.set)
	require.Contains(t, stripANSI(m.status), "`roger model runtime|idle|budget` in a terminal")
}

func TestRogersServedRowsSayTheirBuildAndMeasuredSpeed(t *testing.T) {
	for _, c := range []struct {
		name     string
		sv       modelview.Serving
		want     string
		mustNot  string
		wantNone bool
	}{
		{name: "measured", sv: modelview.Serving{Build: "cuda", Measured: 63.71}, want: "CUDA · 63.7 tok/s measured", mustNot: "estimate"},
		{name: "estimate before it answers", sv: modelview.Serving{Build: "vulkan", Estimate: "~45 tok/s usable (estimate)"},
			want: "Vulkan · ~45 tok/s usable (estimate)", mustNot: "measured"},
		{name: "a measurement replaces an estimate", sv: modelview.Serving{Build: "cpu", Measured: 12, Estimate: "~45 tok/s usable (estimate)"},
			want: "CPU · 12.0 tok/s measured", mustNot: "estimate"},
		{name: "metal", sv: modelview.Serving{Build: "metal"}, want: "Metal"},
		{name: "an unknown build is said as given", sv: modelview.Serving{Build: "rocm"}, want: "rocm"},
		{name: "nothing known adds nothing", sv: modelview.Serving{}, wantNone: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := servingFacts(c.sv)
			if c.wantNone {
				require.Empty(t, got)
				return
			}
			require.Equal(t, c.want, got)
			if c.mustNot != "" {
				require.NotContains(t, got, c.mustNot)
			}
		})
	}

	// In SHARE's detail for the served row...
	f := &modelsFake{view: settingsView()}
	f.view.Serving = &modelview.Serving{Band: "tiny", ID: "acme/Tiny-GGUF:Q4_K_M", Quant: "Q4_K_M", Ctx: 8192,
		Address: "http://127.0.0.1:36277/v1", Build: "cuda", Measured: 63.7}
	f.view.Models[0].Serving = true
	m := shareWithModels(t, f)
	m.shareCursor = 1
	require.Contains(t, stripANSI(m.shareView(130)), "served by roger itself · CUDA · 63.7 tok/s measured · acme/Tiny-GGUF:Q4_K_M · Q4_K_M · 8192 context")

	// ...and on the MODELS screen, for the one on the air and each beside it.
	f.view.Also = []modelview.Serving{{Band: "mine", ID: "mine-Q8_0", Quant: "Q8_0", Ctx: 4096, Build: "cpu",
		Estimate: "~9 tok/s slow (estimate)"}}
	mm := modelsModel(t, f)
	out := screen(mm)
	require.Contains(t, out, "▶ ON THE AIR  \"mine\" · CPU · ~9 tok/s slow (estimate) · mine-Q8_0")
	require.Contains(t, out, "AND         \"tiny\" · CUDA · 63.7 tok/s measured · acme/Tiny-GGUF:Q4_K_M")
}

func TestSettingsAndServedFactsDegradeAt80ColumnsAndWithoutColour(t *testing.T) {
	f := &modelsFake{view: settingsView()}
	f.view.Settings.DiskBudget = 120 << 30
	f.view.Settings.Idle = "1h30m0s"
	f.view.Serving = &modelview.Serving{Band: "tiny", ID: "someone-with-a-long-name/An-Extremely-Long-Repository-Name-GGUF:Q4_K_M",
		Quant: "Q4_K_M", Ctx: 8192, Address: "http://127.0.0.1:36277/v1", Build: "vulkan", Measured: 538.2}
	f.view.Models[0].Serving = true
	m := shareWithModels(t, f)
	rows := m.shareRows
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m = tm.(model)
	m.shareRows = rows
	m.shareCursor = 1 // the row roger serves: its detail scrolls inside 80 columns, never past them
	for i := 0; i < 40; i++ {
		m.frame++
		for _, ln := range strings.Split(m.shareView(80), "\n") {
			require.LessOrEqual(t, lipgloss.Width(ln), 80, "the served row's detail overflows: %q", stripANSI(ln))
		}
	}
	m.frame = 0 // the marquee at rest: the build and the measured speed lead the detail
	require.Contains(t, stripANSI(m.shareView(80)), "served by roger itself · Vulkan · 538.2 tok/s measured")
	m = toSettings(t, m)
	check := func(view string) string {
		t.Helper()
		for _, ln := range strings.Split(view, "\n") {
			require.LessOrEqual(t, lipgloss.Width(ln), 80, "a line overflows 80 columns: %q", stripANSI(ln))
		}
		return stripANSI(view)
	}
	out := check(m.shareView(80))
	require.Contains(t, out, "build: auto, CUDA here · idle: 1h30m0s")
	require.Contains(t, out, "…", "a settings line cut at 80 columns says it was cut")
	m, _ = sKey(t, m, "enter")
	out = check(m.shareView(80))
	require.Contains(t, out, "[build: auto]")
	m, _ = sKey(t, m, "enter")
	out = check(m.shareView(80))
	require.Contains(t, out, "[auto]")
	m, _ = sKey(t, m, "esc", "right", "right", "enter")
	out = check(m.shareView(80))
	require.Contains(t, out, "DISK")

	// MODELS at 80: the build and the measured speed come before anything that may be cut.
	mm := modelsModel(t, f)
	tm, _ = mm.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	mm = tm.(model)
	out = check(mm.modelsView(80))
	require.Contains(t, out, "Vulkan · 538.2 tok/s measured")
}
