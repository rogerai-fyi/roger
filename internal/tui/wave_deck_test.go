package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	add := func(tool, arg string) {
		m.agentRuns = append(m.agentRuns, toolRun{
			Name: tool, Arg: arg, Status: toolOK, Detail: "ok · 51 bytes",
		})
		m.agentLines = append(m.agentLines, toolRef(len(m.agentRuns)-1))
	}
	add("read_file", "settings.yaml")
	add("web_search", "dsh local models")
	add("run_shell", "echo ports")
	m.agentLines = append(m.agentLines, "  Endpoints are live.")
	m.agentOpenRun = -1
	return m
}

func TestToolMachineryFoldsByDefault(t *testing.T) {
	m := foldSeed(t)
	if m.showToolCalls {
		t.Fatal("the machinery must start folded - that is the whole ask")
	}
	got := m.displayAgentLines(110)
	if len(got) != 2 {
		t.Fatalf("three calls + one prose line should fold to 2 rows, got %d:\n%s", len(got), strings.Join(got, "\n"))
	}
	flat := stripANSI(got[0])
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
	// 6 cards + a lid + a closing rail + the prose line. The lid and rail are the
	// box's visible EDGES: an opened drawer you cannot see the sides of is just
	// machinery loose in the flow again.
	if len(got) != 6 {
		t.Errorf("opened: want lid + 3 cards + rail + prose = 6 rows, got %d:\n%s", len(got), strings.Join(got, "\n"))
	}
	if !strings.Contains(stripANSI(got[0]), "▾") {
		t.Errorf("the open box needs a turned-down lid, got %q", stripANSI(got[0]))
	}
	if !strings.Contains(stripANSI(got[0]), "⌃o") {
		t.Error("the open lid must still advertise the way back")
	}
	for _, l := range got {
		if toolRefIndex(l) >= 0 || strings.Contains(l, toolRefMark) {
			t.Error("an unresolved tool reference leaked into a rendered line")
		}
	}
	out, _ = m.onAgentKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if asModel(out).showToolCalls {
		t.Error("ctrl+o must fold it back")
	}
}

// AMENDED 2026-08-20 (round 2): this asserted that a LONE card does not fold, on my
// reasoning that swapping one line for a one-line summary says less. The founder
// screenshotted exactly that case - a single "✓ web_fetch … ok · 132 bytes" still in
// the flow - and asked why it had not folded. The point is not saving rows: machinery
// belongs behind ONE door, so a reader never has to know how many calls a turn made to
// predict what the transcript looks like. Now inverted, with the tool name kept.
func TestSingleToolRunFoldsToo(t *testing.T) {
	m := foldSeed(t)
	m.agentRuns = []toolRun{{Name: "web_fetch", Arg: "https://example.com", Status: toolOK, Detail: "ok · 132 bytes"}}
	m.agentLines = []string{toolRef(0)}
	got := m.displayAgentLines(110)
	if len(got) != 1 {
		t.Fatalf("a lone card folds to one lid, got %d rows", len(got))
	}
	flat := stripANSI(got[0])
	for _, want := range []string{"▸", "1 tool call", "web_fetch", "⌃o"} {
		if !strings.Contains(flat, want) {
			t.Errorf("the lid must carry %q: %q", want, flat)
		}
	}
	// Singular, not "1 tool calls".
	if strings.Contains(flat, "1 tool calls") {
		t.Errorf("count must agree with its noun: %q", flat)
	}
}

// The mark is a C0 byte that ansi.Strip preserves, so anything leaving the TUI has to
// drop it explicitly or it rides invisibly into a clipboard or across the RC wire.
func TestToolCardMarkNeverEscapes(t *testing.T) {
	m := foldSeed(t)
	if txt := m.agentTranscriptText(); strings.Contains(txt, toolRefMark) {
		t.Error("an unresolved tool reference leaked into the copied/streamed transcript")
	}
	// A copy must carry the machinery even when the box is SHUT on screen: a record
	// that silently dropped it would be a worse record than the screen.
	if txt := m.agentTranscriptText(); !strings.Contains(txt, "read_file") {
		t.Errorf("the copied transcript must resolve tool calls, got:\n%s", txt)
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

// ── THE SLATE + THE BOX'S PLACE IN THE FLOW ──────────────────────────────────

// The machinery box must land where the cards actually happened. flushFold was only
// called before PLAIN lines, so an answer or a following ask skipped it and jumped
// ahead of the cards it came after - the box then rendered under prose it preceded.
// Caught on a rendered transcript, not in review: the ordering reads fine in code.
func TestToolBoxLandsBetweenTheAskAndTheAnswer(t *testing.T) {
	m := foldSeed(t)
	m.agentRuns = []toolRun{{Name: "web_fetch", Arg: "https://example.com", Status: toolOK, Detail: "ok · 132 bytes"}}
	m.agentLines = []string{
		askMark + "how are things",
		toolRef(0),
		agentAnswerMark + "Things are good.",
	}
	got := m.displayAgentLines(96)
	ask, box, answer := -1, -1, -1
	for i, l := range got {
		flat := stripANSI(l)
		switch {
		case strings.Contains(flat, "how are things"):
			ask = i
		case strings.Contains(flat, "tool call"):
			box = i
		case strings.Contains(flat, "Things are good"):
			answer = i
		}
	}
	if ask < 0 || box < 0 || answer < 0 {
		t.Fatalf("expected ask, box and answer; got %d/%d/%d in:\n%s", ask, box, answer, strings.Join(got, "\n"))
	}
	if !(ask < box && box < answer) {
		t.Errorf("the box must sit between the ask and the answer, got ask=%d box=%d answer=%d", ask, box, answer)
	}
}

// With the box shut, a tool-output preview belongs IN the box - and the "d·output"
// hint must not hang off the lid, which is not a result line.
func TestClosedBoxSwallowsTheOutputHint(t *testing.T) {
	m := foldSeed(t)
	m.agentRuns = []toolRun{{
		Name: "read_file", Status: toolOK, Detail: "ok · 51 bytes",
		Preview: []string{"some file contents"},
	}}
	m.agentLines = []string{toolRef(0)}
	flat := stripANSI(strings.Join(m.displayAgentLines(96), "\n"))
	if strings.Contains(flat, "d·output") {
		t.Errorf("the shut lid must not carry the output hint:\n%s", flat)
	}
	if strings.Contains(flat, "some file contents") {
		t.Errorf("a shut box must not leak its output:\n%s", flat)
	}
	// Open it and the old behaviour returns intact.
	m.showToolCalls, m.showToolOutput = true, true
	if open := stripANSI(strings.Join(m.displayAgentLines(96), "\n")); !strings.Contains(open, "some file contents") {
		t.Errorf("opened, the output must show:\n%s", open)
	}
}

// The ask is a full-width slate, not a smudge the width of its own text (founder:
// each section "more aggressively visually showned ... slate boxes for each section").
func TestAskRendersAsAFullWidthSlate(t *testing.T) {
	colorOn(t, true)
	oldQ := quiet
	quiet = false
	t.Cleanup(func() { quiet = oldQ })

	// AMENDED 2026-08-20 (round 3): the flat band became a RAISED block - a lit top lip,
	// the face, a fallen bottom lip - so a short ask is three rows, not one. Spanning
	// the full width is the part that did not change and is what keeps the block's
	// edges straight.
	const w = 96
	rows := askSlate("hi", w)
	if len(rows) != 3 {
		t.Fatalf("a short ask is lip + face + lip = 3 rows, got %d", len(rows))
	}
	if !strings.Contains(stripANSI(rows[0]), "▔") {
		t.Errorf("the top lip catches the light: %q", stripANSI(rows[0]))
	}
	if !strings.Contains(stripANSI(rows[2]), "▁") {
		t.Errorf("the bottom lip falls away: %q", stripANSI(rows[2]))
	}
	if !strings.Contains(stripANSI(rows[1]), "▌ hi") {
		t.Errorf("the face keeps the ▌ band and the question: %q", stripANSI(rows[1]))
	}
	// Every row spans, or the block has a ragged edge and stops reading as an object.
	for i, r := range rows {
		if got := lipgloss.Width(r); got != w {
			t.Errorf("slate row %d width %d, want %d", i, got, w)
		}
	}
	// A long ask wraps INSIDE the face, on word boundaries - wrapPlain is a hard
	// character wrap and split "that" across two rows before this was fixed.
	long := askSlate(strings.Repeat("what are some things i can do today that has to wrap ", 3), w)
	if len(long) < 4 {
		t.Fatalf("a long ask should wrap inside the block, got %d rows", len(long))
	}
	for i, r := range long {
		if got := lipgloss.Width(r); got != w {
			t.Errorf("wrapped slate row %d width %d, want %d", i, got, w)
		}
	}
	joined := stripANSI(strings.Join(long, ""))
	if !strings.Contains(joined, "that") {
		t.Error("wrapping must not break a word in half")
	}
}

// The mark must not escape into the clipboard or across the RC wire - it is a C0 byte
// that ansi.Strip preserves, exactly like the other two.
func TestAskMarkNeverEscapes(t *testing.T) {
	m := foldSeed(t)
	m.agentLines = []string{askMark + "a private question"}
	txt := m.agentTranscriptText()
	if strings.Contains(txt, askMark) {
		t.Error("the ask mark leaked into the copied/streamed transcript")
	}
	if !strings.Contains(txt, "a private question") {
		t.Errorf("the ask text itself must survive: %q", txt)
	}
}
