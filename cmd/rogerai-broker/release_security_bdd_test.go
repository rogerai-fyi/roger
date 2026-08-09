package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"golang.org/x/text/unicode/norm"
)

// releaseSecurityState makes features/security/release_dependency_and_version.feature
// executable. The vulnerability scenario runs the real scanner and also verifies the
// exact publication wiring used before main or a release can proceed.
type releaseSecurityState struct {
	t                 *testing.T
	vulnerabilityScan string
	vulnerabilityErr  error
	normTerminated    bool
	rec               *httptest.ResponseRecorder
	body              map[string]any
	malformedCommit   string
}

func (s *releaseSecurityState) buildCompleteModule() error {
	root := filepath.Clean(filepath.Join("..", ".."))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "./...")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("release build exceeded 2 minutes: %w", ctx.Err())
		}
		return fmt.Errorf("release build: %w\n%s", err, out)
	}
	return nil
}

func (s *releaseSecurityState) scanVulnerabilities() error {
	root := filepath.Clean(filepath.Join("..", ".."))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", "golang.org/x/vuln/cmd/govulncheck@v1.6.0", "./...")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	s.vulnerabilityScan = string(out)
	if ctx.Err() != nil {
		s.vulnerabilityErr = fmt.Errorf("govulncheck exceeded 2 minutes: %w", ctx.Err())
	} else {
		s.vulnerabilityErr = err
	}
	return nil
}

func (s *releaseSecurityState) noReachableVulnerability() error {
	if s.vulnerabilityErr != nil {
		return fmt.Errorf("govulncheck failed: %w\n%s", s.vulnerabilityErr, s.vulnerabilityScan)
	}
	return nil
}

func (s *releaseSecurityState) sameGateOnMainAndTags() error {
	root := filepath.Clean(filepath.Join("..", ".."))
	const exactGate = "go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./..."
	for _, rel := range []string{
		filepath.Join(".github", "workflows", "coverage.yml"),
		filepath.Join(".github", "workflows", "release.yml"),
	} {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return err
		}
		if !strings.Contains(string(b), exactGate) {
			return fmt.Errorf("%s does not use the approved vulnerability gate %q", rel, exactGate)
		}
	}
	return nil
}

func (s *releaseSecurityState) invalidUTF8Input() error { return nil }

func (s *releaseSecurityState) normalizeInvalidUTF8() error {
	cmd := exec.Command(os.Args[0], "-test.run=^TestReleaseDependencyAndVersionBDD$")
	cmd.Env = append(os.Environ(), "ROGERAI_RELEASE_SECURITY_UTF8_HELPER=1")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start normalization helper: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("normalization helper: %w", err)
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		return fmt.Errorf("Unicode normalization helper started but did not terminate on invalid UTF-8 (GO-2026-5970)")
	}
	s.normTerminated = true
	return nil
}

func (s *releaseSecurityState) normalizationTerminates() error {
	if !s.normTerminated {
		return fmt.Errorf("normalization did not terminate")
	}
	return nil
}

func (s *releaseSecurityState) knownCommit() error {
	s.t.Setenv("ROGERAI_BUILD_COMMIT", strings.Repeat("A", 40))
	return nil
}

func (s *releaseSecurityState) missingCommit() error {
	s.t.Setenv("ROGERAI_BUILD_COMMIT", "")
	return nil
}

func (s *releaseSecurityState) malformedCommitMetadata() error {
	s.malformedCommit = "not-a-full-commit"
	s.t.Setenv("ROGERAI_BUILD_COMMIT", s.malformedCommit)
	return nil
}

func (s *releaseSecurityState) requestVersion() error {
	return s.requestVersionWithMethod(http.MethodGet)
}

func (s *releaseSecurityState) requestVersionWithMethod(method string) error {
	s.rec = httptest.NewRecorder()
	(&broker{}).routes().ServeHTTP(s.rec, httptest.NewRequest(method, "/version", nil))
	s.body = nil
	if strings.Contains(s.rec.Header().Get("Content-Type"), "json") {
		if err := json.Unmarshal(s.rec.Body.Bytes(), &s.body); err != nil {
			return err
		}
	}
	return nil
}

func (s *releaseSecurityState) responseIs200JSON() error {
	if s.rec.Code != http.StatusOK || s.body == nil {
		return fmt.Errorf("response = %d %q", s.rec.Code, s.rec.Body.String())
	}
	return nil
}

func (s *releaseSecurityState) semanticVersionReported() error {
	if s.body["version"] != version {
		return fmt.Errorf("version = %v, want %s", s.body["version"], version)
	}
	return nil
}

func (s *releaseSecurityState) completeLowercaseCommit() error {
	want := strings.Repeat("a", 40)
	if s.body["commit"] != want {
		return fmt.Errorf("commit = %v, want %s", s.body["commit"], want)
	}
	return nil
}

func (s *releaseSecurityState) responseNotStale() error {
	if got := s.rec.Header().Get("Cache-Control"); got != "no-store" {
		return fmt.Errorf("Cache-Control = %q", got)
	}
	return nil
}

func (s *releaseSecurityState) noCommitClaimed() error {
	if _, ok := s.body["commit"]; ok {
		return fmt.Errorf("invalid commit was asserted: %v", s.body)
	}
	return nil
}

func (s *releaseSecurityState) malformedMetadataVisible() error {
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	logBrokerCommitStatus()
	if !strings.Contains(logs.String(), "omitting it from /version") {
		return fmt.Errorf("invalid metadata was silent: %q", logs.String())
	}
	if strings.Contains(logs.String(), s.malformedCommit) {
		return fmt.Errorf("invalid metadata value leaked: %q", logs.String())
	}
	return nil
}

func (s *releaseSecurityState) nonGETRequest() error {
	return s.requestVersionWithMethod(http.MethodPost)
}

func (s *releaseSecurityState) methodNotAllowed() error {
	if s.rec.Code != http.StatusMethodNotAllowed {
		return fmt.Errorf("status = %d, want 405", s.rec.Code)
	}
	return nil
}

func TestReleaseDependencyAndVersionBDD(t *testing.T) {
	if os.Getenv("ROGERAI_RELEASE_SECURITY_UTF8_HELPER") == "1" {
		var iter norm.Iter
		iter.InitString(norm.NFC, "\xf3\xcc\x80")
		for !iter.Done() {
			_ = iter.Next()
		}
		return
	}

	st := &releaseSecurityState{t: t}
	tags := "~@release_gate"
	if os.Getenv("ROGERAI_RELEASE_GATE") == "1" {
		tags = ""
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = releaseSecurityState{t: t}
				t.Setenv("ROGERAI_BUILD_COMMIT", "")
				return ctx, nil
			})
			sc.Step(`^the complete module is built with the release Go toolchain$`, st.buildCompleteModule)
			sc.Step(`^the current Go vulnerability database is evaluated against every package$`, st.scanVulnerabilities)
			sc.Step(`^no reachable vulnerability is reported$`, st.noReachableVulnerability)
			sc.Step(`^the same vulnerability gate runs on main and before any tagged release is published$`, st.sameGateOnMainAndTags)
			sc.Step(`^an invalid UTF-8 sequence followed by combining input$`, st.invalidUTF8Input)
			sc.Step(`^the Unicode normalization dependency processes it$`, st.normalizeInvalidUTF8)
			sc.Step(`^processing terminates within the test deadline$`, st.normalizationTerminates)
			sc.Step(`^a broker built from a known source commit$`, st.knownCommit)
			sc.Step(`^the broker has no source commit metadata$`, st.missingCommit)
			sc.Step(`^the broker has malformed non-empty source commit metadata$`, st.malformedCommitMetadata)
			sc.Step(`^a client requests GET /version$`, st.requestVersion)
			sc.Step(`^the response is 200 JSON$`, st.responseIs200JSON)
			sc.Step(`^it contains the broker semantic version$`, st.semanticVersionReported)
			sc.Step(`^it contains the complete lowercase source commit$`, st.completeLowercaseCommit)
			sc.Step(`^the response cannot be served from a stale cache$`, st.responseNotStale)
			sc.Step(`^the semantic version is still reported$`, st.semanticVersionReported)
			sc.Step(`^no source commit is claimed$`, st.noCommitClaimed)
			sc.Step(`^malformed non-empty build metadata is visible to the operator$`, st.malformedMetadataVisible)
			sc.Step(`^a client sends a non-GET request to /version$`, st.nonGETRequest)
			sc.Step(`^the broker responds with method not allowed$`, st.methodNotAllowed)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/security/release_dependency_and_version.feature"},
			Tags:     tags,
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("release dependency and version behavior scenarios failed")
	}
}
