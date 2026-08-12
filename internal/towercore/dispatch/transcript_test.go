package dispatch

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestATranscriptRoundTripsAndMatchesItsReceiptDigests(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	req, resp := []byte("the prompt"), []byte("the completion")

	tr, err := SignTranscript(priv, "roger-public", "att-1", req, resp)
	require.NoError(t, err)

	// A receipt over the same bytes commits to the same digests, by construction.
	rec, err := SignReceipt(priv, "roger-public", Grant{AttemptID: "att-1", StationID: "st-1"},
		req, resp, Usage{In: 1, Out: 1})
	require.NoError(t, err)

	got, result, err := AuditTranscript(tr.Signed, pub, "roger-public", "att-1",
		rec.RequestDigest, rec.ResponseDigest)
	require.NoError(t, err)
	require.True(t, result.Matches, result.Reason)
	require.NoError(t, got.VerifyBytes(req, resp))
}

// A transcript signed by anybody but the Station is refused - it is what stops a Tower
// fabricating one to frame the Station.
func TestATranscriptFromAnybodyElseIsRefused(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	tr, err := SignTranscript(priv, "roger-public", "att-1", []byte("q"), []byte("a"))
	require.NoError(t, err)

	impostor, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, _, err = AuditTranscript(tr.Signed, impostor, "roger-public", "att-1", "x", "y")
	require.ErrorContains(t, err, "not signed by the recorded Station key")
}

// A Station that signed one digest in its receipt and a different one in its transcript has
// attributed the disagreement to itself: the audit does NOT match, and it does not error -
// the mismatch is a finding, not a malformed input.
func TestADigestThatDisagreesWithTheReceiptIsAMismatch(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	tr, err := SignTranscript(priv, "roger-public", "att-1", []byte("q"), []byte("a"))
	require.NoError(t, err)

	_, result, err := AuditTranscript(tr.Signed, pub, "roger-public", "att-1",
		tr.RequestDigest, "a-different-response-digest")
	require.NoError(t, err)
	require.False(t, result.Matches)
	require.Contains(t, result.Reason, "response digest")

	_, result, err = AuditTranscript(tr.Signed, pub, "roger-public", "att-1",
		"a-different-request-digest", tr.ResponseDigest)
	require.NoError(t, err)
	require.False(t, result.Matches)
	require.Contains(t, result.Reason, "request digest")
}

// The carried bytes must hash to the signed digests, or the content Core is reading is not
// the content that was attested.
func TestBytesThatDoNotHashToTheDigestAreCaught(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	tr, err := SignTranscript(priv, "roger-public", "att-1", []byte("q"), []byte("a"))
	require.NoError(t, err)

	require.NoError(t, tr.VerifyBytes([]byte("q"), []byte("a")))
	require.Error(t, tr.VerifyBytes([]byte("tampered"), []byte("a")))
	require.Error(t, tr.VerifyBytes([]byte("q"), []byte("tampered")))
}

func TestATranscriptForAnotherAttemptIsRefused(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	tr, err := SignTranscript(priv, "roger-public", "att-1", []byte("q"), []byte("a"))
	require.NoError(t, err)
	_, _, err = AuditTranscript(tr.Signed, pub, "roger-public", "att-OTHER", "x", "y")
	require.ErrorContains(t, err, "not this one")
}

func TestATranscriptNeedsAnAttempt(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, err = SignTranscript(priv, "roger-public", "", nil, nil)
	require.ErrorContains(t, err, "names its attempt")
}

func TestAnUnreadableTranscriptIsRefused(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, _, err = AuditTranscript([]byte("{not json"), pub, "roger-public", "att-1", "x", "y")
	require.Error(t, err)
}
