package main

// The adaptive audit layer: a fresh Station or an anomalous Tower history elevates the
// selection probability; clean corroborated history decays it to zero.

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/reputation"
)

func TestAdaptiveAuditProbability(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := liveEdgeTower(t, b, srv, "adapt-op", "203.0.113.7:8443")
	now := time.Now()

	// No attachment age, no history: nothing to elevate on.
	require.Zero(t, b.adaptiveAuditP(tw.id, time.Time{}, now))

	// A freshly attached station is unproven: elevated.
	node := signedInOperator(t, b, "adapt-node")
	body, _ := selfAttachBodyFor(t, b, node)
	var out struct {
		StationID string `json:"station_id"`
	}
	code, raw := node.attach(t, srv, body, &out)
	require.Equal(t, 200, code, raw)
	at, found, err := b.tower.stations.Station(out.StationID)
	require.NoError(t, err)
	require.True(t, found)
	require.InDelta(t, adaptiveNewStationP, b.adaptiveAuditP(tw.id, at.AttachedAt, now), 1e-9,
		"a brand-new station is watched at the elevated rate")
	require.Zero(t, b.adaptiveAuditP(tw.id, at.AttachedAt, now.Add(2*adaptiveNewStationWindow)),
		"the new-station elevation expires")

	// Anomalous recent history ramps the rate; corroborated history decays it.
	for i := 0; i < 6; i++ {
		b.recordOutcome(tw.id, "", attemptIDf("bad", i), reputation.Disputed)
	}
	for i := 0; i < 2; i++ {
		b.recordOutcome(tw.id, "", attemptIDf("ok", i), reputation.Corroborated)
	}
	p := b.adaptiveAuditP(tw.id, time.Time{}, now)
	require.InDelta(t, 0.75, p, 1e-9, "6 disputed of 8 recent = 75%% elevation")
	for i := 0; i < 24; i++ {
		b.recordOutcome(tw.id, "", attemptIDf("good", i), reputation.Corroborated)
	}
	require.InDelta(t, 6.0/32.0, b.adaptiveAuditP(tw.id, time.Time{}, now), 1e-9,
		"corroborated history decays the elevation")
}

func attemptIDf(prefix string, i int) string { return fmt.Sprintf("att-adapt-%s-%d", prefix, i) }
