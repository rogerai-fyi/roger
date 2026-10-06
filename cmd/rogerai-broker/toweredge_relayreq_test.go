package main

// toweredge_relayreq_test.go: a Tower-relayed attempt's consumer ledger row carries the request
// id it belongs to, exactly like a direct one (contract §14.B7 #13, founder ruling 2026-10-05).

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
)

func TestOpenEdgeAttemptRecordsTheRequestID(t *testing.T) {
	b, _ := towerTestBroker(t)
	cpub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	apub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	g, err := b.tower.dispatch.MintEdge(dispatch.EdgeTarget{
		TowerID: "tw-x", StationID: "st-x", StationEpoch: 1, Model: "m", Modality: "text",
		RelayName: "st-x.relay.example", MaxIn: 100, MaxOut: 100, AssertionKey: apub, ConsumerKey: cpub,
	})
	require.NoError(t, err)
	require.NoError(t, b.openEdgeAttempt(g, dispatch.Target{AssertionKey: apub}, "fedcba9876543210"))
	rec, ok, err := b.tower.dispatch.Store().Get(g.AttemptID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "fedcba9876543210", rec.RelayRequestID)
}

func TestEdgeSettleStampsTheRequestIDOnTheConsumerRow(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_IN", "0")
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_OUT", "1000000")
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "tower-op-rr")
	tw := enrolledTower(t, b, op.login)
	stPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	stationOwner := hexOf(stPub)
	require.NoError(t, b.db.BindOwner(store.Owner{
		Pubkey: stationOwner, Login: "station-op-rr", Email: "rr@x.test", EmailVerifiedAt: time.Now().Unix(),
	}))
	stationPriv := attachStation(t, b, "st-rr", tw.id, stationOwner)

	cpub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	apub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	g, err := b.tower.dispatch.MintEdge(dispatch.EdgeTarget{
		TowerID: tw.id, StationID: "st-rr", StationEpoch: 1, Model: "m", Modality: "text",
		RelayName: "st-rr.relay.example", MaxIn: 1000, MaxOut: 1000, AssertionKey: apub, ConsumerKey: cpub,
	})
	require.NoError(t, err)
	require.NoError(t, b.tower.dispatch.Store().Put(dispatch.Record{
		AttemptID: "att-rr", JobID: g.JobID, TowerID: tw.id, StationID: "st-rr",
		StationEpoch: 1, Model: "m", Modality: "text", Nonce: g.Nonce,
		Deadline: time.Now().Add(time.Hour), Grant: g.Signed, ConsumerKey: cpub,
		RelayRequestID: "00112233aabbccdd", State: dispatch.StateIssued,
	}))
	wallet := bindEdgeConsumer(t, b, cpub)
	_, err = b.db.AddCredits(wallet, 100000)
	require.NoError(t, err)
	held, err := b.db.HoldFor(wallet, "att-rr", 1000)
	require.NoError(t, err)
	require.True(t, held)

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-rr", "attempt_id": "att-rr",
		"receipt": signedReceipt(t, stationPriv, "att-rr", "st-rr", []byte("answer"), dispatch.Usage{In: 0, Out: 50}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)

	mine, err := b.db.RecentByUser(wallet, 10)
	require.NoError(t, err)
	require.Len(t, mine, 1)
	require.Equal(t, "att-rr", mine[0].RequestID)
	require.Equal(t, "00112233aabbccdd", mine[0].RelayRequestID, "the consumer's row ties the bridged attempt to its request")
}
