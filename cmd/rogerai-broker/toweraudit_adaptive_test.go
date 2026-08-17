package main

// The adaptive audit layer: a fresh Station or an anomalous Tower history elevates the
// selection probability; clean corroborated history decays it to zero.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/reputation"
)

func TestAdaptiveAuditProbability(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := liveEdgeTower(t, b, srv, "adapt-op", "203.0.113.7:8443")
	now := time.Now()

	// An unknown station on a tower with no history: nothing to elevate on.
	require.Zero(t, b.adaptiveAuditP(tw.id, "st-unknown", now))

	// A freshly attached station is unproven: elevated.
	node := signedInOperator(t, b, "adapt-node")
	body, _ := selfAttachBody(t)
	var out struct {
		StationID string `json:"station_id"`
	}
	code, raw := node.call(t, srv, "POST", "/tower/edge/attach", body, &out)
	require.Equal(t, 200, code, raw)
	require.InDelta(t, adaptiveNewStationP, b.adaptiveAuditP(tw.id, out.StationID, now), 1e-9,
		"a brand-new station is watched at the elevated rate")
	require.Zero(t, b.adaptiveAuditP(tw.id, out.StationID, now.Add(2*adaptiveNewStationWindow)),
		"the new-station elevation expires")

	// Anomalous recent history ramps the rate; corroborated history decays it.
	for i := 0; i < 6; i++ {
		b.recordOutcome(tw.id, attemptIDf("bad-%d", i), reputation.Disputed)
	}
	for i := 0; i < 2; i++ {
		b.recordOutcome(tw.id, attemptIDf("ok-%d", i), reputation.Corroborated)
	}
	p := b.adaptiveAuditP(tw.id, "st-unknown", now)
	require.InDelta(t, 0.75, p, 1e-9, "6 disputed of 8 recent = 75%% elevation")
	for i := 0; i < 24; i++ {
		b.recordOutcome(tw.id, attemptIDf("good-%d", i), reputation.Corroborated)
	}
	require.InDelta(t, 6.0/32.0, b.adaptiveAuditP(tw.id, "st-unknown", now), 1e-9,
		"corroborated history decays the elevation")
}

func attemptIDf(format string, i int) string { return "att-adapt-" + format[:3] + string(rune('a'+i)) }
