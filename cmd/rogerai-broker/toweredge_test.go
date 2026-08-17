package main

// toweredge_test.go covers Roger Core's whole involvement in a Tower-served request: it
// authorized one earlier, and here it takes the two accounts of what came back.
//
// Contract: features/tower/edge_dispatch.feature.
//
// The pieces underneath have their own suites, so what is tested here is the WIRING and the
// authority questions that only exist at this boundary: that an acknowledgement is verified
// against the key that signed the REQUEST rather than one it names itself, that a receipt is
// verified against the key recorded at ATTACHMENT rather than one the relay sent, and that a
// Tower cannot settle for a Station behind somebody else's origin.

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/store"
	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/attach"
	"rogerai.fm/roger/v5/internal/towercore/audit"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/envelope"
	"rogerai.fm/roger/v5/internal/towercore/fleet"
	"rogerai.fm/roger/v5/internal/towercore/link"
	"rogerai.fm/roger/v5/internal/towercore/reputation"
)

// consumerCall signs as the CONSUMER, which is what the ack route authenticates.
func consumerCall(t *testing.T, srv *httptest.Server, priv ed25519.PrivateKey,
	path string, in any) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(in)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(string(body)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocol.SignRequest(priv, http.MethodPost, path, body)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// issuedAttempt seeds the shared store with an attempt Core would have recorded at
// authorize time. Settlement is one-use against this record, so a test that skips it is a
// test of an attempt that cannot exist.
// issuedAttempt seeds an edge attempt bound to a fresh consumer key, and returns that key so
// an ack test can sign as the authorized consumer. A settle-only test ignores the return.
// issuedAttempt records an edge attempt with a REAL Core-signed grant carrying a generous byte
// ceiling (edgeMaxBytes), so the settle path can read the ceiling (as it does in production,
// where openEdgeAttempt always stores a valid grant) and small test usages settle well under it
// without being clamped. Returns the consumer private key so a caller can sign an ack.
func issuedAttempt(t *testing.T, b *broker, attemptID, towerID, stationID string) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	apub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	g, err := b.tower.dispatch.MintEdge(dispatch.EdgeTarget{
		TowerID: towerID, StationID: stationID, StationEpoch: 1, Model: "m", Modality: "text",
		RelayName: stationID + ".relay.example", MaxIn: 8 << 20, MaxOut: 8 << 20,
		AssertionKey: apub, ConsumerKey: pub,
	})
	require.NoError(t, err)
	require.NoError(t, b.tower.dispatch.Store().Put(dispatch.Record{
		AttemptID: attemptID, JobID: "job-" + attemptID, TowerID: towerID,
		StationID: stationID, Model: "m", Modality: "text", Nonce: "n-" + attemptID,
		Deadline: time.Now().Add(time.Hour), Grant: g.Signed, ConsumerKey: pub,
		State: dispatch.StateIssued,
	}))
	// FUNDED by default: the accrual gate skips traffic whose consumer resolves to no
	// account (Core's canaries), and this fixture stands in for a real, signed-in consumer.
	bindEdgeConsumer(t, b, pub)
	return priv
}

func signedAck(t *testing.T, priv ed25519.PrivateKey, attemptID string, response []byte,
	u dispatch.Usage) string {
	t.Helper()
	a, err := dispatch.SignAck(priv, link.PublicNetwork, attemptID, response, u,
		time.Now(), time.Now())
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(a.Signed)
}

func signedReceipt(t *testing.T, priv ed25519.PrivateKey, attemptID, stationID string,
	response []byte, u dispatch.Usage) string {
	t.Helper()
	rec, err := dispatch.SignReceipt(priv, link.PublicNetwork,
		dispatch.Grant{AttemptID: attemptID, StationID: stationID}, []byte("req-"+attemptID), response, u, dispatch.Usage{})
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(rec.Signed)
}

// THE HAPPY PATH, both halves: the consumer's account arrives, the Station's receipt arrives
// by its Tower, and settlement takes the lower of the two.
func TestAnEdgeAttemptSettlesOnTwoOpposingAccounts(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")

	consumerPriv := issuedAttempt(t, b, "att-1", tw.id, "st-1")
	response := []byte(`{"choices":[{"text":"hello"}]}`)

	code, out := consumerCall(t, srv, consumerPriv, "/tower/edge/ack", map[string]any{
		"attempt_id": "att-1",
		"ack":        signedAck(t, consumerPriv, "att-1", response, dispatch.Usage{In: 10, Out: 90}),
	})
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, true, out["recorded"])

	// The Station CLAIMS more output than the consumer saw - in its own signed receipt,
	// which is now the only place a claim can live.
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-1", response,
			dispatch.Usage{In: 10, Out: 100}),
	})
	require.NoError(t, err)
	var settled map[string]any
	code, _ = tw.call(t, srv, "/tower/edge/settle", body, &settled)
	require.Equal(t, http.StatusOK, code, settled)
	require.Equal(t, true, settled["corroborated"])
	require.Equal(t, float64(90), settled["billable_out"],
		"the Station must be held to the consumer's figure")
}

// A closed laptop is not a fault. The attempt settles on the receipt alone and is MARKED, so
// the rate can be looked at without any single attempt being punished.
func TestAnEdgeAttemptWithNoAcknowledgementSettlesUncorroborated(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")
	issuedAttempt(t, b, "att-lonely", tw.id, "st-1")

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-lonely",
		"receipt": signedReceipt(t, stationPriv, "att-lonely", "st-1", []byte("answer"),
			dispatch.Usage{In: 1, Out: 7}),
	})
	require.NoError(t, err)
	var settled map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &settled)
	require.Equal(t, http.StatusOK, code, settled)
	require.Equal(t, false, settled["corroborated"])
	require.Equal(t, float64(7), settled["billable_out"], "the operator is still paid")
}

// A DISAGREEMENT DOES NOT VOID SETTLEMENT. A review found the old handling dangerous: a
// digest mismatch failed the attempt and blamed the Tower, so a lying consumer could deny
// the Station its pay and frame a third party by signing a false digest. Now the attempt
// SETTLES on the Station's receipt (uncorroborated), is marked disputed as a rate signal,
// and is force-audited - none of which can be triggered against an honest Tower by one lie.
func TestADisagreementSettlesOnTheReceiptAndIsFlagged(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))
	consumerPriv := issuedAttempt(t, b, "att-1", tw.id, "st-1")

	code, _ := consumerCall(t, srv, consumerPriv, "/tower/edge/ack", map[string]any{
		"attempt_id": "att-1",
		"ack": signedAck(t, consumerPriv, "att-1", []byte("what the consumer received"),
			dispatch.Usage{In: 1, Out: 1}),
	})
	require.Equal(t, http.StatusOK, code)

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-1",
			[]byte("what the Station returned"), dispatch.Usage{In: 1, Out: 3}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ = tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, true, out["disputed"])
	require.Equal(t, false, out["corroborated"])
	require.Equal(t, float64(3), out["billable_out"], "the Station is paid for what it signed")

	// The dispute is recorded as a SIGNAL (feeds the rate), and one is not enough to suspend.
	tally, err := b.tower.outcomes.Tally(tw.id, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, tally.Disputed)
	got, _ := b.tower.registry.Get(tw.id)
	require.Equal(t, admit.StateActive, got.State, "one dispute does not take a Tower off")

	// And it was force-audited regardless of the sample.
	pending, err := b.tower.auditWanted.Pending(tw.id, time.Now())
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "att-1", pending[0].AttemptID)
}

// UNSIGNED EVIDENCE SETTLES NOTHING. An acknowledgement that anybody could have filed is not
// a claim from a party with an opposing interest; it is a claim from nobody.
func TestAnUnsignedAcknowledgementIsRefused(t *testing.T) {
	_, srv := towerTestBroker(t)
	resp, err := http.Post(srv.URL+"/tower/edge/ack", "application/json",
		strings.NewReader(`{"attempt_id":"att-1","ack":"x"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// THE KEY THAT SIGNED THE REQUEST IS THE KEY THE OBJECT IS VERIFIED AGAINST. A
// self-describing key would let anybody file an acknowledgement as anybody, which is the
// cheapest possible way to manufacture corroboration - or to poison somebody else's.
func TestAnAcknowledgementSignedByADifferentKeyThanTheRequestIsRefused(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	// Bind the attempt to the caller so it reaches the ack-object check (not the earlier
	// consumer-binding refusal), then present an ack signed by a DIFFERENT key.
	requestPriv := bindAttemptToConsumer(t, b, "att-1", tw.id, "st-1")
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	code, _ := consumerCall(t, srv, requestPriv, "/tower/edge/ack", map[string]any{
		"attempt_id": "att-1",
		"ack":        signedAck(t, otherPriv, "att-1", []byte("x"), dispatch.Usage{}),
	})
	require.Equal(t, http.StatusBadRequest, code)
}

func TestAMalformedAcknowledgementIsRefused(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	priv := bindAttemptToConsumer(t, b, "att-1", tw.id, "st-1")

	for name, in := range map[string]any{
		"no attempt": map[string]any{"ack": "eyJ9"},
		"no ack":     map[string]any{"attempt_id": "att-1"},
		"not base64": map[string]any{"attempt_id": "att-1", "ack": "!!!"},
		"not an ack": map[string]any{"attempt_id": "att-1", "ack": base64.StdEncoding.EncodeToString([]byte("{}"))},
	} {
		t.Run(name, func(t *testing.T) {
			code, _ := consumerCall(t, srv, priv, "/tower/edge/ack", in)
			require.Equal(t, http.StatusBadRequest, code)
		})
	}
}

// THE RECEIPT IS CHECKED AGAINST THE ATTACHMENT RECORD, never against anything the relay
// sent. Otherwise "signed by the Station" would mean "signed by whoever is relaying".
func TestAReceiptSignedByAnybodyButTheStationIsRefused(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	issuedAttempt(t, b, "att-1", tw.id, "st-1")

	_, impostor, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceipt(t, impostor, "att-1", "st-1", []byte("answer"), dispatch.Usage{In: 1, Out: 1}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, msg := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusForbidden, code)
	require.Contains(t, msg, "not signed by the recorded Station key")
}

// A Tower settling for a Station behind somebody else's origin.
func TestATowerCannotSettleForAStationThatIsNotItsOwn(t *testing.T) {
	b, srv := towerTestBroker(t)
	mine := enrolledTower(t, b, "owner-1")
	theirs := enrolledTower(t, b, "owner-2")
	stationPriv := attachStation(t, b, "st-theirs", theirs.id, "owner-2")
	issuedAttempt(t, b, "att-1", theirs.id, "st-theirs")

	body, err := json.Marshal(map[string]any{
		"tower_id": mine.id, "station_id": "st-theirs", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-theirs", []byte("answer"), dispatch.Usage{In: 1, Out: 1}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, msg := mine.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusForbidden, code)
	require.Contains(t, msg, "not attached to this Tower")
}

func TestSettlingRequiresTheTowersOwnSignedRequest(t *testing.T) {
	_, srv := towerTestBroker(t)
	resp, err := http.Post(srv.URL+"/tower/edge/settle", "application/json",
		strings.NewReader(`{"tower_id":"tw-nobody","station_id":"st-1","attempt_id":"a","receipt":"x"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestAMalformedSettlementIsRefused(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")
	issuedAttempt(t, b, "att-1", tw.id, "st-1")
	good := signedReceipt(t, stationPriv, "att-1", "st-1", []byte("answer"), dispatch.Usage{In: 1, Out: 1})

	for name, tc := range map[string]struct {
		in   map[string]any
		want int
	}{
		"no attempt":    {map[string]any{"tower_id": tw.id, "station_id": "st-1", "receipt": good}, http.StatusBadRequest},
		"no station":    {map[string]any{"tower_id": tw.id, "attempt_id": "att-1", "receipt": good}, http.StatusBadRequest},
		"no receipt":    {map[string]any{"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1"}, http.StatusBadRequest},
		"not base64":    {map[string]any{"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1", "receipt": "!!!"}, http.StatusBadRequest},
		"unknown statn": {map[string]any{"tower_id": tw.id, "station_id": "st-nope", "attempt_id": "att-1", "receipt": good}, http.StatusNotFound},
		// A settlement for an attempt that does not exist, and one that names the wrong Station
		// for a real attempt, are the SAME uniform 404 - neither confirms which of the two it was.
		"other attempt": {map[string]any{"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-OTHER", "receipt": good}, http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(tc.in)
			require.NoError(t, err)
			var out map[string]any
			code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
			require.Equal(t, tc.want, code)
		})
	}
}

func TestEdgeRoutesRefuseTheWrongMethod(t *testing.T) {
	_, srv := towerTestBroker(t)
	for _, path := range []string{"/tower/edge/ack", "/tower/edge/settle"} {
		resp, err := http.Get(srv.URL + path)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, path)
	}
}

func TestSettlementReadsWhateverAcknowledgementIsRecorded(t *testing.T) {
	b, _ := towerTestBroker(t)
	// No acknowledgement recorded: uncorroborated, and the receipt's own claim stands.
	s, disputed, err := b.settleEdgeAttempt("att-none",
		dispatch.Receipt{AttemptID: "att-none", ResponseDigest: "d",
			Usage: dispatch.Usage{In: 1, Out: 2}})
	require.NoError(t, err)
	require.False(t, disputed)
	require.False(t, s.Corroborated)
	require.Equal(t, dispatch.Usage{In: 1, Out: 2}, s.Billable)
}

// --- renewal -----------------------------------------------------------------
//
// Contract: features/tower/public_enrollment.feature.
//
// These routes did not exist. The renewal logic behind them was complete and tested and
// reachable from nothing, so every Tower's certificate and lease - both 24 hours by default -
// would have lapsed a day after enrollment with re-enrollment through quarantine as the only
// recovery. What is tested here is that the routes are MOUNTED and authenticated, which is
// exactly what was missing.

func TestRenewalRoutesExistAndAreMounted(t *testing.T) {
	_, srv := towerTestBroker(t)
	for _, path := range []string{"/tower/renew/challenge", "/tower/renew"} {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(`{}`))
		require.NoError(t, err)
		resp.Body.Close()
		require.NotEqual(t, http.StatusNotFound, resp.StatusCode,
			"%s must be mounted: without it every Tower expires in a day", path)
	}
}

// Renewal is signed by the TOWER, not the operator: it spends no token, creates no Tower and
// changes no identity. Requiring a human daily would build exactly the habit a phishing mail
// needs.
func TestRenewalRequiresTheTowersOwnSignedRequest(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")

	// Unsigned.
	resp, err := http.Post(srv.URL+"/tower/renew/challenge", "application/json",
		strings.NewReader(`{"tower_id":"`+tw.id+`"}`))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// Signed, but by a different Tower's key.
	other := enrolledTower(t, b, "owner-2")
	body, err := json.Marshal(map[string]any{"tower_id": tw.id})
	require.NoError(t, err)
	var out map[string]any
	code, _ := other.call(t, srv, "/tower/renew/challenge", body, &out)
	require.Equal(t, http.StatusForbidden, code,
		"anyone who learned a Tower ID must not be able to ask for its renewal nonce")
}

func TestATowerCanGetItsRenewalChallenge(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	body, err := json.Marshal(map[string]any{"tower_id": tw.id})
	require.NoError(t, err)

	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/renew/challenge", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	require.NotEmpty(t, out["nonce"])
	require.NotEmpty(t, out["signing_input"])
}

func TestRenewalRefusesWhatItCannotRead(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")

	for name, tc := range map[string]struct {
		path string
		in   map[string]any
	}{
		"challenge with no tower": {"/tower/renew/challenge", map[string]any{}},
		"renew with no tower":     {"/tower/renew", map[string]any{}},
		"renew with bad base64":   {"/tower/renew", map[string]any{"tower_id": tw.id, "csr": "!!!"}},
	} {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(tc.in)
			require.NoError(t, err)
			var out map[string]any
			code, _ := tw.call(t, srv, tc.path, body, &out)
			require.GreaterOrEqual(t, code, 400)
			require.Less(t, code, 500)
		})
	}
}

// A renewal that does not prove possession of the key on record is refused. Without this,
// anyone who learned a Tower ID could have a certificate for it issued to themselves.
func TestARenewalWithoutAValidProofIsRefused(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")

	body, err := json.Marshal(map[string]any{
		"tower_id":     tw.id,
		"nonce":        "not-a-real-nonce",
		"identity_key": base64.StdEncoding.EncodeToString([]byte("not a key")),
		"signature":    base64.StdEncoding.EncodeToString([]byte("not a signature")),
		"csr":          base64.StdEncoding.EncodeToString([]byte("not a csr")),
	})
	require.NoError(t, err)
	var out map[string]any
	code, msg := tw.call(t, srv, "/tower/renew", body, &out)
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, msg, "not valid")
}

func TestRenewalRoutesRefuseTheWrongMethod(t *testing.T) {
	_, srv := towerTestBroker(t)
	for _, path := range []string{"/tower/renew/challenge", "/tower/renew"} {
		resp, err := http.Get(srv.URL + path)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, path)
	}
}

// THE HAPPY PATH, through the real handler: a Tower proves possession of the key already on
// record and gets a genuinely new certificate with a later expiry. This is the test that
// would have failed for the whole life of the bug - not because renewal computed the wrong
// answer, but because there was nothing to call.
func TestATowerActuallyRenewsAndGetsALaterExpiry(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")

	body, err := json.Marshal(map[string]any{"tower_id": tw.id})
	require.NoError(t, err)
	var ch map[string]any
	code, _ := tw.call(t, srv, "/tower/renew/challenge", body, &ch)
	require.Equal(t, http.StatusOK, code, ch)

	signingInput, err := base64.StdEncoding.DecodeString(ch["signing_input"].(string))
	require.NoError(t, err)

	// The CHANNEL key is a different key from the identity key, as enrollment requires.
	channelPub, channelPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_ = channelPub
	csr, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "roger-tower"}}, channelPriv)
	require.NoError(t, err)

	body, err = json.Marshal(map[string]any{
		"tower_id":     tw.id,
		"nonce":        ch["nonce"],
		"identity_key": base64.StdEncoding.EncodeToString(tw.priv.Public().(ed25519.PublicKey)),
		"signature":    base64.StdEncoding.EncodeToString(ed25519.Sign(tw.priv, signingInput)),
		"csr":          base64.StdEncoding.EncodeToString(csr),
	})
	require.NoError(t, err)

	var out map[string]any
	code, msg := tw.call(t, srv, "/tower/renew", body, &out)
	require.Equal(t, http.StatusOK, code, msg)

	// A real, parseable certificate for this Tower.
	certDER, err := base64.StdEncoding.DecodeString(out["certificate"].(string))
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(certDER)
	require.NoError(t, err)
	require.True(t, leaf.NotAfter.After(time.Now()), "a renewal must push the expiry out")

	// And the lease moved with it - the thing that otherwise lapses at 24 hours.
	require.Greater(t, out["lease_expires"].(float64), float64(time.Now().Unix()))
}

// A one-use nonce. Replaying a renewal must not mint a second certificate.
func TestARenewalNonceCannotBeReplayed(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")

	body, err := json.Marshal(map[string]any{"tower_id": tw.id})
	require.NoError(t, err)
	var ch map[string]any
	code, _ := tw.call(t, srv, "/tower/renew/challenge", body, &ch)
	require.Equal(t, http.StatusOK, code)
	signingInput, err := base64.StdEncoding.DecodeString(ch["signing_input"].(string))
	require.NoError(t, err)

	_, channelPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	csr, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "roger-tower"}}, channelPriv)
	require.NoError(t, err)
	renewal, err := json.Marshal(map[string]any{
		"tower_id":     tw.id,
		"nonce":        ch["nonce"],
		"identity_key": base64.StdEncoding.EncodeToString(tw.priv.Public().(ed25519.PublicKey)),
		"signature":    base64.StdEncoding.EncodeToString(ed25519.Sign(tw.priv, signingInput)),
		"csr":          base64.StdEncoding.EncodeToString(csr),
	})
	require.NoError(t, err)

	var out map[string]any
	code, _ = tw.call(t, srv, "/tower/renew", renewal, &out)
	require.Equal(t, http.StatusOK, code)

	// The same request again.
	code, _ = tw.call(t, srv, "/tower/renew", renewal, &out)
	require.Equal(t, http.StatusBadRequest, code, "a renewal nonce is one-use")
}

// --- authorize: the on-ramp ---------------------------------------------------

// routableEdge publishes a fleet row WITH an endpoint, the way publishRoutable does when the
// Tower's live session advertised one.
func routableEdge(t *testing.T, b *broker, towerID, stationID, model, endpoint string) {
	t.Helper()
	// Out of quarantine: enrollment admits, an administrator promotes, and the projection is
	// only a hint - authority is re-checked at authorize, so an unpromoted Tower gets no work
	// however routable its rows look. Idempotent, because a test may re-publish rows.
	if tw, ok := b.tower.registry.Get(towerID); ok && tw.State != admit.StateActive {
		require.NoError(t, b.tower.registry.Transition(towerID, admit.StateActive))
	}
	require.NoError(t, b.tower.routable.Replace(towerID, []fleet.Station{{
		TowerID: towerID, StationID: stationID, OfferID: "self-" + stationID,
		Model: model, Modality: "text", Capacity: 4,
		Expires: time.Now().Add(time.Hour), Endpoint: endpoint,
	}}))
}

// testEnvKeyHex is a fresh consumer X25519 public key, hex - authorize requires one since
// the sealed path became the only path.
func testEnvKeyHex(t *testing.T) string {
	t.Helper()
	pub, _, err := envelope.NewKey()
	require.NoError(t, err)
	return hex.EncodeToString(pub)
}

// THE ON-RAMP, end to end at the API: a signed consumer asks for a model, Core answers with
// a grant, a relay name, and the Tower's advertised endpoint - and has recorded the attempt
// before the grant left the building.
func TestAConsumerIsAuthorizedOntoTheEdgePath(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	routableEdge(t, b, tw.id, "st-1", "m", "203.0.113.7:8443")

	consumerPriv := signedInConsumer(t, b)
	code, out := consumerCall(t, srv, consumerPriv, "/tower/edge/authorize",
		map[string]any{"model": "m", "consumer_env_key": testEnvKeyHex(t)})
	require.Equal(t, http.StatusOK, code, out)

	require.NotEmpty(t, out["attempt_id"])
	require.Equal(t, "203.0.113.7:8443", out["endpoint"])
	require.Equal(t, "st-1."+relayDomain(), out["relay_name"],
		"the relay name's leftmost label is how the Tower routes by SNI")

	// The grant is real: it parses under the Station's rules, against Core's actual key.
	raw, err := base64.StdEncoding.DecodeString(out["grant"].(string))
	require.NoError(t, err)
	g, err := dispatch.ParseEdgeGrant(raw, b.tower.dispatchPub, link.PublicNetwork, "st-1",
		[]byte("any request within bounds"), time.Now())
	require.NoError(t, err)
	require.Equal(t, out["attempt_id"], g.AttemptID)
	require.Equal(t, tw.id, g.TowerID)

	// And the attempt was RECORDED before the grant was handed out: settlement can find it.
	rec, found, err := b.tower.dispatch.Store().Get(g.AttemptID)
	require.NoError(t, err)
	require.True(t, found, "an authorization nobody recorded is work whose outcome cannot be established")
	require.Equal(t, dispatch.StateIssued, rec.State)
	require.True(t, rec.Deadline.After(g.Deadline),
		"the record outlives the grant: the grant bounds execution, the record bounds evidence")
}

func TestAuthorizeRefusesWithoutASignedRequest(t *testing.T) {
	_, srv := towerTestBroker(t)
	resp, err := http.Post(srv.URL+"/tower/edge/authorize", "application/json",
		strings.NewReader(`{"model":"m"}`))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// One refusal for every kind of "not here": unknown model, no endpoint, ineligible Tower.
// Enumerating which Towers exist is nobody's business.
func TestAuthorizeRefusalsAreUniform(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	consumerPriv := signedInConsumer(t, b)

	// No fleet row at all.
	code, _ := consumerCall(t, srv, consumerPriv, "/tower/edge/authorize", map[string]any{"model": "m", "consumer_env_key": testEnvKeyHex(t)})
	require.Equal(t, http.StatusServiceUnavailable, code)

	// Routable, but the Tower advertises NO data plane: healthy on the relayed path,
	// invisible on the edge.
	routableEdge(t, b, tw.id, "st-1", "m", "")
	code, _ = consumerCall(t, srv, consumerPriv, "/tower/edge/authorize", map[string]any{"model": "m", "consumer_env_key": testEnvKeyHex(t)})
	require.Equal(t, http.StatusServiceUnavailable, code)

	// An endpoint, but the Tower is quarantined: the projection is a hint, authority decides.
	routableEdge(t, b, tw.id, "st-1", "m", "203.0.113.7:8443")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateDraining))
	code, _ = consumerCall(t, srv, consumerPriv, "/tower/edge/authorize", map[string]any{"model": "m", "consumer_env_key": testEnvKeyHex(t)})
	require.Equal(t, http.StatusServiceUnavailable, code)

	// And a malformed ask is its own, earlier refusal.
	code, _ = consumerCall(t, srv, consumerPriv, "/tower/edge/authorize", map[string]any{})
	require.Equal(t, http.StatusBadRequest, code)
}

// The caller may narrow the bounds, never widen them.
func TestAuthorizeCapsWhatACallerMayAskFor(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	routableEdge(t, b, tw.id, "st-1", "m", "203.0.113.7:8443")
	consumerPriv := signedInConsumer(t, b)

	code, out := consumerCall(t, srv, consumerPriv, "/tower/edge/authorize",
		map[string]any{"model": "m", "max_in": 512, "max_out": edgeMaxBytes * 100, "consumer_env_key": testEnvKeyHex(t)})
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, float64(512), out["max_in"], "asking for less is honoured")
	require.Equal(t, float64(edgeMaxBytes), out["max_out"], "asking for more is capped")
}

// AUTHORIZE THEN SETTLE, through the real store: the full control-plane lifecycle, and the
// replay that must lose.
func TestAnAuthorizedAttemptSettlesExactlyOnce(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")
	routableEdge(t, b, tw.id, "st-1", "m", "203.0.113.7:8443")

	consumerPriv := signedInConsumer(t, b)
	code, out := consumerCall(t, srv, consumerPriv, "/tower/edge/authorize",
		map[string]any{"model": "m", "consumer_env_key": testEnvKeyHex(t)})
	require.Equal(t, http.StatusOK, code, out)
	attemptID := out["attempt_id"].(string)

	settle, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": attemptID,
		"receipt": signedReceipt(t, stationPriv, attemptID, "st-1", []byte("answer"),
			dispatch.Usage{In: 3, Out: 5}),
	})
	require.NoError(t, err)

	var settled map[string]any
	code, _ = tw.call(t, srv, "/tower/edge/settle", settle, &settled)
	require.Equal(t, http.StatusOK, code, settled)
	require.Equal(t, float64(5), settled["billable_out"])

	// THE REPLAY. A stale answer served twice, a duplicated forward, a Tower fishing for a
	// second payment - all the same request, and all must lose the swap.
	code, msg := tw.call(t, srv, "/tower/edge/settle", settle, &settled)
	require.Equal(t, http.StatusConflict, code)
	require.Contains(t, msg, "already been settled")

	// And another Tower cannot settle an attempt granted through this one.
	other := enrolledTower(t, b, "owner-2")
	stolen, err := json.Marshal(map[string]any{
		"tower_id": other.id, "station_id": "st-1", "attempt_id": attemptID,
		"receipt": signedReceipt(t, stationPriv, attemptID, "st-1", []byte("answer"),
			dispatch.Usage{In: 3, Out: 5}),
	})
	require.NoError(t, err)
	code, _ = other.call(t, srv, "/tower/edge/settle", stolen, &settled)
	require.Equal(t, http.StatusForbidden, code)
}

// The relay domain is Core's, and configurable, because a domain is deployment topology.
func TestTheRelayDomainIsConfigurable(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_RELAY_DOMAIN", "relay.example.test")
	require.Equal(t, "relay.example.test", relayDomain())
	t.Setenv("ROGERAI_TOWER_RELAY_DOMAIN", "")
	require.Equal(t, "relay.rogerai.fm", relayDomain())
}

func TestAuthorizeRefusesAnUnreadableBody(t *testing.T) {
	b, srv := towerTestBroker(t)
	_ = b
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	// Signed but not JSON - identityOf passes, decode fails.
	body := []byte("{not json")
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/tower/edge/authorize",
		strings.NewReader(string(body)))
	require.NoError(t, err)
	pub, ts, sig := protocol.SignRequest(priv, http.MethodPost, "/tower/edge/authorize", body)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestEdgeRoutesPreflightForBrowsers(t *testing.T) {
	_, srv := towerTestBroker(t)
	for _, path := range []string{"/tower/edge/authorize", "/tower/edge/ack"} {
		req, err := http.NewRequest(http.MethodOptions, srv.URL+path, nil)
		require.NoError(t, err)
		req.Header.Set("Origin", "https://app.example")
		req.Header.Set("Access-Control-Request-Method", "POST")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Less(t, resp.StatusCode, 300, path)
	}
}

// An acknowledgement whose REQUEST key is unreadable garbage is refused before anything is
// parsed against it.
func TestAnAckWithNoUsableKeyIsRefused(t *testing.T) {
	_, srv := towerTestBroker(t)
	body := []byte(`{"attempt_id":"att-1","ack":"eyJ9"}`)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/tower/edge/ack",
		strings.NewReader(string(body)))
	require.NoError(t, err)
	// Headers that pass identityOf's shape checks cannot carry a malformed pubkey - the
	// signature would not verify - so this exercises the unauthenticated refusal instead.
	req.Header.Set(protocol.HeaderPubkey, "zz-not-hex")
	req.Header.Set(protocol.HeaderTS, "1")
	req.Header.Set(protocol.HeaderSig, "zz")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// A settle whose body is not JSON, and one from a Tower that exists but names no attempt.
func TestSettleRefusesWhatItCannotRead(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", []byte("{not json"), &out)
	require.Equal(t, http.StatusBadRequest, code)
}

// The settlement window: the grant bounds execution, the record bounds evidence, and a
// receipt arriving after even the record's deadline is closed by name.
func TestASettlementAfterTheWindowIsRefusedByName(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")
	require.NoError(t, b.tower.dispatch.Store().Put(dispatch.Record{
		AttemptID: "att-late", JobID: "job-late", TowerID: tw.id, StationID: "st-1",
		Model: "m", Modality: "text", Nonce: "n-late",
		Deadline: time.Now().Add(-time.Minute), State: dispatch.StateIssued,
	}))

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-late",
		"receipt": signedReceipt(t, stationPriv, "att-late", "st-1", []byte("answer"),
			dispatch.Usage{In: 1, Out: 1}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, msg := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusForbidden, code)
	require.Contains(t, msg, "settlement window has closed")
}

// A digest disagreement CLOSES the attempt as failed: evidence that broken does not get a
// second try with a different story, so the replay after it is "already settled".
// A DISPUTED ATTEMPT STILL SETTLES EXACTLY ONCE. The disagreement no longer voids anything,
// so the attempt settles (disputed) - and a replay after that loses the one-use swap, exactly
// as an undisputed one would.
func TestADisputedAttemptStillSettlesExactlyOnce(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")
	consumerPriv := issuedAttempt(t, b, "att-1", tw.id, "st-1")

	code, _ := consumerCall(t, srv, consumerPriv, "/tower/edge/ack", map[string]any{
		"attempt_id": "att-1",
		"ack": signedAck(t, consumerPriv, "att-1", []byte("what the consumer received"),
			dispatch.Usage{In: 1, Out: 1}),
	})
	require.Equal(t, http.StatusOK, code)

	settle, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-1",
			[]byte("what the Station returned"), dispatch.Usage{In: 1, Out: 1}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ = tw.call(t, srv, "/tower/edge/settle", settle, &out)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, true, out["disputed"])

	// The replay loses the swap.
	code, msg := tw.call(t, srv, "/tower/edge/settle", settle, &out)
	require.Equal(t, http.StatusConflict, code)
	require.Contains(t, msg, "already been settled")
}

// Every edge and renewal route answers a broker with no Tower subsystem the same way:
// unavailable, not a panic and not a 404 that would read as "wrong URL".
func TestEdgeRoutesOnABrokerWithoutTheSubsystem(t *testing.T) {
	b := testBrokerWithDB(store.NewMem())
	mux := http.NewServeMux()
	b.registerTowerRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	for _, path := range []string{
		"/tower/edge/authorize", "/tower/edge/ack", "/tower/edge/settle",
		"/tower/renew/challenge", "/tower/renew",
	} {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader("{}"))
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, path)
	}
}

// --- reputation --------------------------------------------------------------
//
// Contract: features/tower/edge_dispatch.feature.

// Settling records an outcome, and the rate the Tower is judged on reflects it. A
// corroborated attempt and an uncorroborated one land in different columns.
func TestSettlementFeedsTheReputationLedger(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")

	// One uncorroborated (no ack), one corroborated (matching ack).
	issuedAttempt(t, b, "att-uncorr", tw.id, "st-1")
	settleUncorr, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-uncorr",
		"receipt": signedReceipt(t, stationPriv, "att-uncorr", "st-1", []byte("a"),
			dispatch.Usage{In: 1, Out: 1}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", settleUncorr, &out)
	require.Equal(t, http.StatusOK, code)

	consumerPriv := issuedAttempt(t, b, "att-corr", tw.id, "st-1")
	answer := []byte("the answer")
	code, _ = consumerCall(t, srv, consumerPriv, "/tower/edge/ack", map[string]any{
		"attempt_id": "att-corr",
		"ack":        signedAck(t, consumerPriv, "att-corr", answer, dispatch.Usage{In: 1, Out: 1}),
	})
	require.Equal(t, http.StatusOK, code)
	settleCorr, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-corr",
		"receipt": signedReceipt(t, stationPriv, "att-corr", "st-1", answer, dispatch.Usage{In: 1, Out: 1}),
	})
	require.NoError(t, err)
	code, _ = tw.call(t, srv, "/tower/edge/settle", settleCorr, &out)
	require.Equal(t, http.StatusOK, code)

	tally, err := b.tower.outcomes.Tally(tw.id, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, tally.Uncorroborated)
	require.Equal(t, 1, tally.Corroborated)
}

// A digest disagreement records a DISPUTED outcome - the rate exists to surface exactly this
// across a Tower's attempts.
func TestADisagreementIsRecordedAgainstTheTower(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")
	consumerPriv := issuedAttempt(t, b, "att-1", tw.id, "st-1")

	code, _ := consumerCall(t, srv, consumerPriv, "/tower/edge/ack", map[string]any{
		"attempt_id": "att-1",
		"ack":        signedAck(t, consumerPriv, "att-1", []byte("consumer saw this"), dispatch.Usage{In: 1, Out: 1}),
	})
	require.Equal(t, http.StatusOK, code)

	settle, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-1",
			[]byte("station returned this"), dispatch.Usage{In: 1, Out: 1}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ = tw.call(t, srv, "/tower/edge/settle", settle, &out)
	require.Equal(t, http.StatusOK, code, "a dispute settles on the receipt, it does not fail")
	require.Equal(t, true, out["disputed"])

	tally, err := b.tower.outcomes.Tally(tw.id, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, tally.Disputed)
}

// bindAttemptToConsumer seeds an attempt bound to a specific consumer key it returns - used
// where a test needs to reach the ack-OBJECT checks past the consumer-binding gate.
func bindAttemptToConsumer(t *testing.T, b *broker, attemptID, towerID, stationID string) ed25519.PrivateKey {
	t.Helper()
	return issuedAttempt(t, b, attemptID, towerID, stationID)
}

// The rate is a FINDING, not a reversal: an evaluation that flags a Tower does not undo any
// settlement it already made.
func TestFlaggingNeverReversesASettlement(t *testing.T) {
	b, _ := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))

	// Enough uncorroborated attempts, against no fleet baseline, to flag.
	for i := 0; i < 30; i++ {
		b.recordOutcome(tw.id, "att-"+string(rune('a'+i%26))+string(rune('0'+i/26)),
			reputation.Uncorroborated)
	}
	verdict := b.evaluateTower(tw.id)
	require.Equal(t, reputation.Investigate, verdict)

	// Investigate does NOT quarantine: the Tower is still active, still takes work.
	got, ok := b.tower.registry.Get(tw.id)
	require.True(t, ok)
	require.Equal(t, admit.StateActive, got.State,
		"a rate flags for a human; it does not take the Tower off by itself")
}

// A single audit mismatch quarantines - and withdraws the fleet at once.
func TestAnAuditMismatchQuarantinesTheTower(t *testing.T) {
	b, _ := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))

	b.recordOutcome(tw.id, "att-bad", reputation.AuditMismatch)
	require.Equal(t, reputation.Quarantine, b.evaluateTower(tw.id))

	// An ACTIVE Tower is SUSPENDED - the legal "take it off now" move - not put back in
	// quarantine, which is the post-enrollment pen it cannot legally return to.
	got, ok := b.tower.registry.Get(tw.id)
	require.True(t, ok)
	require.Equal(t, admit.StateSuspended, got.State)
	require.Equal(t, admit.EligibilityNone, admit.EligibleFor(got.State),
		"a suspended Tower takes no work")
}

// The sweep ages out reputation evidence past the window, so the ledger does not grow forever.
func TestTheSweepReapsAgedOutcomes(t *testing.T) {
	b, _ := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")

	require.NoError(t, b.tower.outcomes.Record(reputation.Event{
		TowerID: tw.id, AttemptID: "old", Outcome: reputation.Corroborated,
		At: time.Now().Add(-48 * time.Hour),
	}))
	require.NoError(t, b.tower.outcomes.Record(reputation.Event{
		TowerID: tw.id, AttemptID: "new", Outcome: reputation.Corroborated, At: time.Now(),
	}))
	b.towerInviteSweepOnce(time.Now())

	tally, err := b.tower.outcomes.Tally(tw.id, time.Now().Add(-1000*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, tally.Total, "the aged-out outcome is gone")
}

// The reputation helpers are safe on a broker with no Tower subsystem: they no-op rather
// than panic, because a reputation write is downstream of the money and must never be a gate.
func TestReputationHelpersAreSafeWithoutTheSubsystem(t *testing.T) {
	b := testBrokerWithDB(store.NewMem())
	require.NotPanics(t, func() { b.recordOutcome("tw", "att", reputation.Uncorroborated) })
	require.Equal(t, reputation.Clean, b.evaluateTower("tw"))
}

// A Tower with too little evidence is left alone, and stays active - the guard that keeps a
// single closed laptop from being a finding.
func TestATowerWithLittleEvidenceIsLeftActive(t *testing.T) {
	b, _ := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))

	for i := 0; i < 3; i++ {
		b.recordOutcome(tw.id, "att-"+string(rune('a'+i)), reputation.Uncorroborated)
	}
	require.Equal(t, reputation.Clean, b.evaluateTower(tw.id))
	got, _ := b.tower.registry.Get(tw.id)
	require.Equal(t, admit.StateActive, got.State)
}

// Repeated canary failures suspend an active Tower - the "serving nothing at all" attack,
// caught by evidence rather than by reading the traffic.
func TestRepeatedCanaryFailuresSuspend(t *testing.T) {
	b, _ := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))

	for i := 0; i < 8; i++ {
		b.recordOutcome(tw.id, "canary-"+string(rune('a'+i)), reputation.CanaryFail)
	}
	require.Equal(t, reputation.Quarantine, b.evaluateTower(tw.id))
	got, _ := b.tower.registry.Get(tw.id)
	require.Equal(t, admit.StateSuspended, got.State)
}

// A routable row whose Station is not actually attached is skipped: the projection is a hint,
// the attachment is authority, and a hint without backing yields the uniform refusal.
func TestAuthorizeSkipsARowWithNoAttachment(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))
	// A routable row for a Station that was never attached.
	require.NoError(t, b.tower.routable.Replace(tw.id, []fleet.Station{{
		TowerID: tw.id, StationID: "st-ghost", OfferID: "self-st-ghost", Model: "m",
		Modality: "text", Expires: time.Now().Add(time.Hour), Endpoint: "203.0.113.7:8443",
	}}))
	consumerPriv := signedInConsumer(t, b)
	code, _ := consumerCall(t, srv, consumerPriv, "/tower/edge/authorize", map[string]any{"model": "m", "consumer_env_key": testEnvKeyHex(t)})
	require.Equal(t, http.StatusServiceUnavailable, code)
}

// A caller asking for zero or negative bounds gets the ceiling, not a grant that authorizes
// nothing (or everything).
func TestAuthorizeDefaultsAbsentBounds(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	routableEdge(t, b, tw.id, "st-1", "m", "203.0.113.7:8443")
	consumerPriv := signedInConsumer(t, b)

	code, out := consumerCall(t, srv, consumerPriv, "/tower/edge/authorize",
		map[string]any{"model": "m", "max_in": 0, "max_out": -5, "consumer_env_key": testEnvKeyHex(t)})
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, float64(edgeMaxBytes), out["max_in"])
	require.Equal(t, float64(edgeMaxBytes), out["max_out"])
}

// A settle for an attempt this Tower never had is "no such attempt", not a panic or a leak of
// whether it belongs to somebody else.
func TestSettleForAnUnknownAttempt(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-never",
		"receipt": signedReceipt(t, stationPriv, "att-never", "st-1", []byte("a"),
			dispatch.Usage{In: 1, Out: 1}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, msg := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusNotFound, code)
	require.Contains(t, msg, "no such attempt")
}

// --- sampled transcript audit ------------------------------------------------
//
// Contract: features/tower/edge_dispatch.feature.

// signedTranscript stands in for what a Station's /transcripts/get returns.
func signedTranscript(t *testing.T, priv ed25519.PrivateKey, attemptID string, req, resp []byte) (obj, reqB64, respB64 string) {
	t.Helper()
	tr, err := dispatch.SignTranscript(priv, link.PublicNetwork, attemptID, req, resp)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(tr.Signed),
		base64.StdEncoding.EncodeToString(req), base64.StdEncoding.EncodeToString(resp)
}

// wantAudit seeds the wanted list the way settlement would, with the receipt's digests and the
// usage the Station claimed - here the HONEST length of the bytes, so a matching transcript
// passes both the digest and the usage-length check.
func wantAudit(t *testing.T, b *broker, tw, station, attempt string, req, resp []byte) {
	t.Helper()
	require.NoError(t, b.tower.auditWanted.Want(audit.Wanted{
		TowerID: tw, AttemptID: attempt, StationID: station,
		RequestDigest: digestLike(req), ResponseDigest: digestLike(resp),
		UsageIn: int64(len(req)), UsageOut: int64(len(resp)),
		Deadline: time.Now().Add(time.Hour),
	}))
}

// digestLike mirrors dispatch.digestOf: base64url-raw of the SHA-256, NOT hex. A wanted
// entry seeded with the wrong encoding would never match a real receipt.
func digestLike(b []byte) string {
	h := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// A matching transcript resolves the audit and leaves the Tower clean - the content was
// provably what both ends signed, and Core can screen it.
func TestAMatchingTranscriptPassesTheAudit(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))

	req, resp := []byte("the prompt"), []byte("the completion")
	wantAudit(t, b, tw.id, "st-1", "att-1", req, resp)
	obj, reqB64, respB64 := signedTranscript(t, stationPriv, "att-1", req, resp)

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "attempt_id": "att-1", "available": true,
		"transcript": obj, "request": reqB64, "response": respB64,
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/audit/transcript", body, &out)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, true, out["matched"])

	got, _ := b.tower.registry.Get(tw.id)
	require.Equal(t, admit.StateActive, got.State, "a passing audit does not touch the Tower")

	// Resolved: a second submission finds nothing wanted.
	code, _ = tw.call(t, srv, "/tower/audit/transcript", body, &out)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, true, out["resolved"])
}

// A transcript whose digests do not match the receipt is attributed to the Station and
// suspends the Tower - the audit found content that is not what was signed for.
func TestAMismatchedTranscriptFailsAndSuspends(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))

	// Core wanted a transcript for these digests; the Station signs a DIFFERENT response.
	wantAudit(t, b, tw.id, "st-1", "att-1", []byte("the prompt"), []byte("the real answer"))
	obj, reqB64, respB64 := signedTranscript(t, stationPriv, "att-1",
		[]byte("the prompt"), []byte("a substituted answer"))

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "attempt_id": "att-1", "available": true,
		"transcript": obj, "request": reqB64, "response": respB64,
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/audit/transcript", body, &out)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, false, out["matched"])

	got, _ := b.tower.registry.Get(tw.id)
	require.Equal(t, admit.StateSuspended, got.State)
}

// A Station that cannot produce a sampled transcript is the spec's quarantine trigger.
func TestAStationThatCannotProduceIsSuspended(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))
	// A SAMPLED id: only the deterministic sample carries the retention promise whose breach
	// suspends; an off-sample (adaptive/forced) miss is soft by design.
	require.True(t, auditSampled("att-s0"))
	wantAudit(t, b, tw.id, "st-1", "att-s0", []byte("q"), []byte("a"))

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "attempt_id": "att-s0", "available": false,
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/audit/transcript", body, &out)
	require.Equal(t, http.StatusOK, code)

	got, _ := b.tower.registry.Get(tw.id)
	require.Equal(t, admit.StateSuspended, got.State)
}

// A transcript a Tower forged (wrong signer) is refused - it cannot resolve an audit with
// something the Station never signed.
func TestAForgedTranscriptIsRefused(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	wantAudit(t, b, tw.id, "st-1", "att-1", []byte("q"), []byte("a"))

	_, impostor, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	obj, reqB64, respB64 := signedTranscript(t, impostor, "att-1", []byte("q"), []byte("a"))
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "attempt_id": "att-1", "available": true,
		"transcript": obj, "request": reqB64, "response": respB64,
	})
	require.NoError(t, err)
	var out map[string]any
	code, msg := tw.call(t, srv, "/tower/audit/transcript", body, &out)
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, msg, "not signed by the recorded Station key")
}

// The wanted list is the Tower's own, signed - a stranger cannot read what Core is auditing.
func TestTheAuditListNeedsTheTowersOwnRequest(t *testing.T) {
	_, srv := towerTestBroker(t)
	resp, err := http.Post(srv.URL+"/tower/audit/wanted", "application/json",
		strings.NewReader(`{"tower_id":"tw-x"}`))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestTheAuditListReturnsPendingWork(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	wantAudit(t, b, tw.id, "st-1", "att-1", []byte("q"), []byte("a"))

	body, err := json.Marshal(map[string]any{"tower_id": tw.id})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/audit/wanted", body, &out)
	require.Equal(t, http.StatusOK, code)
	wanted, _ := out["wanted"].([]any)
	require.Len(t, wanted, 1)
	first := wanted[0].(map[string]any)
	require.Equal(t, "att-1", first["attempt_id"])
	require.Equal(t, "st-1", first["station_id"])
	// The digests are NOT handed out - that would tell a Tower what a passing transcript needs.
	_, hasDigest := first["response_digest"]
	require.False(t, hasDigest)
}

// An overdue transcript that never arrived is swept into a finding.
func TestTheSweepTurnsOverdueAuditsIntoFindings(t *testing.T) {
	b, _ := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))
	require.True(t, auditSampled("att-s8"), "the sweep hardens only SAMPLED misses")
	require.NoError(t, b.tower.auditWanted.Want(audit.Wanted{
		TowerID: tw.id, AttemptID: "att-s8", StationID: "st-1",
		RequestDigest: "rq", ResponseDigest: "rs", Deadline: time.Now().Add(-time.Minute),
	}))

	b.towerInviteSweepOnce(time.Now())
	got, _ := b.tower.registry.Get(tw.id)
	require.Equal(t, admit.StateSuspended, got.State,
		"a Station that never produced a sampled transcript is suspended")
}

// Selection samples: over enough attempts, some are wanted and some are not, deterministically.
func TestSelectionSamplesAFraction(t *testing.T) {
	b, _ := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	wanted := 0
	for i := 0; i < 200; i++ {
		id := "att-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		b.selectForAudit(tw.id, "st-1", id, "rq", "rs", 0, 0, 0, 0)
	}
	p, err := b.tower.auditWanted.Pending(tw.id, time.Now())
	require.NoError(t, err)
	wanted = len(p)
	require.Greater(t, wanted, 5, "some attempts are sampled")
	require.Less(t, wanted, 100, "not all attempts are sampled")
}

func TestAuditRoutesRefuseWithoutTheSubsystem(t *testing.T) {
	b := testBrokerWithDB(store.NewMem())
	mux := http.NewServeMux()
	b.registerTowerRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	for _, path := range []string{"/tower/audit/wanted", "/tower/audit/transcript"} {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader("{}"))
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, path)
	}
}

// A transcript for an attempt Core never wanted (or already resolved) is a no-op "resolved",
// not an error - it stops a Tower re-opening a closed audit.
func TestATranscriptForAnUnwantedAttemptIsResolved(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "attempt_id": "att-unwanted", "available": false,
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/audit/transcript", body, &out)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, true, out["resolved"])
}

// A malformed audit submission is refused by name.
func TestAMalformedAuditSubmissionIsRefused(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	wantAudit(t, b, tw.id, "st-1", "att-1", []byte("q"), []byte("a"))

	for name, in := range map[string]any{
		"no attempt": map[string]any{"tower_id": tw.id},
		"bad base64": map[string]any{"tower_id": tw.id, "attempt_id": "att-1", "available": true, "transcript": "!!!"},
	} {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(in)
			require.NoError(t, err)
			var out map[string]any
			code, _ := tw.call(t, srv, "/tower/audit/transcript", body, &out)
			require.Equal(t, http.StatusBadRequest, code)
		})
	}
	// Not JSON at all.
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/audit/transcript", []byte("{nope"), &out)
	require.Equal(t, http.StatusBadRequest, code)
	// And the wanted list with a malformed body.
	code, _ = tw.call(t, srv, "/tower/audit/wanted", []byte("{nope"), &out)
	require.Equal(t, http.StatusBadRequest, code)
}

// Bytes that do not hash to the signed digests are a mismatch even when the digests match the
// receipt - the content handed over is not the content that was attested.
func TestTranscriptBytesMustHashToTheSignedDigests(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))

	req, resp := []byte("q"), []byte("a")
	wantAudit(t, b, tw.id, "st-1", "att-1", req, resp)
	obj, _, _ := signedTranscript(t, stationPriv, "att-1", req, resp)

	// The object is honest, but the carried bytes are tampered.
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "attempt_id": "att-1", "available": true,
		"transcript": obj,
		"request":    base64.StdEncoding.EncodeToString([]byte("tampered")),
		"response":   base64.StdEncoding.EncodeToString(resp),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/audit/transcript", body, &out)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, false, out["matched"])

	got, _ := b.tower.registry.Get(tw.id)
	require.Equal(t, admit.StateSuspended, got.State)
}

// The audit helpers are safe on a broker with no Tower subsystem.
func TestAuditHelpersAreSafeWithoutTheSubsystem(t *testing.T) {
	b := testBrokerWithDB(store.NewMem())
	require.NotPanics(t, func() { b.selectForAudit("tw", "st", "att", "rq", "rs", 0, 0, 0, 0) })
	require.NotPanics(t, func() { b.sweepAuditOverdue(time.Now()) })
}

// A transcript whose wanted Station's key cannot be read is refused rather than checked
// against a key we could not decode.
func TestATranscriptForAGoneStationIsRefused(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")
	// Wanted names a Station that is not attached.
	require.NoError(t, b.tower.auditWanted.Want(audit.Wanted{
		TowerID: tw.id, AttemptID: "att-1", StationID: "st-gone",
		RequestDigest: digestLike([]byte("q")), ResponseDigest: digestLike([]byte("a")),
		Deadline: time.Now().Add(time.Hour),
	}))
	obj, reqB64, respB64 := signedTranscript(t, stationPriv, "att-1", []byte("q"), []byte("a"))
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "attempt_id": "att-1", "available": true,
		"transcript": obj, "request": reqB64, "response": respB64,
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/audit/transcript", body, &out)
	require.Equal(t, http.StatusServiceUnavailable, code)
}

// evaluateTower on a Tower that is already off logs the failed move rather than panicking:
// the evidence still stands, and a Tower cannot be suspended out of a terminal state.
func TestEvaluatingAnAlreadyRevokedTowerIsHarmless(t *testing.T) {
	b, _ := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateRevoked))

	b.recordOutcome(tw.id, "att-bad", reputation.AuditMismatch)
	require.Equal(t, reputation.Quarantine, b.evaluateTower(tw.id),
		"the verdict stands even when the Tower cannot be moved")
	got, _ := b.tower.registry.Get(tw.id)
	require.Equal(t, admit.StateRevoked, got.State, "a revoked Tower stays revoked")
}

// A settlement records its outcome even when evaluation finds nothing to act on - the ledger
// is written on every attempt, which is what makes the rate meaningful.
func TestEverySettlementIsRecordedNotJustTheBadOnes(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))

	for i := 0; i < 3; i++ {
		id := "att-" + string(rune('a'+i))
		issuedAttempt(t, b, id, tw.id, "st-1")
		body, err := json.Marshal(map[string]any{
			"tower_id": tw.id, "station_id": "st-1", "attempt_id": id,
			"receipt": signedReceipt(t, stationPriv, id, "st-1", []byte("a"), dispatch.Usage{In: 1, Out: 1}),
		})
		require.NoError(t, err)
		var out map[string]any
		code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
		require.Equal(t, http.StatusOK, code)
	}
	tally, err := b.tower.outcomes.Tally(tw.id, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 3, tally.Total)
	got, _ := b.tower.registry.Get(tw.id)
	require.Equal(t, admit.StateActive, got.State, "ordinary settlement does not touch the Tower")
}

// --- fault injection: the error branches that guard every store call ----------

type failingOutcomes struct{}

func (failingOutcomes) Record(reputation.Event) error { return assertErr2 }
func (failingOutcomes) Tally(string, time.Time) (reputation.Tally, error) {
	return reputation.Tally{}, assertErr2
}
func (failingOutcomes) FleetTally(time.Time) (reputation.Tally, error) {
	return reputation.Tally{}, assertErr2
}
func (failingOutcomes) Reap(time.Time) (int64, error) { return 0, assertErr2 }

type failingAudit struct{}

func (failingAudit) Want(audit.Wanted) error { return assertErr2 }
func (failingAudit) Pending(string, time.Time) ([]audit.Wanted, error) {
	return nil, assertErr2
}
func (failingAudit) Resolve(string) error                      { return assertErr2 }
func (failingAudit) Overdue(time.Time) ([]audit.Wanted, error) { return nil, assertErr2 }

var assertErr2 = errors.New("store is down")

// A store that will not answer must never panic and never gate the money: every helper logs
// and carries on, because a reputation or audit write is downstream of a committed settlement.
func TestStoreFailuresAreLoggedNotFatal(t *testing.T) {
	b, _ := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))
	b.tower.outcomes = failingOutcomes{}
	b.tower.auditWanted = failingAudit{}

	require.NotPanics(t, func() { b.recordOutcome(tw.id, "att", reputation.Uncorroborated) })
	// evaluateTower cannot read the tally, so it declines to act rather than acting on nothing.
	require.Equal(t, reputation.Clean, b.evaluateTower(tw.id))
	require.NotPanics(t, func() { b.selectForAudit(tw.id, "st-1", "att", "rq", "rs", 0, 0, 0, 0) })
	require.NotPanics(t, func() { b.sweepAuditOverdue(time.Now()) })
}

// The wanted-list endpoint answers a store failure with unavailable, not a panic.
func TestTheWantedEndpointHandlesAStoreFailure(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	b.tower.auditWanted = failingAudit{}
	body, err := json.Marshal(map[string]any{"tower_id": tw.id})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/audit/wanted", body, &out)
	require.Equal(t, http.StatusServiceUnavailable, code)
}

// --- edge certificate issuance -----------------------------------------------

// attachStationOwned attaches a Station under a given owner pubkey.
func attachStationOwned(t *testing.T, b *broker, stationID, towerID, owner string) {
	t.Helper()
	_, apub, err := ed25519.GenerateKey(rand.Reader)
	_ = apub
	require.NoError(t, err)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sessionRaw := make([]byte, 32)
	copy(sessionRaw, stationID)
	assertion := hexOf(pub)
	sess := hexOf(ed25519.PublicKey(sessionRaw))
	authObj, secret, err := attach.NewInvite(attach.Authorization{
		ID: "auth-" + stationID, Network: link.PublicNetwork, StationID: stationID, Owner: owner,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: towerID},
		AssertionKey: assertion, SessionKey: sess,
	}, time.Hour, time.Now())
	require.NoError(t, err)
	require.NoError(t, b.tower.stationStore.PutAuthorization(authObj))
	_, err = b.tower.stations.Admit(attach.Proof{
		AuthID: "auth-" + stationID, Secret: secret, Network: link.PublicNetwork,
		StationID: stationID, Owner: owner,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: towerID},
		AssertionKey: assertion, SessionKey: sess,
	})
	require.NoError(t, err)
	_, err = b.tower.stations.Promote(stationID)
	require.NoError(t, err)
}

func ecdsaKey(t *testing.T) (*ecdsa.PrivateKey, error) {
	t.Helper()
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

func csrFor(t *testing.T, key *ecdsa.PrivateKey, name string) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}}, key)
	require.NoError(t, err)
	return der
}

func x509parse(der []byte) (*x509.Certificate, error) { return x509.ParseCertificate(der) }

// settleStore claims fine but fails to Settle, to hit the "claimed but not committed" branch.
type settleFailStore struct{ dispatch.Store }

func (s settleFailStore) Settle(string, time.Time) (dispatch.Record, error) {
	return dispatch.Record{}, assertErr2
}

// A store that claims an attempt but cannot commit the settlement answers 503 rather than
// pretending the attempt settled - the claim already succeeded, so this is a fault, not a race.
func TestASettlementThatCannotCommitIsReported(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")
	// Seed a real issued attempt in the underlying mem store, then wrap it so Settle fails.
	base := b.tower.dispatch.Store()
	require.NoError(t, base.Put(dispatch.Record{
		AttemptID: "att-1", JobID: "j", TowerID: tw.id, StationID: "st-1", Model: "m",
		Modality: "text", Nonce: "n", Deadline: time.Now().Add(time.Hour), State: dispatch.StateIssued,
	}))
	b.tower.dispatch = dispatch.NewWithStore(dispatch.Config{
		Network: link.PublicNetwork, Lifetime: time.Minute,
	}, settleFailStore{base})

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-1", []byte("a"), dispatch.Usage{In: 1, Out: 1}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusServiceUnavailable, code)
}

func TestRenewalRefusesMalformedFields(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")

	body, err := json.Marshal(map[string]any{"tower_id": tw.id})
	require.NoError(t, err)
	var ch map[string]any
	code, _ := tw.call(t, srv, "/tower/renew/challenge", body, &ch)
	require.Equal(t, http.StatusOK, code, ch)

	body, err = json.Marshal(map[string]any{
		"tower_id": tw.id, "nonce": ch["nonce"],
		"identity_key": "!!!not base64", "signature": "AAAA", "csr": "AAAA",
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ = tw.call(t, srv, "/tower/renew", body, &out)
	require.Equal(t, http.StatusBadRequest, code)
}

// --- ack binding (security review) -------------------------------------------

// The ack is bound to the AUTHORIZED consumer: a different account cannot acknowledge
// somebody else's attempt, even though it holds a valid account and knows the attempt id.
func TestAThirdPartyCannotAcknowledgeSomebodyElsesAttempt(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	_ = issuedAttempt(t, b, "att-1", tw.id, "st-1") // bound to a consumer key we discard

	// A different, signed-in account tries to ack it.
	_, intruder, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	code, _ := consumerCall(t, srv, intruder, "/tower/edge/ack", map[string]any{
		"attempt_id": "att-1",
		"ack":        signedAck(t, intruder, "att-1", []byte("x"), dispatch.Usage{In: 1, Out: 1}),
	})
	require.Equal(t, http.StatusNotFound, code,
		"an account acknowledging work that is not theirs learns nothing, not even that it exists")
}

// An ack for an attempt that was never authorized is refused - this also stops an attacker
// spraying acks at random ids to grow the store.
func TestAnAckForAnUnknownAttemptIsRefused(t *testing.T) {
	_, srv := towerTestBroker(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	code, _ := consumerCall(t, srv, priv, "/tower/edge/ack", map[string]any{
		"attempt_id": "att-never-authorized",
		"ack":        signedAck(t, priv, "att-never-authorized", []byte("x"), dispatch.Usage{In: 1, Out: 1}),
	})
	require.Equal(t, http.StatusNotFound, code)
}

// getFailStore wraps a store so Get fails, to exercise the ack handler's read-failure branch.
type getFailStore struct{ dispatch.Store }

func (getFailStore) Get(string) (dispatch.Record, bool, error) {
	return dispatch.Record{}, false, assertErr2
}

// If the attempt store cannot be read at ack time, the handler answers "try again" rather
// than accepting an ack it could not bind.
func TestAnAckWhenTheStoreCannotBeReadIsUnavailable(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.tower.dispatch = dispatch.NewWithStore(dispatch.Config{
		Network: link.PublicNetwork, Lifetime: time.Minute,
	}, getFailStore{dispatch.NewMemStore()})

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	code, _ := consumerCall(t, srv, priv, "/tower/edge/ack", map[string]any{
		"attempt_id": "att-1",
		"ack":        signedAck(t, priv, "att-1", []byte("x"), dispatch.Usage{In: 1, Out: 1}),
	})
	require.Equal(t, http.StatusServiceUnavailable, code)
}

// forceAudit and the audit helpers are safe without the subsystem, like the others.
func TestForceAuditIsSafeWithoutTheSubsystem(t *testing.T) {
	b := testBrokerWithDB(store.NewMem())
	require.NotPanics(t, func() { b.forceAudit("tw", "st", "att", "rq", "rs", 0, 0, 0, 0) })
}

// A disputed settlement force-audits regardless of the sample, and records the outcome as a
// signal - verified at the helper level so the store failure path is exercised too.
func TestForceAuditWantsTheAttempt(t *testing.T) {
	b, _ := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	b.forceAudit(tw.id, "st-1", "att-forced", "rq", "rs", 0, 0, 0, 0)
	pending, err := b.tower.auditWanted.Pending(tw.id, time.Now())
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "att-forced", pending[0].AttemptID)

	// And with a failing store it logs rather than panicking.
	b.tower.auditWanted = failingAudit{}
	require.NotPanics(t, func() { b.forceAudit(tw.id, "st-1", "att-2", "rq", "rs", 0, 0, 0, 0) })
}

// The consumer-binding gate: an ack whose caller key is not the recorded consumer key is
// refused even when everything else is well-formed. (Covers the subtleConstEq mismatch path.)
func TestTheConsumerBindingRefusesAWrongKeyCleanly(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	_ = issuedAttempt(t, b, "att-1", tw.id, "st-1")

	_, wrong, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	code, msg := consumerCall(t, srv, wrong, "/tower/edge/ack", map[string]any{
		"attempt_id": "att-1",
		"ack":        signedAck(t, wrong, "att-1", []byte("x"), dispatch.Usage{In: 1, Out: 1}),
	})
	require.Equal(t, http.StatusNotFound, code, msg)
}

func TestSettleEdgeAttemptRejectsAnUnusableReceipt(t *testing.T) {
	b, _ := towerTestBroker(t)
	// An empty receipt: Reconcile needs the Station's receipt.
	_, _, err := b.settleEdgeAttempt("att-x", dispatch.Receipt{})
	require.Error(t, err)
	// A negative-usage receipt: Reconcile refuses it.
	_, _, err = b.settleEdgeAttempt("att-x",
		dispatch.Receipt{AttemptID: "att-x", ResponseDigest: "d", Usage: dispatch.Usage{Out: -1}})
	require.Error(t, err)
}

// The renew endpoint (not just the challenge) requires the Tower's own signed request.
func TestRenewRequiresTheTowersOwnRequestOnBothRoutes(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	other := enrolledTower(t, b, "owner-2")
	// other Tower signs a renew for tw.
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "nonce": "n",
		"identity_key": base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"signature":    base64.StdEncoding.EncodeToString(make([]byte, 64)),
		"csr":          base64.StdEncoding.EncodeToString([]byte("x")),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := other.call(t, srv, "/tower/renew", body, &out)
	require.Equal(t, http.StatusForbidden, code)
}

func TestAuthorizeReportsARecordFailure(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	routableEdge(t, b, tw.id, "st-1", "m", "203.0.113.7:8443")
	// A dispatch store that refuses Put, so openEdgeAttempt fails.
	b.tower.dispatch = dispatch.NewWithStore(dispatch.Config{
		Network: link.PublicNetwork, Signer: mintSigner(t, b), Lifetime: time.Minute,
	}, putFailStore{dispatch.NewMemStore()})

	consumerPriv := signedInConsumer(t, b)
	code, _ := consumerCall(t, srv, consumerPriv, "/tower/edge/authorize", map[string]any{"model": "m", "consumer_env_key": testEnvKeyHex(t)})
	require.Equal(t, http.StatusServiceUnavailable, code)
}

type putFailStore struct{ dispatch.Store }

func (putFailStore) Put(dispatch.Record) error { return assertErr2 }

// A not-JSON ack body is a bad request.
func TestAnAckWithANonJSONBodyIsRefused(t *testing.T) {
	_, srv := towerTestBroker(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	// Sign over a body that is valid to the signer but not JSON to the handler.
	body := []byte("not json at all")
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/tower/edge/ack", strings.NewReader(string(body)))
	require.NoError(t, err)
	pub, ts, sig := protocol.SignRequest(priv, http.MethodPost, "/tower/edge/ack", body)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// If the ack store cannot record a well-formed, correctly-bound ack, the consumer is told to
// retry rather than being told it succeeded.
func TestAnAckThatCannotBeStoredIsUnavailable(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	consumerPriv := issuedAttempt(t, b, "att-1", tw.id, "st-1")
	b.tower.acks = failingAcks{}

	code, _ := consumerCall(t, srv, consumerPriv, "/tower/edge/ack", map[string]any{
		"attempt_id": "att-1",
		"ack":        signedAck(t, consumerPriv, "att-1", []byte("x"), dispatch.Usage{In: 1, Out: 1}),
	})
	require.Equal(t, http.StatusServiceUnavailable, code)
}

type failingAcks struct{}

func (failingAcks) Put(string, dispatch.Ack) error         { return assertErr2 }
func (failingAcks) Get(string) (dispatch.Ack, bool, error) { return dispatch.Ack{}, false, nil }
func (failingAcks) Reap(time.Time) (int64, error)          { return 0, nil }

// mintSigner returns the broker's real dispatch signer so a swapped-in registry still mints
// grants Core will accept.
func mintSigner(t *testing.T, b *broker) ed25519.PrivateKey {
	t.Helper()
	// The broker exposes only the public half; for this test a store that fails Put is reached
	// before any signature matters, so a throwaway signer is fine.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return priv
}

// --- certificate revocation (security review) --------------------------------

// adminRevoke posts a signed admin cert-revoke.
func adminRevoke(t *testing.T, b *broker, srv *httptest.Server, towerID string) int {
	t.Helper()
	b.adminKey = "admin-secret"
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/tower/cert/revoke",
		strings.NewReader(`{"tower_id":"`+towerID+`"}`))
	require.NoError(t, err)
	req.Header.Set("X-Roger-Admin", "admin-secret")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	return resp.StatusCode
}

// A revoked certificate stops the Tower on its VERY NEXT request - the review's "revocation
// not enforced" finding. It is enforced at the request-auth layer against the cert serial,
// because this deployment authenticates by signed request rather than at a TLS handshake.
func TestARevokedCertificateStopsTheTowerAtOnce(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))
	// Give it a certificate serial, the way enrollment/renewal does, so there is something to
	// revoke - the test helper admits a Tower without minting a real certificate.
	_, err := b.tower.registry.RecordRenewal(tw.id, admit.Renewal{
		CertSerial: "12345", TLSKeyHash: "hash", At: time.Now(),
	})
	require.NoError(t, err)

	// Before revocation the Tower's own signed request is accepted (a session open).
	body, err := json.Marshal(map[string]any{"tower_id": tw.id})
	require.NoError(t, err)
	var ch map[string]any
	code, _ := tw.call(t, srv, "/tower/renew/challenge", body, &ch)
	require.Equal(t, http.StatusOK, code, "accepted before revocation")

	require.Equal(t, http.StatusOK, adminRevoke(t, b, srv, tw.id))

	// After revocation the SAME signed request is refused - the key still signs, but the
	// certificate it enrolled with is revoked.
	code, _ = tw.call(t, srv, "/tower/renew/challenge", body, &ch)
	require.Equal(t, http.StatusForbidden, code, "a revoked certificate stops the Tower")

	// And it was suspended, and its fleet withdrawn.
	got, _ := b.tower.registry.Get(tw.id)
	require.Equal(t, admit.StateSuspended, got.State)
}

func TestCertRevokeRefusals(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	// Unsigned (no admin header) is refused.
	resp, err := http.Post(srv.URL+"/tower/cert/revoke", "application/json",
		strings.NewReader(`{"tower_id":"tw-x"}`))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// Admin, but an unknown Tower.
	require.Equal(t, http.StatusNotFound, adminRevoke(t, b, srv, "tw-nobody"))

	// Admin, a real Tower, but no certificate recorded yet.
	tw := enrolledTower(t, b, "owner-1")
	require.Equal(t, http.StatusConflict, adminRevoke(t, b, srv, tw.id),
		"a Tower that has not been issued a certificate has none to revoke")
}
