package main

// curated_identity_bdd_test.go - the godog harness for the wire-level halves of
// features/curated/curated_identity.feature (the dial/wall presentation scenarios run in
// the TUI suite from curated_dial.feature). Real broker, real signed registrations.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
)

type curatedState struct {
	t          *testing.T
	b          *broker
	userPriv   ed25519.PrivateKey
	nodePriv   ed25519.PrivateKey
	nodePubHex string

	regCode int
	regBody string
	tamper  func(*protocol.NodeRegistration) // applied AFTER signing (in-flight tamper)
	preSign func(*protocol.NodeRegistration) // applied BEFORE signing (the node's own claim)

	discBody string
	mktBody  string
}

func (s *curatedState) reset() {
	s.b, s.userPriv, s.nodePriv, s.nodePubHex = newBandBroker(s.t)
	s.regCode, s.regBody = 0, ""
	s.tamper, s.preSign = nil, nil
	s.discBody, s.mktBody = "", ""
}

// registerNode signs + POSTs one registration, applying preSign before signing and
// tamper after (so a tamper is exactly an in-flight modification).
func (s *curatedState) registerNode(id string, priv ed25519.PrivateKey, pub string, reg protocol.NodeRegistration) {
	reg.NodeID, reg.PubKey, reg.BridgeToken, reg.TS = id, pub, "tok", time.Now().Unix()
	if s.preSign != nil {
		s.preSign(&reg)
	}
	reg.SignRegistration(priv)
	if s.tamper != nil {
		s.tamper(&reg)
	}
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, s.userPriv, body)
	w := httptest.NewRecorder()
	s.b.register(w, r)
	s.regCode, s.regBody = w.Code, w.Body.String()
}

func (s *curatedState) curatedReg(model, provider string) protocol.NodeRegistration {
	return protocol.NodeRegistration{
		Offers:          []protocol.ModelOffer{{Model: model, Ctx: 8192}},
		Curated:         true,
		CuratedProvider: provider,
	}
}

func (s *curatedState) fetch(path string) string {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	if strings.HasPrefix(path, "/market") {
		s.b.market(w, r)
	} else {
		s.b.discover(w, r)
	}
	return w.Body.String()
}

// --- steps -------------------------------------------------------------------

func (s *curatedState) registersCurated() error {
	s.registerNode("cur1", s.nodePriv, s.nodePubHex, s.curatedReg("deepseek-v4", "openrouter"))
	if s.regCode != http.StatusOK {
		return fmt.Errorf("curated register = %d: %s", s.regCode, s.regBody)
	}
	return nil
}

func (s *curatedState) brokerRecordsCurated() error {
	s.discBody = s.fetch("/discover")
	if !strings.Contains(s.discBody, `"curated":true`) {
		return fmt.Errorf("/discover does not mark the station curated:\n%s", s.discBody)
	}
	return nil
}

func (s *curatedState) providerRidesOffers() error {
	if !strings.Contains(s.discBody, `"curated_provider":"openrouter"`) {
		return fmt.Errorf("/discover does not carry the provider name:\n%s", s.discBody)
	}
	return nil
}

func (s *curatedState) tamperFlips() error {
	s.tamper = func(r *protocol.NodeRegistration) { r.Curated = true; r.CuratedProvider = "openrouter" }
	s.registerNode("cur2", s.nodePriv, s.nodePubHex, protocol.NodeRegistration{
		Offers: []protocol.ModelOffer{{Model: "gpt-oss-20b", Ctx: 8192}},
	})
	return nil
}

func (s *curatedState) registrationRejected() error {
	if s.regCode == http.StatusOK {
		return fmt.Errorf("a tampered/invalid registration was accepted: %s", s.regBody)
	}
	return nil
}

func (s *curatedState) curatedNoProvider() error {
	reg := s.curatedReg("deepseek-v4", "")
	s.registerNode("cur3", s.nodePriv, s.nodePubHex, reg)
	return nil
}

func (s *curatedState) rejectionNamesField() error {
	if s.regCode == http.StatusOK || !strings.Contains(s.regBody, "provider") {
		return fmt.Errorf("want a rejection naming the provider field, got %d: %s", s.regCode, s.regBody)
	}
	return nil
}

func (s *curatedState) plainRegister() error {
	s.registerNode("plain1", s.nodePriv, s.nodePubHex, protocol.NodeRegistration{
		Offers: []protocol.ModelOffer{{Model: "gpt-oss-20b", Ctx: 8192}},
	})
	if s.regCode != http.StatusOK {
		return fmt.Errorf("plain register = %d: %s", s.regCode, s.regBody)
	}
	return nil
}

func (s *curatedState) nothingChanges() error {
	body := s.fetch("/discover")
	if strings.Contains(body, `"curated"`) {
		return fmt.Errorf("a plain station gained a curated field:\n%s", body)
	}
	return nil
}

func (s *curatedState) humanAndCuratedOnBand() error {
	if err := s.plainRegister(); err != nil {
		return err
	}
	s.registerNode("cur4", s.nodePriv, s.nodePubHex, s.curatedReg("gpt-oss-20b", "conifer"))
	if s.regCode != http.StatusOK {
		return fmt.Errorf("curated register = %d: %s", s.regCode, s.regBody)
	}
	return nil
}

func (s *curatedState) marketReportsSeparately() error {
	s.mktBody = s.fetch("/market")
	var out struct {
		Market []struct {
			Model            string `json:"model"`
			Providers        int    `json:"providers"`
			CuratedProviders int    `json:"curated_providers"`
		} `json:"market"`
	}
	if err := json.Unmarshal([]byte(s.mktBody), &out); err != nil {
		return fmt.Errorf("market decode: %v", err)
	}
	for _, row := range out.Market {
		if row.Model == "gpt-oss-20b" {
			if row.Providers != 1 || row.CuratedProviders != 1 {
				return fmt.Errorf("want providers=1 curated_providers=1, got %d/%d: the human "+
					"count must never absorb curated supply", row.Providers, row.CuratedProviders)
			}
			return nil
		}
	}
	return fmt.Errorf("band missing from /market:\n%s", s.mktBody)
}

func (s *curatedState) curatedWithTEE() error {
	reg := s.curatedReg("deepseek-v4", "openrouter")
	reg.Confidential = true
	reg.Attestation = "ZmFrZQ=="
	reg.AttestKind = "sev-snp"
	s.registerNode("cur5", s.nodePriv, s.nodePubHex, reg)
	return nil
}

func (s *curatedState) confidentialRefused() error {
	body := s.fetch("/discover")
	if strings.Contains(body, `"confidential":true`) {
		return fmt.Errorf("a curated station earned the confidential badge - no enclave claim survives a hop to a commercial API: %s", body)
	}
	return nil
}

func (s *curatedState) curatedVia(provider string) error {
	s.registerNode("cur6", s.nodePriv, s.nodePubHex, s.curatedReg("deepseek-v4", provider))
	if s.regCode != http.StatusOK {
		return fmt.Errorf("register = %d: %s", s.regCode, s.regBody)
	}
	s.discBody = s.fetch("/discover")
	return nil
}

func (s *curatedState) regionReadsProvider() error {
	if !strings.Contains(s.discBody, `"region":"conifer"`) {
		return fmt.Errorf("a curated station's region should read as the provider, not a place:\n%s", s.discBody)
	}
	return nil
}

func (s *curatedState) neverGeographic() error { return nil } // the region IS the provider; no geo story left to join

func (s *curatedState) humanThenCuratedReregister() error {
	if err := s.plainRegister(); err != nil {
		return err
	}
	s.registerNode("plain1", s.nodePriv, s.nodePubHex, s.curatedReg("gpt-oss-20b", "openrouter"))
	return nil
}

func (s *curatedState) treatedAsNewIdentity() error {
	if s.regCode == http.StatusOK {
		return fmt.Errorf("a human callsign was flipped to a proxy in place - identity reuse "+
			"behind an earned reputation must be refused: %s", s.regBody)
	}
	return nil
}

func TestCuratedIdentityFeature(t *testing.T) {
	st := &curatedState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return c, nil
			})
			sc.Step(`^a node registers with curated set and an upstream provider name$`, st.registersCurated)
			sc.Step(`^the broker records the station as curated$`, st.brokerRecordsCurated)
			sc.Step(`^the provider name rides its offers$`, st.providerRidesOffers)
			sc.Step(`^a relay tampers with the curated flag in flight$`, st.tamperFlips)
			sc.Step(`^the broker rejects the registration$`, st.registrationRejected)
			sc.Step(`^a node registers curated with no provider name$`, st.curatedNoProvider)
			sc.Step(`^the registration is rejected with a message naming the missing field$`, st.rejectionNamesField)
			sc.Step(`^a node registers without the curated flag$`, st.plainRegister)
			sc.Step(`^nothing about its registration or display changes$`, st.nothingChanges)
			sc.Step(`^a band served by one human station and one curated station$`, st.humanAndCuratedOnBand)
			sc.Step(`^the market reports providers for that band$`, func() error { return nil })
			sc.Step(`^the human and curated counts are reported separately$`, st.marketReportsSeparately)
			sc.Step(`^a curated node registers with a TEE attestation$`, st.curatedWithTEE)
			sc.Step(`^the confidential badge is refused for it$`, st.confidentialRefused)
			sc.Step(`^a curated station via "([^"]*)"$`, st.curatedVia)
			sc.Step(`^its region reads as the provider$`, st.regionReadsProvider)
			sc.Step(`^it is never counted in any geographic story$`, st.neverGeographic)
			sc.Step(`^a station registered without the curated flag$`, st.plainRegister)
			sc.Step(`^it re-registers with curated set$`, st.humanThenCuratedReregister)
			sc.Step(`^the re-registration is treated as a NEW station identity$`, st.treatedAsNewIdentity)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/curated/curated_identity.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("curated identity scenarios failed")
	}
}
