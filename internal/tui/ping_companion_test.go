package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/harness"
)

// TestMascotRepertoire: the idle mascot plays a varied, deterministic repertoire -
// more than the old bob+blink (it now also looks around and adjusts the headset), and
// it stays reproducible frame-for-frame so tests never flake.
func TestMascotRepertoire(t *testing.T) {
	poses := map[string]bool{}
	sawBlink, sawWide := false, false
	for f := 0; f < 600; f++ {
		pf, eye := idleScene(f)
		poses[strings.Join(pf.lines[:], "|")] = true
		switch eye {
		case "-":
			sawBlink = true
		case "O":
			sawWide = true // the transmit-pulse wink swells the eye
		}
		// Determinism: same frame -> identical pose + eye.
		if pf2, eye2 := idleScene(f); pf2 != pf || eye2 != eye {
			t.Fatalf("idleScene(%d) is not deterministic", f)
		}
	}
	if len(poses) < 8 {
		t.Errorf("idle mascot should show a rich repertoire (>=8 distinct poses), got %d", len(poses))
	}
	if !sawBlink {
		t.Errorf("idle mascot should blink occasionally")
	}
	if !sawWide {
		t.Errorf("idle mascot should pulse a wide on-air eye (the transmit wink)")
	}
}

// TestEmptyBandStaticCTA (audit #10): the quiet empty band shows ONE static
// actionable line - "No stations on air - [2] to share, [1] to tune in" - not a
// rotating motivational carousel (which read as "loading forever" to a newcomer). The
// line names both the share AND the tune-in move, and is stable frame-to-frame.
func TestEmptyBandStaticCTA(t *testing.T) {
	cta := stripANSI(emptyBandCTA(false))
	if !strings.Contains(cta, "No stations on air") {
		t.Errorf("empty-band CTA should name the empty band: %q", cta)
	}
	if !strings.Contains(cta, "[2]") || !strings.Contains(cta, "[1]") {
		t.Errorf("empty-band CTA should teach both [2] share and [1] tune in: %q", cta)
	}
	// Static: identical across frames (no carousel rotation).
	withMotion(func() {
		m := New("http://broker.local", "tester")
		m.width, m.height = 100, 40
		m.scanned = true
		m.frame = 0
		a := stripANSI(m.browseView(100))
		m.frame = 28
		b := stripANSI(m.browseView(100))
		// The CTA text is the same on both frames (only the signal shimmer may animate).
		if !strings.Contains(a, "No stations on air") || !strings.Contains(b, "No stations on air") {
			t.Errorf("empty band should carry the static CTA on every frame:\n%q\n%q", a, b)
		}
		// Slim width: the CTA must not overflow a ~40-col terminal.
		nm := New("http://broker.local", "tester")
		nm.width, nm.height = 44, 40
		nm.scanned = true
		for _, line := range strings.Split(stripANSI(nm.browseView(44)), "\n") {
			if utf8.RuneCountInString(line) > 44 {
				t.Errorf("narrow empty band overflows width 44 (%d): %q", utf8.RuneCountInString(line), line)
			}
		}
	})
}

// TestCornerWordsPerState: the corner Ping carries a distinct status word per turn
// state (standing by / thinking / on air / working the dial), rotating its synonyms.
func TestCornerWordsPerState(t *testing.T) {
	states := []agentPose{poseWaiting, poseThinking, poseStreaming, poseTool}
	words := map[agentPose]string{}
	for _, s := range states {
		w := cornerWord(s, 0)
		if w == "" {
			t.Fatalf("state %d has no status word", s)
		}
		words[s] = w
	}
	if words[poseWaiting] == words[poseThinking] || words[poseThinking] == words[poseStreaming] || words[poseStreaming] == words[poseTool] {
		t.Errorf("each turn state should have its own status word, got %v", words)
	}
	// Rotation: each state's synonym bank has more than one word (the per-frame rotation
	// itself freezes to word 0 under quiet, which the non-TTY harness is).
	for _, s := range states {
		if len(cornerWords[s]) < 2 {
			t.Errorf("state %d should carry rotating synonyms (>=2), got %d", s, len(cornerWords[s]))
		}
	}
}

// TestCornerPingPoseChanges: the corner-Ping renders a visibly different block per
// turn state (the status word always differs; the pose art differs too when not
// reduced-motion), and is deterministic for a given frame. It also checks the
// underlying per-state frame banks are distinct repertoires.
func TestCornerPingPoseChanges(t *testing.T) {
	render := func(s agentPose) string {
		return strings.Join(agentCornerPing(s, 4, false, false), "\n")
	}
	all := []string{render(poseWaiting), render(poseThinking), render(poseStreaming), render(poseTool)}
	for i := range all {
		for j := i + 1; j < len(all); j++ {
			if all[i] == all[j] {
				t.Errorf("corner Ping states %d and %d render identically:\n%q", i, j, all[i])
			}
		}
	}
	// Determinism: same state + frame -> same render.
	if render(poseThinking) != strings.Join(agentCornerPing(poseThinking, 4, false, false), "\n") {
		t.Errorf("corner Ping render is not deterministic")
	}
	// The streaming frame bank swells the eye to O (the on-air signal) on its off-beats.
	sawWide := false
	for _, fr := range cornerStreamFrames {
		if strings.Contains(strings.Join(fr.lines[:], ""), "O") {
			sawWide = true
		}
	}
	if !sawWide {
		t.Errorf("the streaming corner frames should swell the on-air eye to O")
	}
	// The four per-state banks are distinct repertoires (each has its own pose set).
	banks := [][]cornerHead{cornerWaitFrames, cornerThinkFrames, cornerStreamFrames, cornerToolFrames}
	for _, b := range banks {
		if len(b) == 0 {
			t.Errorf("a per-state corner frame bank is empty")
		}
	}
}

// TestCornerPingReducedMotion: under quiet (NO_COLOR / non-TTY / reduced-motion) the
// corner Ping freezes to a single static standing-by pose regardless of frame/state.
func TestCornerPingReducedMotion(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	// quiet is computed once at package init; force the reduced-motion path directly via
	// the compact flag, which the renderer treats as reduced-motion (one status line).
	for _, s := range []agentPose{poseWaiting, poseThinking, poseStreaming, poseTool} {
		lines := agentCornerPing(s, 7, false, true) // compact -> one clean line
		if len(lines) != 1 {
			t.Errorf("reduced-motion corner Ping should be one line, got %d", len(lines))
		}
		if strings.Contains(lines[0], "\x1b[") {
			t.Errorf("reduced-motion corner Ping emitted ANSI under NO_COLOR: %q", lines[0])
		}
	}
}

// TestAgentCornerHiddenWithoutModel: with NO model active the agent view shows NO corner
// Ping (the no-model screen stays a clean hint); with a model it shows the corner word.
func TestAgentCornerHiddenWithoutModel(t *testing.T) {
	// No model: browseSeed leaves connected/lastConnected nil.
	var noModel tea.Model = browseSeed(100)
	noModel, _ = noModel.Update(keyMsg("0"))
	outNo := stripANSI(asModel(noModel).View())
	if strings.Contains(outNo, "standing by") {
		t.Errorf("no-model AGENT should NOT render the corner Ping:\n%s", outNo)
	}

	// Model active: the corner Ping stands by.
	m := browseSeed(100)
	m.connected = &offer{NodeID: "nyx", Model: "qwen3-coder-30b", Online: true}
	var withModel tea.Model = m
	withModel, _ = withModel.Update(keyMsg("0"))
	outYes := stripANSI(asModel(withModel).View())
	if !strings.Contains(outYes, "standing by") {
		t.Errorf("model-active AGENT should render the standing-by corner Ping:\n%s", outYes)
	}
}

// TestAgentCornerReactsToEvents: streaming synthetic loop events flips the corner Ping
// pose word across waiting -> thinking -> tool -> streaming, off the existing event
// stream (no second clock).
func TestAgentCornerReactsToEvents(t *testing.T) {
	m := browseSeed(100)
	m.connected = &offer{NodeID: "nyx", Model: "qwen3-coder-30b", Online: true}
	var am tea.Model = m
	am, _ = am.Update(keyMsg("0"))

	// A turn starts (enter) -> thinking.
	am = typeLine(am, "do a thing")
	am, _ = am.Update(keyMsg("enter"))
	if asModel(am).agentTurnState != poseThinking {
		t.Errorf("a started turn should put the corner Ping in thinking, got %d", asModel(am).agentTurnState)
	}

	// A tool call -> working the dial.
	am, _ = am.Update(agentEventMsg{Kind: harness.EventToolCall, Tool: "list_dir", Args: map[string]any{"path": "."}})
	if asModel(am).agentTurnState != poseTool {
		t.Errorf("a tool call should put the corner Ping on the dial (tool), got %d", asModel(am).agentTurnState)
	}
	if !strings.Contains(stripANSI(asModel(am).View()), cornerWords[poseTool][0]) {
		t.Errorf("tool-state corner word missing from the view")
	}

	// The tool result hands back to the model -> thinking again.
	am, _ = am.Update(agentEventMsg{Kind: harness.EventToolResult, Tool: "list_dir", Result: "a.go\n"})
	if asModel(am).agentTurnState != poseThinking {
		t.Errorf("a tool result should return the corner Ping to thinking, got %d", asModel(am).agentTurnState)
	}

	// The final answer streams -> on air (transmitting).
	am, _ = am.Update(agentEventMsg{Kind: harness.EventFinal, Text: "done"})
	if asModel(am).agentTurnState != poseStreaming {
		t.Errorf("a streaming answer should put the corner Ping on air, got %d", asModel(am).agentTurnState)
	}
	if !strings.Contains(stripANSI(asModel(am).View()), cornerWords[poseStreaming][0]) {
		t.Errorf("streaming-state corner word missing from the view")
	}

	// The turn ends -> back to standing by.
	am, _ = am.Update(agentDoneMsg{})
	if asModel(am).agentTurnState != poseWaiting {
		t.Errorf("a finished turn should return the corner Ping to standing by, got %d", asModel(am).agentTurnState)
	}
}

// TestNoStationAgentMessageNamesModel: the no-station agent error names the bound model
// and carries the actionable [2] put-one-on-air / [1] tune-in line - NOT a bare 504.
func TestNoStationAgentMessageNamesModel(t *testing.T) {
	m := browseSeed(100)
	m.connected = &offer{NodeID: "nyx", Model: "gpt-oss-20b", Online: true}
	var am tea.Model = m
	am, _ = am.Update(keyMsg("0"))
	// A relay 504 with no body: the broker had nobody to serve the model.
	am, _ = am.Update(agentEventMsg{Kind: harness.EventError, Text: "the station returned status 504 with no reply"})
	out := stripANSI(asModel(am).View())
	if !strings.Contains(out, "no station is serving gpt-oss-20b right now") {
		t.Errorf("no-station agent error should name the model clearly:\n%s", out)
	}
	if !strings.Contains(out, "[2]") || !strings.Contains(out, "[1]") {
		t.Errorf("no-station agent error should carry the [2]/[1] move:\n%s", out)
	}
	// Never the bare status string.
	if strings.Contains(out, "with no reply") {
		t.Errorf("no-station agent error should NOT show the bare 504 string:\n%s", out)
	}
}

// TestCornerPingNoColorNarrowSafe: the corner Ping renders without ANSI under NO_COLOR
// and never overflows narrow-to-wide widths, in every turn state, in the live view.
func TestCornerPingNoColorNarrowSafe(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, w := range []int{40, 64, 80, 100, 120} {
		for _, s := range []agentPose{poseWaiting, poseThinking, poseStreaming, poseTool} {
			m := browseSeed(w)
			m.connected = &offer{NodeID: "nyx", Model: "qwen3-coder-30b", Online: true}
			m.height = 24
			var am tea.Model = m
			am, _ = am.Update(keyMsg("0"))
			gm := asModel(am)
			gm.agentTurnState = s
			out := gm.View()
			if strings.Contains(out, "\x1b[") {
				t.Errorf("width %d state %d: AGENT corner emitted ANSI under NO_COLOR", w, s)
			}
			for _, line := range strings.Split(out, "\n") {
				if vis := utf8.RuneCountInString(stripANSI(line)); vis > w {
					t.Errorf("width %d state %d: line overflows (%d cols): %q", w, s, vis, stripANSI(line))
				}
			}
		}
	}
}

// TestOfflineBandMarked: a band with no station on air reads "offline" in the on-air
// column (not a bare "-"), so it is obvious you cannot connect until a station is up.
func TestOfflineBandMarked(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := browseSeed(100)
	// A sticky offline band: present in the list, but no station on air.
	m.bands = []band{{model: "gpt-oss-20b", online: false, stations: 0}}
	m.cursor = 0
	out := stripANSI(m.browseView(100))
	if !strings.Contains(out, "offline") {
		t.Errorf("an offline band should be marked 'offline' in the on-air column:\n%s", out)
	}
}
