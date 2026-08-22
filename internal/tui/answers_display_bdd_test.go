package tui

// answers_display_bdd_test.go makes features/answers/tui_display.feature EXECUTABLE
// against the REAL transcript helpers (toolArgSummary / previewableTool / the derived
// toolset line). They are pure functions over the live harness toolset, so no bubbletea
// program is needed - the suite points XDG_CONFIG_HOME at a temp dir and writes the real
// search.json, exactly as the harness suites do, so "configured" and "not configured" are
// the real on-disk states.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/harness"
)

type answersDisplayState struct {
	t       *testing.T
	summary string
	line    string
	offers  []offer
	warning []string
	model   model
	copied  string
}

func (s *answersDisplayState) reset() {
	s.summary, s.line = "", ""
	s.offers, s.warning = nil, nil
	s.model, s.copied = model{}, ""
	_ = os.RemoveAll(filepath.Join(s.configDir(), "rogerai", "search.json"))
}

func (s *answersDisplayState) configDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		s.t.Fatalf("UserConfigDir: %v", err)
	}
	_ = os.MkdirAll(filepath.Join(d, "rogerai"), 0o755)
	return d
}

func (s *answersDisplayState) providerConfigured() error {
	b, _ := json.Marshal(map[string]string{"provider": "brave", "key": "BSA-test-key"})
	return os.WriteFile(filepath.Join(s.configDir(), "rogerai", "search.json"), b, 0o600)
}

func (s *answersDisplayState) noProviderConfigured() error {
	return os.RemoveAll(filepath.Join(s.configDir(), "rogerai", "search.json"))
}

// --- call-line summaries ------------------------------------------------------

func (s *answersDisplayState) callSearchWithQuery(q string) error {
	s.summary = toolArgSummary("web_search", map[string]any{"query": q})
	return nil
}

func (s *answersDisplayState) callSearchLongQuery() error {
	s.summary = toolArgSummary("web_search", map[string]any{"query": strings.Repeat("valkey backoff ", 60)})
	return nil
}

func (s *answersDisplayState) callFetchWithURL(u string) error {
	s.summary = toolArgSummary("web_fetch", map[string]any{"url": u})
	return nil
}

func (s *answersDisplayState) summaryIs(want string) error {
	if s.summary != want {
		return fmt.Errorf("call line summary = %q, want %q", s.summary, want)
	}
	return nil
}

func (s *answersDisplayState) summaryClipped() error {
	if s.summary == "" {
		return fmt.Errorf("a long query produced no summary at all")
	}
	if strings.ContainsAny(s.summary, "\n\r") {
		return fmt.Errorf("the summary spans multiple lines: %q", s.summary)
	}
	if len([]rune(s.summary)) >= 900 {
		return fmt.Errorf("the summary is %d runes, want it clipped to one line", len([]rune(s.summary)))
	}
	return nil
}

// --- previews -----------------------------------------------------------------

func (s *answersDisplayState) isPreviewable(tool string) error {
	if !previewableTool(tool) {
		return fmt.Errorf("%q should be previewable (a read-only tool shows what the user asked to see)", tool)
	}
	return nil
}

func (s *answersDisplayState) isNotPreviewable(tool string) error {
	if previewableTool(tool) {
		return fmt.Errorf("%q should NOT be previewable", tool)
	}
	return nil
}

// --- the band capability warning --------------------------------------------------

func (s *answersDisplayState) bandLacksTools() error {
	// A published set that DID get determined, and tools is not in it.
	s.offers = []offer{{Model: "gpt-oss-20b", Online: true, Capabilities: []string{"vision"}}}
	return nil
}

func (s *answersDisplayState) bandHasNoCapabilitySet() error {
	s.offers = []offer{{Model: "gpt-oss-20b", Online: true}}
	return nil
}

func (s *answersDisplayState) modelNotInOffers() error {
	s.offers = []offer{{Model: "some-other-band", Online: true, Capabilities: []string{"vision"}}}
	return nil
}

func (s *answersDisplayState) bandHasTools() error {
	s.offers = []offer{{Model: "gpt-oss-20b", Online: true, Capabilities: []string{"tools"}}}
	return nil
}

func (s *answersDisplayState) entersAgent() error {
	s.warning = agentBandToolsWarning(s.offers, "gpt-oss-20b", false)
	// The narrow (small-terminal) hint must carry the same meaning, not silently drop it.
	if narrow := agentBandToolsWarning(s.offers, "gpt-oss-20b", true); len(s.warning) != len(narrow) {
		return fmt.Errorf("the narrow layout shows %d lines, want the same %d", len(narrow), len(s.warning))
	}
	return nil
}

func (s *answersDisplayState) saysCannotDriveTools() error {
	if len(s.warning) == 0 {
		return fmt.Errorf("no warning was shown for a band with no verified tools capability")
	}
	joined := strings.ToLower(strings.Join(s.warning, " "))
	if !strings.Contains(joined, "cannot drive tools") {
		return fmt.Errorf("the warning does not say the band cannot drive tools: %q", joined)
	}
	return nil
}

func (s *answersDisplayState) hintsToTune() error {
	joined := strings.ToLower(strings.Join(s.warning, " "))
	if !strings.Contains(joined, "tune") || !strings.Contains(joined, "tools-capable") {
		return fmt.Errorf("the warning does not hint to tune to a tools-capable band: %q", joined)
	}
	return nil
}

func (s *answersDisplayState) noWarningShown() error {
	if len(s.warning) != 0 {
		return fmt.Errorf("a tools-verified band was warned about anyway: %q", s.warning)
	}
	return nil
}

// --- sources survive into the copied transcript -----------------------------------

func (s *answersDisplayState) answerWithSourcesOnScreen() error {
	// The final answer the harness hands the TUI already carries the numbered block
	// (the harness citations suite pins that); what matters HERE is that the transcript
	// rendering and the copy path keep it intact - including through ansi.Strip and the
	// toolOutMark handling that copy applies.
	answer := "here is the answer\n\nSources:\n[1] Backoff A\n    https://a.example/x\n[2] Backoff B\n    https://b.example/y"
	s.model = model{}
	for _, ln := range strings.Split(answer, "\n") {
		s.model.agentLines = append(s.model.agentLines, stDim.Render(ln))
	}
	return nil
}

func (s *answersDisplayState) copiesTranscript() error {
	s.copied = s.model.agentTranscriptText()
	return nil
}

func (s *answersDisplayState) copiedHasNumberedURLs() error {
	for i, u := range []string{"https://a.example/x", "https://b.example/y"} {
		marker := fmt.Sprintf("[%d]", i+1)
		if !strings.Contains(s.copied, marker) {
			return fmt.Errorf("the copied transcript lost the citation number %s:\n%s", marker, s.copied)
		}
		if !strings.Contains(s.copied, u) {
			return fmt.Errorf("the copied transcript lost the source URL %s:\n%s", u, s.copied)
		}
	}
	if !strings.Contains(s.copied, "Sources:") {
		return fmt.Errorf("the copied transcript lost the sources heading:\n%s", s.copied)
	}
	return nil
}

// --- the derived toolset line ---------------------------------------------------

func (s *answersDisplayState) showToolsetLine() error {
	// The line describes the toolset the LOOP is carrying, which is what the agent will
	// actually run - so build it the way the TUI does, from a live loop's tools.
	loop := harness.NewLoop(s.t.TempDir(), "", nil, nil)
	s.line = agentToolsNote(loop.Tools())
	return nil
}

func (s *answersDisplayState) listsAsAutoRunning(tool string) error {
	auto, _, ok := strings.Cut(s.line, "·")
	if !ok {
		return fmt.Errorf("the toolset line has no auto-run / asks-first split: %q", s.line)
	}
	if !strings.Contains(auto, tool) {
		return fmt.Errorf("%q is not listed among the tools that run on their own: %q", tool, s.line)
	}
	return nil
}

func (s *answersDisplayState) listsAsAsksFirst(a, b string) error {
	_, asks, ok := strings.Cut(s.line, "·")
	if !ok {
		return fmt.Errorf("the toolset line has no auto-run / asks-first split: %q", s.line)
	}
	for _, tool := range []string{a, b} {
		if !strings.Contains(asks, tool) {
			return fmt.Errorf("%q is not listed among the tools that ask first: %q", tool, s.line)
		}
	}
	return nil
}

func (s *answersDisplayState) absentFromLine(tool string) error {
	if strings.Contains(s.line, tool) {
		return fmt.Errorf("%q is advertised with no provider configured: %q", tool, s.line)
	}
	return nil
}

func (s *answersDisplayState) remainingToolsListed() error {
	for _, tool := range []string{"read_file", "list_dir", "web_fetch", "write_file", "run_shell"} {
		if !strings.Contains(s.line, tool) {
			return fmt.Errorf("%q is missing from the toolset line: %q", tool, s.line)
		}
	}
	return nil
}

func TestAnswersDisplayBDD(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &answersDisplayState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})

			sc.Step(`^a search provider is configured$`, st.providerConfigured)
			sc.Step(`^no search provider is configured$`, st.noProviderConfigured)

			sc.Step(`^the agent calls web_search with query "([^"]*)"$`, st.callSearchWithQuery)
			sc.Step(`^the agent calls web_search with a query longer than the line budget$`, st.callSearchLongQuery)
			sc.Step(`^the agent calls web_fetch with url "([^"]*)"$`, st.callFetchWithURL)
			sc.Step(`^the call line summary is "([^"]*)"$`, st.summaryIs)
			sc.Step(`^the call line summary is clipped to a single line$`, st.summaryClipped)

			sc.Step(`^"([^"]*)" is previewable$`, st.isPreviewable)
			sc.Step(`^"([^"]*)" is not previewable$`, st.isNotPreviewable)

			sc.Step(`^the tuned band declares capabilities that do not include "tools"$`, st.bandLacksTools)
			sc.Step(`^the tuned band has no capability set at all$`, st.bandHasNoCapabilitySet)
			sc.Step(`^the tuned model is not in the offer list$`, st.modelNotInOffers)
			sc.Step(`^the tuned band carries the verified "tools" capability$`, st.bandHasTools)
			sc.Step(`^the user enters the agent$`, st.entersAgent)
			sc.Step(`^the transcript says this band cannot drive tools$`, st.saysCannotDriveTools)
			sc.Step(`^it hints to tune to a tools-capable band$`, st.hintsToTune)
			sc.Step(`^no tools-capability warning is shown$`, st.noWarningShown)

			sc.Step(`^an agent answer carrying a sources block is on screen$`, st.answerWithSourcesOnScreen)
			sc.Step(`^the user copies the transcript$`, st.copiesTranscript)
			sc.Step(`^the copied text includes the numbered source URLs$`, st.copiedHasNumberedURLs)

			sc.Step(`^the agent shows its toolset line$`, st.showToolsetLine)
			sc.Step(`^it lists "([^"]*)" among the tools that run on their own$`, st.listsAsAutoRunning)
			sc.Step(`^it lists "([^"]*)" and "([^"]*)" among the tools that ask first$`, st.listsAsAsksFirst)
			sc.Step(`^"([^"]*)" is absent from the line$`, st.absentFromLine)
			sc.Step(`^the remaining tools are still listed$`, st.remainingToolsListed)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/answers/tui_display.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("answers display scenarios failed (see godog output above)")
	}
}
