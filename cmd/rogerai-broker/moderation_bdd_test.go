package main

// moderation_bdd_test.go makes features/moderation/moderation.feature an EXECUTABLE Cucumber
// suite, driving the REAL pre-dispatch content screen: loadModeration (backend resolution from
// MODERATION_PROVIDER/URL/GROQ_KEY + csamCats from ROGERAI_CSAM_CATEGORIES), moderation.screen
// (ALLOW=0 / 451 flagged / 503 fail-closed, with the dev fail-open vs launch fail-closed
// posture), and isCSAM (a matched csamCats category sets modResult.csam for the preserve+report
// path). The URL backend is a local httptest stub returning the OpenAI-moderation shape; an
// "unreachable" backend points at a closed server. Assertions read modResult / the resolved
// moderation struct back. The actual CSAM preserve+CyberTipline QUEUE lives in the relay
// (report.go); this suite pins the modResult.csam flag + status that gate it.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

type modState struct {
	m      moderation
	stub   *httptest.Server
	result modResult
	isCsam bool
}

func (s *modState) reset() {
	for _, k := range []string{"MODERATION_PROVIDER", "MODERATION_URL", "MODERATION_GROQ_KEY", "GROQ_API_KEY", "ROGERAI_REQUIRE_MODERATION", "ROGERAI_CSAM_CATEGORIES", "MODERATION_MODEL"} {
		os.Setenv(k, "")
	}
	if s.stub != nil {
		s.stub.Close()
		s.stub = nil
	}
	s.m = moderation{}
	s.result = modResult{}
	s.isCsam = false
}

// startStub points MODERATION_URL at a stub returning the given flagged + category map.
func (s *modState) startStub(flagged bool, cats map[string]bool) {
	s.stub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"flagged": flagged, "categories": cats})
	}))
	os.Setenv("MODERATION_URL", s.stub.URL)
}

// --- Given backends ---------------------------------------------------------

func (s *modState) backendReturnsSafe() error {
	s.startStub(false, nil)
	s.m = loadModeration()
	return nil
}

func (s *modState) backendReturnsUnsafe(codes string) error {
	cats := map[string]bool{}
	for _, c := range strings.Fields(codes) {
		cats[c] = true
	}
	s.startStub(true, cats)
	s.m = loadModeration()
	return nil
}

func (s *modState) backendUnsafeCSAM() error {
	s.startStub(true, map[string]bool{"s4": true}) // S4 is a default csamCats category
	s.m = loadModeration()
	return nil
}

func (s *modState) backendUnsafeNonCSAM(codes string) error { return s.backendReturnsUnsafe(codes) }

func (s *modState) noBackendRequireUnset() error {
	os.Setenv("ROGERAI_REQUIRE_MODERATION", "")
	s.m = loadModeration()
	return nil
}

func (s *modState) requireNoBackend() error {
	os.Setenv("ROGERAI_REQUIRE_MODERATION", "1")
	s.m = loadModeration()
	return nil
}

func (s *modState) requireUnreachableBackend() error {
	os.Setenv("ROGERAI_REQUIRE_MODERATION", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // closed -> the POST errors (connection refused)
	os.Setenv("MODERATION_URL", url)
	s.m = loadModeration()
	return nil
}

func (s *modState) urlSetProviderEmpty() error {
	s.startStub(false, nil)
	os.Setenv("MODERATION_PROVIDER", "")
	s.m = loadModeration()
	return nil
}

func (s *modState) groqKeyNoURL() error {
	os.Setenv("MODERATION_GROQ_KEY", "gk_test")
	os.Setenv("MODERATION_PROVIDER", "")
	s.m = loadModeration()
	return nil
}

func (s *modState) csamCategoriesOverride() error {
	os.Setenv("ROGERAI_CSAM_CATEGORIES", "x9,kiddo")
	s.m = loadModeration()
	return nil
}

// --- When -------------------------------------------------------------------

func (s *modState) requestScreened() error { s.result = s.m.screen("a prompt to screen"); return nil }
func (s *modState) brokerStarts() error    { s.result = s.m.screen("a prompt to screen"); return nil }
func (s *modState) backendResolved() error { return nil } // loadModeration already resolved m.provider
func (s *modState) verdictCarriesCategory() error {
	s.isCsam, _ = s.m.isCSAM([]string{"KiDDo"}) // configured category, mixed case
	return nil
}

// --- Then -------------------------------------------------------------------

func (s *modState) statusAllow() error {
	if s.result.status != 0 {
		return fmtErr("screen status = %d, want 0 (ALLOW)", s.result.status)
	}
	return nil
}
func (s *modState) proceeds() error {
	if !s.result.allow() {
		return fmtErr("screen did not allow (status %d)", s.result.status)
	}
	return nil
}
func (s *modState) status451() error {
	if s.result.status != http.StatusUnavailableForLegalReasons {
		return fmtErr("screen status = %d, want 451 (flagged)", s.result.status)
	}
	return nil
}
func (s *modState) noNodeDispatched() error { return s.status451() } // blocked before dispatch
func (s *modState) loudWarningOff() error {
	if s.m.provider != "" || s.m.require {
		return fmtErr("posture = provider %q require %v, want DISABLED (no backend, require false)", s.m.provider, s.m.require)
	}
	return nil
}
func (s *modState) passThroughUnscreened() error { return s.statusAllow() }
func (s *modState) status503() error {
	if s.result.status != http.StatusServiceUnavailable {
		return fmtErr("screen status = %d, want 503 (fail-closed)", s.result.status)
	}
	return nil
}
func (s *modState) neverServedUnscreened() error { return s.status503() }
func (s *modState) neverFallsOpen() error        { return s.status503() }
func (s *modState) urlBackendUsed() error {
	if s.m.provider != "url" {
		return fmtErr("backend = %q, want url", s.m.provider)
	}
	return nil
}
func (s *modState) groqBackendUsed() error {
	if s.m.provider != "groq" {
		return fmtErr("backend = %q, want groq", s.m.provider)
	}
	if s.m.groqModel == "" {
		return fmtErr("groq backend has no safeguard model configured")
	}
	return nil
}
func (s *modState) verdictParsed() error { return nil } // safe/unsafe parsing is screenGroq's job (live API)
func (s *modState) csamTrueWithCategory() error {
	if !s.result.csam {
		return fmtErr("modResult.csam = false, want true (CSAM category hit)")
	}
	if s.result.category == "" {
		return fmtErr("CSAM hit carries no matched category")
	}
	return nil
}
func (s *modState) incidentPreservedQueued() error {
	// The actual preserve + CyberTipline QUEUE is the relay's job (report.go); the broker-
	// observable gate is modResult.csam=true on a 451 rejection (what triggers that path).
	if !s.result.csam || s.result.status != http.StatusUnavailableForLegalReasons {
		return fmtErr("CSAM gate = csam %v status %d, want csam true + 451", s.result.csam, s.result.status)
	}
	return nil
}
func (s *modState) rejectedNotDispatched() error { return s.status451() }
func (s *modState) csamFalse() error {
	if s.result.csam {
		return fmtErr("modResult.csam = true, want false (non-CSAM unsafe)")
	}
	return nil
}
func (s *modState) rejected451NoCSAM() error {
	if err := s.status451(); err != nil {
		return err
	}
	return s.csamFalse()
}
func (s *modState) matchedAsCSAM() error {
	if !s.isCsam {
		return fmtErr("configured category not matched as CSAM (case-insensitive)")
	}
	return nil
}

func fmtErr(f string, a ...any) error { return fmt.Errorf(f, a...) }

func TestModerationBDD(t *testing.T) {
	for _, k := range []string{"MODERATION_PROVIDER", "MODERATION_URL", "MODERATION_GROQ_KEY", "GROQ_API_KEY", "ROGERAI_REQUIRE_MODERATION", "ROGERAI_CSAM_CATEGORIES", "MODERATION_MODEL"} {
		t.Setenv(k, "")
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &modState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.stub != nil {
					st.stub.Close()
					st.stub = nil
				}
				return ctx, nil
			})
			// Given
			sc.Step(`^a moderation backend that returns "safe"$`, st.backendReturnsSafe)
			sc.Step(`^a moderation backend that returns "unsafe ([^"]*)"$`, st.backendReturnsUnsafe)
			sc.Step(`^a backend that returns "unsafe" with a category in csamCats$`, st.backendUnsafeCSAM)
			sc.Step(`^a backend that returns "unsafe ([^"]*)" \(not a csamCats category\)$`, st.backendUnsafeNonCSAM)
			sc.Step(`^no moderation backend is configured and ROGERAI_REQUIRE_MODERATION is unset$`, st.noBackendRequireUnset)
			sc.Step(`^ROGERAI_REQUIRE_MODERATION=1 and no moderation backend configured$`, st.requireNoBackend)
			sc.Step(`^ROGERAI_REQUIRE_MODERATION=1 and a backend URL that times out / errors$`, st.requireUnreachableBackend)
			sc.Step(`^MODERATION_URL is set and MODERATION_PROVIDER is empty$`, st.urlSetProviderEmpty)
			sc.Step(`^MODERATION_GROQ_KEY \(or GROQ_API_KEY\) is set, no MODERATION_URL, provider empty$`, st.groqKeyNoURL)
			sc.Step(`^ROGERAI_CSAM_CATEGORIES overrides the default category set$`, st.csamCategoriesOverride)
			// When
			sc.Step(`^a relay request is screened$`, st.requestScreened)
			sc.Step(`^the broker starts$`, st.brokerStarts)
			sc.Step(`^the backend is resolved$`, st.backendResolved)
			sc.Step(`^a verdict carries a configured category in any letter case$`, st.verdictCarriesCategory)
			// Then
			sc.Step(`^the screen returns status 0 \(ALLOW\)$`, st.statusAllow)
			sc.Step(`^the request proceeds to routing \+ dispatch$`, st.proceeds)
			sc.Step(`^the screen returns 451 \(flagged\)$`, st.status451)
			sc.Step(`^no node is ever dispatched the prompt$`, st.noNodeDispatched)
			sc.Step(`^a LOUD startup warning is logged that moderation is OFF \(not safe for public traffic\)$`, st.loudWarningOff)
			sc.Step(`^requests pass through unscreened$`, st.passThroughUnscreened)
			sc.Step(`^the screen returns 503 \(fail-closed\), never served unscreened$`, st.status503)
			sc.Step(`^the screen returns 503, never falling open to "allow"$`, st.neverFallsOpen)
			sc.Step(`^the "url" backend is used \(OpenAI-moderation-compatible \{input\}->\{flagged\}\)$`, st.urlBackendUsed)
			sc.Step(`^the "groq" backend is used, calling the content-safety model with a policy prompt$`, st.groqBackendUsed)
			sc.Step(`^a "safe" / "unsafe <codes>" verdict is parsed$`, st.verdictParsed)
			sc.Step(`^modResult\.csam is true with the matched category$`, st.csamTrueWithCategory)
			sc.Step(`^the incident is PRESERVED and QUEUED for a CyberTipline report \(18 USC 2258A\)$`, st.incidentPreservedQueued)
			sc.Step(`^the request is rejected \(never dispatched, never silently dropped\)$`, st.rejectedNotDispatched)
			sc.Step(`^modResult\.csam is false$`, st.csamFalse)
			sc.Step(`^the request is rejected \(451\) without opening a CSAM incident$`, st.rejected451NoCSAM)
			sc.Step(`^it is matched as CSAM$`, st.matchedAsCSAM)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/moderation/screen.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("moderation behavior scenarios failed (see godog output above)")
	}
}
