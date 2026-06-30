package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// registerMaybeConfidential posts an owner-bound registration that optionally CLAIMS
// confidential (with a nonce + a placeholder attestation the mock verifier accepts/rejects)
// and returns the decoded response body + status, so a test can assert the broker echoes
// the confidential-grant outcome.
func registerMaybeConfidential(t *testing.T, b *broker, nodeID string, nodePriv ed25519.PrivateKey, nodePubHex string, userPriv ed25519.PrivateKey, claim bool, nonce string) (map[string]any, int) {
	t.Helper()
	reg := protocol.NodeRegistration{
		NodeID: nodeID, PubKey: nodePubHex, BridgeToken: "tok-" + nodeID, TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: "m", Ctx: 4096, PriceOut: 1.0}},
	}
	if claim {
		reg.Confidential = true
		reg.AttestKind = attestSEVSNP
		reg.AttestNonce = nonce
		reg.Attestation = base64.StdEncoding.EncodeToString([]byte("quote"))
	}
	reg.SignRegistration(nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	b.register(w, r)
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return resp, w.Code
}

// TestRegisterResponseEchoesConfidentialGrant: the OK register response carries
// "confidential": <granted> so a claimant LEARNS its outcome (no silent fail-soft
// downgrade). Three cases: granted, downgraded (require=0 + failing verifier), no claim.
// (features/trust/confidential_attestation.feature: "A confidential claimant always
// learns whether the badge was granted".)
func TestRegisterResponseEchoesConfidentialGrant(t *testing.T) {
	// GRANTED: a verifying mock + an allowlisted measurement + a fresh nonce -> true.
	b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
	b.attest = mockRegistry(true)
	ch := b.attest.issueNonce()
	resp, code := registerMaybeConfidential(t, b, "cn1", nodePriv, nodePubHex, userPriv, true, ch.Nonce)
	if code != http.StatusOK {
		t.Fatalf("granted register = %d, want 200", code)
	}
	if got, ok := resp["confidential"].(bool); !ok || !got {
		t.Errorf("granted register confidential echo = %v, want true (resp=%v)", resp["confidential"], resp)
	}

	// DOWNGRADED: require=0 (default) + a verifier that always fails -> registers as a
	// STANDARD node (200) but echoes confidential:false so the CLI can warn.
	b2, userPriv2, nodePriv2, nodePub2 := newBandBroker(t)
	b2.attest = mockRegistry(false)
	ch2 := b2.attest.issueNonce()
	resp2, code2 := registerMaybeConfidential(t, b2, "cn2", nodePriv2, nodePub2, userPriv2, true, ch2.Nonce)
	if code2 != http.StatusOK {
		t.Fatalf("fail-soft register = %d, want 200 (downgraded to standard)", code2)
	}
	if got, ok := resp2["confidential"].(bool); !ok || got {
		t.Errorf("downgraded register confidential echo = %v, want false", resp2["confidential"])
	}

	// NO CLAIM: a standard register echoes confidential:false (and never needs b.attest).
	b3, userPriv3, nodePriv3, nodePub3 := newBandBroker(t)
	resp3, code3 := registerMaybeConfidential(t, b3, "cn3", nodePriv3, nodePub3, userPriv3, false, "")
	if code3 != http.StatusOK {
		t.Fatalf("no-claim register = %d, want 200", code3)
	}
	if got, ok := resp3["confidential"].(bool); !ok || got {
		t.Errorf("no-claim register confidential echo = %v, want false", resp3["confidential"])
	}
}

// TestRegisterRequireModeRejectsConfidentialClaim: with require=1 a claim that fails
// attestation is REJECTED (403) - never silently downgraded - so the operator's policy
// ("confidential means confidential") holds at the registration boundary.
func TestRegisterRequireModeRejectsConfidentialClaim(t *testing.T) {
	b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
	b.attest = mockRegistry(false) // verifier always fails
	b.attest.required = true       // ROGERAI_TEE_REQUIRE
	ch := b.attest.issueNonce()
	_, code := registerMaybeConfidential(t, b, "cn-req", nodePriv, nodePubHex, userPriv, true, ch.Nonce)
	if code != http.StatusForbidden {
		t.Fatalf("require-mode failed claim = %d, want 403", code)
	}
}
