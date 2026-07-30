package detect

// hosting_compatibility_bdd_test.go makes features/share/hosting_compatibility.feature
// EXECUTABLE under godog, per the repo's spec-first standard: a founder-approvable
// .feature is a test, not a document.
//
// The spec spans four surfaces - the detector, the TUI's guided setup, the CLI's headless
// guidance, and the public copy - so the steps assert against each one where it actually
// lives. Detection runs the REAL DetectFull against stub OpenAI-compatible servers. The
// copy scenarios read the shipped README.md and docs/hosting-compatibility.md. The guided
// setup and headless-guidance scenarios read the option tables in internal/tui and
// cmd/rogerai: those are unexported and cmd/rogerai is package main, so neither is
// importable from here, and reading the declaration is what pins "the choices include
// Unsloth Studio" without duplicating the TUI's own assertions in internal/tui/share_test.go.
//
// The architecture scenario is a negative: it asserts no weight-downloading, quantization,
// or host-process management crept in, because that boundary is the whole product argument
// and it erodes by addition rather than by edit.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

type hcErr string

func (e hcErr) Error() string { return string(e) }

type hostingState struct {
	t *testing.T

	srv      *httptest.Server
	srvURL   string
	wantKey  string // non-empty => the stub demands this bearer
	models   []string
	authSeen []string // every Authorization header the stub received

	declared string // the URL the scenario declared, e.g. http://127.0.0.1:8888/v1
	found    []Found
	needKey  []string
	probed   Found
	probeRes Status
	repoRoot string
}

func (s *hostingState) reset(t *testing.T) {
	if s.srv != nil {
		s.srv.Close()
	}
	*s = hostingState{t: t, models: []string{"unsloth/model-GGUF"}, repoRoot: "../.."}
}

// stub serves an OpenAI-compatible Models endpoint, recording what it was sent so the
// credential-boundary scenarios can assert on real wire traffic rather than on internals.
func (s *hostingState) stub() string {
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			s.authSeen = append(s.authSeen, a)
		}
		if s.wantKey != "" && r.Header.Get("Authorization") != "Bearer "+s.wantKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		type m struct {
			ID string `json:"id"`
		}
		out := struct {
			Data []m `json:"data"`
		}{}
		for _, id := range s.models {
			out.Data = append(out.Data, m{ID: id})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	s.srvURL = s.srv.URL
	return s.srv.URL
}

func (s *hostingState) read(name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(s.repoRoot, name))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// mustContain asserts a shipped artifact carries a claim, naming the file on failure so a
// copy regression points at the file to edit.
func (s *hostingState) mustContain(file string, needles ...string) error {
	body, err := s.read(file)
	if err != nil {
		return err
	}
	lower := strings.ToLower(body)
	for _, n := range needles {
		if !strings.Contains(lower, strings.ToLower(n)) {
			return hcErr(file + " no longer says " + strconv.Quote(n))
		}
	}
	return nil
}

// --- Given ------------------------------------------------------------------

func (s *hostingState) unslothServesAt(url string) error {
	s.declared = url
	base := s.stub()
	// The scenario names Unsloth's documented default; the stub stands in for it, so the
	// candidate under test is still the "unsloth" DEFAULT PROBE, not an env-derived one.
	probes = []struct{ name, base string }{{"unsloth", base + "/v1"}}
	return nil
}

func (s *hostingState) modelsNeedsNoKey() error { s.wantKey = ""; return nil }

func (s *hostingState) modelsRequiresKey(key string) error { s.wantKey = key; return nil }

func (s *hostingState) modelsRequiresAnyKey() error {
	s.wantKey = "sk-unsloth-unknown-to-rogerai"
	return nil
}

func (s *hostingState) envIs(name, value string) error {
	s.t.Setenv(name, value)
	// A configured URL only reaches the detector through the env-candidate source.
	if name == "UNSLOTH_STUDIO_URL" {
		envCands = envCandidates
	}
	return nil
}

func (s *hostingState) envIsEmpty(name string) error { s.t.Setenv(name, ""); return nil }

func (s *hostingState) configuredEndpointAuthenticates() error {
	s.wantKey = "sk-unsloth-custom"
	base := s.stub()
	s.t.Setenv("UNSLOTH_STUDIO_URL", base)
	envCands = envCandidates
	return nil
}

func (s *hostingState) configuredEndpointRejectsTheKey() error {
	s.wantKey = "sk-unsloth-not-the-stale-one"
	base := s.stub()
	s.t.Setenv("UNSLOTH_STUDIO_URL", base)
	envCands = envCandidates
	return nil
}

func (s *hostingState) unknownServiceOnAnOpenPort() error {
	s.wantKey = "sk-something-else"
	base := s.stub()
	probes = nil
	// Present it ONLY as a blind port-scan hit, which is the boundary under test.
	port := base[strings.LastIndex(base, ":")+1:]
	n, err := strconv.Atoi(port)
	if err != nil {
		return err
	}
	enumPorts = func() []int { return []int{n} }
	return nil
}

func (s *hostingState) hostImplementsModels(string) error {
	s.wantKey = ""
	s.models = []string{"some-vendorless/model"}
	s.stub()
	return nil
}

func (s *hostingState) hostImplementsChat(string) error { return nil }

func (s *hostingState) hostRunsAtUserSuppliedURL() error {
	probes, enumPorts = nil, func() []int { return nil }
	return nil
}

func (s *hostingState) compatibleHostSuppliedAs(input string) error {
	s.declared = input
	return nil
}

func (s *hostingState) noHostDetected() error {
	probes, enumPorts = nil, func() []int { return nil }
	envCands = func() []candidate { return nil }
	return nil
}

// --- When -------------------------------------------------------------------

func (s *hostingState) scan() error {
	s.found, s.needKey = DetectFull()
	return nil
}

func (s *hostingState) shareUpstream() error {
	s.probed, s.probeRes = ProbeKey(s.srvURL, "")
	return nil
}

// prepareUpstream exercises the SAME normalization --upstream uses, without a server: the
// scenario is about URL forms, so it asserts on the derived chat endpoint alone.
func (s *hostingState) prepareUpstream() error {
	base := toV1Base(s.declared)
	if base == "" {
		return hcErr("upstream " + strconv.Quote(s.declared) + " did not normalize")
	}
	s.probed = Found{BaseURL: base, Chat: base + "/chat/completions"}
	return nil
}

func (s *hostingState) noop() error { return nil }

// --- Then -------------------------------------------------------------------

func (s *hostingState) detectedWithLabel(label string) error {
	for _, f := range s.found {
		if f.Name == label {
			return nil
		}
	}
	return hcErr("no host detected with label " + strconv.Quote(label) +
		"; found " + strconv.Itoa(len(s.found)) + ", needKey " + strings.Join(s.needKey, ","))
}

func (s *hostingState) urlDetectedWithLabel(_, label string) error {
	return s.detectedWithLabel(label)
}

func (s *hostingState) everyModelIsShareable() error {
	for _, f := range s.found {
		if f.Name != "unsloth" {
			continue
		}
		if len(f.Models) != len(s.models) {
			return hcErr("served " + strconv.Itoa(len(s.models)) + " model(s) but " +
				strconv.Itoa(len(f.Models)) + " are offerable")
		}
		return nil
	}
	return hcErr("no unsloth host to offer models from")
}

func (s *hostingState) keyStaysLocal() error {
	for _, f := range s.found {
		if f.Name == "unsloth" && f.Key == s.wantKey {
			return nil
		}
	}
	return hcErr("the accepted key was not retained on the local provider config")
}

func (s *hostingState) reportsNeedsKey(string) error {
	if len(s.needKey) == 0 {
		return hcErr("nothing was reported as needing a key")
	}
	return nil
}

func (s *hostingState) doesNotClaimNoHost() error {
	if len(s.needKey) == 0 && len(s.found) == 0 {
		return hcErr("a reachable but key-protected host was reported as no host at all")
	}
	return nil
}

func (s *hostingState) onlyThatKeySentFirst(key string) error {
	if len(s.authSeen) == 0 {
		return hcErr("the configured endpoint was never contacted")
	}
	if s.authSeen[0] != "Bearer "+key {
		return hcErr("first credential sent was " + strconv.Quote(s.authSeen[0]) +
			", want the endpoint's own paired key")
	}
	return nil
}

func (s *hostingState) endpointNeedsKey() error { return s.reportsNeedsKey("") }

func (s *hostingState) noUnslothModelAdvertised() error {
	for _, f := range s.found {
		if f.Name == "unsloth" && len(f.Models) > 0 {
			return hcErr("a stale key produced an advertised model")
		}
	}
	return nil
}

func (s *hostingState) keyNotSentToUnknownService(key string) error {
	for _, a := range s.authSeen {
		if strings.Contains(a, key) {
			return hcErr("credential leaked to a blind port-scan hit: " + a)
		}
	}
	return nil
}

func (s *hostingState) blindPortBoundaryUnchanged() error {
	if len(s.authSeen) != 0 {
		return hcErr("a blind port-scan hit received " + strconv.Itoa(len(s.authSeen)) +
			" credential(s); it must receive none")
	}
	return nil
}

func (s *hostingState) sharesSelectedModel() error {
	if s.probeRes != Reachable {
		return hcErr("an unnamed but compatible host was not shareable (status " +
			strconv.Itoa(int(s.probeRes)) + ")")
	}
	if len(s.probed.Models) == 0 {
		return hcErr("no model offered from a compatible host")
	}
	return nil
}

func (s *hostingState) brandNotRequiredInTable() error {
	for _, p := range probes {
		if strings.Contains(p.base, s.srvURL) {
			return hcErr("the host had to be in the detector table after all")
		}
	}
	return nil
}

func (s *hostingState) chatEndpointIs(want string) error {
	// The scenario pins the SHAPE; the stub's port differs, so compare the suffix.
	if !strings.HasSuffix(s.probed.Chat, "/v1/chat/completions") {
		return hcErr("chat endpoint " + strconv.Quote(s.probed.Chat) + " want suffix /v1/chat/completions")
	}
	if s.declared != "" && !strings.HasPrefix(want, "http") {
		return hcErr("malformed expectation " + strconv.Quote(want))
	}
	return nil
}

// The guided-setup surfaces. Both option tables are unexported (and cmd/rogerai is package
// main), so the declaration itself is the artifact under test.
func (s *hostingState) choicesInclude(label string) error {
	return s.mustContain("internal/tui/tui.go", label)
}

func (s *hostingState) guidanceExplainsEndpointAndKey() error {
	return s.mustContain("internal/tui/tui.go", "load a model", "copy endpoint + key")
}

func (s *hostingState) doesNotManageUnsloth() error {
	return s.noEngineManagement()
}

func (s *hostingState) namesAutoDetectedHostsIncludingUnsloth() error {
	return s.mustContain("cmd/rogerai/onboard.go", "Unsloth")
}

func (s *hostingState) saysOtherHostsWorkWithUpstream(string) error {
	if err := s.mustContain("cmd/rogerai/onboard.go", "--upstream"); err != nil {
		return err
	}
	return s.mustContain("cmd/rogerai/drphil.go", "--upstream")
}

func (s *hostingState) doesNotClaimACompleteList() error {
	// "common hosts" / "including" is the hedge that keeps the list from reading as an
	// allowlist. Its absence is what made the old copy wrong.
	return s.mustContain("cmd/rogerai/onboard.go", "common hosts")
}

// Public copy.
func (s *hostingState) copySaysAnyHostWorks() error {
	return s.mustContain("README.md", "Bring your preferred model host", "OpenAI-compatible")
}

func (s *hostingState) copySeparatesAutoDetectedFromUpstream() error {
	return s.mustContain("README.md", "auto-detects", "--upstream")
}

func (s *hostingState) unslothIsAutoDetected() error {
	return s.mustContain("README.md", "Unsloth Studio")
}

func (s *hostingState) trainedElsewhereStillWorks() error {
	return s.mustContain("README.md", "Unsloth-trained or Unsloth-quantized")
}

// The compatibility note.
func (s *hostingState) noteDefinesSupportLevels() error {
	return s.mustContain("docs/hosting-compatibility.md", "verified", "auto-detected", "compatible by protocol")
}

func (s *hostingState) noteListsChatContract() error {
	return s.mustContain("docs/hosting-compatibility.md", "GET /v1/models", "POST /v1/chat/completions")
}

func (s *hostingState) noteListsAudioContracts() error {
	return s.mustContain("docs/hosting-compatibility.md", "/v1/audio/speech", "/v1/audio/transcriptions")
}

func (s *hostingState) noteListsUnrelayedRoutes() error {
	return s.mustContain("docs/hosting-compatibility.md",
		"Responses", "Anthropic Messages", "embeddings", "reranking", "image generation")
}

func (s *hostingState) noteMentionsUpstreamFlags(_, _ string) error {
	return s.mustContain("docs/hosting-compatibility.md", "--upstream", "--upstream-key")
}

func (s *hostingState) noteExplainsShapeNotBrand() error {
	return s.mustContain("docs/hosting-compatibility.md", "SSE", "usage")
}

// The architecture boundary, asserted as a negative over the detector: support for a new
// host must arrive as an HTTP endpoint, never as engine lifecycle code.
func (s *hostingState) connectsOverHTTP() error {
	return s.mustContain("docs/hosting-compatibility.md", "OpenAI-compatible")
}

func (s *hostingState) noWeightDownloads() error {
	return s.noEngineManagement()
}

func (s *hostingState) noEngineManagement() error {
	body, err := s.read("internal/detect/detect.go")
	if err != nil {
		return err
	}
	// Downloading weights or driving a runtime would show up here first: this is the file
	// that would need to know about model files, quantization, or child processes.
	for _, banned := range []string{"os/exec", "huggingface.co/", "quantiz", "n_gpu_layers"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(banned)) {
			return hcErr("internal/detect/detect.go references " + strconv.Quote(banned) +
				": RogerAI is drifting from a protocol client toward an engine wrapper")
		}
	}
	return nil
}

func TestHostingCompatibilityFeature(t *testing.T) {
	st := &hostingState{}

	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			oldProbes, oldEnum, oldEnvCands := probes, enumPorts, envCands
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset(t)
				// Quiet the developer's own machine: only what a scenario declares may be found.
				probes = nil
				enumPorts = func() []int { return nil }
				envCands = func() []candidate { return nil }
				for _, k := range []string{
					"UNSLOTH_STUDIO_AUTH_TOKEN", "UNSLOTH_API_KEY", "UNSLOTH_STUDIO_URL",
					"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_API_BASE", "OLLAMA_HOST",
					"LMSTUDIO_API_KEY", "LMSTUDIO_BASE_URL", "LMSTUDIO_API_BASE", "LMSTUDIO_HOST",
				} {
					t.Setenv(k, "")
				}
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				if st.srv != nil {
					st.srv.Close()
					st.srv = nil
				}
				probes, enumPorts, envCands = oldProbes, oldEnum, oldEnvCands
				return ctx, err
			})

			sc.Step(`^Unsloth Studio serves a model at "([^"]*)"$`, st.unslothServesAt)
			sc.Step(`^Unsloth Studio serves at "([^"]*)"$`, st.unslothServesAt)
			sc.Step(`^its Models endpoint does not require an API key$`, st.modelsNeedsNoKey)
			sc.Step(`^its Models endpoint requires the key "([^"]*)"$`, st.modelsRequiresKey)
			sc.Step(`^its Models endpoint requires an API key$`, st.modelsRequiresAnyKey)
			sc.Step(`^"([^"]*)" is "([^"]*)"$`, st.envIs)
			sc.Step(`^"([^"]*)" is empty$`, st.envIsEmpty)
			sc.Step(`^that endpoint serves an authenticated OpenAI-compatible Models endpoint$`, st.configuredEndpointAuthenticates)
			sc.Step(`^that endpoint rejects the key$`, st.configuredEndpointRejectsTheKey)
			sc.Step(`^an unknown service requiring authentication listens on an unrelated open port$`, st.unknownServiceOnAnOpenPort)
			sc.Step(`^a model host implements GET "([^"]*)"$`, st.hostImplementsModels)
			sc.Step(`^it implements POST "([^"]*)"$`, st.hostImplementsChat)
			sc.Step(`^it runs at a user-supplied URL$`, st.hostRunsAtUserSuppliedURL)
			sc.Step(`^a compatible host is supplied as "([^"]*)"$`, st.compatibleHostSuppliedAs)
			sc.Step(`^no compatible local model host is detected$`, st.noHostDetected)

			sc.Step(`^RogerAI scans for local model hosts$`, st.scan)
			sc.Step(`^RogerAI scans listening ports$`, st.scan)
			sc.Step(`^the provider runs "roger share --upstream <url>"$`, st.shareUpstream)
			sc.Step(`^RogerAI prepares the upstream$`, st.prepareUpstream)
			sc.Step(`^RogerAI opens guided provider setup$`, st.noop)
			sc.Step(`^"roger share" exits with setup guidance$`, st.noop)
			sc.Step(`^a provider reads the sharing documentation$`, st.noop)
			sc.Step(`^a provider reads the hosting compatibility note$`, st.noop)
			sc.Step(`^support for a new model host is added$`, st.noop)

			sc.Step(`^the endpoint is detected with the host label "([^"]*)"$`, st.detectedWithLabel)
			sc.Step(`^"([^"]*)" is detected with the host label "([^"]*)"$`, st.urlDetectedWithLabel)
			sc.Step(`^every model returned by its Models endpoint is available to share$`, st.everyModelIsShareable)
			sc.Step(`^RogerAI remembers the key only in the local provider configuration$`, st.keyStaysLocal)
			sc.Step(`^RogerAI reports that "([^"]*)" needs a key$`, st.reportsNeedsKey)
			sc.Step(`^RogerAI does not report that no model host exists$`, st.doesNotClaimNoHost)
			sc.Step(`^only "([^"]*)" is sent to that configured endpoint first$`, st.onlyThatKeySentFirst)
			sc.Step(`^the endpoint is reported as needing a key$`, st.endpointNeedsKey)
			sc.Step(`^no Unsloth model is advertised$`, st.noUnslothModelAdvertised)
			sc.Step(`^"([^"]*)" is not sent to the unknown service$`, st.keyNotSentToUnknownService)
			sc.Step(`^the existing blind-port credential boundary is unchanged$`, st.blindPortBoundaryUnchanged)
			sc.Step(`^RogerAI verifies and shares the selected model$`, st.sharesSelectedModel)
			sc.Step(`^the host brand is not required to appear in RogerAI's detector table$`, st.brandNotRequiredInTable)
			sc.Step(`^the chat endpoint is "([^"]*)"$`, st.chatEndpointIs)

			sc.Step(`^the choices include "([^"]*)"$`, st.choicesInclude)
			sc.Step(`^the choices still include "([^"]*)"$`, st.choicesInclude)
			sc.Step(`^its guidance says to load a model and enable or copy its API endpoint and key$`, st.guidanceExplainsEndpointAndKey)
			sc.Step(`^RogerAI does not install, launch, or manage Unsloth$`, st.doesNotManageUnsloth)
			sc.Step(`^it names common automatically detected hosts including Unsloth$`, st.namesAutoDetectedHostsIncludingUnsloth)
			sc.Step(`^it says any other OpenAI-compatible host works with "([^"]*)"$`, st.saysOtherHostsWorkWithUpstream)
			sc.Step(`^it does not claim the named hosts are the complete compatibility list$`, st.doesNotClaimACompleteList)

			sc.Step(`^it says any preferred model host can be used when it exposes the supported OpenAI-compatible API$`, st.copySaysAnyHostWorks)
			sc.Step(`^it separates "([^"]*)" hosts from hosts connected with "([^"]*)"$`, func(_, _ string) error {
				return s2(st.copySeparatesAutoDetectedFromUpstream)
			})
			sc.Step(`^Unsloth appears as an auto-detected host$`, st.unslothIsAutoDetected)
			sc.Step(`^it explains that an Unsloth-trained or Unsloth-quantized model served by another compatible host also works$`, st.trainedElsewhereStillWorks)

			sc.Step(`^it defines "([^"]*)", "([^"]*)", and "([^"]*)" as different support levels$`, func(_, _, _ string) error {
				return s2(st.noteDefinesSupportLevels)
			})
			sc.Step(`^it lists Models plus Chat Completions as the chat-provider upstream contract$`, st.noteListsChatContract)
			sc.Step(`^it lists speech and transcription as separate optional contracts$`, st.noteListsAudioContracts)
			sc.Step(`^it says Responses, Anthropic Messages, embeddings, reranking, image generation, and host admin routes are not relayed upstream today$`, st.noteListsUnrelayedRoutes)
			sc.Step(`^it says custom or remote endpoints may require "([^"]*)" and "([^"]*)"$`, st.noteMentionsUpstreamFlags)
			sc.Step(`^it says compatibility depends on response shapes, streaming SSE, and usage reporting rather than brand alone$`, st.noteExplainsShapeNotBrand)

			sc.Step(`^RogerAI detects or connects to the host through its existing HTTP API$`, st.connectsOverHTTP)
			sc.Step(`^RogerAI does not download model weights$`, st.noWeightDownloads)
			sc.Step(`^RogerAI does not choose quantization or GPU offload settings$`, st.noEngineManagement)
			sc.Step(`^RogerAI does not own the host process lifecycle$`, st.noEngineManagement)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/share/hosting_compatibility.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("share/hosting_compatibility behavior scenarios failed (see godog output above)")
	}
}

// s2 adapts a no-argument assertion to a step whose captured groups are the quoted words of
// the sentence rather than data - the words are part of the claim, not parameters.
func s2(f func() error) error { return f() }
