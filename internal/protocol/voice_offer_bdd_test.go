package protocol

// voice_offer_bdd_test.go makes features/voice/offer_modality.feature EXECUTABLE under godog,
// driving the REAL ModelOffer.Normalize / ValidModality so the modality+unit contract fails red
// if it regresses. Layer 1 of the voice/audio modality (VOICE-AUDIO-DESIGN.md).
// Reuses bddErr/errExpect from auth_bdd_test.go (same package).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cucumber/godog"
)

type voiceOfferState struct {
	o         ModelOffer
	offers    []ModelOffer
	roundTrip ModelOffer
	lastValid bool
}

func (s *voiceOfferState) reset() { *s = voiceOfferState{} }

func (s *voiceOfferState) nodeRegistering() error { return nil }

func (s *voiceOfferState) offerNoModality(model string) error {
	s.o = ModelOffer{Model: model}
	return nil
}
func (s *voiceOfferState) offerModelModality(model, modality string) error {
	s.o = ModelOffer{Model: model, Modality: modality}
	return nil
}
func (s *voiceOfferState) offerModality(modality string) error {
	s.o = ModelOffer{Modality: modality}
	return nil
}
func (s *voiceOfferState) offerModalityUnit(modality, unit string) error {
	s.o = ModelOffer{Modality: modality, Unit: unit}
	return nil
}

// the Given establishes the offer FROM raw JSON (a pre-voice node's payload).
func (s *voiceOfferState) rawJSON(j string) error {
	s.o = ModelOffer{}
	return json.Unmarshal([]byte(j), &s.o)
}

func (s *voiceOfferState) registerOffers(t *godog.Table) error {
	s.offers = nil
	for i, row := range t.Rows {
		if i == 0 {
			continue // header: model | modality
		}
		s.offers = append(s.offers, ModelOffer{Model: row.Cells[0].Value, Modality: row.Cells[1].Value})
	}
	return nil
}

func (s *voiceOfferState) normalizeOffer() error { s.o.Normalize(); return nil }
func (s *voiceOfferState) normalizeOffers() error {
	for i := range s.offers {
		s.offers[i].Normalize()
	}
	return nil
}
func (s *voiceOfferState) validateOffer() error { s.lastValid = s.o.ValidModality(); return nil }
func (s *voiceOfferState) marshalRoundTrip() error {
	b, err := json.Marshal(s.o)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &s.roundTrip)
}

func (s *voiceOfferState) modalityIs(m string) error {
	if s.o.Modality != m {
		return errExpect("modality " + m + ", got " + s.o.Modality)
	}
	return nil
}
func (s *voiceOfferState) unitIs(u string) error {
	if s.o.Unit != u {
		return errExpect("unit " + u + ", got " + s.o.Unit)
	}
	return nil
}
func (s *voiceOfferState) offerResult(result string) error {
	want := result == "accepted"
	if s.lastValid != want {
		return errExpect("offer " + result)
	}
	return nil
}
func (s *voiceOfferState) eachCanonical() error {
	for _, o := range s.offers {
		if o.Unit != canonicalUnit(o.Modality) {
			return errExpect("offer " + o.Model + " unit " + canonicalUnit(o.Modality) + ", got " + o.Unit)
		}
	}
	return nil
}
func (s *voiceOfferState) allThreeModalities() error {
	seen := map[string]bool{}
	for _, o := range s.offers {
		seen[o.Modality] = true
	}
	for _, m := range []string{ModalityChat, ModalityTTS, ModalitySTT} {
		if !seen[m] {
			return errExpect("an offer with modality " + m)
		}
	}
	return nil
}
func (s *voiceOfferState) modalityUnitPreserved() error {
	if s.roundTrip.Modality != s.o.Modality || s.roundTrip.Unit != s.o.Unit {
		return errExpect("modality+unit preserved across JSON round-trip")
	}
	return nil
}

func TestVoiceOfferBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &voiceOfferState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^a node registering an offer with the broker$`, st.nodeRegistering)
			sc.Step(`^an offer for model "([^"]*)" with no modality field and no unit field$`, st.offerNoModality)
			sc.Step(`^the raw offer JSON (.+)$`, st.rawJSON)
			sc.Step(`^an offer for model "([^"]*)" with modality "([^"]*)"$`, st.offerModelModality)
			sc.Step(`^an offer with modality "([^"]*)" and (?:a claimed )?unit "([^"]*)"$`, st.offerModalityUnit)
			sc.Step(`^an offer with modality "([^"]*)"$`, st.offerModality)
			sc.Step(`^a node registers offers:$`, st.registerOffers)
			sc.Step(`^the broker normalizes the offer$`, st.normalizeOffer)
			sc.Step(`^the broker normalizes the offers$`, st.normalizeOffers)
			sc.Step(`^the broker validates the offer$`, st.validateOffer)
			sc.Step(`^it is marshalled to JSON and decoded back$`, st.marshalRoundTrip)
			sc.Step(`^the offer modality is "([^"]*)"$`, st.modalityIs)
			sc.Step(`^the offer unit is "([^"]*)"$`, st.unitIs)
			sc.Step(`^it is priced as credits per 1,000,000 tokens, exactly as before$`, func() error { return st.unitIs(UnitToken) })
			sc.Step(`^its price is read as credits per 1,000,000 input characters$`, func() error { return st.unitIs(UnitChar) })
			sc.Step(`^its price is read as credits per 1,000,000 audio-bytes$`, func() error { return st.unitIs(UnitByte) })
			sc.Step(`^the offer is "([^"]*)"$`, st.offerResult)
			sc.Step(`^each offer keeps its own modality and canonical unit$`, st.eachCanonical)
			sc.Step(`^the node is discoverable under all three$`, st.allThreeModalities)
			sc.Step(`^the modality and unit are preserved$`, st.modalityUnitPreserved)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/voice/offer_modality.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("voice/offer_modality behavior scenarios failed (see godog output above)")
	}
}
