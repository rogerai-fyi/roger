package main

// toweredge_placement_cost_test.go is the spec for what a placement COSTS and what it SAYS.
//
// The placement's answer had tests. Its price did not, and its price was the problem: two
// database round trips per candidate, serialized, on a connection pool capped at eight because
// production is a small managed Postgres shared with the wallets, holds and settlement. A
// thirty-candidate model meant sixty-one queries standing between a consumer and a station, and
// the way that failure presents is a payment timeout somewhere else entirely.
//
// Nor did it say anything at all. No log line named the chosen station, the candidate count or
// the score, and the 503 was a bare jsonErr - so the one failure this path has already had
// (every request collapsing onto one station while the other operators earned nothing) would
// have produced no error, no timeout and no signal of any kind.
//
// These tests count the real queries through wrapped stores and read the real log output. They
// drive the real routes.

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/store"
	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/attach"
	"rogerai.fm/roger/v5/internal/towercore/cert"
	"rogerai.fm/roger/v5/internal/towercore/enroll"
	"rogerai.fm/roger/v5/internal/towercore/fleet"
	"rogerai.fm/roger/v5/internal/towercore/head"
)

// --- stores that count what they are asked -----------------------------------

// countingAttachStore wraps the real attachment store and counts the reads placement makes.
// Embedding rather than reimplementing: a hand-written fake would drift from the contract, and
// the point here is to measure the REAL path, not a model of it.
type countingAttachStore struct {
	attach.Store
	byStation  atomic.Int64
	byStations atomic.Int64
}

func (c *countingAttachStore) ByStation(id string) (attach.Attachment, bool, error) {
	c.byStation.Add(1)
	return c.Store.ByStation(id)
}

func (c *countingAttachStore) ByStations(ids []string) (map[string]attach.Attachment, error) {
	c.byStations.Add(1)
	return c.Store.ByStations(ids)
}

// countingAdmitStore counts TowerByID, which is the query behind admit.Registry.MayTakeWork -
// the other half of the per-candidate cost.
type countingAdmitStore struct {
	admit.Store
	towerByID atomic.Int64
}

func (c *countingAdmitStore) TowerByID(id string) (admit.Tower, bool, error) {
	c.towerByID.Add(1)
	return c.Store.TowerByID(id)
}

// countingFleetStore counts the projection read, so the "+1" in the old 2N+1 is accounted for
// rather than assumed.
type countingFleetStore struct {
	fleet.Store
	candidates atomic.Int64
}

func (c *countingFleetStore) Candidates(model string, now time.Time) ([]fleet.Station, error) {
	c.candidates.Add(1)
	return c.Store.Candidates(model, now)
}

type placementQueries struct {
	attach *countingAttachStore
	admit  *countingAdmitStore
	fleet  *countingFleetStore
}

func (q *placementQueries) reset() {
	q.attach.byStation.Store(0)
	q.attach.byStations.Store(0)
	q.admit.towerByID.Store(0)
	q.fleet.candidates.Store(0)
}

// total is every store round trip a placement made. On a durable deployment each one is a
// query on the shared pool, which is the quantity that matters.
func (q *placementQueries) total() int64 {
	return q.attach.byStation.Load() + q.attach.byStations.Load() +
		q.admit.towerByID.Load() + q.fleet.candidates.Load()
}

// countingTowerBroker is towerTestBroker with every store placement touches wrapped in a
// counter. Same construction otherwise - the real subsystem over the real route table.
func countingTowerBroker(t *testing.T) (*broker, *httptest.Server, *placementQueries) {
	t.Helper()
	q := &placementQueries{
		attach: &countingAttachStore{Store: attach.NewMemStore()},
		admit:  &countingAdmitStore{Store: admit.NewMemStore()},
		fleet:  &countingFleetStore{Store: fleet.NewMemStore()},
	}
	b := testBrokerWithDB(store.NewMem())
	ts, err := newTowerSubsystem(b, q.admit, cert.NewMemCustody(), enroll.NewMemStore(),
		cert.Config{TTL: time.Hour},
		linkDeps{stations: q.attach, heads: head.NewMemStore(), routable: q.fleet})
	require.NoError(t, err)
	b.tower = ts

	mux := http.NewServeMux()
	b.registerTowerRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return b, srv, q
}

// attachEdgeStations brings n self-attached stations on air behind one live tower.
func attachEdgeStations(t *testing.T, b *broker, srv *httptest.Server, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		node := signedInOperator(t, b, fmt.Sprintf("cost-op-%d", i))
		body, _ := selfAttachBodyFor(t, b, node)
		var out struct {
			StationID string `json:"station_id"`
		}
		code, raw := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &out)
		require.Equal(t, http.StatusOK, code, raw)
		require.NotEmpty(t, out.StationID)
	}
}

// PLACEMENT COSTS A CONSTANT NUMBER OF QUERIES, not two per candidate.
//
// The bound is per TOWER, not per station: one projection read, one eligibility read for each
// distinct tower in the candidate set, and one batched attachment read for all of them. With
// twelve stations behind one tower that is three queries where it used to be twenty-five.
//
// The exact numbers are asserted rather than a ceiling, because both halves of the fix are
// separately loseable - a memoization that memoizes nothing still passes a generous bound, and
// so does a batch read called once per candidate.
func TestEdgePlacementCostsAConstantNumberOfQueries(t *testing.T) {
	const stations = 12
	b, srv, q := countingTowerBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	attachEdgeStations(t, b, srv, stations)

	q.reset()
	_, row, ok := b.edgeTargetFor("m", edgePlacementRand())
	require.True(t, ok)
	require.NotEmpty(t, row.StationID)

	require.Equal(t, int64(1), q.fleet.candidates.Load(), "the projection is read once")
	require.Equal(t, int64(1), q.admit.towerByID.Load(),
		"a tower's eligibility is one question per tower, not one per station behind it")
	require.Equal(t, int64(1), q.attach.byStations.Load(),
		"every candidate's attachment comes back in one read")
	require.Zero(t, q.attach.byStation.Load(),
		"the per-candidate single-row read is what this change removed")
	require.Equal(t, int64(3), q.total(),
		"%d candidates cost %d queries; the old shape cost 2N+1 = %d",
		stations, q.total(), 2*stations+1)
}

// The same bound must hold as the fleet grows - a "constant" that quietly tracks N is the bug
// wearing a different number. Doubling the stations must not move the query count at all.
func TestEdgePlacementQueryCountDoesNotGrowWithTheFleet(t *testing.T) {
	measure := func(t *testing.T, stations int) int64 {
		t.Helper()
		b, srv, q := countingTowerBroker(t)
		liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
		attachEdgeStations(t, b, srv, stations)
		q.reset()
		_, _, ok := b.edgeTargetFor("m", edgePlacementRand())
		require.True(t, ok)
		return q.total()
	}
	small := measure(t, 3)
	large := measure(t, 24)
	require.Equal(t, small, large,
		"eight times the fleet cost %d queries instead of %d", large, small)
}

// A PLACEMENT SAYS WHERE IT WENT. Without this line the only way to notice edge traffic
// collapsing onto one station is an operator complaining that they stopped earning.
func TestEdgePlacementIsLogged(t *testing.T) {
	b, srv, _ := countingTowerBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	attachEdgeStations(t, b, srv, 3)

	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	_, row, ok := b.edgeTargetFor("m", edgePlacementRand())
	log.SetOutput(old)
	require.True(t, ok)

	line := buf.String()
	require.Contains(t, line, "edge placement", "placement produced no log line at all")
	require.Contains(t, line, "station="+row.StationID, "the chosen station is not named")
	require.Contains(t, line, "tower="+row.TowerID, "the tower carrying it is not named")
	require.Contains(t, line, "node="+row.NodeID)
	require.Contains(t, line, "candidates=3", "the candidate count is what shows a shrinking fleet")
	require.Contains(t, line, "score=", "the score is what shows placement turning into a magnet")
	require.Contains(t, line, "load=")

	// The consumer is not a supply-side fact and must not be in an ops log line.
	require.NotContains(t, line, "u_", "a placement line must not name the account it was for")
}

// AND A REFUSAL SAYS WHY. The 503 is deliberately uninformative to the caller; it must not be
// uninformative to us. An empty fleet, a banned fleet and an unresolvable fleet all produced the
// same silence, and they want completely different responses.
func TestEdgePlacementRefusalIsLoggedWithItsReason(t *testing.T) {
	b, srv, _ := countingTowerBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	attachEdgeStations(t, b, srv, 1)

	// Take the machine off air: the attachment is still live, so the row is still published and
	// still resolvable, and the ONLY thing that refuses it is the eligibility gate. That is the
	// case a log line has to distinguish from "nobody offers this model".
	b.mu.Lock()
	for id := range b.lastSeen {
		b.lastSeen[id] = time.Now().Add(-time.Hour)
	}
	b.mu.Unlock()

	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	_, _, ok := b.edgeTargetFor("m", edgePlacementRand())
	log.SetOutput(old)
	require.False(t, ok)

	line := buf.String()
	require.Contains(t, line, "edge placement", "a refusal produced no log line at all")
	require.Contains(t, line, "REFUSED")
	require.Contains(t, line, "candidates=1", "the count separates an empty fleet from a refused one")
	require.Contains(t, line, "resolvable=1")
	require.Contains(t, line, "stale", "the reason must name the gate that emptied the pool")

	// And the model that nobody serves at all is a DIFFERENT line, with different counts.
	buf.Reset()
	log.SetOutput(&buf)
	_, _, ok = b.edgeTargetFor("nobody-serves-this", edgePlacementRand())
	log.SetOutput(old)
	require.False(t, ok)
	require.Contains(t, buf.String(), "candidates=0")
}

// --- capacity, derived rather than carried -----------------------------------

// A MEASURED RIG ABSORBS MORE CONCURRENT WORK THAN A LAPTOP BEFORE ITS SCORE SAGS.
//
// The edge score divided by a flat 1+load, which is the capacity=1 case, because the only
// capacity in reach was the projection's hardcoded 1. The M0 node join reaches the real signal -
// tokens/sec observed while the node was already busy - and edgeEligible holds both locks that
// guard it, so the divisor is now the same capacity-normalized loadFactor the paid router uses.
//
// Two nodes carrying the same two attempts must therefore score differently when one of them has
// been measured to handle more. Under the old flat divisor they scored identically.
func TestEdgeScoreNormalizesLoadByMeasuredCapacity(t *testing.T) {
	b := towerTestBrokerNoServer(t)
	b.mu.Lock()
	b.nodes["n-rig"] = nodeReg("n-rig")
	b.nodes["n-laptop"] = nodeReg("n-laptop")
	b.mu.Unlock()

	b.metricsMu.Lock()
	// Observed UNDER LOAD, which is the incentive-compatible input capacityOf takes: a node
	// cannot win a bigger allotment by being fast on an idle probe.
	b.concurrentTPS = map[string]float64{}
	b.concurrentTPS["n-rig"] = 400
	b.concurrentTPS["n-laptop"] = 0
	b.edgeLoad = map[string]int{"n-rig": 2, "n-laptop": 2}
	b.metricsMu.Unlock()

	rig := b.edgeCandidateScore(fleet.Station{StationID: "s-rig", NodeID: "n-rig"})
	laptop := b.edgeCandidateScore(fleet.Station{StationID: "s-laptop", NodeID: "n-laptop"})
	require.Greater(t, rig, laptop,
		"two open attempts sag a measured rig (%.4f) as hard as an unmeasured laptop (%.4f)", rig, laptop)

	// And an UNMEASURED node is scored exactly as it was before capacity existed: the fallback
	// is the same conservative hardware prior pickFor uses, which is 1. Nothing about placement
	// on a fleet nobody has measured may change.
	require.InDelta(t, edgeNeutralQuality/3.0, laptop, 1e-9,
		"an unmeasured node must still be quality/(1+load)")
}

// nodeReg is the minimum registration edgeEligible and the capacity derivation need: a node the
// broker knows about, with no hardware string, so capacityOf falls to its conservative prior.
func nodeReg(id string) protocol.NodeRegistration { return protocol.NodeRegistration{NodeID: id} }

// --- the per-request PRNG ------------------------------------------------------

// A PLACEMENT'S RANDOMNESS MUST NOT COST FIVE KILOBYTES.
//
// edgePlacementRand was rand.New(rand.NewSource(...)), and math/rand's default source is a
// lagged Fibonacci generator: seeding it fills and stirs a 607-element int64 table, about 4.9KB
// and eighteen hundred iterations, per authorize - to produce the two random numbers a
// power-of-two-choices draw actually consumes.
//
// The bound is asserted in BYTES rather than allocation count because that is what was wrong:
// the old shape allocated twice, and so does the new one. A count-based test would have passed
// against the bug.
func TestEdgePlacementRandomnessIsCheap(t *testing.T) {
	res := testing.Benchmark(func(bench *testing.B) {
		bench.ReportAllocs()
		for i := 0; i < bench.N; i++ {
			r := edgePlacementRand()
			_ = r.Float64()
			_ = r.Float64()
		}
	})
	const bound = 512 // the old lagged-Fibonacci source alone was ~4.9KB
	require.Less(t, res.AllocedBytesPerOp(), int64(bound),
		"a placement's PRNG allocates %d bytes; the whole point of the draw is that it is cheap",
		res.AllocedBytesPerOp())
}

// And it must still be RANDOM - a cheap generator that returns the same value is a magnet with
// better performance characteristics.
func TestEdgePlacementRandomnessDiffersPerRequest(t *testing.T) {
	seen := map[float64]bool{}
	for i := 0; i < 64; i++ {
		seen[edgePlacementRand().Float64()] = true
	}
	require.Greater(t, len(seen), 32,
		"64 fresh placement PRNGs produced %d distinct first draws", len(seen))
}
