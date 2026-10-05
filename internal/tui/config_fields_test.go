package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/harness"
)

// configModel is a [3] CONFIG screen with one band row, its quant rows on the dial, and a
// store that records saves.
func configModel(t *testing.T) (*model, *int) {
	t.Helper()
	saves := 0
	m := New("http://broker.local", "tester")
	m.width, m.height = 120, 60
	m.limits = &LimitStore{Models: map[string]Limit{}, Save: func(map[string]Limit, Limit) { saves++ }}
	m.bands = []band{{model: "q", quant: "Q8_0"}, {model: "q", quant: "Q4_K_M"}, {model: "q", quant: "q8_0"}}
	m.enterLimits()
	return &m, &saves
}

func cfgKey(m *model, k string) {
	var msg tea.KeyMsg
	switch k {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		msg = tea.KeyMsg{Type: tea.KeyShiftTab}
	case "backspace":
		msg = tea.KeyMsg{Type: tea.KeyBackspace}
	case "space":
		msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
	out, _ := m.onLimitsKey(msg)
	*m = asModel(out)
}

func TestFieldValuesRoundTrip(t *testing.T) {
	l := Limit{MaxOut: 2, MinTPS: 20, MaxIn: 0.5, MaxCost: 0.02, ParamsB: []float64{7, 70}, MinCtx: 32768,
		MaxTTFTMs: 1500, Region: []string{"eu", "us"}, Pref: "fast", TrustMin: "verified", SelfHosted: true,
		Require: []string{"tools"}, Quants: []string{"Q8_0"}}
	for f, want := range map[int]string{lfMaxOut: "2", lfMinTPS: "20", lfMaxIn: "0.5", lfMaxCost: "0.02", lfParams: "7-70",
		lfMinCtx: "32768", lfMaxTTFT: "1500", lfRegion: "eu,us", lfPref: ""} {
		require.Equal(t, want, fieldBuf(l, f), limFieldDefs[f].label)
	}
	for f, want := range map[int]string{lfMaxOut: "out ≤ $2.00/1M", lfMinTPS: "≥20 t/s", lfMaxIn: "in ≤ $0.50/1M",
		lfMaxCost: "≤ $0.02/req", lfQuant: "Q8_0", lfPref: "fast", lfRequire: "tools", lfParams: "7-70B",
		lfMinCtx: "ctx ≥ 32k", lfMaxTTFT: "ttft ≤ 1.5s", lfTrust: "verified", lfSelfHosted: "self-hosted", lfRegion: "eu,us"} {
		require.Equal(t, want, fieldShown(l, f), limFieldDefs[f].label)
	}
	empty := Limit{MinCtx: 1000, MaxTTFTMs: 800}
	require.Equal(t, "ctx ≥ 1000", fieldShown(empty, lfMinCtx))
	require.Equal(t, "ttft ≤ 800ms", fieldShown(empty, lfMaxTTFT))
	for f, want := range map[int]string{lfMaxOut: "-", lfQuant: "any", lfPref: "balanced", lfTrust: "any", lfSelfHosted: "no",
		lfRequire: "-", lfParams: "-", lfRegion: "-", lfMaxCost: "-"} {
		require.Equal(t, want, fieldShown(Limit{}, f), limFieldDefs[f].label)
	}

	got, err := applyField(Limit{}, lfRegion, " eu , eu ,us,")
	require.NoError(t, err)
	require.Equal(t, []string{"eu", "us"}, got.Region)
	for f, bad := range map[int]string{lfMaxOut: "x", lfMinTPS: "-1", lfParams: "70-7", lfMinCtx: "32kb", lfMaxTTFT: "soon", lfRegion: "EU"} {
		_, err := applyField(Limit{}, f, bad)
		require.Error(t, err, limFieldDefs[f].label)
	}
	same, err := applyField(l, lfMaxOut, "  ")
	require.NoError(t, err)
	require.Equal(t, l, same, "an empty value changes nothing")
}

func TestConfigEditorKeys(t *testing.T) {
	m, saves := configModel(t)
	require.Equal(t, []string{"q", defaultLimitRow}, m.limModels)
	require.Equal(t, "max $/1M out", m.limFocusLabel())

	// choices: quant offers the labels on air once each (case-folded), then any again
	for _, f := range []int{lfQuant} {
		m.limField = f
	}
	require.Equal(t, []string{"any", "Q8_0", "Q4_K_M"}, m.quantChoices("q"))
	cfgKey(m, "space")
	require.Equal(t, []string{"Q8_0"}, m.rowLimit("q").Quants)
	cfgKey(m, "space")
	cfgKey(m, "space")
	require.Nil(t, m.rowLimit("q").Quants, "the cycle returns to any")
	require.Contains(t, strings.Join(m.limPlate("q", 120), "\n"), "space cycle")

	// require toggles; self-hosted and trust cycle
	m.limField = lfRequire
	cfgKey(m, "v")
	cfgKey(m, "t")
	require.Equal(t, []string{"tools", "vision"}, m.rowLimit("q").Require)
	require.Contains(t, strings.Join(m.limPlate("q", 120), "\n"), "t tools · v vision")
	m.limField = lfSelfHosted
	cfgKey(m, "space")
	require.True(t, m.rowLimit("q").SelfHosted)
	m.limField = lfTrust
	require.Equal(t, []string{"any", "verified", "confidential"}, m.fieldChoices("q"))
	m.limField = lfPref
	require.Equal(t, []string{"balanced", "cheap", "fast", "reliable"}, m.fieldChoices("q"))
	m.limField = lfSelfHosted
	require.Equal(t, []string{"no", "self-hosted"}, m.fieldChoices("q"))

	// editing a choice field: space and enter cycle; t/v toggle require while editing
	m.limField = lfTrust
	cfgKey(m, "enter")
	cfgKey(m, "enter")
	require.Equal(t, "verified", m.rowLimit("q").TrustMin)
	cfgKey(m, "space")
	require.Equal(t, "confidential", m.rowLimit("q").TrustMin)
	cfgKey(m, "x") // ignored on a choice field
	cfgKey(m, "tab")
	require.Equal(t, "self-hosted", m.limFocusLabel())
	cfgKey(m, "shift+tab")
	cfgKey(m, "shift+tab")
	require.Equal(t, "max ttft", m.limFocusLabel())
	cfgKey(m, "esc")
	m.limField = lfRequire
	cfgKey(m, "enter")
	cfgKey(m, "v")
	require.Equal(t, []string{"tools"}, m.rowLimit("q").Require)
	cfgKey(m, "esc")

	// text field: backspace, an invalid value stays editing with the reason, a valid one saves
	m.limField = lfParams
	cfgKey(m, "enter")
	cfgKey(m, "enter")
	for _, r := range "70-7x" {
		cfgKey(m, string(r))
	}
	cfgKey(m, "backspace")
	cfgKey(m, "enter")
	require.Equal(t, lfParams, m.editField, "an invalid value stays on the plate")
	require.Contains(t, m.status, "min must be ≤ max")
	cfgKey(m, "tab") // a refused value does not move on either
	require.Equal(t, lfParams, m.editField)
	m.editBuf = "7-70"
	cfgKey(m, "enter")
	require.Equal(t, []float64{7, 70}, m.rowLimit("q").ParamsB)

	// number field: up nudges, backspace edits
	m.limField = lfMaxIn
	cfgKey(m, "enter")
	cfgKey(m, "up")
	cfgKey(m, "backspace")
	cfgKey(m, "enter")
	require.Equal(t, -1, m.editField)

	// the default row edits limits.default, and d clears it
	m.limCursor = 1
	cfgKey(m, "p")
	require.Equal(t, "cheap", m.limits.Default.Pref)
	cfgKey(m, "d")
	require.Equal(t, Limit{}, m.limits.Default)

	// a adds a row, esc cancels another
	cfgKey(m, "a")
	for _, r := range "new-model" {
		cfgKey(m, string(r))
	}
	cfgKey(m, "backspace")
	cfgKey(m, "enter")
	require.Contains(t, m.limModels, "new-mode")
	require.Equal(t, defaultLimitRow, m.limModels[len(m.limModels)-1], "the default row stays last")
	cfgKey(m, "a")
	cfgKey(m, "z")
	cfgKey(m, "esc")
	require.False(t, m.limAdding)
	cfgKey(m, "a")
	cfgKey(m, "enter")
	require.False(t, m.limAdding, "an empty name adds nothing")

	require.Positive(t, *saves)
	cfgKey(m, "ctrl+p")
	require.Contains(t, m.status, "perms")
	m.limOnBudget = true
	require.Equal(t, "", m.limFocusLabel())
}

func TestServedLineAndPrefGlyph(t *testing.T) {
	require.Equal(t, "", servedLine(harness.Served{Model: "q", Node: "n1"}, "q"), "a turn served as asked says nothing")
	require.Contains(t, servedLine(harness.Served{Model: "l", Node: "n2"}, "q"), "l · n2")
	require.Contains(t, servedLine(harness.Served{LockedUntil: "2026-10-05T10:00:00Z"}, "q"), "locked until")
	require.Contains(t, servedLine(harness.Served{LockedUntil: "soon"}, "q"), "locked until soon")
	require.Equal(t, "", prefGlyph(""))
	require.Contains(t, prefGlyph("reliable"), "◆")
}

func TestProfileHelpersWithoutAStore(t *testing.T) {
	m := model{}
	require.Empty(t, m.profiles().Names())
	require.Nil(t, m.profileBody(""))
	require.Nil(t, m.profileBody("nope"))
	require.Equal(t, "", m.nextProfile("x"))
	require.Equal(t, "-", profileSummary(nil))
	require.Equal(t, "", m.confirmRoutingLine())
	require.Equal(t, "", m.onProfileRow())
}

// TestEditingABandKeepsItsOwnRuleOnly: a band's rule inherits the default per key when it
// RESOLVES, but an edit writes back only what the band itself sets - the default's keys are
// never frozen into the band entry (so a later default edit still reaches the band).
func TestEditingABandKeepsItsOwnRuleOnly(t *testing.T) {
	m, _ := configModel(t)
	m.limits.Default = Limit{MinTPS: 10, Region: []string{"eu"}}
	m.limits.Models["q"] = Limit{MaxOut: 2}
	m.limCursor, m.editField, m.editBuf = 0, lfMaxOut, "3"
	require.True(t, m.commitLimitField())
	require.Equal(t, Limit{MaxOut: 3}, m.limits.Models["q"], "the plate edit")

	m.cfgModel = "q"
	m.saveQuantRule([]string{"Q8_0"})
	require.Equal(t, Limit{MaxOut: 3, Quants: []string{"Q8_0"}}, m.limits.Models["q"], "the quant picker")
	require.Equal(t, Limit{MaxOut: 3, MinTPS: 10, Region: []string{"eu"}, Quants: []string{"Q8_0"}}, m.limits.Resolve("q"),
		"and the band still resolves with the default's keys under its own")
}

// TestEmptyInputClearsATextField: params, min ctx, max ttft and region are cleared one at a
// time by committing an empty value; the other keys of the rule are untouched.
func TestEmptyInputClearsATextField(t *testing.T) {
	full := Limit{MaxOut: 2, ParamsB: []float64{7, 70}, MinCtx: 32768, MaxTTFTMs: 1500, Region: []string{"eu"}}
	for f, want := range map[int]Limit{
		lfParams:  {MaxOut: 2, MinCtx: 32768, MaxTTFTMs: 1500, Region: []string{"eu"}},
		lfMinCtx:  {MaxOut: 2, ParamsB: []float64{7, 70}, MaxTTFTMs: 1500, Region: []string{"eu"}},
		lfMaxTTFT: {MaxOut: 2, ParamsB: []float64{7, 70}, MinCtx: 32768, Region: []string{"eu"}},
		lfRegion:  {MaxOut: 2, ParamsB: []float64{7, 70}, MinCtx: 32768, MaxTTFTMs: 1500},
	} {
		got, err := applyField(full, f, "  ")
		require.NoError(t, err, limFieldDefs[f].label)
		require.Equal(t, want, got, limFieldDefs[f].label)

		// and through the editor: the cleared key is persisted
		m, _ := configModel(t)
		m.limits.Models["q"] = full
		m.limCursor, m.editField, m.editBuf = 0, f, ""
		require.True(t, m.commitLimitField())
		require.Equal(t, want, m.limits.Models["q"], limFieldDefs[f].label+" via the editor")
	}
}
