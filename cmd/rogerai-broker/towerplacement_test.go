package main

// Ranked placement, as the broker runs it. Contract: features/tower/placement_ranking.feature.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/store"
	"rogerai.fm/roger/v7/internal/towercore/admit"
	"rogerai.fm/roger/v7/internal/towercore/attach"
	"rogerai.fm/roger/v7/internal/towercore/cert"
	"rogerai.fm/roger/v7/internal/towercore/enroll"
	"rogerai.fm/roger/v7/internal/towercore/head"
	"rogerai.fm/roger/v7/internal/towercore/link"
	"rogerai.fm/roger/v7/internal/towercore/placement"
)

// houseTower makes a live Tower whose owner is a house account.
func houseTower(t *testing.T, b *broker, srv *httptest.Server, login, endpoint string) linkTower {
	t.Helper()
	lt := liveEdgeTower(t, b, srv, login, endpoint)
	makeHouse(t, b, login)
	return lt
}

func makeHouse(t *testing.T, b *broker, login string) {
	t.Helper()
	if b.stationLimitExempt == nil {
		b.stationLimitExempt = map[string]bool{}
	}
	b.stationLimitExempt[ownerPubkeyOf(t, b, login)] = true
}

func attachTowerOf(t *testing.T, b *broker, srv *httptest.Server, node operator) (int, string, string) {
	t.Helper()
	body, _ := selfAttachBodyFor(t, b, node)
	var out struct {
		TowerID string `json:"tower_id"`
	}
	code, raw := node.attach(t, srv, body, &out)
	return code, out.TowerID, raw
}

func TestAHouseStationGoesToAHouseTower(t *testing.T) {
	b, srv := towerTestBroker(t)
	community := liveEdgeTower(t, b, srv, "nora", "198.51.100.9:8443")
	house := houseTower(t, b, srv, "roger-house", "203.0.113.9:8443")

	node := signedInOperator(t, b, "house-or")
	makeHouse(t, b, "house-or")
	code, tower, raw := attachTowerOf(t, b, srv, node)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, house.id, tower)

	other := signedInOperator(t, b, "someone")
	code, tower, raw = attachTowerOf(t, b, srv, other)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, community.id, tower, "a community Station: ties go to the community Tower")
}

func TestAHouseStationWithOnlyANewCommunityTowerIsNotPlaced(t *testing.T) {
	b, srv := towerTestBroker(t)
	nora := liveEdgeTower(t, b, srv, "nora", "198.51.100.9:8443")
	setStage(t, b, nora.id, stageNew)
	node := signedInOperator(t, b, "house-or")
	makeHouse(t, b, "house-or")
	code, _, raw := attachTowerOf(t, b, srv, node)
	require.Equal(t, http.StatusServiceUnavailable, code, raw)
	require.Contains(t, raw, "no tower can host this node right now")
}

func TestEveryPlacementIsRecordedWithWhy(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := liveEdgeTower(t, b, srv, "nora", "198.51.100.9:8443")
	node := signedInOperator(t, b, "someone")
	code, _, raw := attachTowerOf(t, b, srv, node)
	require.Equal(t, http.StatusOK, code, raw)

	evs := towerEventsOf(t, b, tw.id, evStationPlaced)
	require.Len(t, evs, 1)
	d := evs[0].Detail
	require.Equal(t, "system", evs[0].Actor)
	require.NotEmpty(t, d["station"])
	require.Equal(t, "unknown", d["class"])
	require.Equal(t, "A", d["tier"])
	require.Equal(t, "community", d["group"])
	require.Equal(t, "best", d["reason"])
	require.EqualValues(t, 1, d["candidates"])
	require.Len(t, towerEventsOf(t, b, tw.id, evStationAttached), 1, "station_attached is unchanged")
}

func TestPlacementRanksHealthThenLocality(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.netTable = testNetTable(t)
	na := liveEdgeTower(t, b, srv, "op-na", "203.0.113.9:8443")
	eu := liveEdgeTower(t, b, srv, "op-eu", "198.51.100.9:8443")
	waitMeasured(t, b, na.id)
	waitMeasured(t, b, eu.id)

	station := placement.Locality{Bucket: "198.51.100.0/24", Continent: "EU"}
	got, _ := b.placeStation("someone-pubkey", station, nil)
	require.Equal(t, eu.id, got.towerID, "same network wins")
	require.Equal(t, "same_network", got.detail["class"])

	// EU's last two health checks fail: tier B, and NA (far) wins.
	now := time.Now()
	require.NoError(t, b.tower.health.Record(eu.id, false, now.Add(-time.Minute)))
	require.NoError(t, b.tower.health.Record(eu.id, false, now))
	got, _ = b.placeStation("someone-pubkey", station, nil)
	require.Equal(t, na.id, got.towerID, "a healthy far Tower beats an unhealthy near one")
	require.Equal(t, "far", got.detail["class"])
}

func TestATowerThatCannotTakeTheStationIsNotACandidate(t *testing.T) {
	b, srv := towerTestBroker(t)
	prev := towerStationCapacity
	towerStationCapacity = func() int { return 1 }
	t.Cleanup(func() { towerStationCapacity = prev })
	// Fill "full" with one live Station while it is the only Tower.
	full := liveEdgeTower(t, b, srv, "op-full", "203.0.113.9:8443")
	attachTo(t, b, srv, full.id)

	ok := liveEdgeTower(t, b, srv, "op-ok", "198.51.100.9:8443")
	suspended := liveEdgeTower(t, b, srv, "op-susp", "192.0.2.9:8443")
	require.NoError(t, b.tower.registry.Transition(suspended.id, admit.StateSuspended))
	noHub := liveEdgeTower(t, b, srv, "op-nohub", "")

	for i := 0; i < 5; i++ {
		got, okd := b.placeStation("pk", placement.Locality{}, map[string]bool{})
		require.True(t, okd)
		require.Equal(t, ok.id, got.towerID)
		require.NotEqual(t, noHub.id, got.towerID)
	}
	// Excluded (the Tower a move is leaving): nowhere left.
	_, okd := b.placeStation("pk", placement.Locality{}, map[string]bool{ok.id: true})
	require.False(t, okd)
}

func TestACommunityTowerAtItsStageCapTakesNoMoreCommunityStations(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := liveEdgeTower(t, b, srv, "nora", "198.51.100.9:8443")
	setStage(t, b, tw.id, stageNew)
	for i := 0; i < stageCaps[stageNew].community; i++ {
		attachTo(t, b, srv, tw.id)
	}
	_, ok := b.placeStation("pk", placement.Locality{}, nil)
	require.False(t, ok, "a new Tower holds at most %d community Stations", stageCaps[stageNew].community)
}

func TestPlacementWithTheMirrorDownSaysSoAndUsesOnlyLocalLinks(t *testing.T) {
	m := link.NewMemMirror()
	b := testBrokerWithDB(store.NewMem())
	ts, err := newTowerSubsystem(b, admit.NewMemStore(), cert.NewMemCustody(), enroll.NewMemStore(),
		cert.Config{TTL: time.Hour}, linkDeps{stations: attach.NewMemStore(), heads: head.NewMemStore(), mirror: m})
	require.NoError(t, err)
	b.tower = ts
	mux := http.NewServeMux()
	b.registerTowerRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	tw := liveEdgeTower(t, b, srv, "nora", "198.51.100.9:8443")
	m.FailForTest(true)
	var buf bytes.Buffer
	defer captureLog(&buf)()
	got, ok := b.placeStation("pk", placement.Locality{}, nil)
	require.True(t, ok)
	require.Equal(t, tw.id, got.towerID)
	b.placeStation("pk", placement.Locality{}, nil)
	require.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("placement degraded: mirror unavailable")), "at most once a minute")
}

// attachTo attaches a fresh community node and asserts it landed on towerID.
func attachTo(t *testing.T, b *broker, srv *httptest.Server, towerID string) {
	t.Helper()
	node := signedInOperator(t, b, "filler-"+randHex(4))
	code, got, raw := attachTowerOf(t, b, srv, node)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, towerID, got)
}

// setStage records a Tower's trust stage as the ramp would, newest wins.
func setStage(t *testing.T, b *broker, towerID string, st placement.Stage) {
	t.Helper()
	ev := admit.Event{TowerID: towerID, Kind: evTrustStage, Actor: admit.ActorSystem, To: admit.State(st), At: time.Now()}
	require.NoError(t, b.tower.registry.Record(ev))
	b.dropStageCache(ev) // as every broker write does
	require.Equal(t, st, b.towerStage(towerID))
}

func TestHouseStationsFailOverToAProvingTowerUpToItsCap(t *testing.T) {
	b, srv := towerTestBroker(t)
	nora := liveEdgeTower(t, b, srv, "nora", "198.51.100.9:8443")
	setStage(t, b, nora.id, stageProving)
	for i := 0; i < stageCaps[stageProving].house; i++ {
		node := signedInOperator(t, b, "house-"+randHex(4))
		makeHouse(t, b, node.login)
		code, got, raw := attachTowerOf(t, b, srv, node)
		require.Equal(t, http.StatusOK, code, raw)
		require.Equal(t, nora.id, got)
	}
	evs := towerEventsOf(t, b, nora.id, evStationPlaced)
	require.Equal(t, "house failover", evs[0].Detail["reason"])
	require.Equal(t, "house", evs[0].Detail["group"])

	node := signedInOperator(t, b, "house-over")
	makeHouse(t, b, node.login)
	code, _, raw := attachTowerOf(t, b, srv, node)
	require.Equal(t, http.StatusServiceUnavailable, code, "the house cap is reached: %s", raw)
}

func TestManyTowersUnderOneOwnerShareOneHouseFailoverCap(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "nora")
	var ids []string
	for i := 0; i < 3; i++ {
		lt := enrolledTower(t, b, op.login)
		require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateActive))
		code, raw := lt.call(t, srv, "/tower/session", jsonOf(t, link.Hello{Network: link.PublicNetwork,
			Versions: []int{1}, TowerID: lt.id, Capabilities: mandatoryCaps(),
			RelayEndpoint: "198.51.100." + string(rune('1'+i)) + ":8443"}), nil)
		require.Equal(t, http.StatusOK, code, raw)
		setStage(t, b, lt.id, stageProving)
		ids = append(ids, lt.id)
	}
	placedN := 0
	for i := 0; i < 3*stageCaps[stageProving].house; i++ {
		node := signedInOperator(t, b, "house-"+randHex(4))
		makeHouse(t, b, node.login)
		if code, _, _ := attachTowerOf(t, b, srv, node); code == http.StatusOK {
			placedN++
		}
	}
	require.Equal(t, stageCaps[stageProving].house, placedN, "one owner's Towers hold %d house Stations in total", stageCaps[stageProving].house)
}

func TestAHouseTowerWinsACommunityStationOnlyWhenNearer(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.netTable = testNetTable(t)
	house := houseTower(t, b, srv, "roger-house", "203.0.113.9:8443")
	nora := liveEdgeTower(t, b, srv, "nora", "192.0.2.9:8443")
	waitMeasured(t, b, house.id)
	waitMeasured(t, b, nora.id)
	got, ok := b.placeStation("someone", placement.Locality{Bucket: "203.0.113.0/24", Continent: "NA"}, nil)
	require.True(t, ok)
	require.Equal(t, house.id, got.towerID)
	require.Equal(t, "nearer house tower", got.detail["reason"])

	got, _ = b.placeStation("someone", placement.Locality{Continent: "AF", Bucket: "192.0.2.0/24"}, nil)
	require.Equal(t, nora.id, got.towerID)
}

// REVIEW FINDING (godog run): a house Station's placement counted community Towers its stage
// keeps closed as candidates.
func TestTheRecordedCandidateCountIsOfTowersThatCouldTakeTheStation(t *testing.T) {
	b, srv := towerTestBroker(t)
	nora := liveEdgeTower(t, b, srv, "nora", "198.51.100.9:8443")
	setStage(t, b, nora.id, stageNew) // allows no house Stations
	houseTower(t, b, srv, "roger-house", "203.0.113.9:8443")
	signedInOperator(t, b, "house-or")
	makeHouse(t, b, "house-or")
	got, ok := b.placeStation(ownerPubkeyOf(t, b, "house-or"), placement.Locality{}, nil)
	require.True(t, ok)
	require.EqualValues(t, 1, got.detail["candidates"])
}
