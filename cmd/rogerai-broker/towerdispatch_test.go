package main

// towerdispatch_test.go drives a request all the way to a Station and back.
//
// Everything in this file is REAL except the model: a real grant signed by the broker's own
// derived key, a real Station holding its own assertion key and running the real Executor, a
// real receipt, and the real verification path. Only the thing at the far end that would
// otherwise need a GPU is a stub.
//
// That matters because every individual check here can be made to pass by a component that
// checks nothing. Only running the two halves against each other shows they agree.

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/station"
	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/link"
)

// stubModel is the only unreal part.
type stubModel struct {
	body []byte
	saw  []byte
	err  error
}

func (m *stubModel) Serve(_ context.Context, req []byte) ([]byte, error) {
	m.saw = append([]byte(nil), req...)
	return m.body, m.err
}

// dispatchable stands a Tower up with one routable, promoted, real Station behind it.
func dispatchable(t *testing.T, b *broker, srv *httptest.Server, op operator) (linkTower, *station.Station) {
	t.Helper()
	lt := enrolledTower(t, b, op.login)
	// Out of quarantine, or it may hold a link and still take no work - which is the correct
	// behaviour and not what this file is about.
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateActive))
	stn := attachReal(t, b, "auth-dispatch-"+lt.id, lt.id, ownerPubkeyOf(t, b, op.login))
	openLink(t, srv, lt)

	signed, err := stn.SignOffer(station.Offer{
		Network: link.PublicNetwork, TowerID: lt.id, Model: "roger-1", Modality: "text",
		PriceIn: 1000, PriceOut: 2000, EarnIn: 800, EarnOut: 1600, Capacity: 4,
		Capabilities: []string{"chat"}, TTL: time.Hour,
	}, time.Now())
	require.NoError(t, err)
	code, raw := lt.call(t, srv, "/tower/inventory",
		wrapLeaves(t, lt, 1, "genesis", []json.RawMessage{signed}), nil)
	require.Equal(t, http.StatusOK, code, raw)
	return lt, stn
}

// THE WHOLE CHAIN. A request no direct node can serve reaches a Station behind a Tower, and
// the answer that comes back is one Core verified rather than one it was told.
func TestARequestReachesAStationAndTheAnswerComesBackVerified(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt, stn := dispatchable(t, b, srv, op)

	model := &stubModel{body: []byte(`{"choices":[{"message":{"content":"hello from the station"}}]}`)}
	exec := station.Executor{
		Station: stn, CoreKey: b.tower.dispatchPub, Network: link.PublicNetwork, Upstream: model,
	}

	// The Tower's side, driven directly: poll, relay, return.
	done := make(chan struct{})
	go func() {
		defer close(done)
		work := pollForWork(t, srv, lt)
		resp := exec.Execute(context.Background(), station.ExecuteRequest{
			Grant: work.Grant, Request: work.Request,
		})
		require.Empty(t, resp.Failure)
		returnResult(t, srv, lt, work.AttemptID, resp)
	}()

	request := []byte(`{"model":"roger-1","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(request)))
	require.True(t, b.tryTowerDispatch(rec, r, "roger-1", request, false),
		"a routable Station must be used rather than refused")
	<-done

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, string(model.body), rec.Body.String())
	require.Equal(t, request, model.saw, "the Station served exactly the authorized bytes")

	// FREE, and said so on the response. A caller has no other way to tell this apart from a
	// billed answer, and "you were not charged" must not be implicit.
	require.Equal(t, "tower", rec.Header().Get("X-RogerAI-Origin"))
	require.Equal(t, "0", rec.Header().Get("X-RogerAI-Cost"))
}

// A TOWER CANNOT ALTER THE ANSWER. It holds every byte on the way back and is the party with
// something to gain; the receipt commits to a digest, and Core refuses the mismatch.
func TestATowerCannotChangeTheStationsAnswer(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt, stn := dispatchable(t, b, srv, op)

	model := &stubModel{body: []byte(`{"content":"the real answer"}`)}
	exec := station.Executor{
		Station: stn, CoreKey: b.tower.dispatchPub, Network: link.PublicNetwork, Upstream: model,
	}

	work := issueWork(t, b, "roger-1", []byte(`{"model":"roger-1"}`))
	resp := exec.Execute(context.Background(), station.ExecuteRequest{
		Grant: work.Grant, Request: work.Request,
	})
	require.Empty(t, resp.Failure)

	// The relay rewrites the body, keeping the Station's perfectly valid receipt.
	code, raw := postResult(t, srv, lt, map[string]any{
		"tower_id": lt.id, "attempt_id": work.AttemptID,
		"receipt": resp.Receipt, "body": json.RawMessage(`{"content":"a cheaper answer"}`),
	})
	require.Equal(t, http.StatusBadRequest, code, raw)
	require.Contains(t, raw, "response digest")
}

// A TOWER CANNOT FORGE A RESULT either: it has a valid identity of its own and no access to
// the Station's assertion key.
func TestATowerCannotFabricateAResult(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt, _ := dispatchable(t, b, srv, op)

	work := issueWork(t, b, "roger-1", []byte(`{"model":"roger-1"}`))
	// The Tower signs a receipt with its own good key.
	_, forgerPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	body := []byte(`{"content":"made up"}`)
	forged, err := dispatch.SignReceipt(forgerPriv, link.PublicNetwork,
		dispatch.Grant{AttemptID: work.AttemptID, StationID: "st-x"}, body)
	require.NoError(t, err)

	code, raw := postResult(t, srv, lt, map[string]any{
		"tower_id": lt.id, "attempt_id": work.AttemptID,
		"receipt": forged, "body": json.RawMessage(body),
	})
	require.Equal(t, http.StatusBadRequest, code, raw)
	require.Contains(t, raw, "assertion key")
}

// A QUARANTINED TOWER COLLECTS NOTHING. It holds a live link and a valid key by design -
// that is what quarantine is - and must still never be handed customer work.
func TestAQuarantinedTowerIsHandedNoWork(t *testing.T) {
	shortPolls(t)
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	openLink(t, srv, lt)

	code, raw := lt.call(t, srv, "/tower/dispatch",
		[]byte(`{"tower_id":"`+lt.id+`"}`), nil)
	require.Equal(t, http.StatusForbidden, code, raw)
	require.Contains(t, raw, "not eligible")
}

// And a Tower cannot collect another Tower's work: the queue is keyed by who signed.
func TestATowerOnlySeesItsOwnWork(t *testing.T) {
	shortPolls(t)
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt, _ := dispatchable(t, b, srv, op)
	other, _ := dispatchable(t, b, srv, signedInOperator(t, b, "hubot"))

	// Offered but NOT claimed: this test is about who may collect it, and claiming here would
	// mean the rightful Tower's poll found it already taken.
	work := offerWork(t, b, "roger-1", []byte(`{"model":"roger-1"}`))
	require.NotEqual(t, lt.id, other.id)

	// Whoever it was issued for, the OTHER Tower's poll must not return it.
	holder, bystander := lt, other
	if work.towerID != lt.id {
		holder, bystander = other, lt
	}
	code, raw := bystander.call(t, srv, "/tower/dispatch",
		[]byte(`{"tower_id":"`+bystander.id+`"}`), nil)
	require.Equal(t, http.StatusNoContent, code, raw)

	// The rightful Tower still has it.
	got := pollForWork(t, srv, holder)
	require.Equal(t, work.AttemptID, got.AttemptID)
}

// The Station's grant key is published so a Station can pin it. Without it a Station has no
// way to tell a real grant from one its own relay wrote.
func TestCorePublishesItsGrantKey(t *testing.T) {
	b, srv := towerTestBroker(t)
	resp, err := http.Get(srv.URL + "/tower/dispatch/key")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct {
		Network     string `json:"network"`
		DispatchKey string `json:"dispatch_key"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Equal(t, link.PublicNetwork, out.Network)
	raw, err := hex.DecodeString(out.DispatchKey)
	require.NoError(t, err)
	require.Equal(t, []byte(b.tower.dispatchPub), raw, "the published key must be the signing one")
}

// THE DERIVED KEY IS STABLE. A Station pins it, so a broker restart that changed it would
// silently break every Station on the network - and the failure would look like tampering.
func TestTheGrantKeyIsStableAcrossRestarts(t *testing.T) {
	b, _ := towerTestBroker(t)
	first := b.tower.dispatchPub

	again, err := deriveDispatchKey(b.tower.ca)
	require.NoError(t, err)
	require.Equal(t, []byte(first), []byte(again.Public().(ed25519.PublicKey)))

	// And it is a DIFFERENT KEY from the CA root, and a different algorithm: the root signs
	// certificates with ECDSA, grants are Ed25519, and deriving is what keeps a mistake in
	// one use from changing what the other means.
	_, isECDSA := b.tower.ca.RootKey().(*ecdsa.PrivateKey)
	require.True(t, isECDSA, "the CA root signs certificates, not grants")
}

// When no Station can serve the model, dispatch declines and the caller's own refusal stands.
func TestDispatchDeclinesWhenNoStationServesTheModel(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	_, _ = dispatchable(t, b, srv, op)

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	require.False(t, b.tryTowerDispatch(rec, r, "a-model-nobody-serves", []byte(`{}`), false))

	// And a STREAMING request is declined too: a streamed answer through a relay-of-a-relay
	// needs the inner session, and answering a stream with a whole body would break the
	// client's contract.
	require.False(t, b.tryTowerDispatch(rec, r, "roger-1", []byte(`{}`), true))
}

// A Station that refuses is relayed as a failure, and the caller is told - rather than
// waiting out the deadline for an answer that is never coming.
func TestAStationFailureReachesTheCallerPromptly(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt, _ := dispatchable(t, b, srv, op)

	done := make(chan struct{})
	go func() {
		defer close(done)
		work := pollForWork(t, srv, lt)
		returnResult(t, srv, lt, work.AttemptID,
			station.ExecuteResponse{Failure: "the model is not loaded"})
	}()

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	require.True(t, b.tryTowerDispatch(rec, r, "roger-1", []byte(`{"model":"roger-1"}`), false))
	<-done
	require.Equal(t, http.StatusBadGateway, rec.Code)
}

// --- helpers ---------------------------------------------------------------

func pollForWork(t *testing.T, srv *httptest.Server, lt linkTower) towerWork {
	t.Helper()
	var work towerWork
	code, raw := lt.call(t, srv, "/tower/dispatch", []byte(`{"tower_id":"`+lt.id+`"}`), &work)
	require.Equal(t, http.StatusOK, code, raw)
	require.NotEmpty(t, work.AttemptID)
	return work
}

func returnResult(t *testing.T, srv *httptest.Server, lt linkTower, attemptID string, resp station.ExecuteResponse) {
	t.Helper()
	payload := map[string]any{"tower_id": lt.id, "attempt_id": attemptID}
	if resp.Failure != "" {
		payload["failure"] = resp.Failure
	} else {
		payload["receipt"] = resp.Receipt
		payload["body"] = resp.Body
	}
	code, raw := postResult(t, srv, lt, payload)
	require.Equal(t, http.StatusOK, code, raw)
}

func postResult(t *testing.T, srv *httptest.Server, lt linkTower, payload map[string]any) (int, string) {
	t.Helper()
	return lt.call(t, srv, "/tower/dispatch/result", jsonOf(t, payload), nil)
}

// offerWork puts one attempt on the queue without claiming it - the state work is in
// between being created and a Tower collecting it.
func offerWork(t *testing.T, b *broker, model string, request []byte) towerWork {
	t.Helper()
	target, ok := b.pickTowerStation(model)
	require.True(t, ok, "no routable Station for %s", model)
	g, err := b.tower.dispatch.Issue(target, request)
	require.NoError(t, err)
	w := towerWork{AttemptID: g.AttemptID, Grant: g.Signed, Request: request, towerID: target.TowerID}
	b.tower.queue.offer(w)
	return w
}

// issueWork is offerWork plus the claim, standing in for the poll that would have handed it
// out. The tests that use it are about what happens to a RESULT, and an unclaimed attempt is
// refused long before any of that.
func issueWork(t *testing.T, b *broker, model string, request []byte) towerWork {
	t.Helper()
	w := offerWork(t, b, model, request)
	_, err := b.tower.dispatch.Claim(w.AttemptID, w.towerID)
	require.NoError(t, err)
	return w
}

// shortPolls stops a "there was nothing to collect" assertion from costing the full
// production poll window. A suite that takes a minute per such test is a suite people stop
// running, which is a worse outcome than the realism it buys.
func shortPolls(t *testing.T) {
	t.Helper()
	was := dispatchPollWait
	dispatchPollWait = 150 * time.Millisecond
	t.Cleanup(func() { dispatchPollWait = was })
}
