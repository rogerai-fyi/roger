package main

// topology2_e2e_test.go is THE PRODUCT LOOP, end to end with every real component:
//
//	a `roger share` node (agent.ServeTower: self-attach at ITS OWN price, blind-serve)
//	a tower hub (mounted exactly as roger-tower mounts it: real grant check, settle courier)
//	a consumer (authorize at Core, seal to the node, submit to the TOWER, open the answer)
//	Roger Core (this broker: authorize, hold, settle, 70/10/20 into real wallets)
//
// The broker never touches the payload; the tower carries only ciphertext; and at the end the
// node's owner holds 70% of its listed price, the tower operator 10%, with the consumer
// debited exactly tokens x price. This is the founder's sentence - "providers set their own
// per-token prices, the tower relays, 70/10/20" - as one passing test.

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/agent"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/envelope"
	"rogerai.fm/roger/v5/internal/towercore/link"
	"rogerai.fm/roger/v5/internal/towerhub"
)

func TestFullProductLoopANodeEarnsThroughATower(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_IN", "0")
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_OUT", "0") // byte tariff OFF: only listed prices can bill
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	b, srv := towerTestBroker(t)
	b.feeRate = 0.30

	// THE MODEL: an OpenAI-compatible upstream reporting big token usage (the response body is
	// padded past the token count, since tokens<=bytes is a hard clamp).
	modelBody := fmt.Sprintf(`{"choices":[{"message":{"content":"the answer"}}],"usage":{"prompt_tokens":0,"completion_tokens":500000},"pad":%q}`,
		strings.Repeat("x", 600_000))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(modelBody))
	}))
	t.Cleanup(upstream.Close)

	// THE TOWER: enrolled + live, advertising the hub listener's address as its data plane.
	hubLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = hubLn.Close() })
	endpoint := hubLn.Addr().String()
	tw := liveEdgeTower(t, b, srv, "tower-op", endpoint)
	towerAcct := ownerPubkeyOf(t, b, "tower-op")

	// The hub, mounted as roger-tower mounts it: real EdgeGrantMeta (Core's key, THIS tower's
	// id, a real clock) + the settle courier forwarding receipts to Core, tower-signed.
	hub := towerhub.New()
	hubServer := towerhub.NewServer(hub, func(grant []byte) (string, string, error) {
		att, station, _, gerr := dispatch.EdgeGrantMeta(grant, b.tower.dispatchPub, link.PublicNetwork, tw.id, time.Now())
		return att, station, gerr
	}, 10*time.Second, 500*time.Millisecond)
	hubServer.OnComplete = func(stationID string, res towerhub.Result) {
		body := map[string]any{
			"tower_id": tw.id, "station_id": stationID, "attempt_id": res.AttemptID,
			"receipt": base64.StdEncoding.EncodeToString(res.Receipt),
		}
		var out map[string]any
		tw.call(t, srv, "/tower/edge/settle", jsonOf(t, body), &out) // 200 or 409, both fine
	}
	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathSubmit, hubServer.Submit)
	mux.HandleFunc(towerhub.PathPoll, hubServer.Poll)
	mux.HandleFunc(towerhub.PathComplete, hubServer.Complete)
	go func() { _ = http.Serve(hubLn, mux) }()

	// THE NODE: a roger share operator lists their model at THEIR price ($0.30/1M out) and
	// serves through the tower - the whole agent path, self-attach included.
	nodeOp := signedInOperator(t, b, "node-op")
	nodeAcct := ownerPubkeyOf(t, b, "node-op")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = agent.ServeTower(ctx, agent.Config{
			Broker: srv.URL, Model: "my-model", Modality: "chat",
			PriceIn: 0, PriceOut: 0.30, Upstream: upstream.URL, Parallel: 1,
		}, nodeOp.priv, t.TempDir(), io.Discard)
	}()

	// The hub's node-registration refresher, as roger-tower runs it: fetch this tower's nodes
	// from Core and register them. Loop until the self-attach has landed.
	var stationID string
	require.Eventually(t, func() bool {
		ats, aerr := b.tower.stations.ByTower(tw.id)
		if aerr != nil || len(ats) == 0 {
			return false
		}
		stationID = ats[0].StationID
		hubServer.RegisterNode(ats[0].StationID, ats[0].HubToken)
		return true
	}, 10*time.Second, 50*time.Millisecond, "the node self-attaches and the hub registers it")

	// THE CONSUMER: authorize at Core with a sealing key; Core pins the node's listed price
	// into the grant and hands back the station's session key.
	consumer := signedInConsumer(t, b)
	consEnvPub, consEnvPriv, err := envelope.NewKey()
	require.NoError(t, err)
	code, auth := consumerCall(t, srv, consumer, "/tower/edge/authorize", map[string]any{
		"model": "my-model", "consumer_env_key": hex.EncodeToString(consEnvPub),
	})
	require.Equal(t, http.StatusOK, code, auth)
	require.EqualValues(t, 300_000, auth["price_out_micros"], "the grant pins the node's own listed price")
	attemptID, _ := auth["attempt_id"].(string)
	grantRaw, err := base64.StdEncoding.DecodeString(auth["grant"].(string))
	require.NoError(t, err)
	sessionKey, err := hex.DecodeString(auth["station_session_key"].(string))
	require.NoError(t, err)

	// Seal the request to the NODE (Core handed us its key; the tower never chooses it),
	// submit to the TOWER, and open the sealed answer.
	plaintext := []byte(`{"model":"my-model","messages":[{"role":"user","content":"what is the answer"}]}`)
	sealedReq, err := envelope.SealTo(sessionKey, plaintext, attemptID)
	require.NoError(t, err)
	sealedRaw, err := sealedReq.Marshal()
	require.NoError(t, err)

	hubClient := &towerhub.Client{BaseURL: "http://" + endpoint, HTTP: &http.Client{Timeout: 15 * time.Second}}
	sctx, sc := context.WithTimeout(context.Background(), 15*time.Second)
	defer sc()
	res, err := hubClient.SubmitJob(sctx, grantRaw, sealedRaw)
	require.NoError(t, err)
	require.Empty(t, res.Failure)
	sealedRes, err := envelope.Parse(res.Envelope)
	require.NoError(t, err)
	answer, err := envelope.OpenWith(consEnvPriv, sealedRes, attemptID)
	require.NoError(t, err)
	require.Equal(t, []byte(modelBody), answer, "the consumer gets the model's own bytes, unreadable to the tower in transit")

	// THE MONEY: the courier settles, and the split lands - 500k tokens x $0.30/1M = 0.15
	// credits: node owner 0.105 (70%), tower operator 0.015 (10%), consumer debited 0.15.
	require.Eventually(t, func() bool {
		s, _ := b.db.EarningSplitOf(nodeAcct, time.Now().Add(time.Hour))
		return s.Payable > 0.104 && s.Payable < 0.106
	}, 10*time.Second, 100*time.Millisecond, "the node owner banks 70%% of their listed price")
	sTw, _ := b.db.EarningSplitOf(towerAcct, time.Now().Add(time.Hour))
	require.InDelta(t, 0.015, sTw.Payable, 1e-9, "the tower operator banks 10%% of gross")
	require.NotEmpty(t, stationID)
}
