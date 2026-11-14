package main

// tower_trust_ramp_bdd_test.go makes features/tower/tower_trust_ramp.feature EXECUTABLE.
//
// Every scenario runs against a fresh broker over the real Tower subsystem (towerTestBroker:
// the production route table, in-process stores). Steps drive the production code: the hub
// check (towerHubCheckOnce against real towerhub servers, a real TLS listener for the pin),
// the governance sweep (towerGovSweepOnce -> sweepStage), the reader (towerStage,
// stageReport), the admin stage routes over HTTP, placement through the real attach route,
// the mobility pass, the per-request coin (edgeSideWeight, edgeCoinWeighted, mergeEdgePlan)
// and the edge settlement (captureEdgeCharge).
//
// Time: the 7- and 30-day scenarios use real recorded events and real health records with
// backdated timestamps, exactly as the unit tests in towerstage_test.go do. No clock inside
// production code is stubbed.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/towercore/admit"
	"rogerai.fm/roger/v7/internal/towercore/fleet"
	"rogerai.fm/roger/v7/internal/towercore/health"
	"rogerai.fm/roger/v7/internal/towercore/link"
	"rogerai.fm/roger/v7/internal/towercore/placement"
	"rogerai.fm/roger/v7/internal/towercore/reputation"
	"rogerai.fm/roger/v7/internal/towerhub"
)

// rampHubReq is one request a recording hub saw.
type rampHubReq struct {
	method, path, nonce string
	tls, credential     bool
	keyHeader           string
}

// rampHub records every request that reaches a hub, then lets the real towerhub server answer.
type rampHub struct {
	mu   sync.Mutex
	reqs []rampHubReq
}

func (h *rampHub) seen() []rampHubReq {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]rampHubReq(nil), h.reqs...)
}

type rampState struct {
	t      *testing.T
	b      *broker
	srv    *httptest.Server
	tw     linkTower // the Tower the scenario is about
	op     operator  // its operator
	stage0 placement.Stage
	hub    *rampHub
	extra  []linkTower

	// what a When observed
	elapsed     time.Duration
	readerStage placement.Stage
	causeKind   string
	causeReason string
	stageBefore int // trust_stage events before the When
	codes       []int
	nodes       []string // nudge-capable nodes attached to tw, oldest first
	stations    []string // their Station ids, oldest first
	placedOn    string
	placeCode   int
	passAt      time.Time
	houseNodes  []string
	snap        rampMoneySnap
	shares      map[placement.Stage][3]float64
	coinCalls   int
	streakSum   health.Summary
	firstCanary time.Time
	sweptAt     time.Time
}

// rampMoneySnap is everything a hub check could have touched on the money and work paths.
type rampMoneySnap struct {
	pending       int
	towerTraffic  int
	towerEarn     float64
	towerSplit    float64
	stationEarn   float64
	ownerLedger   int
	stationLedger int
	nudges        int
	cancels       int
}

func (s *rampState) rampReset() {
	s.b, s.srv = towerTestBroker(s.t)
	s.b.adminKey = "admin-secret"
	s.tw, s.op, s.stage0, s.hub, s.extra = linkTower{}, operator{}, "", nil, nil
	s.elapsed, s.readerStage, s.causeKind, s.causeReason, s.stageBefore = 0, "", "", "", 0
	s.codes, s.nodes, s.stations, s.placedOn, s.placeCode = nil, nil, nil, "", 0
	s.passAt, s.houseNodes, s.snap, s.shares, s.coinCalls = time.Time{}, nil, rampMoneySnap{}, nil, 0
}

// --- fixtures ---------------------------------------------------------------------------

// rampOpen opens tw's link session advertising endpoint (and pin), and returns the session id.
func (s *rampState) rampOpen(lt linkTower, endpoint, pin string) (string, error) {
	var acc link.Accepted
	code, raw := lt.call(s.t, s.srv, "/tower/session", jsonOf(s.t, link.Hello{
		Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
		Capabilities: mandatoryCaps(), RelayEndpoint: endpoint, RelayTLSSPKI: pin,
	}), &acc)
	if code != http.StatusOK {
		return "", fmt.Errorf("session open: %d %s", code, raw)
	}
	return acc.SessionID, nil
}

// rampApproved enrolls a Tower for op and approves it (quarantine -> active) at approvedAt.
func (s *rampState) rampApproved(op operator, approvedAt time.Time) (linkTower, error) {
	lt := enrolledTower(s.t, s.b, op.login)
	if err := s.b.tower.registry.TransitionAs(lt.id, admit.StateActive, admit.Event{Kind: evLifecycle,
		Actor: "admin:broker-key", At: approvedAt}); err != nil {
		return lt, err
	}
	return lt, nil
}

// rampLive is a live community Tower of op, approved at `at`, recorded at stage st entered at
// `at` (no stage when st is ""), with its link open on endpoint.
func (s *rampState) rampLive(op operator, endpoint, pin string, st placement.Stage, at time.Time) (linkTower, error) {
	lt, err := s.rampApproved(op, at)
	if err != nil {
		return lt, err
	}
	if st != "" {
		setStageAt(s.t, s.b, lt.id, st, at)
	}
	_, err = s.rampOpen(lt, endpoint, pin)
	return lt, err
}

// rampRealHub mounts a real towerhub server keyed with keyOf's identity, behind a recorder.
func (s *rampState) rampRealHub(keyOf linkTower, towerID string, tlsOn bool) (endpoint, pin string, h *rampHub) {
	hs := towerhub.NewServer(towerhub.New(), nil, towerhub.ServerOptions{TowerID: towerID, EpochKey: keyOf.priv,
		SubmitTTL: time.Second, PollTTL: 50 * time.Millisecond})
	h = &rampHub{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cred := r.Header.Get("Authorization") != "" || r.Header.Get(protocol.HeaderPubkey) != "" ||
			r.Header.Get(protocol.HeaderSig) != ""
		if r.URL.Path == towerhub.PathPoll {
			hs.Poll(w, r)
		} else {
			http.NotFound(w, r)
		}
		h.mu.Lock()
		h.reqs = append(h.reqs, rampHubReq{method: r.Method, path: r.URL.Path, nonce: r.URL.Query().Get("nonce"),
			tls: r.TLS != nil, credential: cred, keyHeader: w.Header().Get(towerhub.HubKeyHeader)})
		h.mu.Unlock()
	})
	var srv *httptest.Server
	if tlsOn {
		srv = httptest.NewTLSServer(handler)
		pin = towerhub.CertPin(srv.Certificate())
	} else {
		srv = httptest.NewServer(handler)
	}
	s.t.Cleanup(srv.Close)
	return srv.Listener.Addr().String(), pin, h
}

// rampFakeHub is a hub that answers with whatever h writes.
func (s *rampState) rampFakeHub(h http.HandlerFunc) string {
	srv := httptest.NewServer(h)
	s.t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

// rampAttachNudge self-attaches one Station for a fresh node that declared the nudge, and
// returns the node id, the Station id and the Tower it landed on.
func (s *rampState) rampAttachNudge(login string, house bool) (string, string, string, int, string) {
	node := signedInOperator(s.t, s.b, login)
	if house {
		makeHouse(s.t, s.b, login)
	}
	body, _ := selfAttachBodyFor(s.t, s.b, node)
	body["nudge"] = true
	var out struct {
		StationID string `json:"station_id"`
		TowerID   string `json:"tower_id"`
	}
	code, raw := node.attach(s.t, s.srv, body, &out)
	nodeID, _ := body["node_id"].(string)
	if code == http.StatusOK {
		s.b.noteCancelCapable(nodeID)
	}
	return nodeID, out.StationID, out.TowerID, code, raw
}

// rampAttachMany attaches n nudge-capable community Stations; every one must land on want.
func (s *rampState) rampAttachMany(n int, want string) error {
	for i := 0; i < n; i++ {
		nodeID, st, got, code, raw := s.rampAttachNudge("ramp-node-"+randHex(5), false)
		if code != http.StatusOK || got != want {
			return fmt.Errorf("attach %d: %d on %q, want %q: %s", i+1, code, got, want, raw)
		}
		s.nodes, s.stations = append(s.nodes, nodeID), append(s.stations, st)
		time.Sleep(2 * time.Millisecond) // distinct attach times, so "newest" is well defined
	}
	return nil
}

func (s *rampState) rampHealth(id string, since time.Time) (int, int) {
	sum, err := s.b.tower.health.Summary(id, since)
	if err != nil {
		return -1, -1
	}
	return sum.Passed, sum.Failed
}

func (s *rampState) rampStageEvents(id string) []admit.Event { return stageEvents(s.t, s.b, id) }

func (s *rampState) rampLiveStations(id string) (int, error) {
	atts, err := s.b.tower.stationStore.ByTower(id)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, at := range atts {
		if at.Live() {
			n++
		}
	}
	return n, nil
}

func rampKeyHash(lt linkTower) string {
	sum := sha256.Sum256(lt.priv.Public().(ed25519.PublicKey))
	return hex.EncodeToString(sum[:])
}

// --- the hub check ------------------------------------------------------------------------

func (s *rampState) liveTowerWithHubNoStations() error {
	s.op = signedInOperator(s.t, s.b, "ramp-octo")
	lt, err := s.rampApproved(s.op, time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	s.tw = lt
	endpoint, pin, h := s.rampRealHub(lt, lt.id, true)
	s.hub = h
	if _, err := s.rampOpen(lt, endpoint, pin); err != nil {
		return err
	}
	if n, err := s.rampLiveStations(lt.id); err != nil || n != 0 {
		return fmt.Errorf("fixture: the Tower holds %d Stations (%v), want none", n, err)
	}
	return nil
}

func (s *rampState) hubCheckRuns() error {
	start := time.Now()
	s.b.towerHubCheckOnce(start)
	s.elapsed = time.Since(start)
	s.passAt = start
	return nil
}

func (s *rampState) hubDialedPinAndProof() error {
	reqs := s.hub.seen()
	if len(reqs) != 1 {
		return fmt.Errorf("the hub saw %d requests, want exactly the one check", len(reqs))
	}
	r := reqs[0]
	if r.method != http.MethodGet || r.path != towerhub.PathPoll {
		return fmt.Errorf("the check was %s %s, want GET %s", r.method, r.path, towerhub.PathPoll)
	}
	if !r.tls {
		return fmt.Errorf("the hub advertises a pin, but the check did not dial it over TLS")
	}
	if r.nonce == "" {
		return fmt.Errorf("the check carried no nonce")
	}
	// The proof answered is the admitted Tower key's.
	key, err := hex.DecodeString(r.keyHeader)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("the hub answered no identity key: %q", r.keyHeader)
	}
	sum := sha256.Sum256(key)
	if hex.EncodeToString(sum[:]) != mustTower(s.t, s.b, s.tw.id).KeyHash {
		return fmt.Errorf("the hub's key is not the admitted key")
	}
	if p, f := s.rampHealth(s.tw.id, time.Now().Add(-time.Hour)); p != 1 || f != 0 {
		return fmt.Errorf("verified proof: %d passed %d failed, want one pass", p, f)
	}
	// A fresh nonce every time: the next check carries a different one.
	s.b.towerHubCheckOnce(s.passAt.Add(canaryInterval))
	reqs = s.hub.seen()
	if len(reqs) != 2 || reqs[1].nonce == "" || reqs[1].nonce == reqs[0].nonce {
		return fmt.Errorf("the second check did not carry a fresh nonce: %+v", reqs)
	}
	// (A pin that does not match is the pin_mismatch row of the outline below.)
	return nil
}

func (s *rampState) hubOutcomeWithRTT() error {
	p, f := s.rampHealth(s.tw.id, time.Now().Add(-time.Hour))
	if p < 1 || f != 0 {
		return fmt.Errorf("no pass outcome was recorded (%d passed, %d failed)", p, f)
	}
	// The latest check is recorded as a hub check, with the round trip it measured.
	sum, err := s.b.tower.health.Summary(s.tw.id, time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	if sum.LastSource != health.SourceHubCheck {
		return fmt.Errorf("the latest outcome is recorded as %q, want %q", sum.LastSource, health.SourceHubCheck)
	}
	if sum.LastRTT <= 0 {
		return fmt.Errorf("no round-trip time was recorded with the outcome (%+v)", sum)
	}
	return nil
}

func (s *rampState) hubPacedAtCanaryInterval() error {
	id := s.tw.id
	base := s.passAt
	before, _ := s.rampHealth(id, time.Now().Add(-time.Hour))
	s.b.hubCheckMu.Lock()
	s.b.hubCheckAt[id] = base
	s.b.hubCheckMu.Unlock()
	n := len(s.hub.seen())
	for _, d := range []time.Duration{time.Second, time.Minute, canaryInterval - time.Second} {
		s.b.towerHubCheckOnce(base.Add(d))
	}
	if got := len(s.hub.seen()); got != n {
		return fmt.Errorf("%d checks inside one canary interval, want none", got-n)
	}
	s.b.towerHubCheckOnce(base.Add(canaryInterval))
	if got := len(s.hub.seen()); got != n+1 {
		return fmt.Errorf("%d checks at the canary interval, want one", got-n)
	}
	after, _ := s.rampHealth(id, time.Now().Add(-time.Hour))
	if after != before+1 {
		return fmt.Errorf("passes went %d -> %d, want one more", before, after)
	}
	if canaryInterval != 5*time.Minute {
		return fmt.Errorf("the canary interval is %v", canaryInterval)
	}
	return nil
}

func (s *rampState) liveTowerWhoseHub(cond string) error {
	s.op = signedInOperator(s.t, s.b, "ramp-octo")
	lt, err := s.rampApproved(s.op, time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	s.tw = lt
	var endpoint, pin string
	switch cond {
	case "does not accept a connection within 5 seconds":
		done := make(chan struct{})
		endpoint = s.rampFakeHub(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-done:
			}
		})
		s.t.Cleanup(func() { close(done) })
	case "presents a certificate that does not match the pin":
		var real string
		endpoint, real, _ = s.rampRealHub(lt, lt.id, true)
		pin = "00" + real[2:]
		if pin == real {
			pin = "11" + real[2:]
		}
	case "answers without an identity proof":
		endpoint = s.rampFakeHub(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	case "signs the nonce with a key other than the admitted one":
		impostor := enrolledTower(s.t, s.b, s.op.login)
		endpoint, _, _ = s.rampRealHub(impostor, lt.id, false)
	case "signs a different nonce", "answers with a malformed proof":
		// The real hub, keyed with the Tower's own identity, answering SOMEONE ELSE's nonce.
		hs := towerhub.NewServer(towerhub.New(), nil, towerhub.ServerOptions{TowerID: lt.id, EpochKey: lt.priv,
			SubmitTTL: time.Second, PollTTL: 50 * time.Millisecond})
		captured := httptest.NewRecorder()
		hs.Poll(captured, httptest.NewRequest(http.MethodGet, towerhub.PathPoll+"?nonce=someone-elses-nonce", nil))
		if captured.Header().Get(towerhub.HubProofHeader) == "" {
			return fmt.Errorf("fixture: the real hub signed nothing")
		}
		malformed := cond == "answers with a malformed proof"
		endpoint = s.rampFakeHub(func(w http.ResponseWriter, r *http.Request) {
			for _, h := range []string{towerhub.HubEpochHeader, towerhub.HubKeyHeader, towerhub.HubProofHeader} {
				w.Header().Set(h, captured.Header().Get(h))
			}
			if malformed {
				w.Header().Set(towerhub.HubProofHeader, "not-hex")
			}
			w.WriteHeader(http.StatusUnauthorized)
		})
	default:
		return fmt.Errorf("unknown hub condition %q", cond)
	}
	_, err = s.rampOpen(lt, endpoint, pin)
	return err
}

func (s *rampState) outcomeIsFail(reason string) error {
	p, f := s.rampHealth(s.tw.id, time.Now().Add(-time.Hour))
	if p != 0 || f != 1 {
		return fmt.Errorf("outcomes: %d passed, %d failed, want one fail", p, f)
	}
	evs := towerEventsOf(s.t, s.b, s.tw.id, evHubCheckFailed)
	if len(evs) != 1 || evs[0].Reason != reason || evs[0].Actor != admit.ActorSystem {
		return fmt.Errorf("hub_check_failed events %+v, want one with reason %q by system", evs, reason)
	}
	if reason == "unreachable" {
		// It waited the full 5 seconds and no longer.
		if hubCheckTimeout != 5*time.Second || s.elapsed < 4500*time.Millisecond || s.elapsed > 8*time.Second {
			return fmt.Errorf("an unanswering hub was given %v (timeout %v), want 5s", s.elapsed, hubCheckTimeout)
		}
		// And a hub whose port refuses outright is unreachable too.
		dead := httptest.NewServer(http.NotFoundHandler())
		addr := dead.Listener.Addr().String()
		dead.Close()
		other := enrolledTower(s.t, s.b, s.op.login)
		if err := s.b.tower.registry.Transition(other.id, admit.StateActive); err != nil {
			return err
		}
		if _, err := s.rampOpen(other, addr, ""); err != nil {
			return err
		}
		s.b.towerHubCheckOnce(time.Now())
		evs := towerEventsOf(s.t, s.b, other.id, evHubCheckFailed)
		if len(evs) != 1 || evs[0].Reason != "unreachable" {
			return fmt.Errorf("a refused port: %+v, want unreachable", evs)
		}
	}
	return nil
}

func (s *rampState) liveTowerNoHub() error {
	s.op = signedInOperator(s.t, s.b, "ramp-octo")
	lt, err := s.rampApproved(s.op, time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	s.tw = lt
	_, err = s.rampOpen(lt, "", "")
	if err != nil {
		return err
	}
	if !s.b.tower.link.Live(lt.id) {
		return fmt.Errorf("fixture: the Tower is not live")
	}
	return nil
}

func (s *rampState) noOutcomeRecorded() error {
	sum, err := s.b.tower.health.Summary(s.tw.id, time.Now().Add(-24*time.Hour))
	if err != nil {
		return err
	}
	if sum.Passed+sum.Failed != 0 || !sum.LastAt.IsZero() {
		return fmt.Errorf("an outcome was recorded for a Tower with no hub: %+v", sum)
	}
	if evs := towerEventsOf(s.t, s.b, s.tw.id, evHubCheckFailed); len(evs) != 0 {
		return fmt.Errorf("a hub_check_failed was recorded for a Tower with no hub")
	}
	return nil
}

// hubChecksRun runs real hub checks, 29 days ago and since, for an honest Tower (passes) and an
// impostor-keyed one (fails), more fails than the hourly budget.
func (s *rampState) hubChecksRun() error {
	s.op = signedInOperator(s.t, s.b, "ramp-octo")
	honest, err := s.rampApproved(s.op, time.Now().Add(-40*24*time.Hour))
	if err != nil {
		return err
	}
	ep, _, _ := s.rampRealHub(honest, honest.id, false)
	if _, err := s.rampOpen(honest, ep, ""); err != nil {
		return err
	}
	bad, err := s.rampApproved(s.op, time.Now().Add(-40*24*time.Hour))
	if err != nil {
		return err
	}
	impostor := enrolledTower(s.t, s.b, s.op.login)
	ep, _, _ = s.rampRealHub(impostor, bad.id, false)
	if _, err := s.rampOpen(bad, ep, ""); err != nil {
		return err
	}
	s.tw, s.extra = honest, []linkTower{bad}
	s.passAt = time.Now().Add(-29 * 24 * time.Hour)
	for i := 0; i < towerEventBudget+5; i++ {
		s.b.towerHubCheckOnce(s.passAt.Add(time.Duration(i) * canaryInterval))
	}
	// The housekeeping sweep that reaps old evidence runs, as it does in production.
	s.b.towerInviteSweepOnce(time.Now())
	return nil
}

func (s *rampState) outcomesCountableOver30Days() error {
	n := towerEventBudget + 5
	since := time.Now().Add(-30 * 24 * time.Hour)
	if p, f := s.rampHealth(s.tw.id, since); p != n || f != 0 {
		return fmt.Errorf("honest Tower over 30 days: %d passed %d failed, want %d passes", p, f, n)
	}
	if p, f := s.rampHealth(s.extra[0].id, since); p != 0 || f != n {
		return fmt.Errorf("impostor Tower over 30 days: %d passed %d failed, want %d fails", p, f, n)
	}
	// What the ramp counts is exactly this store: the report reads the same numbers.
	if healthRetention < 30*24*time.Hour {
		return fmt.Errorf("evidence is kept %v, shorter than the 30 days the ramp counts", healthRetention)
	}
	return nil
}

func (s *rampState) failEventsOnlyPaced() error {
	if evs := towerEventsOf(s.t, s.b, s.tw.id, evHubCheckFailed); len(evs) != 0 {
		return fmt.Errorf("%d hub_check_failed events for a Tower that only passed", len(evs))
	}
	evs := towerEventsOf(s.t, s.b, s.extra[0].id, evHubCheckFailed)
	if len(evs) != towerEventBudget {
		return fmt.Errorf("%d fails recorded %d events, want the per-Tower budget of %d", towerEventBudget+5, len(evs), towerEventBudget)
	}
	for _, ev := range evs {
		if ev.Reason != "wrong_key" || ev.Actor != admit.ActorSystem {
			return fmt.Errorf("unexpected event %+v", ev)
		}
	}
	if !pacedEvent(admit.Event{Kind: evHubCheckFailed, Actor: admit.ActorSystem}) {
		return fmt.Errorf("hub_check_failed is not under the per-Tower budget")
	}
	return nil
}

func (s *rampState) rampMoneyNow(stationNode, stationOwner string) rampMoneySnap {
	ownerKey := ownerPubkeyOf(s.t, s.b, s.op.login)
	var m rampMoneySnap
	m.pending = s.b.tower.dispatch.Pending()
	if tr, err := s.b.tower.earnings.TowerTraffic(s.tw.id, time.Time{}); err == nil {
		m.towerTraffic = tr.Attempts
	}
	m.towerEarn, _ = s.b.db.EarningsOf(towerNode(s.tw.id))
	if sp, err := s.b.db.EarningSplitOf(ownerKey, time.Now()); err == nil {
		m.towerSplit = sp.Held + sp.Reserved + sp.Payable + sp.Paid
	}
	m.stationEarn, _ = s.b.db.EarningsOf(stationNode)
	ol, _ := s.b.db.LedgerOf(ownerKey, nil, 100)
	sl, _ := s.b.db.LedgerOf(stationOwner, nil, 100)
	m.ownerLedger, m.stationLedger = len(ol), len(sl)
	m.nudges = reattachNudges(s.b, stationNode)
	s.b.cancels.mu.Lock()
	m.cancels = len(s.b.cancels.buf[stationNode])
	s.b.cancels.mu.Unlock()
	return m
}

func (s *rampState) aHubCheckPasses() error {
	s.op = signedInOperator(s.t, s.b, "ramp-octo")
	lt, err := s.rampApproved(s.op, time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	setStageAt(s.t, s.b, lt.id, stageEstablished, time.Now().Add(-time.Hour))
	s.tw = lt
	ep, _, h := s.rampRealHub(lt, lt.id, false)
	s.hub = h
	if _, err := s.rampOpen(lt, ep, ""); err != nil {
		return err
	}
	// A Station is behind it, so "no Station is contacted" is a claim about a real one.
	nodeID, st, got, code, raw := s.rampAttachNudge("ramp-station-op", false)
	if code != http.StatusOK || got != lt.id {
		return fmt.Errorf("fixture attach: %d %q: %s", code, got, raw)
	}
	s.nodes, s.stations = []string{nodeID}, []string{st}
	stationOwner := ownerPubkeyOf(s.t, s.b, "ramp-station-op")
	s.snap = s.rampMoneyNow(nodeID, stationOwner)
	before := len(s.hub.seen())
	s.b.towerHubCheckOnce(time.Now())
	if p, f := s.rampHealth(lt.id, time.Now().Add(-time.Hour)); p != 1 || f != 0 {
		return fmt.Errorf("the hub check did not pass: %d passed %d failed", p, f)
	}
	if len(s.hub.seen()) != before+1 {
		return fmt.Errorf("the hub saw %d requests, want one", len(s.hub.seen())-before)
	}
	return nil
}

func (s *rampState) noWorkNoMoney() error {
	reqs := s.hub.seen()
	r := reqs[len(reqs)-1]
	if r.method != http.MethodGet || r.path != towerhub.PathPoll || r.credential {
		return fmt.Errorf("the hub check was %s %s credential=%v, want one uncredentialed GET of the poll route", r.method, r.path, r.credential)
	}
	after := s.rampMoneyNow(s.nodes[0], ownerPubkeyOf(s.t, s.b, "ramp-station-op"))
	if after != s.snap {
		return fmt.Errorf("a hub check moved work or money: before %+v after %+v", s.snap, after)
	}
	if after.pending != 0 || after.towerTraffic != 0 || after.towerEarn != 0 || after.towerSplit != 0 || after.stationEarn != 0 {
		return fmt.Errorf("grants, traffic or earnings exist after only a hub check: %+v", after)
	}
	if after.nudges != 0 || after.cancels != 0 {
		return fmt.Errorf("the Station's node was contacted: %+v", after)
	}
	return nil
}

// --- computing the stage --------------------------------------------------------------------

func (s *rampState) movesQuarantineToActive() error {
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	lt := enrolledTower(s.t, s.b, s.op.login)
	if _, found, _ := s.b.tower.registry.FindTransition(lt.id, admit.StateQuarantine, admit.StateActive); found {
		return fmt.Errorf("fixture: already approved once")
	}
	if err := s.b.tower.registry.TransitionAs(lt.id, admit.StateActive, admit.Event{Kind: evLifecycle,
		Actor: "admin:broker-key", At: time.Now().Add(-time.Minute)}); err != nil {
		return err
	}
	s.tw = lt
	return nil
}

func (s *rampState) sweepComputes() error {
	s.stageBefore = len(s.rampStageEvents(s.tw.id))
	s.b.towerGovSweepOnce(time.Now())
	return nil
}

func (s *rampState) stageIs(want string) error {
	if got := s.b.towerStage(s.tw.id); string(got) != want {
		return fmt.Errorf("stage %q, want %q", got, want)
	}
	if rep := s.b.stageReport(mustTower(s.t, s.b, s.tw.id), time.Now()); rep == nil || rep["stage"] != want {
		return fmt.Errorf("the stage report says %v, want %q", rep, want)
	}
	return nil
}

func (s *rampState) firstStageEvent() error {
	evs := s.rampStageEvents(s.tw.id)
	if len(evs) != 1 {
		return fmt.Errorf("%d trust_stage events, want 1", len(evs))
	}
	ev := evs[0]
	if ev.From != "" || ev.To != "new" || ev.Actor != admit.ActorSystem {
		return fmt.Errorf("event from %q to %q actor %q", ev.From, ev.To, ev.Actor)
	}
	s.b.towerGovSweepOnce(time.Now())
	if n := len(s.rampStageEvents(s.tw.id)); n != 1 {
		return fmt.Errorf("an unchanged stage wrote again: %d events", n)
	}
	return nil
}

func (s *rampState) rampRecordChecks(id string, from time.Time, pass, fail int, failEvery int) {
	i, f := 0, 0
	for k := 0; k < pass+fail; k++ {
		ok := true
		if f < fail && failEvery > 0 && k%failEvery == failEvery-1 {
			ok = false
			f++
		} else if i >= pass {
			ok = false
			f++
		} else {
			i++
		}
		s.b.recordHealthResult(id, ok, from.Add(time.Duration(k)*time.Minute))
	}
}

func (s *rampState) inNew7Days50At96() error {
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	entered := time.Now().Add(-7*24*time.Hour - time.Minute)
	lt, err := s.rampApproved(s.op, entered)
	if err != nil {
		return err
	}
	setStageAt(s.t, s.b, lt.id, stageNew, entered)
	s.tw = lt
	s.rampRecordChecks(lt.id, entered.Add(time.Hour), 50, 2, 20) // 50 passed, 2 failed apart: 96.2%
	if p, f := s.rampHealth(lt.id, entered); p != 50 || f != 2 {
		return fmt.Errorf("fixture: %d/%d", p, f)
	}
	if len(towerEventsOf(s.t, s.b, lt.id, evHealthFailing)) != 0 {
		return fmt.Errorf("fixture: the fails formed a streak")
	}
	return nil
}

func (s *rampState) stageBecomes(want string) error {
	if got := s.b.towerStage(s.tw.id); string(got) != want {
		return fmt.Errorf("stage %q, want %q", got, want)
	}
	evs := s.rampStageEvents(s.tw.id)
	if len(evs) <= s.stageBefore || string(evs[0].To) != want {
		return fmt.Errorf("no trust_stage event to %q was written (%d events, newest %+v)", want, len(evs), evs)
	}
	return nil
}

func (s *rampState) evidenceEvent() error {
	ev := s.rampStageEvents(s.tw.id)[0]
	if ev.Reason != "promoted" || ev.Actor != admit.ActorSystem {
		return fmt.Errorf("event %+v", ev)
	}
	if fmt.Sprint(ev.Detail["days"]) != "7" || fmt.Sprint(ev.Detail["passed"]) != "50" || ev.Detail["pass_rate"] != "0.962" {
		return fmt.Errorf("evidence days=%v passed=%v pass_rate=%v, want 7, 50, 0.962", ev.Detail["days"], ev.Detail["passed"], ev.Detail["pass_rate"])
	}
	return nil
}

func (s *rampState) inNewWithShortfall(short string) error {
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	now := time.Now()
	entered := now.Add(-8 * 24 * time.Hour)
	if short == "6 days and 23 hours in new" {
		entered = now.Add(-(6*24 + 23) * time.Hour)
	}
	lt, err := s.rampApproved(s.op, entered)
	if err != nil {
		return err
	}
	setStageAt(s.t, s.b, lt.id, stageNew, entered)
	s.tw = lt
	pass, fail, every := 60, 0, 0
	switch short {
	case "49 passed health checks":
		pass = 49
	case "a 94% health-check pass rate":
		pass, fail, every = 94, 6, 16
	}
	s.rampRecordChecks(lt.id, entered.Add(time.Hour), pass, fail, every)
	reg := s.b.tower.registry
	switch short {
	case "6 days and 23 hours in new", "49 passed health checks", "a 94% health-check pass rate":
	case "a suspension 3 days ago":
		at := now.Add(-3 * 24 * time.Hour)
		for i, st := range []admit.State{admit.StateSuspended, admit.StateQuarantine, admit.StateActive} {
			if err := reg.TransitionAs(lt.id, st, admit.Event{Kind: evLifecycle, Actor: "admin:broker-key",
				At: at.Add(time.Duration(i) * time.Hour)}); err != nil {
				return err
			}
		}
	case "a lease lapse of 2 hours 5 days ago":
		at := now.Add(-5 * 24 * time.Hour)
		if err := reg.Record(admit.Event{TowerID: lt.id, Kind: evLeaseLapsed, Actor: admit.ActorSystem, At: at}); err != nil {
			return err
		}
		if err := reg.Record(admit.Event{TowerID: lt.id, Kind: evCertRenewed, Actor: admit.ActorTower, At: at.Add(2 * time.Hour)}); err != nil {
			return err
		}
	case "a health-check fail streak of 2 yesterday":
		y := now.Add(-24 * time.Hour)
		s.b.recordHealthResult(lt.id, false, y)
		s.b.recordHealthResult(lt.id, false, y.Add(time.Minute))
		if len(towerEventsOf(s.t, s.b, lt.id, evHealthFailing)) != 1 {
			return fmt.Errorf("fixture: the streak was not recorded")
		}
	case "an admin-recorded incident 6 days ago":
		if err := reg.Record(admit.Event{TowerID: lt.id, Kind: evIncident, Actor: "admin:broker-key",
			Reason: "served stale weights", At: now.Add(-6 * 24 * time.Hour)}); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown shortfall %q", short)
	}
	s.b.dropStageCache(admit.Event{TowerID: lt.id, Kind: evTrustStage})
	return nil
}

func (s *rampState) stageStays(want string) error {
	if got := s.b.towerStage(s.tw.id); string(got) != want {
		return fmt.Errorf("stage %q, want %q", got, want)
	}
	for _, ev := range s.rampStageEvents(s.tw.id) {
		if string(ev.To) != want {
			return fmt.Errorf("a trust_stage event to %q was written", ev.To)
		}
	}
	return nil
}

func (s *rampState) proving30DaysAt985() error {
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	now := time.Now()
	lt, err := s.rampApproved(s.op, now.Add(-40*24*time.Hour))
	if err != nil {
		return err
	}
	entered := now.Add(-30*24*time.Hour - time.Minute)
	setStageAt(s.t, s.b, lt.id, stageProving, entered)
	s.tw = lt
	s.rampRecordChecks(lt.id, entered.Add(time.Hour), 197, 3, 50) // 98.5%
	p, f := s.rampHealth(lt.id, entered)
	if p != 197 || f != 3 {
		return fmt.Errorf("fixture: %d/%d", p, f)
	}
	return nil
}

func (s *rampState) newSatisfyingEstablished() error {
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	entered := time.Now().Add(-40 * 24 * time.Hour)
	lt, err := s.rampApproved(s.op, entered)
	if err != nil {
		return err
	}
	setStageAt(s.t, s.b, lt.id, stageNew, entered)
	s.tw = lt
	s.rampRecordChecks(lt.id, entered.Add(time.Hour), 300, 0, 0)
	return nil
}

func (s *rampState) oneStepThenOwn30Days() error {
	if err := s.stageBecomes("proving"); err != nil {
		return err
	}
	ev := s.rampStageEvents(s.tw.id)[0]
	if ev.From != "new" {
		return fmt.Errorf("it rose from %q", ev.From)
	}
	n := len(s.rampStageEvents(s.tw.id))
	s.b.towerGovSweepOnce(time.Now())
	if got := s.b.towerStage(s.tw.id); got != stageProving || len(s.rampStageEvents(s.tw.id)) != n {
		return fmt.Errorf("a second sweep moved it to %q", got)
	}
	rep := s.b.stageReport(mustTower(s.t, s.b, s.tw.id), time.Now())
	if rep["next"] != "30 more days" {
		return fmt.Errorf("next is %v, want 30 more days from entering proving", rep["next"])
	}
	return nil
}

// --- demotion ----------------------------------------------------------------------------------

func (s *rampState) towerIn(stage string) error {
	st := placement.Stage(stage)
	if !placement.ValidStage(st) {
		return fmt.Errorf("unknown stage %q", stage)
	}
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	lt, err := s.rampLive(s.op, "192.0.2.9:8443", "", st, time.Now().Add(-3*time.Hour))
	if err != nil {
		return err
	}
	s.tw, s.stage0 = lt, st
	if err := s.rampAttachMany(12, lt.id); err != nil {
		return err
	}
	if got := s.b.towerStage(lt.id); got != st {
		return fmt.Errorf("fixture stage %q", got)
	}
	return nil
}

func (s *rampState) eventHappens(event string) error {
	id := s.tw.id
	s.stageBefore = len(s.rampStageEvents(id))
	s.passAt = time.Now()
	switch event {
	case "Core suspends it":
		s.b.autoSuspend(id, reputation.Tally{TowerID: id}, reputation.Tally{})
		s.causeKind, s.causeReason = evLifecycle, "suspended"
		if mustTower(s.t, s.b, id).State != admit.StateSuspended {
			return fmt.Errorf("Core did not suspend it")
		}
	case "its lease lapses for more than one hour":
		s.b.towerEvent(admit.Event{TowerID: id, Kind: evLeaseLapsed, Actor: admit.ActorSystem,
			At: time.Now().Add(-2 * time.Hour), Detail: map[string]any{"lease_expired": "x"}})
		s.causeKind, s.causeReason = evLeaseLapsed, "lease_lapsed"
	case "its health checks fail twice in a row":
		s.b.recordHealthResult(id, false, time.Now())
		s.b.recordHealthResult(id, false, time.Now().Add(time.Second))
		s.causeKind, s.causeReason = evHealthFailing, "health_failing"
		// The failing trigger outranks every other while the failures are fresh; the cap is
		// what remains once they are not (towermobility.go mobilityTower).
		s.passAt = time.Now().Add(observationStaleAfter + time.Minute)
	case "an admin records an incident against it":
		code, raw := adminTowerPost(s.t, s.srv, "/tower/incident", `{"tower_id":"`+id+`","reason":"served stale weights"}`)
		if code != http.StatusOK {
			return fmt.Errorf("incident: %d %s", code, raw)
		}
		s.causeKind, s.causeReason = evIncident, "incident"
	default:
		return fmt.Errorf("unknown event %q", event)
	}
	s.readerStage = s.b.towerStage(id)
	return nil
}

func (s *rampState) demotedInSameWrite() error {
	if s.readerStage != stageNew {
		return fmt.Errorf("right after the event the stage read %q, want new", s.readerStage)
	}
	if n := len(s.rampStageEvents(s.tw.id)); n != s.stageBefore {
		return fmt.Errorf("a stage event was written before any sweep (%d -> %d): the reader must not need it", s.stageBefore, n)
	}
	return nil
}

func (s *rampState) stageEventNamesCause() error {
	s.b.towerGovSweepOnce(time.Now())
	evs := s.rampStageEvents(s.tw.id)
	if len(evs) != s.stageBefore+1 {
		return fmt.Errorf("%d new trust_stage events, want 1", len(evs)-s.stageBefore)
	}
	ev := evs[0]
	if ev.To != "new" || ev.From != admit.State(s.stage0) || ev.Reason != s.causeReason || ev.Actor != admit.ActorSystem {
		return fmt.Errorf("demotion event %+v, want %s -> new for %q", ev, s.stage0, s.causeReason)
	}
	causes := towerEventsOf(s.t, s.b, s.tw.id, s.causeKind)
	if len(causes) == 0 {
		return fmt.Errorf("no %s event on the record", s.causeKind)
	}
	ids := eventIDs(ev.Detail["cause_events"])
	found := false
	for _, c := range ids {
		found = found || c == causes[0].ID
	}
	if !found {
		return fmt.Errorf("the demotion names causes %v, not the %s event %d", ids, s.causeKind, causes[0].ID)
	}
	return nil
}

func (s *rampState) overCapDueStageCap() error {
	// Somewhere for them to go: a move with no destination is never started.
	liveEdgeTower(s.t, s.b, s.srv, "ramp-elsewhere-"+randHex(3), "203.0.113.201:8443")
	s.b.mobilityPassOnce(s.passAt)
	started := towerEventsOf(s.t, s.b, s.tw.id, evStationMoveStarted)
	if len(started) != 12-stageCaps[stageNew].community {
		return fmt.Errorf("%d Stations due, want %d (12 on a Tower now capped at 10)", len(started), 12-stageCaps[stageNew].community)
	}
	newest := map[string]bool{s.stations[10]: true, s.stations[11]: true}
	for _, ev := range started {
		if ev.Reason != triggerCap {
			return fmt.Errorf("trigger %q, want %q", ev.Reason, triggerCap)
		}
		if !newest[fmt.Sprint(ev.Detail["station"])] {
			return fmt.Errorf("Station %v moved, want only the newest two", ev.Detail["station"])
		}
	}
	return nil
}

func (s *rampState) anyStageRevoked() error {
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	lt, err := s.rampLive(s.op, "192.0.2.9:8443", "", stageProving, time.Now().Add(-48*time.Hour))
	if err != nil {
		return err
	}
	s.tw = lt
	if err := s.rampAttachMany(2, lt.id); err != nil {
		return err
	}
	code, raw := adminTowerPost(s.t, s.srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"revoked","reason":"key compromise"}`)
	if code != http.StatusOK {
		return fmt.Errorf("revoke: %d %s", code, raw)
	}
	return nil
}

func (s *rampState) noStageNoStations() error {
	tw := mustTower(s.t, s.b, s.tw.id)
	if rep := s.b.stageReport(tw, time.Now()); rep != nil {
		return fmt.Errorf("a revoked Tower still reports a stage: %v", rep)
	}
	if d := adminTowerDetailOf(s.t, s.srv, s.tw.id); d["trust"] != nil {
		return fmt.Errorf("the admin detail still shows a stage: %v", d["trust"])
	}
	n := len(s.rampStageEvents(s.tw.id))
	s.b.towerGovSweepOnce(time.Now())
	if len(s.rampStageEvents(s.tw.id)) != n {
		return fmt.Errorf("the sweep computed a stage for a revoked Tower")
	}
	s.b.mobilityPassOnce(time.Now())
	if live, err := s.rampLiveStations(s.tw.id); err != nil || live != 0 {
		return fmt.Errorf("the revoked Tower still holds %d live Stations (%v)", live, err)
	}
	for _, ev := range towerEventsOf(s.t, s.b, s.tw.id, evStationMoveStarted) {
		if ev.Reason != triggerGone {
			return fmt.Errorf("a Station left with trigger %q", ev.Reason)
		}
	}
	_, _, got, code, _ := s.rampAttachNudge("ramp-late-"+randHex(4), false)
	if code == http.StatusOK && got == s.tw.id {
		return fmt.Errorf("a new Station was placed on the revoked Tower")
	}
	return nil
}

func (s *rampState) readmittedStartsNew() error {
	reg := s.b.tower.registry
	// The Tower itself cannot come back: revocation is terminal and its key stays burned.
	if err := reg.Transition(s.tw.id, admit.StateQuarantine); err == nil {
		return fmt.Errorf("a revoked Tower moved back to quarantine")
	}
	tok, err := reg.IssueToken(s.op.login)
	if err != nil {
		return err
	}
	if _, err := reg.Enroll(tok, rampKeyHash(s.tw)); err == nil || !strings.Contains(err.Error(), "revoked") {
		return fmt.Errorf("re-enrolling the revoked key: %v, want refused as revoked", err)
	}
	// Readmission is a new admission, and it starts at "new" like any other.
	again, err := s.rampApproved(s.op, time.Now().Add(-time.Minute))
	if err != nil {
		return err
	}
	s.b.towerGovSweepOnce(time.Now())
	evs := s.rampStageEvents(again.id)
	if len(evs) != 1 || evs[0].From != "" || evs[0].To != "new" {
		return fmt.Errorf("the readmitted Tower's stage events %+v, want one from \"\" to new", evs)
	}
	if s.b.towerStage(again.id) != stageNew {
		return fmt.Errorf("the readmitted Tower is not new")
	}
	return nil
}

// --- excusals and admin actions -------------------------------------------------------------------

// rampDemotedByLapse is a Tower that earned "proving" and lost it to a lease lapse at -26h.
func (s *rampState) rampDemotedByLapse() (admit.Event, error) {
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	lt, err := s.rampApproved(s.op, time.Now().Add(-10*24*time.Hour))
	if err != nil {
		return admit.Event{}, err
	}
	setStageAt(s.t, s.b, lt.id, stageProving, time.Now().Add(-5*24*time.Hour))
	s.tw = lt
	if err := s.b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evLeaseLapsed, Actor: admit.ActorSystem,
		At: time.Now().Add(-26 * time.Hour)}); err != nil {
		return admit.Event{}, err
	}
	s.b.towerGovSweepOnce(time.Now())
	if s.b.towerStage(lt.id) != stageNew {
		return admit.Event{}, fmt.Errorf("fixture: the lapse did not demote")
	}
	return towerEventsOf(s.t, s.b, lt.id, evLeaseLapsed)[0], nil
}

func (s *rampState) leaseLapsedCoreDefect() error {
	_, err := s.rampDemotedByLapse()
	return err
}

func (s *rampState) adminExcusesLapse() error {
	lapse := towerEventsOf(s.t, s.b, s.tw.id, evLeaseLapsed)[0]
	code, raw := adminTowerPost(s.t, s.srv, "/tower/excuse", `{"tower_id":"`+s.tw.id+`","event_id":`+
		strconv.FormatInt(lapse.ID, 10)+`,"reason":"renewal signed with the wrong key (Core defect)"}`)
	if code != http.StatusOK {
		return fmt.Errorf("excuse: %d %s", code, raw)
	}
	return nil
}

func (s *rampState) excusalAttributed() error {
	lapse := towerEventsOf(s.t, s.b, s.tw.id, evLeaseLapsed)[0]
	ex := towerEventsOf(s.t, s.b, s.tw.id, evExcusal)
	if len(ex) != 1 {
		return fmt.Errorf("%d excusal events, want 1", len(ex))
	}
	e := ex[0]
	if e.Actor != "admin:broker-key" || e.Reason == "" || eventID(e.Detail["event_id"]) != lapse.ID || e.Detail["event_kind"] != evLeaseLapsed {
		return fmt.Errorf("excusal %+v, want admin:broker-key naming lease_lapsed %d with a reason", e, lapse.ID)
	}
	return nil
}

func (s *rampState) lapseNoLongerCounts() error {
	v, err := s.b.readStage(s.tw.id, time.Now())
	if err != nil {
		return err
	}
	if len(v.disq) != 0 {
		return fmt.Errorf("the excused lapse still counts against clean: %+v", v.disq)
	}
	s.b.towerGovSweepOnce(time.Now())
	if got := s.b.towerStage(s.tw.id); got != stageProving {
		return fmt.Errorf("stage %q after the excusal, want proving restored", got)
	}
	if ev := s.rampStageEvents(s.tw.id)[0]; ev.Reason != "excused" || ev.To != "proving" {
		return fmt.Errorf("restoring event %+v", ev)
	}
	return nil
}

func (s *rampState) blankOrGeneralExcusal() error {
	lapse, err := s.rampDemotedByLapse()
	if err != nil {
		return err
	}
	stageEv := s.rampStageEvents(s.tw.id)[0]
	id := s.tw.id
	for _, body := range []string{
		`{"tower_id":"` + id + `","reason":"our bug"}`,                                                  // no event
		`{"tower_id":"` + id + `","event_id":0,"reason":"everything last week"}`,                        // no event
		`{"tower_id":"` + id + `","event_id":` + strconv.FormatInt(lapse.ID, 10) + `}`,                  // no reason
		`{"tower_id":"` + id + `","event_id":` + strconv.FormatInt(lapse.ID, 10) + `,"reason":"   "}`,   // blank reason
		`{"tower_id":"` + id + `","event_id":999999,"reason":"x"}`,                                      // not recorded
		`{"tower_id":"` + id + `","event_id":` + strconv.FormatInt(stageEv.ID, 10) + `,"reason":"all"}`, // not a disqualifier
	} {
		code, _ := adminTowerPost(s.t, s.srv, "/tower/excuse", body)
		s.codes = append(s.codes, code)
	}
	return nil
}

func (s *rampState) itIsRefused() error {
	for i, c := range s.codes {
		if c == http.StatusOK || c < 400 {
			return fmt.Errorf("attempt %d was accepted (%d)", i+1, c)
		}
	}
	if len(s.codes) == 0 {
		return fmt.Errorf("nothing was attempted")
	}
	return nil
}

func (s *rampState) nothingExcused() error {
	if ex := towerEventsOf(s.t, s.b, s.tw.id, evExcusal); len(ex) != 0 {
		return fmt.Errorf("%d excusals were recorded", len(ex))
	}
	s.b.towerGovSweepOnce(time.Now())
	if got := s.b.towerStage(s.tw.id); got != stageNew {
		return fmt.Errorf("stage %q, want still new", got)
	}
	return nil
}

func (s *rampState) adminSetsCeiling() error {
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	lt, err := s.rampApproved(s.op, time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	s.tw = lt
	setStage(s.t, s.b, lt.id, stageProving) // what the evidence earned
	code, raw := adminTowerPost(s.t, s.srv, "/tower/stage-ceiling", `{"tower_id":"`+lt.id+`","stage":"new","reason":"hold while we look"}`)
	if code != http.StatusOK {
		return fmt.Errorf("ceiling: %d %s", code, raw)
	}
	return nil
}

func (s *rampState) lowerOfComputedAndCeiling() error {
	if got := s.b.towerStage(s.tw.id); got != stageNew {
		return fmt.Errorf("computed proving, ceiling new: stage %q", got)
	}
	ev := towerEventsOf(s.t, s.b, s.tw.id, evStageCeiling)
	if len(ev) != 1 || ev[0].Actor != "admin:broker-key" || ev[0].Reason == "" {
		return fmt.Errorf("ceiling events %+v", ev)
	}
	// A ceiling above the computed stage leaves the computed one.
	code, _ := adminTowerPost(s.t, s.srv, "/tower/stage-ceiling", `{"tower_id":"`+s.tw.id+`","stage":"established","reason":"lifted"}`)
	if code != http.StatusOK {
		return fmt.Errorf("lifting: %d", code)
	}
	if got := s.b.towerStage(s.tw.id); got != stageProving {
		return fmt.Errorf("computed proving, ceiling established: stage %q", got)
	}
	return nil
}

func (s *rampState) noAdminRaise() error {
	id := s.tw.id
	// A ceiling cannot be a stage that does not exist, nor be set without a reason.
	for _, body := range []string{
		`{"tower_id":"` + id + `","stage":"gold","reason":"x"}`,
		`{"tower_id":"` + id + `","stage":"established"}`,
	} {
		if code, _ := adminTowerPost(s.t, s.srv, "/tower/stage-ceiling", body); code != http.StatusBadRequest {
			return fmt.Errorf("%s: %d, want 400", body, code)
		}
	}
	// There is no route that sets a stage directly.
	if code, _ := adminTowerPost(s.t, s.srv, "/tower/stage", `{"tower_id":"`+id+`","stage":"established","reason":"x"}`); code != http.StatusNotFound {
		return fmt.Errorf("POST /tower/stage answered %d, want no such route", code)
	}
	// An excusal of a non-disqualifier cannot promote either, and no sweep promotes past evidence.
	s.b.towerGovSweepOnce(time.Now())
	if got := s.b.towerStage(id); got != stageProving {
		return fmt.Errorf("stage %q after admin actions, want the computed proving", got)
	}
	for _, ev := range s.rampStageEvents(id) {
		if ev.To == "established" {
			return fmt.Errorf("an established stage was recorded without evidence")
		}
	}
	return nil
}

func (s *rampState) adminRecordsIncident() error {
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	lt, err := s.rampApproved(s.op, time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	s.tw = lt
	setStage(s.t, s.b, lt.id, stageEstablished)
	s.stageBefore = len(s.rampStageEvents(lt.id))
	code, raw := adminTowerPost(s.t, s.srv, "/tower/incident", `{"tower_id":"`+lt.id+`","reason":"served stale weights"}`)
	if code != http.StatusOK {
		return fmt.Errorf("incident: %d %s", code, raw)
	}
	s.readerStage = s.b.towerStage(lt.id)
	return nil
}

func (s *rampState) demotedAsAnyDemotion() error {
	if s.readerStage != stageNew {
		return fmt.Errorf("stage %q right after the incident", s.readerStage)
	}
	s.stage0, s.causeKind, s.causeReason = stageEstablished, evIncident, "incident"
	return s.stageEventNamesCause()
}

func (s *rampState) incidentAttributed() error {
	evs := towerEventsOf(s.t, s.b, s.tw.id, evIncident)
	if len(evs) != 1 || evs[0].Actor != "admin:broker-key" || evs[0].Reason != "served stale weights" {
		return fmt.Errorf("incident events %+v", evs)
	}
	return nil
}

// --- what the stage controls ---------------------------------------------------------------------

func (s *rampState) newHolds10() error {
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	lt, err := s.rampLive(s.op, "192.0.2.9:8443", "", stageNew, time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	s.tw = lt
	return s.rampAttachMany(10, lt.id)
}

func (s *rampState) eleventhPlaced() error {
	other, err := s.rampLive(signedInOperator(s.t, s.b, "ramp-other"), "198.51.100.9:8443", "", stageEstablished, time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	s.extra = []linkTower{other}
	_, _, got, code, raw := s.rampAttachNudge("ramp-eleventh", false)
	s.placedOn, s.placeCode = got, code
	if code != http.StatusOK {
		return fmt.Errorf("the 11th: %d %s", code, raw)
	}
	return nil
}

func (s *rampState) thatTowerNotCandidate() error {
	if s.placedOn == s.tw.id {
		return fmt.Errorf("the 11th community Station went to the Tower at its cap")
	}
	evs := towerEventsOf(s.t, s.b, s.extra[0].id, evStationPlaced)
	if len(evs) != 1 || fmt.Sprint(evs[0].Detail["candidates"]) != "1" {
		return fmt.Errorf("placement events %+v, want one with a single candidate", evs)
	}
	for i := 0; i < 20; i++ {
		if got, ok := s.b.placeStation("pk-"+strconv.Itoa(i), placement.Locality{}, nil); !ok || got.towerID == s.tw.id {
			return fmt.Errorf("placement offered the capped Tower")
		}
	}
	return nil
}

func (s *rampState) onlyNewCommunityEligible() error {
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	lt, err := s.rampLive(s.op, "198.51.100.9:8443", "", stageNew, time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	s.tw = lt
	return nil
}

func (s *rampState) houseStationPlaced() error {
	nodeID, _, got, code, _ := s.rampAttachNudge("ramp-house-"+randHex(4), true)
	s.placedOn, s.placeCode = got, code
	if code == http.StatusOK {
		s.houseNodes = append(s.houseNodes, nodeID)
	}
	return nil
}

func (s *rampState) notPlacedThere() error {
	if s.placeCode == http.StatusOK || s.placedOn == s.tw.id {
		return fmt.Errorf("the house Station was placed (%d, on %q)", s.placeCode, s.placedOn)
	}
	if n, _ := s.rampLiveStations(s.tw.id); n != 0 {
		return fmt.Errorf("the new Tower holds %d Stations", n)
	}
	return nil
}

func (s *rampState) provingNoHouseTower() error {
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	lt, err := s.rampLive(s.op, "198.51.100.9:8443", "", stageProving, time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	s.tw = lt
	return nil
}

func (s *rampState) mayGoUpToHouseCap() error {
	if s.placeCode != http.StatusOK || s.placedOn != s.tw.id {
		return fmt.Errorf("the first house Station: %d on %q, want the proving Tower", s.placeCode, s.placedOn)
	}
	for i := 1; i < stageCaps[stageProving].house; i++ {
		if err := s.houseStationPlaced(); err != nil {
			return err
		}
		if s.placeCode != http.StatusOK || s.placedOn != s.tw.id {
			return fmt.Errorf("house Station %d: %d on %q", i+1, s.placeCode, s.placedOn)
		}
	}
	evs := towerEventsOf(s.t, s.b, s.tw.id, evStationPlaced)
	if len(evs) != 5 || evs[0].Detail["reason"] != "house failover" || evs[0].Detail["group"] != "house" {
		return fmt.Errorf("placements %+v, want 5 as house failover", evs)
	}
	nodes := s.houseNodes
	if err := s.houseStationPlaced(); err != nil {
		return err
	}
	if s.placeCode == http.StatusOK {
		return fmt.Errorf("a 6th house Station was placed past the cap of 5")
	}
	s.houseNodes = nodes
	return nil
}

func (s *rampState) houseBackDueBetterTower() error {
	if stageCaps[stageProving].house != 5 {
		return fmt.Errorf("the proving house cap is %d", stageCaps[stageProving].house)
	}
	house := houseTower(s.t, s.b, s.srv, "ramp-roger-house", "203.0.113.9:8443")
	now := time.Now()
	for i := 0; i < betterObservations; i++ {
		at := now.Add(time.Duration(i) * canaryInterval)
		s.b.recordHealthResult(s.tw.id, true, at)
		s.b.mobilityPassOnce(at)
	}
	started := towerEventsOf(s.t, s.b, s.tw.id, evStationMoveStarted)
	if len(started) != 5 {
		return fmt.Errorf("%d house Stations due to move back, want 5", len(started))
	}
	for _, ev := range started {
		if ev.Reason != triggerBetter || ev.Detail["to"] != house.id {
			return fmt.Errorf("move %+v, want better_tower to the house Tower %s", ev, house.id)
		}
	}
	return nil
}

func (s *rampState) stationBehindNewTower() error {
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	lt, err := s.rampLive(s.op, "198.51.100.9:8443", "", stageNew, time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	s.tw = lt
	return nil
}

func (s *rampState) rampRows() []attemptCand {
	return []attemptCand{{model: "m", edge: &edgeCand{row: fleet.Station{TowerID: s.tw.id, NodeID: "n-behind"}, tier: "A"}}}
}

func (s *rampState) coinDecides() error { return nil }

// rampWeightIs checks the Tower side's weight is w on the real coin path: edgeSideWeight with
// the broker's own towerEdgeWeight, and edgeCoinWeighted scaling the Tower side's capacity by
// exactly w (a Tower capacity of 4 at weight w decides every request like a capacity of 4w).
func (s *rampState) rampWeightIs(w float64, scaled int) error {
	got := edgeSideWeight(s.rampRows(), func(string) int { return 4 }, s.b.towerEdgeWeight)
	if got != w {
		return fmt.Errorf("the Tower side's weight is %v, want %v", got, w)
	}
	for i := 0; i < 3000; i++ {
		seed := "req-" + strconv.Itoa(i)
		if edgeCoinWeighted(seed, 3, 4, got) != edgeCoin(seed, 3, scaled) {
			return fmt.Errorf("request %s: weight %v is not the Tower capacity multiplied by %v", seed, got, w)
		}
	}
	return nil
}

func (s *rampState) weightTimesQuarter() error { return s.rampWeightIs(0.25, 1) }

func (s *rampState) weightProvingEstablished() error {
	setStage(s.t, s.b, s.tw.id, stageProving)
	if err := s.rampWeightIs(0.5, 2); err != nil {
		return err
	}
	setStage(s.t, s.b, s.tw.id, stageEstablished)
	return s.rampWeightIs(1.0, 4)
}

// rampMerge runs the real merge for one direct candidate and one Tower row, counting the coin.
func (s *rampState) rampMerge(m edgeMerge, towerTier string, edgeCost float64) ([]attemptCand, int) {
	calls := 0
	direct := attemptCand{model: "m", node: protocol.NodeRegistration{NodeID: "n-direct"}}
	tw := attemptCand{model: "m", edge: &edgeCand{row: fleet.Station{TowerID: s.tw.id, NodeID: "n-behind"}, tier: towerTier,
		metric: edgeMetric{cost: edgeCost}}}
	m.headModel = "m"
	m.towers = map[string][]attemptCand{"m": {tw}}
	m.coinEdge = func() bool {
		calls++
		w := edgeSideWeight(m.towers["m"], func(string) int { return 1 }, s.b.towerEdgeWeight)
		return edgeCoinWeighted("seed", 1, 1, w)
	}
	return s.b.mergeEdgePlan([]attemptCand{direct}, []string{"m"}, m, time.Now()), calls
}

func rampTowerFirst(plan []attemptCand) bool { return len(plan) == 2 && plan[0].edge != nil }

func (s *rampState) mergeUnchangedBeforeCoin() error {
	setStage(s.t, s.b, s.tw.id, stageNew)
	// provider order
	if p, c := s.rampMerge(edgeMerge{order: []string{s.tw.id}}, "A", 0); !rampTowerFirst(p) || c != 0 {
		return fmt.Errorf("order naming the Tower: towerFirst=%v coin=%d", rampTowerFirst(p), c)
	}
	if p, c := s.rampMerge(edgeMerge{order: []string{"n-direct"}}, "A", 0); rampTowerFirst(p) || c != 0 {
		return fmt.Errorf("order naming the direct station: towerFirst=%v coin=%d", rampTowerFirst(p), c)
	}
	// sort
	if p, c := s.rampMerge(edgeMerge{sort: sortPrice}, "A", 1); rampTowerFirst(p) || c != 0 {
		return fmt.Errorf("price sort, dearer Tower: towerFirst=%v coin=%d", rampTowerFirst(p), c)
	}
	// tier gate
	if p, c := s.rampMerge(edgeMerge{}, "B", 0); rampTowerFirst(p) || c != 0 {
		return fmt.Errorf("tier gate (Tower B, direct A): towerFirst=%v coin=%d", rampTowerFirst(p), c)
	}
	// curated home first: a curated direct station, a home Tower row: the home side first.
	s.b.mu.Lock()
	if s.b.nodes == nil {
		s.b.nodes = map[string]protocol.NodeRegistration{}
	}
	s.b.nodes["n-direct"] = protocol.NodeRegistration{NodeID: "n-direct", Curated: true}
	s.b.mu.Unlock()
	if p, c := s.rampMerge(edgeMerge{}, "A", 0); !rampTowerFirst(p) || c != 0 {
		return fmt.Errorf("curated direct vs home Tower: towerFirst=%v coin=%d", rampTowerFirst(p), c)
	}
	s.b.mu.Lock()
	delete(s.b.nodes, "n-direct")
	s.b.mu.Unlock()
	// Only a tie on all of that reaches the coin, once.
	if _, c := s.rampMerge(edgeMerge{}, "A", 0); c != 1 {
		return fmt.Errorf("a tie consulted the coin %d times, want once", c)
	}
	return nil
}

func (s *rampState) requestThroughAnyStage() error {
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	lt, err := s.rampLive(s.op, "198.51.100.9:8443", "", stageNew, time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	s.tw = lt
	s.b.feeRate = defaultFeeRate // the production fee, so every share is non-zero
	s.shares = map[placement.Stage][3]float64{}
	const cost = 2.0
	for i, st := range []placement.Stage{stageNew, stageProving, stageEstablished} {
		setStage(s.t, s.b, lt.id, st)
		wallet := "ramp-consumer-" + strconv.Itoa(i)
		stationAcct, towerAcct := "ramp-station-acct-"+strconv.Itoa(i), "ramp-tower-acct-"+strconv.Itoa(i)
		attempt := "ramp-attempt-" + strconv.Itoa(i)
		if _, err := s.b.db.AddCredits(wallet, 10); err != nil {
			return err
		}
		if ok, err := s.b.db.HoldFor(wallet, attempt, cost); err != nil || !ok {
			return fmt.Errorf("hold: %v %v", ok, err)
		}
		s.b.captureEdgeCharge(lt.id, "n-station-"+strconv.Itoa(i), stationAcct,
			edgeParties{consumerWallet: wallet, billable: true, keyed: true, towerAcct: towerAcct, towerPaid: true},
			attempt, "relay-"+attempt, "m", cost, 10, 10, time.Now())
		stSplit, _ := s.b.db.EarningSplitOf(stationAcct, time.Now())
		twSplit, _ := s.b.db.EarningSplitOf(towerAcct, time.Now())
		spend, _ := s.b.db.SpendOf(wallet)
		stationGot := stSplit.Held + stSplit.Reserved + stSplit.Payable
		towerGot := twSplit.Held + twSplit.Reserved + twSplit.Payable
		s.shares[st] = [3]float64{stationGot, towerGot, spend - stationGot - towerGot}
	}
	return nil
}

func (s *rampState) sharesAsToday() error {
	const cost = 2.0
	wantStation, wantTower := s.b.edgeShares(cost)
	want := [3]float64{wantStation, wantTower, cost - wantStation - wantTower}
	if wantStation != cost*(1-s.b.feeRate) || wantTower != cost*edgeTowerRate() || wantTower <= 0 || want[2] <= 0 {
		return fmt.Errorf("today's split moved: %v %v", wantStation, wantTower)
	}
	for st, got := range s.shares {
		for k := 0; k < 3; k++ {
			if d := got[k] - want[k]; d > 1e-9 || d < -1e-9 {
				return fmt.Errorf("stage %s: shares %v, want %v (station, tower, platform)", st, got, want)
			}
		}
	}
	if len(s.shares) != 3 {
		return fmt.Errorf("measured %d stages", len(s.shares))
	}
	return nil
}

// --- house Towers ---------------------------------------------------------------------------------

func (s *rampState) aHouseTower() error {
	s.tw = houseTower(s.t, s.b, s.srv, "ramp-roger-house", "203.0.113.9:8443")
	// Whatever its record says, a house Tower is not ramped: record the lowest stage on it.
	setStage(s.t, s.b, s.tw.id, stageNew)
	return nil
}

func (s *rampState) consultsStage() error {
	s.b.towerGovSweepOnce(time.Now())
	return nil
}

func (s *rampState) noCapsWeightOne() error {
	if w := s.b.towerEdgeWeight(s.tw.id); w != 1.0 {
		return fmt.Errorf("edge weight %v, want 1.0", w)
	}
	if rep := s.b.stageReport(mustTower(s.t, s.b, s.tw.id), time.Now()); rep["stage"] != "house" || len(rep) != 1 {
		return fmt.Errorf("stage report %v, want house only", rep)
	}
	n := len(s.rampStageEvents(s.tw.id))
	s.b.towerGovSweepOnce(time.Now())
	if len(s.rampStageEvents(s.tw.id)) != n {
		return fmt.Errorf("the sweep computed a stage for a house Tower")
	}
	// No community cap: 12 community Stations, more than "new" allows, all land on it.
	if err := s.rampAttachMany(12, s.tw.id); err != nil {
		return err
	}
	// And house Stations, which "new" forbids.
	_, _, got, code, raw := s.rampAttachNudge("ramp-house-st", true)
	if code != http.StatusOK || got != s.tw.id {
		return fmt.Errorf("a house Station on the house Tower: %d %q %s", code, got, raw)
	}
	return nil
}

func (s *rampState) liveHealthyUnderCapacity() error {
	// Healthy: of two house Towers, the one whose last two checks failed loses.
	b2, srv2 := towerTestBroker(s.t)
	s2 := &rampState{t: s.t, b: b2, srv: srv2}
	sick := houseTower(s.t, b2, srv2, "ramp-house-a", "203.0.113.9:8443")
	well := houseTower(s.t, b2, srv2, "ramp-house-b", "203.0.113.10:8443")
	b2.recordHealthResult(sick.id, false, time.Now().Add(-time.Minute))
	b2.recordHealthResult(sick.id, false, time.Now())
	for i := 0; i < 3; i++ {
		_, _, got, code, raw := s2.rampAttachNudge("ramp-hs-"+randHex(4), true)
		if code != http.StatusOK || got != well.id {
			return fmt.Errorf("an unhealthy house Tower was chosen: %d %q (sick %s) %s", code, got, sick.id, raw)
		}
	}
	// Under capacity: a full house Tower takes nothing.
	prev := towerStationCapacity
	n, _ := s.rampLiveStations(s.tw.id)
	towerStationCapacity = func() int { return n }
	_, _, got, code, _ := s.rampAttachNudge("ramp-hs-full", true)
	towerStationCapacity = prev
	if code == http.StatusOK && got == s.tw.id {
		return fmt.Errorf("a full house Tower took a Station")
	}
	// Live: a house Tower whose link is closed takes nothing.
	s.b.tower.link.Close(mustCurrent(s.t, s.b, s.tw.id), s.tw.id)
	_, _, got, code, _ = s.rampAttachNudge("ramp-hs-dark", true)
	if code == http.StatusOK && got == s.tw.id {
		return fmt.Errorf("a house Tower with no live link took a Station")
	}
	return nil
}

func (s *rampState) noRankingAdvantage() error {
	b3, srv3 := towerTestBroker(s.t)
	s3 := &rampState{t: s.t, b: b3, srv: srv3}
	house := houseTower(s.t, b3, srv3, "ramp-house-c", "203.0.113.9:8443")
	community := liveEdgeTower(s.t, b3, srv3, "ramp-comm", "198.51.100.9:8443")
	for i := 0; i < 3; i++ {
		_, _, got, code, raw := s3.rampAttachNudge("ramp-cs-"+randHex(4), false)
		if code != http.StatusOK || got != community.id {
			return fmt.Errorf("a community Station on a tie went to %q (house %s): %d %s", got, house.id, code, raw)
		}
	}
	return nil
}

// --- who sees the stage ---------------------------------------------------------------------------

func (s *rampState) adminOpensDetail() error {
	if _, err := s.rampDemotedByLapse(); err != nil {
		return err
	}
	if err := s.adminExcusesLapse(); err != nil {
		return err
	}
	s.b.towerGovSweepOnce(time.Now()) // proving restored
	id := s.tw.id
	if code, raw := adminTowerPost(s.t, s.srv, "/tower/incident", `{"tower_id":"`+id+`","reason":"served stale weights"}`); code != http.StatusOK {
		return fmt.Errorf("incident: %d %s", code, raw)
	}
	if code, raw := adminTowerPost(s.t, s.srv, "/tower/stage-ceiling", `{"tower_id":"`+id+`","stage":"proving","reason":"hold"}`); code != http.StatusOK {
		return fmt.Errorf("ceiling: %d %s", code, raw)
	}
	s.b.towerGovSweepOnce(time.Now()) // the demotion is written
	passChecks(s.t, s.b, id, 9, time.Now().Add(-time.Minute*20))
	s.b.recordHealthResult(id, false, time.Now())
	return nil
}

func (s *rampState) detailShowsEverything() error {
	d := adminTowerDetailOf(s.t, s.srv, s.tw.id)
	st, ok := d["trust"].(map[string]any)
	if !ok {
		return fmt.Errorf("no trust section: %v", d)
	}
	if st["stage"] != "new" || st["entered_at"] == "" || st["entered_at"] == nil {
		return fmt.Errorf("stage/entered: %v", st)
	}
	if fmt.Sprint(st["passed"]) != "9" || fmt.Sprint(st["failed"]) != "1" || st["pass_rate"] != "0.900" {
		return fmt.Errorf("checks passed/failed/rate: %v %v %v", st["passed"], st["failed"], st["pass_rate"])
	}
	if _, ok := st["days"]; !ok {
		return fmt.Errorf("no days counted: %v", st)
	}
	if st["ceiling"] != "proving" || fmt.Sprint(st["excusals"]) != "1" {
		return fmt.Errorf("ceiling/excusals: %v %v", st["ceiling"], st["excusals"])
	}
	if next, _ := st["next"].(string); !strings.Contains(next, "more days") || !strings.Contains(next, "41 more passed health checks") {
		return fmt.Errorf("next %q", st["next"])
	}
	kinds := map[string]bool{}
	evs, _ := d["events"].([]any)
	for _, e := range evs {
		if m, ok := e.(map[string]any); ok {
			kinds[fmt.Sprint(m["kind"])] = true
		}
	}
	for _, k := range []string{evIncident, evExcusal, evStageCeiling} {
		if !kinds[k] {
			return fmt.Errorf("the detail does not show the %s (kinds %v)", k, kinds)
		}
	}
	return nil
}

func (s *rampState) operatorViews() error {
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	lt, err := s.rampApproved(s.op, time.Now().Add(-2*24*time.Hour))
	if err != nil {
		return err
	}
	s.tw = lt
	s.b.towerGovSweepOnce(time.Now())
	// The record holds something the operator must not see.
	if err := s.b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evStageCeiling, Actor: "admin:broker-key",
		To: "established", Reason: "secret-admin-note"}); err != nil {
		return err
	}
	passChecks(s.t, s.b, lt.id, 10, time.Now().Add(-time.Hour))
	// Another operator's Tower at another stage.
	other, err := s.rampApproved(signedInOperator(s.t, s.b, "ramp-other"), time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	setStage(s.t, s.b, other.id, stageProving)
	s.extra = []linkTower{other}
	return nil
}

func (s *rampState) rampStatus() ([]map[string]any, string, error) {
	var status struct {
		Towers []map[string]any `json:"towers"`
	}
	code, raw := s.op.call(s.t, s.srv, http.MethodGet, "/tower/status", nil, &status)
	if code != http.StatusOK {
		return nil, raw, fmt.Errorf("status: %d %s", code, raw)
	}
	return status.Towers, raw, nil
}

func (s *rampState) operatorSeesStage() error {
	towers, _, err := s.rampStatus()
	if err != nil {
		return err
	}
	if len(towers) != 1 {
		return fmt.Errorf("%d Towers listed", len(towers))
	}
	trust, ok := towers[0]["trust"].(map[string]any)
	if !ok {
		return fmt.Errorf("no trust section")
	}
	if trust["stage"] != "new" || fmt.Sprint(trust["community_stations"]) != "10" || fmt.Sprint(trust["house_stations"]) != "0" {
		return fmt.Errorf("stage/caps %v", trust)
	}
	if trust["next"] != "5 more days, 40 more passed health checks" {
		return fmt.Errorf("next %q, want it in plain words", trust["next"])
	}
	return nil
}

func (s *rampState) operatorSeesNoOthers() error {
	towers, raw, err := s.rampStatus()
	if err != nil {
		return err
	}
	if strings.Contains(raw, s.extra[0].id) || strings.Contains(raw, "proving") {
		return fmt.Errorf("another Tower's stage leaked: %s", raw)
	}
	trust := towers[0]["trust"].(map[string]any)
	for k := range trust {
		switch k {
		case "stage", "next", "community_stations", "house_stations":
		default:
			return fmt.Errorf("the operator sees %q", k)
		}
	}
	for _, leak := range []string{"secret-admin-note", "stage_ceiling", "admin:", "excusal", "ceiling", "events"} {
		if strings.Contains(raw, leak) {
			return fmt.Errorf("the governance record leaked (%q): %s", leak, raw)
		}
	}
	// The record itself is admin only.
	req, _ := http.NewRequest(http.MethodGet, s.srv.URL+"/admin/tower?id="+s.tw.id, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return fmt.Errorf("the admin detail answered without the admin key")
	}
	return nil
}

// --- adversarial ----------------------------------------------------------------------------------

func (s *rampState) ownerDemotedOrRevoked() error {
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	lt, err := s.rampApproved(s.op, time.Now().Add(-40*24*time.Hour))
	if err != nil {
		return err
	}
	setStageAt(s.t, s.b, lt.id, stageEstablished, time.Now().Add(-5*24*time.Hour))
	if code, raw := adminTowerPost(s.t, s.srv, "/tower/incident", `{"tower_id":"`+lt.id+`","reason":"bad"}`); code != http.StatusOK {
		return fmt.Errorf("incident: %d %s", code, raw)
	}
	s.b.towerGovSweepOnce(time.Now())
	s.tw = lt
	// And a second Tower of the same owner, revoked.
	gone, err := s.rampApproved(s.op, time.Now().Add(-40*24*time.Hour))
	if err != nil {
		return err
	}
	if code, raw := adminTowerPost(s.t, s.srv, "/tower/lifecycle", `{"tower_id":"`+gone.id+`","state":"revoked","reason":"x"}`); code != http.StatusOK {
		return fmt.Errorf("revoke: %d %s", code, raw)
	}
	s.extra = []linkTower{gone}
	return nil
}

func (s *rampState) sameOwnerEnrolls() error {
	fresh, err := s.rampApproved(s.op, time.Now().Add(-time.Minute))
	if err != nil {
		return err
	}
	s.extra = append(s.extra, fresh)
	s.b.towerGovSweepOnce(time.Now())
	return nil
}

func (s *rampState) newTowerStartsNew() error {
	fresh := s.extra[len(s.extra)-1]
	evs := s.rampStageEvents(fresh.id)
	if len(evs) != 1 || evs[0].From != "" || evs[0].To != "new" {
		return fmt.Errorf("the new Tower's stage events %+v", evs)
	}
	// The old record is not reset: the demoted Tower is still new with its incident standing.
	if s.b.towerStage(s.tw.id) != stageNew || len(towerEventsOf(s.t, s.b, s.tw.id, evIncident)) != 1 {
		return fmt.Errorf("the demoted Tower's record changed")
	}
	if s.b.stageReport(mustTower(s.t, s.b, s.extra[0].id), time.Now()) != nil {
		return fmt.Errorf("the revoked Tower regained a stage")
	}
	return nil
}

func (s *rampState) quotaStillApplies() error {
	reg := s.b.tower.registry
	live := 0
	for _, tw := range reg.ByOwner(s.op.login) {
		if tw.State != admit.StateRevoked && tw.State != admit.StateExpired {
			live++
		}
	}
	for live < 10 {
		enrolledTower(s.t, s.b, s.op.login)
		live++
	}
	tok, err := reg.IssueToken(s.op.login)
	if err != nil {
		return err
	}
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(pub)
	if _, err := reg.Enroll(tok, hex.EncodeToString(sum[:])); err == nil || !strings.Contains(err.Error(), "already runs 10 Towers") {
		return fmt.Errorf("an 11th live Tower: %v, want the per-account quota", err)
	}
	return nil
}

func (s *rampState) threeProvingOneOwner() error {
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	for i := 0; i < 3; i++ {
		lt, err := s.rampLive(s.op, "198.51.100."+strconv.Itoa(i+1)+":8443", "", stageProving, time.Now().Add(-time.Hour))
		if err != nil {
			return err
		}
		s.extra = append(s.extra, lt)
	}
	return nil
}

func (s *rampState) houseFailsOver() error {
	for i := 0; i < 3*stageCaps[stageProving].house; i++ {
		if err := s.houseStationPlaced(); err != nil {
			return err
		}
	}
	return nil
}

func (s *rampState) ownerHoldsAtMost5House() error {
	total := 0
	for _, lt := range s.extra {
		n, err := s.rampLiveStations(lt.id)
		if err != nil {
			return err
		}
		total += n
	}
	if total != 5 || len(s.houseNodes) != 5 {
		return fmt.Errorf("the owner's three Towers hold %d house Stations (%d placed), want 5 in total", total, len(s.houseNodes))
	}
	return nil
}

func (s *rampState) canariesWellPaidBadly() error {
	s.op = signedInOperator(s.t, s.b, "ramp-nora")
	lt, err := s.rampApproved(s.op, time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	setStageAt(s.t, s.b, lt.id, stageNew, time.Now().Add(-time.Hour))
	s.tw = lt
	ep, _, h := s.rampRealHub(lt, lt.id, false)
	s.hub = h
	sess, err := s.rampOpen(lt, ep, "")
	if err != nil {
		return err
	}
	// Core's own checks: 20 hub checks and 5 Station canaries, all passed.
	base := time.Now().Add(-50 * time.Minute)
	for i := 0; i < 20; i++ {
		s.b.towerHubCheckOnce(base.Add(time.Duration(i) * canaryInterval))
	}
	for i := 0; i < 5; i++ {
		s.b.recordTowerHealth(lt.id, reputation.CanaryPass)
	}
	// Probes that say nothing about the Tower are not counted either way.
	s.b.recordTowerHealth(lt.id, reputation.StationFault)
	s.b.recordTowerHealth(lt.id, "aborted")
	// Paid work, badly: Core's record of paid-attempt outcomes.
	for i := 0; i < 30; i++ {
		s.b.recordOutcome(lt.id, "st-1", "paid-"+strconv.Itoa(i), reputation.Uncorroborated)
	}
	// The Tower talks about itself: heartbeats over its link.
	for i := 0; i < 3; i++ {
		code, raw := lt.call(s.t, s.srv, "/tower/session/heartbeat", jsonOf(s.t, frameFor(lt, sess)), nil)
		if code != http.StatusOK {
			return fmt.Errorf("heartbeat: %d %s", code, raw)
		}
	}
	return nil
}

func (s *rampState) passRateComputed() error { return nil }

func (s *rampState) onlyCoreOutcomesCount() error {
	rep := s.b.stageReport(mustTower(s.t, s.b, s.tw.id), time.Now())
	if fmt.Sprint(rep["passed"]) != "25" || fmt.Sprint(rep["failed"]) != "0" || rep["pass_rate"] != "1.000" {
		return fmt.Errorf("pass rate from %v passed %v failed (%v); want exactly Core's 25 checks", rep["passed"], rep["failed"], rep["pass_rate"])
	}
	if len(s.hub.seen()) != 20 {
		return fmt.Errorf("the hub saw %d checks, want 20", len(s.hub.seen()))
	}
	return nil
}

func (s *rampState) paidOutcomesFeedTier() error {
	now := time.Now()
	if tier := s.b.towerTier(s.tw.id, now, s.b.fleetTally(now)); tier != placement.TierB {
		return fmt.Errorf("a Tower with a perfect check record and bad paid work is tier %v, want B", tier)
	}
	// And placement acts on it: a clean Tower beside it wins the Station.
	setStage(s.t, s.b, s.tw.id, stageEstablished)
	clean, err := s.rampLive(signedInOperator(s.t, s.b, "ramp-clean"), "198.51.100.77:8443", "", stageEstablished, time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	for i := 0; i < 5; i++ {
		got, ok := s.b.placeStation("pk-"+strconv.Itoa(i), placement.Locality{}, nil)
		if !ok || got.towerID != clean.id {
			return fmt.Errorf("placement chose %q over the clean Tower %s", got.towerID, clean.id)
		}
	}
	setStage(s.t, s.b, s.tw.id, stageNew)
	return nil
}

func (s *rampState) noSelfReportRead() error {
	// Three heartbeats and the Tower's session claims left the counts exactly at Core's 25.
	p, f := s.rampHealth(s.tw.id, time.Now().Add(-24*time.Hour))
	if p != 25 || f != 0 {
		return fmt.Errorf("health evidence %d/%d after the Tower's own reports, want 25/0", p, f)
	}
	v, err := s.b.readStage(s.tw.id, time.Now())
	if err != nil {
		return err
	}
	if v.stage != stageNew || len(v.disq) != 0 {
		return fmt.Errorf("the stage read %+v", v)
	}
	return nil
}

// --- added 2026-11-14: each kind keeps its own run; one outage counts once; pre-record Towers ---

func (s *rampState) rampStreakTower() string {
	if s.tw.id == "" {
		s.op = signedInOperator(s.t, s.b, "ramp-streak")
		s.tw = enrolledTower(s.t, s.b, s.op.login)
	}
	return s.tw.id
}

func (s *rampState) hubPassesCanariesFailTwoIntervals() error {
	id := s.rampStreakTower()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 2; i++ {
		at := base.Add(time.Duration(i) * canaryInterval)
		s.b.recordHealthCheck(id, health.Check{OK: true, At: at, Source: health.SourceHubCheck, Slot: healthSlot(at), MinGap: healthMinGap})
		c := at.Add(time.Second)
		if i == 0 {
			s.firstCanary = c
		}
		s.b.recordHealthCheck(id, health.Check{OK: false, At: c, Source: health.SourceCanary, Slot: healthSlot(at), MinGap: healthMinGap})
	}
	return nil
}

func (s *rampState) computesFailStreak() error {
	sum, err := s.b.tower.health.Summary(s.rampStreakTower(), time.Now().Add(-24*time.Hour))
	s.streakSum = sum
	return err
}

func (s *rampState) streakIsFromCanaries(n int) error {
	if s.streakSum.FailStreak != n {
		return fmt.Errorf("streak %d, want %d", s.streakSum.FailStreak, n)
	}
	if !s.streakSum.StreakSince.Equal(s.firstCanary) {
		return fmt.Errorf("the run began at %v, not at the first canary failure %v", s.streakSum.StreakSince, s.firstCanary)
	}
	return nil
}

func (s *rampState) passesClearOnlyTheirOwnKind() error {
	id := s.rampStreakTower()
	// The next interval, while the canaries are still current (within health.RunStaleAfter).
	at := s.firstCanary.Add(2 * canaryInterval)
	s.b.recordHealthCheck(id, health.Check{OK: true, At: at, Source: health.SourceHubCheck, Slot: healthSlot(at), MinGap: healthMinGap})
	if sum, _ := s.b.tower.health.Summary(id, at.Add(-24*time.Hour)); sum.FailStreak != 2 {
		return fmt.Errorf("a hub pass cleared canary failures: streak %d", sum.FailStreak)
	}
	at = at.Add(canaryInterval)
	s.b.recordHealthCheck(id, health.Check{OK: false, At: at, Source: health.SourceHubCheck, Slot: healthSlot(at), MinGap: healthMinGap})
	s.b.recordHealthCheck(id, health.Check{OK: true, At: at.Add(time.Second), Source: health.SourceCanary, Slot: healthSlot(at), MinGap: healthMinGap})
	if sum, _ := s.b.tower.health.Summary(id, at.Add(-24*time.Hour)); sum.FailStreak != 1 {
		return fmt.Errorf("after a canary pass and a hub failure the streak is %d, want 1 (the hub run)", sum.FailStreak)
	}
	return nil
}

func (s *rampState) hubAndCanaryFailOneSweep() error {
	id := s.rampStreakTower()
	now := time.Now()
	s.b.sweepSlot.Store(healthSlot(now))
	defer s.b.sweepSlot.Store(0)
	s.b.recordHealthCheck(id, health.Check{OK: false, At: now, Source: health.SourceHubCheck, Slot: s.b.slotFor(now), MinGap: healthMinGap})
	s.b.recordTowerHealth(id, reputation.CanaryFail)
	return nil
}

func (s *rampState) twoInstancesOneBlip() error {
	id := s.rampStreakTower()
	peer := &broker{tower: s.b.tower, db: s.b.db, instanceID: "ramp-peer"}
	boundary := time.Unix(0, (time.Now().UnixNano()/int64(canaryInterval)+1)*int64(canaryInterval))
	a, b := boundary.Add(-2*time.Second), boundary.Add(3*time.Second)
	s.b.recordHealthCheck(id, health.Check{OK: false, At: a, Source: health.SourceCanary, Slot: healthSlot(a), MinGap: healthMinGap})
	peer.recordHealthCheck(id, health.Check{OK: false, At: b, Source: health.SourceCanary, Slot: healthSlot(b), MinGap: healthMinGap})
	if healthSlot(a) == healthSlot(b) {
		return fmt.Errorf("fixture: the two records must fall in different intervals")
	}
	return nil
}

func (s *rampState) streakIs(n int) error {
	if s.streakSum.FailStreak != n {
		return fmt.Errorf("streak %d, want %d", s.streakSum.FailStreak, n)
	}
	return nil
}

func (s *rampState) activeTowerNoApprovalNoStage() error {
	s.op = signedInOperator(s.t, s.b, "ramp-prerecord")
	s.tw = enrolledTower(s.t, s.b, s.op.login)
	if err := s.b.tower.registry.Transition(s.tw.id, admit.StateActive); err != nil {
		return err
	}
	if _, found, _ := s.b.tower.registry.FindTransition(s.tw.id, admit.StateQuarantine, admit.StateActive); found {
		return fmt.Errorf("fixture: an approval is on record")
	}
	if len(s.rampStageEvents(s.tw.id)) != 0 {
		return fmt.Errorf("fixture: a stage is on record")
	}
	s.sweptAt = time.Now()
	return nil
}

func (s *rampState) preRecordNewEvent() error {
	evs := s.rampStageEvents(s.tw.id)
	if len(evs) != 1 || evs[0].From != "" || evs[0].To != "new" || evs[0].Reason != "no approval on record" {
		return fmt.Errorf("stage events %+v", evs)
	}
	entered, ok := timeOf(evs[0].Detail["entered_at"])
	if !ok || entered.Before(s.sweptAt.Add(-time.Second)) || entered.After(time.Now()) {
		return fmt.Errorf("the stage counts from %v, not from that sweep", evs[0].Detail["entered_at"])
	}
	if rep := s.b.stageReport(mustTower(s.t, s.b, s.tw.id), time.Now()); rep == nil || rep["stage"] != "new" {
		return fmt.Errorf("the stage report says %v", rep)
	}
	return nil
}

func TestTowerTrustRampBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			s := &rampState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.rampReset()
				return ctx, nil
			})
			// the hub check
			sc.Step(`^a live Tower advertising a hub and holding no Stations$`, s.liveTowerWithHubNoStations)
			sc.Step(`^the hub check runs$`, s.hubCheckRuns)
			sc.Step(`^Core dials the hub, verifies the pin if one is advertised, and verifies the hub's signed identity proof over a fresh nonce against the admitted Tower key$`, s.hubDialedPinAndProof)
			sc.Step(`^records a "hub_check" outcome of pass or fail with the round-trip time$`, s.hubOutcomeWithRTT)
			sc.Step(`^runs at the canary interval, never more often per Tower$`, s.hubPacedAtCanaryInterval)
			sc.Step(`^a live Tower whose hub "([^"]*)"$`, s.liveTowerWhoseHub)
			sc.Step(`^the outcome is a fail with reason "([^"]*)"$`, s.outcomeIsFail)
			sc.Step(`^a live Tower that advertises no hub$`, s.liveTowerNoHub)
			sc.Step(`^no outcome is recorded for it$`, s.noOutcomeRecorded)
			sc.Step(`^hub checks run$`, s.hubChecksRun)
			sc.Step(`^each outcome is stored where the ramp can count it over 30 days$`, s.outcomesCountableOver30Days)
			sc.Step(`^the governance record gets a "hub_check_failed" event only for a fail, paced by the existing per-Tower budget, never one event per pass$`, s.failEventsOnlyPaced)
			sc.Step(`^a hub check passes$`, s.aHubCheckPasses)
			sc.Step(`^a Tower whose hub checks pass while its Station canaries fail in two consecutive canary intervals$`, s.hubPassesCanariesFailTwoIntervals)
			sc.Step(`^Core computes its fail streak$`, s.computesFailStreak)
			sc.Step(`^the streak is (\d+), from the canaries$`, s.streakIsFromCanaries)
			sc.Step(`^a passing hub check does not clear canary failures, nor a passing canary hub-check failures$`, s.passesClearOnlyTheirOwnKind)
			sc.Step(`^a hub check and a canary of one Tower fail in the same sweep$`, s.hubAndCanaryFailOneSweep)
			sc.Step(`^two broker instances record a failure of one Tower less than half a canary interval apart, across an interval boundary$`, s.twoInstancesOneBlip)
			sc.Step(`^the streak is (\d+)$`, s.streakIs)
			sc.Step(`^an active Tower whose record holds no approval and no stage$`, s.activeTowerNoApprovalNoStage)
			sc.Step(`^a "trust_stage" event is recorded with from "" to "new", reason "no approval on record", and the stage counts from that sweep$`, s.preRecordNewEvent)
			sc.Step(`^no Station is contacted, no grant is minted, no hold is placed and nobody is paid$`, s.noWorkNoMoney)
			// computing the stage
			sc.Step(`^a Tower moves from quarantine to active for the first time$`, s.movesQuarantineToActive)
			sc.Step(`^Core computes its stage$`, s.sweepComputes)
			sc.Step(`^the stage is "([^"]*)"$`, s.stageIs)
			sc.Step(`^a "trust_stage" event is recorded with from "" to "new" and actor "system"$`, s.firstStageEvent)
			sc.Step(`^a Tower has been in "new" for 7 days with a clean record and 50 passed health checks at 96%$`, s.inNew7Days50At96)
			sc.Step(`^the governance sweep computes stages$`, s.sweepComputes)
			sc.Step(`^the stage becomes "([^"]*)"$`, s.stageBecomes)
			sc.Step(`^a "trust_stage" event is recorded naming the evidence: days, health checks passed, pass rate$`, s.evidenceEvent)
			sc.Step(`^a Tower in "new" with "([^"]*)"$`, s.inNewWithShortfall)
			sc.Step(`^the stage stays "([^"]*)"$`, s.stageStays)
			sc.Step(`^a Tower has been in "proving" for 30 days, clean, at a 98\.5% health-check pass rate$`, s.proving30DaysAt985)
			sc.Step(`^a Tower in "new" whose record would satisfy "established"$`, s.newSatisfyingEstablished)
			sc.Step(`^the stage becomes "proving", and "established" needs its own 30 days from there$`, s.oneStepThenOwn30Days)
			sc.Step(`^a Tower in "([^"]*)"$`, s.towerIn)
			sc.Step(`^"([^"]*)" happens$`, s.eventHappens)
			sc.Step(`^its stage becomes "new" in the same write that records the event$`, s.demotedInSameWrite)
			sc.Step(`^a "trust_stage" event is recorded naming the cause$`, s.stageEventNamesCause)
			sc.Step(`^Stations above the "new" caps become due to move with trigger "stage_cap"$`, s.overCapDueStageCap)
			sc.Step(`^a Tower in any stage is revoked$`, s.anyStageRevoked)
			sc.Step(`^it has no stage and no Stations$`, s.noStageNoStations)
			sc.Step(`^a revoked Tower readmitted later starts at "new"$`, s.readmittedStartsNew)
			// excusals and admin actions
			sc.Step(`^a Tower's lease lapsed because of a Core-side defect$`, s.leaseLapsedCoreDefect)
			sc.Step(`^an admin records an excusal for that lapse with a reason$`, s.adminExcusesLapse)
			sc.Step(`^the excusal is an attributed governance event naming the admin and the excused event$`, s.excusalAttributed)
			sc.Step(`^that lapse no longer counts against "clean" or as a demotion cause$`, s.lapseNoLongerCounts)
			sc.Step(`^an admin records an excusal without naming one specific recorded event, or without a reason$`, s.blankOrGeneralExcusal)
			sc.Step(`^it is refused$`, s.itIsRefused)
			sc.Step(`^nothing is excused$`, s.nothingExcused)
			sc.Step(`^an admin sets a ceiling on a Tower's stage with a reason$`, s.adminSetsCeiling)
			sc.Step(`^the Tower's stage is the lower of its computed stage and the ceiling$`, s.lowerOfComputedAndCeiling)
			sc.Step(`^no admin action can set a stage higher than the computed one$`, s.noAdminRaise)
			sc.Step(`^an admin records an incident against a Tower with a reason$`, s.adminRecordsIncident)
			sc.Step(`^the Tower is demoted to "new" as any demotion is$`, s.demotedAsAnyDemotion)
			sc.Step(`^the incident is an attributed governance event$`, s.incidentAttributed)
			// what the stage controls
			sc.Step(`^a Tower in "new" holds 10 live community Stations$`, s.newHolds10)
			sc.Step(`^an 11th community Station is placed$`, s.eleventhPlaced)
			sc.Step(`^that Tower is not a candidate$`, s.thatTowerNotCandidate)
			sc.Step(`^only a "new" community Tower is eligible$`, s.onlyNewCommunityEligible)
			sc.Step(`^a house Station is placed$`, s.houseStationPlaced)
			sc.Step(`^it is not placed there$`, s.notPlacedThere)
			sc.Step(`^a "proving" community Tower and no eligible house Tower$`, s.provingNoHouseTower)
			sc.Step(`^it may go to the community Tower, up to its house cap of 5$`, s.mayGoUpToHouseCap)
			sc.Step(`^when a house Tower becomes eligible again, those Stations become due to move back with trigger "better_tower"$`, s.houseBackDueBetterTower)
			sc.Step(`^a Station behind a "new" Tower is a candidate for a request alongside the direct path$`, s.stationBehindNewTower)
			sc.Step(`^the existing capacity-weighted coin decides which goes first$`, s.coinDecides)
			sc.Step(`^the Tower side's weight is multiplied by 0\.25$`, s.weightTimesQuarter)
			sc.Step(`^for "proving" by 0\.5, and for "established" by 1\.0$`, s.weightProvingEstablished)
			sc.Step(`^everything else in the merge \(provider order, sort, tier gate, curated home first\) runs unchanged and before the coin$`, s.mergeUnchangedBeforeCoin)
			sc.Step(`^a request goes through a Tower at any stage$`, s.requestThroughAnyStage)
			sc.Step(`^the Station, Tower and platform shares are exactly as today$`, s.sharesAsToday)
			// house Towers
			sc.Step(`^a house Tower$`, s.aHouseTower)
			sc.Step(`^placement or the coin consults its stage$`, s.consultsStage)
			sc.Step(`^it is treated as having no stage caps and an edge weight of 1\.0$`, s.noCapsWeightOne)
			sc.Step(`^it still must be live, healthy and under capacity like any Tower$`, s.liveHealthyUnderCapacity)
			sc.Step(`^it gets no ranking advantage for community Stations \(placement_ranking\.feature\)$`, s.noRankingAdvantage)
			// who sees the stage
			sc.Step(`^an admin opens a Tower's detail$`, s.adminOpensDetail)
			sc.Step(`^it shows the stage, when it was entered, the health checks passed and pass rate, the days counted, any excusal, ceiling or incident, and what the next stage still needs$`, s.detailShowsEverything)
			sc.Step(`^a Tower's operator views their Tower$`, s.operatorViews)
			sc.Step(`^they see its stage, the caps that stage carries, and what the next stage still needs, in plain words$`, s.operatorSeesStage)
			sc.Step(`^they do not see other Towers' stages or the governance record itself \(admin only, ruled 2026-11-13\)$`, s.operatorSeesNoOthers)
			// adversarial
			sc.Step(`^an owner's Tower was demoted or revoked$`, s.ownerDemotedOrRevoked)
			sc.Step(`^the same owner enrolls a new Tower$`, s.sameOwnerEnrolls)
			sc.Step(`^the new Tower starts at "new" like any other$`, s.newTowerStartsNew)
			sc.Step(`^the owner's per-account Tower quota still applies$`, s.quotaStillApplies)
			sc.Step(`^one owner runs three "proving" Towers$`, s.threeProvingOneOwner)
			sc.Step(`^house Stations fail over$`, s.houseFailsOver)
			sc.Step(`^that owner's Towers hold at most 5 house Stations in total$`, s.ownerHoldsAtMost5House)
			sc.Step(`^a Tower that answers canaries well and paid work badly$`, s.canariesWellPaidBadly)
			sc.Step(`^a Tower's pass rate is computed$`, s.passRateComputed)
			sc.Step(`^only Core's own health-check outcomes count toward the pass rate$`, s.onlyCoreOutcomesCount)
			sc.Step(`^Core's own record of paid-attempt outcomes keeps feeding the health tier, as it does today$`, s.paidOutcomesFeedTier)
			sc.Step(`^no number the Tower reports about itself is read$`, s.noSelfReportRead)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"../../features/tower/tower_trust_ramp.feature"},
			Strict: true, TestingT: t,
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("the trust ramp spec is not satisfied")
	}
}
