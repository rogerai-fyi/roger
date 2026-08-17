package main

// towerauditleniency_test.go pins the self-retiring exemption: a hub node is spared a
// quarantine-grade "cannot produce" only until it has PROVEN it can produce one. No flag day,
// no version sniffing, no claim the node makes about itself - behaviour decides.

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/audit"
)

func TestLeniencyEndsTheFirstTimeAStationAnswersAnAudit(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "len-op", "203.0.113.4:8444") // already active

	node := signedInOperator(t, b, "len-node")
	body, _ := selfAttachBody(t)
	var attached struct {
		StationID string `json:"station_id"`
	}
	code, raw := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &attached)
	require.Equal(t, http.StatusOK, code, raw)
	station := attached.StationID

	// A hub node that has never answered: lenient, so a miss is soft.
	require.True(t, b.auditLenientStation(station), "a node that has never answered is spared")

	// It answers one audit with a real, verifying transcript.
	at, found, err := b.tower.stations.Station(station)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, at.AuditProvenAt.IsZero())

	b.markAuditProven(station)

	require.False(t, b.auditLenientStation(station),
		"having proven it can produce one, it is held to the standard like everyone else")
	at, _, _ = b.tower.stations.Station(station)
	require.False(t, at.AuditProvenAt.IsZero(), "and the proof is recorded, not recomputed")

	// Idempotent: the FIRST answer is the proof, so a later one does not re-stamp it.
	firstAt := at.AuditProvenAt
	time.Sleep(2 * time.Millisecond)
	b.markAuditProven(station)
	at, _, _ = b.tower.stations.Station(station)
	require.Equal(t, firstAt, at.AuditProvenAt)
}

// A PROVEN station's sampled miss is a real finding - the exemption is genuinely gone, not
// merely renamed.
func TestAProvenStationsMissIsAHardFinding(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := liveEdgeTower(t, b, srv, "len-op2", "203.0.113.6:8444") // already active
	node := signedInOperator(t, b, "len-node2")
	body, _ := selfAttachBody(t)
	var attached struct {
		StationID string `json:"station_id"`
	}
	code, raw := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &attached)
	require.Equal(t, http.StatusOK, code, raw)
	b.markAuditProven(attached.StationID)

	// A SAMPLED attempt it then cannot produce.
	require.True(t, auditSampled("att-s0"))
	require.NoError(t, b.tower.auditWanted.Want(audit.Wanted{
		TowerID: tw.id, AttemptID: "att-s0", StationID: attached.StationID,
		RequestDigest: "rq", ResponseDigest: "rs", Deadline: time.Now().Add(time.Hour),
	}))
	payload, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "attempt_id": "att-s0", "available": false,
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ = tw.call(t, srv, "/tower/audit/transcript", payload, &out)
	require.Equal(t, http.StatusOK, code)

	tally, err := b.tower.outcomes.Tally(tw.id, time.Time{})
	require.NoError(t, err)
	require.Equal(t, 1, tally.AuditMismatch,
		"a station that proved it can answer and then cannot is a real finding")
}
