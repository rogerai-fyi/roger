package main

// toweredge_abuse_test.go is the spec for what one account may do to the fleet through
// /tower/edge/authorize.
//
// The route was registered bare: no b.rl, no b.anonRL, no per-account cap on open attempts.
// And an authorize is the cheapest consequential request on the network - a few hundred bytes,
// a refundable ceiling hold - that reserves a real station before the consumer has submitted
// anything. Measured against the real handler: 500 attempts opened for $0.0001 of held (not
// spent) balance, each one pinning a chosen station for the full grant-plus-settlement window.
//
// The three harms that made it urgent were all on the OTHER fabric, because the pin was written
// into b.inflight: the classic paid router divides by that number, peers merge it, and
// probeOnce skips any node with a non-zero count - so one sustained attempt could stop a victim
// being canary-probed ever again. The load signals are separate now, and these are the bounds
// that keep the edge path's own signal honest.

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ONE ACCOUNT CANNOT HOLD THE FLEET OPEN. The standing cap is the bound that matters: the rate
// limiter caps how FAST attempts are opened, and nothing in it caps how many stay open.
func TestEdgeAuthorizeCapsSimultaneouslyOpenAttemptsPerAccount(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	routableEdge(t, b, tw.id, "st-1", "m", "203.0.113.7:8443")
	// A rate limit generous enough that it is not what refuses: this leg is about the standing
	// cap, and a test that cannot tell the two apart proves neither.
	b.rl = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 100000, burst: 100000}

	consumer := signedInConsumer(t, b)
	opened, refused := 0, 0
	for i := 0; i < maxOpenEdgeAttemptsPerAccount*3; i++ {
		code, out := consumerCall(t, srv, consumer, "/tower/edge/authorize",
			map[string]any{"model": "m", "consumer_env_key": testEnvKeyHex(t)})
		switch code {
		case http.StatusOK:
			opened++
		case http.StatusTooManyRequests:
			refused++
		default:
			t.Fatalf("attempt %d answered %d: %v", i, code, out)
		}
	}
	require.Equal(t, maxOpenEdgeAttemptsPerAccount, opened,
		"one account opened %d simultaneous edge attempts (cap is %d)", opened, maxOpenEdgeAttemptsPerAccount)
	require.Positive(t, refused, "the cap never refused anything")

	// And the cap is a STANDING one, not a lifetime quota: closing attempts frees the slots.
	b.metricsMu.Lock()
	ids := make([]string, 0, len(b.edgeInflight))
	for id := range b.edgeInflight {
		ids = append(ids, id)
	}
	b.metricsMu.Unlock()
	for _, id := range ids {
		b.edgeExitInflight(id)
	}
	code, out := consumerCall(t, srv, consumer, "/tower/edge/authorize",
		map[string]any{"model": "m", "consumer_env_key": testEnvKeyHex(t)})
	require.Equal(t, http.StatusOK, code, "settling the open attempts did not free the account's slots: %v", out)
}

// The RATE bound, keyed on the account wallet rather than the device key - so a caller cannot
// multiply its rate by minting keypairs against one account.
func TestEdgeAuthorizeIsRateLimitedPerAccount(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	attachStation(t, b, "st-1", tw.id, "owner-1")
	routableEdge(t, b, tw.id, "st-1", "m", "203.0.113.7:8443")
	b.rl = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 60, burst: 3}

	consumer := signedInConsumer(t, b)
	var lastCode int
	var lastOut map[string]any
	for i := 0; i < 6; i++ {
		lastCode, lastOut = consumerCall(t, srv, consumer, "/tower/edge/authorize",
			map[string]any{"model": "m", "consumer_env_key": testEnvKeyHex(t)})
		if lastCode == http.StatusTooManyRequests {
			break
		}
	}
	require.Equal(t, http.StatusTooManyRequests, lastCode,
		"the authorize route accepted an unbounded rate: %v", lastOut)
}

// THE POINT OF THE WHOLE SEPARATION: an edge attempt must not be able to steer the classic
// paid fabric or silence the prober.
//
// probeOnce skips any node with a non-zero b.inflight, so before this a single sustained
// authorize froze a victim's canary probing - and with it verifiedServing(), the /market
// signal and the concierge gate - for as long as the attacker cared to keep it open. pickFor
// divides by the same number, so the same attempt cut the victim's paid-fabric score.
func TestOpenEdgeAttemptsDoNotTouchTheClassicLoadCounter(t *testing.T) {
	b := &broker{
		inflight: map[string]int{}, edgeInflight: map[string]edgeAttemptLoad{},
		edgeLoad: map[string]int{}, edgeOpenByAccount: map[string]int{},
		trust: map[string]trustState{},
	}
	for i := 0; i < 50; i++ {
		b.edgeEnterInflight(fmt.Sprintf("at-%d", i), "victim", "u_attacker", time.Now().Add(time.Hour))
	}

	require.Equal(t, 50, b.nodeEdgeLoad("victim"), "edge placement must still see the load it created")
	require.Zero(t, b.nodeInFlight("victim"),
		"50 open edge attempts pinned the victim's RELAYED in-flight count - "+
			"that is the number pickFor divides by and probeOnce skips on")
}

// The same pin, seen from the two consumers of b.inflight, on a real fleet: the classic
// router's score for a node under heavy edge load is the score it had when idle, and the
// prober still considers it.
func TestEdgeLoadDoesNotSuppressProbingOrPaidRouting(t *testing.T) {
	b, nodes := edgeFleet(t, 1)
	victim := nodes[0]

	for i := 0; i < 40; i++ {
		b.edgeEnterInflight(fmt.Sprintf("at-%d", i), victim, "u_attacker", time.Now().Add(time.Hour))
	}
	b.metricsMu.Lock()
	classic := b.inflight[victim]
	b.metricsMu.Unlock()
	require.Zero(t, classic,
		"probeOnce skips a node whose inflight is above zero; an outside party could stop this node being probed")
	require.Positive(t, b.nodeEdgeLoad(victim), "and edge placement still sees the reservation it made")
}

// The reservation is bounded by the window the WORK had, not the window the EVIDENCE has. The
// grant's deadline is when the Station starts refusing the attempt, so past it the node is by
// definition not carrying it; holding the pin for the courier's sake kept a station reserved
// for minutes over a request that could only have run for one.
func TestEdgeReservationExpiresWithTheGrantNotTheSettlementWindow(t *testing.T) {
	b := &broker{
		inflight: map[string]int{}, edgeInflight: map[string]edgeAttemptLoad{},
		edgeLoad: map[string]int{}, edgeOpenByAccount: map[string]int{},
	}
	deadline := time.Now().Add(40 * time.Millisecond)
	b.edgeEnterInflight("at-1", "n-1", "u_a", deadline)
	require.Equal(t, 1, b.nodeEdgeLoad("n-1"))
	require.Eventually(t, func() bool { return b.nodeEdgeLoad("n-1") == 0 }, 5*time.Second, 5*time.Millisecond,
		"the reservation outlived the deadline past which the Station refuses the work")
	// The account's slot came back with it - otherwise an attacker's abandoned attempts would
	// still cost them nothing and cost the account cap everything.
	b.metricsMu.Lock()
	held := b.edgeOpenByAccount["u_a"]
	b.metricsMu.Unlock()
	require.Zero(t, held, "an expired attempt kept the account's slot")
}
