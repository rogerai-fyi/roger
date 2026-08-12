package dispatch

// evidence_test.go covers the two opposing claims settlement rests on once Roger Core is out
// of the data path.
//
// Contract: features/tower/edge_dispatch.feature.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
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
	rec, err := SignReceipt(priv, "roger-public", Grant{AttemptID: attemptID, StationID: "st-1"}, response, u)
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

	// The Station claims MORE output than the consumer saw - in its own signed receipt,
	// because that is now the only place a claim can live.
	s, err := Reconcile(stationReceipt(t, "att-1", response, Usage{In: 10, Out: 100}), &parsed)
	require.NoError(t, err)
	require.True(t, s.Corroborated)
	require.Equal(t, Usage{In: 10, Out: 90}, s.Billable,
		"the Station must not profit from its own count")

	// And the other direction: a Station understating input does not raise the bill.
	s, err = Reconcile(stationReceipt(t, "att-1", response, Usage{In: 4, Out: 90}), &parsed)
	require.NoError(t, err)
	require.Equal(t, Usage{In: 4, Out: 90}, s.Billable)
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
		[]byte("the answer"), Usage{In: 7, Out: 10})
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
		[]byte("x"), Usage{In: 1, Out: 1})
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
		[]byte("x"), Usage{In: 1, Out: 1})
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
