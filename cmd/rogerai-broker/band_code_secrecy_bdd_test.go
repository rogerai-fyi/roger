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
	"github.com/rogerai-fyi/roger/internal/store"
)

type bandSecrecyState struct {
	t *testing.T

	b          *broker
	code       string // the one-time full band_code (with the secret tail)
	display    string // the persisted, masked band_display
	legacyCode string // a pre-fix band's recoverable display (== the resolvable full code)
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
	s.legacyCode = ""
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

// --- legacy re-mask migration (the second scenario) --------------------------

// givenLegacyBand seeds a band as a PRE-FIX mint persisted it: the stored code_display IS
// the resolvable full code ("freq · TAIL"), bound to the already-live hidden node "priv1".
// This is the on-disk state the one-time migration must scrub.
func (s *bandSecrecyState) givenLegacyBand() error {
	s.legacyCode = "147.520 MHz · 8F3K-9M2Q"
	if protocol.CanonicalBandTail(s.legacyCode) == "" {
		return fmt.Errorf("legacy display %q is not recoverable - test premise wrong", s.legacyCode)
	}
	return s.b.db.CreateBand(store.Band{
		ID: "band_legacy", CodeHash: protocol.BandCodeHash(s.legacyCode),
		CodeDisplay: s.legacyCode, Owner: "legacy-owner", NodeID: "priv1",
	})
}

// storedDisplay reads the legacy band's CURRENT persisted display (by its unchanged hash).
func (s *bandSecrecyState) storedDisplay() (string, error) {
	bnd, ok, err := s.b.db.BandByCodeHash(protocol.BandCodeHash(s.legacyCode))
	if err != nil || !ok {
		return "", fmt.Errorf("legacy band lost (the code_hash must be unchanged): ok=%v err=%v", ok, err)
	}
	return bnd.CodeDisplay, nil
}

func (s *bandSecrecyState) legacyDisplayResolves() error {
	// PRE-migration: the stored display (== the code) resolves the hidden node (the vuln).
	st, body := s.resolveStatus(s.legacyCode)
	if st != http.StatusOK || !strings.Contains(body, `"node_id":"priv1"`) {
		return fmt.Errorf("legacy display resolve = %d body=%s, want 200 with the hidden node (the vuln must be present pre-migration)", st, body)
	}
	return nil
}

func (s *bandSecrecyState) runRemaskMigration() error {
	s.b.remaskExistingBands()
	return nil
}

func (s *bandSecrecyState) legacyDisplayNoLongerResolves() error {
	disp, err := s.storedDisplay()
	if err != nil {
		return err
	}
	if disp == s.legacyCode {
		return fmt.Errorf("display not re-masked: still the resolvable code %q", disp)
	}
	if tail := protocol.CanonicalBandTail(disp); tail != "" {
		return fmt.Errorf("re-masked display %q still canonicalizes to a recoverable tail %q", disp, tail)
	}
	st, body := s.resolveStatus(disp)
	if st != http.StatusNotFound || !strings.Contains(body, `"offers":[]`) {
		return fmt.Errorf("re-masked display resolve = %d body=%s, want the uniform 404 negative", st, body)
	}
	return nil
}

func (s *bandSecrecyState) ownerCodeStillResolves() error {
	// The owner's saved one-time code still tunes in (the code_hash was left unchanged).
	st, body := s.resolveStatus(s.legacyCode)
	if st != http.StatusOK || !strings.Contains(body, `"node_id":"priv1"`) {
		return fmt.Errorf("owner full-code resolve after migration = %d body=%s, want 200 (hash must be intact)", st, body)
	}
	return nil
}

func (s *bandSecrecyState) migrationIsIdempotent() error {
	before, err := s.storedDisplay()
	if err != nil {
		return err
	}
	s.b.remaskExistingBands() // a second run must be a no-op
	after, err := s.storedDisplay()
	if err != nil {
		return err
	}
	if after != before {
		return fmt.Errorf("second migration changed the display %q -> %q (not idempotent)", before, after)
	}
	return s.ownerCodeStillResolves()
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
			// legacy re-mask migration
			sc.Step(`^a band persisted with the OLD recoverable display \(legacy state\)$`, st.givenLegacyBand)
			sc.Step(`^the legacy display resolves the hidden node \(the vulnerability\)$`, st.legacyDisplayResolves)
			sc.Step(`^the broker runs the band-display re-mask migration$`, st.runRemaskMigration)
			sc.Step(`^the persisted display no longer resolves the band$`, st.legacyDisplayNoLongerResolves)
			sc.Step(`^the owner's saved frequency code still resolves the band \(hash unchanged\)$`, st.ownerCodeStillResolves)
			sc.Step(`^running the migration again changes nothing$`, st.migrationIsIdempotent)
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
