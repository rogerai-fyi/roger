package protocol

// Makes features/trust/receipt_signature_versions.feature executable. It lives in the
// protocol package because building a LEGACY fixture requires signing over the
// unexported node canonical form - the one thing an external package cannot do.

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/cucumber/godog"
)

type svState struct {
	brokerPub  ed25519.PublicKey
	brokerPriv ed25519.PrivateKey
	nodePriv   ed25519.PrivateKey
	rec        UsageReceipt
	ok, covers bool
}

func (s *svState) reset() {
	s.brokerPub, s.brokerPriv, _ = ed25519.GenerateKey(nil)
	_, s.nodePriv, _ = ed25519.GenerateKey(nil)
	s.rec = UsageReceipt{
		RequestID: "req-1", NodeID: "node-1", Model: "m",
		PromptTokens: 100, CompletionTokens: 200, PriceIn: 1, PriceOut: 2, TS: 1,
	}
	s.rec.SignNode(s.nodePriv)
	s.ok, s.covers = false, false
}

func (s *svState) key() string { return hexKey(s.brokerPub) }

// withBrokerFields sets the fields whose coverage is the whole point.
func (s *svState) withBrokerFields() {
	s.rec.BrokerPromptTokens, s.rec.BrokerCompletionTokens, s.rec.GrantID = 90, 180, "g1"
}

func (s *svState) brokerCounterSigns() error {
	s.withBrokerFields()
	s.rec.SignBroker(s.brokerPriv)
	return nil
}

func (s *svState) carriesVersion(v int) error {
	if s.rec.SigVersion != v {
		return fmt.Errorf("SigVersion = %d, want %d", s.rec.SigVersion, v)
	}
	return nil
}

func (s *svState) verifiesOverBrokerForm() error {
	ok, covers := s.rec.VerifyBrokerCoverage(s.key())
	if !ok || !covers {
		return fmt.Errorf("want verified+covering, got ok=%v covers=%v", ok, covers)
	}
	return nil
}

// legacyCoSign reproduces the pre-repair behaviour: counter-sign the NODE form.
func (s *svState) legacyCoSign() error {
	s.withBrokerFields()
	s.rec.SigVersion = 0
	s.rec.BrokerSig = signHex(s.brokerPriv, s.rec.nodeSigningBytes())
	return nil
}

func (s *svState) noVersionTag() error {
	if s.rec.SigVersion != 0 {
		return fmt.Errorf("a legacy receipt must carry no version, got %d", s.rec.SigVersion)
	}
	return nil
}

func (s *svState) verifyBrokerRuns() error {
	s.ok, s.covers = s.rec.VerifyBrokerCoverage(s.key())
	return nil
}

func (s *svState) verifiesUnderLegacyRule() error {
	if !s.ok {
		return fmt.Errorf("a legacy receipt must still verify")
	}
	return nil
}

func (s *svState) reportedLegacy() error {
	if s.covers {
		return fmt.Errorf("a legacy signature must NOT be reported as covering the billed counts")
	}
	return nil
}

func (s *svState) alterBrokerCompletion() error {
	s.rec.BrokerCompletionTokens = 1
	s.ok, s.covers = s.rec.VerifyBrokerCoverage(s.key())
	return nil
}

func (s *svState) stillVerifiesLegacy() error { return s.verifiesUnderLegacyRule() }

func (s *svState) declaresVersion1() error {
	s.withBrokerFields()
	s.rec.SigVersion = 1
	return nil
}

func (s *svState) signedOverLegacyForm() error {
	v := s.rec.SigVersion
	s.rec.SigVersion = 0
	sig := signHex(s.brokerPriv, s.rec.nodeSigningBytes())
	s.rec.SigVersion = v
	s.rec.BrokerSig = sig
	return nil
}

func (s *svState) verifyBrokerRejects() error {
	if s.rec.VerifyBroker(s.key()) {
		return fmt.Errorf("VerifyBroker must reject this receipt")
	}
	return nil
}

func (s *svState) declaresVersionStr(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	if err := s.brokerCounterSigns(); err != nil {
		return err
	}
	s.rec.SigVersion = n
	return nil
}

func (s *svState) rejectedWithoutTryingEitherForm() error {
	if s.ok {
		return fmt.Errorf("an unknown signature version must be rejected")
	}
	if s.covers {
		return fmt.Errorf("an unknown version must never report coverage")
	}
	return nil
}

func TestReceiptSignatureVersionsBDD(t *testing.T) {
	st := &svState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^the broker counter-signs a receipt$`, st.brokerCounterSigns)
			sc.Step(`^the receipt carries broker signature version (\d+)$`, st.carriesVersion)
			sc.Step(`^VerifyBroker verifies it over the broker canonical form$`, st.verifiesOverBrokerForm)
			sc.Step(`^a receipt co-signed before the coverage repair, over the node canonical form$`, st.legacyCoSign)
			sc.Step(`^it carries no broker signature version$`, st.noVersionTag)
			sc.Step(`^VerifyBroker checks it$`, st.verifyBrokerRuns)
			sc.Step(`^it verifies under the legacy rule$`, st.verifiesUnderLegacyRule)
			sc.Step(`^the receipt is reported as legacy-signed so its billed counts are known to be uncovered$`, st.reportedLegacy)
			sc.Step(`^a legacy co-signed receipt$`, st.legacyCoSign)
			sc.Step(`^its BrokerCompletionTokens is altered$`, st.alterBrokerCompletion)
			sc.Step(`^VerifyBroker still verifies, because the legacy form never covered that field$`, st.stillVerifiesLegacy)
			sc.Step(`^the verification result reports the legacy coverage so a caller cannot mistake it for proof$`, st.reportedLegacy)
			sc.Step(`^a receipt declaring broker signature version 1$`, st.declaresVersion1)
			sc.Step(`^its signature is actually over the legacy node form$`, st.signedOverLegacyForm)
			sc.Step(`^VerifyBroker rejects it$`, st.verifyBrokerRejects)
			sc.Step(`^a receipt declaring broker signature version "([^"]*)"$`, st.declaresVersionStr)
			sc.Step(`^it is rejected without attempting either canonical form$`, st.rejectedWithoutTryingEitherForm)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/trust/receipt_signature_versions.feature"},
			TestingT: t,
			Output:   os.Stdout,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("receipt signature-version scenarios failed (see godog output above)")
	}
}
