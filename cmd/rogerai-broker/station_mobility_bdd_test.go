package main

// station_mobility_bdd_test.go makes features/tower/station_mobility.feature EXECUTABLE against the
// real mover (towermobility.go), the real attach handler, the real cancel long-poll, the real
// governance record and the real classic relay.
//
// The rig is one broker instance (two where a scenario is about two) over the production route
// table (b.routes()), sharing every durable store a second instance would share in production:
// the Tower registry, the Station registry, the link mirror, health evidence and the mobility
// store. A second instance is a second broker over those same stores with its own link sessions,
// and both instances then share one Valkey (miniredis) for the cancel long-poll, the mobility
// lease and the peer load view.
//
// The node's half of a move (it hears the nudge, finishes its work, acts once a minute, and prints
// where it went in `roger share`) lives in unexported internal/agent code, so the three scenarios
// about it run from internal/agent/station_mobility_bdd_test.go instead.
//
// "An attempt caught by a move is void" (line 242) runs against the real settle handler. It was
// held out at first because the handler refused Tower A's receipt with a 403 before the epoch
// fence could answer; the handler now lets the Tower the attempt was granted to reach the fence.
//
// Two rationale lines of the file (151, 253) were not Gherkin; the founder approved making them
// comments on 2026-11-13, so the runner reads the approved file directly.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cucumber/godog"
	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/store"
	"rogerai.fm/roger/v7/internal/towercore/admit"
	"rogerai.fm/roger/v7/internal/towercore/attach"
	"rogerai.fm/roger/v7/internal/towercore/cert"
	"rogerai.fm/roger/v7/internal/towercore/dispatch"
	"rogerai.fm/roger/v7/internal/towercore/enroll"
	"rogerai.fm/roger/v7/internal/towercore/fleet"
	"rogerai.fm/roger/v7/internal/towercore/head"
	"rogerai.fm/roger/v7/internal/towercore/health"
	"rogerai.fm/roger/v7/internal/towercore/link"
	"rogerai.fm/roger/v7/internal/towercore/mobility"
	"rogerai.fm/roger/v7/internal/towercore/placement"
)

const mobFeatureFile = "../../features/tower/station_mobility.feature"

// The one scenario that needs a link freshness window a test can wait out. Everything else runs
// on the production link configuration.
const (
	mobHeartbeat = 500 * time.Millisecond
	mobFreshness = 3 * time.Second
)

// Endpoints. A is in AF, B and C in EU (testNetTable); S is observed in B's network, so B is a
// strictly better class for it than A (far) - the arrangement newMovingRig uses.
const (
	mobEndpointA = "192.0.2.9:8443"
	mobEndpointB = "198.51.100.9:8443"
	mobEndpointC = "198.51.100.77:8443"
	mobEndpointD = "203.0.113.50:8443"
	mobBucketB   = "198.51.100.0/24"
)

// mobFeature is the approved file, read as it is.
func mobFeature(t *testing.T) string {
	t.Helper()
	return mobFeatureFile
}

// mobRig is Station S live on Tower A at epoch 4, Tower B up after it attached, and every store a
// second instance would share.
type mobRig struct {
	t        *testing.T
	db       *store.Mem
	registry admit.Store
	custody  cert.Custody
	enrolls  enroll.Store
	deps     linkDeps
	short    bool

	b    *broker
	srv  *httptest.Server
	peer *broker
	vs   *valkeyStore
	mr   *miniredis.Miniredis

	a, bt     linkTower
	opA, opB  operator
	aSess     string
	bSess     string
	aLastSeen time.Time

	node       operator
	body       map[string]any
	station    string
	nodeID     string
	assertPriv ed25519.PrivateKey
	hubToken   string // the hub token the latest attach answer carried
	declared   bool   // Facts.Nudge as the attach handler recorded it
}

// instance is one broker over the rig's shared stores, serving the production route table.
func (r *mobRig) instance(id string) (*broker, *httptest.Server) {
	r.t.Helper()
	b := relayBroker(r.db)
	b.adminKey = "admin-secret"
	b.netTable = testNetTable(r.t)
	b.instanceID = id
	ts, err := newTowerSubsystem(b, r.registry, r.custody, r.enrolls, cert.Config{TTL: time.Hour}, r.deps)
	require.NoError(r.t, err)
	if r.short {
		ts.link = link.New(link.Config{Network: link.PublicNetwork,
			Versions:  []int{towerProtocolMin, towerProtocolMax},
			Heartbeat: mobHeartbeat, Freshness: mobFreshness, Mirror: r.deps.mirror})
	}
	b.tower = ts
	srv := httptest.NewServer(b.routes())
	r.t.Cleanup(srv.Close)
	return b, srv
}

// liveTower enrolls, approves and links a Tower, established so the trust ramp's caps bound
// nothing a scenario is not about.
func (r *mobRig) liveTower(login, endpoint, pin string) (linkTower, operator, string) {
	r.t.Helper()
	op := signedInOperator(r.t, r.b, login)
	lt := enrolledTower(r.t, r.b, op.login)
	require.NoError(r.t, r.b.tower.registry.Transition(lt.id, admit.StateActive))
	setStage(r.t, r.b, lt.id, stageEstablished)
	return lt, op, r.openSession(lt, r.srv, endpoint, pin)
}

func (r *mobRig) openSession(lt linkTower, srv *httptest.Server, endpoint, pin string) string {
	r.t.Helper()
	var acc link.Accepted
	code, raw := lt.call(r.t, srv, "/tower/session", jsonOf(r.t, link.Hello{
		Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
		Capabilities: mandatoryCaps(), RelayEndpoint: endpoint, RelayTLSSPKI: pin,
	}), &acc)
	require.Equal(r.t, http.StatusOK, code, raw)
	return acc.SessionID
}

func (r *mobRig) closeSession(lt linkTower, sess string) {
	r.t.Helper()
	code, raw := lt.call(r.t, r.srv, "/tower/session/close", jsonOf(r.t, link.Frame{
		Network: link.PublicNetwork, Version: 1, TowerID: lt.id, SessionID: sess}), nil)
	require.Equal(r.t, http.StatusOK, code, raw)
}

func (r *mobRig) heartbeat(lt linkTower, sess string) {
	r.t.Helper()
	code, raw := lt.call(r.t, r.srv, "/tower/session/heartbeat", jsonOf(r.t, link.Frame{
		Network: link.PublicNetwork, Version: 1, TowerID: lt.id, SessionID: sess}), nil)
	require.Equal(r.t, http.StatusOK, code, raw)
}

// attach sends body to Core's attach route, signed by its owner and co-signed by its assertion key.
func (r *mobRig) attach(op operator, body map[string]any) (int, map[string]any, string) {
	r.t.Helper()
	var out map[string]any
	code, raw := op.attach(r.t, r.srv, body, &out)
	return code, out, raw
}

// reattach is S's node attaching again with its own station id and both keys.
func (r *mobRig) reattach() (int, map[string]any, string) {
	r.t.Helper()
	code, out, raw := r.attach(r.node, r.body)
	if code == http.StatusOK {
		r.hubToken, _ = out["hub_token"].(string)
	}
	return code, out, raw
}

func newMobRig(t *testing.T, short bool) *mobRig {
	t.Helper()
	r := &mobRig{t: t, db: store.NewMem(), short: short,
		registry: admit.NewMemStore(), custody: cert.NewMemCustody(), enrolls: enroll.NewMemStore(),
		deps: linkDeps{stations: attach.NewMemStore(), heads: head.NewMemStore(), mirror: link.NewMemMirror(),
			health: health.NewMemStore(), mobility: mobility.NewMemStore(), routable: fleet.NewMemStore()}}
	r.b, r.srv = r.instance("i-1")
	r.a, r.opA, r.aSess = r.liveTower("op-a", mobEndpointA, "")
	r.aLastSeen = time.Now()

	r.node = signedInOperator(t, r.b, "node-op")
	body, _ := selfAttachBodyFor(t, r.b, r.node)
	body["nudge"] = true
	code, out, raw := r.attach(r.node, body)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, r.a.id, out["tower_id"])
	r.body, r.station, r.nodeID = body, out["station_id"].(string), body["node_id"].(string)
	r.body["station_id"] = r.station // every later attach names its own station id
	r.hubToken, _ = out["hub_token"].(string)
	priv, ok := assertionKeyOf(body["assertion_key"].(string))
	require.True(t, ok)
	r.assertPriv = priv

	// Epoch 4: the idle sweep sets S dormant and S's node attaches again, three times, each a
	// revival through Admit (the epoch's only writer). Only Tower A exists, so S stays on A.
	for r.attachment().Epoch < 4 {
		swept, err := r.b.tower.stationStore.DetachIdle(r.a.id, time.Now().Add(time.Hour))
		require.NoError(t, err)
		require.Contains(t, swept, r.station)
		code, out, raw := r.reattach()
		require.Equal(t, http.StatusOK, code, raw)
		require.Equal(t, r.a.id, out["tower_id"])
	}

	r.bt, r.opB, r.bSess = r.liveTower("op-b", mobEndpointB, testHubPin())
	waitMeasured(t, r.b, r.a.id)
	waitMeasured(t, r.b, r.bt.id)
	r.b.noteCancelCapable(r.nodeID)
	f, _, err := r.b.tower.mobility.Facts(r.station)
	require.NoError(t, err)
	r.declared = f.Nudge
	r.placeInB()
	if short {
		r.heartbeat(r.a, r.aSess)
		r.aLastSeen = time.Now()
		r.heartbeat(r.bt, r.bSess)
	}
	return r
}

// placeInB records S as observed inside B's network (so B is a strictly better class than A),
// keeping what the attach handler recorded about the nudge.
func (r *mobRig) placeInB() {
	r.t.Helper()
	f, _, err := r.b.tower.mobility.Facts(r.station)
	require.NoError(r.t, err)
	require.NoError(r.t, r.b.tower.mobility.PutFacts(r.station, mobility.Facts{
		Locality: placement.Locality{Bucket: mobBucketB, Continent: "EU"}, Nudge: f.Nudge}))
}

func (r *mobRig) attachment() attach.Attachment {
	r.t.Helper()
	at, ok, err := r.b.tower.stations.Station(r.station)
	require.NoError(r.t, err)
	require.True(r.t, ok)
	return at
}

func (r *mobRig) streak(b *broker) moveStreak {
	b.moveMu.Lock()
	defer b.moveMu.Unlock()
	return b.moveStreaks[r.station]
}

// multi turns the rig into two instances sharing one Valkey, the way production runs.
func (r *mobRig) multi() {
	r.t.Helper()
	if r.vs != nil {
		return
	}
	r.vs, r.mr = testValkeyShared(r.t)
	r.peer, _ = r.instance("i-2")
	for _, b := range []*broker{r.b, r.peer} {
		b.multiInstance, b.shared = true, r.vs
	}
	r.b.noteCancelCapable(r.nodeID) // the capability now lives in the shared store
}

// sharedNudges drains the node's cancel buffer from the shared store and counts the nudges in it.
func (r *mobRig) sharedNudges() int {
	r.t.Helper()
	raws, err := r.vs.cancelDrain(r.nodeID)
	require.NoError(r.t, err)
	n := 0
	for _, raw := range raws {
		var e cancelEntry
		require.NoError(r.t, json.Unmarshal(raw, &e))
		if e.Reattach != "" {
			n++
		}
	}
	return n
}

// mobState is one scenario's state.
type mobState struct {
	t    *testing.T
	r    *mobRig
	at   time.Time // the clock the latest observation was recorded at; passes run on it
	logs *bytes.Buffer

	passes      int
	runPass     bool // "the move starts" is a When that still has to run the pass
	cmp         *bool
	clear       func()
	before      map[string]int
	results     []bool
	stale       []attach.Attachment
	reply       map[string]any
	prevToken   string
	pollBody    []byte
	epochs      []int64
	directID    string
	kindErr     error
	newOwners   []moveStreak
	emptyBefore []bool
	factsBefore mobility.Facts
	attemptID   string
	settleCode  int
	towerC      linkTower
	others      []string
	worker      *binNodeWorker
	consumer    ed25519.PrivateKey
	served      []string
	startedPass map[string]int
}

func (s *mobState) reset(t *testing.T) {
	if s.worker != nil {
		s.worker.close()
	}
	*s = mobState{t: t, logs: s.logs}
}

func (s *mobState) pass() {
	s.passes++
	s.r.b.mobilityPassOnce(s.at)
}

// observe records one health check of A at the next observation time and runs the pass, which
// is how an observation reaches the mover in production.
func (s *mobState) observe(ok bool) {
	s.sample(ok)
	s.pass()
}

// sample records one health check of A without a pass.
func (s *mobState) sample(ok bool) {
	s.at = s.at.Add(canaryInterval)
	s.r.b.recordHealthResult(s.r.a.id, ok, s.at)
}

func (s *mobState) started(towerID string) []admit.Event {
	return towerEventsOf(s.t, s.r.b, towerID, evStationMoveStarted)
}

func (s *mobState) startedFor(towerID, station string) []admit.Event {
	var out []admit.Event
	for _, ev := range s.started(towerID) {
		if ev.Detail["station"] == station {
			out = append(out, ev)
		}
	}
	return out
}

func (s *mobState) moveEvents(towerID string) int {
	return len(towerEventsOf(s.t, s.r.b, towerID, evStationMoveStarted, evStationMoved, evStationMovedAway,
		evStationMoveReturned, evStationMoveAbandoned))
}

func (s *mobState) nudges() int { return reattachNudges(s.r.b, s.r.nodeID) }

// --- Background ---------------------------------------------------------------------

func (s *mobState) background(epoch int) error {
	at := s.r.attachment()
	if !at.Live() || at.Origin.TowerID != s.r.a.id || at.Epoch != int64(epoch) || at.Origin.Kind != attach.OriginJoined {
		return fmt.Errorf("want S live, joined to A at epoch %d; got %+v", epoch, at)
	}
	s.at = time.Now()
	return nil
}

func (s *mobState) declaredNudge() error {
	if !s.r.declared {
		return fmt.Errorf("the attach handler did not record the node's nudge declaration")
	}
	if !s.r.b.cancelCapable(s.r.nodeID) {
		return fmt.Errorf("the node holds no cancel long-poll for the nudge to ride on")
	}
	return nil
}

// --- shared When / Then ------------------------------------------------------------

func (s *mobState) passRuns() error { s.pass(); return nil }

func (s *mobState) dueWith(trigger string) error {
	evs := s.startedFor(s.r.a.id, s.r.station)
	if len(evs) != 1 || evs[0].Reason != trigger {
		return fmt.Errorf("want one station_move_started %q for S on A, got %+v", trigger, evs)
	}
	at := s.r.attachment()
	if at.State != attach.StateDormant {
		return fmt.Errorf("S is %s, not dormant for its move", at.State)
	}
	mv, moving, err := s.r.b.tower.mobility.Move(s.r.station)
	if err != nil || !moving || mv.Trigger != trigger {
		return fmt.Errorf("want a move in progress with trigger %q, got %+v moving=%v err=%v", trigger, mv, moving, err)
	}
	return nil
}

func (s *mobState) dueWithTarget(trigger string) error {
	if err := s.dueWith(trigger); err != nil {
		return err
	}
	mv, _, _ := s.r.b.tower.mobility.Move(s.r.station)
	if mv.To != s.r.bt.id {
		return fmt.Errorf("the move targets %q, want B %q", mv.To, s.r.bt.id)
	}
	if to := s.startedFor(s.r.a.id, s.r.station)[0].Detail["to"]; to != s.r.bt.id {
		return fmt.Errorf("the event names target %v, want B", to)
	}
	return nil
}

func (s *mobState) noObservationWait() error {
	sum, err := s.r.b.tower.health.Summary(s.r.a.id, time.Time{})
	if err != nil {
		return err
	}
	if sum.Passed+sum.Failed != 0 {
		return fmt.Errorf("the scenario recorded %d health observations; the move must not have needed any", sum.Passed+sum.Failed)
	}
	if s.passes != 1 {
		return fmt.Errorf("the move took %d passes, want the first", s.passes)
	}
	return nil
}

func (s *mobState) notDue() error {
	at := s.r.attachment()
	if !at.Live() || at.Origin.TowerID != s.r.a.id || at.Epoch != 4 {
		return fmt.Errorf("S left its place: %+v", at)
	}
	if evs := s.started(s.r.a.id); len(evs) != 0 {
		return fmt.Errorf("a move started: %+v", evs)
	}
	if _, moving, _ := s.r.b.tower.mobility.Move(s.r.station); moving {
		return fmt.Errorf("a move is in progress")
	}
	if st := s.r.streak(s.r.b); st.count >= betterObservations {
		return fmt.Errorf("S's streak is at %d, which is due", st.count)
	}
	return nil
}

// --- triggers -------------------------------------------------------------------

func (s *mobState) towerAbsent() error {
	if !s.r.short {
		return fmt.Errorf("fixture: this scenario needs the short link window")
	}
	// B keeps heartbeating; A sends nothing for longer than the freshness window.
	deadline := s.r.aLastSeen.Add(mobFreshness + 500*time.Millisecond)
	for time.Now().Before(deadline) {
		s.r.heartbeat(s.r.bt, s.r.bSess)
		time.Sleep(250 * time.Millisecond)
	}
	s.r.heartbeat(s.r.bt, s.r.bSess)
	if since := time.Since(s.r.aLastSeen); since <= mobFreshness {
		return fmt.Errorf("A was absent %v, not longer than the window %v", since, mobFreshness)
	}
	live, err := s.r.b.tower.link.LiveTowersChecked()
	if err != nil {
		return err
	}
	if len(live) != 1 || live[0] != s.r.bt.id {
		return fmt.Errorf("the fleet-wide live set is %v, want only B", live)
	}
	// It left the live set when its last heartbeat was one window old; the pass runs a further
	// window after that, so A has been ABSENT from the live set longer than the window.
	s.at = s.r.aLastSeen.Add(2*s.r.b.tower.link.Freshness() + time.Second)
	return nil
}

func (s *mobState) briefDropout() error {
	start := time.Now()
	s.r.closeSession(s.r.a, s.r.aSess)
	// A pass runs WHILE A is away (audit finding: without one, this scenario could not catch a
	// mover that treats any absence as gone).
	s.r.b.mobilityPassOnce(time.Now())
	if len(s.startedFor(s.r.a.id, s.r.station)) != 0 {
		return fmt.Errorf("a pass during a brief absence started a move")
	}
	s.r.aSess = s.r.openSession(s.r.a, s.r.srv, mobEndpointA, "")
	if el := time.Since(start); el >= towerFreshnessWindow {
		return fmt.Errorf("the new session took %v, not within the window %v", el, towerFreshnessWindow)
	}
	if !s.r.b.tower.link.Live(s.r.a.id) {
		return fmt.Errorf("A is not live after its new session")
	}
	s.at = time.Now()
	return nil
}

func (s *mobState) twoFailed() error {
	s.sample(false)
	s.sample(false)
	return nil
}

func (s *mobState) failThenPass() error {
	s.sample(true)
	s.sample(false)
	return nil
}

// threeBetter: two observations favoring B already counted by passes, and the third sample in;
// the scenario's own pass counts it.
func (s *mobState) threeBetter() error {
	s.observe(true)
	s.observe(true)
	if st := s.r.streak(s.r.b); st.count != 2 || st.target != s.r.bt.id {
		return fmt.Errorf("after two observations the streak is %+v, want 2 for B", st)
	}
	s.sample(true)
	return nil
}

// What counts as strictly better: the decision function the pass calls (placement.StrictlyBetter),
// fed candidates whose classes come from placement.ClassOf over real localities.
func (s *mobState) onAWithCandidate() error {
	if !s.r.b.tower.link.Live(s.r.bt.id) || s.r.attachment().Origin.TowerID != s.r.a.id {
		return fmt.Errorf("fixture: S on A with B live")
	}
	return nil
}

func mobClass(where string) placement.Class {
	station := placement.Locality{Bucket: mobBucketB, Continent: "EU"}
	towers := map[string]placement.Locality{
		"same_network":   {Bucket: mobBucketB, Continent: "EU"},
		"same_continent": {Bucket: "198.51.200.0/24", Continent: "EU"},
		"far":            {Bucket: "203.0.113.0/24", Continent: "NA"},
		"unknown":        {},
	}
	return placement.ClassOf(station, towers[where])
}

type mobCmp struct {
	b, a  placement.Candidate
	house bool
}

// mobComparisons are the outline's rows, each as the candidates it describes. A row whose words
// leave tier or class open is checked with both an equal and a worse value, so the verdict does
// not lean on a value the row never stated.
func mobComparisons() map[string][]mobCmp {
	A, B := placement.TierA, placement.TierB
	net, cont, far, unk := mobClass("same_network"), mobClass("same_continent"), mobClass("far"), mobClass("unknown")
	c := func(house bool, tier placement.Tier, class placement.Class, load float64) placement.Candidate {
		return placement.Candidate{House: house, Tier: tier, Class: class, Load: load}
	}
	return map[string][]mobCmp{
		"B is health tier A and A is tier B":                   {{c(false, A, cont, 0), c(false, B, cont, 0), false}},
		"same tier, B is same_network and A is same_continent": {{c(false, A, net, 0), c(false, A, cont, 0), false}},
		"same tier, B is same_continent and A is far":          {{c(false, A, cont, 0), c(false, A, far, 0), false}},
		"same tier, B is same_continent and A is unknown":      {{c(false, A, cont, 0), c(false, A, unk, 0), false}},
		"same tier, same class, B is less loaded":              {{c(false, A, cont, 0.1), c(false, A, cont, 0.9), false}},
		"B is nearer but tier B, and A is tier A":              {{c(false, B, net, 0), c(false, A, far, 0), false}},
		"S is a house Station, B is a house Tower, A is a community Tower": {
			{c(true, A, cont, 0), c(false, A, cont, 0), true},
			{c(true, B, far, 0), c(false, A, net, 0), true}},
		"S is a community Station, B is a community Tower, A is a house Tower, same tier and class": {
			{c(false, A, cont, 0), c(true, A, cont, 0), false}},
		"S is a community Station, B is a house Tower, A is a community Tower, same tier and class": {
			{c(true, A, cont, 0), c(false, A, cont, 0), false}},
	}
}

func (s *mobState) verdict(want string) error {
	if s.cmp == nil {
		return fmt.Errorf("no comparison was made")
	}
	if got := map[bool]string{true: "is", false: "is not"}[*s.cmp]; got != want {
		return fmt.Errorf("B %s strictly better, want %s", got, want)
	}
	return nil
}

// condition makes one of the idle-gate outline's rows true, and records how it ends.
func (s *mobState) condition(c string) error {
	r := s.r
	switch c {
	case "S has a classic request in flight on this instance":
		r.b.metricsMu.Lock()
		r.b.inflight[r.nodeID] = 1
		r.b.metricsMu.Unlock()
		s.clear = func() {
			r.b.metricsMu.Lock()
			r.b.inflight[r.nodeID] = 0
			r.b.metricsMu.Unlock()
		}
	case "S has an edge attempt open on this instance":
		r.b.edgeEnterInflight("att-mob-local", r.nodeID, "acct-mob", time.Now().Add(time.Hour))
		s.clear = func() { r.b.edgeExitInflight("att-mob-local") }
	case "a peer instance reports load for S":
		r.multi()
		r.peer.edgeEnterInflight("att-mob-peer", r.nodeID, "acct-mob", time.Now().Add(time.Hour))
		r.b.mergeSharedInflight()
		r.b.metricsMu.Lock()
		peer := r.b.peerEdgeLoad[r.nodeID]
		r.b.metricsMu.Unlock()
		if peer != 1 {
			return fmt.Errorf("the peer's attempt is not in this instance's view (peer load %d)", peer)
		}
		s.clear = func() {
			r.peer.edgeExitInflight("att-mob-peer")
			r.b.mergeSharedInflight()
		}
	case "the peer load view is older than its freshness bound":
		r.multi()
		r.b.mergeSharedInflight()
		// The last clean merge is older than two sync ticks: every tick since failed.
		r.b.metricsMu.Lock()
		r.b.peerLoadAt = time.Now().Add(-2 * peerLoadFreshness())
		r.b.metricsMu.Unlock()
		s.clear = func() { r.b.mergeSharedInflight() }
	case "the shared store cannot be read":
		r.multi()
		r.mr.SetError("ERR the shared store is unreachable")
		r.b.mergeSharedInflight() // the sync tick runs, and cannot read the store
		r.b.metricsMu.Lock()
		stamped := !r.b.peerLoadAt.IsZero()
		r.b.metricsMu.Unlock()
		if stamped {
			return fmt.Errorf("a merge against an unreadable store stamped the peer view fresh")
		}
		s.clear = func() {
			r.mr.SetError("")
			r.b.mergeSharedInflight()
		}
	default:
		return fmt.Errorf("unknown row %q", c)
	}
	if ok, why := r.b.stationQuiescent(r.nodeID); ok {
		return fmt.Errorf("fixture: stationQuiescent still proves S idle (%s)", why)
	}
	return nil
}

// quoted dispatches the two outlines' bare "<value>" steps.
func (s *mobState) quoted(v string) error {
	if rows, ok := mobComparisons()[v]; ok {
		var got *bool
		for _, row := range rows {
			verdict := placement.StrictlyBetter(row.b, row.a, row.house)
			if got != nil && *got != verdict {
				return fmt.Errorf("the verdict changes with a value the row does not state")
			}
			got = &verdict
		}
		s.cmp = got
		return nil
	}
	return s.condition(v)
}

func (s *mobState) stageDemoted() error {
	r := s.r
	// S is a house Station; the community Stations below attach after it.
	makeHouse(s.t, r.b, r.node.login)
	r.closeSession(r.bt, r.bSess) // so the newcomers land on A, as they would have before B came up
	for i := 0; i < stageCaps[stageNew].community+1; i++ {
		op := signedInOperator(s.t, r.b, fmt.Sprintf("comm-%d-%s", i, randHex(3)))
		body, _ := selfAttachBodyFor(s.t, r.b, op)
		body["nudge"] = true
		code, out, raw := r.attach(op, body)
		if code != http.StatusOK || out["tower_id"] != r.a.id {
			return fmt.Errorf("community Station %d did not land on A: %d %s", i, code, raw)
		}
		r.b.noteCancelCapable(body["node_id"].(string))
		s.others = append(s.others, out["station_id"].(string))
		time.Sleep(2 * time.Millisecond) // distinct attach times: "newest" must mean something
	}
	r.bSess = r.openSession(r.bt, r.srv, mobEndpointB, testHubPin())
	waitMeasured(s.t, r.b, r.bt.id)
	setStage(s.t, r.b, r.a.id, stageNew)
	if stageCaps[stageNew].house != 0 {
		return fmt.Errorf("fixture assumes the new stage holds no house Stations")
	}
	return nil
}

func (s *mobState) newestOverCap(trigger string) error {
	got := map[string]string{}
	for _, ev := range s.started(s.r.a.id) {
		got[ev.Detail["station"].(string)] = ev.Reason
	}
	newest := s.others[len(s.others)-1]
	want := map[string]string{s.r.station: trigger, newest: trigger}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("moves started %v, want S (house, cap 0) and the newest community Station %s", got, newest)
	}
	for _, id := range s.others[:len(s.others)-1] {
		at, _, _ := s.r.b.tower.stations.Station(id)
		if !at.Live() || at.Origin.TowerID != s.r.a.id {
			return fmt.Errorf("an older community Station %s was not kept: %+v", id, at)
		}
	}
	return nil
}

// --- streaks ---------------------------------------------------------------------

func (s *mobState) passFasterThanChecks() error {
	if towerMobilityInterval >= canaryInterval {
		return fmt.Errorf("the pass (%v) is not more frequent than the health checks (%v)", towerMobilityInterval, canaryInterval)
	}
	return nil
}

func (s *mobState) threePassesOneSample() error {
	s.sample(true)
	for i := 0; i < 3; i++ {
		s.passes++
		s.r.b.mobilityPassOnce(s.at.Add(time.Duration(i) * towerMobilityInterval))
	}
	return nil
}

func (s *mobState) advancedOnce() error {
	if st := s.r.streak(s.r.b); st.count != 1 || st.target != s.r.bt.id {
		return fmt.Errorf("the streak is %+v, want 1 for B", st)
	}
	return nil
}

func (s *mobState) advancesOnlyOnNewer() error {
	// A sample stamped no later than the one counted does not advance it.
	s.r.b.recordHealthResult(s.r.a.id, true, s.at)
	s.r.b.recordHealthResult(s.r.a.id, true, s.at.Add(-time.Minute))
	s.r.b.mobilityPassOnce(s.at.Add(time.Minute))
	if st := s.r.streak(s.r.b); st.count != 1 {
		return fmt.Errorf("a sample no newer than the last counted moved the streak to %d", st.count)
	}
	s.observe(true)
	if st := s.r.streak(s.r.b); st.count != 2 {
		return fmt.Errorf("a newer sample left the streak at %d, want 2", st.count)
	}
	return nil
}

func (s *mobState) twoFavoringB() error {
	s.observe(true)
	s.observe(true)
	if st := s.r.streak(s.r.b); st.count != 2 || st.target != s.r.bt.id {
		return fmt.Errorf("the streak is %+v, want 2 for B", st)
	}
	return nil
}

func (s *mobState) nextNotFavoringB() error {
	s.r.closeSession(s.r.bt, s.r.bSess) // B is no longer there to favor
	s.observe(true)
	return nil
}

func (s *mobState) streakZero() error {
	s.r.b.moveMu.Lock()
	st, ok := s.r.b.moveStreaks[s.r.station]
	s.r.b.moveMu.Unlock()
	if ok || st.count != 0 {
		return fmt.Errorf("the streak survived: %+v", st)
	}
	if !s.r.attachment().Live() {
		return fmt.Errorf("S moved")
	}
	return nil
}

func (s *mobState) newObservationAfterGap() error {
	// The next sample lands more than twice the health-check interval after the last counted one.
	s.at = s.at.Add(observationStaleAfter + time.Second - canaryInterval)
	s.observe(true)
	return nil
}

func (s *mobState) startsFromOne() error {
	if st := s.r.streak(s.r.b); st.count != 1 || st.target != s.r.bt.id {
		return fmt.Errorf("the streak is %+v, want 1 for B", st)
	}
	if !s.r.attachment().Live() {
		return fmt.Errorf("S moved on a discarded streak")
	}
	return nil
}

func (s *mobState) thirdFavorsC() error {
	c, _, _ := s.r.liveTower("op-c", mobEndpointC, "")
	waitMeasured(s.t, s.r.b, c.id)
	s.towerC = c
	s.r.closeSession(s.r.bt, s.r.bSess)
	s.observe(true)
	return nil
}

func (s *mobState) oneForC() error {
	if st := s.r.streak(s.r.b); st.count != 1 || st.target != s.towerC.id {
		return fmt.Errorf("the streak is %+v, want 1 for C %s", st, s.towerC.id)
	}
	return nil
}

func (s *mobState) passOnLinkHolder() error {
	if !s.r.b.tower.link.HoldsLocally(s.r.a.id) {
		return fmt.Errorf("fixture: the first instance does not hold A's link")
	}
	return s.twoFavoringB()
}

func (s *mobState) ownerChanges() error {
	r := s.r
	f, _, err := r.b.tower.mobility.Facts(r.station)
	if err != nil {
		return err
	}
	s.factsBefore = f
	// A's link moves to another instance (A opens its session there), and then the first instance
	// restarts as a fresh process over the same stores and A's link comes back to it.
	peer, psrv := r.instance("i-2")
	restarted, rsrv := r.instance("i-1")
	for _, nb := range []struct {
		b   *broker
		srv *httptest.Server
	}{{peer, psrv}, {restarted, rsrv}} {
		r.aSess = r.openSession(r.a, nb.srv, mobEndpointA, "")
		if !nb.b.tower.link.HoldsLocally(r.a.id) {
			return fmt.Errorf("the new owner does not hold A's link")
		}
		nb.b.moveMu.Lock()
		s.emptyBefore = append(s.emptyBefore, len(nb.b.moveStreaks) == 0)
		nb.b.moveMu.Unlock()
		s.at = s.at.Add(canaryInterval)
		nb.b.recordHealthResult(r.a.id, true, s.at)
		nb.b.mobilityPassOnce(s.at)
		nb.b.moveMu.Lock()
		s.newOwners = append(s.newOwners, nb.b.moveStreaks[r.station])
		nb.b.moveMu.Unlock()
	}
	return nil
}

func (s *mobState) streakStartsOver() error {
	for i, st := range s.newOwners {
		if st.count != 1 || st.target != s.r.bt.id {
			return fmt.Errorf("new owner %d holds streak %+v, want 1 for B", i, st)
		}
	}
	if st := s.r.streak(s.r.b); st.count != 2 {
		return fmt.Errorf("fixture: the old owner's streak was %d", st.count)
	}
	return nil
}

func (s *mobState) streakNotShared() error {
	for i, empty := range s.emptyBefore {
		if !empty {
			return fmt.Errorf("new owner %d started with streaks it never counted", i)
		}
	}
	r := s.r
	if _, moving, _ := r.b.tower.mobility.Move(r.station); moving {
		return fmt.Errorf("a move row was written")
	}
	if tw, until, _ := r.b.tower.mobility.Skip(r.station); tw != "" || !until.IsZero() {
		return fmt.Errorf("a skip was written")
	}
	if f, _, _ := r.b.tower.mobility.Facts(r.station); f != s.factsBefore {
		return fmt.Errorf("the Station's facts changed: %+v -> %+v", s.factsBefore, f)
	}
	for _, ev := range towerEventsOf(s.t, r.b, r.a.id) {
		if strings.Contains(ev.Kind, "streak") || strings.HasPrefix(ev.Kind, "station_move") {
			return fmt.Errorf("the record holds %s", ev.Kind)
		}
	}
	return nil
}

// --- the idle gate -------------------------------------------------------------

func (s *mobState) isDue() error {
	s.sample(false)
	s.sample(false)
	sum, err := s.r.b.tower.health.Summary(s.r.a.id, time.Time{})
	if err != nil || sum.FailStreak < 2 {
		return fmt.Errorf("fixture: A's fail streak is %d (%v)", sum.FailStreak, err)
	}
	return nil
}

func (s *mobState) quiescentFleetWide() error {
	s.r.multi()
	s.r.b.mergeSharedInflight()
	ok, why := s.r.b.stationQuiescent(s.r.nodeID)
	if !ok || why != "quiescent" {
		return fmt.Errorf("stationQuiescent = %v %q, want a fleet-wide quiescent answer", ok, why)
	}
	s.pass()
	return nil
}

func (s *mobState) moveStarts() error {
	if s.runPass {
		r := s.r
		// The dormant write is conditional: a different Tower or epoch than the row's writes nothing.
		for _, probe := range []struct {
			tower string
			epoch int64
		}{{r.bt.id, 4}, {r.a.id, 3}} {
			ok, err := r.b.tower.stationStore.DormantForMove(r.station, probe.tower, probe.epoch)
			if err != nil || ok {
				return fmt.Errorf("a dormant write for %s at %d landed (%v, %v)", probe.tower, probe.epoch, ok, err)
			}
		}
		if !r.attachment().Live() {
			return fmt.Errorf("a refused dormant write changed the row")
		}
		s.runPass = false
		s.pass()
	}
	if len(s.startedFor(s.r.a.id, s.r.station)) != 1 || s.r.attachment().State != attach.StateDormant {
		return fmt.Errorf("the move did not start")
	}
	return nil
}

func (s *mobState) moveNotStarted() error {
	if !s.r.attachment().Live() || len(s.started(s.r.a.id)) != 0 {
		return fmt.Errorf("the move started")
	}
	return nil
}

func (s *mobState) staysDue() error {
	s.clear()
	s.pass()
	return s.dueWith(triggerFailing)
}

// closeAndOutlast takes Tower A off the link cleanly and lets its absence outlast the link
// freshness window: one pass sees it missing, and the scenario's clock moves past the window,
// so the scenario's own pass is the one that finds it gone (an orderly close is not "gone"
// until then - station_mobility.feature, "A Tower that drops out briefly is not gone").
func (s *mobState) closeAndOutlast() {
	s.r.closeSession(s.r.a, s.r.aSess)
	s.r.b.mobilityPassOnce(s.at)
	s.at = s.at.Add(s.r.b.tower.link.Freshness() + time.Second)
}

func (s *mobState) goneWithLoad() error {
	s.closeAndOutlast()
	s.r.b.metricsMu.Lock()
	s.r.b.inflight[s.r.nodeID] = 1
	s.r.b.metricsMu.Unlock()
	if ok, _ := s.r.b.stationQuiescent(s.r.nodeID); ok {
		return fmt.Errorf("fixture: S does not show load")
	}
	return nil
}

func (s *mobState) movesAnyway() error { return s.dueWith(triggerGone) }

// --- the move itself -------------------------------------------------------------

func (s *mobState) dueToBIdle() error {
	if err := s.threeBetter(); err != nil {
		return err
	}
	if ok, why := s.r.b.stationQuiescent(s.r.nodeID); !ok {
		return fmt.Errorf("fixture: S is not idle (%s)", why)
	}
	s.runPass = true
	return nil
}

func (s *mobState) dormantOnlyIf(epoch int) error {
	at := s.r.attachment()
	if at.State != attach.StateDormant || at.Origin.TowerID != s.r.a.id || at.Epoch != int64(epoch) {
		return fmt.Errorf("want dormant on A at %d, got %+v", epoch, at)
	}
	return nil
}

func (s *mobState) rowRecordsIntent() error {
	mv, moving, err := s.r.b.tower.mobility.Move(s.r.station)
	if err != nil || !moving {
		return fmt.Errorf("no move row: %v", err)
	}
	if mv.To != s.r.bt.id || mv.Trigger != triggerBetter || mv.From != s.r.a.id || mv.Epoch != 4 || mv.PriorState != attach.StateActive {
		return fmt.Errorf("the move row is %+v", mv)
	}
	return nil
}

func (s *mobState) startedEvent() error {
	evs := s.started(s.r.a.id)
	if len(evs) != 1 {
		return fmt.Errorf("want one station_move_started on A, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Detail["station"] != s.r.station || ev.Detail["to"] != s.r.bt.id || ev.Reason != triggerBetter || ev.Actor != admit.ActorSystem {
		return fmt.Errorf("the event is %+v", ev)
	}
	return nil
}

// poll is one real GET /agent/cancels by S's node.
func (s *mobState) poll() (int, []byte) {
	r := s.r
	r.b.mu.Lock()
	if r.b.tunnels[r.nodeID] == nil {
		r.b.tunnels[r.nodeID] = &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}, token: "tok-" + r.nodeID}
	}
	tok := r.b.tunnels[r.nodeID].token
	r.b.mu.Unlock()
	req, _ := http.NewRequest(http.MethodGet, r.srv.URL+"/agent/cancels?node="+r.nodeID, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	require.NoError(s.t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (s *mobState) nudgeQueued() error {
	if n := s.nudges(); n != 1 {
		return fmt.Errorf("%d nudges queued, want 1", n)
	}
	code, raw := s.poll()
	var out map[string]any
	if code != http.StatusOK || json.Unmarshal(raw, &out) != nil || out["reattach"] != triggerBetter {
		return fmt.Errorf("the node's poll answered %d %s", code, raw)
	}
	return nil
}

func (s *mobState) concurrentChange() error {
	at := s.r.attachment()
	onB, older := at, at
	onB.Origin.TowerID = s.r.bt.id // the pass read S on B; it is on A
	older.Epoch = 3                // the pass read epoch 3; it is at 4
	s.stale = []attach.Attachment{onB, older, at}
	return nil
}

func (s *mobState) dormantWriteRuns() error {
	r := s.r
	for i, read := range s.stale {
		if i == 2 { // the state changes under the third read
			if _, err := r.b.tower.stationStore.SetState(r.station, attach.StateRevoked); err != nil {
				return err
			}
		}
		s.results = append(s.results, r.b.startMove(read, r.bt.id, triggerBetter, time.Now()))
	}
	return nil
}

func (s *mobState) nothingWritten() error {
	for i, ok := range s.results {
		if ok {
			return fmt.Errorf("stale read %d started a move", i)
		}
	}
	at := s.r.attachment()
	if at.State != attach.StateRevoked || at.Origin.TowerID != s.r.a.id || at.Epoch != 4 {
		return fmt.Errorf("the mover wrote to the row: %+v", at)
	}
	if _, moving, _ := s.r.b.tower.mobility.Move(s.r.station); moving {
		return fmt.Errorf("a move row was left behind")
	}
	if n := s.nudges(); n != 0 {
		return fmt.Errorf("%d nudges sent", n)
	}
	return nil
}

func (s *mobState) noMoveEvent() error {
	if n := s.moveEvents(s.r.a.id) + s.moveEvents(s.r.bt.id); n != 0 {
		return fmt.Errorf("%d move events recorded", n)
	}
	return nil
}

func (s *mobState) twoDecide() error {
	s.r.multi()
	at := s.r.attachment()
	s.stale = []attach.Attachment{at, at}
	return nil
}

func (s *mobState) bothWrite() error {
	var wg sync.WaitGroup
	start := make(chan struct{})
	res := make([]bool, 2)
	for i, b := range []*broker{s.r.b, s.r.peer} {
		wg.Add(1)
		go func(i int, b *broker) {
			defer wg.Done()
			<-start
			res[i] = b.startMove(s.stale[i], s.r.bt.id, triggerBetter, time.Now())
		}(i, b)
	}
	close(start)
	wg.Wait()
	s.results = res
	return nil
}

func (s *mobState) exactlyOneLands() error {
	n := 0
	for _, ok := range s.results {
		if ok {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%d dormant writes landed, want 1", n)
	}
	if s.r.attachment().State != attach.StateDormant {
		return fmt.Errorf("S is not dormant")
	}
	return nil
}

func (s *mobState) oneNudgeOneEvent() error {
	if n := len(s.started(s.r.a.id)); n != 1 {
		return fmt.Errorf("%d station_move_started events, want 1", n)
	}
	if n := s.r.sharedNudges(); n != 1 {
		return fmt.Errorf("%d nudges in the shared cancel buffer, want 1", n)
	}
	for _, b := range []*broker{s.r.b, s.r.peer} {
		if n := reattachNudges(b, s.r.nodeID); n != 0 {
			return fmt.Errorf("an instance kept %d nudges of its own", n)
		}
	}
	return nil
}

func (s *mobState) dormantForB() error {
	s.observe(true)
	s.observe(true)
	s.observe(true)
	mv, moving, _ := s.r.b.tower.mobility.Move(s.r.station)
	if !moving || mv.To != s.r.bt.id || s.r.attachment().State != attach.StateDormant {
		return fmt.Errorf("S is not dormant for B: %+v", mv)
	}
	s.prevToken = s.r.hubToken
	return nil
}

func (s *mobState) nodeReattaches() error {
	code, out, raw := s.r.reattach()
	if code != http.StatusOK {
		return fmt.Errorf("the re-attach was refused: %d %s", code, raw)
	}
	s.reply = out
	return nil
}

func (s *mobState) revivedOnB() error {
	at := s.r.attachment()
	if s.reply["tower_id"] != s.r.bt.id || !at.Live() || at.Origin.TowerID != s.r.bt.id || at.StationID != s.r.station {
		return fmt.Errorf("S was not placed on B: reply %v, row %+v", s.reply["tower_id"], at)
	}
	if at.Epoch != 5 { // prior + 1: the revival branch raised it
		return fmt.Errorf("the revival left the epoch at %d", at.Epoch)
	}
	return nil
}

func (s *mobState) epochBecomes(n int) error {
	if e := s.r.attachment().Epoch; e != int64(n) {
		return fmt.Errorf("the epoch is %d, want %d", e, n)
	}
	return nil
}

func (s *mobState) replyCarriesB() error {
	r := s.r
	fpB, fpA := r.b.towerKeyFingerprint(r.bt.id), r.b.towerKeyFingerprint(r.a.id)
	switch {
	case s.reply["endpoint"] != mobEndpointB:
		return fmt.Errorf("endpoint %v, want B's", s.reply["endpoint"])
	case s.reply["endpoint_tls_spki"] != testHubPin():
		return fmt.Errorf("pin %v, want B's", s.reply["endpoint_tls_spki"])
	case s.reply["hub_token"] == "" || s.reply["hub_token"] == s.prevToken:
		return fmt.Errorf("the hub token is not new")
	case fpB == "" || fpB == fpA || s.reply["tower_key_hash"] != fpB:
		return fmt.Errorf("tower_key_hash %v, want B's %s", s.reply["tower_key_hash"], fpB)
	}
	return nil
}

func (s *mobState) movedEvent(epoch int) error {
	evs := towerEventsOf(s.t, s.r.b, s.r.bt.id, evStationMoved)
	if len(evs) != 1 {
		return fmt.Errorf("%d station_moved events on B", len(evs))
	}
	d := evs[0].Detail
	if d["station"] != s.r.station || d["from"] != s.r.a.id || fmt.Sprint(d["epoch"]) != fmt.Sprint(epoch) || evs[0].Reason != triggerBetter {
		return fmt.Errorf("station_moved is %+v", evs[0])
	}
	return nil
}

func (s *mobState) movedAwayEvent() error {
	evs := towerEventsOf(s.t, s.r.b, s.r.a.id, evStationMovedAway)
	if len(evs) != 1 {
		return fmt.Errorf("%d station_moved_away events on A", len(evs))
	}
	d := evs[0].Detail
	if d["station"] != s.r.station || d["to"] != s.r.bt.id || fmt.Sprint(d["epoch"]) != "5" || evs[0].Reason != triggerBetter {
		return fmt.Errorf("station_moved_away is %+v", evs[0])
	}
	return nil
}

func (s *mobState) bIneligible() error {
	// Another eligible Tower is up, so "afresh" has somewhere to go besides A.
	c, _, _ := s.r.liveTower("op-c", mobEndpointD, "")
	s.towerC = c
	s.r.closeSession(s.r.bt, s.r.bSess)
	if s.r.b.tower.link.Live(s.r.bt.id) {
		return fmt.Errorf("B is still live")
	}
	return nil
}

func (s *mobState) rerankedExcludingA() error {
	if !s.r.b.tower.link.Live(s.r.a.id) {
		return fmt.Errorf("fixture: A was not live, so nothing excluded it")
	}
	if s.reply["tower_id"] != s.towerC.id {
		return fmt.Errorf("re-ranked to %v, want C (A excluded, B ineligible)", s.reply["tower_id"])
	}
	return nil
}

func (s *mobState) reasonIntendedUnavailable() error {
	evs := towerEventsOf(s.t, s.r.b, s.towerC.id, evStationMoved)
	if len(evs) != 1 || evs[0].Detail["intended_unavailable"] != true || evs[0].Detail["intended"] != s.r.bt.id {
		return fmt.Errorf("station_moved on C is %+v", evs)
	}
	return nil
}

func (s *mobState) dormantOnlyA() error {
	if err := s.isDue(); err != nil {
		return err
	}
	s.pass()
	if s.r.attachment().State != attach.StateDormant {
		return fmt.Errorf("S is not dormant")
	}
	s.r.closeSession(s.r.bt, s.r.bSess)
	if live := s.r.b.tower.link.LiveTowers(); len(live) != 1 || live[0] != s.r.a.id {
		return fmt.Errorf("live Towers %v, want only A", live)
	}
	return nil
}

func (s *mobState) liveOnAEpoch(n int) error {
	at := s.r.attachment()
	if s.reply["tower_id"] != s.r.a.id || !at.Live() || at.Origin.TowerID != s.r.a.id || at.Epoch != int64(n) {
		return fmt.Errorf("want live on A at %d, got %+v", n, at)
	}
	return nil
}

func (s *mobState) recordReturned() error {
	if n := len(towerEventsOf(s.t, s.r.b, s.r.a.id, evStationMoveReturned)); n != 1 {
		return fmt.Errorf("%d station_move_returned events", n)
	}
	if n := len(towerEventsOf(s.t, s.r.b, s.r.a.id, evStationMoved, evStationMovedAway)); n != 0 {
		return fmt.Errorf("a return was recorded as a move")
	}
	return nil
}

func (s *mobState) coreSendsNudge() error {
	if !s.r.b.startMove(s.r.attachment(), s.r.bt.id, triggerBetter, time.Now()) {
		return fmt.Errorf("the move did not start")
	}
	code, raw := s.poll()
	if code != http.StatusOK {
		return fmt.Errorf("the poll answered %d %s", code, raw)
	}
	s.pollBody = raw
	return nil
}

func (s *mobState) namesNoTower() error {
	var out map[string]any
	if err := json.Unmarshal(s.pollBody, &out); err != nil {
		return err
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"ids", "reattach"}) || out["reattach"] != triggerBetter {
		return fmt.Errorf("the nudge is %s", s.pollBody)
	}
	for _, leak := range []string{s.r.a.id, s.r.bt.id, mobEndpointA, mobEndpointB, testHubPin(), "198.51.100", "192.0.2"} {
		if bytes.Contains(s.pollBody, []byte(leak)) {
			return fmt.Errorf("the nudge names %q: %s", leak, s.pollBody)
		}
	}
	return nil
}

// The node acts by attaching to Core again at the address it already holds. The nudge gave it
// nothing else to act on; Core's own placement decides where it lands, which is where the move chose.
func (s *mobState) onlyReattachToCore() error {
	code, out, raw := s.r.reattach()
	if code != http.StatusOK || out["tower_id"] != s.r.bt.id {
		return fmt.Errorf("the re-attach to Core answered %d %s", code, raw)
	}
	s.reply = out
	return nil
}

func (s *mobState) forgedNudgeHarmless() error {
	r := s.r
	before := r.attachment()
	evBefore := s.moveEvents(r.a.id) + s.moveEvents(r.bt.id)
	r.b.sendReattach(r.nodeID, triggerBetter) // a nudge no move asked for
	code, raw := s.poll()
	if code != http.StatusOK {
		return fmt.Errorf("the forged nudge did not arrive: %d %s", code, raw)
	}
	code, out, raw2 := r.reattach() // the one extra re-attach it can cause
	if code != http.StatusOK || out["tower_id"] != before.Origin.TowerID {
		return fmt.Errorf("the extra re-attach answered %d %s", code, raw2)
	}
	after := r.attachment()
	if after.Epoch != before.Epoch || after.Origin != before.Origin || !after.Live() {
		return fmt.Errorf("the forged nudge changed the attachment: %+v -> %+v", before, after)
	}
	if _, moving, _ := r.b.tower.mobility.Move(r.station); moving {
		return fmt.Errorf("the forged nudge started a move")
	}
	if n := s.moveEvents(r.a.id) + s.moveEvents(r.bt.id); n != evBefore {
		return fmt.Errorf("the forged nudge recorded %d move events", n-evBefore)
	}
	return nil
}

func (s *mobState) oldNode() error {
	s.r.body["nudge"] = false
	code, _, raw := s.r.reattach() // the idempotent branch refreshes the facts on every attach
	if code != http.StatusOK {
		return fmt.Errorf("re-attach: %d %s", code, raw)
	}
	if f, _, _ := s.r.b.tower.mobility.Facts(s.r.station); f.Nudge {
		return fmt.Errorf("the attach handler still records the nudge")
	}
	s.r.placeInB()
	return nil
}

func (s *mobState) dueBetterOrCap() error {
	r := s.r
	for i := 0; i < betterObservations+1; i++ {
		s.observe(true)
	}
	if st := r.streak(r.b); st.count < betterObservations || st.target != r.bt.id {
		return fmt.Errorf("fixture: S was never due for B (streak %+v)", st)
	}
	// stage_cap: S is a house Station on a community Tower now capped at no house Stations.
	makeHouse(s.t, r.b, r.node.login)
	setStage(s.t, r.b, r.a.id, stageNew)
	if over := r.b.overStageCap(mustTower(s.t, r.b, r.a.id), []attach.Attachment{r.attachment()},
		map[string]bool{}); !over[r.station] {
		return fmt.Errorf("fixture: S is not above A's cap")
	}
	s.pass()
	s.before = map[string]int{"nudges": s.nudges(), "started": len(s.started(r.a.id))}
	if !r.attachment().Live() {
		return fmt.Errorf("Core moved an old node for stage_cap")
	}
	// Back to a community Station on an established Tower for the rest of the scenario.
	delete(r.b.stationLimitExempt, ownerPubkeyOf(s.t, r.b, r.node.login))
	setStage(s.t, r.b, r.a.id, stageEstablished)
	return nil
}

func (s *mobState) notMoved() error {
	if !s.r.attachment().Live() || s.before["started"] != 0 || s.before["nudges"] != 0 {
		return fmt.Errorf("Core moved or nudged an old node for better_tower/stage_cap: %v", s.before)
	}
	return nil
}

func (s *mobState) failingAndGoneMoveSilently() error {
	r := s.r
	s.sample(false)
	s.sample(false)
	s.pass()
	if err := s.dueWith(triggerFailing); err != nil {
		return err
	}
	if n := s.nudges(); n != 0 {
		return fmt.Errorf("an old node was nudged %d times", n)
	}
	code, out, raw := r.reattach() // the node's own re-attach on hub failure
	if code != http.StatusOK || out["tower_id"] != r.bt.id {
		return fmt.Errorf("the node's own re-attach answered %d %s", code, raw)
	}
	// tower_gone: S's Tower B goes away, and stays away past the freshness window.
	r.closeSession(r.bt, r.bSess)
	s.pass()
	s.at = s.at.Add(r.b.tower.link.Freshness() + time.Second)
	s.pass()
	evs := s.startedFor(r.bt.id, r.station)
	if len(evs) != 1 || evs[0].Reason != triggerGone || r.attachment().State != attach.StateDormant {
		return fmt.Errorf("the gone move did not start: %+v", evs)
	}
	if n := s.nudges(); n != 0 {
		return fmt.Errorf("an old node was nudged %d times", n)
	}
	code, out, raw = r.reattach()
	if code != http.StatusOK || out["tower_id"] != r.a.id {
		return fmt.Errorf("the node's own re-attach after the gone move answered %d %s", code, raw)
	}
	return nil
}

// --- self-healing ------------------------------------------------------------------

// dormantFor30s: S was moved toward B and nothing re-attached; a pass before 30 seconds resends nothing.
func (s *mobState) dormantFor30s() error {
	if err := s.dormantForB(); err != nil {
		return err
	}
	if n := s.nudges(); n != 1 {
		return fmt.Errorf("fixture: %d nudges at the start", n)
	}
	s.r.b.mobilityPassOnce(s.at.Add(10 * time.Second))
	if n := s.nudges(); n != 1 {
		return fmt.Errorf("a nudge was resent before 30 seconds (%d)", n)
	}
	return nil
}

func (s *mobState) resentUpTo3() error {
	for i, want := range []int{2, 3, 3, 3} {
		s.r.b.mobilityPassOnce(s.at.Add(time.Duration(i+1) * (moveNudgeEvery + time.Second)))
		if n := s.nudges(); n != want {
			return fmt.Errorf("after pass %d, %d nudges; want %d", i+1, n, want)
		}
	}
	return nil
}

func (s *mobState) dormantThrough3() error {
	if err := s.dormantForB(); err != nil {
		return err
	}
	for i := 1; i <= 2; i++ {
		s.r.b.mobilityPassOnce(s.at.Add(time.Duration(i) * (moveNudgeEvery + time.Second)))
	}
	if n := s.nudges(); n != moveNudges {
		return fmt.Errorf("%d nudges, want %d", n, moveNudges)
	}
	s.at = s.at.Add(3 * (moveNudgeEvery + time.Second))
	// The undo is conditional: a different Tower or epoch than the dormant row writes nothing.
	r := s.r
	for _, probe := range []struct {
		tower string
		epoch int64
	}{{r.bt.id, 4}, {r.a.id, 3}} {
		if ok, err := r.b.tower.stationStore.UndoMove(r.station, probe.tower, probe.epoch, attach.StateActive); err != nil || ok {
			return fmt.Errorf("an undo for %s at %d landed (%v %v)", probe.tower, probe.epoch, ok, err)
		}
	}
	if r.attachment().State != attach.StateDormant {
		return fmt.Errorf("a refused undo changed the row")
	}
	return nil
}

func (s *mobState) restoredOnA() error {
	at := s.r.attachment()
	if at.State != attach.StateActive || at.Origin.TowerID != s.r.a.id || at.Epoch != 4 {
		return fmt.Errorf("want live on A at 4, got %+v", at)
	}
	return nil
}

func (s *mobState) abandonedNoReattach() error {
	evs := towerEventsOf(s.t, s.r.b, s.r.a.id, evStationMoveAbandoned)
	if len(evs) != 1 || evs[0].Reason != "no re-attach" || evs[0].Detail["station"] != s.r.station {
		return fmt.Errorf("station_move_abandoned is %+v", evs)
	}
	return nil
}

func (s *mobState) skipBForAnHour() error {
	tw, until, err := s.r.b.tower.mobility.Skip(s.r.station)
	if err != nil || tw != s.r.bt.id {
		return fmt.Errorf("skip names %q (%v), want B", tw, err)
	}
	if d := until.Sub(s.at); d < moveSkipFor-time.Second || d > moveSkipFor+time.Second {
		return fmt.Errorf("B is skipped for %v, want an hour", d)
	}
	if _, _, excl := s.r.b.moveOnReattach(s.r.station, s.at.Add(59*time.Minute)); !excl[s.r.bt.id] {
		return fmt.Errorf("a re-attach inside the hour is not kept off B")
	}
	if _, _, excl := s.r.b.moveOnReattach(s.r.station, s.at.Add(61*time.Minute)); excl[s.r.bt.id] {
		return fmt.Errorf("B is still skipped after the hour")
	}
	return nil
}

func (s *mobState) dormantGoneNoReattach() error {
	r := s.r
	s.closeAndOutlast()
	s.pass()
	if err := s.dueWith(triggerGone); err != nil {
		return err
	}
	for i := 1; i <= moveNudges-1; i++ {
		r.b.mobilityPassOnce(s.at.Add(time.Duration(i) * (moveNudgeEvery + time.Second)))
	}
	if n := s.nudges(); n != moveNudges {
		return fmt.Errorf("%d nudges before the abandon time, want %d", n, moveNudges)
	}
	return nil
}

func (s *mobState) abandonTime() error {
	s.r.b.mobilityPassOnce(s.at.Add(time.Duration(moveNudges) * (moveNudgeEvery + time.Second)))
	return nil
}

func (s *mobState) staysDormant() error {
	r := s.r
	if at := r.attachment(); at.State != attach.StateDormant || at.Origin.TowerID != r.a.id {
		return fmt.Errorf("S was not left dormant: %+v", at)
	}
	if _, moving, _ := r.b.tower.mobility.Move(r.station); moving {
		return fmt.Errorf("the move was not ended")
	}
	evs := towerEventsOf(s.t, r.b, r.a.id, evStationMoveAbandoned)
	if len(evs) != 1 || evs[0].Detail["left"] != "dormant" {
		return fmt.Errorf("station_move_abandoned is %+v", evs)
	}
	return nil
}

func (s *mobState) placedAfresh() error {
	code, out, raw := s.r.reattach()
	if code != http.StatusOK || out["tower_id"] != s.r.bt.id {
		return fmt.Errorf("the next attach answered %d %s", code, raw)
	}
	if n := s.moveEvents(s.r.bt.id); n != 0 {
		return fmt.Errorf("a fresh placement was recorded as a move (%d events)", n)
	}
	if n := len(towerEventsOf(s.t, s.r.b, s.r.bt.id, evStationPlaced)); n != 1 {
		return fmt.Errorf("%d station_placed events on B, want 1", n)
	}
	return nil
}

// --- the fence (line 242) ------------------------

func (s *mobState) attemptAtEpoch(epoch int) error {
	s.attemptID = "att-mob-" + randHex(4)
	issuedAttemptAtEpoch(s.t, s.r.b, s.attemptID, s.r.a.id, s.r.station, int64(epoch))
	return nil
}

func (s *mobState) movedBeforeSettle(epoch int) error {
	if err := s.dormantForB(); err != nil {
		return err
	}
	if err := s.nodeReattaches(); err != nil {
		return err
	}
	return s.epochBecomes(epoch)
}

func (s *mobState) courierPresents() error {
	s.logs.Reset()
	code, raw := s.r.a.call(s.t, s.r.srv, "/tower/edge/settle",
		settleBody(s.t, s.r.a.id, s.r.station, s.attemptID, s.r.assertPriv, 4), nil)
	s.settleCode = code
	s.t.Logf("settle by Tower A answered %d %s", code, raw)
	return nil
}

func (s *mobState) answers410() error {
	if s.settleCode != http.StatusGone {
		return fmt.Errorf("settlement answered %d, want 410", s.settleCode)
	}
	return nil
}

func (s *mobState) nobodyPaid() error {
	if st := attemptState(s.t, s.r.b, s.attemptID); st != dispatch.StateIssued {
		return fmt.Errorf("the attempt is %s, want issued (the orphan sweep releases the hold on age)", st)
	}
	owed, err := s.r.b.tower.earnings.OwedTo(ownerPubkeyOf(s.t, s.r.b, s.r.node.login), time.Now().Add(-time.Hour))
	if err != nil || owed.Attempts != 0 || owed.Accrued != 0 {
		return fmt.Errorf("someone is owed: %+v %v", owed, err)
	}
	tally, err := s.r.b.tower.outcomes.Tally(s.r.a.id, time.Time{})
	if err != nil || tally.Total != 0 {
		return fmt.Errorf("reputation was written: %+v %v", tally, err)
	}
	return nil
}

func (s *mobState) fenceLogLine() error {
	line := s.logs.String()
	for _, f := range []string{"edge fence MOVED ", "tower=" + s.r.a.id, "grant_epoch=4", "attach_epoch=5", "deadline_open="} {
		if !strings.Contains(line, f) {
			return fmt.Errorf("the fence line lacks %q: %s", f, line)
		}
	}
	return nil
}

// --- records ---------------------------------------------------------------------

func (s *mobState) moveWithLoad() error {
	r := s.r
	r.b.metricsMu.Lock()
	r.b.inflight[r.nodeID] = 2
	r.b.peerInflight = map[string]int{r.nodeID: 4}
	r.b.peerEdgeLoad = map[string]int{r.nodeID: 5}
	r.b.metricsMu.Unlock()
	for i := 0; i < 3; i++ {
		r.b.edgeEnterInflight(fmt.Sprintf("att-mob-load-%d", i), r.nodeID, "acct-mob", time.Now().Add(time.Hour))
	}
	// A loaded Station is moved only off a Tower that is gone.
	s.closeAndOutlast()
	s.pass()
	return s.dueWith(triggerGone)
}

func (s *mobState) detailCarriesLoad() error {
	d := s.started(s.r.a.id)[0].Detail
	for k, v := range map[string]int{"inflight": 2, "edge_load": 3, "peer_inflight": 4, "peer_edge_load": 5} {
		if fmt.Sprint(d[k]) != fmt.Sprint(v) {
			return fmt.Errorf("detail %s = %v, want %d (%+v)", k, d[k], v, d)
		}
	}
	return nil
}

func (s *mobState) nOnADue(n int) error {
	r := s.r
	r.closeSession(r.bt, r.bSess) // newcomers land on A
	for i := 1; i < n; i++ {
		op := signedInOperator(s.t, r.b, fmt.Sprintf("pace-%d-%s", i, randHex(3)))
		body, _ := selfAttachBodyFor(s.t, r.b, op)
		body["nudge"] = true
		code, out, raw := r.attach(op, body)
		if code != http.StatusOK || out["tower_id"] != r.a.id {
			return fmt.Errorf("Station %d did not land on A: %d %s", i, code, raw)
		}
		id := out["station_id"].(string)
		r.b.noteCancelCapable(body["node_id"].(string))
		require.NoError(s.t, r.b.tower.mobility.PutFacts(id, mobility.Facts{
			Locality: placement.Locality{Bucket: mobBucketB, Continent: "EU"}, Nudge: true}))
		s.others = append(s.others, id)
	}
	r.bSess = r.openSession(r.bt, r.srv, mobEndpointB, testHubPin())
	waitMeasured(s.t, r.b, r.bt.id)
	if atts, _ := r.b.tower.stations.ByTower(r.a.id); len(atts) != n {
		return fmt.Errorf("%d Stations on A, want %d", len(atts), n)
	}
	s.observe(true)
	s.observe(true)
	s.sample(true) // all of them are due for B on this sample
	s.startedPass = s.countStarted()
	return nil
}

// countStarted counts station_move_started events per source and target ("" = none).
func (s *mobState) countStarted() map[string]int {
	out := map[string]int{}
	for _, tw := range s.r.b.tower.registry.All() {
		for _, ev := range s.started(tw.ID) {
			to, _ := ev.Detail["to"].(string)
			out[tw.ID+"|"+to]++
		}
	}
	return out
}

func (s *mobState) passDelta() map[string]int {
	out := map[string]int{}
	for k, v := range s.countStarted() {
		if d := v - s.startedPass[k]; d > 0 {
			out[k] = d
		}
	}
	return out
}

func (s *mobState) atMostFromA(n int) error {
	from := 0
	for k, v := range s.passDelta() {
		if strings.HasPrefix(k, s.r.a.id+"|") {
			from += v
		}
	}
	if from > n || from == 0 {
		return fmt.Errorf("%d moves started from A in the pass, want at most %d (and some)", from, n)
	}
	return nil
}

func (s *mobState) atMostIntoOne(n int) error {
	into := map[string]int{}
	for k, v := range s.passDelta() {
		if to := k[strings.Index(k, "|")+1:]; to != "" {
			into[to] += v
		}
	}
	if len(into) == 0 {
		return fmt.Errorf("no targeted move started in the pass")
	}
	for to, v := range into {
		if v > n {
			return fmt.Errorf("%d moves target %s, want at most %d", v, to, n)
		}
	}
	return nil
}

func (s *mobState) atMostFleet(n int) error {
	total := 0
	for _, v := range s.passDelta() {
		total += v
	}
	if total > n {
		return fmt.Errorf("%d moves started fleet-wide, want at most %d", total, n)
	}
	return nil
}

func (s *mobState) gonePacedSame() error {
	s.startedPass = s.countStarted()
	goneBefore := 0
	for _, ev := range s.started(s.r.a.id) {
		if ev.Reason == triggerGone {
			goneBefore++
		}
	}
	s.closeAndOutlast()
	s.pass()
	gone := -goneBefore
	for _, ev := range s.started(s.r.a.id) {
		if ev.Reason == triggerGone {
			gone++
		}
	}
	if gone != movesPerTowerPerPass {
		return fmt.Errorf("%d tower_gone moves started from A in one pass, want %d", gone, movesPerTowerPerPass)
	}
	if err := s.atMostFromA(movesPerTowerPerPass); err != nil {
		return err
	}
	return s.atMostFleet(movesPerPass)
}

func (s *mobState) movesRecorded() error {
	r := s.r
	if err := s.isDue(); err != nil {
		return err
	}
	s.pass()
	if err := s.dueWith(triggerFailing); err != nil {
		return err
	}
	// More move events on A within the hour than the budget allows, through the same recorder.
	for i := 0; i < towerEventBudget+5; i++ {
		r.b.towerEvent(admit.Event{TowerID: r.a.id, Kind: evStationMoveStarted, Actor: admit.ActorSystem,
			Reason: triggerFailing, Detail: map[string]any{"station": fmt.Sprintf("st-%d", i)}})
	}
	return nil
}

func (s *mobState) pacedSameBudget() error {
	r := s.r
	if n := len(s.started(r.a.id)); n != towerEventBudget {
		return fmt.Errorf("%d station_move_started recorded for A in the hour, want the budget %d", n, towerEventBudget)
	}
	// Per kind: another move kind on A still records. Per Tower: the same kind on B still records.
	r.b.towerEvent(admit.Event{TowerID: r.a.id, Kind: evStationMovedAway, Actor: admit.ActorSystem, Detail: map[string]any{"station": "x"}})
	r.b.towerEvent(admit.Event{TowerID: r.bt.id, Kind: evStationMoveStarted, Actor: admit.ActorSystem, Detail: map[string]any{"station": "x"}})
	if len(towerEventsOf(s.t, r.b, r.a.id, evStationMovedAway)) != 1 || len(s.started(r.bt.id)) != 1 {
		return fmt.Errorf("the budget is not per kind and per Tower")
	}
	// The same budget the record already applies: a Tower-driven kind stops at the same count.
	c, _, _ := r.liveTower("op-c", mobEndpointD, "")
	for i := 0; i < towerEventBudget+5; i++ {
		r.b.towerEvent(admit.Event{TowerID: c.id, Kind: evStationAttached, Actor: admit.ActorTower})
	}
	if n := len(towerEventsOf(s.t, r.b, c.id, evStationAttached)); n != towerEventBudget {
		return fmt.Errorf("the record's existing budget let %d through", n)
	}
	return nil
}

func (s *mobState) actorSystem() error {
	for _, tw := range []string{s.r.a.id, s.r.bt.id} {
		for _, ev := range towerEventsOf(s.t, s.r.b, tw, evStationMoveStarted, evStationMoved, evStationMovedAway,
			evStationMoveReturned, evStationMoveAbandoned) {
			if ev.Actor != "system" {
				return fmt.Errorf("a move event is by %q", ev.Actor)
			}
		}
	}
	return nil
}

// --- who sees what ------------------------------------------------------------------

// hubNodes is a Tower's own view of the Stations it carries (POST /tower/hub/nodes, Tower-signed).
func (s *mobState) hubNodes(lt linkTower) (int, string) {
	var out struct {
		Nodes []map[string]any `json:"nodes"`
	}
	code, raw := lt.call(s.t, s.r.srv, "/tower/hub/nodes", jsonOf(s.t, map[string]any{"tower_id": lt.id}), &out)
	require.Equal(s.t, http.StatusOK, code, raw)
	return len(out.Nodes), raw
}

func (s *mobState) movesAToB() error {
	nA, _ := s.hubNodes(s.r.a)
	nB, _ := s.hubNodes(s.r.bt)
	s.before = map[string]int{"A": nA, "B": nB}
	if err := s.dormantForB(); err != nil {
		return err
	}
	return s.nodeReattaches()
}

func (s *mobState) countsChange() error {
	nA, _ := s.hubNodes(s.r.a)
	nB, _ := s.hubNodes(s.r.bt)
	if nA != s.before["A"]-1 || nB != s.before["B"]+1 {
		return fmt.Errorf("A %d->%d, B %d->%d", s.before["A"], nA, s.before["B"], nB)
	}
	return nil
}

func (s *mobState) operatorsSeeNothingElse() error {
	r := s.r
	owner := ownerPubkeyOf(s.t, r.b, r.node.login)
	_, hubA := s.hubNodes(r.a)
	_, hubB := s.hubNodes(r.bt)
	_, statA := r.opA.call(s.t, r.srv, http.MethodGet, "/tower/status", nil, nil)
	_, statB := r.opB.call(s.t, r.srv, http.MethodGet, "/tower/status", nil, nil)
	for _, v := range []struct {
		name, other string
		raws        []string
	}{{"A", r.bt.id, []string{hubA, statA}}, {"B", r.a.id, []string{hubB, statB}}} {
		for _, raw := range v.raws {
			for _, leak := range []string{v.other, owner, r.node.login} {
				if strings.Contains(raw, leak) {
					return fmt.Errorf("Tower %s's operator view shows %q: %s", v.name, leak, raw)
				}
			}
		}
	}
	return nil
}

func (s *mobState) adminOpensDetail() error {
	if err := s.dormantForB(); err != nil {
		return err
	}
	return s.nodeReattaches()
}

func (s *mobState) adminSeesMoves() error {
	r := s.r
	for _, c := range []struct {
		tower, kind, other, key string
		epoch                   int
	}{
		{r.a.id, evStationMoveStarted, r.bt.id, "to", 4},
		{r.a.id, evStationMovedAway, r.bt.id, "to", 5},
		{r.bt.id, evStationMoved, r.a.id, "from", 5},
	} {
		var out map[string]any
		adminGetJSON(s.t, r.srv.URL, "/admin/tower?id="+c.tower, &out)
		evs, _ := out["events"].([]any)
		var row map[string]any
		for _, e := range evs {
			if m := e.(map[string]any); m["kind"] == c.kind {
				row = m
			}
		}
		if row == nil {
			return fmt.Errorf("Tower %s's detail lists no %s", c.tower, c.kind)
		}
		d, _ := row["detail"].(map[string]any)
		if row["reason"] != triggerBetter || d["station"] != r.station || d[c.key] != c.other || fmt.Sprint(d["epoch"]) != fmt.Sprint(c.epoch) {
			return fmt.Errorf("the %s row is %v", c.kind, row)
		}
	}
	return nil
}

// --- what does not change -----------------------------------------------------------

func (s *mobState) coreConsidersMoves() error {
	r := s.r
	// A direct-origin Station beside S: it names no Tower.
	s.directID = "st-" + randHex(8)
	op := signedInOperator(s.t, r.b, "direct-op")
	owner := ownerPubkeyOf(s.t, r.b, op.login)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sess := make([]byte, 32)
	copy(sess, s.directID)
	auth, secret, err := attach.NewInvite(attach.Authorization{ID: "auth-" + s.directID, Network: link.PublicNetwork,
		StationID: s.directID, Owner: owner, Origin: attach.Origin{Kind: attach.OriginDirect},
		AssertionKey: hexOf(pub), SessionKey: hex.EncodeToString(sess)}, time.Hour, time.Now())
	require.NoError(s.t, err)
	require.NoError(s.t, r.b.tower.stationStore.PutAuthorization(auth))
	_, err = r.b.tower.stations.Admit(attach.Proof{AuthID: auth.ID, Secret: secret, Network: link.PublicNetwork,
		StationID: s.directID, Owner: owner, Origin: attach.Origin{Kind: attach.OriginDirect},
		AssertionKey: hexOf(pub), SessionKey: hex.EncodeToString(sess)})
	require.NoError(s.t, err)
	_, err = r.b.tower.stations.Promote(s.directID)
	require.NoError(s.t, err)

	if err := s.isDue(); err != nil {
		return err
	}
	s.pass()
	if err := s.dueWith(triggerFailing); err != nil {
		return err
	}
	// While S is dormant mid-move, an attempt to bring it back as a direct Station.
	at := r.attachment()
	auth2, secret2, err := attach.NewInvite(attach.Authorization{ID: "auth-mob-kind-" + randHex(4), Network: link.PublicNetwork,
		StationID: r.station, Owner: at.Owner, Origin: attach.Origin{Kind: attach.OriginDirect},
		AssertionKey: at.AssertionKey, SessionKey: at.SessionKey}, time.Hour, time.Now())
	require.NoError(s.t, err)
	require.NoError(s.t, r.b.tower.stationStore.PutAuthorization(auth2))
	_, s.kindErr = r.b.tower.stations.Admit(attach.Proof{AuthID: auth2.ID, Secret: secret2, Network: link.PublicNetwork,
		StationID: r.station, Owner: at.Owner, Origin: attach.Origin{Kind: attach.OriginDirect},
		AssertionKey: at.AssertionKey, SessionKey: at.SessionKey})
	return s.nodeReattaches()
}

func (s *mobState) onlyJoinedTowerToTower() error {
	r := s.r
	for _, tw := range r.b.tower.registry.All() {
		for _, ev := range s.started(tw.ID) {
			if ev.Detail["station"] != r.station {
				return fmt.Errorf("Core moved %v", ev.Detail["station"])
			}
		}
	}
	d, ok, _ := r.b.tower.stations.Station(s.directID)
	if !ok || !d.Live() || d.Origin.Kind != attach.OriginDirect || d.Origin.TowerID != "" {
		return fmt.Errorf("the direct Station changed: %+v", d)
	}
	at := r.attachment()
	if at.Origin.Kind != attach.OriginJoined || at.Origin.TowerID != r.bt.id {
		return fmt.Errorf("S was not moved joined Tower to joined Tower: %+v", at.Origin)
	}
	return nil
}

func (s *mobState) kindImmutable() error {
	if s.kindErr == nil || !strings.Contains(s.kindErr.Error(), "origin kind cannot change") {
		return fmt.Errorf("reviving S as direct was not refused for its kind: %v", s.kindErr)
	}
	if k := s.r.attachment().Origin.Kind; k != attach.OriginJoined {
		return fmt.Errorf("S's origin kind is %s", k)
	}
	return nil
}

func (s *mobState) anyMoveCompletes() error {
	r := s.r
	s.epochs = append(s.epochs, r.attachment().Epoch)
	s.observe(true)
	s.observe(true)
	s.observe(true) // the dormant write
	s.epochs = append(s.epochs, r.attachment().Epoch)
	r.b.mobilityPassOnce(s.at.Add(moveNudgeEvery + time.Second)) // a resent nudge
	s.epochs = append(s.epochs, r.attachment().Epoch)
	if err := s.nodeReattaches(); err != nil {
		return err
	}
	s.epochs = append(s.epochs, r.attachment().Epoch)
	// A second move, off B, that its node never completes.
	start := time.Now()
	if !r.b.startMove(r.attachment(), r.a.id, triggerBetter, start) {
		return fmt.Errorf("the second move did not start")
	}
	for i := 1; i <= moveNudges; i++ {
		r.b.mobilityPending(start.Add(time.Duration(i) * (moveNudgeEvery + time.Second)))
	}
	return nil
}

func (s *mobState) raisedOnlyByRevival() error {
	if !reflect.DeepEqual(s.epochs, []int64{4, 4, 4, 5}) {
		return fmt.Errorf("epochs through the move: %v, want 4 4 4 then 5 from the revival", s.epochs)
	}
	if n := len(towerEventsOf(s.t, s.r.b, s.r.bt.id, evStationMoved)); n != 1 {
		return fmt.Errorf("the move did not complete through the attach handler's revival")
	}
	return nil
}

func (s *mobState) abandonedRestoresEpoch() error {
	at := s.r.attachment()
	if !at.Live() || at.Origin.TowerID != s.r.bt.id || at.Epoch != 5 {
		return fmt.Errorf("after the undo S is %+v, want live on B at 5", at)
	}
	if n := len(towerEventsOf(s.t, s.r.b, s.r.bt.id, evStationMoveAbandoned)); n != 1 {
		return fmt.Errorf("the second move was not abandoned")
	}
	return nil
}

// alsoClassic stands S's node up on the classic direct path: a registered priced offer of the same
// model, a tunnel, and a worker returning results over the real POST /agent/result.
func (s *mobState) alsoClassic() error {
	r := s.r
	pub := hex.EncodeToString(r.node.priv.Public().(ed25519.PublicKey))
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 8), waiters: map[string]chan protocol.JobResult{}, token: "tok-" + r.nodeID}
	r.b.mu.Lock()
	r.b.nodes[r.nodeID] = protocol.NodeRegistration{NodeID: r.nodeID, PubKey: pub,
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 1, PriceOut: 1}}}
	r.b.lastSeen[r.nodeID] = time.Now()
	r.b.tunnels[r.nodeID] = tun
	r.b.mu.Unlock()
	require.NoError(s.t, r.db.BindNode(r.nodeID, pub))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"served direct"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	s.t.Cleanup(upstream.Close)
	s.worker = &binNodeWorker{brokerURL: r.srv.URL, nodeID: r.nodeID, token: tun.token, nodePriv: r.node.priv,
		model: "m", upstream: upstream.URL + "/v1/chat/completions", client: &http.Client{Timeout: 10 * time.Second},
		jobs: tun.jobs, stop: make(chan struct{}), lastReqID: make(chan string, 8)}
	s.worker.run()
	_, cpriv, _ := ed25519.GenerateKey(rand.Reader)
	s.consumer = cpriv
	require.NoError(s.t, r.db.BindOwner(store.Owner{GitHubID: 4343, Login: "mob-buyer",
		Pubkey: hex.EncodeToString(cpriv.Public().(ed25519.PublicKey))}))
	_, err := r.db.AddCredits("u_gh_4343", 100)
	require.NoError(s.t, err)
	// Served before any move. Pinned to the node, because while S is live an unpinned request may
	// equally ride S's Tower, which is not the path this scenario is about.
	return s.directRequest(true)
}

func (s *mobState) directRequest(pinned bool) error {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest(http.MethodPost, s.r.srv.URL+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if pinned {
		req.Header.Set("X-Roger-Node", s.r.nodeID)
	}
	signReq(req, s.consumer, body)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Contains(raw, []byte("served direct")) {
		return fmt.Errorf("the direct request answered %d %s", resp.StatusCode, raw)
	}
	select {
	case id := <-s.worker.lastReqID:
		s.served = append(s.served, id)
	case <-time.After(2 * time.Second):
		return fmt.Errorf("the node's worker never served the request")
	}
	return nil
}

func (s *mobState) dormantMidMove() error {
	if err := s.isDue(); err != nil {
		return err
	}
	s.pass()
	return s.dueWith(triggerFailing)
}

func (s *mobState) directKeepsServing() error {
	if s.r.attachment().State != attach.StateDormant {
		return fmt.Errorf("fixture: S is not dormant")
	}
	// Pinned to the node, and unpinned: with S dormant there is no Tower path for "m" at all.
	for _, pinned := range []bool{true, false} {
		if err := s.directRequest(pinned); err != nil {
			return err
		}
	}
	if len(s.served) != 3 {
		return fmt.Errorf("the node served %d direct requests, want 3", len(s.served))
	}
	return nil
}

// mobBrokerLines are the scenarios this runner executes: every scenario in the file except the
// three about the node's own code (internal/agent runner).
var mobBrokerLines = []int{47, 53, 58, 63, 68, 73, 90, 98, 104, 109, 114, 119, 127, 132, 147, 155, 163, 169,
	175, 184, 191, 197, 203, 222, 227, 234, 242, 250, 257, 265, 277, 282, 288, 293, 298}

func mobSteps(sc *godog.ScenarioContext, s *mobState) {
	sc.Step(`^a Station "S" live on Tower "A" at epoch (\d+)$`, s.background)
	sc.Step(`^S's node declared at attach that it understands the re-attach nudge$`, s.declaredNudge)
	sc.Step(`^the mobility pass runs$`, s.passRuns)
	sc.Step(`^S is due to move with trigger "([^"]*)"$`, s.dueWith)
	sc.Step(`^S is due to move with trigger "([^"]*)" and target B$`, s.dueWithTarget)
	sc.Step(`^the move does not wait for any observation count$`, s.noObservationWait)
	sc.Step(`^S is not due to move$`, s.notDue)
	sc.Step(`^Tower A has been absent from the fleet-wide live set for longer than the link freshness window$`, s.towerAbsent)
	sc.Step(`^Tower A's link closed and a new session opened within the freshness window$`, s.briefDropout)
	sc.Step(`^two consecutive fresh health checks of Tower A \(tower_trust_ramp\.feature\) both failed$`, s.twoFailed)
	sc.Step(`^the latest health check of Tower A failed and the one before it passed$`, s.failThenPass)
	sc.Step(`^on three consecutive fresh observations some eligible Tower B was in a strictly better class for S than Tower A$`, s.threeBetter)
	sc.Step(`^S on Tower A and a candidate Tower B$`, s.onAWithCandidate)
	sc.Step(`^"([^"]*)"$`, s.quoted)
	sc.Step(`^B "(is|is not)" strictly better for S$`, s.verdict)
	sc.Step(`^Tower A was demoted and S is above A's new cap for its group$`, s.stageDemoted)
	sc.Step(`^the newest Stations above the cap are due to move with trigger "([^"]*)", oldest kept$`, s.newestOverCap)
	sc.Step(`^the mobility pass runs more often than the health checks$`, s.passFasterThanChecks)
	sc.Step(`^three passes see the same health-check sample$`, s.threePassesOneSample)
	sc.Step(`^the streak has advanced once, not three times$`, s.advancedOnce)
	sc.Step(`^it advances only when the sample's own timestamp is newer than the last one counted$`, s.advancesOnlyOnNewer)
	sc.Step(`^S has two consecutive observations favoring Tower B$`, s.twoFavoringB)
	sc.Step(`^the next fresh observation does not favor B$`, s.nextNotFavoringB)
	sc.Step(`^the streak for S is zero$`, s.streakZero)
	sc.Step(`^S's last counted observation is older than twice the health-check interval$`, s.twoFavoringB)
	sc.Step(`^a new observation arrives$`, s.newObservationAfterGap)
	sc.Step(`^the streak starts again from one$`, s.startsFromOne)
	sc.Step(`^S has two observations favoring Tower B$`, s.twoFavoringB)
	sc.Step(`^the third favors Tower C instead$`, s.thirdFavorsC)
	sc.Step(`^the streak is one, for C$`, s.oneForC)
	sc.Step(`^the mobility pass for Tower A runs on the instance holding A's link$`, s.passOnLinkHolder)
	sc.Step(`^that instance restarts or A's link moves to another instance$`, s.ownerChanges)
	sc.Step(`^S's streak starts over on the new owner$`, s.streakStartsOver)
	sc.Step(`^no streak is shared between instances or written to the store$`, s.streakNotShared)
	sc.Step(`^S is due to move$`, s.isDue)
	sc.Step(`^stationQuiescent reports no work on either fabric, fleet-wide, from a fresh peer view$`, s.quiescentFleetWide)
	sc.Step(`^the move starts$`, s.moveStarts)
	sc.Step(`^the move does not start$`, s.moveNotStarted)
	sc.Step(`^S stays due and is checked again next pass$`, s.staysDue)
	sc.Step(`^Tower A is gone and S shows load$`, s.goneWithLoad)
	sc.Step(`^S's move starts anyway$`, s.movesAnyway)
	sc.Step(`^S is due to move to Tower B and is idle$`, s.dueToBIdle)
	sc.Step(`^S's attachment is set dormant only if it is still live, still on Tower A and still at epoch (\d+)$`, s.dormantOnlyIf)
	sc.Step(`^the dormant row records the intended Tower B and the trigger$`, s.rowRecordsIntent)
	sc.Step(`^a "station_move_started" event is recorded for Tower A naming S, B and the trigger$`, s.startedEvent)
	sc.Step(`^a re-attach nudge is queued on S's node's broker poll$`, s.nudgeQueued)
	sc.Step(`^S's attachment changed state, Tower or epoch after the pass read it$`, s.concurrentChange)
	sc.Step(`^the dormant write runs$`, s.dormantWriteRuns)
	sc.Step(`^nothing is written and no nudge is sent$`, s.nothingWritten)
	sc.Step(`^no move event is recorded$`, s.noMoveEvent)
	sc.Step(`^two instances decide to move S at the same moment$`, s.twoDecide)
	sc.Step(`^both attempt the dormant write$`, s.bothWrite)
	sc.Step(`^exactly one write lands$`, s.exactlyOneLands)
	sc.Step(`^exactly one nudge and one "station_move_started" event exist$`, s.oneNudgeOneEvent)
	sc.Step(`^S is dormant with intended Tower B$`, s.dormantForB)
	sc.Step(`^S's node re-attaches with its own station id and both keys$`, s.nodeReattaches)
	sc.Step(`^Admit's revival branch places S on Tower B, if B is still eligible$`, s.revivedOnB)
	sc.Step(`^S's epoch becomes (\d+)$`, s.epochBecomes)
	sc.Step(`^the reply carries Tower B's endpoint, pin, a new hub token and B's key fingerprint$`, s.replyCarriesB)
	sc.Step(`^a "station_moved" event is recorded for Tower B naming S, Tower A, epoch (\d+) and the trigger$`, s.movedEvent)
	sc.Step(`^a matching "station_moved_away" event is recorded for Tower A$`, s.movedAwayEvent)
	sc.Step(`^B became ineligible before the node re-attached$`, s.bIneligible)
	sc.Step(`^the node re-attaches$`, s.nodeReattaches)
	sc.Step(`^placement ranks the eligible Towers afresh, excluding A unless A is the only one$`, s.rerankedExcludingA)
	sc.Step(`^the recorded reason says the intended Tower was unavailable$`, s.reasonIntendedUnavailable)
	sc.Step(`^S is dormant and the only eligible Tower is A$`, s.dormantOnlyA)
	sc.Step(`^S is live on A with epoch (\d+)$`, s.liveOnAEpoch)
	sc.Step(`^the record says "station_move_returned" rather than "station_moved"$`, s.recordReturned)
	sc.Step(`^Core sends a re-attach nudge$`, s.coreSendsNudge)
	sc.Step(`^the message names no Tower, endpoint or pin$`, s.namesNoTower)
	sc.Step(`^the node's only action is to re-attach to Core over its already-pinned Core address$`, s.onlyReattachToCore)
	sc.Step(`^a forged nudge can at worst cause one extra re-attach$`, s.forgedNudgeHarmless)
	sc.Step(`^a node did not declare at attach that it understands the nudge$`, s.oldNode)
	sc.Step(`^its Station is due to move with trigger "better_tower" or "stage_cap"$`, s.dueBetterOrCap)
	sc.Step(`^Core does not move it$`, s.notMoved)
	sc.Step(`^when the trigger is "tower_gone" or "tower_failing" Core moves it with no nudge, relying on the node's own re-attach on hub failure$`, s.failingAndGoneMoveSilently)
	sc.Step(`^S has been dormant for a move for 30 seconds without a re-attach$`, s.dormantFor30s)
	sc.Step(`^the nudge is queued again, up to 3 times in all$`, s.resentUpTo3)
	sc.Step(`^S stayed dormant through 3 nudges$`, s.dormantThrough3)
	sc.Step(`^S's attachment is restored to live on Tower A at epoch 4, only if it is still that dormant row$`, s.restoredOnA)
	sc.Step(`^a "station_move_abandoned" event is recorded naming the reason "no re-attach"$`, s.abandonedNoReattach)
	sc.Step(`^Tower B is skipped for S for one hour$`, s.skipBForAnHour)
	sc.Step(`^S is dormant because Tower A is gone and its node never re-attached$`, s.dormantGoneNoReattach)
	sc.Step(`^the abandon time comes$`, s.abandonTime)
	sc.Step(`^S stays dormant, as the idle sweep would leave it$`, s.staysDormant)
	sc.Step(`^its node is placed afresh whenever it next attaches$`, s.placedAfresh)
	sc.Step(`^an edge attempt was granted to S at epoch (\d+) through Tower A$`, s.attemptAtEpoch)
	sc.Step(`^S moved to Tower B at epoch (\d+) before the attempt settled$`, s.movedBeforeSettle)
	sc.Step(`^Tower A's courier presents the receipt$`, s.courierPresents)
	sc.Step(`^settlement answers 410, exactly as the built fence does$`, s.answers410)
	sc.Step(`^nobody is paid, no reputation is written, and the consumer's hold is released on age$`, s.nobodyPaid)
	sc.Step(`^the "edge fence MOVED" log line names Tower A, both epochs and whether the deadline was still open$`, s.fenceLogLine)
	sc.Step(`^a move starts$`, s.moveWithLoad)
	sc.Step(`^the "station_move_started" event's detail carries the four load terms stationQuiescent read$`, s.detailCarriesLoad)
	sc.Step(`^(\d+) Stations on Tower A are due to move$`, s.nOnADue)
	sc.Step(`^at most (\d+) moves start from Tower A in that pass$`, s.atMostFromA)
	sc.Step(`^at most (\d+) moves in that pass target any one Tower$`, s.atMostIntoOne)
	sc.Step(`^at most (\d+) moves start fleet-wide in that pass$`, s.atMostFleet)
	sc.Step(`^"tower_gone" moves are paced the same way$`, s.gonePacedSame)
	sc.Step(`^moves are recorded$`, s.movesRecorded)
	sc.Step(`^they are paced by the same per actor, per kind, per Tower budget the record already applies$`, s.pacedSameBudget)
	sc.Step(`^the actor is "system"$`, s.actorSystem)
	sc.Step(`^S moves from Tower A to Tower B$`, s.movesAToB)
	sc.Step(`^Tower A's operator view counts one Station fewer and Tower B's one more$`, s.countsChange)
	sc.Step(`^neither operator sees the other Tower's id or S's owner$`, s.operatorsSeeNothingElse)
	sc.Step(`^an admin opens Tower A's or Tower B's detail$`, s.adminOpensDetail)
	sc.Step(`^the move events appear in its events list with trigger, Station, other Tower and epoch$`, s.adminSeesMoves)
	sc.Step(`^Core considers moves$`, s.coreConsidersMoves)
	sc.Step(`^it only ever moves a joined Station from one Tower to another$`, s.onlyJoinedTowerToTower)
	sc.Step(`^origin kind stays immutable, as the approved station_attachment\.feature requires$`, s.kindImmutable)
	sc.Step(`^any move completes$`, s.anyMoveCompletes)
	sc.Step(`^the epoch was raised only by Admit's revival branch$`, s.raisedOnlyByRevival)
	sc.Step(`^an abandoned move restores the old epoch rather than writing a new one$`, s.abandonedRestoresEpoch)
	sc.Step(`^S's node also serves on the classic direct path$`, s.alsoClassic)
	sc.Step(`^S is dormant mid-move$`, s.dormantMidMove)
	sc.Step(`^direct requests to the node keep being served$`, s.directKeepsServing)
}

func mobRun(t *testing.T, lines []int) int {
	feature := mobFeature(t)
	var paths []string
	for _, l := range lines {
		paths = append(paths, fmt.Sprintf("%s:%d", feature, l))
	}
	s := &mobState{logs: &bytes.Buffer{}}
	prev := log.Writer()
	log.SetOutput(s.logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, scn *godog.Scenario) (context.Context, error) {
				s.reset(t)
				s.r = newMobRig(t, scn.Name == "Trigger 1, the Tower is gone")
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if s.worker != nil {
					s.worker.close()
					s.worker = nil
				}
				return ctx, nil
			})
			mobSteps(sc, s)
		},
		Options: &godog.Options{Format: "pretty", Paths: paths, TestingT: t, Strict: true},
	}
	return bddrun.Run(suite)
}

func TestStationMobilityBDD(t *testing.T) {
	if mobRun(t, mobBrokerLines) != 0 {
		t.Fatal("features/tower/station_mobility.feature is not satisfied (see godog output above)")
	}
}
