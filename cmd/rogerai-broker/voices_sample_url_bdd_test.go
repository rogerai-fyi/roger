package main

// voices_sample_url_bdd_test.go makes features/voice/sample_url_passthrough.feature an
// EXECUTABLE Cucumber suite - the LIVE-VERIFIED (2026-07-02) regression where a tts offer
// registered WITH SampleURL lost it on the register -> GET /voices trip while its sibling
// fields (name, language) survived. It reuses the namespacing suite's nsState harness so the
// trip is the REAL one end-to-end: a signed, owner-bound POST /nodes/register through
// b.register (moderation screen configured and required, a local httptest stub - never a mock
// of b.mod), then the REAL computeVoices aggregation; the restart scenarios re-hydrate a
// FRESH broker from the SAME real store (ephemeral Postgres when ROGERAI_TEST_DATABASE_URL is
// set - the cover-gate provisions one - else the in-memory reference store). NO mocks.

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// suState wraps the namespacing harness with the last-registered offer + station so the
// re-registration steps can re-post the SAME node id (same deterministic node key, same
// owner, same model+name) with only the sample_url varied - exactly what a node does when
// its operator adds a voice sample to an already-on-air voice.
type suState struct {
	*nsState
	lastOffer   protocol.ModelOffer
	lastStation string
}

// registerVoiceSample posts a signed, owner-bound tts register whose offer carries the full
// display metadata (name, language, sample_url - sample may be empty for the "no sample_url"
// steps), through the REAL register path.
func (s *suState) registerVoiceSample(login, model, name, lang, sample string) error {
	o := s.owner(login)
	off := protocol.ModelOffer{Model: model, Modality: protocol.ModalityTTS, Name: name, Language: lang, SampleURL: sample}
	reg := protocol.NodeRegistration{
		NodeID: s.nodeID("node", login), TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{off}, Station: stationFor(login),
	}
	s.lastOffer, s.lastStation = off, stationFor(login)
	s.doRegister(reg, &o)
	return nil
}

func (s *suState) registerVoiceNoSample(login, model, name, lang string) error {
	return s.registerVoiceSample(login, model, name, lang, "")
}

// reRegisterSample re-posts the LAST registration's node id with the SAME offer, now carrying
// the given sample_url (empty = removed). doRegister derives the node key deterministically
// from the node id, so this is the idempotent same-key re-register the TOFU binding allows.
func (s *suState) reRegisterSample(sample string) error {
	off := s.lastOffer
	off.SampleURL = sample
	reg := protocol.NodeRegistration{
		NodeID: s.lastNodeID, TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{off}, Station: s.lastStation,
	}
	s.lastOffer = off
	s.doRegister(reg, s.lastOwner)
	return nil
}

func (s *suState) reRegisterNoSample() error { return s.reRegisterSample("") }

// registrationSucceeded guards the trip: a scenario about the /voices VIEW must never "pass"
// by the register itself failing (that would be a different bug wearing the same green).
func (s *suState) registrationSucceeded() error {
	if s.lastCode != http.StatusOK {
		return fmt.Errorf("register = %d (%q), want 200", s.lastCode, s.lastMsg)
	}
	return nil
}

// brokerRestart simulates a redeploy: a BRAND-NEW broker over the SAME store re-hydrates the
// persisted node registry (rehydrateNodes), exactly as main() does at startup. The owner
// bindings live in the same store, so /voices attribution resolves as in production.
func (s *suState) brokerRestart() error {
	db := s.b.db
	s.b = newBroker(db)
	s.b.rehydrateNodes()
	return nil
}

func (s *suState) sampleURLIs(rawID, want string) error {
	v, ok := s.byRaw(rawID)
	if !ok {
		return fmt.Errorf("voice %q not listed; %d voices in payload: %s", rawID, len(s.voices), s.payload)
	}
	if v.SampleURL != want {
		return fmt.Errorf("voice %q sample_url = %q, want %q (payload: %s)", rawID, v.SampleURL, want, s.payload)
	}
	return nil
}

func (s *suState) sampleURLAbsent(rawID string) error {
	v, ok := s.byRaw(rawID)
	if !ok {
		return fmt.Errorf("voice %q not listed; %d voices in payload: %s", rawID, len(s.voices), s.payload)
	}
	if v.SampleURL != "" {
		return fmt.Errorf("voice %q sample_url = %q, want it absent (payload: %s)", rawID, v.SampleURL, s.payload)
	}
	return nil
}

func (s *suState) nameLangIs(rawID, name, lang string) error {
	v, ok := s.byRaw(rawID)
	if !ok {
		return fmt.Errorf("voice %q not listed; %d voices in payload: %s", rawID, len(s.voices), s.payload)
	}
	if v.Name != name || v.Language != lang {
		return fmt.Errorf("voice %q name/language = %q/%q, want %q/%q", rawID, v.Name, v.Language, name, lang)
	}
	return nil
}

func TestVoiceSampleURLPassthroughBDD(t *testing.T) {
	st := &suState{nsState: &nsState{}}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset(t)
				st.lastOffer, st.lastStation = protocol.ModelOffer{}, ""
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.modStub != nil {
					st.modStub.Close()
					st.modStub = nil
				}
				return ctx, nil
			})

			// Background (same wording as the namespacing suite)
			sc.Step(`^the broker with content screening configured and required$`, st.brokerScreeningRequired)
			sc.Step(`^an owner "([^"]*)" \(GitHub login "([^"]*)"\) who has logged in$`, st.ownerLoggedIn)

			// register / re-register / restart
			sc.Step(`^"([^"]*)" registers an on-air tts offer with model "([^"]*)" named "([^"]*)", language "([^"]*)", sample_url "([^"]*)"$`, st.registerVoiceSample)
			sc.Step(`^"([^"]*)" registers an on-air tts offer with model "([^"]*)" named "([^"]*)", language "([^"]*)" and no sample_url$`, st.registerVoiceNoSample)
			sc.Step(`^the same node re-registers with sample_url "([^"]*)"$`, st.reRegisterSample)
			sc.Step(`^the same node re-registers with no sample_url$`, st.reRegisterNoSample)
			sc.Step(`^the registration succeeded$`, st.registrationSucceeded)
			sc.Step(`^the broker restarts and re-hydrates from the store$`, st.brokerRestart)

			// When
			sc.Step(`^an anonymous GET /voices arrives$`, st.getVoicesStep)

			// Then
			sc.Step(`^a voice with raw id "([^"]*)" is listed$`, st.listsRaw)
			sc.Step(`^the voice with raw id "([^"]*)" carries sample_url "([^"]*)"$`, st.sampleURLIs)
			sc.Step(`^the voice with raw id "([^"]*)" carries no sample_url$`, st.sampleURLAbsent)
			sc.Step(`^the voice with raw id "([^"]*)" carries name "([^"]*)" and language "([^"]*)"$`, st.nameLangIs)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/voice/sample_url_passthrough.feature"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("voice/sample_url_passthrough behavior scenarios failed (see godog output above)")
	}
}
