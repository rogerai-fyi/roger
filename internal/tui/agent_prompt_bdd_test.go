package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/harness"
	"rogerai.fm/roger/v6/internal/operator"
)

type agentPromptBDD struct {
	t           *testing.T
	m           model
	lastCmd     tea.Cmd
	copyText    string
	quietOrig   bool
	multiDraft  string
	historyPath string
}

func (s *agentPromptBDD) previousWriteFile() error {
	out, _ := s.m.onAgentEvent(agentEventMsg{Kind: harness.EventToolResult, Tool: "write_file", Result: "ok"})
	s.m = asModel(out)
	return nil
}

func (s *agentPromptBDD) previousError() error {
	out, _ := s.m.onAgentEvent(agentEventMsg{Kind: harness.EventError, Text: "failed"})
	s.m = asModel(out)
	return nil
}

func (s *agentPromptBDD) previousQuestion() error {
	out, _ := s.m.onAgentEvent(agentEventMsg{Kind: harness.EventFinal, Text: "Which target should I use?"})
	s.m = asModel(out)
	return nil
}

func (s *agentPromptBDD) placeholderContains(want string) error {
	if got := s.m.agentPromptPlaceholder(); !strings.Contains(got, want) {
		return fmt.Errorf("placeholder = %q, want %q", got, want)
	}
	return nil
}

func (s *agentPromptBDD) validationPlaceholder() error { return s.placeholderContains("run the tests") }
func (s *agentPromptBDD) recoveryPlaceholder() error   { return s.placeholderContains("fix the error") }
func (s *agentPromptBDD) questionPlaceholder() error {
	return s.placeholderContains("answer the agent's question")
}

func (s *agentPromptBDD) threeLineDraft() error {
	s.multiDraft = "first line\nsecond line\nthird line"
	s.m.agentHist = &inputHistory{entries: []string{"older prompt"}, cursor: 1}
	s.m.agentIn.SetValue(s.multiDraft)
	return nil
}

func (s *agentPromptBDD) cursorLastLine() error {
	if got := s.m.agentIn.Line(); got != 2 {
		return fmt.Errorf("cursor line = %d, want 2", got)
	}
	return nil
}

func (s *agentPromptBDD) pressUp() error {
	out, _ := s.m.onAgentKey(keyMsg("up"))
	s.m = asModel(out)
	return nil
}

func (s *agentPromptBDD) cursorPreviousLine() error {
	if got := s.m.agentIn.Line(); got != 1 {
		return fmt.Errorf("cursor line = %d, want 1", got)
	}
	return nil
}

func (s *agentPromptBDD) draftUnchanged() error {
	if got := s.m.agentIn.Value(); got != s.multiDraft {
		return fmt.Errorf("multiline draft = %q, want %q", got, s.multiDraft)
	}
	return nil
}

func (s *agentPromptBDD) reachFirstThenUp() error {
	for textareaCanMoveUp(s.m.agentIn) {
		if err := s.pressUp(); err != nil {
			return err
		}
	}
	return s.pressUp()
}

func (s *agentPromptBDD) previousPromptRecalled() error {
	if got := s.m.agentIn.Value(); got != "older prompt" {
		return fmt.Errorf("recalled prompt = %q", got)
	}
	return nil
}

func (s *agentPromptBDD) sendThreeLinePrompt() error {
	s.multiDraft = "first line\nsecond line\nthird line"
	s.historyPath = filepath.Join(s.t.TempDir(), "history-agent")
	s.m.agentHist = &inputHistory{path: s.historyPath}
	s.m.agentIn.SetValue(s.multiDraft)
	out, _ := s.m.onAgentKey(keyMsg("enter"))
	s.m = asModel(out)
	return nil
}

func (s *agentPromptBDD) multilineHistoryReloads() error {
	h := &inputHistory{path: s.historyPath}
	h.load()
	if len(h.entries) != 1 || h.entries[0] != s.multiDraft {
		return fmt.Errorf("reloaded entries = %#v, want one exact multiline entry", h.entries)
	}
	return nil
}

func (s *agentPromptBDD) reset() {
	s.m = browseSeed(100)
	s.m.connected = &offer{
		NodeID:       "tools-node",
		Model:        "tools-model",
		Online:       true,
		Capabilities: []string{"tools"},
	}
	out, _ := s.m.Update(keyMsg("0"))
	s.m = asModel(out)
	s.lastCmd = nil
	s.copyText = ""
	s.quietOrig = quiet
}

func (s *agentPromptBDD) enteredAgent() error {
	if s.m.mode != modeAgent || !s.m.agentIn.Focused() {
		return fmt.Errorf("AGENT input is not focused")
	}
	return nil
}

func (s *agentPromptBDD) terminalWidth(w int) error {
	out, _ := s.m.Update(tea.WindowSizeMsg{Width: w, Height: 24})
	s.m = asModel(out)
	return nil
}

func (s *agentPromptBDD) typeLongPrompt() error {
	s.m.agentIn.SetValue(strings.Repeat("x", 120))
	s.m.agentIn.CursorEnd()
	return nil
}

func (s *agentPromptBDD) detectedLandingGuests(_, _, _ string) error {
	for _, g := range operator.Registry()[:3] {
		s.m.operatorDetections = append(s.m.operatorDetections, operator.Detection{
			Guest: g, Path: "/fake/" + g.Bin, Version: g.KnownGood,
		})
	}
	return nil
}

func (s *agentPromptBDD) landingDeskCollapsed() error {
	if got := stripANSI(s.m.deskRosterBlock(s.m.effWidth())); strings.Contains(got, "THE DESK") {
		return fmt.Errorf("decorative landing desk remained while typing:\n%s", got)
	}
	return nil
}

func (s *agentPromptBDD) promptWraps() error {
	for _, line := range s.m.agentPromptLines(s.m.effWidth()) {
		if got := lipgloss.Width(line); got > s.m.effWidth() {
			return fmt.Errorf("prompt row is %d cells wide at width %d: %q", got, s.m.effWidth(), stripANSI(line))
		}
	}
	return nil
}

func (s *agentPromptBDD) promptLossless() error {
	want := strings.Repeat("x", 120)
	if got := s.m.agentIn.Value(); got != want {
		return fmt.Errorf("prompt value = %q, want exact %q", got, want)
	}
	painted := stripANSI(strings.Join(s.m.agentPromptLines(s.m.effWidth()), "\n"))
	if got := strings.Count(painted, "x"); got != 120 {
		return fmt.Errorf("painted prompt contains %d/120 characters:\n%s", got, painted)
	}
	return nil
}

func (s *agentPromptBDD) fillFirstAgentRow() error {
	s.m.width, s.m.height = 170, 32
	out, _ := s.m.Update(tea.WindowSizeMsg{Width: 170, Height: 32})
	s.m = asModel(out)
	runes := []rune(composerGrowthPrompt)
	out, _ = s.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: runes[:150]})
	s.m = asModel(out)
	_ = s.m.View()
	return nil
}

func (s *agentPromptBDD) wrapSecondAgentRow() error {
	runes := []rune(composerGrowthPrompt)
	out, _ := s.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: runes[150:]})
	s.m = asModel(out)
	return nil
}

func (s *agentPromptBDD) agentGrowthVisible() error {
	view := stripANSI(strings.Join(s.m.agentPromptLines(s.m.effWidth()), "\n"))
	for _, fragment := range []string{"can we test how long", "writing on this new line"} {
		if !strings.Contains(view, fragment) {
			return fmt.Errorf("AGENT prompt lost %q while growing:\n%s", fragment, view)
		}
	}
	return nil
}

func (s *agentPromptBDD) noColorAndPipedSafe() error {
	for _, line := range s.m.agentPromptLines(s.m.effWidth()) {
		if strings.Contains(line, "\x1b[") {
			return fmt.Errorf("NO_COLOR prompt leaked ANSI: %q", line)
		}
		if lipgloss.Width(line) > s.m.effWidth() {
			return fmt.Errorf("NO_COLOR prompt overflowed: %q", line)
		}
	}
	return nil
}

func (s *agentPromptBDD) idle() error {
	s.m.agentBusy = false
	s.m.agentPendingConfirm = nil
	return nil
}

func (s *agentPromptBDD) idlePlaceholder() error {
	if got := s.m.agentPromptPlaceholder(); got != "ask the agent to do something" {
		return fmt.Errorf("placeholder = %q", got)
	}
	return nil
}

func (s *agentPromptBDD) previousToolResult() error {
	out, _ := s.m.Update(agentEventMsg{Kind: harness.EventToolResult, Tool: "list_dir", Result: "ok"})
	s.m = asModel(out)
	return nil
}

func (s *agentPromptBDD) continuationPlaceholder() error {
	got := s.m.agentPromptPlaceholder()
	if !strings.Contains(got, "continue") && !strings.Contains(got, "next step") {
		return fmt.Errorf("continuation placeholder = %q", got)
	}
	return nil
}

func (s *agentPromptBDD) pendingMutatingTool() error {
	resp := make(chan bool, 1)
	out, _ := s.m.Update(agentConfirmMsg(agentConfirm{
		tool: "run_shell",
		args: map[string]any{"cmd": strings.Repeat("printf safe; ", 12)},
		resp: resp,
	}))
	s.m = asModel(out)
	return nil
}

func (s *agentPromptBDD) confirmInstruction(_ string) error {
	view := stripANSI(s.m.agentView(s.m.effWidth()))
	if !strings.Contains(view, "[y/N]") || !strings.Contains(view, "deny=default") {
		return fmt.Errorf("confirm instruction missing:\n%s", view)
	}
	return nil
}

func (s *agentPromptBDD) confirmLamp() error {
	view := s.m.agentView(s.m.effWidth())
	if !strings.Contains(stripANSI(view), "● APPROVAL REQUIRED") {
		return fmt.Errorf("approval lamp missing:\n%s", stripANSI(view))
	}
	if !quiet && !strings.Contains(view, "\x1b[") {
		return fmt.Errorf("approval lamp has no accent styling")
	}
	return nil
}

func (s *agentPromptBDD) pasteArmed() error { return nil }

func (s *agentPromptBDD) pasteMultiline() error {
	paste := "printf 'one\\n'\nprintf 'two\\n'"
	out, _ := s.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(paste), Paste: true})
	s.m = asModel(out)
	return nil
}

func (s *agentPromptBDD) pastedTextIntact() error {
	want := "printf 'one\\n'\nprintf 'two\\n'"
	if got := s.m.agentIn.Value(); got != want {
		return fmt.Errorf("pasted value = %q, want %q", got, want)
	}
	return nil
}

func (s *agentPromptBDD) transcriptContent() error {
	s.m.agentLines = append(s.m.agentLines, agentAnswerMark+"**answer**\n```go\nfmt.Println(\"ok\")\n```")
	return nil
}

func (s *agentPromptBDD) pressCopy() error {
	s.copyText = s.m.agentTranscriptText()
	out, cmd := s.m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	s.m, s.lastCmd = asModel(out), cmd
	return nil
}

func (s *agentPromptBDD) transcriptCopied() error {
	if s.lastCmd == nil || !strings.Contains(s.copyText, "```go") {
		return fmt.Errorf("copy did not preserve the canonical transcript")
	}
	return nil
}

func (s *agentPromptBDD) copyToast() error {
	if !strings.Contains(stripANSI(s.m.status), "Copied to clipboard") {
		return fmt.Errorf("copy toast missing: %q", stripANSI(s.m.status))
	}
	return nil
}

func (s *agentPromptBDD) inputStillFocused() error {
	if !s.m.agentIn.Focused() {
		return fmt.Errorf("input lost focus")
	}
	return nil
}

func (s *agentPromptBDD) transcriptLine() error { return s.transcriptContent() }

func (s *agentPromptBDD) dimSeam() error {
	if !strings.Contains(stripANSI(s.m.agentView(s.m.effWidth())), "── ask ") {
		return fmt.Errorf("idle prompt seam missing")
	}
	return nil
}

func (s *agentPromptBDD) labeledSeam() error {
	s.m.agentPaneFocus = true
	view := stripANSI(s.m.agentView(s.m.effWidth()))
	if !strings.Contains(view, "● transcript") {
		return fmt.Errorf("focused seam is not lit/labeled:\n%s", view)
	}
	return nil
}

func (s *agentPromptBDD) runShellPending() error { return s.pendingMutatingTool() }

func (s *agentPromptBDD) wrappedCommandAndConfirm(_ string) error {
	view := stripANSI(s.m.agentView(s.m.effWidth()))
	if !strings.Contains(view, "printf safe") || !strings.Contains(view, "[y/N]") ||
		!strings.Contains(view, "deny=default") {
		return fmt.Errorf("wrapped command/confirm missing:\n%s", view)
	}
	return nil
}

func (s *agentPromptBDD) redConfirmRow() error { return s.confirmLamp() }

func (s *agentPromptBDD) noColor() error {
	quiet = true
	s.t.Setenv("NO_COLOR", "1")
	s.t.Cleanup(func() { quiet = s.quietOrig })
	return nil
}

func (s *agentPromptBDD) allStatesSafe() error {
	s.m.agentLines = append(s.m.agentLines, agentAnswerMark+"answer")
	s.m.agentIn.SetValue(strings.Repeat("界", 40))
	if err := s.promptWraps(); err != nil {
		return err
	}
	view := s.m.agentView(s.m.effWidth())
	if strings.Contains(view, "\x1b[") {
		return fmt.Errorf("NO_COLOR view leaked ANSI")
	}
	return nil
}

func (s *agentPromptBDD) sessionTotals(_ int, _ int, _ string) error {
	s.m.agentTokensIn = 1200
	s.m.agentTokensOut = 340
	s.m.agentCost = 0.05
	return nil
}

func (s *agentPromptBDD) turnStep(step, max int) error {
	s.m.agentStep, s.m.agentMaxSteps = step, max
	return nil
}

func (s *agentPromptBDD) deckShowsRail() error {
	deck := stripANSI(strings.SplitN(s.m.agentView(s.m.effWidth()), "\n", 2)[0])
	for _, want := range []string{"SESSION", "STEPS 3/8", "SPENT $0.05"} {
		if !strings.Contains(deck, want) {
			return fmt.Errorf("wide deck lacks %q: %q", want, deck)
		}
	}
	return nil
}

func (s *agentPromptBDD) factsAppearOnce() error {
	deck := stripANSI(strings.SplitN(s.m.agentView(s.m.effWidth()), "\n", 2)[0])
	for _, fact := range []string{"SESSION", "STEPS", "SPENT"} {
		if strings.Count(deck, fact) != 1 {
			return fmt.Errorf("%s appears %d times in deck %q", fact, strings.Count(deck, fact), deck)
		}
	}
	return nil
}

func (s *agentPromptBDD) mediumDeck() error {
	deck := stripANSI(strings.SplitN(s.m.agentView(s.m.effWidth()), "\n", 2)[0])
	if strings.Contains(deck, "SESSION") || !strings.Contains(deck, "3/8") || !strings.Contains(deck, "$0.05") {
		return fmt.Errorf("medium deck did not collapse its rail: %q", deck)
	}
	return nil
}

func (s *agentPromptBDD) narrowDeckFits() error {
	line := strings.SplitN(s.m.agentView(s.m.effWidth()), "\n", 2)[0]
	if lipgloss.Width(line) > s.m.effWidth() {
		return fmt.Errorf("narrow deck is %d cells at width %d: %q", lipgloss.Width(line), s.m.effWidth(), stripANSI(line))
	}
	return nil
}

func (s *agentPromptBDD) noBodyTutorial() error {
	body := stripANSI(s.m.agentView(s.m.effWidth()))
	if strings.Contains(body, "enter asks") || strings.Contains(body, "enter ask · /model") {
		return fmt.Errorf("AGENT body still contains duplicated tutorial:\n%s", body)
	}
	return nil
}

func (s *agentPromptBDD) footerTeachesActions() error {
	footer := strings.ToLower(stripANSI(s.m.footer(s.m.effWidth())))
	for _, want := range []string{"ask", "copy", "perms", "transcript", "exit"} {
		if !strings.Contains(footer, want) {
			return fmt.Errorf("AGENT footer lacks %q: %q", want, footer)
		}
	}
	return nil
}

func (s *agentPromptBDD) noAgentTimeout() error {
	s.t.Setenv("ROGERAI_AGENT_TIMEOUT", "")
	s.m.agent.callLimit = agentTimeoutFromEnv()
	return nil
}

func (s *agentPromptBDD) configuredTimeout(v string) error {
	s.t.Setenv("ROGERAI_AGENT_TIMEOUT", v)
	s.m.agent.callLimit = agentTimeoutFromEnv()
	return nil
}

func (s *agentPromptBDD) modelWorkingFor(sec int) error {
	s.m.agentBusy = true
	s.m.agent.callMu.Lock()
	s.m.agent.callStart = time.Now().Add(-time.Duration(sec) * time.Second)
	if s.m.agent.callLimit > 0 {
		s.m.agent.callSoft = s.m.agent.callStart.Add(s.m.agent.callLimit)
		s.m.agent.callExtend = func(time.Duration) {}
	}
	s.m.agent.callMu.Unlock()
	return nil
}

func (s *agentPromptBDD) workingReadsUnlimited() error {
	if got := stripANSI(s.m.agentWorkingLine(301, 301)); !strings.Contains(got, "unlimited") {
		return fmt.Errorf("working line does not disclose unlimited duration: %q", got)
	}
	return nil
}

func (s *agentPromptBDD) noCapArmed() error {
	_, _, past, _ := s.m.agent.callState()
	if past || s.m.agent.callExtend != nil {
		return fmt.Errorf("unlimited call armed cap state")
	}
	return nil
}

func (s *agentPromptBDD) configuredCap() error {
	if got := stripANSI(s.m.agentWorkingLine(601, 601)); !strings.Contains(got, "600s cap") {
		return fmt.Errorf("working line lacks configured cap: %q", got)
	}
	return nil
}

func (s *agentPromptBDD) slowControls() error {
	got := stripANSI(s.m.agentWorkingLine(601, 601))
	for _, want := range []string{"slow call", "tab", "esc"} {
		if !strings.Contains(got, want) {
			return fmt.Errorf("configured timeout line lacks %q: %q", want, got)
		}
	}
	return nil
}

func (s *agentPromptBDD) callsReadFile(path string) error {
	out, _ := s.m.Update(agentEventMsg{Kind: harness.EventToolCall, Tool: "read_file", Args: map[string]any{"path": path}})
	s.m = asModel(out)
	return nil
}

func (s *agentPromptBDD) oneRunningCard() error {
	view := stripANSI(s.m.agentView(s.m.effWidth()))
	if strings.Count(view, "read_file") != 1 || !strings.Contains(view, "running") {
		return fmt.Errorf("running activity was not one card:\n%s", view)
	}
	return nil
}

func (s *agentPromptBDD) toolSucceeds(n int) error {
	out, _ := s.m.Update(agentEventMsg{Kind: harness.EventToolResult, Tool: "read_file", Result: strings.Repeat("x", n)})
	s.m = asModel(out)
	return nil
}

func (s *agentPromptBDD) successCard() error {
	// AMENDED 2026-08-20: tool cards fold behind a ⌃o box by default (founder). This
	// scenario is about what the CARD says once you look at it - the stateful
	// running→success transition - so it opens the box. Whether cards are hidden by
	// default is owned by TestToolMachineryFoldsByDefault.
	s.m.showToolCalls = true
	view := stripANSI(s.m.agentView(s.m.effWidth()))
	card := ""
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "✓ read_file") {
			card = line
			break
		}
	}
	for _, want := range []string{"✓", "read_file", "internal/tui/agent.go", "16 bytes"} {
		if !strings.Contains(card, want) {
			return fmt.Errorf("success card lacks %q:\n%s", want, card)
		}
	}
	if strings.Contains(card, "running") {
		return fmt.Errorf("completed card still reads running:\n%s", card)
	}
	return nil
}

func (s *agentPromptBDD) outputBehindToggle(_ string) error {
	if !strings.Contains(stripANSI(s.m.agentView(s.m.effWidth())), "d·output") {
		return fmt.Errorf("activity card does not advertise output toggle")
	}
	return nil
}

func TestAgentPromptBDD(t *testing.T) {
	st := &agentPromptBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^I have entered AGENT mode with a tools-capable band tuned in$`, st.enteredAgent)
			sc.Step(`^the terminal is (\d+) columns wide$`, st.terminalWidth)
			sc.Step(`^I type a 120-character prompt$`, st.typeLongPrompt)
			sc.Step(`^detected guests "([^"]*)", "([^"]*)" and "([^"]*)"$`, st.detectedLandingGuests)
			sc.Step(`^the input line wraps within the 80-col width$`, st.promptWraps)
			sc.Step(`^no characters are lost off the right edge$`, st.promptLossless)
			sc.Step(`^I rapidly type enough text to fill one AGENT row$`, st.fillFirstAgentRow)
			sc.Step(`^the next input chunk wraps onto a second AGENT row$`, st.wrapSecondAgentRow)
			sc.Step(`^both the beginning and continuation of the AGENT prompt remain visible$`, st.agentGrowthVisible)
			sc.Step(`^the decorative landing desk collapses while I am typing$`, st.landingDeskCollapsed)
			sc.Step(`^the same holds under NO_COLOR and when piped$`, st.noColorAndPipedSafe)
			sc.Step(`^no turn is in flight and no confirm is pending$`, st.idle)
			sc.Step(`^the placeholder reads "ask the agent to do something"$`, st.idlePlaceholder)
			sc.Step(`^the previous turn ended with a tool result$`, st.previousToolResult)
			sc.Step(`^the placeholder reads something like "run the next step" or "continue"$`, st.continuationPlaceholder)
			sc.Step(`^the previous turn successfully wrote a file$`, st.previousWriteFile)
			sc.Step(`^the placeholder suggests running tests or reviewing the change$`, st.validationPlaceholder)
			sc.Step(`^the previous turn ended with an error$`, st.previousError)
			sc.Step(`^the placeholder suggests retrying or fixing the error$`, st.recoveryPlaceholder)
			sc.Step(`^the previous answer ended with a question$`, st.previousQuestion)
			sc.Step(`^the placeholder suggests answering the agent's question$`, st.questionPlaceholder)
			sc.Step(`^a mutating tool is waiting for approval$`, st.pendingMutatingTool)
			sc.Step(`^the placeholder \(or the prompt line itself\) shows "([^"]*)"$`, st.confirmInstruction)
			sc.Step(`^a red accent bar or lamp makes the confirm impossible to miss$`, st.confirmLamp)
			sc.Step(`^bracketed paste is armed$`, st.pasteArmed)
			sc.Step(`^I paste a multi-line command into the ask prompt$`, st.pasteMultiline)
			sc.Step(`^the full text appears in the input without corruption$`, st.pastedTextIntact)
			sc.Step(`^the prompt still wraps correctly$`, st.promptWraps)
			sc.Step(`^the ask prompt contains three logical lines$`, st.threeLineDraft)
			sc.Step(`^the cursor is on the last line$`, st.cursorLastLine)
			sc.Step(`^I press up$`, st.pressUp)
			sc.Step(`^the cursor moves to the previous prompt line$`, st.cursorPreviousLine)
			sc.Step(`^the multiline draft is not replaced by history$`, st.draftUnchanged)
			sc.Step(`^the cursor reaches the first visual line and I press up$`, st.reachFirstThenUp)
			sc.Step(`^the previous sent prompt is recalled$`, st.previousPromptRecalled)
			sc.Step(`^I send a three-line AGENT prompt$`, st.sendThreeLinePrompt)
			sc.Step(`^reloading AGENT history returns the exact three-line prompt as one entry$`, st.multilineHistoryReloads)
			sc.Step(`^the transcript has content$`, st.transcriptContent)
			sc.Step(`^I press ctrl\+y$`, st.pressCopy)
			sc.Step(`^the agent transcript is copied to the clipboard$`, st.transcriptCopied)
			sc.Step(`^the "✓ Copied to clipboard" toast appears$`, st.copyToast)
			sc.Step(`^focus remains on the input$`, st.inputStillFocused)
			sc.Step(`^the transcript has at least one line$`, st.transcriptLine)
			sc.Step(`^a dim hairline seam "──" is shown above the prompt$`, st.dimSeam)
			sc.Step(`^when the user has scrolled up or transcript focus is active the seam is lit and labeled$`, st.labeledSeam)
			sc.Step(`^a run_shell confirm is pending$`, st.runShellPending)
			sc.Step(`^the prompt row shows the full command \(soft-wrapped\) plus "([^"]*)"$`, st.wrappedCommandAndConfirm)
			sc.Step(`^the row uses the red accent so it stands out from normal transcript lines$`, st.redConfirmRow)
			sc.Step(`^NO_COLOR is active$`, st.noColor)
			sc.Step(`^every prompt, seam, confirm, and placeholder still renders without overflow or color leakage$`, st.allStatesSafe)
			sc.Step(`^the input still wraps$`, st.promptWraps)
			sc.Step(`^the session has billed (\d+) input tokens, (\d+) output tokens, and spent "([^"]*)"$`, st.sessionTotals)
			sc.Step(`^the current turn is on step (\d+) of (\d+)$`, st.turnStep)
			sc.Step(`^the deck shows "SESSION", "STEPS 3/8", and "SPENT \$0.05"$`, st.deckShowsRail)
			sc.Step(`^each session fact appears only once in the deck$`, st.factsAppearOnce)
			sc.Step(`^the session facts remain in the deck without a detached right rail$`, st.mediumDeck)
			sc.Step(`^the compact session reading fits without overflow$`, st.narrowDeckFits)
			sc.Step(`^the AGENT body has no duplicated idle command tutorial$`, st.noBodyTutorial)
			sc.Step(`^the global AGENT footer teaches ask, copy, permissions, transcript focus, and exit$`, st.footerTeachesActions)
			sc.Step(`^no AGENT timeout is configured$`, st.noAgentTimeout)
			sc.Step(`^the AGENT timeout is configured as "([^"]*)"$`, st.configuredTimeout)
			sc.Step(`^a model call has been working for (\d+) seconds$`, st.modelWorkingFor)
			sc.Step(`^the working rail reads "unlimited"$`, st.workingReadsUnlimited)
			sc.Step(`^no cap warning or automatic stop is armed$`, st.noCapArmed)
			sc.Step(`^the working rail shows the configured 600s cap$`, st.configuredCap)
			sc.Step(`^the slow-call extension and stop controls are offered$`, st.slowControls)
			sc.Step(`^the agent calls read_file for "([^"]*)"$`, st.callsReadFile)
			sc.Step(`^one running activity card is shown$`, st.oneRunningCard)
			sc.Step(`^that tool succeeds with (\d+) bytes$`, st.toolSucceeds)
			sc.Step(`^the same card becomes a green success card with its target and size$`, st.successCard)
			sc.Step(`^full output remains behind the "([^"]*)" toggle$`, st.outputBehindToggle)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/tui/agent_prompt_fixes.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("agent prompt scenarios failed")
	}
}
