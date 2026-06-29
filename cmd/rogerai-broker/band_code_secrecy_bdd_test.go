package main

// band_code_secrecy_bdd_test.go makes features/security/band_code_secrecy.feature
// EXECUTABLE, driving the REAL mint -> persist -> resolve path. It pins the fixed
// vulnerability as a permanent regression: the one-time full code resolves the band, but
// the PERSISTED cosmetic display cannot reconstruct or resolve it. No mocks - it registers
// a private band on a real broker (Mem store) and hits bandResolve with each string.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

type bandSecrecyState struct {
	t *testing.T

	b       *broker
	code    string // the one-time full band_code (with the secret tail)
	display string // the persisted, masked band_display
}

func (s *bandSecrecyState) reset() {
	b, userPriv, nodePriv, nodePubHex := newBandBroker(s.t)
	s.b = b
	resp, st := registerPrivate(s.t, b, nodePriv, nodePubHex, userPriv, true)
	if st != http.StatusOK {
		s.t.Fatalf("private register = %d, want 200", st)
	}
	s.code, _ = resp["band_code"].(string)
	s.display, _ = resp["band_display"].(string)
}

// resolveStatus drives POST /bands/resolve with freq and returns (status, body).
func (s *bandSecrecyState) resolveStatus(freq string) (int, string) {
	body, _ := json.Marshal(map[string]string{"freq": freq})
	w := httptest.NewRecorder()
	s.b.bandResolve(w, httptest.NewRequest(http.MethodPost, "/bands/resolve", bytes.NewReader(body)))
	return w.Code, w.Body.String()
}

func (s *bandSecrecyState) ownerMintsBand() error {
	if s.code == "" {
		return fmt.Errorf("mint did not return a one-time band_code")
	}
	if s.display == "" {
		return fmt.Errorf("mint did not return a cosmetic band_display")
	}
	if s.display == s.code {
		return fmt.Errorf("persisted display equals the one-time code %q - the secret would be stored", s.code)
	}
	return nil
}

func (s *bandSecrecyState) oneTimeCodeResolves() error {
	st, body := s.resolveStatus(s.code)
	if st != http.StatusOK || !strings.Contains(body, `"node_id":"priv1"`) {
		return fmt.Errorf("one-time code resolve = %d body=%s, want 200 with the hidden node", st, body)
	}
	return nil
}

func (s *bandSecrecyState) persistedDisplayDoesNotResolve() error {
	st, body := s.resolveStatus(s.display)
	if st != http.StatusNotFound {
		return fmt.Errorf("persisted display resolve = %d, want 404 (it must not reach the band); body=%s", st, body)
	}
	// Uniform negative (no oracle): the same empty-offers shape a wrong code returns.
	if !strings.Contains(body, `"offers":[]`) {
		return fmt.Errorf("persisted display resolve body = %s, want the uniform {\"offers\":[]} negative", body)
	}
	return nil
}

func (s *bandSecrecyState) displayHasNoRecoverableTail() error {
	if tail := protocol.CanonicalBandTail(s.display); tail != "" {
		return fmt.Errorf("persisted display %q canonicalizes to a tail %q - it must be masked", s.display, tail)
	}
	return nil
}

func TestBandCodeSecrecyBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &bandSecrecyState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^an owner mints a private band$`, st.ownerMintsBand)
			sc.Step(`^the one-time frequency code resolves to the hidden node$`, st.oneTimeCodeResolves)
			sc.Step(`^the persisted cosmetic display does NOT resolve the band$`, st.persistedDisplayDoesNotResolve)
			sc.Step(`^the persisted display carries no recoverable secret tail$`, st.displayHasNoRecoverableTail)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/security/band_code_secrecy.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("band-code-secrecy behavior scenarios failed (see godog output above)")
	}
}
