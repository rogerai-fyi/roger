package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"rogerai.fm/roger/v5/internal/harness"
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

	// AMENDED 2026-08-21 (round 4): the three-row plate (lit lip / face / fallen lip)
	// is now a two-row CARD - face plus shadow, no glyphs. Two reasons, both from a
	// founder screenshot: it was built to the VIEWPORT width while transcriptContent
	// wraps at width-2 and then indents, so every plate wrapped and came back as a
	// stray fragment; and even fixed, a top lip scrolls off on its own in a moving
	// transcript, leaving an orphan line above the text. Depth in a terminal is just
	// relative brightness - a lighter face over a darker row under it - and painting
	// both as background means nothing depends on a font having ▔.
	// AMENDED again 2026-08-21 (round 5): the block gained INTERIOR PADDING - a tinted
	// blank row above and below the text - which is what makes it read as a box rather
	// than a highlighted line, and is the thing opencode's blocks have that ours did
	// not. So a short ask is pad + face + pad + shadow = 4 rows.
	const w = 96
	rows := askSlate("hi", w)
	if len(rows) != 4 {
		t.Fatalf("a short ask is pad + face + pad + shadow = 4 rows, got %d", len(rows))
	}
	if !strings.Contains(stripANSI(rows[1]), "▌ hi") {
		t.Errorf("the face keeps the ▌ band and the question: %q", stripANSI(rows[1]))
	}
	for _, i := range []int{0, 2, 3} {
		if strings.TrimSpace(stripANSI(rows[i])) != "" {
			t.Errorf("row %d is padding or shadow, not text: %q", i, stripANSI(rows[i]))
		}
	}
	// Every row spans exactly, or the card has a ragged edge - and one cell over is
	// what made it wrap in the first place.
	for i, r := range rows {
		if got := lipgloss.Width(r); got != w {
			t.Errorf("card row %d width %d, want exactly %d", i, got, w)
		}
	}
	// A long ask wraps INSIDE the face, on word boundaries.
	long := askSlate(strings.Repeat("what are some things i can do today that has to wrap ", 3), w)
	if len(long) < 5 {
		t.Fatalf("a long ask should wrap inside the card, got %d rows", len(long))
	}
	for i, r := range long {
		if got := lipgloss.Width(r); got != w {
			t.Errorf("wrapped card row %d width %d, want %d", i, got, w)
		}
	}
	if !strings.Contains(stripANSI(strings.Join(long, "")), "that") {
		t.Error("wrapping must not break a word in half")
	}
}

// THE WIDTH CONTRACT. transcriptContent wraps every entry at width-2 and then indents
// by two, so anything that paints to its own edges must be built to the CONTENT width.
// Getting this wrong is invisible in code and obvious on screen: the founder
// screenshotted a card whose rows had wrapped into fragments.
func TestFullWidthRowsFitTheTranscriptContentWidth(t *testing.T) {
	m := browseSeed(80)
	m.width, m.height = 80, 26
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	m.agentRuns = []toolRun{{Name: "web_search", Status: toolOK, Detail: "ok"}}
	m.agentLines = []string{askMark + "how are things", toolRef(0)}
	m.agentOpenRun = -1

	// The invariant is EXCEEDS, not equals: the mono fallback deliberately does not pad
	// (there is no background to carry), and only an over-long row wraps into a
	// fragment. TestAskRendersAsAFullWidthSlate covers the tinted path's exact padding.
	content := transcriptContent(m.displayAgentLines(m.effWidth()), m.effWidth())
	for i, ln := range strings.Split(content, "\n") {
		if got := lipgloss.Width(ln); got > m.effWidth() {
			t.Errorf("content row %d is %d cells, over %d - it will wrap into a fragment",
				i, got, m.effWidth())
		}
	}

	// And with colour on, where the card DOES paint to its edges, nothing overflows
	// either - that is the case the founder's screenshot came from.
	colorOn(t, true)
	oldQ := quiet
	quiet = false
	defer func() { quiet = oldQ }()
	content = transcriptContent(m.displayAgentLines(m.effWidth()), m.effWidth())
	for i, ln := range strings.Split(content, "\n") {
		if got := lipgloss.Width(ln); got > m.effWidth() {
			t.Errorf("tinted content row %d is %d cells, over %d", i, got, m.effWidth())
		}
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

// ── THE PAINTED DECK GROUND ──────────────────────────────────────────────────
// Founder: "i want the background a different color, lets make it more roger like
// like a radio". A terminal hands you whatever ground the operator's theme picked -
// the founder's is purple - and a faceplate that changes colour with the room is not
// a faceplate.

func deckOn(t *testing.T) {
	t.Helper()
	colorOn(t, true)
	oldQ, oldD := quiet, deckGround
	quiet, deckGround = false, true
	t.Cleanup(func() { quiet, deckGround = oldQ, oldD })
}

// The ground reaches the EDGES: every row padded to the full width, or the paint
// stops where the text does and the screen reads as a ragged column.
func TestDeckPaintsEveryRowToTheEdge(t *testing.T) {
	deckOn(t)
	const w = 80
	frame := paintDeck("short\nalso short\n", w)
	for i, ln := range strings.Split(strings.TrimRight(frame, "\n"), "\n") {
		if got := len([]rune(stripANSI(ln))); got != w {
			t.Errorf("row %d painted to %d cells, want %d", i, got, w)
		}
	}
}

// Nested foreground styles emit SGR resets, which punch holes in an outer background
// and leave a mottled screen. solidBackground re-arms the ground after each one; this
// pins that it is actually being used.
func TestDeckSurvivesNestedStyles(t *testing.T) {
	deckOn(t)
	inner := stLive.Render("red") + " plain " + stDim.Render("dim")
	frame := paintDeck(inner, 40)
	resets := strings.Count(frame, "\x1b[0m")
	if resets == 0 {
		t.Fatal("precondition: nested styles should emit resets")
	}
	// Every reset except the line's final one must be followed by the ground being
	// re-armed, or the rest of that span paints on the terminal's own background.
	ground := lipgloss.NewStyle().Background(cDeck).Render("X")
	prefix := ground[:strings.Index(ground, "X")]
	if !strings.Contains(frame, "\x1b[0m"+prefix) {
		t.Error("the ground must be re-armed after a nested reset, or the screen mottles")
	}
}

// REVERSIBILITY - the same rule the lamp board follows: no visual layer may be
// unremovable. Three independent off switches, and each must fully restore the frame.
func TestDeckIsFullyRemovable(t *testing.T) {
	deckOn(t)
	const raw = "a line\n"

	deckGround = false
	if got := paintDeck(raw, 40); got != raw {
		t.Error("`deck off` must hand the background back untouched")
	}
	deckGround = true

	SetPalette("mono")
	if got := paintDeck(raw, 40); got != raw {
		t.Error("the mono escape hatch must drop the deck too")
	}
	SetPalette("full")

	quiet = true
	if got := paintDeck(raw, 40); got != raw {
		t.Error("a terminal that cannot tint must never be painted")
	}
	quiet = false
}

// Headless renders (tests, pipes, NO_COLOR) must be byte-identical to what they always
// were - which is what lets the rest of this suite keep asserting on exact frames.
func TestDeckIsInertHeadless(t *testing.T) {
	m := browseSeed(80)
	m.width, m.height = 80, 24
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	if strings.Contains(m.View(), "\x1b[48") {
		t.Error("a headless frame must carry no background paint")
	}
}

// The preset bar was the one row on the screen that did not clip, so below ~80 columns
// it wrapped - costing a row and breaking the painted ground's rectangle where it
// spilled. Every row fits its terminal now.
func TestNoRowOverflowsAtAnyWidth(t *testing.T) {
	for w := 60; w <= 160; w += 5 {
		m := browseSeed(w)
		m.width, m.height = w, 24
		m.mode = modeAgent
		m.connected = &offer{NodeID: "station", Model: "m", Online: true}
		m.agent = m.newAgentRuntime()
		for i, ln := range strings.Split(stripANSI(m.View()), "\n") {
			if got := len([]rune(ln)); got > w {
				t.Errorf("width %d: row %d overflows at %d cells: %q", w, i, got, ln)
			}
		}
	}
}

// ── LARGE PASTES ─────────────────────────────────────────────────────────────
// Founder 2026-08-21: "when pasting a large amount of text into the tui, it breaks
// the text box". A 300-line paste is 300 rows trying to live in a six-row composer.

func pasteInto(t *testing.T, m model, text string) model {
	t.Helper()
	m.agentIn.Focus()
	out, _ := m.onAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text), Paste: true})
	return asModel(out)
}

func TestLargePasteBecomesAPlaceholder(t *testing.T) {
	m := browseSeed(96)
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	big := strings.Repeat("a line of pasted content\n", 247)
	m = pasteInto(t, m, big)

	got := m.agentIn.Value()
	if strings.Contains(got, "pasted content") {
		t.Errorf("the composer must hold a placeholder, not the paste: %q", got)
	}
	if !strings.Contains(got, "247 lines") {
		t.Errorf("the placeholder must say how much arrived: %q", got)
	}
	// The MODEL still gets every byte.
	if exp := m.expandPastes(got); exp != big {
		t.Errorf("expansion must be lossless: got %d bytes, pasted %d", len(exp), len(big))
	}
}

// A single enormous line (a JSON blob, a key) has no useful line count, so the
// placeholder reports size instead.
func TestOneHugeLinePasteReportsSize(t *testing.T) {
	m := browseSeed(96)
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	m = pasteInto(t, m, strings.Repeat("x", 5000))
	got := m.agentIn.Value()
	if !strings.Contains(got, "KB") {
		t.Errorf("a one-line paste should report size, not lines: %q", got)
	}
}

// SMALL pastes stay inline: a URL or a short snippet is something you want to SEE
// before sending, and hiding it would be strictly worse than the bug this fixes.
func TestSmallPasteStaysInline(t *testing.T) {
	m := browseSeed(96)
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	m = pasteInto(t, m, "https://rogerai.fm/models")
	if got := m.agentIn.Value(); got != "https://rogerai.fm/models" {
		t.Errorf("a small paste must land as itself: %q", got)
	}
	if len(m.agentPastes) != 0 {
		t.Error("a small paste must not be held")
	}
}

// Typed text that merely LOOKS like a placeholder expands to nothing - substituting
// there would silently delete what the operator wrote.
func TestUnbackedPlaceholderIsLeftAlone(t *testing.T) {
	m := browseSeed(96)
	m.agentPastes = []string{"real content"}
	in := "see [Pasted text #9 +3 lines] and [Pasted text #1 +2 lines]"
	got := m.expandPastes(in)
	if !strings.Contains(got, "[Pasted text #9 +3 lines]") {
		t.Errorf("a placeholder with nothing behind it must survive verbatim: %q", got)
	}
	if !strings.Contains(got, "real content") {
		t.Errorf("a backed placeholder must expand: %q", got)
	}
}

// ── THE DELEGATION STRIP ─────────────────────────────────────────────────────
// Founder 2026-08-21: show delegation "neatly and beautifully under the ask footer".

func withDelegates(t *testing.T, tools ...string) model {
	t.Helper()
	m := browseSeed(96)
	m.width, m.height = 96, 26
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	for i, tool := range tools {
		m.noteDelegateEvent(fmt.Sprintf("#%d", i+1),
			agentEventMsg{Kind: harness.EventToolCall, Tool: tool, Step: i + 1})
	}
	return m
}

// The strip names each child and what it is DOING - "reading", not "read_file". A
// status line is read by a person.
func TestDelegationStripNamesWhatEachChildIsDoing(t *testing.T) {
	m := withDelegates(t, "read_file", "web_search")
	got := stripANSI(m.delegationStrip(96))
	for _, want := range []string{"2 delegated", "#1", "reading", "#2", "searching"} {
		if !strings.Contains(got, want) {
			t.Errorf("the strip must carry %q: %q", want, got)
		}
	}
	if strings.Contains(got, "read_file") {
		t.Errorf("the strip speaks human, not tool names: %q", got)
	}
}

// It NEVER wraps: the readout is a fixed two rows, and that fixed height is what keeps
// the composer from moving. It sheds verbs before it sheds a child, because knowing
// three are running matters more than knowing what the third is doing.
func TestDelegationStripNeverWraps(t *testing.T) {
	m := withDelegates(t, "read_file", "web_search", "web_fetch", "list_dir")
	for w := 24; w <= 120; w += 4 {
		strip := m.delegationStrip(w)
		if got := lipgloss.Width(strip); got > w {
			t.Errorf("width %d: strip is %d cells: %q", w, got, stripANSI(strip))
		}
	}
	// Wide enough: the verbs are there. Narrow: the children still are.
	if !strings.Contains(stripANSI(m.delegationStrip(120)), "reading") {
		t.Error("a wide terminal should show the verbs")
	}
	if !strings.Contains(stripANSI(m.delegationStrip(40)), "#4") {
		t.Error("a narrow one must still account for every child")
	}
}

// A finished child leaves the strip; when the last one goes, the carrier returns and
// the row is never empty.
func TestFinishedDelegatesLeaveTheStrip(t *testing.T) {
	m := withDelegates(t, "read_file", "web_search")
	m.noteDelegateEvent("#1", agentEventMsg{AgentDone: true})
	got := stripANSI(m.delegationStrip(96))
	if strings.Contains(got, "#1") {
		t.Errorf("a finished child must leave the strip: %q", got)
	}
	if !strings.Contains(got, "#2") {
		t.Errorf("the running one must stay: %q", got)
	}
	m.noteDelegateEvent("#2", agentEventMsg{AgentDone: true})
	if s := m.delegationStrip(96); s != "" {
		t.Errorf("with no live children the strip yields the row back to the carrier: %q", s)
	}
}

// A subagent's own tool calls must NOT walk into the parent's transcript: a child can
// make a dozen calls to answer one question, and pouring those into the parent's flow
// is the noise the machinery fold exists to remove.
func TestSubagentEventsStayOutOfTheTranscript(t *testing.T) {
	m := browseSeed(96)
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	before := len(m.agentLines)
	out, _ := m.onAgentEvent(agentEventMsg{
		Kind: harness.EventToolCall, Tool: "read_file", Agent: "#1",
	})
	m2 := asModel(out)
	if len(m2.agentLines) != before {
		t.Errorf("a child's event added %d transcript lines; it belongs on the strip",
			len(m2.agentLines)-before)
	}
	if len(m2.agentDelegates) != 1 {
		t.Error("...and it must reach the strip")
	}
}

// The delegation RECEIPT closes the loop the strip opens: the strip says who is
// working, this says what they did. Without it the per-agent receipts the harness
// keeps never reach a human, and attribution nobody can see is bookkeeping for its own
// sake.
func TestDelegationReceiptNamesEachChild(t *testing.T) {
	m := browseSeed(96)
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	m.agent.loop.Guards = []harness.Guard{}
	// Two children, one of which did not finish.
	m.agent.loop.SetChildReceiptsForTest([]harness.Receipt{
		{Agent: "#1", Steps: 4, Complete: true},
		{Agent: "#2", Steps: 1, Complete: false},
	})
	line := stripANSI(m.delegationReceiptLine())
	for _, want := range []string{"2 delegated", "#1", "4 steps", "#2", "1 step", "unfinished"} {
		if !strings.Contains(line, want) {
			t.Errorf("the receipt must carry %q: %q", want, line)
		}
	}
	// "1 steps" is the kind of small wrongness that makes a reader stop trusting the
	// rest of the numbers.
	if strings.Contains(line, "1 steps") {
		t.Errorf("count must agree with its noun: %q", line)
	}
}

// A turn that delegated to nobody says nothing.
func TestNoDelegationNoReceiptLine(t *testing.T) {
	m := browseSeed(96)
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	if got := m.delegationReceiptLine(); got != "" {
		t.Errorf("a turn with no children must add no line: %q", got)
	}
}

// The shared plural helper must be able to spell the nouns it is now used for.
func TestPluralHandlesTheEsCases(t *testing.T) {
	cases := map[string]string{"search": "3 searches", "fetch": "2 fetches", "step": "5 steps", "band": "4 bands"}
	counts := map[string]int{"search": 3, "fetch": 2, "step": 5, "band": 4}
	for noun, want := range cases {
		if got := plural(counts[noun], noun); got != want {
			t.Errorf("plural(%d, %q) = %q, want %q", counts[noun], noun, got, want)
		}
	}
	if got := plural(1, "search"); got != "1 search" {
		t.Errorf("singular must not pluralise: %q", got)
	}
}

// A footer status must be READABLE, not run off the right edge. The founder hit the
// private-band limit and the refusal was cut mid-sentence, losing the half that says
// what to do about it - a message you cannot finish reading is worse than none, because
// you know something is wrong and not what.
func TestFooterStatusWrapsInsteadOfRunningOff(t *testing.T) {
	long := "! private band limit reached (free plan allows 1) - yours is on " +
		"roggentoo-gemma-4-31b-8086. Move it to this model to keep the same frequency code, " +
		"or revoke it first - manage your bands in the console."
	for _, w := range []int{60, 80, 120} {
		out := wrapStatus(long, w)
		rows := strings.Split(out, "\n")
		if len(rows) < 2 {
			t.Errorf("width %d: a long status must wrap, got one row", w)
		}
		for i, r := range rows {
			if got := lipgloss.Width(r); got > w {
				t.Errorf("width %d: row %d is %d cells - still running off", w, i, got)
			}
		}
		// Nothing may be lost: the actionable half is the point.
		flat := stripANSI(strings.ReplaceAll(out, "\n", " "))
		for _, want := range []string{"limit reached", "Move it to this model", "revoke it first"} {
			if !strings.Contains(flat, want) {
				t.Errorf("width %d: wrapping dropped %q", w, want)
			}
		}
	}
}

// ── THE PRIVATE-BAND QUOTA OFFER ─────────────────────────────────────────────
// Founder 2026-08-21: hitting the limit on the SHARE screen produced a refusal and a
// signpost to another screen. A dead end that names another screen is still a dead end;
// the operator wanted this model on a private band, and moving their existing one does
// exactly that while keeping its code, so everyone already tuned in keeps working.

func TestQuotaRefusalOffersTheMove(t *testing.T) {
	flat := stripANSI(bandQuotaOffer("grok-4.3"))
	for _, want := range []string{"press y", "grok-4.3", "keeps its code"} {
		if !strings.Contains(flat, want) {
			t.Errorf("the refusal must offer the fix HERE, not signpost another screen: missing %q in %q", want, flat)
		}
	}
	// It still names the other surface for anything the one-key offer cannot do.
	if !strings.Contains(flat, "manage bands") {
		t.Errorf("the fuller surface should still be reachable: %q", flat)
	}
}

// `m` only means "move" while an offer is standing, so it keeps its usual meaning
// everywhere else.
// The accept key must not shadow a global one: m already toggles the compact
// windowshade everywhere, and shadowing it inside one view teaches an operator not to
// trust their own muscle memory. y matches the other confirms on this screen.
func TestMoveKeyIsInertWithoutAnOffer(t *testing.T) {
	m := browseSeed(120)
	m.mode = modeShare
	m.bandMoveOffer = ""
	before := m.status
	out, cmd := m.onKey(keyMsg("y"))
	if cmd != nil {
		t.Error("with no offer standing, y must not fire a band move")
	}
	if asModel(out).status != before {
		t.Error("...and must not claim to be moving anything")
	}
}

// ── THE PRICE EDITOR'S ARROW KEYS ────────────────────────────────────────────
// Founder 2026-08-21: up and down did nothing while editing a limit, so setting a
// price meant typing every digit - and the arrows are the first thing anyone tries in
// a numeric field.
func TestArrowKeysNudgeALimitByOneUnit(t *testing.T) {
	// The price field steps by a cent and formats to two places.
	if got := nudgeLimit("0.30", true, true); got != "0.31" {
		t.Errorf("up on a price = %q, want 0.31", got)
	}
	if got := nudgeLimit("0.30", true, false); got != "0.29" {
		t.Errorf("down on a price = %q, want 0.29", got)
	}
	// min t/s is whole tokens.
	if got := nudgeLimit("7", false, true); got != "8" {
		t.Errorf("up on t/s = %q, want 8", got)
	}
}

// DOWN FLOORS AT ZERO. A negative cap is not a smaller cap, it is a nonsense the commit
// would have to reject - so the field never offers one.
func TestNudgeNeverGoesNegative(t *testing.T) {
	for _, start := range []string{"0", "0.00", "0.01", ""} {
		if got := nudgeLimit(start, true, false); got != "0.00" {
			t.Errorf("down from %q = %q, want 0.00", start, got)
		}
	}
	if got := nudgeLimit("0", false, false); got != "0" {
		t.Errorf("down from 0 t/s = %q, want 0", got)
	}
}

// An empty or unparsable field starts from zero, so the first press does the obvious
// thing rather than nothing.
func TestNudgeStartsFromZeroWhenTheFieldIsBlank(t *testing.T) {
	if got := nudgeLimit("", true, true); got != "0.01" {
		t.Errorf("up on a blank price = %q, want 0.01", got)
	}
	if got := nudgeLimit("abc", false, true); got != "1" {
		t.Errorf("up on junk = %q, want 1", got)
	}
}

// Repeated steps must not drift: a price field showing "0.30000000000000004" has lost
// the operator's trust over a rounding artifact.
func TestNudgeDoesNotAccumulateFloatDrift(t *testing.T) {
	v := "0.00"
	for i := 0; i < 30; i++ {
		v = nudgeLimit(v, true, true)
	}
	if v != "0.30" {
		t.Errorf("30 cent-steps from zero = %q, want 0.30", v)
	}
	if strings.Contains(v, "000000") {
		t.Errorf("float drift reached the field: %q", v)
	}
}

// ── SECRETS MUST NOT REACH DISK ──────────────────────────────────────────────
// The palette records every command it runs and persists that history to a file,
// recalled with ↑. `/freq <code>` carries a band's frequency code - so typing it there
// wrote the secret to disk and left it one keypress from anyone at that terminal,
// flatly contradicting the promise that a code is shown once and never stored.
func TestFrequencyCodesNeverReachCommandHistory(t *testing.T) {
	const secret = "147.520 MHz 8F3K-9M2Q"
	for _, cmd := range []string{"/freq " + secret, "/f " + secret, "freq " + secret} {
		got := scrubSecretArgs(cmd)
		if strings.Contains(got, "8F3K") || strings.Contains(got, "9M2Q") {
			t.Errorf("%q kept the secret: %q", cmd, got)
		}
		// The VERB survives, so recall still tells you what you ran.
		if !strings.Contains(strings.ToLower(got), "f") {
			t.Errorf("%q lost the verb too: %q", cmd, got)
		}
	}
}

// Ordinary commands are untouched: history is useful precisely because it is complete.
func TestOrdinaryCommandsKeepTheirArguments(t *testing.T) {
	for _, cmd := range []string{"/model grok-4.6", "/limit gpt-oss-20b 1.00", "/share", "/help"} {
		if got := scrubSecretArgs(cmd); got != cmd {
			t.Errorf("%q must be recorded whole, got %q", cmd, got)
		}
	}
}

// ── THE BAND SCREENS HAVE THEIR OWN KEYS ─────────────────────────────────────
// BASE STATION had no footer case, so it fell through to the BROWSE keys and taught
// five that do nothing there. A footer describing a different screen is worse than
// none: it is the one place an operator looks to learn what a screen does.
func TestBandScreensTeachTheirOwnKeys(t *testing.T) {
	cases := []struct {
		mode mode
		want []string
		deny []string
	}{
		{modePrivate, []string{"manage", "revoke"}, []string{"tune in", "filter", "sort"}},
		{modeBandMove, []string{"move the band"}, []string{"tune in", "sort"}},
		{modeBandRevokeConfirm, []string{"revoke", "keep it"}, []string{"tune in", "sort"}},
	}
	for _, tc := range cases {
		m := browseSeed(120)
		m.width, m.height = 120, 30
		m.mode = tc.mode
		f := strings.ToLower(stripANSI(m.footer(120)))
		for _, w := range tc.want {
			if !strings.Contains(f, w) {
				t.Errorf("mode %v must teach %q: %q", tc.mode, w, f)
			}
		}
		for _, d := range tc.deny {
			if strings.Contains(f, d) {
				t.Errorf("mode %v must not teach the browse key %q: %q", tc.mode, d, f)
			}
		}
	}
}
