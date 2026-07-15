package tui

// Founder-approved spec: slash-command autocomplete for the [0] AGENT `ask ›` prompt.
//   - Typing "/" alone shows a passive one-line strip listing ALL commands, sorted,
//     directly ABOVE the input; a longer word narrows it to case-insensitive
//     PREFIX matches (start of the command word only).
//   - Tab completes: a unique match fills the input + a trailing space; several
//     matches CYCLE Minecraft-style on repeated Tab (wrapping), the strip carating
//     the current pick in the house selection treatment.
//   - The strip hides once the command word is complete and followed by a space
//     (typing args is untouched) or when the input is not a slash command; Tab is
//     then a no-op. No new keybinds beyond Tab; esc + enter keep their behavior.
//   - ONE registry (agentCommands, beside the runAgentCommand switch) feeds the
//     strip, so every suggested command really dispatches - never "unknown:".

import (
	"sort"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// agentReady enters the [0] AGENT off a seeded BROWSE via the same real-message path
// a user takes (pressing 0), returning the tea.Model ready for typed keys.
func agentReady(t *testing.T) tea.Model {
	t.Helper()
	var tm tea.Model = browseSeed(120)
	tm, _ = tm.Update(keyMsg("0"))
	if asModel(tm).mode != modeAgent {
		t.Fatalf("0 should enter modeAgent, got mode %d", asModel(tm).mode)
	}
	return tm
}

// typeRunes feeds s one rune per KeyMsg - key-by-key, like real typing.
func typeRunes(tm tea.Model, s string) tea.Model {
	for _, r := range s {
		tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return tm
}

// pressTab sends a real Tab key press.
func pressTab(tm tea.Model) tea.Model {
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyTab})
	return tm
}

// lineAbovePrompt returns the rendered line directly ABOVE the `ask ›` input line,
// trimmed - the strip's specced position. "" means the blank separator, i.e. NO strip.
func lineAbovePrompt(t *testing.T, tm tea.Model) string {
	t.Helper()
	view := stripANSI(asModel(tm).View())
	lines := strings.Split(view, "\n")
	for i, ln := range lines {
		if strings.Contains(ln, "ask ›") {
			if i == 0 {
				return ""
			}
			return strings.TrimSpace(lines[i-1])
		}
	}
	t.Fatalf("no `ask ›` prompt line in the AGENT view:\n%s", view)
	return ""
}

// allCommandsStrip is the full sorted registry as the strip renders it on a bare "/".
const allCommandsStrip = "/clear · /commands · /copy · /help · /model · /operator · /perms · /persona · /remote-control"

// TestAgentSlashStripMatrix drives the strip with real typed keys: which commands it
// suggests for what input, and when it hides. Matching is case-insensitive and
// PREFIX-only; the strip renders on the one hint line directly above the input.
func TestAgentSlashStripMatrix(t *testing.T) {
	cases := []struct {
		name  string
		typed string
		want  string // "" = NO strip (the blank separator above the prompt)
	}{
		{"bare slash lists ALL commands sorted", "/", allCommandsStrip},
		{"slash-p lists both p-commands", "/p", "/perms · /persona"},
		{"slash-c narrows to clear commands copy", "/c", "/clear · /commands · /copy"},
		{"case-insensitive capital P", "/P", "/perms · /persona"},
		{"case-insensitive capital CL", "/CL", "/clear"},
		{"no matches hides the strip", "/zz", ""},
		{"leading text is not a slash command", "hello /p", ""},
		{"complete word plus space hides while typing args", "/model ", ""},
		{"args after the word stay strip-free", "/model up", ""},
		{"empty input has no strip", "", ""},
		{"complete word without a space still shows", "/remote-control", "/remote-control"},
		{"leading spaces match enter's trim", " /p", "/perms · /persona"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tm := typeRunes(agentReady(t), tc.typed)
			if got := lineAbovePrompt(t, tm); got != tc.want {
				t.Fatalf("typed %q: strip above the prompt = %q, want %q", tc.typed, got, tc.want)
			}
		})
	}
}

// TestAgentSlashTabUnique: Tab on a unique match fills the input with the command plus
// a trailing space (ready for args / enter) and the strip hides; a second Tab is a
// no-op (the word is complete and spaced - nothing to complete, nothing inserted).
func TestAgentSlashTabUnique(t *testing.T) {
	tm := typeRunes(agentReady(t), "/pers")
	tm = pressTab(tm)
	if got := asModel(tm).agentIn.Value(); got != "/persona " {
		t.Fatalf("Tab on the unique match /pers should fill %q, got %q", "/persona ", got)
	}
	if got := lineAbovePrompt(t, tm); got != "" {
		t.Fatalf("strip should hide after a completed word + space, still shows %q", got)
	}
	tm = pressTab(tm)
	if got := asModel(tm).agentIn.Value(); got != "/persona " {
		t.Fatalf("Tab after completion must be a no-op, input became %q", got)
	}
}

// TestAgentSlashTabCycleWraps: several matches CYCLE Minecraft-style on repeated Tab -
// the input steps /clear -> /commands -> /copy -> back to /clear (wrap-around) - and
// the strip keeps showing all three with the current pick carated (the house picker
// selection treatment: red + carat, carat-only under NO_COLOR).
func TestAgentSlashTabCycleWraps(t *testing.T) {
	tm := typeRunes(agentReady(t), "/c")
	if got := lineAbovePrompt(t, tm); got != "/clear · /commands · /copy" {
		t.Fatalf("before Tab the strip lists the /c matches uncarated, got %q", got)
	}
	steps := []struct{ value, strip string }{
		{"/clear", "▸ /clear · /commands · /copy"},
		{"/commands", "/clear · ▸ /commands · /copy"},
		{"/copy", "/clear · /commands · ▸ /copy"},
		{"/clear", "▸ /clear · /commands · /copy"}, // wraps around
	}
	for i, st := range steps {
		tm = pressTab(tm)
		if got := asModel(tm).agentIn.Value(); got != st.value {
			t.Fatalf("Tab #%d should fill %q, got %q", i+1, st.value, got)
		}
		if got := lineAbovePrompt(t, tm); got != st.strip {
			t.Fatalf("Tab #%d strip = %q, want %q (current pick carated)", i+1, got, st.strip)
		}
	}
}

// TestAgentSlashTabNoOpMatrix: Tab outside a completable slash word changes nothing -
// no candidates, plain chat text, args already being typed, or an empty input. The
// input must stay byte-identical (no literal tab inserted) and the strip stays hidden.
func TestAgentSlashTabNoOpMatrix(t *testing.T) {
	cases := []struct{ name, typed string }{
		{"no matches", "/zz"},
		{"chat text", "hello"},
		{"typing args", "/model x"},
		{"empty input", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tm := typeRunes(agentReady(t), tc.typed)
			tm = pressTab(tm)
			if got := asModel(tm).agentIn.Value(); got != tc.typed {
				t.Fatalf("Tab must be a no-op on %q, input became %q", tc.typed, got)
			}
			if got := lineAbovePrompt(t, tm); got != "" {
				t.Fatalf("no strip should show for %q, got %q", tc.typed, got)
			}
		})
	}
}

// TestAgentSlashCycleResets: any non-Tab key ends the cycle and the strip re-derives
// from what is actually typed - more typing narrows or hides it, backspace re-widens
// it, and a typed space finalizes the word (strip gone, args mode).
func TestAgentSlashCycleResets(t *testing.T) {
	// Typing after a cycle fill keeps editing the filled word; a non-matching word
	// hides the strip and turns Tab back into a no-op.
	tm := typeRunes(agentReady(t), "/c")
	tm = pressTab(tm) // -> /clear (cycling)
	tm = typeRunes(tm, "x")
	if got := asModel(tm).agentIn.Value(); got != "/clearx" {
		t.Fatalf("typing during a cycle should edit the filled word, got %q", got)
	}
	if got := lineAbovePrompt(t, tm); got != "" {
		t.Fatalf("strip should hide on a non-matching word, still shows %q", got)
	}
	tm = pressTab(tm)
	if got := asModel(tm).agentIn.Value(); got != "/clearx" {
		t.Fatalf("Tab on a non-matching word must be a no-op, got %q", got)
	}

	// Backspace mid-cycle re-derives from the edited word: "/commands" -> "/command"
	// shows the single match uncarated (the cycle ended), then Tab unique-completes.
	tm2 := typeRunes(agentReady(t), "/c")
	tm2 = pressTab(tm2) // /clear
	tm2 = pressTab(tm2) // /commands
	tm2, _ = tm2.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := asModel(tm2).agentIn.Value(); got != "/command" {
		t.Fatalf("backspace should edit the cycled fill, got %q", got)
	}
	if got := lineAbovePrompt(t, tm2); got != "/commands" {
		t.Fatalf("strip should re-derive uncarated after backspace, got %q", got)
	}
	tm2 = pressTab(tm2)
	if got := asModel(tm2).agentIn.Value(); got != "/commands " {
		t.Fatalf("Tab should unique-complete after the cycle reset, got %q", got)
	}

	// A typed space right after a cycle fill finalizes the word: strip hidden.
	tm3 := typeRunes(agentReady(t), "/c")
	tm3 = pressTab(tm3) // /clear
	tm3 = typeRunes(tm3, " ")
	if got := asModel(tm3).agentIn.Value(); got != "/clear " {
		t.Fatalf("a typed space should land after the filled word, got %q", got)
	}
	if got := lineAbovePrompt(t, tm3); got != "" {
		t.Fatalf("a space after the word should hide the strip, still shows %q", got)
	}
}

// TestAgentSlashEnterUnchanged: enter still just runs what is in the input - a
// Tab-completed command dispatches for real (/clear clears), a pick only the strip
// could have suggested dispatches too (/commands lists the commands), and an unknown
// command still prints the existing "unknown: X · /help" hint verbatim.
func TestAgentSlashEnterUnchanged(t *testing.T) {
	// Tab-completed /clear + enter really clears the session.
	tm := typeRunes(agentReady(t), "/c")
	tm = pressTab(tm) // -> /clear
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gm := asModel(tm)
	if gm.agentIn.Value() != "" {
		t.Fatalf("enter should submit and empty the input, got %q", gm.agentIn.Value())
	}
	out := stripANSI(gm.View())
	if !strings.Contains(out, "session cleared") {
		t.Fatalf("Tab-completed /clear + enter should run /clear:\n%s", out)
	}
	if got := lineAbovePrompt(t, tm); got != "" {
		t.Fatalf("strip should be gone after enter, still shows %q", got)
	}

	// /commands (a strip suggestion) dispatches to the command list - NOT "unknown:".
	tm2 := typeRunes(agentReady(t), "/commands")
	tm2, _ = tm2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	out2 := stripANSI(asModel(tm2).View())
	if strings.Contains(out2, "unknown:") {
		t.Fatalf("/commands is suggested by the strip so it must dispatch, got unknown:\n%s", out2)
	}
	if !strings.Contains(out2, "/model switches model") {
		t.Fatalf("/commands should print the AGENT command list:\n%s", out2)
	}

	// Unknown commands keep the existing hint line verbatim.
	tm3 := typeRunes(agentReady(t), "/zz")
	tm3, _ = tm3.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if out3 := stripANSI(asModel(tm3).View()); !strings.Contains(out3, "unknown: /zz · /help for AGENT commands") {
		t.Fatalf("the unknown-command hint must stay unchanged:\n%s", out3)
	}
}

// TestAgentCommandRegistrySeam: agentCommands is the ONE registry the strip suggests
// from, extracted beside the runAgentCommand switch. Every entry is a lowercase slash
// word, the list is sorted and deduped, and - the single-source guarantee - every
// entry really dispatches (a suggested command can never come back "unknown:").
func TestAgentCommandRegistrySeam(t *testing.T) {
	if len(agentCommands) == 0 {
		t.Fatalf("agentCommands must not be empty")
	}
	if !sort.StringsAreSorted(agentCommands) {
		t.Fatalf("agentCommands must be sorted: %v", agentCommands)
	}
	seen := map[string]bool{}
	for _, c := range agentCommands {
		if !strings.HasPrefix(c, "/") || c != strings.ToLower(c) || strings.ContainsAny(c, " \t") {
			t.Fatalf("registry entries are lowercase slash words, got %q", c)
		}
		if seen[c] {
			t.Fatalf("duplicate registry entry %q", c)
		}
		seen[c] = true
	}
	for _, want := range strings.Split(allCommandsStrip, " · ") {
		if !seen[want] {
			t.Fatalf("registry must list %s, got %v", want, agentCommands)
		}
	}
	// Single-source guarantee: every suggested command dispatches in runAgentCommand.
	for _, c := range agentCommands {
		t.Run(c, func(t *testing.T) {
			am := asModel(agentReady(t))
			nm, _ := am.runAgentCommand(c)
			if view := stripANSI(asModel(nm).View()); strings.Contains(view, "unknown:") {
				t.Fatalf("registry command %s must dispatch, runAgentCommand said unknown:\n%s", c, view)
			}
		})
	}
}
