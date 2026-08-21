package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// wave_deck_test.go - the 2026-08-20 AGENT deck revamp, locked.
//
// Four founder asks, each with a failure mode that is invisible in code review and
// obvious on screen: the composer moving under the user, the working readout landing
// in the wrong place, a shortcut that overflows the footer, and the Wave Spectrum
// drifting out of step with the website's own ladder.

// agentDeckSeed builds a measured AGENT frame - measured because the bottom pin and
// the readout slot are both no-ops without a real terminal height.
func agentDeckSeed(t *testing.T, w, h int, busy bool) model {
	t.Helper()
	m := browseSeed(w)
	m.width, m.height = w, h
	m.mode = modeAgent
	m.connected = &offer{NodeID: "station", Model: "model", Online: true}
	m.agent = m.newAgentRuntime()
	m.agentLines = []string{"  you ▸ hi", "  ◂ hello there"}
	if busy {
		m.agentBusy = true
		m.agentStart = time.Now().Add(-22 * time.Second)
		m.agentLastEvent = time.Now()
		m.agentTurnState = poseStreaming
	}
	return m
}

func rowOf(lines []string, needle string) int {
	for i, l := range lines {
		if strings.Contains(l, needle) {
			return i
		}
	}
	return -1
}

// The composer must not move when a turn starts. This is the whole point of the
// permanent readout slot: the founder watched the input hop a row on every single
// turn, because the working line was rendered above it and sized to the live state.
func TestAgentComposerDoesNotMoveWhenTurnStarts(t *testing.T) {
	for _, w := range []int{80, 120} {
		idle := strings.Split(stripANSI(agentDeckSeed(t, w, 24, false).View()), "\n")
		busy := strings.Split(stripANSI(agentDeckSeed(t, w, 24, true).View()), "\n")
		iRow, bRow := rowOf(idle, "ask ›"), rowOf(busy, "ask ›")
		if iRow < 0 || bRow < 0 {
			t.Fatalf("width %d: no composer row (idle=%d busy=%d)", w, iRow, bRow)
		}
		if iRow != bRow {
			t.Errorf("width %d: the composer moved when the turn started (idle row %d, busy row %d)", w, iRow, bRow)
		}
		if len(idle) != len(busy) {
			t.Errorf("width %d: frame height changed with the turn (%d vs %d)", w, len(idle), len(busy))
		}
	}
}

// The working readout goes BELOW the ask, never above it (founder: "i want the
// working wave to appear below the ask › area").
func TestAgentWorkingReadoutSitsBelowTheAsk(t *testing.T) {
	lines := strings.Split(stripANSI(agentDeckSeed(t, 120, 24, true).View()), "\n")
	ask := rowOf(lines, "ask ›")
	work := -1
	for i, l := range lines {
		if strings.Contains(l, "receiving…") || strings.Contains(l, "working…") {
			work = i
		}
	}
	if ask < 0 || work < 0 {
		t.Fatalf("expected both a composer and a working readout (ask=%d work=%d)", ask, work)
	}
	if work <= ask {
		t.Errorf("the working readout must sit below the ask (ask row %d, readout row %d)", ask, work)
	}
	if tools := rowOf(lines, "TOOLS:"); tools >= 0 && work >= tools {
		t.Errorf("the working readout belongs between the ask and TOOLS: (readout %d, TOOLS %d)", work, tools)
	}
}

// The ask sits on the floor of the frame with only the helper rows beneath it, and
// the slack lands ABOVE it - not below the footer, where it used to go.
func TestAgentComposerIsPinnedToTheBottom(t *testing.T) {
	lines := strings.Split(stripANSI(agentDeckSeed(t, 120, 30, false).View()), "\n")
	ask := rowOf(lines, "ask ›")
	if ask < 0 {
		t.Fatal("no composer row")
	}
	// Everything under the composer is helper chrome: the readout slot, TOOLS:, the
	// rule, the key line, the status line. Six rows is the whole of it.
	if below := len(lines) - 1 - ask; below > 7 {
		t.Errorf("the composer is not on the floor: %d rows below it", below)
	}
	if rowOf(lines, "TOOLS:") < ask {
		t.Error("TOOLS: must stay under the composer, not above it")
	}
	// The pin marker itself must never reach a terminal.
	if strings.Contains(strings.Join(lines, "\n"), "rogerai-pin") {
		t.Error("the pin sentinel leaked into the rendered frame")
	}
}

// A headless render (no WindowSizeMsg) has no floor to pin to and must stay exactly
// as it was - this is what keeps the rest of the suite's golden frames honest.
func TestAgentPinIsInertWithoutAMeasuredHeight(t *testing.T) {
	m := browseSeed(80)
	m.width, m.height = 80, 0
	m.mode = modeAgent
	m.connected = &offer{NodeID: "station", Model: "model", Online: true}
	m.agent = m.newAgentRuntime()
	out := m.View()
	if strings.Contains(out, "rogerai-pin") {
		t.Error("the pin sentinel leaked into a headless frame")
	}
	if strings.Contains(stripANSI(out), "\n\n\n\n") {
		t.Error("a headless frame must not gain pin padding")
	}
}

// The AGENT footer must fit its terminal at every width - the fit ladder replaced
// hard-coded cut-offs precisely because those kept going stale by a cell or two.
func TestAgentFooterFitsEveryWidth(t *testing.T) {
	for w := 40; w <= 200; w += 2 {
		m := agentDeckSeed(t, w, 24, false)
		for _, l := range strings.Split(stripANSI(m.footer(w)), "\n") {
			if got := len([]rune(l)); got > w {
				t.Errorf("width %d: footer line overflows (%d cells): %q", w, got, l)
			}
		}
	}
}

// ⌃w is taught wherever the footer has room for it. A shortcut nobody is told about
// does not exist.
func TestAgentFooterTeachesTheConsoleKey(t *testing.T) {
	for _, w := range []int{60, 80, 100, 120, 160} {
		f := stripANSI(agentDeckSeed(t, w, 24, false).footer(w))
		if !strings.Contains(f, "⌃w") {
			t.Errorf("width %d: the footer must teach ⌃w: %q", w, f)
		}
	}
}

// THE WAVE SPECTRUM. These seven hues are the founder's own ladder, and they are the
// SAME seven the website paints (web/src/styles/base.css --tier-*). If the site's
// palette moves and this does not, the terminal and the browser stop being one
// product - so the values are pinned here, in ladder order, with their names.
func TestWaveSpectrumMatchesTheSiteLadder(t *testing.T) {
	want := []struct{ name, light, dark string }{
		{"Pico", "#b23a2a", "#e6604f"},
		{"Nano", "#c96a1c", "#e88b3c"},
		{"Micro", "#b0891a", "#d4aa2e"},
		{"Giga", "#2f8a52", "#48b873"},
		{"Tera", "#1f8f8f", "#39b7b7"},
		{"Peta", "#2f63bf", "#5b8ee6"},
		{"Exa", "#5b3fbf", "#8a6df0"},
	}
	if len(waveSpectrum) != len(want) || len(waveTierNames) != len(want) {
		t.Fatalf("the Spectrum is seven tiers: got %d hues, %d names", len(waveSpectrum), len(waveTierNames))
	}
	for i, w := range want {
		if waveTierNames[i] != w.name {
			t.Errorf("tier %d: name %q, want %q (ladder order is load-bearing)", i, waveTierNames[i], w.name)
		}
		if got := waveSpectrum[i].Light; got != w.light {
			t.Errorf("%s light = %s, want %s (must match the site's --tier-%s)", w.name, got, w.light, strings.ToLower(w.name))
		}
		if got := waveSpectrum[i].Dark; got != w.dark {
			t.Errorf("%s dark = %s, want %s (must match the site's dark --tier-%s)", w.name, got, w.dark, strings.ToLower(w.name))
		}
	}
}

// ── THE TOOL-MACHINERY FOLD ──────────────────────────────────────────────────
// Founder 2026-08-20, with a screenshot of a turn whose ⚙/✓ chatter had pushed the
// answer off the screen: "i want this to be hidden ... lets hide a lot of the extra
// noise and keep it clean".

// foldSeed builds a transcript with a run of tool cards around some prose.
func foldSeed(t *testing.T) model {
	t.Helper()
	m := browseSeed(110)
	m.width, m.height = 110, 30
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	card := func(tool, arg string) []string {
		return []string{
			toolCardMark + agentToolCallLine(tool, arg),
			toolCardMark + stLive.Render("  ✓ ") + stDim.Render(tool) + stDim.Render("   "+arg) + stDim.Render(" · ok · 51 bytes"),
		}
	}
	var ls []string
	ls = append(ls, card("read_file", "settings.yaml")...)
	ls = append(ls, card("web_search", "dsh local models")...)
	ls = append(ls, card("run_shell", "echo ports")...)
	ls = append(ls, "  Endpoints are live.")
	m.agentLines = ls
	return m
}

func TestToolMachineryFoldsByDefault(t *testing.T) {
	m := foldSeed(t)
	if m.showToolCalls {
		t.Fatal("the machinery must start folded - that is the whole ask")
	}
	got := m.displayAgentLines(110)
	if len(got) != 2 {
		t.Fatalf("six cards + one prose line should fold to 2 rows, got %d:\n%s", len(got), strings.Join(got, "\n"))
	}
	flat := stripANSI(got[0])
	// Counts RESULTS, not cards: a call and its result are one tool run.
	if !strings.Contains(flat, "3 tool calls") {
		t.Errorf("the fold must count runs, not cards: %q", flat)
	}
	// The tool NAMES survive - they are what a reader scans for.
	for _, want := range []string{"read_file", "web_search", "run_shell"} {
		if !strings.Contains(flat, want) {
			t.Errorf("the fold must name %q: %q", want, flat)
		}
	}
	if !strings.Contains(flat, "⌃o") {
		t.Errorf("the fold must say how to open it: %q", flat)
	}
	// A glyph is not a tool name (the extractor used to read "⚙" as one).
	if strings.Contains(flat, "⚙") {
		t.Errorf("a glyph leaked into the tool-name list: %q", flat)
	}
	// Prose is never folded.
	if !strings.Contains(stripANSI(got[1]), "Endpoints are live") {
		t.Errorf("prose must survive the fold: %q", got[1])
	}
}

func TestToolMachineryOpensWithCtrlO(t *testing.T) {
	m := foldSeed(t)
	out, _ := m.onAgentKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = asModel(out)
	if !m.showToolCalls {
		t.Fatal("ctrl+o must open the fold")
	}
	got := m.displayAgentLines(110)
	if len(got) != 7 {
		t.Errorf("opened, every card shows: want 7 rows, got %d", len(got))
	}
	for _, l := range got {
		if strings.Contains(l, toolCardMark) {
			t.Error("the card mark leaked into a rendered line")
		}
	}
	out, _ = m.onAgentKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if asModel(out).showToolCalls {
		t.Error("ctrl+o must fold it back")
	}
}

// A single tool run does not fold: swapping one line for a one-line summary that says
// less is pure loss.
func TestSingleToolRunDoesNotFold(t *testing.T) {
	m := foldSeed(t)
	m.agentLines = []string{toolCardMark + agentToolCallLine("read_file", "settings.yaml")}
	got := m.displayAgentLines(110)
	if len(got) != 1 || strings.Contains(stripANSI(got[0]), "tool call ·") {
		t.Errorf("a lone card must render as itself, got %q", got)
	}
}

// The mark is a C0 byte that ansi.Strip preserves, so anything leaving the TUI has to
// drop it explicitly or it rides invisibly into a clipboard or across the RC wire.
func TestToolCardMarkNeverEscapes(t *testing.T) {
	m := foldSeed(t)
	if txt := m.agentTranscriptText(); strings.Contains(txt, toolCardMark) {
		t.Error("the card mark leaked into the copied/streamed transcript")
	}
	m.showToolCalls = true
	if txt := m.agentTranscriptText(); strings.Contains(txt, toolCardMark) {
		t.Error("the card mark leaked with the fold open")
	}
}

// ── SHIFT+TAB IS A TOGGLE ────────────────────────────────────────────────────
// Founder 2026-08-20: "just how pressing shift-tab on a chat [1] Tune In section
// moves me to the [0] Agent section, pressing shift-tab again on the Agent section
// should move me back". It was a one-way door; the way back is the same key.
func TestShiftTabTogglesBetweenChannelAndAgent(t *testing.T) {
	m := browseSeed(100)
	m.width, m.height = 100, 30
	m.connected = &offer{NodeID: "amber-fox", Model: "m1", Online: true}
	m.mode = modeChat
	m.chatIn.Focus()

	// TUNE IN -> AGENT (the leg that already existed)
	out, _ := m.onKey(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = asModel(out)
	if m.mode != modeAgent {
		t.Fatalf("shift+tab from the channel should open AGENT, got mode %v", m.mode)
	}

	// AGENT -> TUNE IN (the return leg)
	out, _ = m.onAgentKey(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = asModel(out)
	if m.mode != modeChat {
		t.Fatalf("shift+tab from AGENT should go back to the channel, got mode %v", m.mode)
	}
	// Looking away must not end anything: the station stays tuned and the agent
	// session is kept, so the toggle can be pressed all day.
	if m.connected == nil {
		t.Error("the channel must stay open across the toggle")
	}
	if m.agent == nil {
		t.Error("the agent session must be kept, not torn down")
	}

	// ...and it keeps toggling.
	out, _ = m.onKey(tea.KeyMsg{Type: tea.KeyShiftTab})
	if asModel(out).mode != modeAgent {
		t.Error("the toggle must keep working, not fire once")
	}
}

// AGENT can be reached with nothing tuned in, and can outlive a disconnect. Sending
// someone to a channel with no station would be worse than saying so.
func TestShiftTabInAgentWithNoChannelExplains(t *testing.T) {
	m := browseSeed(100)
	m.width, m.height = 100, 30
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	m.connected = nil
	out, _ := m.onAgentKey(tea.KeyMsg{Type: tea.KeyShiftTab})
	m2 := asModel(out)
	if m2.mode != modeAgent {
		t.Errorf("with no channel open shift+tab must stay put, got mode %v", m2.mode)
	}
	if !strings.Contains(stripANSI(m2.status), "no channel open") {
		t.Errorf("it must say why nothing happened, got %q", stripANSI(m2.status))
	}
}

// The footer ladder sheds words as the terminal narrows, and twice now a new key
// pushed a spec'd word off the rung that was actually chosen - /operator
// (desk_view.feature) and "transcript" (agent_prompt_fixes.feature). Both failures
// looked like unrelated BDD breakage. This says it directly: at any width with real
// room, the chosen line still teaches what those specs require.
func TestAgentFooterKeepsSpecdWordsWhileItHasRoom(t *testing.T) {
	for w := 96; w <= 200; w += 4 {
		f := stripANSI(agentDeckSeed(t, w, 24, false).footer(w))
		for _, want := range []string{"transcript", "/operator", "ask", "copy", "perms", "esc"} {
			if !strings.Contains(f, want) {
				t.Errorf("width %d: the footer dropped %q while it still had room:\n%s", w, want, f)
			}
		}
	}
}
