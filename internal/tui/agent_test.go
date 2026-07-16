package tui

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/harness"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestZeroEntersAgentMode: pressing 0 in BROWSE jumps to the [0] AGENT mode, and the
// preset bar lights AGENT.
func TestZeroEntersAgentMode(t *testing.T) {
	m := browseSeed(100)
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg("0"))
	got := asModel(tm)
	if got.mode != modeAgent {
		t.Fatalf("0 should enter modeAgent, got mode %d", got.mode)
	}
	out := stripANSI(got.View())
	if !strings.Contains(out, "AGENT") {
		t.Errorf("AGENT view/preset not shown:\n%s", out)
	}
}

// TestAgentPresetAtFront: the preset bar leads with [0] AGENT, then [1] TUNE IN, etc.
func TestAgentPresetAtFront(t *testing.T) {
	m := browseSeed(120)
	out := stripANSI(m.View())
	bar := firstLineContaining(out, "AGENT")
	if bar == "" {
		t.Fatalf("no preset bar line with AGENT:\n%s", out)
	}
	ai := strings.Index(bar, "AGENT")
	ti := strings.Index(bar, "TUNE IN")
	if ai < 0 || ti < 0 || ai > ti {
		t.Errorf("AGENT must come before TUNE IN on the preset bar: %q", bar)
	}
}

// TestZeroNotStolenDuringTextEntry: a typed 0 in the command palette and in the agent
// prompt is a literal digit, NOT a jump back into AGENT (the guard the spec requires).
func TestZeroNotStolenDuringTextEntry(t *testing.T) {
	// In the command palette (/), 0 is typed text.
	m := browseSeed(100)
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg("/"))
	tm, _ = tm.Update(keyMsg("0"))
	cm := asModel(tm)
	if cm.mode != modeCommand {
		t.Fatalf("/ then 0 should stay in modeCommand, got %d", cm.mode)
	}
	if !strings.Contains(cm.cmd.Value(), "0") {
		t.Errorf("0 should be typed into the command input, got %q", cm.cmd.Value())
	}

	// In the AGENT prompt, 0 is typed text (not a re-entry / not stolen).
	var am tea.Model = browseSeed(100)
	am, _ = am.Update(keyMsg("0")) // enter AGENT
	am, _ = am.Update(keyMsg("0")) // type a 0
	gm := asModel(am)
	if gm.mode != modeAgent {
		t.Fatalf("should still be in modeAgent after typing 0, got %d", gm.mode)
	}
	if !strings.Contains(gm.agentIn.Value(), "0") {
		t.Errorf("0 should be typed into the agent prompt, got %q", gm.agentIn.Value())
	}
}

// TestAgentConfirmGate: a pending mutating-tool confirm renders an obvious y/N gate,
// and answering n denies it (releases the loop with false). We drive the model's
// confirm message directly (no network) to keep it deterministic.
func TestAgentConfirmGate(t *testing.T) {
	var am tea.Model = browseSeed(100)
	am, _ = am.Update(keyMsg("0"))
	resp := make(chan bool, 1)
	am, _ = am.Update(agentConfirmMsg(agentConfirm{tool: "run_shell", args: map[string]any{"cmd": "rm -rf /"}, resp: resp}))
	gm := asModel(am)
	if gm.agentPendingConfirm == nil {
		t.Fatalf("a confirm message should set a pending confirm")
	}
	out := stripANSI(gm.View())
	if !strings.Contains(out, "run_shell") || !strings.Contains(out, "rm -rf /") || !strings.Contains(out, "[y/N]") {
		t.Errorf("confirm gate should show the tool + the command + y/N:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "not sandboxed") {
		t.Errorf("run_shell confirm must say it is NOT sandboxed:\n%s", out)
	}
	// Deny with n.
	am, _ = gm.Update(keyMsg("n"))
	if asModel(am).agentPendingConfirm != nil {
		t.Errorf("n should clear the pending confirm")
	}
	if got := <-resp; got != false {
		t.Errorf("n should answer the loop with false (deny), got %v", got)
	}
}

// TestAgentEventRendering: streamed loop events render the tool call + result lines
// with the shared iconography and the final answer.
func TestAgentEventRendering(t *testing.T) {
	var am tea.Model = browseSeed(100)
	am, _ = am.Update(keyMsg("0"))
	am, _ = am.Update(agentEventMsg{Kind: harness.EventToolCall, Tool: "list_dir", Args: map[string]any{"path": "."}})
	am, _ = am.Update(agentEventMsg{Kind: harness.EventToolResult, Tool: "list_dir", Result: "a.go\nb.go\n"})
	am, _ = am.Update(agentEventMsg{Kind: harness.EventFinal, Text: "there are two go files"})
	out := stripANSI(asModel(am).View())
	for _, want := range []string{"list_dir", "⚙", "ok", "there are two go files"} { // ⚙ = dim tool-call machinery (design overhaul inc 7; was ◉)
		if !strings.Contains(out, want) {
			t.Errorf("agent transcript missing %q:\n%s", want, out)
		}
	}
}

// TestAgentToolResultPreviewLong: a long read-only tool result renders a bounded
// preview (the head of the listing) plus a "... +N more lines" marker under the
// existing "ok · N bytes" summary - so the user SEES the real output even when the
// model's prose is terse or truncated.
func TestAgentToolResultPreviewLong(t *testing.T) {
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, "entry-"+strings.Repeat("x", 3)+"-line")
	}
	result := strings.Join(lines, "\n") + "\n"

	var am tea.Model = browseSeed(100)
	am, _ = am.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	am, _ = am.Update(keyMsg("0"))
	am, _ = am.Update(agentEventMsg{Kind: harness.EventToolResult, Tool: "list_dir", Result: result})
	out := stripANSI(asModel(am).View())

	if !strings.Contains(out, "list_dir") || !strings.Contains(out, "bytes") {
		t.Errorf("summary line (tool + bytes) missing:\n%s", out)
	}
	if !strings.Contains(out, "entry-") {
		t.Errorf("a long result should preview the head of the output:\n%s", out)
	}
	if !strings.Contains(out, "more line") {
		t.Errorf("a long result should mark the elided remainder (+N more lines):\n%s", out)
	}
	// Only the bounded head is shown, never all 30 lines.
	if strings.Count(out, "entry-") > previewMaxLines {
		t.Errorf("preview should cap at %d lines, showed %d:\n%s", previewMaxLines, strings.Count(out, "entry-"), out)
	}
}

// TestAgentToolResultPreviewShort: a short read-only result renders inline in full
// (every line shown, no "+N more" marker).
func TestAgentToolResultPreviewShort(t *testing.T) {
	var am tea.Model = browseSeed(100)
	am, _ = am.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	am, _ = am.Update(keyMsg("0"))
	am, _ = am.Update(agentEventMsg{Kind: harness.EventToolResult, Tool: "read_file", Result: "alpha\nbeta\ngamma\n"})
	out := stripANSI(asModel(am).View())
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(out, want) {
			t.Errorf("a short result should preview inline (missing %q):\n%s", want, out)
		}
	}
	if strings.Contains(out, "more line") {
		t.Errorf("a short result should NOT show a +N more marker:\n%s", out)
	}
}

// TestAgentToolResultPreviewCompact: in compact mode the result shows just the
// summary line (tool + bytes), no inlined preview - the dense strip stays dense.
func TestAgentToolResultPreviewCompact(t *testing.T) {
	m := browseSeed(100)
	m.compact = true // compact persists across Update (value-carried), only [m] toggles it
	var am tea.Model = m
	am, _ = am.Update(keyMsg("0"))
	am, _ = am.Update(agentEventMsg{Kind: harness.EventToolResult, Tool: "list_dir", Result: "one\ntwo\nthree\nfour\nfive\n"})
	gm := asModel(am)
	if !gm.compact {
		t.Fatalf("compact should persist across Update")
	}
	out := stripANSI(gm.View())
	if !strings.Contains(out, "list_dir") || !strings.Contains(out, "bytes") {
		t.Errorf("compact should still show the summary line:\n%s", out)
	}
	if strings.Contains(out, "two") || strings.Contains(out, "three") {
		t.Errorf("compact should NOT inline the result preview:\n%s", out)
	}
}

// TestAgentResultPreviewWidthNoColorSafe: the preview never overflows the terminal
// width and emits no ANSI under NO_COLOR, even for very long single lines.
func TestAgentResultPreviewWidthNoColorSafe(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	longResult := strings.Repeat("verylongtoken", 200) + "\n" + strings.Repeat("z", 4000)
	for _, w := range []int{40, 50, 64, 80, 120} {
		var am tea.Model = browseSeed(w)
		am, _ = am.Update(tea.WindowSizeMsg{Width: w, Height: 30})
		am, _ = am.Update(keyMsg("0"))
		am, _ = am.Update(agentEventMsg{Kind: harness.EventToolResult, Tool: "run_shell", Result: longResult})
		out := asModel(am).View()
		if strings.Contains(out, "\x1b[") {
			t.Errorf("width %d: preview emitted ANSI under NO_COLOR", w)
		}
		for _, line := range strings.Split(out, "\n") {
			if vis := utf8.RuneCountInString(stripANSI(line)); vis > w {
				t.Errorf("width %d: preview line overflows (%d cols): %q", w, vis, stripANSI(line))
			}
		}
	}
}

// TestAgentNoColorNarrowSafe: AGENT renders without ANSI under NO_COLOR and never
// overflows narrow widths, including with a pending confirm and a streamed turn.
func TestAgentNoColorNarrowSafe(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	assertSafe := func(w int, am tea.Model) {
		out := am.View()
		if strings.Contains(out, "\x1b[") {
			t.Errorf("width %d: AGENT emitted ANSI under NO_COLOR", w)
		}
		for _, line := range strings.Split(out, "\n") {
			if vis := utf8.RuneCountInString(stripANSI(line)); vis > w {
				t.Errorf("width %d: AGENT line overflows (%d cols): %q", w, vis, stripANSI(line))
			}
		}
	}
	for _, w := range []int{40, 50, 64, 80, 120} {
		// Plain AGENT view (empty input -> placeholder; the prompt + help + footer lines).
		var plain tea.Model = browseSeed(w)
		plain, _ = plain.Update(tea.WindowSizeMsg{Width: w, Height: 24})
		plain, _ = plain.Update(keyMsg("0"))
		assertSafe(w, plain)
		// A streamed turn + a pending confirm (long args, long results).
		var am tea.Model = browseSeed(w)
		am, _ = am.Update(tea.WindowSizeMsg{Width: w, Height: 24})
		am, _ = am.Update(keyMsg("0"))
		am, _ = am.Update(agentEventMsg{Kind: harness.EventToolCall, Tool: "run_shell", Args: map[string]any{"cmd": "ls -la /some/very/long/path/that/keeps/going/on/and/on"}})
		am, _ = am.Update(agentEventMsg{Kind: harness.EventToolResult, Tool: "run_shell", Result: strings.Repeat("x", 500)})
		am, _ = am.Update(agentConfirmMsg(agentConfirm{tool: "write_file", args: map[string]any{"path": "x.txt", "content": "yy"}, resp: make(chan bool, 1)}))
		assertSafe(w, am)
	}
}

// TestAgentErrorRendersActionableHint: a failed turn renders the concise cause + the
// actionable [1]/[2] next step (not a bare "status 504 with no reply" dead end).
func TestAgentErrorRendersActionableHint(t *testing.T) {
	var am tea.Model = browseSeed(100)
	am, _ = am.Update(keyMsg("0"))
	am, _ = am.Update(agentEventMsg{Kind: harness.EventError, Text: "the station returned status 504 with no reply"})
	out := stripANSI(asModel(am).View())
	if !strings.Contains(out, "(504)") {
		t.Errorf("agent error should carry the concise cause + status:\n%s", out)
	}
	if !strings.Contains(out, "[1]") || !strings.Contains(out, "[2]") {
		t.Errorf("agent error should carry the actionable [1]/[2] hint:\n%s", out)
	}
	if strings.Contains(out, "with no reply") {
		t.Errorf("agent error should NOT show the bare status string:\n%s", out)
	}
}

// TestEnterAgentNoModelTunedIn: entering AGENT with nothing tuned in shows the up-front
// "no model tuned in" + [1]/[2] hint, names the gap in the heading (not a stale default
// model), and does not crash.
func TestEnterAgentNoModelTunedIn(t *testing.T) {
	m := browseSeed(100) // browseSeed leaves connected == nil (nothing tuned in)
	if m.connected != nil {
		t.Fatalf("browseSeed should leave nothing tuned in")
	}
	var am tea.Model = m
	am, _ = am.Update(keyMsg("0"))
	gm := asModel(am)
	if gm.agent == nil {
		t.Fatalf("entering AGENT should build the runtime")
	}
	if gm.agent.model != "" {
		t.Errorf("with nothing tuned in the agent model must be empty, got %q", gm.agent.model)
	}
	out := stripANSI(gm.View())
	if !strings.Contains(out, "no model tuned in") {
		t.Errorf("AGENT with nothing tuned in should show the up-front hint:\n%s", out)
	}
	if !strings.Contains(out, "[1]") || !strings.Contains(out, "[2]") {
		t.Errorf("AGENT no-model state should carry the [1]/[2] hint:\n%s", out)
	}
	if strings.Contains(out, "gpt-oss-20b") {
		t.Errorf("AGENT must NOT fall back to a stale default model:\n%s", out)
	}
}

// TestAgentUsesTunedInModel: when a channel is tuned in, the agent runs on THAT model,
// and the heading names it.
func TestAgentUsesTunedInModel(t *testing.T) {
	m := browseSeed(100)
	m.connected = &offer{NodeID: "nyx-home", Model: "qwen3-coder-30b", Online: true}
	var am tea.Model = m
	am, _ = am.Update(keyMsg("0"))
	gm := asModel(am)
	if gm.agent.model != "qwen3-coder-30b" {
		t.Errorf("agent should run on the tuned-in model, got %q", gm.agent.model)
	}
	out := stripANSI(gm.View())
	if !strings.Contains(out, "qwen3-coder-30b") {
		t.Errorf("AGENT heading should name the tuned-in model:\n%s", out)
	}
}

// TestAgentReusesLastTunedModelAfterDisconnect is the core bug fix: tune in -> esc
// (which disconnects, clearing m.connected) -> enter AGENT must reuse the LAST model
// via lastConnected, NOT dead-end on "no model tuned in".
func TestAgentReusesLastTunedModelAfterDisconnect(t *testing.T) {
	m := browseSeed(100)
	// Simulate having tuned in to a band and then disconnected: the disconnect fix keeps
	// the model on lastConnected even though m.connected is now nil.
	m.connected = nil
	m.lastConnected = &offer{NodeID: "demo-node", Model: "gpt-oss-20b", Online: true}
	var am tea.Model = m
	am, _ = am.Update(keyMsg("0"))
	gm := asModel(am)
	if gm.agent == nil || gm.agent.model != "gpt-oss-20b" {
		t.Fatalf("AGENT should reuse the last-tuned model gpt-oss-20b, got %q", agentModelOf(gm))
	}
	out := stripANSI(gm.View())
	if strings.Contains(out, "no model tuned in") {
		t.Errorf("AGENT must NOT dead-end on 'no model' when a band was just tuned in:\n%s", out)
	}
	if !strings.Contains(out, "gpt-oss-20b") {
		t.Errorf("AGENT heading should name the reused model:\n%s", out)
	}
}

// TestAgentResolutionPrefersOpenChannelOverLast: when a channel is open AND a different
// band was tuned earlier, the OPEN channel's model wins (priority (a) over (b)).
func TestAgentResolutionPrefersOpenChannelOverLast(t *testing.T) {
	m := browseSeed(100)
	m.lastConnected = &offer{Model: "gpt-oss-20b"}
	m.connected = &offer{NodeID: "nyx", Model: "qwen3-coder-30b", Online: true}
	if got := m.resolveAgentModel(); got != "qwen3-coder-30b" {
		t.Errorf("open channel model should win, got %q", got)
	}
}

// TestSlashModelOneCandidateAutoSelects: /model with exactly one candidate auto-selects
// it (no picker prompt) and re-points the agent.
func TestSlashModelOneCandidateAutoSelects(t *testing.T) {
	m := browseSeed(100)
	// Exactly one candidate: a single recent band, no on-air discover bands.
	m.offers = nil
	m.bands = nil
	m.recentBands = map[string]bool{"solo-model": true}
	m.connected = nil
	m.lastConnected = nil
	var am tea.Model = m
	am, _ = am.Update(keyMsg("0")) // enter AGENT (no model resolved yet)
	am = typeLine(am, "/model")    // bare /model
	gm := asModel(am)
	if gm.agentPicker {
		t.Errorf("/model with one candidate should NOT open the picker")
	}
	if agentModelOf(gm) != "solo-model" {
		t.Errorf("/model with one candidate should auto-select it, got %q", agentModelOf(gm))
	}
	if !gm.agentPicked {
		t.Errorf("auto-select should mark the model as explicitly picked")
	}
}

// TestSlashModelManyOpensPickerAndSelects: /model with several candidates opens the
// picker; arrowing down + enter re-points the agent at the chosen model.
func TestSlashModelManyOpensPickerAndSelects(t *testing.T) {
	m := browseSeed(100) // browseSeed seeds two on-air bands -> two candidates
	var am tea.Model = m
	am, _ = am.Update(keyMsg("0"))
	am = typeLine(am, "/model")
	gm := asModel(am)
	if !gm.agentPicker {
		t.Fatalf("/model with several candidates should open the picker; rows=%v", gm.agentPickerRows)
	}
	if len(gm.agentPickerRows) < 2 {
		t.Fatalf("picker should list 2+ candidates, got %v", gm.agentPickerRows)
	}
	out := stripANSI(gm.View())
	if !strings.Contains(out, "pick a model") {
		t.Errorf("picker view should render the prompt:\n%s", out)
	}
	want := gm.agentPickerRows[1]
	var pm tea.Model = gm
	pm, _ = pm.Update(keyMsg2(tea.KeyDown)) // move to the second row
	pm, _ = pm.Update(keyMsg2(tea.KeyEnter))
	fm := asModel(pm)
	if fm.agentPicker {
		t.Errorf("enter should close the picker")
	}
	if agentModelOf(fm) != want {
		t.Errorf("selecting row 2 should re-point the agent at %q, got %q", want, agentModelOf(fm))
	}
}

// TestSlashModelPickerChatOnly: the /model picker chooses the agent's BRAIN, so it
// offers CHAT bands only. A voice (tts/stt) station can never serve the chat relay -
// picking one could only fail the next turn - so it must never appear, no matter how
// it is named. The filter rides the band's canonical modality (canonModality mirrors
// the broker's offerModality; band.isVoice is the band-table canon), never a
// model-name guess. E2E on v5.0.0: the picker listed gpt-oss-120b, voice, whisper-1.
func TestSlashModelPickerChatOnly(t *testing.T) {
	cases := []struct {
		name       string
		offers     offersMsg
		wantPicker bool     // whether bare /model opens the modal picker
		wantRows   []string // the exact picker rows when it opens
		wantModel  string   // the agent's model after /model (auto-select); "" = none
	}{
		{
			name: "chat + tts + stt on air: only the chat band is offered (auto-selects)",
			offers: offersMsg{
				{NodeID: "n1", Model: "gpt-oss-120b", PriceOut: 0.3, Online: true},
				{NodeID: "n2", Model: "voice", Modality: protocol.ModalityTTS, Online: true},
				{NodeID: "n3", Model: "whisper-1", Modality: protocol.ModalitySTT, Online: true},
			},
			wantPicker: false,
			wantModel:  "gpt-oss-120b",
		},
		{
			name: "two chat bands + a voice: the picker lists ONLY the chat ones",
			offers: offersMsg{
				{NodeID: "n1", Model: "gpt-oss-20b", PriceOut: 0.3, Online: true},
				{NodeID: "n2", Model: "voice", Modality: protocol.ModalityTTS, Online: true},
				{NodeID: "n3", Model: "llama-3.3-70b-instruct", PriceOut: 0.41, Online: true},
			},
			wantPicker: true,
			wantRows:   []string{"gpt-oss-20b", "llama-3.3-70b-instruct"},
		},
		{
			name: "explicit chat + legacy empty modality both stay offered (back-compat canon)",
			offers: offersMsg{
				{NodeID: "n1", Model: "explicit-chat", Modality: protocol.ModalityChat, PriceOut: 0.3, Online: true},
				{NodeID: "n2", Model: "legacy-empty", PriceOut: 0.41, Online: true},
			},
			wantPicker: true,
			wantRows:   []string{"explicit-chat", "legacy-empty"},
		},
		{
			name: "voice-only air: no candidate at all (the tune-in hint, never a voice brain)",
			offers: offersMsg{
				{NodeID: "n2", Model: "voice", Modality: protocol.ModalityTTS, Online: true},
				{NodeID: "n3", Model: "whisper-1", Modality: protocol.ModalitySTT, Online: true},
			},
			wantPicker: false,
			wantModel:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var tm tea.Model = New("http://broker.local", "tester")
			tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			tm, _ = tm.Update(tc.offers)
			tm, _ = tm.Update(keyMsg("0")) // enter AGENT (nothing tuned in this session)
			tm = typeLine(tm, "/model")
			gm := asModel(tm)
			if gm.agentPicker != tc.wantPicker {
				t.Fatalf("picker open = %v, want %v (rows=%v)", gm.agentPicker, tc.wantPicker, gm.agentPickerRows)
			}
			if tc.wantPicker && !reflect.DeepEqual(gm.agentPickerRows, tc.wantRows) {
				t.Errorf("picker rows = %v, want chat-only %v", gm.agentPickerRows, tc.wantRows)
			}
			if !tc.wantPicker && agentModelOf(gm) != tc.wantModel {
				t.Errorf("agent model after /model = %q, want %q", agentModelOf(gm), tc.wantModel)
			}
			if !tc.wantPicker && tc.wantModel == "" {
				if out := stripANSI(gm.View()); !strings.Contains(out, "no model tuned in") {
					t.Errorf("voice-only air should show the no-model hint, not offer a voice:\n%s", out)
				}
			}
			// A voice station must never surface as an agent brain - not as a picker row
			// and not as an auto-selected model.
			for _, mdl := range append(append([]string{}, gm.agentPickerRows...), agentModelOf(gm)) {
				if mdl == "voice" || mdl == "whisper-1" {
					t.Errorf("voice station %q offered as an agent brain", mdl)
				}
			}
		})
	}
}

// TestAgentPickerKeysGuarded: while the /model picker is open, presets / digits / a
// typed prompt do NOT leak through (the picker is modal and owns its keys).
func TestAgentPickerKeysGuarded(t *testing.T) {
	m := browseSeed(100)
	var am tea.Model = m
	am, _ = am.Update(keyMsg("0"))
	am = typeLine(am, "/model")
	gm := asModel(am)
	if !gm.agentPicker {
		t.Fatalf("expected the picker open")
	}
	// A digit that would otherwise be a preset jump / typed into the prompt is swallowed.
	before := gm.agentPickerCursor
	var pm tea.Model = gm
	pm, _ = pm.Update(keyMsg("1"))
	gm2 := asModel(pm)
	if !gm2.agentPicker || gm2.mode != modeAgent {
		t.Errorf("a digit must not escape the open picker (mode=%d, picker=%v)", gm2.mode, gm2.agentPicker)
	}
	if gm2.agentIn.Value() != "" {
		t.Errorf("a digit must not be typed into the prompt while the picker is open, got %q", gm2.agentIn.Value())
	}
	if gm2.agentPickerCursor != before {
		t.Errorf("a digit must not move the picker cursor")
	}
}

// TestSlashModelNoCandidateShowsHint: /model with truly no model anywhere shows the
// actionable tune-in / share hint, not an empty picker.
func TestSlashModelNoCandidateShowsHint(t *testing.T) {
	m := browseSeed(100)
	m.offers, m.bands, m.recentBands = nil, nil, nil
	m.connected, m.lastConnected = nil, nil
	var am tea.Model = m
	am, _ = am.Update(keyMsg("0"))
	am = typeLine(am, "/model")
	gm := asModel(am)
	if gm.agentPicker {
		t.Errorf("/model with no candidate should NOT open an empty picker")
	}
	out := stripANSI(gm.View())
	if !strings.Contains(out, "no model tuned in") || !strings.Contains(out, "[1]") {
		t.Errorf("/model with no candidate should show the no-model + [1]/[2] hint:\n%s", out)
	}
}

// TestAgentPickerNoColorNarrowSafe: the open picker renders without ANSI under NO_COLOR
// and never overflows narrow widths.
func TestAgentPickerNoColorNarrowSafe(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, w := range []int{40, 50, 64, 80, 120} {
		var am tea.Model = browseSeed(w)
		am, _ = am.Update(tea.WindowSizeMsg{Width: w, Height: 24})
		am, _ = am.Update(keyMsg("0"))
		am = typeLine(am, "/model")
		out := am.View()
		if strings.Contains(out, "\x1b[") {
			t.Errorf("width %d: picker emitted ANSI under NO_COLOR", w)
		}
		for _, line := range strings.Split(out, "\n") {
			if vis := utf8.RuneCountInString(stripANSI(line)); vis > w {
				t.Errorf("width %d: picker line overflows (%d cols): %q", w, vis, stripANSI(line))
			}
		}
	}
}

// agentModelOf reads the agent's current model ("" when the runtime is nil).
func agentModelOf(m model) string {
	if m.agent == nil {
		return ""
	}
	return m.agent.model
}

// keyMsg2 builds a non-rune key (arrows / enter) for driving the picker.
func keyMsg2(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

// typeLine feeds each rune of s into the AGENT prompt then submits with enter (the way
// a user enters a slash command).
func typeLine(m tea.Model, s string) tea.Model {
	for _, r := range s {
		m, _ = m.Update(keyMsg(string(r)))
	}
	m, _ = m.Update(keyMsg2(tea.KeyEnter))
	return m
}

// firstLineContaining returns the first line of s that contains sub ("" if none).
func firstLineContaining(s, sub string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, sub) {
			return line
		}
	}
	return ""
}
