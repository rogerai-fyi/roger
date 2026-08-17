package dispatch

// evidence_test.go covers the two opposing claims settlement rests on once Roger Core is out
// of the data path.
//
// Contract: features/tower/edge_dispatch.feature.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towerobj"
)

func consumer(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub, priv
}

func stationReceipt(t *testing.T, attemptID string, response []byte, u Usage) Receipt {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	rec, err := SignReceipt(priv, "roger-public", Grant{AttemptID: attemptID, StationID: "st-1"}, []byte("req"), response, u, Usage{})
	require.NoError(t, err)
	return rec
}

func TestAConsumerAcknowledgementRoundTrips(t *testing.T) {
	pub, priv := consumer(t)
	fb := time.Unix(1_700_000_001, 0)
	done := time.Unix(1_700_000_009, 0)

	a, err := SignAck(priv, "roger-public", "att-1", []byte("the answer"), Usage{In: 12, Out: 34}, fb, done)
	require.NoError(t, err)

	got, err := ParseAck(a.Signed, pub, "roger-public", "att-1")
	require.NoError(t, err)
	require.Equal(t, "att-1", got.AttemptID)
	require.Equal(t, a.ResponseDigest, got.ResponseDigest)
	require.Equal(t, Usage{In: 12, Out: 34}, got.Usage)
	require.Equal(t, fb.Unix(), got.FirstByte.Unix())
	require.Equal(t, done.Unix(), got.Completed.Unix())
}

// An acknowledgement for a DIFFERENT attempt is a real signature over a real statement about
// other work. Without this check it would corroborate whatever it was filed against - which
// is the cheapest possible way to manufacture corroboration.
func TestAnAcknowledgementCannotBeFiledAgainstAnotherAttempt(t *testing.T) {
	pub, priv := consumer(t)
	a, err := SignAck(priv, "roger-public", "att-OTHER", []byte("x"), Usage{}, time.Now(), time.Now())
	require.NoError(t, err)

	_, err = ParseAck(a.Signed, pub, "roger-public", "att-1")
	require.ErrorContains(t, err, `for attempt "att-OTHER"`)
}

func TestAnAcknowledgementSignedByAnybodyElseIsRefused(t *testing.T) {
	_, priv := consumer(t)
	a, err := SignAck(priv, "roger-public", "att-1", []byte("x"), Usage{}, time.Now(), time.Now())
	require.NoError(t, err)

	impostor, _ := consumer(t)
	_, err = ParseAck(a.Signed, impostor, "roger-public", "att-1")
	require.ErrorContains(t, err, "not signed by the consumer")
}

func TestAnAcknowledgementNeedsSomethingToAcknowledge(t *testing.T) {
	_, priv := consumer(t)
	_, err := SignAck(priv, "roger-public", "", []byte("x"), Usage{}, time.Now(), time.Now())
	require.ErrorContains(t, err, "names the attempt")

	_, err = SignAck(priv, "roger-public", "att-1", nil, Usage{}, time.Now(), time.Now())
	require.ErrorContains(t, err, "there is none")

	_, err = SignAck(priv, "roger-public", "att-1", []byte("x"), Usage{Out: -1}, time.Now(), time.Now())
	require.ErrorContains(t, err, "negative usage")
}

func TestAnUnreadableAcknowledgementIsRefused(t *testing.T) {
	pub, _ := consumer(t)
	_, err := ParseAck([]byte("{not json"), pub, "roger-public", "att-1")
	require.Error(t, err)
}

// THE INCENTIVE TEST. The Station gains by reporting more than it spent; the consumer by
// reporting less than it received. Taking the minimum means neither profits by lying.
func TestSettlementTakesTheLowerOfTwoOpposingClaims(t *testing.T) {
	response := []byte("the answer")
	pub, priv := consumer(t)
	a, err := SignAck(priv, "roger-public", "att-1", response, Usage{In: 10, Out: 90},
		time.Now(), time.Now())
	require.NoError(t, err)
	parsed, err := ParseAck(a.Signed, pub, "roger-public", "att-1")
	require.NoError(t, err)

	// The Station claims MORE output than the consumer saw - in its own signed receipt. Output
	// is witnessed by both parties (both commit to the response digest), so it settles on the
	// lower figure: the Station must not profit from its own count.
	s, err := Reconcile(stationReceipt(t, "att-1", response, Usage{In: 10, Out: 100}), &parsed)
	require.NoError(t, err)
	require.True(t, s.Corroborated)
	require.Equal(t, int64(90), s.Billable.Out, "output settles on the lower of the two claims")

	// INPUT comes from the receipt, not the ack. The acknowledgement commits only to the
	// response digest - it does not attest the request - so input is the Station's count
	// (bounded by the grant ceiling and re-checked against the transcript at audit), whatever
	// the ack's input field says. Here the ack claims In 10 and the receipt In 25; billable In
	// is the receipt's 25, not min'd down by an input figure the consumer never truly attested.
	s, err = Reconcile(stationReceipt(t, "att-1", response, Usage{In: 25, Out: 90}), &parsed)
	require.NoError(t, err)
	require.Equal(t, int64(25), s.Billable.In, "input is the receipt's, not reconciled against the ack")
	require.False(t, s.UsageDisputed, "an input difference is not a dispute - the ack cannot attest input")
}

// THE PRODUCTION CLIENT SIGNS usage_in = 0 (its ack carries no request digest, so it has
// nothing to attest input with). Input must therefore NOT be reconciled against the ack, or
// every honest corroborated attempt would zero the operator's input pay and be falsely
// disputed. Output, which the ack does attest, still reconciles normally.
func TestAnAckThatCannotAttestInputDoesNotZeroOrDisputeInput(t *testing.T) {
	response := []byte("a real response body")
	pub, priv := consumer(t)
	// The shape internal/edgeclient signs: In 0, Out the true response length.
	a, err := SignAck(priv, "roger-public", "att-1", response, Usage{In: 0, Out: int64(len(response))},
		time.Now(), time.Now())
	require.NoError(t, err)
	parsed, err := ParseAck(a.Signed, pub, "roger-public", "att-1")
	require.NoError(t, err)

	s, err := Reconcile(stationReceipt(t, "att-1", response, Usage{In: 100, Out: int64(len(response))}), &parsed)
	require.NoError(t, err)
	require.True(t, s.Corroborated)
	require.Equal(t, int64(100), s.Billable.In, "input pay is not zeroed by an ack that says In 0")
	require.Equal(t, int64(len(response)), s.Billable.Out)
	require.False(t, s.UsageDisputed, "the honest corroborated path is not disputed")
}

// TWO SIGNED DIGESTS WITH A RELAY BETWEEN THEM. This is the whole mechanism for detecting a
// Tower that altered the answer, and it must REFUSE rather than settle at the lower figure -
// a disagreement about the bytes is not a rounding difference, it is attributable.
func TestAResponseTheTwoEndsDisagreeAboutIsRefusedRatherThanSettled(t *testing.T) {
	rec := stationReceipt(t, "att-1", []byte("what the Station returned"), Usage{In: 1, Out: 1})
	pub, priv := consumer(t)
	a, err := SignAck(priv, "roger-public", "att-1", []byte("what the consumer received"),
		Usage{In: 1, Out: 1}, time.Now(), time.Now())
	require.NoError(t, err)
	parsed, err := ParseAck(a.Signed, pub, "roger-public", "att-1")
	require.NoError(t, err)

	_, err = Reconcile(rec, &parsed)
	require.ErrorIs(t, err, ErrDigestMismatch)
}

// A consumer that never acknowledges is NOT a fault. Customers close laptops and third-party
// clients will never ack at all; an operator who lost money for that is an operator who
// leaves, and a network with no operators is not more secure, it is empty.
func TestAnAttemptWithNoAcknowledgementStillSettlesAndSaysSo(t *testing.T) {
	rec := stationReceipt(t, "att-1", []byte("the answer"), Usage{In: 10, Out: 100})

	s, err := Reconcile(rec, nil)
	require.NoError(t, err)
	require.False(t, s.Corroborated, "the rate is the signal, so this has to be recorded")
	require.Equal(t, Usage{In: 10, Out: 100}, s.Billable)
	require.Equal(t, "att-1", s.AttemptID)
}

func TestSettlementNeedsTheStationsReceipt(t *testing.T) {
	_, err := Reconcile(Receipt{}, nil)
	require.ErrorContains(t, err, "needs the Station's receipt")

	// A hand-built receipt claiming negative usage - impossible through SignReceipt, which
	// is exactly why Reconcile must not assume its inputs came through SignReceipt.
	_, err = Reconcile(Receipt{AttemptID: "att-1", ResponseDigest: "d", Usage: Usage{Out: -1}}, nil)
	require.ErrorContains(t, err, "negative usage")
}

// Reconcile is the last line before money moves, so it re-checks the pairing rather than
// trusting that whoever assembled the two objects got it right.
func TestReconcileRefusesAMismatchedPairing(t *testing.T) {
	rec := stationReceipt(t, "att-1", []byte("x"), Usage{In: 1, Out: 1})
	_, priv := consumer(t)
	a, err := SignAck(priv, "roger-public", "att-2", []byte("x"), Usage{}, time.Now(), time.Now())
	require.NoError(t, err)

	_, err = Reconcile(rec, &a)
	require.ErrorContains(t, err, "and the receipt for")
}

// signedWith builds an object with arbitrary field values, so the malformed-field branches
// can be reached at all. They are unreachable through the constructors by design - which is
// the point: these are the paths a HOSTILE or corrupt input takes, not ours.
func signedWith(t *testing.T, priv ed25519.PrivateKey, typ, field string, fields map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(fields)
	require.NoError(t, err)
	raw, err := towerobj.Sign(priv, "roger-public", typ, Version, body, field)
	require.NoError(t, err)
	return raw
}

func TestAnAcknowledgementWithUnreadableNumbersIsRefused(t *testing.T) {
	pub, priv := consumer(t)
	base := map[string]any{
		"network": "roger-public", "type": TypeAck, "version": towerobj.FormatInt(Version),
		"attempt_id": "att-1", "response_digest": "d",
		"usage_in": "1", "usage_out": "2", "first_byte": "3", "completed": "4",
	}
	for field, bad := range map[string]string{
		"usage_in": "many", "usage_out": "lots", "first_byte": "soon", "completed": "later",
	} {
		t.Run(field, func(t *testing.T) {
			fields := map[string]any{}
			for k, v := range base {
				fields[k] = v
			}
			fields[field] = bad
			_, err := ParseAck(signedWith(t, priv, TypeAck, "consumer_sig", fields),
				pub, "roger-public", "att-1")
			require.Error(t, err)
		})
	}

	// And a negative figure, which would otherwise let a consumer acknowledge its way to a
	// credit rather than a smaller bill.
	fields := map[string]any{}
	for k, v := range base {
		fields[k] = v
	}
	fields["usage_out"] = "-5"
	_, err := ParseAck(signedWith(t, priv, TypeAck, "consumer_sig", fields), pub, "roger-public", "att-1")
	require.ErrorContains(t, err, "negative usage")
}

// ParseReceipt is Core's only check on the Station's statement when it never saw the
// response. The signature and the two context fields are all it has.
func TestAReceiptIsVerifiedAgainstTheAttachmentRecordedKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	rec, err := SignReceipt(priv, "roger-public", Grant{AttemptID: "att-1", StationID: "st-1"},
		[]byte("req"), []byte("the answer"), Usage{In: 7, Out: 10}, Usage{})
	require.NoError(t, err)

	got, err := ParseReceipt(rec.Signed, pub, "roger-public", "att-1", "st-1")
	require.NoError(t, err)
	require.Equal(t, "att-1", got.AttemptID)
	require.Equal(t, rec.ResponseDigest, got.ResponseDigest)
	require.Equal(t, Usage{In: 7, Out: 10}, got.Usage,
		"the usage claim must survive the round trip - it is what settlement bills from")
	require.Equal(t, rec.Signed, got.Signed)
}

func TestAReceiptSignedByAnybodyElseIsRefused(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	rec, err := SignReceipt(priv, "roger-public", Grant{AttemptID: "att-1", StationID: "st-1"},
		[]byte("req"), []byte("x"), Usage{In: 1, Out: 1}, Usage{})
	require.NoError(t, err)

	impostor, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, err = ParseReceipt(rec.Signed, impostor, "roger-public", "att-1", "st-1")
	require.ErrorContains(t, err, "not signed by the recorded Station key")
}

// A perfectly signed receipt for a DIFFERENT attempt or from a different Station would
// otherwise settle whatever it was filed against.
func TestAReceiptForAnotherContextIsRefused(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pub := priv.Public().(ed25519.PublicKey)
	rec, err := SignReceipt(priv, "roger-public", Grant{AttemptID: "att-1", StationID: "st-1"},
		[]byte("req"), []byte("x"), Usage{In: 1, Out: 1}, Usage{})
	require.NoError(t, err)

	_, err = ParseReceipt(rec.Signed, pub, "roger-public", "att-OTHER", "st-1")
	require.ErrorContains(t, err, "not this one")
	_, err = ParseReceipt(rec.Signed, pub, "roger-public", "att-1", "st-OTHER")
	require.ErrorContains(t, err, "not this one")
}

// A receipt committing to nothing would corroborate any answer at all.
func TestAReceiptThatCommitsToNothingIsRefused(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pub := priv.Public().(ed25519.PublicKey)

	raw := signedWith(t, priv, TypeReceipt, "station_sig", map[string]any{
		"network": "roger-public", "type": TypeReceipt, "version": towerobj.FormatInt(Version),
		"attempt_id": "att-1", "station_id": "st-1", "response_digest": "",
	})
	_, err = ParseReceipt(raw, pub, "roger-public", "att-1", "st-1")
	require.ErrorContains(t, err, "commits to no response")
}

func TestAnUnreadableReceiptIsRefused(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pub := priv.Public().(ed25519.PublicKey)
	_, err = ParseReceipt([]byte("{not json"), pub, "roger-public", "att-1", "st-1")
	require.Error(t, err)
}

// Option C: a Station's TOKEN claim rides the receipt alongside the byte usage, survives the
// sign -> parse round trip, and Reconcile carries it into Settlement.BillableTokens for the
// per-token path while the byte Billable is unchanged.
func TestReceiptCarriesTokenClaimAndReconcileBillsIt(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pub := priv.Public().(ed25519.PublicKey)
	rec, err := SignReceipt(priv, "roger-public", Grant{AttemptID: "att-1", StationID: "st-1"},
		[]byte("req"), []byte("the answer"), Usage{In: 20, Out: 40}, Usage{In: 5, Out: 11})
	require.NoError(t, err)

	got, err := ParseReceipt(rec.Signed, pub, "roger-public", "att-1", "st-1")
	require.NoError(t, err)
	require.Equal(t, Usage{In: 20, Out: 40}, got.Usage, "byte usage survives")
	require.Equal(t, Usage{In: 5, Out: 11}, got.TokUsage, "token claim survives")

	s, err := Reconcile(got, nil)
	require.NoError(t, err)
	require.Equal(t, Usage{In: 20, Out: 40}, s.Billable, "byte billable unchanged")
	require.Equal(t, Usage{In: 5, Out: 11}, s.BillableTokens, "token billable is the node's claim")
}

// A byte-only receipt (no token claim, e.g. an old receipt) reconciles to zero token billable:
// the per-token path bills nothing until a token claim is actually signed.
func TestAByteOnlyReceiptHasNoTokenBillable(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pub := priv.Public().(ed25519.PublicKey)
	rec, err := SignReceipt(priv, "roger-public", Grant{AttemptID: "att-1", StationID: "st-1"},
		[]byte("req"), []byte("x"), Usage{In: 1, Out: 1}, Usage{})
	require.NoError(t, err)
	got, err := ParseReceipt(rec.Signed, pub, "roger-public", "att-1", "st-1")
	require.NoError(t, err)
	require.Zero(t, got.TokUsage.In)
	require.Zero(t, got.TokUsage.Out)
	s, err := Reconcile(got, nil)
	require.NoError(t, err)
	require.Equal(t, Usage{}, s.BillableTokens)
}

// A negative token claim is refused. ParseReceipt already rejects it on the wire; Reconcile
// guards it too, since a Receipt can be constructed directly.
func TestReconcileRejectsNegativeTokenClaim(t *testing.T) {
	_, err := Reconcile(Receipt{AttemptID: "att-1", ResponseDigest: "d", TokUsage: Usage{Out: -1}}, nil)
	require.Error(t, err)
}

// The token claim is the number a Station is paid on under Option C, so it must be as
// tamper-evident as the byte claim: flipping tok_out in a signed receipt has to fail
// verification, or a relay carrying the receipt could inflate what the operator is owed.
func TestTamperingWithTheTokenClaimBreaksTheReceiptSignature(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pub := priv.Public().(ed25519.PublicKey)
	rec, err := SignReceipt(priv, "roger-public", Grant{AttemptID: "att-1", StationID: "st-1"},
		[]byte("req"), []byte("the answer"), Usage{In: 20, Out: 40}, Usage{In: 5, Out: 777})
	require.NoError(t, err)

	tampered := []byte(strings.Replace(string(rec.Signed), "777", "778", 1))
	require.NotEqual(t, rec.Signed, tampered, "the token value must actually be in the signed body to tamper with")
	_, err = ParseReceipt(tampered, pub, "roger-public", "att-1", "st-1")
	require.Error(t, err, "an altered token claim must not verify")
}

// A corroborated ack reconciles the BYTE output (min of the two digest-committed claims) but
// must NOT touch the token billable - consumer token recount is phase 6, so BillableTokens
// stays the Station's raw signed token claim.
func TestACorroboratedAckDoesNotTouchTokenBillable(t *testing.T) {
	response := []byte("the answer")
	pub, priv := consumer(t)
	a, err := SignAck(priv, "roger-public", "att-1", response, Usage{In: 0, Out: int64(len(response))},
		time.Now(), time.Now())
	require.NoError(t, err)
	parsed, err := ParseAck(a.Signed, pub, "roger-public", "att-1")
	require.NoError(t, err)

	_, spriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	rec, err := SignReceipt(spriv, "roger-public", Grant{AttemptID: "att-1", StationID: "st-1"},
		[]byte("req"), response, Usage{In: 10, Out: int64(len(response))}, Usage{In: 3, Out: 9})
	require.NoError(t, err)

	s, err := Reconcile(rec, &parsed)
	require.NoError(t, err)
	require.True(t, s.Corroborated)
	require.Equal(t, int64(len(response)), s.Billable.Out, "byte output still reconciles against the ack")
	require.Equal(t, Usage{In: 3, Out: 9}, s.BillableTokens, "the ack does not touch token billable (phase 6)")
}

// The sign side rejects a negative token claim too, symmetric with the byte usage guard.
func TestSignReceiptRejectsNegativeTokenClaim(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, err = SignReceipt(priv, "roger-public", Grant{AttemptID: "att-1", StationID: "st-1"},
		[]byte("req"), []byte("x"), Usage{In: 1, Out: 1}, Usage{Out: -1})
	require.ErrorContains(t, err, "negative token usage")
}
