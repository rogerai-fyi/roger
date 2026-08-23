package main

// toweredge_eligibility_test.go is the spec for WHO may be placed on the edge path at all -
// as distinct from toweredge_placement_test.go, which asks who wins among those who may.
//
// It exists because edge placement had no eligibility gate. It filtered on three properties
// of the TOWER and the projection ROW - a non-empty endpoint, a self- offer, a tower that may
// take work - and asked nothing at all about the machine on the other end. pickFor, on the
// classic fabric, drops a stale node, a banned node, a banned owner's node and a private node
// before it scores anything.
//
// That gap was survivable while the relay fabric was opt-in behind `roger share --tower`. It
// stopped being survivable when the flag was removed: every signed-in share now joins, so the
// set of machines reachable this way is the whole fleet, and the M0 join means there is a node
// id on every row with which to ask each of those questions.

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/towercore/fleet"
)

// edgeFleet stands up a live tower with n self-attached, on-air stations and returns their
// node ids in attach order.
func edgeFleet(t *testing.T, n int) (*broker, []string) {
	t.Helper()
	b, srv := towerTestBroker(t)
	b.private = map[string]bool{}
	b.edgeInflight, b.edgeLoad = map[string]edgeAttemptLoad{}, map[string]int{}
	b.edgeOpenByAccount = map[string]int{}
	b.inflight, b.trust = map[string]int{}, map[string]trustState{}
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	var nodes []string
	for i := 0; i < n; i++ {
		op := signedInOperator(t, b, fmt.Sprintf("node-op-%d", i))
		body, _ := selfAttachBodyFor(t, b, op)
		var out struct {
			StationID string `json:"station_id"`
		}
		code, raw := op.attach(t, srv, body, &out)
		require.Equal(t, http.StatusOK, code, raw)
		nodeID := body["node_id"].(string)
		// The durable node -> account binding a real registration writes. It is what the
		// owner-ban set is resolved through, and without it a banned account's nodes cannot be
		// found at all - which would make the owner-ban test pass for the wrong reason.
		require.NoError(t, b.db.BindNode(nodeID, b.accountKeyOfPubkey(hexOf(op.priv.Public().(ed25519.PublicKey)))))
		nodes = append(nodes, nodeID)
	}
	return b, nodes
}

// placedNodes runs the real placement n times and reports which nodes it chose, opening each
// placement exactly as towerEdgeAuthorize opens it. The opening matters: with no load moving,
// equally-scored stations tie and the sampler resolves ties the same way every time, so a
// count of who was CHOSEN would not tell you who was ELIGIBLE - which is the only question
// this file asks.
func placedNodes(b *broker, n int) map[string]int {
	seen := map[string]int{}
	for i := 0; i < n; i++ {
		_, row, ok := b.edgeTargetFor("m", edgePlacementRand(), nil)
		if !ok {
			seen[""]++
			continue
		}
		seen[row.NodeID]++
		b.edgeEnterInflight(fmt.Sprintf("at-%s-%d", row.NodeID, i), row.NodeID, "u_probe", time.Now().Add(time.Hour))
	}
	return seen
}

// A NODE THAT WENT HOME IS NOT A CANDIDATE.
//
// Nothing ever assigns StateDetached outside terminal reaping, and publishRoutable republishes
// every live attachment on every sweep - so a machine that ran `roger share` once and pressed
// Ctrl-C stayed a routable candidate indefinitely. Routing to it is not degraded availability:
// it is a guaranteed timeout, a stranded consumer hold, and a burned attempt. The heartbeat is
// the only thing that knows, and placement now asks it.
func TestEdgePlacementDropsANodeThatStoppedHeartbeating(t *testing.T) {
	b, nodes := edgeFleet(t, 2)
	gone, stays := nodes[0], nodes[1]

	require.Contains(t, placedNodes(b, 60), gone, "both on-air nodes should be reachable to begin with")

	b.mu.Lock()
	b.lastSeen[gone] = time.Now().Add(-2 * nodeTTL)
	b.mu.Unlock()

	got := placedNodes(b, 60)
	require.NotContains(t, got, gone, "an offline node still took edge placements: %v", got)
	require.Equal(t, 60, got[stays], "the live node must absorb the traffic: %v", got)
}

// A BAN APPLIED AFTER ATTACH MUST BITE.
//
// MayEnroll is checked at attach time and nowhere else, and isOwnerBanned appeared on this
// path only for the CONSUMER. But a ban follows evidence, so it is applied AFTER the node is
// on the network by definition - that is how the fraud pipeline works. Until this gate, a
// banned operator's node kept taking paid edge traffic and accruing earnings.
func TestEdgePlacementDropsANodeBannedAfterItAttached(t *testing.T) {
	b, nodes := edgeFleet(t, 2)
	banned, stays := nodes[0], nodes[1]

	require.Contains(t, placedNodes(b, 60), banned, "the node is eligible before the ban")

	b.metricsMu.Lock()
	b.banned[banned] = true
	b.metricsMu.Unlock()

	got := placedNodes(b, 60)
	require.NotContains(t, got, banned, "a banned node kept taking paid edge work: %v", got)
	require.Equal(t, 60, got[stays])
}

// The DURABLE OWNER ban - the anti-rotation one, which follows the account rather than the
// node id, and which pickFor has enforced all along.
func TestEdgePlacementDropsABannedOwnersNode(t *testing.T) {
	b, nodes := edgeFleet(t, 2)
	banned, stays := nodes[0], nodes[1]

	acct, found := b.cachedOwnerOf(banned)
	require.True(t, found, "the attach flow binds the node to its account")
	b.metricsMu.Lock()
	if b.bannedOwners == nil {
		b.bannedOwners = map[string]bool{}
	}
	b.bannedOwners[acct] = true
	b.metricsMu.Unlock()

	got := placedNodes(b, 60)
	require.NotContains(t, got, banned, "a banned OWNER's node kept taking paid edge work: %v", got)
	require.Equal(t, 60, got[stays])
}

// A PRIVATE band is reachable by frequency code and by nothing else. The edge path is public
// placement, so it must never be the door that publishes one.
func TestEdgePlacementDropsAPrivateNode(t *testing.T) {
	b, nodes := edgeFleet(t, 2)
	priv, stays := nodes[0], nodes[1]

	b.mu.Lock()
	b.private[priv] = true
	b.mu.Unlock()

	got := placedNodes(b, 60)
	require.NotContains(t, got, priv, "a private band was published to the public relay fabric: %v", got)
	require.Equal(t, 60, got[stays])
}

// Health is GRADED where liveness is absolute. A probe-troubled node falls to Tier B, so a
// healthy peer always beats it - but if it is the only station left it still serves, because
// the edge fleet is small and a probe streak measured broker-to-node says little about a path
// that avoids the broker.
func TestEdgePlacementDemotesButDoesNotStrandAProbeTroubledNode(t *testing.T) {
	b, nodes := edgeFleet(t, 2)
	sick, healthy := nodes[0], nodes[1]

	b.metricsMu.Lock()
	b.trust[sick] = trustState{probed: true, probeOK: false, probeFails: probeDeadStreak}
	b.metricsMu.Unlock()

	got := placedNodes(b, 60)
	require.NotContains(t, got, sick, "a probe-dead node competed with a healthy one: %v", got)

	// Now it is all there is. Tier B is the fallback, not a second graveyard.
	b.mu.Lock()
	b.lastSeen[healthy] = time.Now().Add(-2 * nodeTTL)
	b.mu.Unlock()
	got = placedNodes(b, 20)
	require.Equal(t, 20, got[sick], "Tier B did not carry the last station standing: %v", got)
}

// THE NEVER-PROBED NODE MUST NOT SCORE THE CEILING.
//
// `tq, probed := b.trust[id]` reads MAP PRESENCE, not tq.probed, and observeRecount creates an
// entry on the first re-count of any served request with probed=false. trustState.score()
// starts at 1.0 and is only subtracted from, so one served request promoted a station from the
// 0.75 neutral to the score reserved for a canary-verified node - permanently, on zero liveness
// evidence, beating every honest newly attached station out of the P2C band.
func TestEdgeQualityDoesNotPromoteOnRecountEvidenceAlone(t *testing.T) {
	fresh := edgeQuality(trustState{})
	// Exactly the state observeRecount leaves behind after one clean re-count of one served
	// request: an entry exists, nothing has ever been probed.
	recounted := edgeQuality(trustState{recounts: 1, lastClaimed: 10, lastRecount: 10, lastExact: true})
	verified := edgeQuality(trustState{probed: true, probeOK: true, probeCompleted: true})

	require.Equal(t, edgeNeutralQuality, fresh, "an unmeasured node scores neutral")
	require.Equal(t, fresh, recounted,
		"one re-counted request promoted an unprobed station to %v (a fresh one scores %v) - "+
			"recount evidence is about honesty, not liveness, and must not buy a verified score", recounted, fresh)
	require.Greater(t, verified, recounted,
		"a canary-verified node (%v) must outrank one that has merely been re-counted (%v)", verified, recounted)
}

// Recount evidence is admitted in ONE direction. It cannot lift a station to the verified
// ceiling, but a node caught over-reporting its tokens is worse than an unknown one, and that
// finding is already trusted enough to hold its earnings - so it may pull the score down.
func TestEdgeQualityStillPunishesAnOverReporter(t *testing.T) {
	liar := edgeQuality(trustState{recounts: 4, discrepancies: 3})
	require.Less(t, liar, edgeNeutralQuality,
		"a node caught over-reporting (%v) must rank below an unknown one (%v)", liar, edgeNeutralQuality)
}

// The scores above have to reach the actual placement, not just the scoring function: a
// never-probed-but-recounted station must not beat a fresh one in the real draw.
func TestEdgePlacementDoesNotFavourARecountedButUnprobedStation(t *testing.T) {
	b, nodes := edgeFleet(t, 2)
	recounted, fresh := nodes[0], nodes[1]

	b.metricsMu.Lock()
	b.trust[recounted] = trustState{recounts: 1, lastClaimed: 10, lastRecount: 10, lastExact: true}
	b.metricsMu.Unlock()

	got := placedNodes(b, 400)
	require.Positive(t, got[fresh], "the honest fresh station was frozen out entirely: %v", got)
	require.Less(t, got[recounted], 300,
		"a station with one re-count and no canary took %d of 400 placements against a fresh peer: %v",
		got[recounted], got)
}

// A row with no join cannot be asked any of these questions, so it is not routable. Before M0
// that described every row; now it describes only a pre-join leftover in the projection.
func TestEdgePlacementRefusesARowWithNoJoin(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	op := signedInOperator(t, b, "node-op")
	body, _ := selfAttachBodyFor(t, b, op)
	var out struct {
		StationID string `json:"station_id"`
	}
	code, raw := op.attach(t, srv, body, &out)
	require.Equal(t, http.StatusOK, code, raw)

	// Rewrite the projection the way a pre-M0 publisher left it: an endpoint, a self- offer,
	// and nothing to identify the machine.
	require.NoError(t, b.tower.routable.Replace(tw.id, []fleet.Station{{
		TowerID: tw.id, StationID: out.StationID, OfferID: "self-" + out.StationID,
		Model: "m", Modality: "text",
		Expires: time.Now().Add(time.Hour), Endpoint: "203.0.113.9:8443",
	}}))

	_, _, ok := b.edgeTargetFor("m", edgePlacementRand(), nil)
	require.False(t, ok, "a row with no node id behind it was handed to a consumer")
}

// EVERY REFUSAL SAYS SO, INCLUDING THE FIRST ONE. logEdgePlacementRefusal exists because the
// 503 a consumer gets is deliberately uninformative and was uninformative to us as well: an
// empty fleet, a wholly banned fleet and a wholly unresolvable one produce the same answer and
// want completely different responses. Its own doc calls that "one line per refusal".
//
// The earliest return in edgeTargetFor - a broker with no tower subsystem, or one whose
// projection is not wired - was the hole in that. It is the case with no other symptom either:
// every edge consumer is refused, permanently, and it looks exactly like a fleet with nothing
// in it.
func TestEveryEdgePlacementRefusalIsLogged(t *testing.T) {
	var logged bytes.Buffer
	defer captureLog(&logged)()

	// No tower subsystem at all, which is what a broker deployed without one looks like.
	bare := &broker{}
	_, _, ok := bare.edgeTargetFor("m", nil, nil)
	require.False(t, ok)
	require.Contains(t, logged.String(), "REFUSED",
		"the earliest refusal on the placement path returns in silence")
	require.Contains(t, logged.String(), "no tower subsystem")

	// And the case just after it - a wired subsystem with an empty projection - still says its
	// own, different thing, so the two are told apart in an aggregator.
	logged.Reset()
	b, _ := towerTestBroker(t)
	_, _, ok = b.edgeTargetFor("nobody-serves-this", nil, nil)
	require.False(t, ok)
	require.Contains(t, logged.String(), "no Tower publishes a routable Station for this model")
}
