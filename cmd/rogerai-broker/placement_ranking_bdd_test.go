package main

// placement_ranking_bdd_test.go makes features/tower/placement_ranking.feature EXECUTABLE.
//
// Every scenario runs against a fresh world: real in-process stores shared by one or two REAL
// broker instances over the production route table (registry, custody, stations, heads,
// health, mobility, the wallet store and the link mirror are shared, exactly as production
// shares PostgreSQL; link sessions stay per instance). Towers are real enrolled Towers with
// real link sessions, so their locality is MEASURED from the hub endpoint they advertise
// against the configured network-to-continent table, their health tier comes from recorded
// health checks and their trust stage from the governance record. Stations are placed through
// placeStation / chooseFrom / placementSnapshot and through POST /tower/edge/attach, with the
// Station's address carried the way the edge carries it (CF-Connecting-IP).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	mrand "math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/store"
	"rogerai.fm/roger/v7/internal/towercore/admit"
	"rogerai.fm/roger/v7/internal/towercore/attach"
	"rogerai.fm/roger/v7/internal/towercore/cert"
	"rogerai.fm/roger/v7/internal/towercore/dispatch"
	"rogerai.fm/roger/v7/internal/towercore/enroll"
	"rogerai.fm/roger/v7/internal/towercore/head"
	"rogerai.fm/roger/v7/internal/towercore/health"
	"rogerai.fm/roger/v7/internal/towercore/link"
	"rogerai.fm/roger/v7/internal/towercore/mobility"
	"rogerai.fm/roger/v7/internal/towercore/placement"
)

// prkNetTable is the configured network-to-continent table: the shared test table plus wider
// ranges, so a Station can be on the same continent as a Tower without sharing its /24.
const prkNetTable = "203.0.113.0/24 NA\n198.51.100.0/24 EU\n192.0.2.0/24 AF\n" +
	"203.0.0.0/16 NA\n198.51.0.0/16 EU\n192.0.0.0/16 AF\n"

// Where things are. Hubs are advertised on these endpoints; Stations attach from these
// addresses.
const (
	prkHubNA   = "203.0.113.9:8443"
	prkHubNA2  = "203.0.114.9:8443"
	prkHubEU   = "198.51.100.9:8443"
	prkHubEU2  = "198.51.200.9:8443"
	prkHubAF   = "192.0.2.9:8443"
	prkHubNone = "8.8.8.9:8443" // a bucket with no continent in the table: "unknown"

	prkHouseRig = "203.0.113.50" // the house rig, on HouseOC's network
	prkAtNA     = "203.0.7.7"
	prkAtEU     = "198.51.7.7"
	prkAtEUNet  = "198.51.100.50" // on the EU hub's own network
	prkAtAF     = "192.0.7.7"
)

type prkReply struct {
	code    int
	raw     string
	out     map[string]any
	tower   string
	station string
	body    map[string]any
	node    operator
}

type prkState struct {
	t *testing.T

	db       store.Store
	reg      admit.Store
	custody  cert.Custody
	enrolls  enroll.Store
	stations attach.Store
	heads    head.Store
	health   *health.MemStore
	mobility *mobility.MemStore
	mirror   *link.MemMirror
	nt       *netTable
	b        map[int]*broker
	srv      map[int]*httptest.Server
	hclock   time.Time

	towers map[string]linkTower // by scenario name
	ids    []string             // the Towers under test ("T" may be several)

	// the Station under test
	owner   string
	loc     placement.Locality
	house   bool
	exclude map[string]bool
	cond    string
	moveSID string

	replies   []prkReply
	alone     bool
	picks     []placed
	logBuf    bytes.Buffer
	capacity  int
	counts    map[string]int
	draws     int
	gapMax    int
	violation string
	nonMin    int
	before    map[string]towerFacts
	expected  string
	prevCap   func() int
	attempts  int
}

func (s *prkState) reset() {
	s.db = store.NewMem()
	s.reg = admit.NewMemStore()
	s.custody = cert.NewMemCustody()
	s.enrolls = enroll.NewMemStore()
	s.stations = attach.NewMemStore()
	s.heads = head.NewMemStore()
	s.health = health.NewMemStore()
	s.mobility = mobility.NewMemStore()
	s.mirror = nil
	s.nt = nil
	s.b = map[int]*broker{}
	s.srv = map[int]*httptest.Server{}
	s.hclock = time.Now().Add(-30 * time.Minute)
	s.towers = map[string]linkTower{}
	s.ids = nil
	s.owner, s.loc, s.house, s.exclude, s.cond, s.moveSID = "", placement.Locality{}, false, nil, "", ""
	s.replies, s.alone, s.picks = nil, false, nil
	s.logBuf.Reset()
	s.capacity, s.counts, s.draws, s.gapMax, s.violation, s.nonMin = 0, nil, 0, 0, "", 0
	s.before, s.expected, s.attempts = nil, "", 0
	s.prevCap = towerStationCapacity
}

// inst is broker instance i over the shared stores, built on first use.
func (s *prkState) inst(i int) *broker {
	if b, ok := s.b[i]; ok {
		return b
	}
	b := testBrokerWithDB(s.db)
	deps := linkDeps{stations: s.stations, heads: s.heads, health: s.health, mobility: s.mobility}
	if s.mirror != nil {
		deps.mirror = s.mirror
	}
	ts, err := newTowerSubsystem(b, s.reg, s.custody, s.enrolls, cert.Config{TTL: time.Hour}, deps)
	if err != nil {
		s.t.Fatalf("tower subsystem: %v", err)
	}
	b.tower = ts
	b.netTable = s.nt
	mux := http.NewServeMux()
	b.registerTowerRoutes(mux)
	srv := httptest.NewServer(mux)
	s.t.Cleanup(srv.Close)
	s.b[i], s.srv[i] = b, srv
	return b
}

func (s *prkState) b1() *broker                       { return s.inst(1) }
func (s *prkState) srv1() *httptest.Server            { s.inst(1); return s.srv[1] }
func (s *prkState) addr(ip string) placement.Locality { return s.b1().addrLocality(ip) }

// tower opens a real, active, established community Tower (or a house Tower) on instance i,
// advertising endpoint (and pin), and waits for Core's measurement of its hub.
func (s *prkState) towerOn(i int, name string, house bool, endpoint, pin string) linkTower {
	t := s.t
	b := s.inst(i)
	op := signedInOperator(t, b, "prk-"+strings.ToLower(name)+"-"+randHex(3))
	lt := enrolledTower(t, b, op.login)
	if err := b.tower.registry.Transition(lt.id, admit.StateActive); err != nil {
		t.Fatalf("activate %s: %v", name, err)
	}
	setStage(t, b, lt.id, stageEstablished)
	if house {
		makeHouse(t, b, op.login)
	}
	code, raw := lt.call(t, s.srv[i], "/tower/session", jsonOf(t, link.Hello{
		Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
		Capabilities: mandatoryCaps(), RelayEndpoint: endpoint, RelayTLSSPKI: pin,
	}), nil)
	if code != http.StatusOK {
		t.Fatalf("open %s: %d %s", name, code, raw)
	}
	waitMeasured(t, b, lt.id)
	s.towers[name] = lt
	return lt
}

func (s *prkState) tower(name string, house bool, endpoint string) linkTower {
	return s.towerOn(1, name, house, endpoint, "")
}

func (s *prkState) id(name string) string {
	lt, ok := s.towers[name]
	if !ok {
		s.t.Fatalf("no Tower named %s", name)
	}
	return lt.id
}

func (s *prkState) nameOf(id string) string {
	for n, lt := range s.towers {
		if lt.id == id {
			return n
		}
	}
	if id == "" {
		return "nowhere"
	}
	return id
}

// setTier records health checks so the Tower's tier is the one asked for: two failures make
// B, one pass makes A.
func (s *prkState) setTier(b *broker, id, tier string) error {
	rec := func(ok bool) error {
		s.hclock = s.hclock.Add(time.Second)
		return b.tower.health.Record(id, ok, s.hclock)
	}
	switch tier {
	case "A":
		if err := rec(true); err != nil {
			return err
		}
	case "B":
		if err := rec(false); err != nil {
			return err
		}
		if err := rec(false); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown tier %q", tier)
	}
	want := placement.TierA
	if tier == "B" {
		want = placement.TierB
	}
	now := time.Now()
	if got := b.towerTier(id, now, b.fleetTally(now)); got != want {
		return fmt.Errorf("tier of %s is %v, want %s", id, got, tier)
	}
	return nil
}

func (s *prkState) node(prefix string, house bool) operator {
	b := s.b1()
	op := signedInOperator(s.t, b, prefix+"-"+randHex(4))
	if house {
		makeHouse(s.t, b, op.login)
	}
	return op
}

func (s *prkState) houseOwnerKey() string {
	op := s.node("prk-house-owner", true)
	return ownerPubkeyOf(s.t, s.b1(), op.login)
}

// attachFrom is POST /tower/edge/attach signed by node, with the Station's address carried
// in CF-Connecting-IP as the edge carries it ("" = the loopback test address).
func (s *prkState) attachFrom(i int, node operator, ip string, body map[string]any) prkReply {
	t := s.t
	b := s.inst(i)
	if body == nil {
		body, _ = selfAttachBodyFor(t, b, node)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	const path = "/tower/edge/attach"
	req, err := http.NewRequest(http.MethodPost, s.srv[i].URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocol.SignRequest(node.priv, http.MethodPost, path, raw)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)
	apub, _ := body["assertion_key"].(string)
	priv, ok := assertionKeyOf(apub)
	if !ok {
		t.Fatalf("unknown assertion key %s", apub)
	}
	stationID, _ := body["station_id"].(string)
	skey, _ := body["session_key"].(string)
	req.Header.Set(protocol.HeaderAttachProof, protocol.AttachProof{
		Network: link.PublicNetwork, CallerPubkey: pub, TS: ts,
		StationID: stationID, AssertionKey: apub, SessionKey: skey, Body: raw,
	}.Sign(priv))
	if ip != "" {
		req.Header.Set("CF-Connecting-IP", ip)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var rawOut json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&rawOut)
	r := prkReply{code: resp.StatusCode, raw: string(rawOut), body: body, node: node}
	_ = json.Unmarshal(rawOut, &r.out)
	r.tower, _ = r.out["tower_id"].(string)
	r.station, _ = r.out["station_id"].(string)
	s.replies = append(s.replies, r)
	return r
}

func (s *prkState) last() prkReply {
	if len(s.replies) == 0 {
		s.t.Fatal("no attach was made")
	}
	return s.replies[len(s.replies)-1]
}

// fill attaches n Stations from the loopback address and requires each to land on towerID.
func (s *prkState) fill(n int, towerID string, house bool) error {
	for i := 0; i < n; i++ {
		r := s.attachFrom(1, s.node("prk-filler", house), "", nil)
		if r.code != http.StatusOK || r.tower != towerID {
			return fmt.Errorf("filler %d: %d on %s, want %s (%s)", i, r.code, s.nameOf(r.tower), s.nameOf(towerID), r.raw)
		}
	}
	return nil
}

// placedEvent is the station_placed event for one Station on one Tower.
func (s *prkState) placedEvent(b *broker, towerID, stationID string) (admit.Event, error) {
	var found []admit.Event
	for _, ev := range towerEventsOf(s.t, b, towerID, evStationPlaced) {
		if ev.Detail["station"] == stationID {
			found = append(found, ev)
		}
	}
	if len(found) != 1 {
		return admit.Event{}, fmt.Errorf("want one station_placed for %s on %s, got %d", stationID, s.nameOf(towerID), len(found))
	}
	return found[0], nil
}

func (s *prkState) snapIDs(b *broker) []string {
	var out []string
	for _, f := range b.placementSnapshot() {
		out = append(out, f.cand.TowerID)
	}
	sort.Strings(out)
	return out
}

func prkSorted(ids ...string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

func prkEqual(a, b []string) bool {
	return strings.Join(a, ",") == strings.Join(b, ",")
}

// liveLoad is a Tower's live Station count, from the store.
func (s *prkState) liveLoad(towerID string) int {
	atts, err := s.stations.ByTower(towerID)
	if err != nil {
		s.t.Fatal(err)
	}
	n := 0
	for _, at := range atts {
		if at.Live() {
			n++
		}
	}
	return n
}

// prkCountingSource counts every draw taken from it.
type prkCountingSource struct {
	n   int
	src mrand.Source
}

func (c *prkCountingSource) Int63() int64    { c.n++; return c.src.Int63() }
func (c *prkCountingSource) Seed(seed int64) { c.src.Seed(seed) }

// --- Background -----------------------------------------------------------------------------

func (s *prkState) mirrorReadable() error {
	s.mirror = link.NewMemMirror()
	s.inst(1)
	_, err := s.mirror.All()
	return err
}

func (s *prkState) netTableConfigured() error {
	nt, err := parseNetTable([]byte(prkNetTable))
	if err != nil {
		return err
	}
	s.nt = nt
	for _, b := range s.b {
		b.netTable = nt
	}
	if got := s.addr(prkAtNA); got.Continent != "NA" || got.Bucket != "203.0.7.0/24" {
		return fmt.Errorf("table not in force: %+v", got)
	}
	return nil
}

// --- the candidate set ----------------------------------------------------------------------

func (s *prkState) twoInstancesHoldingLinks() error {
	s.inst(2)
	s.towerOn(1, "T1", false, prkHubNA, strings.Repeat("ab", 32))
	s.towerOn(2, "T2", false, prkHubEU, strings.Repeat("cd", 32))
	for _, n := range []string{"T1", "T2"} {
		if _, there, err := s.mirror.Get(s.id(n)); err != nil || !there {
			return fmt.Errorf("%s has no mirror record (%v)", n, err)
		}
	}
	if !s.inst(2).tower.link.Live(s.id("T2")) {
		return fmt.Errorf("T2's link is not held by instance 2")
	}
	return nil
}

func (s *prkState) attachLandsOnInstance1() error {
	r := s.attachFrom(1, s.node("prk-node", false), "", nil)
	if r.code != http.StatusOK {
		return fmt.Errorf("attach: %d %s", r.code, r.raw)
	}
	return nil
}

func (s *prkState) bothCandidatesFromMirror() error {
	want := prkSorted(s.id("T1"), s.id("T2"))
	if got := s.snapIDs(s.b1()); !prkEqual(got, want) {
		return fmt.Errorf("instance 1 sees candidates %v, want %v", got, want)
	}
	r := s.last()
	ev, err := s.placedEvent(s.b1(), r.tower, r.station)
	if err != nil {
		return err
	}
	if fmt.Sprint(ev.Detail["candidates"]) != "2" {
		return fmt.Errorf("station_placed counted %v candidates, want 2", ev.Detail["candidates"])
	}
	return nil
}

func (s *prkState) checkPlaneFromMirror(r prkReply) error {
	rec, there, err := s.mirror.Get(r.tower)
	if err != nil || !there {
		return fmt.Errorf("no mirror record for %s (%v)", s.nameOf(r.tower), err)
	}
	if rec.Relay.Endpoint == "" || rec.Relay.TLSSPKI == "" {
		return fmt.Errorf("mirror record has no plane: %+v", rec.Relay)
	}
	if r.out["endpoint"] != rec.Relay.Endpoint || r.out["endpoint_tls_spki"] != rec.Relay.TLSSPKI {
		return fmt.Errorf("reply plane %v/%v, mirror record %s/%s", r.out["endpoint"], r.out["endpoint_tls_spki"],
			rec.Relay.Endpoint, rec.Relay.TLSSPKI)
	}
	return nil
}

func (s *prkState) planeFromSameMirrorRecord() error {
	first := s.last()
	if err := s.checkPlaneFromMirror(first); err != nil {
		return err
	}
	// The next node goes to the other (less loaded) Tower, so both the local and the remote
	// record are exercised.
	if err := s.attachLandsOnInstance1(); err != nil {
		return err
	}
	second := s.last()
	if second.tower == first.tower {
		return fmt.Errorf("both nodes went to %s; the second should take the less loaded Tower", s.nameOf(first.tower))
	}
	return s.checkPlaneFromMirror(second)
}

func (s *prkState) mirrorCannotBeRead() error {
	s.inst(2)
	s.tower("Local", false, prkHubNA)
	s.towerOn(2, "Remote", false, prkHubEU, "")
	s.mirror.FailForTest(true)
	if _, err := s.mirror.All(); err == nil {
		return fmt.Errorf("the mirror still answers")
	}
	return nil
}

func (s *prkState) aNodeAttaches() error {
	restore := captureLog(&s.logBuf)
	defer restore()
	r := s.attachFrom(1, s.node("prk-node", false), "", nil)
	if r.code != http.StatusOK {
		return fmt.Errorf("attach: %d %s", r.code, r.raw)
	}
	for i := 0; i < 3; i++ {
		got, ok := s.b1().placeStation("prk-pk", placement.Locality{}, nil)
		if !ok || got.towerID != s.id("Local") {
			return fmt.Errorf("placement %d went to %s", i, s.nameOf(got.towerID))
		}
	}
	return nil
}

func (s *prkState) onlyLocalCandidates() error {
	if got, want := s.snapIDs(s.b1()), []string{s.id("Local")}; !prkEqual(got, want) {
		return fmt.Errorf("candidates %v, want only %v", got, want)
	}
	r := s.last()
	if r.tower != s.id("Local") {
		return fmt.Errorf("attached to %s", s.nameOf(r.tower))
	}
	ev, err := s.placedEvent(s.b1(), r.tower, r.station)
	if err != nil {
		return err
	}
	if fmt.Sprint(ev.Detail["candidates"]) != "1" {
		return fmt.Errorf("station_placed counted %v candidates, want 1", ev.Detail["candidates"])
	}
	return nil
}

func (s *prkState) degradedLoggedOncePerMinute() error {
	if n := strings.Count(s.logBuf.String(), "placement degraded: mirror unavailable"); n != 1 {
		return fmt.Errorf("logged %d degraded lines over four placements, want 1", n)
	}
	return nil
}

func (s *prkState) neverInvents() error {
	for i := 0; i < 5; i++ {
		r := s.attachFrom(1, s.node("prk-node", false), "", nil)
		if r.code != http.StatusOK || r.tower != s.id("Local") {
			return fmt.Errorf("attach %d: %d on %s", i, r.code, s.nameOf(r.tower))
		}
	}
	if got, ok := s.b1().placeStation("prk-pk", placement.Locality{}, map[string]bool{s.id("Local"): true}); ok {
		return fmt.Errorf("with the only visible Tower excluded, placement invented %s", s.nameOf(got.towerID))
	}
	return nil
}

// --- exclusions -----------------------------------------------------------------------------

const (
	prkCondLifecycle = "its lifecycle state is not active or quarantine"
	prkCondRegistry  = "the registry says it may not take work"
	prkCondNoHub     = "it advertises no hub endpoint"
	prkCondStale     = "its link record is older than the freshness window"
	prkCondCapacity  = "it is at its Station capacity"
	prkCondCommCap   = "its trust stage caps community Stations and the cap is reached"
	prkCondHouseNone = "the Station is a house Station and its trust stage allows none"
	prkCondHouseCap  = "the Station is a house Station and its house cap is reached"
	prkCondMovingOff = "it is the Tower this Station is being moved off"
	prkCondFailedTry = "it is the Tower the Station's last failed move tried, within the move's retry window"
)

// mustPlaceOn requires the Station under test to be placed on towerID right now.
func (s *prkState) mustPlaceOn(towerID string) error {
	got, ok := s.b1().placeStation(s.owner, s.loc, s.exclude)
	if !ok || got.towerID != towerID {
		return fmt.Errorf("before the condition, the Station should go to %s; got %s (placed=%v)",
			s.nameOf(towerID), s.nameOf(got.towerID), ok)
	}
	return nil
}

func (s *prkState) towerLiveExcept(cond string) error {
	b := s.b1()
	s.cond = cond
	s.house = cond == prkCondHouseNone || cond == prkCondHouseCap
	if s.house {
		s.owner = s.houseOwnerKey()
	} else {
		s.owner = "prk-community-" + randHex(4)
	}
	// T is on the Station's own network: if it were a candidate, it would win.
	s.loc = s.addr(prkAtEUNet)

	switch cond {
	case prkCondLifecycle:
		for i, st := range []admit.State{admit.StateSuspended, admit.StateDraining, admit.StateRevoked, admit.StateExpired} {
			lt := s.tower(fmt.Sprintf("T-%s", st), false, prkHubEU)
			if err := s.mustPlaceOn(lt.id); err != nil {
				return err
			}
			if err := b.tower.registry.Transition(lt.id, st); err != nil {
				return fmt.Errorf("transition %d to %s: %v", i, st, err)
			}
			s.ids = append(s.ids, lt.id)
		}
		return nil
	case prkCondStale:
		// Short windows so "older than the freshness window" is a wait, not a stall; the rest
		// of the session layer is the production configuration.
		b.tower.link = link.New(link.Config{Network: link.PublicNetwork,
			Versions: []int{towerProtocolMin, towerProtocolMax}, Heartbeat: 50 * time.Millisecond,
			Freshness: 400 * time.Millisecond, Mirror: s.mirror})
	case prkCondCapacity:
		towerStationCapacity = func() int { return 1 }
	}

	endpoint := prkHubEU
	if cond == prkCondNoHub {
		endpoint = ""
	}
	lt := s.tower("T", false, endpoint)
	s.ids = []string{lt.id}
	if cond != prkCondNoHub && cond != prkCondHouseCap {
		if err := s.mustPlaceOn(lt.id); err != nil {
			return err
		}
	}

	switch cond {
	case prkCondRegistry:
		if err := b.tower.registry.ExpireLease(lt.id); err != nil {
			return err
		}
		tw, _ := b.tower.registry.Get(lt.id)
		if tw.State != admit.StateActive || b.tower.registry.MayTakeWork(lt.id) {
			return fmt.Errorf("want an active Tower the registry refuses work to; state %s", tw.State)
		}
	case prkCondNoHub:
		if !b.tower.link.Live(lt.id) {
			return fmt.Errorf("T should be live")
		}
		if plane, _ := b.tower.link.RelayPlane(lt.id); plane.Endpoint != "" {
			return fmt.Errorf("T advertises %q", plane.Endpoint)
		}
	case prkCondStale:
		time.Sleep(500 * time.Millisecond)
		if b.tower.link.Live(lt.id) {
			return fmt.Errorf("T's link record is still fresh")
		}
	case prkCondCapacity:
		return s.fill(1, lt.id, false)
	case prkCondCommCap:
		setStage(s.t, b, lt.id, stageNew)
		if err := s.mustPlaceOn(lt.id); err != nil {
			return err
		}
		return s.fill(stageCaps[stageNew].community, lt.id, false)
	case prkCondHouseNone:
		setStage(s.t, b, lt.id, stageNew)
		if stageCaps[stageNew].house != 0 {
			return fmt.Errorf("a new Tower allows %d house Stations", stageCaps[stageNew].house)
		}
	case prkCondHouseCap:
		setStage(s.t, b, lt.id, stageProving)
		if err := s.mustPlaceOn(lt.id); err != nil {
			return err
		}
		return s.fill(stageCaps[stageProving].house, lt.id, true)
	case prkCondMovingOff:
		s.moveSID = "prk-moving-" + randHex(4)
		ok, err := b.tower.mobility.StartMove(mobility.Move{StationID: s.moveSID, From: lt.id,
			Trigger: triggerBetter, Started: time.Now()})
		if err != nil || !ok {
			return fmt.Errorf("start move: %v %v", ok, err)
		}
		_, moving, excl := b.moveOnReattach(s.moveSID, time.Now())
		if !moving {
			return fmt.Errorf("the Station is not moving")
		}
		s.exclude = excl
	case prkCondFailedTry:
		s.moveSID = "prk-failed-" + randHex(4)
		if err := b.tower.mobility.FinishMove(s.moveSID, lt.id, time.Now().Add(time.Hour)); err != nil {
			return err
		}
		_, _, excl := b.moveOnReattach(s.moveSID, time.Now())
		s.exclude = excl
	default:
		return fmt.Errorf("no driver for condition %q", cond)
	}
	return nil
}

func (s *prkState) aStationIsPlaced() error {
	b := s.b1()
	got, ok := b.placeStation(s.owner, s.loc, s.exclude)
	s.alone = ok
	if ok {
		s.picks = append(s.picks, got)
	}
	// A far control Tower that can take the Station: the placement must land there.
	s.tower("C", false, prkHubAF)
	for i := 0; i < 10; i++ {
		got, ok := b.placeStation(s.owner, s.loc, s.exclude)
		if !ok {
			return fmt.Errorf("with the control Tower live, placement %d found nowhere", i)
		}
		s.picks = append(s.picks, got)
	}
	return nil
}

func (s *prkState) tIsNotACandidate() error {
	if s.alone {
		return fmt.Errorf("with only T live, the Station was placed on %s", s.nameOf(s.picks[0].towerID))
	}
	for _, p := range s.picks {
		if p.towerID != s.id("C") {
			return fmt.Errorf("placed on %s, want the control Tower (T ranks better, so T must have been a candidate)", s.nameOf(p.towerID))
		}
		// For a community Station every filtered Tower is out of the count. For a house
		// Station the community Towers that cannot take it are filtered by the draw rather
		// than before the count, so the count is not asserted there.
		if !s.house && fmt.Sprint(p.detail["candidates"]) != "1" {
			return fmt.Errorf("counted %v candidates, want 1 (the control)", p.detail["candidates"])
		}
	}
	if s.cond == prkCondFailedTry {
		// Only WITHIN the retry window: once it has passed, T is a candidate again and wins.
		_, _, excl := s.b1().moveOnReattach(s.moveSID, time.Now().Add(2*time.Hour))
		got, ok := s.b1().placeStation(s.owner, s.loc, excl)
		if !ok || got.towerID != s.id("T") {
			return fmt.Errorf("after the retry window T should be a candidate again; went to %s", s.nameOf(got.towerID))
		}
	}
	return nil
}

// --- capacity -------------------------------------------------------------------------------

func (s *prkState) hubCap(n int) error {
	if hubConnCap != n {
		return fmt.Errorf("the hub connection cap Core sizes on is %d, want %d", hubConnCap, n)
	}
	return nil
}

func (s *prkState) perStationWorkers() error {
	if stationHubWorkers != 4 {
		return fmt.Errorf("a node's default worker count is 4; Core sizes on %d", stationHubWorkers)
	}
	return nil
}

func (s *prkState) computeCapacity() error { s.capacity = towerStationCapacity(); return nil }

func (s *prkState) capacityIsCapOverWorkers() error {
	want := int(math.Floor(float64(hubConnCap) / float64(stationHubWorkers)))
	if s.capacity != want || s.capacity != 128 {
		return fmt.Errorf("capacity %d, want floor(%d/%d) = %d", s.capacity, hubConnCap, stationHubWorkers, want)
	}
	return nil
}

func (s *prkState) atCapacityUntilOneLeaves() error {
	b := s.b1()
	tw := s.tower("T", false, prkHubEU)
	var first string
	for i := 0; i < s.capacity; i++ {
		id := fmt.Sprintf("st-c%06x%s", i, randHex(4))
		attachStation(s.t, b, id, tw.id, "prk-cap-owner-"+randHex(4))
		if first == "" {
			first = id
		}
	}
	if n := s.liveLoad(tw.id); n != s.capacity {
		return fmt.Errorf("T holds %d live Stations, want %d", n, s.capacity)
	}
	r := s.attachFrom(1, s.node("prk-node", false), "", nil)
	if r.code != http.StatusServiceUnavailable {
		return fmt.Errorf("a Tower at capacity took a Station: %d %s", r.code, r.raw)
	}
	if _, err := b.tower.stations.Revoke(first); err != nil {
		return err
	}
	r = s.attachFrom(1, s.node("prk-node", false), "", nil)
	if r.code != http.StatusOK || r.tower != tw.id {
		return fmt.Errorf("after one left, attach: %d on %s %s", r.code, s.nameOf(r.tower), r.raw)
	}
	return nil
}

// --- house and community --------------------------------------------------------------------

func (s *prkState) houseAndEstablishedCommunity() error {
	h := s.tower("House", true, prkHubNA)
	c := s.tower("Community", false, prkHubEU)
	if st := s.b1().towerStage(c.id); st != stageEstablished {
		return fmt.Errorf("community Tower stage %v", st)
	}
	// The house Tower is the more loaded and the far one for the Station that follows.
	if err := s.fill(3, h.id, true); err != nil {
		return err
	}
	s.expected = prkAtEUNet
	return nil
}

func (s *prkState) aHouseStationIsPlaced() error {
	s.attachFrom(1, s.node("prk-house-node", true), s.expected, nil)
	return nil
}

func (s *prkState) goesToHouseWhatever() error {
	r := s.last()
	if r.code != http.StatusOK || r.tower != s.id("House") {
		return fmt.Errorf("house Station: %d on %s %s", r.code, s.nameOf(r.tower), r.raw)
	}
	snap := s.b1().placementSnapshot()
	h := s.b1().towerCandidate(snap, s.id("House"), s.addr(prkAtEUNet))
	c := s.b1().towerCandidate(snap, s.id("Community"), s.addr(prkAtEUNet))
	if !(h.Load > c.Load) || h.Class != placement.Far || c.Class != placement.SameNetwork {
		return fmt.Errorf("the scenario must put the house Tower behind on load and locality: house %+v community %+v", h, c)
	}
	return nil
}

func (s *prkState) noHouseTowerEligible() error {
	h := s.tower("House", true, prkHubNA)
	if err := s.b1().tower.registry.Transition(h.id, admit.StateSuspended); err != nil {
		return err
	}
	for _, f := range s.b1().placementSnapshot() {
		if f.cand.House {
			return fmt.Errorf("a house Tower is still a candidate")
		}
	}
	return nil
}

func (s *prkState) communityAllowingHouse() error {
	n := s.tower("Nora", false, prkHubAF)
	setStage(s.t, s.b1(), n.id, stageProving)
	if stageCaps[stageProving].house <= 0 {
		return fmt.Errorf("a proving Tower allows no house Stations")
	}
	return nil
}

func (s *prkState) goesToThatCommunity() error {
	r := s.last()
	if r.code != http.StatusOK || r.tower != s.id("Nora") {
		return fmt.Errorf("house Station: %d on %s %s", r.code, s.nameOf(r.tower), r.raw)
	}
	return nil
}

func (s *prkState) recordedWithReason(reason string) error {
	r := s.last()
	ev, err := s.placedEvent(s.b1(), r.tower, r.station)
	if err != nil {
		return err
	}
	if ev.Detail["reason"] != reason {
		return fmt.Errorf("reason %v, want %q", ev.Detail["reason"], reason)
	}
	return nil
}

func (s *prkState) noCommunityAllowsHouse() error {
	n := s.tower("Nora", false, prkHubAF)
	setStage(s.t, s.b1(), n.id, stageNew)
	return nil
}

func (s *prkState) aHouseStationAttaches() error {
	s.attachFrom(1, s.node("prk-house-node", true), "", nil)
	return nil
}

func (s *prkState) coreAnswers(msg string) error {
	r := s.last()
	if r.code != http.StatusServiceUnavailable {
		return fmt.Errorf("status %d, want 503: %s", r.code, r.raw)
	}
	if !strings.Contains(r.raw, msg+" - try again shortly") {
		return fmt.Errorf("answer %s, want %q", r.raw, msg)
	}
	return nil
}

func (s *prkState) nodeKeepsServingDirect() error {
	r := s.last()
	b := s.b1()
	nodeID, _ := r.body["node_id"].(string)
	b.mu.Lock()
	_, registered := b.nodes[nodeID]
	seen := b.lastSeen[nodeID]
	b.mu.Unlock()
	if !registered || time.Since(seen) > nodeTTL {
		return fmt.Errorf("the node's direct registration was disturbed (registered=%v)", registered)
	}
	apub, _ := r.body["assertion_key"].(string)
	if _, found, _ := b.tower.stations.ByAssertionKey(apub); found {
		return fmt.Errorf("a refused attach left an attachment behind")
	}
	return nil
}

func (s *prkState) sameTierAndClass() error {
	h := s.tower("House", true, prkHubNA)
	c := s.tower("Community", false, prkHubEU)
	snap := s.b1().placementSnapshot()
	hc := s.b1().towerCandidate(snap, h.id, placement.Locality{})
	cc := s.b1().towerCandidate(snap, c.id, placement.Locality{})
	if hc.Tier != cc.Tier || hc.Class != cc.Class {
		return fmt.Errorf("not the same tier and class: house %+v community %+v", hc, cc)
	}
	return nil
}

func (s *prkState) aCommunityStationIsPlaced() error {
	s.attachFrom(1, s.node("prk-node", false), "", nil)
	return nil
}

func (s *prkState) goesToCommunity() error {
	r := s.last()
	if r.code != http.StatusOK || r.tower != s.id("Community") {
		return fmt.Errorf("community Station: %d on %s %s", r.code, s.nameOf(r.tower), r.raw)
	}
	return nil
}

func (s *prkState) houseOnlyIfNoCommunityInBest() error {
	// While the community Tower shares the best tier and class it takes every community
	// Station, however loaded it gets relative to the empty house Tower.
	if err := s.fill(5, s.id("Community"), false); err != nil {
		return err
	}
	// Once no community Tower is in the best tier, the house Tower is considered.
	if err := s.setTier(s.b1(), s.id("Community"), "B"); err != nil {
		return err
	}
	r := s.attachFrom(1, s.node("prk-node", false), "", nil)
	if r.code != http.StatusOK || r.tower != s.id("House") {
		return fmt.Errorf("with the community Tower in tier B: %d on %s", r.code, s.nameOf(r.tower))
	}
	return nil
}

func (s *prkState) houseNearerCommunityFar() error {
	h := s.tower("House", true, prkHubNA)
	s.tower("Nora", false, prkHubAF)
	s.tower("OpEU", false, prkHubEU)
	s.expected = prkAtNA
	loc := s.addr(prkAtNA)
	snap := s.b1().placementSnapshot()
	if c := s.b1().towerCandidate(snap, h.id, loc).Class; c != placement.SameContinent {
		return fmt.Errorf("house Tower class %v", c)
	}
	for _, n := range []string{"Nora", "OpEU"} {
		if c := s.b1().towerCandidate(snap, s.id(n), loc).Class; c != placement.Far {
			return fmt.Errorf("%s class %v", n, c)
		}
	}
	return nil
}

func (s *prkState) theStationIsPlaced() error {
	switch {
	case s.expected == "seeded-equal":
		return s.equalLoadDraws()
	case s.expected == "one-best":
		return s.oneBestPlaced()
	}
	s.attachFrom(1, s.node("prk-node", false), s.expected, nil)
	return nil
}

func (s *prkState) goesToHouse() error {
	r := s.last()
	if r.code != http.StatusOK || r.tower != s.id("House") {
		return fmt.Errorf("%d on %s %s", r.code, s.nameOf(r.tower), r.raw)
	}
	return nil
}

// --- house Towers never win a tie -----------------------------------------------------------

func (s *prkState) rankingForCommunity() error {
	s.tower("H", true, prkHubNA)
	s.tower("C1", false, prkHubEU)
	s.tower("C2", false, prkHubAF)
	s.owner = "prk-community-" + randHex(4)
	return nil
}

func prkRank(tier string, c placement.Class) [2]int {
	t := 0
	if tier == "B" {
		t = 1
	}
	return [2]int{t, int(c)}
}

func prkLess(a, b [2]int) bool { return a[0] < b[0] || (a[0] == b[0] && a[1] < b[1]) }

func (s *prkState) houseOnlyWhenStrictlyBetter() error {
	b := s.b1()
	hubs := map[string]string{"H": prkHubNA, "C1": prkHubEU, "C2": prkHubAF}
	places := []string{prkHouseRig, prkAtNA, prkAtEUNet, prkAtEU, prkAtAF, ""}
	tiers := []string{"A", "B"}
	for _, th := range tiers {
		for _, t1 := range tiers {
			for _, t2 := range tiers {
				set := map[string]string{"H": th, "C1": t1, "C2": t2}
				for n, tier := range set {
					if err := s.setTier(b, s.id(n), tier); err != nil {
						return err
					}
				}
				for _, at := range places {
					loc := placement.Locality{}
					if at != "" {
						loc = s.addr(at)
					}
					rank := map[string][2]int{}
					for n, hub := range hubs {
						host, _, _ := net.SplitHostPort(hub)
						rank[n] = prkRank(set[n], placement.ClassOf(loc, s.addr(host)))
					}
					bestComm := rank["C1"]
					if prkLess(rank["C2"], bestComm) {
						bestComm = rank["C2"]
					}
					houseWins := prkLess(rank["H"], bestComm)
					snap := b.placementSnapshot()
					for i := 0; i < 40; i++ {
						s.draws++
						got, ok := b.chooseFrom(snap, s.owner, loc, nil, "", placementRand())
						if !ok {
							return fmt.Errorf("no placement for %v at %q", set, at)
						}
						isHouse := got.towerID == s.id("H")
						if houseWins != isHouse {
							return fmt.Errorf("tiers %v, Station at %q: house strictly better=%v but chose %s",
								set, at, houseWins, s.nameOf(got.towerID))
						}
						if !isHouse {
							n := s.nameOf(got.towerID)
							if rank[n] != bestComm {
								return fmt.Errorf("chose %s, not a best community Tower", n)
							}
							if got.detail["reason"] != string(placement.ReasonBest) {
								return fmt.Errorf("community pick reason %v", got.detail["reason"])
							}
						} else if got.detail["reason"] != string(placement.ReasonNearerHouse) {
							return fmt.Errorf("house pick reason %v", got.detail["reason"])
						}
						if rank["H"] == bestComm {
							s.counts["tie"]++
						}
					}
				}
			}
		}
	}
	return nil
}

func (s *prkState) noMixedDraw() error {
	// Every tie between the house Tower and the best community Tower(s) above went to the
	// community, over hundreds of draws: the house Tower never entered a tie's draw.
	if s.counts["tie"] < 100 {
		return fmt.Errorf("only %d tie draws were exercised", s.counts["tie"])
	}
	return nil
}

// --- the ranking ----------------------------------------------------------------------------

func (s *prkState) eligibleCandidates() error {
	s.tower("SN", false, prkHubEU)
	s.tower("SN2", false, "198.51.100.11:8443")
	s.tower("SC", false, prkHubEU2)
	s.tower("UN", false, prkHubNone)
	s.tower("FA", false, prkHubAF)
	s.tower("H", true, "198.51.100.10:8443")
	s.loc = s.addr(prkAtEUNet)
	s.owner = "prk-community-" + randHex(4)
	return nil
}

func (s *prkState) coreRanksThem() error {
	got := s.snapIDs(s.b1())
	var want []string
	for _, lt := range s.towers {
		want = append(want, lt.id)
	}
	if !prkEqual(got, prkSorted(want...)) {
		return fmt.Errorf("snapshot %v, want every eligible Tower %v", got, want)
	}
	return nil
}

func (s *prkState) place(owner string, excl ...string) (placed, bool) {
	ex := map[string]bool{}
	for _, n := range excl {
		ex[s.id(n)] = true
	}
	return s.b1().placeStation(owner, s.loc, ex)
}

func (s *prkState) tierAAboveB() error {
	b := s.b1()
	for _, n := range []string{"SN", "SN2"} {
		if err := s.setTier(b, s.id(n), "B"); err != nil {
			return err
		}
	}
	got, _ := s.place(s.owner, "H")
	if got.towerID != s.id("SC") {
		return fmt.Errorf("tier A same_continent should beat tier B same_network; got %s", s.nameOf(got.towerID))
	}
	got, _ = s.place(s.owner, "H", "SC", "UN")
	if got.towerID != s.id("FA") || got.detail["class"] != "far" || got.detail["tier"] != "A" {
		return fmt.Errorf("tier A far should beat tier B same_network; got %s %v", s.nameOf(got.towerID), got.detail)
	}
	for _, n := range []string{"SN", "SN2"} {
		if err := s.setTier(b, s.id(n), "A"); err != nil {
			return err
		}
	}
	return nil
}

func (s *prkState) classOrder() error {
	steps := []struct {
		excl  []string
		want  []string
		class string
	}{
		{[]string{"H"}, []string{"SN", "SN2"}, "same_network"},
		{[]string{"H", "SN", "SN2"}, []string{"SC"}, "same_continent"},
		{[]string{"H", "SN", "SN2", "SC"}, []string{"UN"}, "unknown"},
		{[]string{"H", "SN", "SN2", "SC", "UN"}, []string{"FA"}, "far"},
	}
	for _, st := range steps {
		got, ok := s.place(s.owner, st.excl...)
		if !ok {
			return fmt.Errorf("excluding %v: nowhere", st.excl)
		}
		hit := false
		for _, w := range st.want {
			hit = hit || got.towerID == s.id(w)
		}
		if !hit || got.detail["class"] != st.class {
			return fmt.Errorf("excluding %v: got %s (%v), want %v (%s)", st.excl, s.nameOf(got.towerID), got.detail["class"], st.want, st.class)
		}
	}
	return nil
}

func (s *prkState) drawOnLoad() error {
	b := s.b1()
	attachStation(s.t, b, "st-d"+randHex(8), s.id("SN"), "prk-load-owner-"+randHex(4))
	for i := 0; i < 30; i++ {
		got, _ := s.place(s.owner, "H")
		if got.towerID != s.id("SN2") {
			return fmt.Errorf("draw %d took the more loaded %s", i, s.nameOf(got.towerID))
		}
	}
	attachStation(s.t, b, "st-d"+randHex(8), s.id("SN2"), "prk-load-owner-"+randHex(4))
	seen := map[string]int{}
	for i := 0; i < 200; i++ {
		got, _ := s.place(s.owner, "H")
		seen[s.nameOf(got.towerID)]++
	}
	if seen["SN"] == 0 || seen["SN2"] == 0 || len(seen) != 2 {
		return fmt.Errorf("equal load within the class should split the draw: %v", seen)
	}
	return nil
}

func (s *prkState) communityOnlyDraw() error {
	// SN and SN2 now carry a Station each; the house Tower in the same tier and class
	// carries none, and still never wins a community Station.
	for i := 0; i < 100; i++ {
		got, _ := s.place(s.owner)
		if got.towerID == s.id("H") {
			return fmt.Errorf("the house Tower entered the community draw")
		}
	}
	got, _ := s.place(s.owner, "SN", "SN2")
	if got.towerID != s.id("H") || got.detail["reason"] != string(placement.ReasonNearerHouse) {
		return fmt.Errorf("with no community Tower in the best class the house Tower is considered; got %s %v",
			s.nameOf(got.towerID), got.detail["reason"])
	}
	// For a house Station the house group comes first, and the community only on failover.
	house := s.houseOwnerKey()
	got, _ = s.place(house)
	if got.towerID != s.id("H") || got.detail["group"] != "house" {
		return fmt.Errorf("house Station went to %s", s.nameOf(got.towerID))
	}
	got, _ = s.place(house, "H")
	if got.detail["reason"] != string(placement.ReasonHouseFailover) ||
		(got.towerID != s.id("SN") && got.towerID != s.id("SN2")) {
		return fmt.Errorf("house failover went to %s (%v)", s.nameOf(got.towerID), got.detail["reason"])
	}
	return nil
}

func (s *prkState) farTierA() error {
	s.tower("Far", false, prkHubAF)
	s.expected = prkAtEUNet
	return nil
}

func (s *prkState) nearTierB() error {
	n := s.tower("Near", false, prkHubEU)
	if err := s.setTier(s.b1(), n.id, "B"); err != nil {
		return err
	}
	snap := s.b1().placementSnapshot()
	loc := s.addr(prkAtEUNet)
	if c := s.b1().towerCandidate(snap, n.id, loc).Class; c != placement.SameNetwork {
		return fmt.Errorf("Near class %v", c)
	}
	if c := s.b1().towerCandidate(snap, s.id("Far"), loc).Class; c != placement.Far {
		return fmt.Errorf("Far class %v", c)
	}
	return nil
}

func (s *prkState) goesToFar() error {
	r := s.last()
	if r.code != http.StatusOK || r.tower != s.id("Far") {
		return fmt.Errorf("%d on %s %s", r.code, s.nameOf(r.tower), r.raw)
	}
	ev, err := s.placedEvent(s.b1(), r.tower, r.station)
	if err != nil {
		return err
	}
	if ev.Detail["class"] != "far" || ev.Detail["tier"] != "A" {
		return fmt.Errorf("recorded %v", ev.Detail)
	}
	return nil
}

// --- the two-choice draw --------------------------------------------------------------------

func (s *prkState) fiveShareBest() error {
	// Room for every Station in the run: capacity only scales load, it does not reorder it.
	towerStationCapacity = func() int { return 4096 }
	for i := 1; i <= 5; i++ {
		s.tower(fmt.Sprintf("F%d", i), false, fmt.Sprintf("198.51.100.%d:8443", 10+i))
	}
	s.loc = s.addr(prkAtEUNet)
	snap := s.b1().placementSnapshot()
	for _, f := range snap {
		c := s.b1().towerCandidate(snap, f.cand.TowerID, s.loc)
		if c.Tier != placement.TierA || c.Class != placement.SameNetwork {
			return fmt.Errorf("%s not in the best tier and class: %+v", s.nameOf(f.cand.TowerID), c)
		}
	}
	return nil
}

// prkTwoChoiceBound is the gap the two-choice draw keeps between the most and the least loaded
// of n Towers, at every point of the run. The theory (Berenbrink, Czumaj, Steger and Vocking,
// "Balanced allocations: the heavily loaded case") bounds it by ln ln n / ln 2 + O(1)
// INDEPENDENT of how many Stations are placed, where a uniform single draw grows as the square
// root of the count. The O(1) term is taken as 12: in 20000 simulated runs of exactly this
// draw (5 Towers, 1000 placements, distinct pair, coin on ties) the largest gap ever seen
// mid-run was 11, while every one of 2000 simulated runs of a single uniform draw exceeded 13.
func prkTwoChoiceBound(n int) int {
	return int(math.Log(math.Log(float64(n)))/math.Ln2) + 12
}

func (s *prkState) placeInARow(n int) error {
	b := s.b1()
	names := []string{"F1", "F2", "F3", "F4", "F5"}
	s.counts = map[string]int{}
	for i := 0; i < n; i++ {
		owner := fmt.Sprintf("prk-row-owner-%d-%s", i, randHex(2))
		got, ok := b.placeStation(owner, s.loc, nil)
		if !ok {
			return fmt.Errorf("placement %d found nowhere", i)
		}
		chosen := s.counts[s.nameOf(got.towerID)]
		minLoad, othersMax := math.MaxInt, -1
		for _, nm := range names {
			minLoad = min(minLoad, s.counts[nm])
			if nm != s.nameOf(got.towerID) {
				othersMax = max(othersMax, s.counts[nm])
			}
		}
		// Two drawn, the less loaded taken: the winner is never more loaded than every other.
		if chosen > othersMax && s.violation == "" {
			s.violation = fmt.Sprintf("placement %d took the strictly most loaded Tower (%d vs %d)", i, chosen, othersMax)
		}
		if chosen > minLoad {
			s.nonMin++ // the least loaded Tower was not drawn: a draw, not a global minimum
		}
		attachStation(s.t, b, fmt.Sprintf("st-e%06x%s", i, randHex(4)), got.towerID, owner)
		s.counts[s.nameOf(got.towerID)]++
		lo, hi := math.MaxInt, 0
		for _, nm := range names {
			lo, hi = min(lo, s.counts[nm]), max(hi, s.counts[nm])
		}
		s.gapMax = max(s.gapMax, hi-lo)
	}
	for _, nm := range names {
		if s.liveLoad(s.id(nm)) != s.counts[nm] {
			return fmt.Errorf("%s holds %d live Stations, counted %d", nm, s.liveLoad(s.id(nm)), s.counts[nm])
		}
	}
	s.attempts = n
	return nil
}

func (s *prkState) drawsTwoTakesLess() error {
	if s.violation != "" {
		return fmt.Errorf("%s", s.violation)
	}
	if s.nonMin == 0 {
		return fmt.Errorf("every placement took the global least loaded Tower: that is not a random draw of two")
	}
	return nil
}

func (s *prkState) withinTwoChoiceBound() error {
	if bound := prkTwoChoiceBound(5); s.gapMax > bound {
		return fmt.Errorf("the load gap reached %d, over the two-choice bound %d (%v)", s.gapMax, bound, s.counts)
	}
	return nil
}

func (s *prkState) seededPerPlacement() error {
	// Each placement gets its own generator from the system's randomness.
	a, c := placementRand(), placementRand()
	same := true
	for i := 0; i < 4; i++ {
		same = same && a.Int63() == c.Int63()
	}
	if same {
		return fmt.Errorf("two placements drew the same sequence")
	}
	// Identical inputs - the same owner, the same address, the same everything a node can
	// send - over five equally loaded Towers still spread across them.
	tb, srv := towerTestBroker(s.t)
	tb.netTable = s.nt
	for i := 1; i <= 5; i++ {
		lt := liveEdgeTower(s.t, tb, srv, fmt.Sprintf("prk-seed-%d", i), fmt.Sprintf("198.51.100.%d:8443", 30+i))
		waitMeasured(s.t, tb, lt.id)
	}
	seen := map[string]int{}
	for i := 0; i < 300; i++ {
		got, ok := tb.placeStation("prk-same-owner", s.loc, nil)
		if !ok {
			return fmt.Errorf("nowhere")
		}
		seen[got.towerID]++
	}
	if len(seen) != 5 {
		return fmt.Errorf("identical inputs reached only %d of 5 Towers: the draw follows the input", len(seen))
	}
	return nil
}

func (s *prkState) exactlyOneBest() error {
	s.tower("One", false, prkHubEU)
	s.tower("FarNA", false, prkHubNA)
	s.tower("FarAF", false, prkHubAF)
	s.loc = s.addr(prkAtEUNet)
	s.owner = "prk-community-" + randHex(4)
	s.expected = "one-best"
	return nil
}

func (s *prkState) oneBestPlaced() error {
	b := s.b1()
	src := &prkCountingSource{src: mrand.NewSource(time.Now().UnixNano())}
	got, ok := b.chooseFrom(b.placementSnapshot(), s.owner, s.loc, nil, "", mrand.New(src))
	if !ok {
		return fmt.Errorf("nowhere")
	}
	s.picks = append(s.picks, got)
	s.draws = src.n
	s.attachFrom(1, s.node("prk-node", false), prkAtEUNet, nil)
	return nil
}

func (s *prkState) goesToThatTower() error {
	if s.picks[0].towerID != s.id("One") {
		return fmt.Errorf("chose %s", s.nameOf(s.picks[0].towerID))
	}
	if s.draws != 0 {
		return fmt.Errorf("a single best candidate took %d random draws", s.draws)
	}
	if r := s.last(); r.code != http.StatusOK || r.tower != s.id("One") {
		return fmt.Errorf("attach: %d on %s", r.code, s.nameOf(r.tower))
	}
	// Control: two in the best class do draw, so the zero above is meaningful.
	s.tower("One2", false, "198.51.100.12:8443")
	src := &prkCountingSource{src: mrand.NewSource(1)}
	b := s.b1()
	if _, ok := b.chooseFrom(b.placementSnapshot(), s.owner, s.loc, nil, "", mrand.New(src)); !ok || src.n == 0 {
		return fmt.Errorf("two best candidates should draw (draws=%d)", src.n)
	}
	return nil
}

func (s *prkState) twoEqualLoad() error {
	s.tower("A1", false, "198.51.100.11:8443")
	s.tower("A2", false, "198.51.100.12:8443")
	s.loc = s.addr(prkAtEUNet)
	s.owner = "prk-community-" + randHex(4)
	if s.liveLoad(s.id("A1")) != s.liveLoad(s.id("A2")) {
		return fmt.Errorf("loads differ")
	}
	s.expected = "seeded-equal"
	return nil
}

func (s *prkState) equalLoadDraws() error {
	b := s.b1()
	snap := b.placementSnapshot()
	s.counts = map[string]int{}
	s.draws = 4000
	for i := 0; i < s.draws; i++ {
		got, ok := b.chooseFrom(snap, s.owner, s.loc, nil, "", placementRand())
		if !ok {
			return fmt.Errorf("nowhere")
		}
		s.counts[got.towerID]++
	}
	return nil
}

func (s *prkState) equalChance() error {
	for _, n := range []string{"A1", "A2"} {
		share := float64(s.counts[s.id(n)]) / float64(s.draws)
		if share < 0.45 || share > 0.55 {
			return fmt.Errorf("%s won %.3f of equal-load ties", n, share)
		}
	}
	return nil
}

func (s *prkState) idOrderDoesNotWin() error {
	first := prkSorted(s.id("A1"), s.id("A2"))[0]
	if share := float64(s.counts[first]) / float64(s.draws); share > 0.55 {
		return fmt.Errorf("the id that sorts first won %.3f of ties", share)
	}
	return nil
}

// --- worked placements ----------------------------------------------------------------------

var prkTowerSpec = regexp.MustCompile(`(\w+)\(([^)]*)\)`)

var prkHubOf = map[string]string{
	"HouseOC": prkHubNA, "HouseCore": prkHubEU, "Nora": prkHubAF, "OpNA": prkHubNA2,
}

var prkWhere = map[string]string{
	"the house rig": prkHouseRig, "an EU network": prkAtEU, "an AF network": prkAtAF, "an NA network": prkAtNA,
}

func (s *prkState) eligibleTowers(spec string) error {
	b := s.b1()
	var capped []string
	for _, m := range prkTowerSpec.FindAllStringSubmatch(spec, -1) {
		name, args := m[1], strings.Split(m[2], ",")
		for i := range args {
			args[i] = strings.TrimSpace(args[i])
		}
		hub, ok := prkHubOf[name]
		if !ok || len(args) < 2 {
			return fmt.Errorf("unknown Tower %q", m[0])
		}
		host, _, _ := net.SplitHostPort(hub)
		if got := s.addr(host).Continent; got != args[1] {
			return fmt.Errorf("%s's hub is measured in %s, the table says %s", name, got, args[1])
		}
		lt := s.tower(name, strings.HasPrefix(name, "House"), hub)
		if err := s.setTier(b, lt.id, args[0]); err != nil {
			return err
		}
		for _, q := range args[2:] {
			switch q {
			case "same net as house rig":
				if s.addr(host).Bucket != s.addr(prkHouseRig).Bucket {
					return fmt.Errorf("%s is not on the house rig's network", name)
				}
			case "established":
				setStage(s.t, b, lt.id, stageEstablished)
			case "new":
				setStage(s.t, b, lt.id, stageNew)
			case "at community cap":
				setStage(s.t, b, lt.id, stageNew)
				capped = append(capped, lt.id)
			default:
				return fmt.Errorf("unknown qualifier %q", q)
			}
		}
	}
	for _, id := range capped {
		if err := s.fill(stageCaps[stageNew].community, id, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *prkState) kindPlacedIn(kind, where string) error {
	ip, ok := prkWhere[where]
	if !ok {
		return fmt.Errorf("unknown place %q", where)
	}
	s.attachFrom(1, s.node("prk-"+kind, kind == "house"), ip, nil)
	return nil
}

func (s *prkState) goesTo(expected string) error {
	r := s.last()
	if expected == "nowhere" {
		if r.code != http.StatusServiceUnavailable || !strings.Contains(r.raw, "no tower can host this node right now") {
			return fmt.Errorf("want nowhere; got %d on %s %s", r.code, s.nameOf(r.tower), r.raw)
		}
		return nil
	}
	if r.code != http.StatusOK || r.tower != s.id(expected) {
		return fmt.Errorf("want %s; got %d on %s %s", expected, r.code, s.nameOf(r.tower), r.raw)
	}
	if _, err := s.placedEvent(s.b1(), r.tower, r.station); err != nil {
		return err
	}
	return nil
}

// --- what placement writes ------------------------------------------------------------------

func (s *prkState) corePlacesAStation() error {
	s.tower("EU", false, prkHubEU)
	s.tower("AF", false, prkHubAF)
	s.attachFrom(1, s.node("prk-node", false), prkAtEUNet, nil)
	s.attachFrom(1, s.node("prk-house-node", true), prkAtEUNet, nil)
	for _, r := range s.replies {
		if r.code != http.StatusOK {
			return fmt.Errorf("attach: %d %s", r.code, r.raw)
		}
	}
	return nil
}

func (s *prkState) stationPlacedEvent() error {
	for _, r := range s.replies {
		ev, err := s.placedEvent(s.b1(), r.tower, r.station)
		if err != nil {
			return err
		}
		if ev.TowerID != r.tower || ev.Actor != admit.ActorSystem {
			return fmt.Errorf("event %+v", ev)
		}
	}
	return nil
}

func (s *prkState) placedDetail() error {
	want := []map[string]string{
		{"group": "community", "reason": "best"},
		{"group": "house", "reason": "house failover"},
	}
	for i, r := range s.replies {
		ev, _ := s.placedEvent(s.b1(), r.tower, r.station)
		d := ev.Detail
		if d["station"] != r.station || d["class"] != "same_network" || d["tier"] != "A" ||
			d["group"] != want[i]["group"] || d["reason"] != want[i]["reason"] || fmt.Sprint(d["candidates"]) != "2" {
			return fmt.Errorf("placement %d detail %v", i, d)
		}
		keys := make([]string, 0, len(d))
		for k := range d {
			keys = append(keys, k)
		}
		if !prkEqual(prkSorted(keys...), prkSorted("station", "class", "tier", "group", "reason", "candidates")) {
			return fmt.Errorf("detail keys %v", keys)
		}
	}
	return nil
}

func (s *prkState) stationAttachedUnchanged() error {
	for _, r := range s.replies {
		var found []admit.Event
		for _, ev := range towerEventsOf(s.t, s.b1(), r.tower, evStationAttached) {
			if ev.Detail["station"] == r.station {
				found = append(found, ev)
			}
		}
		if len(found) != 1 {
			return fmt.Errorf("%d station_attached events for %s", len(found), r.station)
		}
		ev := found[0]
		if ev.Actor != "operator:"+r.node.login || len(ev.Detail) != 2 || ev.Detail["quarantine"] != false {
			return fmt.Errorf("station_attached changed: actor %s detail %v", ev.Actor, ev.Detail)
		}
	}
	return nil
}

var prkReplyKeys = []string{"station_id", "tower_id", "endpoint", "endpoint_tls_spki", "hub_token", "tower_key_hash", "state", "note"}

func (s *prkState) placedByRankedChoice() error {
	if len(s.towers) == 0 {
		s.towerOn(1, "T", false, prkHubEU, strings.Repeat("ab", 32))
	}
	r := s.attachFrom(1, s.node("prk-station-op", false), "", nil)
	if r.code != http.StatusOK {
		return fmt.Errorf("attach: %d %s", r.code, r.raw)
	}
	if _, err := s.placedEvent(s.b1(), r.tower, r.station); err != nil {
		return err
	}
	return nil
}

func (s *prkState) checkReplyShape(r prkReply) error {
	keys := make([]string, 0, len(r.out))
	for k := range r.out {
		keys = append(keys, k)
	}
	if !prkEqual(prkSorted(keys...), prkSorted(prkReplyKeys...)) {
		return fmt.Errorf("reply fields %v, want %v", prkSorted(keys...), prkSorted(prkReplyKeys...))
	}
	at, found, err := s.b1().tower.stations.Station(r.station)
	if err != nil || !found {
		return fmt.Errorf("no attachment for %s", r.station)
	}
	plane, _ := s.b1().tower.link.RelayPlane(r.tower)
	if r.out["tower_id"] != at.Origin.TowerID || r.out["endpoint"] != plane.Endpoint ||
		r.out["endpoint_tls_spki"] != plane.TLSSPKI || plane.TLSSPKI == "" ||
		r.out["hub_token"] != at.HubToken || r.out["hub_token"] == "" ||
		r.out["tower_key_hash"] != s.b1().towerKeyFingerprint(r.tower) || r.out["tower_key_hash"] == "" ||
		fmt.Sprint(r.out["state"]) != fmt.Sprint(at.State) {
		return fmt.Errorf("reply %v does not match the attachment and plane", r.out)
	}
	return nil
}

func (s *prkState) replySameFields() error { return s.checkReplyShape(s.last()) }

func (s *prkState) olderNodeAttaches() error {
	// The body an older node sends: the keys, the offer and its verified join; nothing about
	// placement.
	node := s.node("prk-old-node", false)
	body, _ := selfAttachBodyFor(s.t, s.b1(), node)
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	if !prkEqual(prkSorted(keys...), prkSorted("assertion_key", "session_key", "model", "modality", "node_id")) {
		return fmt.Errorf("an older node's body has %v", keys)
	}
	r := s.attachFrom(1, node, "", body)
	if r.code != http.StatusOK {
		return fmt.Errorf("older node: %d %s", r.code, r.raw)
	}
	return s.checkReplyShape(r)
}

func (s *prkState) liveOnT() error {
	t := s.tower("T", false, prkHubAF)
	r := s.attachFrom(1, s.node("prk-node", false), prkAtEUNet, nil)
	if r.code != http.StatusOK || r.tower != t.id {
		return fmt.Errorf("attach: %d on %s", r.code, s.nameOf(r.tower))
	}
	return nil
}

func (s *prkState) nodeReattaches() error {
	// A strictly better home now exists and T has fallen to tier B: a fresh placement would
	// not choose T.
	s.tower("Better", false, prkHubEU)
	if err := s.setTier(s.b1(), s.id("T"), "B"); err != nil {
		return err
	}
	if got, _ := s.b1().placeStation("prk-pk", s.addr(prkAtEUNet), nil); got.towerID != s.id("Better") {
		return fmt.Errorf("the control placement went to %s", s.nameOf(got.towerID))
	}
	first := s.replies[0]
	s.attachFrom(1, first.node, prkAtEUNet, first.body)
	return nil
}

func (s *prkState) answersFromExisting() error {
	first, r := s.replies[0], s.last()
	if r.code != http.StatusOK || r.tower != s.id("T") || r.station != first.station ||
		r.out["hub_token"] != first.out["hub_token"] || r.out["note"] != "already attached - this is your existing registration" {
		return fmt.Errorf("re-attach: %d %v", r.code, r.out)
	}
	if n := len(towerEventsOf(s.t, s.b1(), s.id("T"), evStationPlaced)); n != 1 {
		return fmt.Errorf("T has %d station_placed events, want the original 1", n)
	}
	if n := len(towerEventsOf(s.t, s.b1(), s.id("Better"), evStationPlaced)); n != 0 {
		return fmt.Errorf("placement re-ran: Better has %d station_placed events", n)
	}
	return nil
}

func (s *prkState) onlyMoveChanges() error {
	at, found, err := s.b1().tower.stations.Station(s.replies[0].station)
	if err != nil || !found || !at.Live() || at.Origin.TowerID != s.id("T") {
		return fmt.Errorf("the live Station's Tower changed: %+v", at)
	}
	if _, moving, _ := s.b1().tower.mobility.Move(at.StationID); moving {
		return fmt.Errorf("a move was started by a re-attach")
	}
	return nil
}

// --- money and self-dealing -----------------------------------------------------------------

func (s *prkState) payable(acct string) float64 {
	sp, err := s.b1().db.EarningSplitOf(acct, time.Now())
	if err != nil {
		s.t.Fatal(err)
	}
	return sp.Payable
}

// settle holds and settles one priced attempt worth 1.0 credit through r's Tower, for a
// consumer bound to consumerLogin ("" = an arms-length stranger), and returns the consumer's
// spend.
func (s *prkState) settle(r prkReply, attemptID, consumerLogin string) (float64, error) {
	t, b := s.t, s.b1()
	priv, ok := assertionKeyOf(r.body["assertion_key"].(string))
	if !ok {
		return 0, fmt.Errorf("no assertion key")
	}
	cpub := issuedEdgeGrantPriced(t, b, attemptID, r.tower, r.station, 0, 5_000_000_000)
	var wallet string
	if consumerLogin == "" {
		wallet = bindEdgeConsumer(t, b, cpub)
	} else {
		wallet = rebindConsumerTo(t, b, cpub, consumerLogin)
	}
	if _, err := b.db.AddCredits(wallet, 100); err != nil {
		return 0, err
	}
	before, _ := b.db.BalanceOf(wallet, 0)
	if held, err := b.db.HoldFor(wallet, attemptID, 10); err != nil || !held {
		return 0, fmt.Errorf("hold: %v %v", held, err)
	}
	body, err := json.Marshal(map[string]any{
		"tower_id": r.tower, "station_id": r.station, "attempt_id": attemptID,
		"receipt": signedReceiptTok(t, priv, attemptID, r.station, make([]byte, 300),
			dispatch.Usage{In: 0, Out: 300}, dispatch.Usage{In: 0, Out: 200}),
	})
	if err != nil {
		return 0, err
	}
	code, raw := s.towers["T"].call(t, s.srv1(), "/tower/edge/settle", body, nil)
	if code != http.StatusOK {
		return 0, fmt.Errorf("settle: %d %s", code, raw)
	}
	after, _ := b.db.BalanceOf(wallet, 0)
	return before - after, nil
}

func (s *prkState) stationOwner(r prkReply) string {
	at, found, err := s.b1().tower.stations.Station(r.station)
	if err != nil || !found {
		s.t.Fatalf("no attachment for %s", r.station)
	}
	return at.Owner
}

func (s *prkState) attemptSettles() error {
	s.b1().feeRate = 0.10
	spent, err := s.settle(s.last(), "prk-att-1", "")
	if err != nil {
		return err
	}
	s.counts = map[string]int{"spent_micro": int(math.Round(spent * 1e6))}
	return nil
}

func (s *prkState) sharesAsToday() error {
	r := s.last()
	tw, _ := s.b1().tower.registry.Get(r.tower)
	towerAcct := ownerPubkeyOf(s.t, s.b1(), tw.Owner)
	station := s.payable(s.stationOwner(r))
	relay := s.payable(towerAcct)
	spent := float64(s.counts["spent_micro"]) / 1e6
	// The same split the existing settlement tests pin (TestArmsLengthTrafficStillSettlesAndPaysBothShares):
	// 90% to the Station's owner, 5% of gross to the Tower operator, the rest to the platform.
	if math.Abs(spent-1.0) > 1e-6 || math.Abs(station-0.90) > 1e-6 || math.Abs(relay-0.05) > 1e-6 ||
		math.Abs(spent-station-relay-0.05) > 1e-6 {
		return fmt.Errorf("spent %.6f, Station %.6f, Tower %.6f, platform %.6f", spent, station, relay, spent-station-relay)
	}
	return nil
}

func (s *prkState) selfDealingUnchanged() error {
	// A second Station, placed by the ranked choice, whose consumer is its own owner's
	// account: the serving share is withheld, the arms-length Tower is still paid.
	if err := s.placedByRankedChoice(); err != nil {
		return err
	}
	r := s.last()
	tw, _ := s.b1().tower.registry.Get(r.tower)
	towerAcct := ownerPubkeyOf(s.t, s.b1(), tw.Owner)
	relayBefore := s.payable(towerAcct)
	if _, err := s.settle(r, "prk-att-2", r.node.login); err != nil {
		return err
	}
	if got := s.payable(s.stationOwner(r)); got != 0 {
		return fmt.Errorf("a self-dealt serving share was paid: %.6f", got)
	}
	if got := s.payable(towerAcct) - relayBefore; math.Abs(got-0.05) > 1e-6 {
		return fmt.Errorf("the arms-length Tower earned %.6f on the self-dealt attempt, want 0.05", got)
	}
	return nil
}

// --- an operator cannot buy placement -------------------------------------------------------

func (s *prkState) operatorSetsFields() error {
	b := s.b1()
	s.tower("X", false, prkHubEU2)
	y := s.tower("Y", false, "198.51.201.9:8443")
	z := s.tower("Z", false, prkHubAF)
	s.loc = s.addr(prkAtEU)
	s.owner = "prk-community-" + randHex(4)
	s.before = map[string]towerFacts{}
	for _, f := range b.placementSnapshot() {
		s.before[f.cand.TowerID] = f
	}
	// Y and Z reopen their sessions saying whatever they like: a zero relay price, a region,
	// a display name, a priority, a claimed locality, and an extra capability. Z, far away,
	// claims to be in the Station's region and network.
	for _, lt := range []linkTower{y, z} {
		plane, _ := b.tower.link.RelayPlane(lt.id)
		hello := map[string]any{
			"network": link.PublicNetwork, "versions": []int{1}, "tower_id": lt.id,
			"capabilities":   append(mandatoryCaps(), "priority"),
			"relay_endpoint": plane.Endpoint,
			"relay_price":    0, "relay_price_micros": 0, "price_in_micros": 0, "price_out_micros": 0,
			"region": "EU", "continent": "EU", "bucket": s.loc.Bucket, "display_name": "AAA best tower",
			"name": "000-first", "priority": 1000, "weight": 100, "tier": "A", "house": true,
		}
		code, raw := lt.call(s.t, s.srv1(), "/tower/session", jsonOf(s.t, hello), nil)
		if code != http.StatusOK {
			return fmt.Errorf("reopen: %d %s", code, raw)
		}
		want := s.before[lt.id].loc
		deadline := time.Now().Add(2 * time.Second)
		for {
			if got, ok := b.tower.link.Locality(lt.id); ok && got == want {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("%s's locality was not re-measured", s.nameOf(lt.id))
			}
			time.Sleep(time.Millisecond)
		}
	}
	return nil
}

func (s *prkState) stationsArePlaced() error {
	b := s.b1()
	snap := b.placementSnapshot()
	s.counts = map[string]int{}
	s.draws = 4000
	for i := 0; i < s.draws; i++ {
		got, ok := b.chooseFrom(snap, s.owner, s.loc, nil, "", placementRand())
		if !ok {
			return fmt.Errorf("nowhere")
		}
		s.counts[got.towerID]++
	}
	return nil
}

func (s *prkState) fieldsDoNotChangeRanking() error {
	b := s.b1()
	for _, f := range b.placementSnapshot() {
		was := s.before[f.cand.TowerID]
		if f.cand != was.cand || f.loc != was.loc || f.stage != was.stage || f.full != was.full {
			return fmt.Errorf("%s's ranking facts changed: %+v -> %+v", s.nameOf(f.cand.TowerID), was.cand, f.cand)
		}
	}
	if s.counts[s.id("Z")] != 0 {
		return fmt.Errorf("the far Tower claiming the Station's region won %d placements", s.counts[s.id("Z")])
	}
	for _, n := range []string{"X", "Y"} {
		share := float64(s.counts[s.id(n)]) / float64(s.draws)
		if share < 0.45 || share > 0.55 {
			return fmt.Errorf("%s won %.3f of placements against an equal Tower", n, share)
		}
	}
	return nil
}

// --- the suite ------------------------------------------------------------------------------

func TestPlacementRankingSpec(t *testing.T) {
	// The money scenarios settle the way the existing settlement tests do: the byte tariff
	// off (only the pinned token price bills) and earnings payable at once. The wallet store
	// reads the payout policy when it is built, so this is set before any world is.
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_IN", "0")
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_OUT", "0")
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	s := &prkState{t: t}
	suite := godog.TestSuite{
		Name: "placement-ranking",
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx2 context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset()
				return ctx2, nil
			})
			sc.After(func(ctx2 context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				towerStationCapacity = s.prevCap
				if s.mirror != nil {
					s.mirror.FailForTest(false)
				}
				return ctx2, nil
			})

			sc.Step(`^the link mirror is readable$`, s.mirrorReadable)
			sc.Step(`^a network-to-continent table is configured$`, s.netTableConfigured)

			sc.Step(`^Tower T1's link is held by broker instance 1 and Tower T2's by instance 2$`, s.twoInstancesHoldingLinks)
			sc.Step(`^a node's attach lands on instance 1$`, s.attachLandsOnInstance1)
			sc.Step(`^both T1 and T2 are candidates, read from the shared link mirror$`, s.bothCandidatesFromMirror)
			sc.Step(`^the chosen Tower's hub endpoint and certificate pin come from that same mirror record$`, s.planeFromSameMirrorRecord)
			sc.Step(`^the link mirror cannot be read$`, s.mirrorCannotBeRead)
			sc.Step(`^a node attaches$`, s.aNodeAttaches)
			sc.Step(`^only Towers whose link this instance holds are candidates$`, s.onlyLocalCandidates)
			sc.Step(`^Core logs one "placement degraded: mirror unavailable" line per minute at most$`, s.degradedLoggedOncePerMinute)
			sc.Step(`^placement never invents a Tower it cannot see$`, s.neverInvents)

			sc.Step(`^Tower T is live except that "([^"]*)"$`, s.towerLiveExcept)
			sc.Step(`^a Station is placed$`, s.aStationIsPlaced)
			sc.Step(`^T is not a candidate$`, s.tIsNotACandidate)

			sc.Step(`^a hub accepts at most (\d+) concurrent connections$`, s.hubCap)
			sc.Step(`^each Station holds as many connections as its worker count$`, s.perStationWorkers)
			sc.Step(`^Core computes the Tower's Station capacity$`, s.computeCapacity)
			sc.Step(`^it is the connection cap divided by the per-Station worker count, rounded down$`, s.capacityIsCapOverWorkers)
			sc.Step(`^a Tower at capacity takes no new Station until one leaves$`, s.atCapacityUntilOneLeaves)

			sc.Step(`^an eligible house Tower and an eligible established community Tower$`, s.houseAndEstablishedCommunity)
			sc.Step(`^a house Station is placed$`, s.aHouseStationIsPlaced)
			sc.Step(`^it goes to the house Tower, whatever the locality or load of either$`, s.goesToHouseWhatever)
			sc.Step(`^no house Tower is eligible$`, s.noHouseTowerEligible)
			sc.Step(`^a community Tower whose trust stage allows house Stations and has room under its house cap$`, s.communityAllowingHouse)
			sc.Step(`^it goes to that community Tower$`, s.goesToThatCommunity)
			sc.Step(`^the placement is recorded with reason "([^"]*)"$`, s.recordedWithReason)
			sc.Step(`^no community Tower allows house Stations$`, s.noCommunityAllowsHouse)
			sc.Step(`^a house Station attaches$`, s.aHouseStationAttaches)
			sc.Step(`^Core answers "([^"]*)" exactly as today$`, s.coreAnswers)
			sc.Step(`^the node keeps serving on the classic direct path, unaffected$`, s.nodeKeepsServingDirect)
			sc.Step(`^an eligible community Tower and an eligible house Tower in the same health tier and locality class$`, s.sameTierAndClass)
			sc.Step(`^a community Station is placed$`, s.aCommunityStationIsPlaced)
			sc.Step(`^it goes to the community Tower$`, s.goesToCommunity)
			sc.Step(`^the house Tower is considered only if no community Tower is in that best tier and class$`, s.houseOnlyIfNoCommunityInBest)
			sc.Step(`^a community Station whose locality class is "same_continent" with a house Tower and "far" with every community Tower$`, s.houseNearerCommunityFar)
			sc.Step(`^the Station is placed$`, s.theStationIsPlaced)
			sc.Step(`^it goes to the house Tower$`, s.goesToHouse)
			sc.Step(`^the ranking is applied to a community Station$`, s.rankingForCommunity)
			sc.Step(`^a house Tower is chosen only when it is in a strictly better health tier or locality class than every eligible community Tower$`, func() error {
				s.counts = map[string]int{}
				return s.houseOnlyWhenStrictlyBetter()
			})
			sc.Step(`^no draw ever includes a house Tower alongside a community Tower$`, s.noMixedDraw)

			sc.Step(`^the eligible candidates for a Station \(for a house Station, the house group first\)$`, s.eligibleCandidates)
			sc.Step(`^Core ranks them$`, s.coreRanksThem)
			sc.Step(`^a health tier A Tower ranks above any tier B Tower$`, s.tierAAboveB)
			sc.Step(`^within a tier, the locality classes rank same_network, then same_continent, then unknown, then far$`, s.classOrder)
			sc.Step(`^within one tier and one class, the choice is a two-choice draw on load$`, s.drawOnLoad)
			sc.Step(`^for a community Station the draw is over community Towers only, unless none is in that tier and class$`, s.communityOnlyDraw)
			sc.Step(`^Tower Far is tier A and "far" from the Station$`, s.farTierA)
			sc.Step(`^Tower Near is tier B and "same_network" with the Station$`, s.nearTierB)
			sc.Step(`^it goes to Tower Far$`, s.goesToFar)
			sc.Step(`^five eligible Towers share the best tier and locality class$`, s.fiveShareBest)
			sc.Step(`^(\d+) Stations are placed in a row$`, s.placeInARow)
			sc.Step(`^each placement draws two of the five at random and takes the less loaded$`, s.drawsTwoTakesLess)
			sc.Step(`^no Tower's load exceeds the least loaded one's by more than the two-choice bound$`, s.withinTwoChoiceBound)
			sc.Step(`^the draw is seeded per placement, never from anything the node sends$`, s.seededPerPlacement)
			sc.Step(`^exactly one Tower is in the best tier and locality class$`, s.exactlyOneBest)
			sc.Step(`^it goes to that Tower$`, s.goesToThatTower)
			sc.Step(`^the two drawn Towers have equal load$`, s.twoEqualLoad)
			sc.Step(`^either may be chosen with equal chance$`, s.equalChance)
			sc.Step(`^a Tower cannot win ties by choosing an id that sorts first$`, s.idOrderDoesNotWin)

			sc.Step(`^eligible Towers "([^"]*)"$`, s.eligibleTowers)
			sc.Step(`^a "([^"]*)" Station in "([^"]*)" is placed$`, s.kindPlacedIn)
			sc.Step(`^it goes to "([^"]*)"$`, s.goesTo)

			sc.Step(`^Core places a Station on a Tower$`, s.corePlacesAStation)
			sc.Step(`^the governance record gets a "station_placed" event for that Tower$`, s.stationPlacedEvent)
			sc.Step(`^its detail names the Station, the locality class, the health tier, the group \(house or community\), the reason and how many candidates there were$`, s.placedDetail)
			sc.Step(`^the existing "station_attached" event is unchanged$`, s.stationAttachedUnchanged)
			sc.Step(`^a Station is placed by the ranked choice$`, s.placedByRankedChoice)
			sc.Step(`^the reply carries the same fields as today: station id, Tower id, endpoint, pin, hub token, Tower key fingerprint and state$`, s.replySameFields)
			sc.Step(`^an older node that knows nothing of ranking attaches exactly as before$`, s.olderNodeAttaches)
			sc.Step(`^a Station is live on Tower T$`, s.liveOnT)
			sc.Step(`^its node re-attaches$`, s.nodeReattaches)
			sc.Step(`^Core answers from the existing attachment on Tower T, exactly as today$`, s.answersFromExisting)
			sc.Step(`^only a move \(station_mobility\.feature\) can change a live Station's Tower$`, s.onlyMoveChanges)

			sc.Step(`^an edge attempt through its Tower settles$`, s.attemptSettles)
			sc.Step(`^the Station, Tower and platform shares are computed exactly as today$`, s.sharesAsToday)
			sc.Step(`^the existing self-dealing checks run unchanged$`, s.selfDealingUnchanged)
			sc.Step(`^a Tower operator sets a lower relay price, a region, a display name or any other field they control$`, s.operatorSetsFields)
			sc.Step(`^Stations are placed$`, s.stationsArePlaced)
			sc.Step(`^none of those fields changes the ranking$`, s.fieldsDoNotChangeRanking)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"../../features/tower/placement_ranking.feature"},
			Strict: true, TestingT: t,
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("the placement ranking spec is not satisfied")
	}
}
