package main

// dispatch_bdd_test.go makes features/routing/dispatch.feature EXECUTABLE, driving the REAL
// multi-instance relay dispatch path + its telemetry counters (instmetrics.go) over a real
// in-process Valkey (miniredis): a single-instance broker dispatches LOCALLY; a job whose
// poller is on the PEER instance crosses the rendezvous bus (busDispatch); a job with no
// poller anywhere is a clean 503 (busNoPoller); a dead bus fails cleanly and counts a
// distinct error (busDispatchErr); and the counters + instance_id surface read-only on
// /admin/live ONLY in multi-instance mode. It reuses the proven multiinstance_test.go
// harness (newMIBroker / miRegisterNode / pollOnce / miSigned*), so the scenarios exercise
// the same real relay/poll/result path as production - no mocks.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type dispatchState struct {
	t        *testing.T
	a        *broker // the originating instance under test
	wg       *sync.WaitGroup
	w        *httptest.ResponseRecorder // last relay response
	health   map[string]any             // last /admin/live health blob
	userPriv ed25519.PrivateKey         // the paying user (dead-bus scenario needs a balance)
}

func (s *dispatchState) reset(t *testing.T) {
	s.t = t
	s.a = nil
	s.wg = &sync.WaitGroup{}
	s.w = nil
	s.health = nil
	s.userPriv = nil
}

// startPoller attaches a provider that long-polls instance b for node, serves the one
// dispatched job with a signed 200 result, and exits. The 150ms settle lets the poll/bus
// subscription register before the relay dispatches (mirrors the instmetrics tests).
func (s *dispatchState) startPoller(b *broker, node, token, model string, nodePriv ed25519.PrivateKey) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		job, ok := pollOnce(s.t, b, node, token)
		if !ok {
			return
		}
		res := miSignedResult(job.ID, node, model, "hi", nodePriv, 200)
		rb, _ := json.Marshal(res)
		rr := httptest.NewRequest(http.MethodPost, "/agent/result?node="+node, bytes.NewReader(rb))
		rr.Header.Set("Authorization", miNodeBearer(token))
		b.agentResult(httptest.NewRecorder(), rr)
	}()
	time.Sleep(150 * time.Millisecond)
}

func (s *dispatchState) relayFree() {
	_, userPriv, _ := ed25519.GenerateKey(nil)
	r := miSignedRelayReq(s.t, userPriv, []byte(`{"model":"free-m","max_tokens":8}`), nil)
	s.w = httptest.NewRecorder()
	s.a.relay(s.w, r)
	s.wg.Wait()
}

// --- Given -----------------------------------------------------------------

func (s *dispatchState) singleInstanceWithPoller(node string) error {
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	s.a = newMIBroker(s.t, brokerPriv, store.NewMem(), nil) // mr==nil => single-instance fast path
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	token := "tok-" + node
	miRegisterNode(s.a, node, hex.EncodeToString(nodePub), token, []protocol.ModelOffer{{Model: "free-m"}})
	s.startPoller(s.a, node, token, "free-m", nodePriv)
	return nil
}

func (s *dispatchState) instanceAandBPoller(node string) error {
	mr := miniredis.RunT(s.t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	s.a = newMIBroker(s.t, brokerPriv, db, mr)
	bInst := newMIBroker(s.t, brokerPriv, db, mr)
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	token, pubHex := "tok-"+node, hex.EncodeToString(nodePub)
	miRegisterNode(s.a, node, pubHex, token, []protocol.ModelOffer{{Model: "free-m"}})
	miRegisterNode(bInst, node, pubHex, token, []protocol.ModelOffer{{Model: "free-m"}})
	s.startPoller(bInst, node, token, "free-m", nodePriv) // poller ONLY on the peer instance
	return nil
}

func (s *dispatchState) multiNoPoller(node string) error {
	mr := miniredis.RunT(s.t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	s.a = newMIBroker(s.t, brokerPriv, store.NewMem(), mr)
	nodePub, _, _ := ed25519.GenerateKey(nil)
	miRegisterNode(s.a, node, hex.EncodeToString(nodePub), "tok", []protocol.ModelOffer{{Model: "free-m"}})
	return nil // no poller attached on any instance
}

func (s *dispatchState) deadBus() error {
	mr := miniredis.RunT(s.t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	s.a = newMIBroker(s.t, brokerPriv, db, mr)
	_, userPriv, _ := ed25519.GenerateKey(nil)
	s.userPriv = userPriv
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "octocat", Pubkey: userPubHex})
	_, _ = db.BalanceOf("u_gh_7", 50)
	nodePub, _, _ := ed25519.GenerateKey(nil)
	miRegisterNode(s.a, "n1", hex.EncodeToString(nodePub), "tok",
		[]protocol.ModelOffer{{Model: "paid-m", PriceOut: 0.5}})
	mr.Close() // kill the rendezvous bus
	return nil
}

func (s *dispatchState) multiHandledDispatches() error {
	mr := miniredis.RunT(s.t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	s.a = newMIBroker(s.t, brokerPriv, store.NewMem(), mr)
	s.a.stats.busDispatch.Add(3) // it has handled some dispatches
	s.a.adminKey = "admin-secret-key"
	return nil
}

func (s *dispatchState) singleInstanceBroker() error {
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	s.a = newMIBroker(s.t, brokerPriv, store.NewMem(), nil)
	s.a.adminKey = "admin-secret-key"
	return nil
}

// --- When ------------------------------------------------------------------

func (s *dispatchState) relayDispatches(node string) error { s.relayFree(); return nil }

func (s *dispatchState) relayCrossInstanceRuns() error {
	r := miSignedRelayReq(s.t, s.userPriv, []byte(`{"model":"paid-m","max_tokens":8}`), nil)
	s.w = httptest.NewRecorder()
	s.a.relay(s.w, r)
	return nil
}

func (s *dispatchState) readAdminLive() error {
	r := httptest.NewRequest(http.MethodGet, "/admin/live", nil)
	r.Header.Set("X-Roger-Admin", s.a.adminKey)
	w := httptest.NewRecorder()
	s.a.adminLive(w, r)
	if w.Code != http.StatusOK {
		return fmt.Errorf("adminLive = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Health map[string]any `json:"health"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		return fmt.Errorf("decode /admin/live: %w", err)
	}
	s.health = resp.Health
	return nil
}

// --- Then ------------------------------------------------------------------

func (s *dispatchState) localDispatchIncr() error {
	if got := s.a.stats.localDispatch.Load(); got != 1 {
		return fmt.Errorf("localDispatch = %d, want 1", got)
	}
	return nil
}

func (s *dispatchState) noBusCounterMoves() error {
	if got := s.a.stats.busDispatch.Load() + s.a.stats.busNoPoller.Load() + s.a.stats.busDispatchErr.Load(); got != 0 {
		return fmt.Errorf("a bus counter moved (sum=%d), want 0 on the single-instance path", got)
	}
	return nil
}

func (s *dispatchState) busDispatchIncr() error {
	if got := s.a.stats.busDispatch.Load(); got != 1 {
		return fmt.Errorf("busDispatch = %d, want 1 on instance A", got)
	}
	return nil
}

func (s *dispatchState) localDispatchStaysZero() error {
	if got := s.a.stats.localDispatch.Load(); got != 0 {
		return fmt.Errorf("localDispatch = %d, want 0 (multi-instance dispatches only over the bus)", got)
	}
	return nil
}

func (s *dispatchState) noPollerErrStayZero() error {
	if got := s.a.stats.busNoPoller.Load() + s.a.stats.busDispatchErr.Load(); got != 0 {
		return fmt.Errorf("no-poller/bus-error counters = %d, want 0 on a successful dispatch", got)
	}
	return nil
}

func (s *dispatchState) busNoPollerIncr() error {
	if got := s.a.stats.busNoPoller.Load(); got != 1 {
		return fmt.Errorf("busNoPoller = %d, want 1", got)
	}
	return nil
}

func (s *dispatchState) busDispatchStaysZero() error {
	if got := s.a.stats.busDispatch.Load(); got != 0 {
		return fmt.Errorf("busDispatch = %d, want 0 (no poller delivered the job)", got)
	}
	return nil
}

func (s *dispatchState) relay503() error {
	if s.w.Code != http.StatusServiceUnavailable {
		return fmt.Errorf("relay = %d, want 503 Service Unavailable", s.w.Code)
	}
	return nil
}

func (s *dispatchState) busDispatchErrIncr() error {
	if got := s.a.stats.busDispatchErr.Load(); got < 1 {
		return fmt.Errorf("busDispatchErr = %d, want >= 1 on a dead bus", got)
	}
	return nil
}

func (s *dispatchState) healthHasDispatchSnapshot() error {
	disp, ok := s.health["dispatch"].(map[string]any)
	if !ok {
		return fmt.Errorf("health.dispatch missing or not a map: %v", s.health["dispatch"])
	}
	for _, k := range []string{"local_dispatch", "bus_dispatch", "bus_no_poller", "bus_dispatch_err"} {
		if _, ok := disp[k]; !ok {
			return fmt.Errorf("dispatch snapshot missing key %q", k)
		}
	}
	return nil
}

func (s *dispatchState) healthHasInstanceID() error {
	id, _ := s.health["instance_id"].(string)
	if id == "" || id != s.a.instanceID {
		return fmt.Errorf("health.instance_id = %v, want %q", s.health["instance_id"], s.a.instanceID)
	}
	return nil
}

func (s *dispatchState) healthOmitsBothKeys() error {
	if _, present := s.health["instance_id"]; present {
		return fmt.Errorf("single-instance /admin/live must omit instance_id, got %v", s.health["instance_id"])
	}
	if _, present := s.health["dispatch"]; present {
		return fmt.Errorf("single-instance /admin/live must omit dispatch, got %v", s.health["dispatch"])
	}
	return nil
}

func TestRoutingDispatchBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &dispatchState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset(t)
				return ctx, nil
			})
			// Given
			sc.Step(`^a single-instance broker with a poller attached to node "([^"]*)"$`, st.singleInstanceWithPoller)
			sc.Step(`^instance A holds the request and instance B has the poller for node "([^"]*)"$`, st.instanceAandBPoller)
			sc.Step(`^a multi-instance broker with NO poller attached to node "([^"]*)" on any instance$`, st.multiNoPoller)
			sc.Step(`^a multi-instance broker whose rendezvous bus \(Valkey\) is down$`, st.deadBus)
			sc.Step(`^a multi-instance broker that has handled some dispatches$`, st.multiHandledDispatches)
			sc.Step(`^a single-instance broker$`, st.singleInstanceBroker)
			// When
			sc.Step(`^a relay for "([^"]*)" dispatches$`, st.relayDispatches)
			sc.Step(`^instance A dispatches the relay for "([^"]*)"$`, st.relayDispatches)
			sc.Step(`^a relay that needs cross-instance dispatch runs$`, st.relayCrossInstanceRuns)
			sc.Step(`^the founder reads GET /admin/live$`, st.readAdminLive)
			// Then
			sc.Step(`^localDispatch increments by 1$`, st.localDispatchIncr)
			sc.Step(`^no bus counter \(busDispatch/busNoPoller/busDispatchErr\) moves$`, st.noBusCounterMoves)
			sc.Step(`^busDispatch increments by 1 on instance A$`, st.busDispatchIncr)
			sc.Step(`^localDispatch stays 0 on instance A$`, st.localDispatchStaysZero)
			sc.Step(`^the no-poller / bus-error counters stay 0$`, st.noPollerErrStayZero)
			sc.Step(`^busNoPoller increments by 1$`, st.busNoPollerIncr)
			sc.Step(`^busDispatch stays 0$`, st.busDispatchStaysZero)
			sc.Step(`^the relay responds 503 Service Unavailable$`, st.relay503)
			sc.Step(`^busDispatchErr increments by at least 1$`, st.busDispatchErrIncr)
			sc.Step(`^the relay responds 503 \(never a hang or a silent drop\)$`, st.relay503)
			sc.Step(`^health\.dispatch carries the local/bus/no-poller/bus-error snapshot$`, st.healthHasDispatchSnapshot)
			sc.Step(`^health\.instance_id identifies which instance answered$`, st.healthHasInstanceID)
			sc.Step(`^health has neither instance_id nor a dispatch snapshot$`, st.healthOmitsBothKeys)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/routing/dispatch.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("routing/dispatch behavior scenarios failed (see godog output above)")
	}
}
