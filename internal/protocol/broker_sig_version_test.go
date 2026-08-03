package protocol

import (
	"crypto/ed25519"
	"testing"

	"github.com/stretchr/testify/require"
)

// Receipts co-signed before the coverage repair were signed over the NODE canonical
// form and are already persisted. Without a version tag a verifier cannot tell which
// bytes a receipt was signed over, so every historical receipt would read as forged.
// SigVersion records it: absent/0 = legacy node form, 1 = broker superset form.

func legacyCoSigned(t *testing.T) (UsageReceipt, ed25519.PublicKey) {
	t.Helper()
	_, nodePriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	brokerPub, brokerPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	rec := UsageReceipt{
		RequestID: "req-legacy", NodeID: "node-1", Model: "m",
		PromptTokens: 100, CompletionTokens: 200, PriceIn: 1, PriceOut: 2, TS: 1,
	}
	rec.SignNode(nodePriv)
	rec.BrokerPromptTokens, rec.BrokerCompletionTokens, rec.GrantID = 90, 180, "g1"
	// The OLD behaviour: sign the node form even though broker fields are set.
	rec.BrokerSig = signHex(brokerPriv, rec.nodeSigningBytes())
	rec.SigVersion = 0 // legacy receipts carry no version
	return rec, brokerPub
}

func TestNewReceiptDeclaresVersion1(t *testing.T) {
	rec, _, brokerPub := signedPair(t)
	require.Equal(t, 1, rec.SigVersion, "SignBroker must stamp the current signature version")
	require.True(t, rec.VerifyBroker(hexKey(brokerPub)))
}

func TestLegacyReceiptStillVerifies(t *testing.T) {
	rec, brokerPub := legacyCoSigned(t)
	require.True(t, rec.VerifyBroker(hexKey(brokerPub)),
		"a receipt co-signed before the repair must still verify, else all history reads as forged")
}

// The honest limit of a legacy signature: it never covered the broker-set fields, so
// altering them does not break it. Callers must be able to LEARN that rather than
// mistake a legacy pass for proof of the billed counts.
func TestLegacyCoverageIsReportedNotAssumed(t *testing.T) {
	rec, brokerPub := legacyCoSigned(t)
	ok, covers := rec.VerifyBrokerCoverage(hexKey(brokerPub))
	require.True(t, ok, "legacy signature verifies")
	require.False(t, covers, "but it does NOT cover the broker-set billing fields")

	rec.BrokerCompletionTokens = 1
	ok, covers = rec.VerifyBrokerCoverage(hexKey(brokerPub))
	require.True(t, ok, "legacy signature is unaffected by a broker-field change")
	require.False(t, covers, "and the caller is told the counts are not covered")
}

func TestCurrentCoverageIsReported(t *testing.T) {
	rec, _, brokerPub := signedPair(t)
	ok, covers := rec.VerifyBrokerCoverage(hexKey(brokerPub))
	require.True(t, ok)
	require.True(t, covers, "a version-1 signature covers the billed counts")
}

func TestVersion1ReceiptRejectsLegacyBytes(t *testing.T) {
	rec, brokerPub := legacyCoSigned(t)
	rec.SigVersion = 1 // claims the new form but was signed over the old one
	require.False(t, rec.VerifyBroker(hexKey(brokerPub)),
		"a receipt declaring v1 must not verify against legacy bytes")
}

func TestUnknownSignatureVersionRejected(t *testing.T) {
	for _, v := range []int{2, 99, -1} {
		rec, _, brokerPub := signedPair(t)
		rec.SigVersion = v
		require.False(t, rec.VerifyBroker(hexKey(brokerPub)),
			"unknown signature version %d must be rejected", v)
	}
}

// The version tag is broker-set, so it must live inside the broker-signed bytes -
// otherwise an attacker could downgrade a v1 receipt to legacy and then freely edit
// the billed counts.
func TestSigVersionCannotBeDowngraded(t *testing.T) {
	rec, _, brokerPub := signedPair(t)
	rec.SigVersion = 0
	require.False(t, rec.VerifyBroker(hexKey(brokerPub)),
		"downgrading the version must invalidate the signature, not unlock the legacy rule")
}
