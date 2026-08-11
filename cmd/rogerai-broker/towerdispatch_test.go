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
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/station"
	"rogerai.fm/roger/v5/internal/store"
	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/attach"
	"rogerai.fm/roger/v5/internal/towercore/attempt"
	"rogerai.fm/roger/v5/internal/towercore/cert"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/enroll"
	"rogerai.fm/roger/v5/internal/towercore/fleet"
	"rogerai.fm/roger/v5/internal/towercore/head"
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
	// Issue already made it pending in the store, which IS the queue - nothing else to do.
	return towerWork{
		AttemptID: g.AttemptID, Grant: g.Signed, Request: request, towerID: target.TowerID,
	}
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

// --- two brokers ------------------------------------------------------------
//
// Production runs more than one, and everything below is a property that was silently FALSE
// while the attempt table lived in each process: a Tower reaches whichever instance the load
// balancer chose, and that is very often not the one holding its work.

// twoBrokers builds two instances over ONE set of stores - which is what a real fleet is.
// The CA custody is shared too, so both derive the same grant key and a Station pinning it
// can verify a grant whichever broker issued it.
func twoBrokers(t *testing.T) (*broker, *httptest.Server, *broker, *httptest.Server) {
	t.Helper()
	registry, custody, enrollment := admit.NewMemStore(), cert.NewMemCustody(), enroll.NewMemStore()
	stations, heads, attempts := attach.NewMemStore(), head.NewMemStore(), dispatch.NewMemStore()
	routable := fleet.NewMemStore()
	shared := store.NewMem()

	build := func() (*broker, *httptest.Server) {
		b := testBrokerWithDB(shared)
		ts, err := newTowerSubsystem(b, registry, custody, enrollment, cert.Config{TTL: time.Hour},
			linkDeps{stations: stations, heads: heads, attempts: attempts, routable: routable})
		require.NoError(t, err)
		b.tower = ts
		mux := http.NewServeMux()
		b.registerTowerRoutes(mux)
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		return b, srv
	}
	a, aSrv := build()
	c, cSrv := build()
	return a, aSrv, c, cSrv
}

// WORK CREATED ON ONE BROKER IS COLLECTED FROM THE OTHER. Without this a Tower is served only
// when the load balancer happens to send its poll to the instance holding its work - so half
// the requests time out, and nothing anywhere reports an error.
func TestWorkCreatedOnOneBrokerIsCollectedFromTheOther(t *testing.T) {
	shortPolls(t)
	a, aSrv, c, cSrv := twoBrokers(t)
	op := signedInOperator(t, a, "octocat")
	lt, _ := dispatchable(t, a, aSrv, op)

	// Issued on A.
	work := offerWork(t, a, "roger-1", []byte(`{"model":"roger-1"}`))

	// Collected from C, which never saw it created.
	ltOnC := lt
	var got towerWork
	code, raw := ltOnC.call(t, cSrv, "/tower/dispatch", []byte(`{"tower_id":"`+lt.id+`"}`), &got)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, work.AttemptID, got.AttemptID)
	require.NotEmpty(t, got.Grant, "the signed grant travelled with it")
	require.JSONEq(t, `{"model":"roger-1"}`, string(got.Request),
		"and so did the request, or the other broker would have nothing to relay")

	// And A will not hand it out again: the claim is shared, not per-instance.
	code, _ = lt.call(t, aSrv, "/tower/dispatch", []byte(`{"tower_id":"`+lt.id+`"}`), nil)
	require.Equal(t, http.StatusNoContent, code, "one claim, one delivery, across brokers")
	_ = c
}

// TWO BROKERS POLLED AT ONCE HAND OUT ONE ATTEMPT ONCE. This is the guarantee that used to
// hold per-instance and therefore not at all.
func TestTwoBrokersPolledTogetherServeAnAttemptOnce(t *testing.T) {
	shortPolls(t)
	a, aSrv, _, cSrv := twoBrokers(t)
	op := signedInOperator(t, a, "octocat")
	lt, _ := dispatchable(t, a, aSrv, op)
	offerWork(t, a, "roger-1", []byte(`{"model":"roger-1"}`))

	type outcome struct {
		code int
		id   string
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for _, srv := range []*httptest.Server{aSrv, cSrv} {
		wg.Add(1)
		go func(s *httptest.Server) {
			defer wg.Done()
			var got towerWork
			code, _ := lt.call(t, s, "/tower/dispatch", []byte(`{"tower_id":"`+lt.id+`"}`), &got)
			results <- outcome{code, got.AttemptID}
		}(srv)
	}
	wg.Wait()
	close(results)

	served := 0
	for r := range results {
		if r.code == http.StatusOK && r.id != "" {
			served++
		}
	}
	require.Equal(t, 1, served, "exactly one broker may hand the attempt out")
}

// A RESULT POSTED TO THE OTHER BROKER IS STILL REFUSED ONCE SETTLED. Both instances share the
// one-use rule, so a Tower cannot get a second answer accepted by asking the other one.
func TestAResultCannotBeSettledTwiceAcrossBrokers(t *testing.T) {
	a, aSrv, _, cSrv := twoBrokers(t)
	op := signedInOperator(t, a, "octocat")
	lt, stn := dispatchable(t, a, aSrv, op)

	work := issueWork(t, a, "roger-1", []byte(`{"model":"roger-1"}`))
	exec := station.Executor{
		Station: stn, CoreKey: a.tower.dispatchPub, Network: link.PublicNetwork,
		Upstream: &stubModel{body: []byte(`{"content":"answer"}`)},
	}
	resp := exec.Execute(context.Background(), station.ExecuteRequest{
		Grant: work.Grant, Request: work.Request,
	})
	require.Empty(t, resp.Failure)

	payload := map[string]any{
		"tower_id": lt.id, "attempt_id": work.AttemptID,
		"receipt": resp.Receipt, "body": resp.Body,
	}
	code, raw := postResult(t, aSrv, lt, payload)
	require.Equal(t, http.StatusOK, code, raw)

	// The same result, posted to the OTHER broker.
	code, raw = postResult(t, cSrv, lt, payload)
	require.Equal(t, http.StatusConflict, code, raw)
	require.Contains(t, raw, "already settled")
}

// BOTH BROKERS SIGN GRANTS A STATION WILL ACCEPT. They share a CA root, so they derive the
// same grant key - a Station pins one key and must not care which instance it reached.
func TestBothBrokersSignGrantsTheSameStationAccepts(t *testing.T) {
	a, aSrv, c, _ := twoBrokers(t)
	require.Equal(t, []byte(a.tower.dispatchPub), []byte(c.tower.dispatchPub),
		"a Station pins ONE key; two brokers signing differently would break half its work")

	op := signedInOperator(t, a, "octocat")
	_, stn := dispatchable(t, a, aSrv, op)
	request := []byte(`{"model":"roger-1"}`)

	// A grant minted by C for the same Station, verified by a Station that pinned the key
	// published by A. The target is taken from A because C cannot SELECT one yet - see
	// TestABrokerCannotYetRouteToATowerConnectedElsewhere - but C signs it, which is what
	// this test is about.
	target, ok := a.pickTowerStation("roger-1")
	require.True(t, ok)
	g, err := c.tower.dispatch.Issue(target, request)
	require.NoError(t, err)

	exec := station.Executor{
		Station: stn, CoreKey: a.tower.dispatchPub, Network: link.PublicNetwork,
		Upstream: &stubModel{body: []byte(`{"content":"ok"}`)},
	}
	got := exec.Execute(context.Background(), station.ExecuteRequest{Grant: g.Signed, Request: request})
	require.Empty(t, got.Failure)
	require.NotNil(t, got.Receipt)
}

// A BROKER CAN ROUTE TO A TOWER CONNECTED ELSEWHERE.
//
// A Tower holds ONE link, to ONE broker, and its accepted inventory lives in that instance's
// memory - so every other broker used to be unable to see the Station at all and fell back
// to "no node offers this model". With two brokers a perfectly healthy Tower served roughly
// half the requests it should have, and nothing anywhere reported a problem.
//
// This assertion was the inverse until the fleet projection existed; it is inverted rather
// than deleted because the inversion is the change.
func TestABrokerCanRouteToATowerConnectedElsewhere(t *testing.T) {
	a, aSrv, c, _ := twoBrokers(t)
	op := signedInOperator(t, a, "octocat")
	_, _ = dispatchable(t, a, aSrv, op)

	_, ok := a.pickTowerStation("roger-1")
	require.True(t, ok, "the broker holding the link can route to it")

	target, ok := c.pickTowerStation("roger-1")
	require.True(t, ok, "and so can the one that has never seen this Tower's link")
	require.NotEmpty(t, target.StationID)
	require.NotEmpty(t, target.AssertionKey,
		"with the key from the ATTACHMENT, never from the projection")
}

// THE PROJECTION IS A HINT, NOT AUTHORITY. Every check is re-run against the registry and
// the attachment before a grant is issued, so a Station that has since been revoked - or a
// Tower that has since been suspended - is not dispatched to however fresh the row looks.
func TestTheFleetViewNeverOverridesAuthority(t *testing.T) {
	a, aSrv, c, _ := twoBrokers(t)
	op := signedInOperator(t, a, "octocat")
	lt, stn := dispatchable(t, a, aSrv, op)

	_, ok := c.pickTowerStation("roger-1")
	require.True(t, ok)

	// Suspended on A. C's projection still lists the Station.
	require.NoError(t, a.tower.registry.Transition(lt.id, admit.StateSuspended))
	_, ok = c.pickTowerStation("roger-1")
	require.False(t, ok, "a suspended Tower's rows must not be dispatched to")

	// Back in service, then the STATION is revoked.
	require.NoError(t, a.tower.registry.Transition(lt.id, admit.StateQuarantine))
	require.NoError(t, a.tower.registry.Transition(lt.id, admit.StateActive))
	_, ok = c.pickTowerStation("roger-1")
	require.True(t, ok)

	_, err := a.tower.stations.Revoke(stn.StationID)
	require.NoError(t, err)
	_, ok = c.pickTowerStation("roger-1")
	require.False(t, ok, "a revoked Station must not be dispatched to")
}

// A DRAIN WITHDRAWS THE FLEET EVERYWHERE. That is the whole difference between draining and
// walking away: the capacity stops being offered at once rather than aging out.
func TestDrainingWithdrawsTheFleetFromEveryBroker(t *testing.T) {
	a, aSrv, c, _ := twoBrokers(t)
	op := signedInOperator(t, a, "octocat")
	lt, _ := dispatchable(t, a, aSrv, op)

	_, ok := c.pickTowerStation("roger-1")
	require.True(t, ok)

	code, raw := lt.call(t, aSrv, "/tower/session/close", jsonOf(t, link.Frame{
		Network: link.PublicNetwork, Version: 1, TowerID: lt.id,
	}), nil)
	require.Equal(t, http.StatusOK, code, raw)

	_, ok = c.pickTowerStation("roger-1")
	require.False(t, ok, "a drained Tower is offered by nobody")
}

// THE ANSWER CROSSES BACK. The caller waits on the broker that issued the grant, and the
// Tower returns the result to whichever broker it reached - very often a different one. This
// is the other half of multi-instance: without it the work is dispatched perfectly and the
// caller times out anyway.
func TestAnAnswerReturnedToOneBrokerReachesTheCallerOnTheOther(t *testing.T) {
	mr := miniredis.RunT(t)
	a, aSrv, c, cSrv := twoBrokersOnBus(t, mr)
	op := signedInOperator(t, a, "octocat")
	lt, stn := dispatchable(t, a, aSrv, op)

	model := &stubModel{body: []byte(`{"choices":[{"message":{"content":"across brokers"}}]}`)}
	exec := station.Executor{
		Station: stn, CoreKey: a.tower.dispatchPub, Network: link.PublicNetwork, Upstream: model,
	}

	// The Tower polls C and returns its answer to C.
	done := make(chan struct{})
	go func() {
		defer close(done)
		var work towerWork
		require.Eventually(t, func() bool {
			code, _ := lt.call(t, cSrv, "/tower/dispatch", []byte(`{"tower_id":"`+lt.id+`"}`), &work)
			return code == http.StatusOK && work.AttemptID != ""
		}, 10*time.Second, 20*time.Millisecond)

		resp := exec.Execute(context.Background(), station.ExecuteRequest{
			Grant: work.Grant, Request: work.Request,
		})
		require.Empty(t, resp.Failure)
		returnResult(t, cSrv, lt, work.AttemptID, resp)
	}()

	// The caller is on A.
	request := []byte(`{"model":"roger-1","messages":[]}`)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(request)))
	require.True(t, a.tryTowerDispatch(rec, r, "roger-1", request, false))
	<-done

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, string(model.body), rec.Body.String(),
		"the answer settled on one broker reached the caller parked on the other")
	require.Equal(t, "0", rec.Header().Get("X-RogerAI-Cost"), "and it is still free")
	_ = c
}

// A result nobody is waiting for anywhere is ACCEPTED rather than refused: the Station did
// its job, the caller gave up, and re-sending would not help.
func TestAnAnswerNobodyIsWaitingForIsStillAccepted(t *testing.T) {
	mr := miniredis.RunT(t)
	a, aSrv, _, cSrv := twoBrokersOnBus(t, mr)
	op := signedInOperator(t, a, "octocat")
	lt, stn := dispatchable(t, a, aSrv, op)

	work := issueWork(t, a, "roger-1", []byte(`{"model":"roger-1"}`))
	exec := station.Executor{
		Station: stn, CoreKey: a.tower.dispatchPub, Network: link.PublicNetwork,
		Upstream: &stubModel{body: []byte(`{"content":"nobody home"}`)},
	}
	resp := exec.Execute(context.Background(), station.ExecuteRequest{
		Grant: work.Grant, Request: work.Request,
	})
	require.Empty(t, resp.Failure)

	code, raw := postResult(t, cSrv, lt, map[string]any{
		"tower_id": lt.id, "attempt_id": work.AttemptID,
		"receipt": resp.Receipt, "body": resp.Body,
	})
	require.Equal(t, http.StatusOK, code, raw)
	require.Contains(t, raw, `"ok":true`)
}

// twoBrokersOnBus is twoBrokers with a real shared bus between them.
func twoBrokersOnBus(t *testing.T, mr *miniredis.Miniredis) (*broker, *httptest.Server, *broker, *httptest.Server) {
	t.Helper()
	a, aSrv, c, cSrv := twoBrokers(t)
	for _, b := range []*broker{a, c} {
		vs, err := newValkeyStore("redis://" + mr.Addr())
		require.NoError(t, err)
		t.Cleanup(func() { _ = vs.Close() })
		b.shared, b.multiInstance = vs, true
	}
	return a, aSrv, c, cSrv
}

// --- the shared refusals ----------------------------------------------------
//
// Both dispatch routes carry the same preamble, copied per handler - which is exactly the
// kind of check that goes missing from one of them. Asserted per route rather than once.

func TestTheDispatchRoutesRefuseTheirBadInputs(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateActive))

	for _, path := range []string{"/tower/dispatch", "/tower/dispatch/result"} {
		// The wrong method.
		resp, err := http.Get(srv.URL + path)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, path)

		// A malformed body is a 400, never a 500.
		code, raw := lt.call(t, srv, path, []byte("{nope"), nil)
		require.Equal(t, http.StatusBadRequest, code, "%s: %s", path, raw)

		// AN UNSIGNED CALLER REACHES NOTHING. Work and results are both Tower-authenticated;
		// either one open would let anybody collect somebody else's jobs or answer them.
		unsigned, err := http.Post(srv.URL+path, "application/json",
			strings.NewReader(`{"tower_id":"`+lt.id+`","attempt_id":"att-1"}`))
		require.NoError(t, err)
		body, _ := io.ReadAll(unsigned.Body)
		unsigned.Body.Close()
		require.Equal(t, http.StatusForbidden, unsigned.StatusCode, "%s: %s", path, body)
	}

	// GET-only for the key, and it is public - a Station's operator has to fetch it before
	// they have any credential at all.
	resp, err := http.Post(srv.URL+"/tower/dispatch/key", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

// A deployment with no joined-Tower subsystem answers "unavailable" on every dispatch route
// rather than dereferencing a nil.
func TestTheDispatchRoutesNeedTheSubsystem(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	b.tower = nil

	for _, path := range []string{"/tower/dispatch", "/tower/dispatch/result"} {
		code, raw := lt.call(t, srv, path, []byte(`{"tower_id":"x"}`), nil)
		require.Equal(t, http.StatusServiceUnavailable, code, "%s: %s", path, raw)
	}
	resp, err := http.Get(srv.URL + "/tower/dispatch/key")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

// A broker whose dispatch machinery was never built says so rather than pretending there is
// no work - "nothing to do" is an answer a Tower acts on, and it would be a wrong one.
func TestADeploymentWithoutDispatchSaysSoRatherThanSayingNoWork(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateActive))
	b.tower.queue = nil

	code, raw := lt.call(t, srv, "/tower/dispatch", []byte(`{"tower_id":"`+lt.id+`"}`), nil)
	require.Equal(t, http.StatusServiceUnavailable, code, raw)
	code, raw = lt.call(t, srv, "/tower/dispatch/result",
		[]byte(`{"tower_id":"`+lt.id+`","attempt_id":"att-1"}`), nil)
	require.Equal(t, http.StatusServiceUnavailable, code, raw)
}

// AN UNREADABLE ATTEMPT STORE IS NOT AN EMPTY ONE. Answering "no work" would be a confident
// wrong answer, and the Tower would poll a broken broker forever while its Stations idled.
func TestABrokenAttemptStoreIsReportedRatherThanReadAsIdle(t *testing.T) {
	shortPolls(t)
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateActive))
	b.tower.dispatch = dispatch.NewWithStore(dispatch.Config{Network: link.PublicNetwork},
		brokenAttemptStore{})

	code, raw := lt.call(t, srv, "/tower/dispatch", []byte(`{"tower_id":"`+lt.id+`"}`), nil)
	require.Equal(t, http.StatusServiceUnavailable, code, raw)
	require.Contains(t, raw, "could not read pending work")
}

// brokenAttemptStore fails the read the poll depends on.
type brokenAttemptStore struct{ dispatch.Store }

func (brokenAttemptStore) ClaimNext(string, time.Time) (dispatch.Record, bool, error) {
	return dispatch.Record{}, false, errors.New("the attempt store is unreachable")
}

// An idle Tower waits out its poll and is told there is nothing, rather than being left
// hanging or handed an empty job.
func TestAnIdleTowerIsToldThereIsNothing(t *testing.T) {
	shortPolls(t)
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt, _ := dispatchable(t, b, srv, op)

	start := time.Now()
	code, raw := lt.call(t, srv, "/tower/dispatch", []byte(`{"tower_id":"`+lt.id+`"}`), nil)
	require.Equal(t, http.StatusNoContent, code, raw)
	require.GreaterOrEqual(t, time.Since(start), dispatchPollWait,
		"it waits for work rather than returning immediately and re-polling in a loop")
}

// A Station that never answers costs the caller its deadline and no more. Without the
// timeout the relay would hold the connection until the client gave up, which looks to a
// consumer exactly like the broker having hung.
func TestAStationThatNeverAnswersTimesOut(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	_, _ = dispatchable(t, b, srv, op)

	was := towerAttemptLifetimeForTest(t, 200*time.Millisecond)
	defer was()

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	require.True(t, b.tryTowerDispatch(rec, r, "roger-1", []byte(`{"model":"roger-1"}`), false))
	require.Equal(t, http.StatusGatewayTimeout, rec.Code)
	require.Contains(t, rec.Body.String(), "did not answer in time")
}

// A caller that goes away mid-flight is not an error and produces no write to a dead
// connection.
func TestACallerThatGivesUpIsNotAnError(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	_, _ = dispatchable(t, b, srv, op)

	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}")).WithContext(ctx)
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()

	require.True(t, b.tryTowerDispatch(rec, r, "roger-1", []byte(`{"model":"roger-1"}`), false))
	// A recorder reports 200 whether or not anything was written, so the BODY is what says
	// so: nothing was sent to a connection that had already gone.
	require.Zero(t, rec.Body.Len(), "nothing was written to a connection that had gone")
	require.Empty(t, rec.Header().Get("X-RogerAI-Origin"))
}

// A Station whose attachment is not live is not a candidate, however routable its offer
// looks: dispatching to one whose recorded key we cannot read means accepting a receipt we
// have no way to check.
func TestARevokedStationStopsBeingACandidate(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	_, stn := dispatchable(t, b, srv, op)

	_, ok := b.pickTowerStation("roger-1")
	require.True(t, ok)

	_, err := b.tower.stations.Revoke(stn.StationID)
	require.NoError(t, err)
	_, ok = b.pickTowerStation("roger-1")
	require.False(t, ok, "a revoked Station must not be dispatched to")
}

// towerAttemptLifetimeForTest shortens the attempt deadline and returns the restore.
func towerAttemptLifetimeForTest(t *testing.T, d time.Duration) func() {
	t.Helper()
	was := towerAttemptLifetime
	towerAttemptLifetime = d
	return func() { towerAttemptLifetime = was }
}

// --- the attempt ledger, driven by real dispatch ----------------------------
//
// The ledger is the record money will be decided from, so what matters is not that it can
// hold a chain but that the REAL dispatch path writes the right one. These drive actual
// requests and then read the history back.

// A served request leaves a complete, terminal history: issued, leased, evidence_complete,
// settled - each binding the one before it.
func TestAServedRequestLeavesASettledAttemptHistory(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt, stn := dispatchable(t, b, srv, op)

	model := &stubModel{body: []byte(`{"content":"hello"}`)}
	exec := station.Executor{
		Station: stn, CoreKey: b.tower.dispatchPub, Network: link.PublicNetwork, Upstream: model,
	}
	var attemptID string
	done := make(chan struct{})
	go func() {
		defer close(done)
		work := pollForWork(t, srv, lt)
		attemptID = work.AttemptID
		resp := exec.Execute(context.Background(), station.ExecuteRequest{
			Grant: work.Grant, Request: work.Request,
		})
		require.Empty(t, resp.Failure)
		returnResult(t, srv, lt, work.AttemptID, resp)
	}()

	request := []byte(`{"model":"roger-1"}`)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(request)))
	require.True(t, b.tryTowerDispatch(rec, r, "roger-1", request, false))
	<-done
	require.Equal(t, http.StatusOK, rec.Code)

	state, revision, ok, err := b.tower.attempts.State(attemptID)
	require.NoError(t, err)
	require.True(t, ok, "the attempt was recorded")
	require.Equal(t, attempt.StateSettled, state)
	require.Equal(t, int64(4), revision,
		"issued, leased, evidence_complete, settled - four links, no shortcuts")
	require.True(t, attempt.Terminal(state))
}

// A Station that fails leaves a FAILED attempt, and the hold is released rather than
// captured. Nothing about a failed attempt may look settleable afterwards.
func TestAFailedRequestLeavesAFailedAttempt(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt, _ := dispatchable(t, b, srv, op)

	var attemptID string
	done := make(chan struct{})
	go func() {
		defer close(done)
		work := pollForWork(t, srv, lt)
		attemptID = work.AttemptID
		returnResult(t, srv, lt, work.AttemptID,
			station.ExecuteResponse{Failure: "the model is not loaded"})
	}()

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	require.True(t, b.tryTowerDispatch(rec, r, "roger-1", []byte(`{"model":"roger-1"}`), false))
	<-done

	state, _, ok, err := b.tower.attempts.State(attemptID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, attempt.StateFailed, state)

	// And it cannot then be settled - a terminal attempt is not revivable by anyone.
	_, err = b.tower.attempts.Commit(attemptID, attempt.Observation{
		Kind: attempt.KindSettlementCommitted, EvidenceHash: "e", Reason: "sneaky",
	})
	require.ErrorIs(t, err, attempt.ErrTerminal)
}

// THE GRANT IS NOT TRANSMITTED BEFORE THE ATTEMPT COMMITS. "the lease or grant cannot be
// transmitted before that commit" - an attempt nobody recorded is work whose outcome cannot
// be established afterwards, so a ledger that refuses means nothing is dispatched.
func TestNothingIsDispatchedIfTheAttemptCannotBeRecorded(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt, _ := dispatchable(t, b, srv, op)
	b.tower.attempts = attempt.New(attempt.Config{
		Network: link.PublicNetwork, Signer: b.tower.attemptKey,
	}, brokenAttemptChain{})

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	require.False(t, b.tryTowerDispatch(rec, r, "roger-1", []byte(`{"model":"roger-1"}`), false),
		"no attempt, no dispatch")

	// And the Tower is offered nothing, so the Station never sees work Core did not record.
	shortPolls(t)
	code, raw := lt.call(t, srv, "/tower/dispatch", []byte(`{"tower_id":"`+lt.id+`"}`), nil)
	require.Equal(t, http.StatusNoContent, code, raw)
}

// brokenAttemptChain refuses to record anything.
type brokenAttemptChain struct{ attempt.Store }

func (brokenAttemptChain) Append(attempt.Record, int64) error {
	return errors.New("the attempt ledger is unreachable")
}
func (brokenAttemptChain) Head(string) (attempt.Record, bool, error) {
	return attempt.Record{}, false, errors.New("the attempt ledger is unreachable")
}

// The attempt-state signer is a DIFFERENT key from the dispatch signer. A compromise of the
// one that signs authorizations must not be able to forge the record money is decided from.
func TestTheAttemptSignerIsNotTheGrantSigner(t *testing.T) {
	b, _ := towerTestBroker(t)
	attemptPub := b.tower.attemptKey.Public().(ed25519.PublicKey)
	require.NotEqual(t, []byte(b.tower.dispatchPub), []byte(attemptPub),
		"attempt state and dispatch authorization must not share a key")

	// Both are still stable across restarts, which is what makes a chain verifiable later.
	again, err := deriveAttemptKey(b.tower.ca)
	require.NoError(t, err)
	require.Equal(t, []byte(attemptPub), []byte(again.Public().(ed25519.PublicKey)))
}
