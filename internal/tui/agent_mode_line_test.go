package tui

// Increment 1 of the TUI design overhaul: the persistent, NEVER-EMPTY `TOOLS:
// <MODE>` control-panel line under the AGENT input, plus a `STANDBY N` chip when
// asks are queued. The founder's original complaint ("did /perms toggle?") - at
// the confirm default the masthead chip was empty, so a permissive session (or a
// reverted one) was invisible. The mode now shows ALWAYS, colored by the
// increment-0 lamps (dim confirm / amber auto-edits / red auto-all), living on the
// control panel directly under the prompt where the eye rests to act.
//
// Spec approved 2026-07-15 (increment 1). No mocks: a real seeded model rendered
// through the real agentView, real lamp tokens via the live renderer profile.
//
// agentPermTag() and its committed test are deliberately left UNTOUCHED (this is a
// new line, not a change to that function) - see R2 / agent_perms_test.go.

import (
	"strings"
	"testing"
)

// Dark-mode truecolor RGB of the increment-0 lamps (colorOn sets a dark bg, so the
// AdaptiveColor Dark arm is emitted). Used to prove the mode line is colored, and
// that the wrong lamp never leaks.
const (
	rgbAmber = "245;166;35"  // lamp(roleDialGlow) dark #F5A623 - auto-edits
	rgbLive  = "255;86;54"   // lamp(roleLive) dark #FF5636 - auto-all (also the ▌ bar)
	rgbDial  = "126;166;216" // lamp(roleDial) dark #7EA6D8 - STANDBY
)

// agentAt seeds a real model tuned into a model in AGENT mode, with the given
// approval mode set. Width 120 (wide, non-narrow), the same rig agent_perms_test uses.
func agentAt(t *testing.T, mode agentPermMode) model {
	t.Helper()
	base := browseSeed(120)
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	nm, _ := base.enterAgent()
	m := asModel(nm)
	m.agent.perms.Store(int32(mode))
	return m
}

// lineWith returns the first rendered line containing sub (ANSI intact), so a color
// assertion is scoped to the mode line and not fooled by the red ▌ masthead bar.
func lineWith(view, sub string) string {
	for _, ln := range strings.Split(view, "\n") {
		if strings.Contains(stripANSI(ln), sub) {
			return ln
		}
	}
	return ""
}

// M1-M3 - the mode line is ALWAYS present and colored by mode.
func TestModeLineNeverEmptyByMode(t *testing.T) {
	colorOn(t, true)
	cases := []struct {
		mode      agentPermMode
		label     string
		wantRGB   string // the lamp that MUST light (or "" for dim confirm)
		bannedRGB []string
	}{
		{permConfirm, "CONFIRM", "", []string{rgbAmber, rgbLive}}, // M1: dim, no amber/red
		{permEdits, "AUTO-EDITS", rgbAmber, []string{rgbLive}},    // M2: amber
		{permAll, "AUTO-ALL", rgbLive, []string{rgbAmber}},        // M3: red
	}
	for _, c := range cases {
		view := agentAt(t, c.mode).agentView(120)
		line := lineWith(view, "TOOLS:")
		if line == "" {
			t.Fatalf("%v: no `TOOLS:` mode line rendered at all (the founder's bug)", c.mode)
		}
		if !strings.Contains(stripANSI(line), c.label) {
			t.Errorf("%v: mode line missing %q: %q", c.mode, c.label, stripANSI(line))
		}
		if c.wantRGB != "" && !strings.Contains(line, c.wantRGB) {
			t.Errorf("%v: mode line should light %s (%s)", c.mode, c.label, c.wantRGB)
		}
		for _, b := range c.bannedRGB {
			if strings.Contains(line, b) {
				t.Errorf("%v: mode line leaked the wrong lamp %s", c.mode, b)
			}
		}
	}
}

// M4 - the mode line sits on the control panel: AFTER the `ask ›` input line.
func TestModeLineUnderInput(t *testing.T) {
	view := stripANSI(agentAt(t, permConfirm).agentView(120))
	ask := strings.Index(view, "ask ›")
	tools := strings.Index(view, "TOOLS:")
	if ask < 0 || tools < 0 {
		t.Fatalf("need both the prompt and the mode line: ask=%d tools=%d", ask, tools)
	}
	if tools < ask {
		t.Errorf("mode line must be UNDER the input (ask=%d, TOOLS=%d)", ask, tools)
	}
}

// M5 - never empty even pre-connect: with no agent the line still reads CONFIRM
// (the default gate), so the readout is never blank.
func TestModeLineNoAgentShowsConfirm(t *testing.T) {
	m := agentAt(t, permConfirm)
	m.agent = nil // simulate the no-agent / pre-connect AGENT screen (agentIn stays valid)
	view := stripANSI(m.agentView(120))
	line := lineWith(view, "TOOLS:")
	if line == "" || !strings.Contains(line, "CONFIRM") {
		t.Errorf("no-agent screen must still show `TOOLS: CONFIRM`, got %q", line)
	}
}

// S1-S3 - the STANDBY chip surfaces the queue depth, blue, and is ABSENT at zero.
func TestModeLineStandbyChip(t *testing.T) {
	colorOn(t, true)

	// S1 - nothing queued: no STANDBY token.
	m := agentAt(t, permConfirm)
	if strings.Contains(stripANSI(m.agentView(120)), "STANDBY") {
		t.Error("S1: no STANDBY chip when the queue is empty")
	}

	// S2 - one queued: `STANDBY 1`, blue.
	m = agentAt(t, permConfirm)
	m.agentQueued = []queuedPrompt{{text: "one"}}
	line := lineWith(m.agentView(120), "TOOLS:")
	if !strings.Contains(stripANSI(line), "STANDBY 1") {
		t.Errorf("S2: want `STANDBY 1`, got %q", stripANSI(line))
	}
	if !strings.Contains(line, rgbDial) {
		t.Error("S2: STANDBY chip should be the dial blue")
	}

	// S3 - three queued: the exact count.
	m = agentAt(t, permConfirm)
	m.agentQueued = []queuedPrompt{{text: "a"}, {text: "b"}, {text: "c"}}
	if !strings.Contains(stripANSI(m.agentView(120)), "STANDBY 3") {
		t.Errorf("S3: want `STANDBY 3`, got %q", stripANSI(lineWith(m.agentView(120), "TOOLS:")))
	}
}

// X1 - mono palette collapses the amber auto-edits chip to dim (no amber hue),
// riding the increment-0 switch; the mode TEXT still reads.
func TestModeLineMonoCollapse(t *testing.T) {
	colorOn(t, true)
	restore := paletteMono
	t.Cleanup(func() { paletteMono = restore })
	paletteMono = true

	line := lineWith(agentAt(t, permEdits).agentView(120), "TOOLS:")
	if strings.Contains(line, rgbAmber) {
		t.Error("mono: the auto-edits chip must not emit the amber lamp hue")
	}
	if !strings.Contains(stripANSI(line), "AUTO-EDITS") {
		t.Error("mono: the mode text must still read")
	}
}

// X2 - with color OFF (NO_COLOR / non-TTY, the default test renderer) the mode line
// still renders its text: the readout is legible even with every style stripped.
func TestModeLineTextWithoutColor(t *testing.T) {
	view := stripANSI(agentAt(t, permAll).agentView(120))
	if !strings.Contains(view, "TOOLS:") || !strings.Contains(view, "AUTO-ALL") {
		t.Errorf("mode text must survive with color stripped, got:\n%s", view)
	}
}

// R1 - no duplication: perms now live ONLY on the control line, so the full
// masthead (line 1) no longer carries the perm chip.
func TestFullMastheadHasNoPermChip(t *testing.T) {
	view := agentAt(t, permAll).agentView(120)
	masthead := stripANSI(strings.SplitN(view, "\n", 2)[0])
	if strings.Contains(masthead, "AUTO-ALL") {
		t.Errorf("the full masthead must not repeat the perm chip (it's on the control line now): %q", masthead)
	}
}
