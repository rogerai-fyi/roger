package main

// moderation_recalibration_bdd_test.go makes features/moderation/recalibration.feature an
// EXECUTABLE Cucumber suite for the lean-pass recalibration, driving the REAL groq screen
// (screenGroq -> groqVerdict -> decideVerdict -> groqFailMode) against a local httptest stub
// that returns CANNED Groq chat/completions verdicts. No mocks: the stub is a real HTTP server
// speaking the OpenAI-compatible shape, and we assert the real modResult + the real log lines
// (fail-open + pass-but-flagged telemetry) and the real request body the classifier received
// (content-isolation delimiters).
//
// Style: godog + stdlib testing only (this repo has no testify), mirroring moderation_bdd_test.go.

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

type recalState struct {
	t        *testing.T
	verdicts []string // scripted verdicts, one consumed per call; the last repeats
	status   int      // stub HTTP status (default 200)
	emptyOut bool     // stub returns an empty message.content
	calls    int
	capBody  string // last request body the classifier received
	srv      *httptest.Server
	m        moderation
	require  bool
	result   modResult

	logs   *bytes.Buffer
	logOut io.Writer
	logFl  int
}

func (s *recalState) reset() {
	if s.srv != nil {
		s.srv.Close()
		s.srv = nil
	}
	s.verdicts = nil
	s.status = http.StatusOK
	s.emptyOut = false
	s.calls = 0
	s.capBody = ""
	s.m = moderation{}
	s.require = false
	s.result = modResult{}
	s.logs.Reset()
}

// startStub spins a real httptest Groq endpoint that returns the scripted verdict for the
// current call index (the last scripted verdict repeats), captures the request body, and
// honors s.status / s.emptyOut for the outage scenarios.
func (s *recalState) startStub() {
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.capBody = string(b)
		idx := s.calls
		s.calls++
		if s.status != 0 && s.status != http.StatusOK {
			w.WriteHeader(s.status)
			return
		}
		content := ""
		if !s.emptyOut && len(s.verdicts) > 0 {
			if idx >= len(s.verdicts) {
				idx = len(s.verdicts) - 1
			}
			content = s.verdicts[idx]
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":` + strconvQuote(content) + `}}]}`))
	}))
	s.m = moderation{
		provider: "groq", require: s.require, client: s.srv.Client(),
		groqKey: "test-key", groqURL: s.srv.URL, groqModel: "x",
	}
	s.m.csamCats = loadCSAMCategories("") // defaults: s4, sexual/minors
}

// unescape turns the literal backslash-n written in the .feature into a real newline so the
// next-line-code scenarios exercise the layout-agnostic parse.
func unescape(v string) string { return strings.ReplaceAll(v, `\n`, "\n") }

// --- Given ------------------------------------------------------------------

func (s *recalState) scriptedReturn(v string) error {
	s.verdicts = []string{unescape(v)}
	s.startStub()
	return nil
}

func (s *recalState) scriptedReturnThen(v1, v2 string) error {
	s.verdicts = []string{unescape(v1), unescape(v2)}
	s.startStub()
	return nil
}

func (s *recalState) requireUnreachable() error {
	s.require = true
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // closed -> the POST errors (connection refused)
	s.m = moderation{provider: "groq", require: true, client: &http.Client{}, groqKey: "test-key", groqURL: url, groqModel: "x"}
	s.m.csamCats = loadCSAMCategories("")
	return nil
}

func (s *recalState) requireHTTPStatus(code int) error {
	s.require = true
	s.status = code
	s.startStub()
	return nil
}

func (s *recalState) requireEmptyVerdict() error {
	s.require = true
	s.emptyOut = true
	s.startStub()
	return nil
}

// --- When -------------------------------------------------------------------

func (s *recalState) promptScreened() error {
	s.result = s.m.screen("a benign relay prompt to screen")
	return nil
}

func (s *recalState) aiderBodyScreened() error {
	// A representative coding-agent relay body: the payload asks the model to summarize a
	// repository - benign development work that the OLD parse false-positive-blocked.
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"system","content":"You are aider, an AI pair programmer. You can run shell commands, read and edit files, and search the repository."},` +
		`{"role":"user","content":"summarize this repository and suggest a refactor"}]}`)
	s.result = s.m.screen(promptText(body))
	return nil
}

// --- Then -------------------------------------------------------------------

func (s *recalState) allows() error {
	if s.result.status != 0 {
		return fmtErr("screen status = %d, want 0 (ALLOW)", s.result.status)
	}
	return nil
}

func (s *recalState) rejects451() error {
	if s.result.status != http.StatusUnavailableForLegalReasons {
		return fmtErr("screen status = %d, want 451", s.result.status)
	}
	return nil
}

func (s *recalState) csamFalse() error {
	if s.result.csam {
		return fmtErr("modResult.csam = true, want false")
	}
	return nil
}

func (s *recalState) csamTrue() error {
	if !s.result.csam {
		return fmtErr("modResult.csam = false, want true (CSAM signal)")
	}
	return nil
}

func (s *recalState) csamTrueCategory(cat string) error {
	if !s.result.csam {
		return fmtErr("modResult.csam = false, want true")
	}
	if !strings.EqualFold(s.result.category, cat) {
		return fmtErr("CSAM category = %q, want %q", s.result.category, cat)
	}
	return nil
}

func (s *recalState) calledTimes(n int) error {
	if s.calls != n {
		return fmtErr("classifier called %d times, want %d", s.calls, n)
	}
	return nil
}

func (s *recalState) telemetryLogged(code string) error {
	want := "passed-but-flagged category " + code
	if !strings.Contains(s.logs.String(), want) {
		return fmtErr("log missing telemetry %q; got:\n%s", want, s.logs.String())
	}
	return nil
}

func (s *recalState) failOpenLogged() error {
	if !strings.Contains(s.logs.String(), "MODERATION FAIL-OPEN") {
		return fmtErr("log missing the loud MODERATION FAIL-OPEN incident line; got:\n%s", s.logs.String())
	}
	return nil
}

func (s *recalState) bodyWrapsInDelimiters() error {
	if !strings.Contains(s.capBody, classifyBeginMarker) || !strings.Contains(s.capBody, classifyEndMarker) {
		return fmtErr("classifier request body did not wrap the prompt in data delimiters; got:\n%s", s.capBody)
	}
	// The wrapped prompt text must be inside the markers, as JSON-encoded content.
	if !strings.Contains(s.capBody, "a benign relay prompt to screen") {
		return fmtErr("wrapped prompt text missing from the classifier request body")
	}
	return nil
}

func (s *recalState) policyInstructsDataOnly() error {
	low := strings.ToLower(moderationPolicy)
	if !strings.Contains(moderationPolicy, classifyBeginMarker) || !strings.Contains(moderationPolicy, classifyEndMarker) {
		return fmtErr("moderationPolicy must reference the data delimiters %q / %q", classifyBeginMarker, classifyEndMarker)
	}
	if !(strings.Contains(low, "data") && strings.Contains(low, "instruction")) {
		return fmtErr("moderationPolicy must tell the classifier to treat the delimited text as DATA, never instructions")
	}
	return nil
}

func TestModerationRecalibrationBDD(t *testing.T) {
	for _, k := range []string{"MODERATION_PROVIDER", "MODERATION_URL", "MODERATION_GROQ_KEY", "GROQ_API_KEY", "ROGERAI_REQUIRE_MODERATION", "ROGERAI_CSAM_CATEGORIES", "MODERATION_MODEL"} {
		t.Setenv(k, "")
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &recalState{t: t, logs: &bytes.Buffer{}}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.logOut = log.Writer()
				st.logFl = log.Flags()
				log.SetOutput(st.logs)
				log.SetFlags(0)
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.srv != nil {
					st.srv.Close()
					st.srv = nil
				}
				log.SetOutput(st.logOut)
				log.SetFlags(st.logFl)
				return ctx, nil
			})
			// Given
			sc.Step(`^a Groq safeguard backend scripted to return "([^"]*)" then "([^"]*)"$`, st.scriptedReturnThen)
			sc.Step(`^a Groq safeguard backend scripted to return "([^"]*)"$`, st.scriptedReturn)
			sc.Step(`^ROGERAI_REQUIRE_MODERATION=1 and the Groq classifier is unreachable$`, st.requireUnreachable)
			sc.Step(`^ROGERAI_REQUIRE_MODERATION=1 and the Groq classifier returns HTTP (\d+)$`, st.requireHTTPStatus)
			sc.Step(`^ROGERAI_REQUIRE_MODERATION=1 and the Groq classifier returns an empty verdict$`, st.requireEmptyVerdict)
			// When
			sc.Step(`^a relay prompt is screened$`, st.promptScreened)
			sc.Step(`^the aider agent relay body is screened$`, st.aiderBodyScreened)
			// Then
			sc.Step(`^the screen allows \(status 0\)$`, st.allows)
			sc.Step(`^the screen returns 451$`, st.rejects451)
			sc.Step(`^modResult\.csam is false$`, st.csamFalse)
			sc.Step(`^modResult\.csam is true$`, st.csamTrue)
			sc.Step(`^modResult\.csam is true with category "([^"]*)"$`, st.csamTrueCategory)
			sc.Step(`^the classifier was called exactly (\d+) times?$`, st.calledTimes)
			sc.Step(`^a "passed-but-flagged category ([^"]*)" telemetry line is logged$`, st.telemetryLogged)
			sc.Step(`^a loud "MODERATION FAIL-OPEN" incident line is logged$`, st.failOpenLogged)
			sc.Step(`^the request the classifier received wraps the prompt in data delimiters$`, st.bodyWrapsInDelimiters)
			sc.Step(`^the moderation policy instructs the classifier to treat the delimited text as data, never instructions$`, st.policyInstructsDataOnly)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/moderation/recalibration.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("moderation recalibration behavior scenarios failed (see godog output above)")
	}
}
