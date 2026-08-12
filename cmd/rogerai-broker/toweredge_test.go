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
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/link"
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

func signedAck(t *testing.T, priv ed25519.PrivateKey, attemptID string, response []byte,
	u dispatch.Usage) string {
	t.Helper()
	a, err := dispatch.SignAck(priv, link.PublicNetwork, attemptID, response, u,
		time.Now(), time.Now())
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(a.Signed)
}

func signedReceipt(t *testing.T, priv ed25519.PrivateKey, attemptID, stationID string,
	response []byte) string {
	t.Helper()
	rec, err := dispatch.SignReceipt(priv, link.PublicNetwork,
		dispatch.Grant{AttemptID: attemptID, StationID: stationID}, response)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(rec.Signed)
}

// THE HAPPY PATH, both halves: the consumer's account arrives, the Station's receipt arrives
// by its Tower, and settlement takes the lower of the two.
func TestAnEdgeAttemptSettlesOnTwoOpposingAccounts(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")

	response := []byte(`{"choices":[{"text":"hello"}]}`)
	_, consumerPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	code, out := consumerCall(t, srv, consumerPriv, "/tower/edge/ack", map[string]any{
		"attempt_id": "att-1",
		"ack":        signedAck(t, consumerPriv, "att-1", response, dispatch.Usage{In: 10, Out: 90}),
	})
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, true, out["recorded"])

	// The Station CLAIMS more output than the consumer saw.
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt":   signedReceipt(t, stationPriv, "att-1", "st-1", response),
		"usage_in":  10,
		"usage_out": 100,
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

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-lonely",
		"receipt":  signedReceipt(t, stationPriv, "att-lonely", "st-1", []byte("answer")),
		"usage_in": 1, "usage_out": 7,
	})
	require.NoError(t, err)
	var settled map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &settled)
	require.Equal(t, http.StatusOK, code, settled)
	require.Equal(t, false, settled["corroborated"])
	require.Equal(t, float64(7), settled["billable_out"], "the operator is still paid")
}

// TWO SIGNED DIGESTS WITH A RELAY BETWEEN THEM. A disagreement about the bytes is not a
// rounding difference; it is attributable to the only party that saw both.
func TestADisagreementAboutTheAnswerIsRefusedAndAttributed(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")

	_, consumerPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	code, _ := consumerCall(t, srv, consumerPriv, "/tower/edge/ack", map[string]any{
		"attempt_id": "att-1",
		"ack": signedAck(t, consumerPriv, "att-1", []byte("what the consumer received"),
			dispatch.Usage{In: 1, Out: 1}),
	})
	require.Equal(t, http.StatusOK, code)

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-1",
			[]byte("what the Station returned")),
		"usage_in": 1, "usage_out": 1,
	})
	require.NoError(t, err)
	var out map[string]any
	code, msg := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusConflict, code)
	require.Contains(t, msg, "the only party between them is the relay")
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
	_, srv := towerTestBroker(t)
	_, requestPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	code, _ := consumerCall(t, srv, requestPriv, "/tower/edge/ack", map[string]any{
		"attempt_id": "att-1",
		"ack":        signedAck(t, otherPriv, "att-1", []byte("x"), dispatch.Usage{}),
	})
	require.Equal(t, http.StatusBadRequest, code)
}

func TestAMalformedAcknowledgementIsRefused(t *testing.T) {
	_, srv := towerTestBroker(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

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

	_, impostor, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceipt(t, impostor, "att-1", "st-1", []byte("answer")),
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

	body, err := json.Marshal(map[string]any{
		"tower_id": mine.id, "station_id": "st-theirs", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-theirs", []byte("answer")),
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
	good := signedReceipt(t, stationPriv, "att-1", "st-1", []byte("answer"))

	for name, tc := range map[string]struct {
		in   map[string]any
		want int
	}{
		"no attempt":     {map[string]any{"tower_id": tw.id, "station_id": "st-1", "receipt": good}, http.StatusBadRequest},
		"no station":     {map[string]any{"tower_id": tw.id, "attempt_id": "att-1", "receipt": good}, http.StatusBadRequest},
		"no receipt":     {map[string]any{"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1"}, http.StatusBadRequest},
		"not base64":     {map[string]any{"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1", "receipt": "!!!"}, http.StatusBadRequest},
		"unknown statn":  {map[string]any{"tower_id": tw.id, "station_id": "st-nope", "attempt_id": "att-1", "receipt": good}, http.StatusNotFound},
		"other attempt":  {map[string]any{"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-OTHER", "receipt": good}, http.StatusForbidden},
		"negative usage": {map[string]any{"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1", "receipt": good, "usage_out": -1}, http.StatusBadRequest},
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
	// No acknowledgement recorded: uncorroborated, and the Station's own count stands.
	s, err := b.settleEdgeAttempt("att-none",
		dispatch.Receipt{AttemptID: "att-none", ResponseDigest: "d"},
		dispatch.Usage{In: 1, Out: 2})
	require.NoError(t, err)
	require.False(t, s.Corroborated)
	require.Equal(t, dispatch.Usage{In: 1, Out: 2}, s.Billable)
}
