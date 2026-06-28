package agent

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestConfidentialPreflightNoDevice: the CI/dev box is not an SEV-SNP CVM (no
// /dev/sev-guest; the stub build returns "" on every non-linux/amd64 platform), so the
// cheap local preflight must return the typed ErrNoTEEDevice - the signal the CLI uses to
// abort `roger share --confidential` BEFORE any broker round-trip. (features/trust/
// confidential_attestation.feature: "no TEE device never sends a confidential claim".)
func TestConfidentialPreflightNoDevice(t *testing.T) {
	err := ConfidentialPreflight()
	if !errors.Is(err, ErrNoTEEDevice) {
		t.Fatalf("ConfidentialPreflight() = %v, want ErrNoTEEDevice on a non-CVM host", err)
	}
}

// TestAttestForRegistrationNoDeviceClearsClaim: on a host with no TEE, attestForRegistration
// must FAIL and CLEAR every confidential field rather than send a fake claim (the honesty
// rule) - and it must do so locally, never contacting the broker (here an invalid URL).
func TestAttestForRegistrationNoDeviceClearsClaim(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	reg := &protocol.NodeRegistration{
		Confidential: true, AttestKind: "sev-snp", AttestNonce: "abcd", Attestation: "blob",
	}
	err := attestForRegistration("http://broker.invalid.test", priv, reg)
	if err == nil {
		t.Fatal("attestForRegistration on a non-TEE host must error, not send a fake claim")
	}
	if reg.Confidential || reg.Attestation != "" || reg.AttestKind != "" || reg.AttestNonce != "" {
		t.Errorf("a failed attestation must CLEAR the claim; got %+v", reg)
	}
}

// TestRegisterResultParsesConfidential: the broker echoes whether the ◆ badge was granted
// in the register response; the agent must parse it so a node learns its outcome.
func TestRegisterResultParsesConfidential(t *testing.T) {
	var rr registerResult
	if err := json.Unmarshal([]byte(`{"confidential":true,"effective_offers":[]}`), &rr); err != nil {
		t.Fatalf("unmarshal register response: %v", err)
	}
	if !rr.Confidential {
		t.Errorf("registerResult.Confidential = false, want true from {\"confidential\":true}")
	}
	// Absent field => false (a standard register / older broker).
	var rr2 registerResult
	_ = json.Unmarshal([]byte(`{"effective_offers":[]}`), &rr2)
	if rr2.Confidential {
		t.Errorf("registerResult.Confidential = true with no field, want false")
	}
}

// TestSessionConfidentialAccessors: Session distinguishes "did not ask" from "asked and
// granted" from "asked but downgraded to standard" - the three states the CLI surfaces.
func TestSessionConfidentialAccessors(t *testing.T) {
	granted := &Session{cfg: Config{Confidential: true}, confidential: true}
	if !granted.RequestedConfidential() || !granted.Confidential() {
		t.Errorf("granted: requested=%v confidential=%v, want true/true", granted.RequestedConfidential(), granted.Confidential())
	}
	downgraded := &Session{cfg: Config{Confidential: true}, confidential: false}
	if !downgraded.RequestedConfidential() || downgraded.Confidential() {
		t.Errorf("downgraded: requested=%v confidential=%v, want true/false", downgraded.RequestedConfidential(), downgraded.Confidential())
	}
	none := &Session{cfg: Config{Confidential: false}}
	if none.RequestedConfidential() || none.Confidential() {
		t.Errorf("non-confidential session must report false/false")
	}
}
