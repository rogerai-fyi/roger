package envelope

// envelope_test.go is the confidentiality property, stated as tests.
//
// The thing being proven throughout is negative and therefore easy to fake: "the Tower cannot
// read this". So the tests do not check that decryption works and stop - they check that the
// bytes a Tower actually holds contain none of the content, that an envelope for one attempt
// cannot be used as another, and that every tampering a relay is positioned to do fails.

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func stationKeys(t *testing.T) (pub, priv []byte) {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	return k.PublicKey().Bytes(), k.Bytes()
}

// The round trip, both legs, which is the baseline everything else is measured against.
func TestARequestAndItsAnswerSurviveTheRelay(t *testing.T) {
	stationPub, stationPriv := stationKeys(t)
	corePub, corePriv := stationKeys(t)
	const attempt = "att-1"
	request := []byte(`{"model":"m1","messages":[{"role":"user","content":"my private prompt"}]}`)
	answer := []byte(`{"choices":[{"message":{"content":"the completion"}}]}`)

	sealedReq, err := SealTo(stationPub, request, attempt)
	require.NoError(t, err)
	gotReq, err := OpenWith(stationPriv, sealedReq, attempt)
	require.NoError(t, err)
	require.Equal(t, request, gotReq)

	sealedResp, err := SealTo(corePub, answer, attempt)
	require.NoError(t, err)
	gotResp, err := OpenWith(corePriv, sealedResp, attempt)
	require.NoError(t, err)
	require.Equal(t, answer, gotResp)
}

// ANY INSTANCE CAN OPEN THE ANSWER. The response comes back to whichever broker the Tower
// reached, which is very often not the one that sent the request - so a per-exchange session
// key held in one process's memory would strand the answer. Sealing to Core's static key is
// what makes the reply openable by a broker that never saw the request go out.
func TestAnyInstanceHoldingCoresKeyCanOpenTheAnswer(t *testing.T) {
	corePub, corePriv := stationKeys(t)
	const attempt = "att-1"

	// Sealed by a Station that only ever saw the public half.
	sealed, err := SealTo(corePub, []byte(`{"answer":"hello"}`), attempt)
	require.NoError(t, err)

	// Opened by an instance with no memory of the exchange at all - only the key.
	got, err := OpenWith(corePriv, sealed, attempt)
	require.NoError(t, err)
	require.Equal(t, `{"answer":"hello"}`, string(got))
}

// WHAT THE TOWER HOLDS. The whole point: a packet capture of the relay leg must contain no
// prompt, no completion, and nothing that identifies either.
func TestWhatTheTowerCarriesContainsNoContent(t *testing.T) {
	pub, priv := stationKeys(t)
	const attempt = "att-1"
	const secret = "the patient's diagnosis is"
	request := []byte(`{"messages":[{"role":"user","content":"` + secret + `"}]}`)
	const answerText = "a completion nobody else should read"

	sealedReq, err := SealTo(pub, request, attempt)
	require.NoError(t, err)

	// Exactly the bytes a Tower relays.
	onTheWire, err := sealedReq.Marshal()
	require.NoError(t, err)
	require.NotContains(t, string(onTheWire), secret, "the prompt was readable on the relay leg")
	require.NotContains(t, string(onTheWire), "messages", "even the request's shape leaked")
	require.NotContains(t, string(onTheWire), "role")

	corePub, _ := stationKeys(t)
	sealedResp, err := SealTo(corePub, []byte(`{"content":"`+answerText+`"}`), attempt)
	require.NoError(t, err)
	back, err := sealedResp.Marshal()
	require.NoError(t, err)
	require.NotContains(t, string(back), answerText, "the completion was readable on the way home")
	_ = priv
}

// A TOWER HOLDING BOTH ENVELOPES CANNOT SWAP THEM. Without the attempt bound in, a valid
// envelope for attempt A relayed as attempt B would decrypt perfectly and the Station would
// serve the wrong request under the right authorization.
func TestAnEnvelopeIsBoundToItsAttempt(t *testing.T) {
	pub, priv := stationKeys(t)
	sealed, err := SealTo(pub, []byte(`{"a":1}`), "att-A")
	require.NoError(t, err)

	// The relay presents it as another attempt. Both the derived key and the AEAD's
	// additional data refuse it.
	_, err = OpenWith(priv, sealed, "att-B")
	require.Error(t, err)

	// And under its own attempt it opens.
	got, err := OpenWith(priv, sealed, "att-A")
	require.NoError(t, err)
	require.Equal(t, `{"a":1}`, string(got))
}

// A relay that alters ANY part of the envelope breaks it. AEAD, so this is not a property
// worth being clever about - but it is worth asserting, because it is what stops a Tower
// substituting content it cannot read.
func TestEveryAlterationBreaksTheEnvelope(t *testing.T) {
	pub, priv := stationKeys(t)
	const attempt = "att-1"
	sealed, err := SealTo(pub, []byte(`{"prompt":"hello"}`), attempt)
	require.NoError(t, err)

	for name, mangle := range map[string]func(s *Sealed){
		"a flipped ciphertext bit": func(s *Sealed) { s.Ciphertext[0] ^= 1 },
		"a truncated ciphertext":   func(s *Sealed) { s.Ciphertext = s.Ciphertext[:len(s.Ciphertext)-1] },
		"an appended byte":         func(s *Sealed) { s.Ciphertext = append(s.Ciphertext, 0) },
		"a different nonce":        func(s *Sealed) { s.Nonce[0] ^= 1 },
		"an empty ciphertext":      func(s *Sealed) { s.Ciphertext = nil },
	} {
		t.Run(name, func(t *testing.T) {
			bad := Sealed{
				EphemeralKey: append([]byte(nil), sealed.EphemeralKey...),
				Nonce:        append([]byte(nil), sealed.Nonce...),
				Ciphertext:   append([]byte(nil), sealed.Ciphertext...),
			}
			mangle(&bad)
			_, oerr := OpenWith(priv, bad, attempt)
			require.Error(t, oerr)
		})
	}
}

// A relay substituting its OWN ephemeral key gets an envelope the Station cannot open: it
// would need the ciphertext to have been sealed under the matching secret, which requires the
// Station's private key it does not have.
func TestARelayCannotSubstituteItsOwnExchange(t *testing.T) {
	pub, priv := stationKeys(t)
	const attempt = "att-1"
	sealed, err := SealTo(pub, []byte(`{"prompt":"hello"}`), attempt)
	require.NoError(t, err)

	relay, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	sealed.EphemeralKey = relay.PublicKey().Bytes()

	_, err = OpenWith(priv, sealed, attempt)
	require.Error(t, err)
}

// AN ENVELOPE OPENS ONLY WITH THE KEY IT WAS ADDRESSED TO. The request leg is sealed to the
// Station and the response leg to Core, so neither party can read the other's mail even
// though both envelopes cross the same relay.
func TestAnEnvelopeOpensOnlyWithItsOwnKey(t *testing.T) {
	stationPub, stationPriv := stationKeys(t)
	corePub, corePriv := stationKeys(t)
	const attempt = "att-1"

	toStation, err := SealTo(stationPub, []byte(`{"prompt":"secret"}`), attempt)
	require.NoError(t, err)
	toCore, err := SealTo(corePub, []byte(`{"answer":"secret"}`), attempt)
	require.NoError(t, err)

	_, err = OpenWith(corePriv, toStation, attempt)
	require.Error(t, err, "Core must not be able to open what was addressed to the Station")
	_, err = OpenWith(stationPriv, toCore, attempt)
	require.Error(t, err, "and the Station must not read the answer meant for Core")
}

// Two attempts to the same Station never share a key, even before the AEAD's additional data
// is considered - the attempt is in the HKDF info as well.
func TestTwoAttemptsNeverShareAKey(t *testing.T) {
	pub, priv := stationKeys(t)
	// An envelope sealed under one attempt must not open under the other.
	sealedA, err := SealTo(pub, []byte(`{"x":1}`), "att-A")
	require.NoError(t, err)
	_, err = OpenWith(priv, sealedA, "att-B")
	require.Error(t, err)

	// And two exchanges for the SAME attempt differ too, because the ephemeral key does.
	sealedAgain, err := SealTo(pub, []byte(`{"x":1}`), "att-A")
	require.NoError(t, err)
	require.NotEqual(t, sealedA.EphemeralKey, sealedAgain.EphemeralKey,
		"each exchange is its own")
}

// A key that is not a key is refused where the mistake is, not later as a decryption failure.
func TestAKeyThatIsNotAKeyIsRefusedAtOnce(t *testing.T) {
	_, err := SealTo([]byte("far too short"), []byte("x"), "att-1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "X25519")

	_, priv := stationKeys(t)
	_, err = OpenWith(priv, Sealed{EphemeralKey: []byte("nope"), Nonce: make([]byte, 12),
		Ciphertext: []byte("x")}, "att-1")
	require.Error(t, err)

	_, err = OpenWith([]byte("short"), Sealed{}, "att-1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "X25519")
}

// The wire shape, in one place so both ends and the relay agree without any of them defining
// it separately.
func TestTheWireShapeRoundTrips(t *testing.T) {
	pub, _ := stationKeys(t)
	sealed, err := SealTo(pub, []byte(`{"a":1}`), "att-1")
	require.NoError(t, err)

	raw, err := sealed.Marshal()
	require.NoError(t, err)
	got, err := Parse(raw)
	require.NoError(t, err)
	require.Equal(t, sealed.Nonce, got.Nonce)
	require.Equal(t, sealed.Ciphertext, got.Ciphertext)
	require.Equal(t, sealed.EphemeralKey, got.EphemeralKey)

	_, err = Parse(json.RawMessage(`{nope`))
	require.Error(t, err)
	_, err = Parse(json.RawMessage(`{}`))
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "carries nothing")
}

// A failure says the same thing however it failed. A wrong key, altered bytes and somebody
// else's attempt are all "this is not for me"; distinguishing them tells a relay which of its
// guesses got closest.
func TestEveryFailureReadsTheSame(t *testing.T) {
	pub, priv := stationKeys(t)
	sealed, err := SealTo(pub, []byte(`{"a":1}`), "att-1")
	require.NoError(t, err)

	altered := sealed
	altered.Ciphertext = append([]byte(nil), sealed.Ciphertext...)
	altered.Ciphertext[0] ^= 1
	_, tamperErr := OpenWith(priv, altered, "att-1")
	require.Error(t, tamperErr)

	_, wrongErr := OpenWith(priv, sealed, "att-other")
	require.Error(t, wrongErr)

	require.Equal(t, tamperErr.Error(), wrongErr.Error())
}

// A nonce of the wrong size is refused before the AEAD sees it, so the failure names the
// actual problem rather than surfacing as "could not open".
func TestANonceOfTheWrongSizeIsRefusedClearly(t *testing.T) {
	pub, priv := stationKeys(t)
	sealed, err := SealTo(pub, []byte(`{"a":1}`), "att-1")
	require.NoError(t, err)

	short := sealed
	short.Nonce = sealed.Nonce[:len(sealed.Nonce)-1]
	_, err = OpenWith(priv, short, "att-1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "nonce")

	long := sealed
	long.Nonce = append(append([]byte(nil), sealed.Nonce...), 0)
	_, err = OpenWith(priv, long, "att-1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "nonce")
}

// An empty payload still seals and opens. A request body is never empty in practice, but an
// envelope that silently mangled one would be a bug found at the worst possible moment.
func TestAnEmptyPayloadStillRoundTrips(t *testing.T) {
	pub, priv := stationKeys(t)
	sealed, err := SealTo(pub, nil, "att-1")
	require.NoError(t, err)
	require.NotEmpty(t, sealed.Ciphertext, "even nothing seals to the AEAD tag")

	got, err := OpenWith(priv, sealed, "att-1")
	require.NoError(t, err)
	require.Empty(t, got)
}

// A DEGENERATE KEY IS REFUSED. An all-zero X25519 point drives the exchange to a known shared
// secret - so a Station registering one, or a relay substituting one, would be choosing the
// key its own traffic is sealed under. Go's ECDH refuses it and so must we, rather than
// sealing to something an attacker picked.
func TestADegenerateKeyIsRefusedRatherThanUsed(t *testing.T) {
	zero := make([]byte, 32)

	_, err := SealTo(zero, []byte("x"), "att-1")
	require.Error(t, err, "sealing to a low-order point would seal to a known secret")

	// And on the receiving side, an envelope carrying one as its ephemeral key.
	_, priv := stationKeys(t)
	_, err = OpenWith(priv, Sealed{
		EphemeralKey: zero, Nonce: make([]byte, 12), Ciphertext: []byte("x"),
	}, "att-1")
	require.Error(t, err)
}

// Key minting, used by a Station for its secure-session key and by Core for the key answers
// come home to. A fresh key must actually work in both directions.
func TestAMintedKeyWorksAndItsPublicHalfIsDerivable(t *testing.T) {
	pub, priv, err := NewKey()
	require.NoError(t, err)
	require.Len(t, pub, 32)
	require.Len(t, priv, 32)

	derived, err := PublicKeyOf(priv)
	require.NoError(t, err)
	require.Equal(t, pub, derived, "the public half must be recoverable from the private one")

	sealed, err := SealTo(pub, []byte(`{"a":1}`), "att-1")
	require.NoError(t, err)
	got, err := OpenWith(priv, sealed, "att-1")
	require.NoError(t, err)
	require.Equal(t, `{"a":1}`, string(got))

	// Two mints are two different keys.
	other, _, err := NewKey()
	require.NoError(t, err)
	require.NotEqual(t, pub, other)

	_, err = PublicKeyOf([]byte("not a key"))
	require.Error(t, err)
}
