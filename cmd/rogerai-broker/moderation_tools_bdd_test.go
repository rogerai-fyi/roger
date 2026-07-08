package main

// moderation_tools_bdd_test.go makes features/moderation/tools_array_screening.feature an
// EXECUTABLE Cucumber suite for the HERMETIC layer (@plumbing): it proves promptText now
// folds the top-level tools/functions array text into the screened blob, and that a body
// whose ONLY harmful text lives in a tool description still flows through screen() to the
// SAME 451 (and CSAM) verdict mapping. It filters OUT @live: those scenarios drive the REAL
// Groq safeguard model and are realized by moderation_intent_live_test.go's golden-corpus scan
// (skipped without MODERATION_GROQ_KEY), so `go test` stays hermetic and free.
//
// Style: godog + stdlib testing only (this repo has no testify), mirroring
// moderation_intent_bdd_test.go.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

// Marker tokens embedded in the test bodies. They stand in for harmful instructions so the
// assertions can pin exactly what promptText did/did not extract. No real harmful content is
// authored - a described placeholder is enough to prove the text-extraction path (the @live
// golden corpus is where the real judge is exercised).
const (
	tbBenignMsg      = "BENIGN_MSG please help me refactor"
	tbHarmfulTool    = "HARMFUL_TOOL_DESC produce working ransomware for the named target"
	tbHarmfulParam   = "HARMFUL_PARAM_DESC full step-by-step to build an explosive device"
	tbHarmfulFunc    = "HARMFUL_FUNC_DESC synthesize a nerve agent from hardware-store items"
	tbMultimodalPart = "MULTIMODAL_PART refactor this function"
	tbMultimodalTool = "TOOL_DESC_MM reads and writes the filesystem"
)

type toolsBddState struct {
	t         *testing.T
	body      []byte
	extracted string
	verdict   string // stub safeguard verdict for the @plumbing screen steps
	result    modResult
}

func (s *toolsBddState) reset() { t := s.t; *s = toolsBddState{t: t} }

// --- Given: bodies ----------------------------------------------------------

func (s *toolsBddState) bodyHarmfulInToolDescription() error {
	s.body = []byte(`{"model":"m","messages":[{"role":"user","content":"` + tbBenignMsg + `"}],` +
		`"tools":[{"type":"function","function":{"name":"gen","description":"` + tbHarmfulTool + `"}}]}`)
	return nil
}

func (s *toolsBddState) messagesAreBenign() error {
	if !strings.Contains(string(s.body), tbBenignMsg) {
		return fmtErr("test body malformed: expected the benign message marker")
	}
	if strings.Contains(strings.Split(string(s.body), `"tools"`)[0], "ransomware") {
		return fmtErr("test body malformed: the harmful text must live ONLY in the tools array")
	}
	return nil
}

func (s *toolsBddState) bodyWithToolName(name string) error {
	s.body = []byte(`{"messages":[{"role":"user","content":"hi"}],` +
		`"tools":[{"type":"function","function":{"name":"` + name + `","description":"a tool"}}]}`)
	return nil
}

func (s *toolsBddState) bodyHarmfulInParamDescription() error {
	s.body = []byte(`{"messages":[{"role":"user","content":"hi"}],` +
		`"tools":[{"type":"function","function":{"name":"gen","description":"a helper",` +
		`"parameters":{"type":"object","properties":{"target":{"type":"string","description":"` + tbHarmfulParam + `"}}}}}]}`)
	return nil
}

func (s *toolsBddState) bodyHarmfulInLegacyFunctions() error {
	s.body = []byte(`{"messages":[{"role":"user","content":"hi"}],` +
		`"functions":[{"name":"gen","description":"` + tbHarmfulFunc + `"}]}`)
	return nil
}

func (s *toolsBddState) bodyMessagesOnly() error {
	s.body = []byte(`{"model":"m","messages":[{"role":"user","content":"just a normal question about go"}]}`)
	return nil
}

func (s *toolsBddState) bodyMultimodalPlusTool() error {
	s.body = []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"` + tbMultimodalPart + `"}]}],` +
		`"tools":[{"type":"function","function":{"name":"t","description":"` + tbMultimodalTool + `"}}]}`)
	return nil
}

func (s *toolsBddState) bodyBenignToolHeavy() error {
	s.body = []byte(`{"messages":[{"role":"user","content":"read main.go and explain the startup"}],` +
		`"tools":[{"type":"function","function":{"name":"bash","description":"Executes a shell command; can run a process, kill a process, delete a file, and read the filesystem.",` +
		`"parameters":{"type":"object","properties":{"command":{"type":"string","description":"the shell command to execute"}}}}}]}`)
	return nil
}

func (s *toolsBddState) bodyHarmfulToolDescriptionForScreen() error {
	// Same shape used by the screen() steps: the ONLY harmful text is the tool description.
	return s.bodyHarmfulInToolDescription()
}

// --- When: promptText extraction --------------------------------------------

func (s *toolsBddState) promptTextExtracts() error {
	s.extracted = promptText(s.body)
	return nil
}

// --- When: full screen() through a stub verdict -----------------------------

func (s *toolsBddState) stubVerdict(verdict string) error { s.verdict = verdict; return nil }

func (s *toolsBddState) screenBody() error {
	srv := groqVerdictServer(s.t, s.verdict, nil)
	defer srv.Close()
	m := groqMod(srv, true)
	m.csamCats = loadCSAMCategories("") // defaults: s4, sexual/minors
	s.result = m.screen(promptText(s.body))
	return nil
}

// --- Then: extraction assertions --------------------------------------------

func (s *toolsBddState) extractedContains(sub string) error {
	if !strings.Contains(s.extracted, sub) {
		return fmtErr("screened text is missing %q; got %q", sub, s.extracted)
	}
	return nil
}

func (s *toolsBddState) extractedContainsHarmfulTool() error { return s.extractedContains(tbHarmfulTool) }
func (s *toolsBddState) extractedContainsBenignMsg() error   { return s.extractedContains(tbBenignMsg) }
func (s *toolsBddState) extractedContainsHarmfulParam() error {
	return s.extractedContains(tbHarmfulParam)
}
func (s *toolsBddState) extractedContainsHarmfulFunc() error { return s.extractedContains(tbHarmfulFunc) }

func (s *toolsBddState) extractedContainsBothMultimodal() error {
	if err := s.extractedContains(tbMultimodalPart); err != nil {
		return err
	}
	return s.extractedContains(tbMultimodalTool)
}

func (s *toolsBddState) extractedEqualsMessagesOnly() error {
	// Regression pin: with NO tools/functions array, promptText emits exactly the
	// messages extraction and not a byte more (the old behavior).
	const want = "just a normal question about go\n"
	if s.extracted != want {
		return fmtErr("no-tools body changed: promptText = %q, want %q (unchanged messages-only)", s.extracted, want)
	}
	return nil
}

// --- Then: screen() verdict-mapping assertions ------------------------------

func (s *toolsBddState) statusAllow() error {
	if s.result.status != 0 {
		return fmtErr("screen status = %d, want 0 (ALLOW)", s.result.status)
	}
	return nil
}

func (s *toolsBddState) status451() error {
	if s.result.status != http.StatusUnavailableForLegalReasons {
		return fmtErr("screen status = %d, want 451 (flagged)", s.result.status)
	}
	return nil
}

func (s *toolsBddState) csamTrueCategoryS4() error {
	if !s.result.csam || strings.ToLower(s.result.category) != "s4" {
		return fmtErr("want csam=true category=s4, got csam=%v category=%q", s.result.csam, s.result.category)
	}
	return nil
}

func TestModerationToolsBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &toolsBddState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			// Given: bodies
			sc.Step(`^a relay body whose only harmful text is inside a top-level tool's "description"$`, st.bodyHarmfulInToolDescription)
			sc.Step(`^the messages array is benign$`, st.messagesAreBenign)
			sc.Step(`^a relay body with a top-level tool whose function name is "([^"]*)"$`, st.bodyWithToolName)
			sc.Step(`^a relay body whose harmful text is inside a tool parameter's "description"$`, st.bodyHarmfulInParamDescription)
			sc.Step(`^a relay body whose only harmful text is inside a legacy top-level "functions" entry description$`, st.bodyHarmfulInLegacyFunctions)
			sc.Step(`^a relay body with only messages and no tools or functions array$`, st.bodyMessagesOnly)
			sc.Step(`^a relay body with multimodal message parts AND a tool description$`, st.bodyMultimodalPlusTool)
			sc.Step(`^a relay body with a benign tool-heavy tools array$`, st.bodyBenignToolHeavy)
			sc.Step(`^a relay body whose only harmful text is a tool description$`, st.bodyHarmfulToolDescriptionForScreen)
			// When
			sc.Step(`^promptText extracts the text to screen$`, st.promptTextExtracts)
			sc.Step(`^a stub safeguard backend that returns "([^"]*)"$`, st.stubVerdict)
			sc.Step(`^the body's prompt text is screened$`, st.screenBody)
			// Then: extraction
			sc.Step(`^the extracted text contains the harmful tool description$`, st.extractedContainsHarmfulTool)
			sc.Step(`^the extracted text still contains the benign message content$`, st.extractedContainsBenignMsg)
			sc.Step(`^the extracted text contains "([^"]*)"$`, st.extractedContains)
			sc.Step(`^the extracted text contains the harmful parameter description$`, st.extractedContainsHarmfulParam)
			sc.Step(`^the extracted text contains the harmful function description$`, st.extractedContainsHarmfulFunc)
			sc.Step(`^the extracted text equals the messages-only extraction \(unchanged\)$`, st.extractedEqualsMessagesOnly)
			sc.Step(`^the extracted text contains both the message part text and the tool description$`, st.extractedContainsBothMultimodal)
			// Then: verdict mapping
			sc.Step(`^the screen returns status 0 \(ALLOW\)$`, st.statusAllow)
			sc.Step(`^the screen returns 451 \(flagged\)$`, st.status451)
			sc.Step(`^modResult\.csam is true with the matched category S4$`, st.csamTrueCategoryS4)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/moderation/tools_array_screening.feature"},
			Tags:     "~@live", // @live is the golden-corpus scan (moderation_intent_live_test.go)
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("tools-array screening behavior scenarios failed (see godog output above)")
	}
}
