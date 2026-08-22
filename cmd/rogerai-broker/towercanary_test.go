package main

// towercanary_test.go covers the canary's judgement and its sweep. The healthy-path pass
// (through a REAL hub + serving node) lives in towercanary_sealed_test.go; here are the
// failure shapes, the sweep, and the guard rails. The raw-TLS relay rig this file used to
// stand up died with the leaf-station generation.
//
// Contract: features/tower/edge_dispatch.feature.

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/admit"
	"rogerai.fm/roger/v6/internal/towercore/fleet"
	"rogerai.fm/roger/v6/internal/towercore/reputation"
)

// deadHubTower is a tower whose self-attached node's hub endpoint is DEAD - attached and
// routable, serving nothing.
func deadHubTower(t *testing.T, b *broker) string {
	t.Helper()
	tw := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))
	attachStation(t, b, "st-1", tw.id, "owner-1")
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	deadAddr := dead.Addr().String()
	require.NoError(t, dead.Close())
	require.NoError(t, b.tower.routable.Replace(tw.id, []fleet.Station{{
		TowerID: tw.id, StationID: "st-1", OfferID: "self-st-1", Model: "m", Modality: "text",
		Expires: time.Now().Add(time.Hour), Endpoint: deadAddr,
	}}))
	return tw.id
}

// THE FAIL: a Tower whose data plane is unreachable is caught. Repeated, it suspends.
func TestACanaryToADeadTowerFailsAndRepeatedFailuresSuspend(t *testing.T) {
	b, _ := towerTestBroker(t)
	towerID := deadHubTower(t, b)

	// Each probe fails until the evidence crosses the threshold and the Tower is suspended;
	// once suspended it takes no work, so a further canary correctly finds nothing to probe.
	suspended := false
	for i := 0; i < 10 && !suspended; i++ {
		b.RunCanary(towerID)
		got, _ := b.tower.registry.Get(towerID)
		suspended = got.State == admit.StateSuspended
	}
	require.True(t, suspended, "a Tower that fails canaries repeatedly is taken off")
}

// A Tower with nothing routable is not canaried - there is nothing to probe, which is not a
// failure. A LEAF row (non-self) is likewise not a target: nothing serves that plane anymore.
func TestACanaryWithNoTargetRecordsNothing(t *testing.T) {
	b, _ := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))
	require.Equal(t, reputation.Outcome(""), b.RunCanary(tw.id))

	attachStation(t, b, "st-1", tw.id, "owner-1")
	require.NoError(t, b.tower.routable.Replace(tw.id, []fleet.Station{{
		TowerID: tw.id, StationID: "st-1", OfferID: "of-legacy", Model: "m", Modality: "text",
		Expires: time.Now().Add(time.Hour), Endpoint: "203.0.113.9:1",
	}}))
	require.Equal(t, reputation.Outcome(""), b.RunCanary(tw.id),
		"a legacy leaf row is not probed - the plane it served is gone")
}

func TestTheCanarySweepIsSafeWithoutTheSubsystem(t *testing.T) {
	b := testBrokerWithDB(store.NewMem())
	require.NotPanics(t, func() { b.towerCanarySweepOnce() })
	require.Equal(t, reputation.Outcome(""), b.RunCanary("tw-x"))
}

// The canary sweep loop runs on its ticker and stops when asked.
func TestTheCanarySweepLoopStops(t *testing.T) {
	b, _ := towerTestBroker(t)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); b.towerCanarySweep(stop) }()
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the canary sweep did not stop")
	}
}

// The sweep on a broker whose fleet lists a failing Tower records the failure.
func TestTheSweepProbesADeadTowerAndRecordsIt(t *testing.T) {
	b, _ := towerTestBroker(t)
	towerID := deadHubTower(t, b)
	b.towerCanarySweepOnce()
	tally, err := b.tower.outcomes.Tally(towerID, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, tally.CanaryFail)
}
