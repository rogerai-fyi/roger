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
	"crypto/ed25519"
	"encoding/base64"
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
	"rogerai.fm/roger/v5/internal/edgeclient"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
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
	settleOut := make(chan map[string]any, 1)
	hubServer.OnComplete = func(stationID string, res towerhub.Result) {
		// The ack grace, as roger-tower's courier holds it: the consumer's acknowledgement
		// gets a head start on the receipt, so the settlement it corroborates is corroborated.
		// The test waits for the ack to actually be RECORDED (not a fixed sleep - the ack
		// races this goroutine from the very same /complete, and a loaded box loses races).
		for waited := 0; waited < 8000; waited += 25 {
			if _, found, _ := b.tower.acks.Get(res.AttemptID); found {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		body := map[string]any{
			"tower_id": tw.id, "station_id": stationID, "attempt_id": res.AttemptID,
			"receipt": base64.StdEncoding.EncodeToString(res.Receipt),
		}
		var out map[string]any
		tw.call(t, srv, "/tower/edge/settle", jsonOf(t, body), &out) // 200 or 409, both fine
		settleOut <- out
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

	// THE CONSUMER: the first-party edgeclient, end to end - authorize (Core pins the node's
	// listed price and hands back the station's session key), seal, submit to the TOWER, open,
	// and acknowledge. This is the P5e client path, dogfooded.
	consumer := signedInConsumer(t, b)
	consWallet, ok := b.edgeConsumerWallet(consumer.Public().(ed25519.PublicKey))
	require.True(t, ok, "the consumer's account wallet resolves")
	balBefore, err := b.db.BalanceOf(consWallet, 0)
	require.NoError(t, err)
	ec := &edgeclient.Client{Broker: srv.URL, Key: consumer}
	sctx, sc := context.WithTimeout(context.Background(), 20*time.Second)
	defer sc()
	auth, err := ec.AuthorizeSealed(sctx, "my-model")
	require.NoError(t, err)
	require.EqualValues(t, 300_000, auth.PriceOutMicros, "the grant pins the node's own listed price")

	plaintext := []byte(`{"model":"my-model","messages":[{"role":"user","content":"what is the answer"}]}`)
	res, err := ec.DoSealed(sctx, &auth, plaintext)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.Status)
	require.Equal(t, []byte(modelBody), res.Body, "the consumer gets the model's own bytes, unreadable to the tower in transit")
	require.NoError(t, ec.AckSealed(sctx, &auth, res), "the acknowledgement lands")

	// The courier's ack grace means the acknowledgement beat the receipt to Core: this
	// settlement is CORROBORATED - the consumer's own signed account of the bytes agrees
	// with the node's, and the tower's judged rate reflects it.
	select {
	case out := <-settleOut:
		require.Equal(t, true, out["corroborated"], "the ack beat the receipt: corroborated, not merely funded (%v)", out)
	case <-time.After(15 * time.Second):
		t.Fatal("the settle courier never reported")
	}

	// THE MONEY: the courier settles, and the split lands - 500k tokens x $0.30/1M = 0.15
	// credits: node owner 0.105 (70%), tower operator 0.015 (10%), consumer debited 0.15.
	require.Eventually(t, func() bool {
		s, _ := b.db.EarningSplitOf(nodeAcct, time.Now().Add(time.Hour))
		return s.Payable > 0.104 && s.Payable < 0.106
	}, 10*time.Second, 100*time.Millisecond, "the node owner banks 70%% of their listed price")
	sTw, _ := b.db.EarningSplitOf(towerAcct, time.Now().Add(time.Hour))
	require.InDelta(t, 0.015, sTw.Payable, 1e-9, "the tower operator banks 10%% of gross")
	// And the consumer paid EXACTLY tokens x price - the hold's excess was released, not kept.
	balAfter, err := b.db.BalanceOf(consWallet, 0)
	require.NoError(t, err)
	require.InDelta(t, 0.15, balBefore-balAfter, 1e-9, "the consumer is debited exactly 500k x $0.30/1M")
	require.NotEmpty(t, stationID)
}

// The FAILURE path: the node is attached and routable but its serving loop is DOWN. The
// consumer's submit fails cleanly, nothing settles, nobody earns - and the pre-auth hold is
// reclaimed by the backstop sweep, so the consumer is made whole to the credit.
func TestTopology2NodeDownTheConsumerIsMadeWhole(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_IN", "0")
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_OUT", "0")
	b, srv := towerTestBroker(t)
	b.feeRate = 0.30

	// A real hub with NO registered node: the station self-attached, then its loop died.
	hubLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = hubLn.Close() })
	endpoint := hubLn.Addr().String()
	tw := liveEdgeTower(t, b, srv, "tower-op-down", endpoint)
	towerAcct := ownerPubkeyOf(t, b, "tower-op-down")
	hub := towerhub.New()
	hubServer := towerhub.NewServer(hub, func(grant []byte) (string, string, error) {
		att, station, _, gerr := dispatch.EdgeGrantMeta(grant, b.tower.dispatchPub, link.PublicNetwork, tw.id, time.Now())
		return att, station, gerr
	}, 2*time.Second, 500*time.Millisecond)
	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathSubmit, hubServer.Submit)
	mux.HandleFunc(towerhub.PathPoll, hubServer.Poll)
	mux.HandleFunc(towerhub.PathComplete, hubServer.Complete)
	go func() { _ = http.Serve(hubLn, mux) }()

	// The node self-attaches at its price (the attach IS the offer) - but never polls.
	nodeOp := signedInOperator(t, b, "node-op-down")
	nodeAcct := ownerPubkeyOf(t, b, "node-op-down")
	body, _ := selfAttachBody(t)
	body["model"], body["modality"], body["price_out_micros"] = "down-model", "chat", 300_000
	var attached map[string]any
	code, raw := nodeOp.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &attached)
	require.Equal(t, http.StatusOK, code, raw)

	consumer := signedInConsumer(t, b)
	consWallet, ok := b.edgeConsumerWallet(consumer.Public().(ed25519.PublicKey))
	require.True(t, ok)
	balBefore, err := b.db.BalanceOf(consWallet, 0)
	require.NoError(t, err)

	ec := &edgeclient.Client{Broker: srv.URL, Key: consumer}
	sctx, sc := context.WithTimeout(context.Background(), 15*time.Second)
	defer sc()
	auth, err := ec.AuthorizeSealed(sctx, "down-model")
	require.NoError(t, err)
	balHeld, err := b.db.BalanceOf(consWallet, 0)
	require.NoError(t, err)
	require.Less(t, balHeld, balBefore, "authorize placed a pre-auth hold")

	// The submit fails cleanly - no node is polling this Station.
	_, err = ec.DoSealed(sctx, &auth, []byte(`{"model":"down-model","messages":[]}`))
	require.Error(t, err, "with the node down, the consumer hears a clean refusal, not silence")

	// Nothing settled, so nobody earned a cent of the consumer's money...
	sN, _ := b.db.EarningSplitOf(nodeAcct, time.Now().Add(time.Hour))
	require.Zero(t, sN.Payable, "a node that served nothing earns nothing")
	sTw, _ := b.db.EarningSplitOf(towerAcct, time.Now().Add(time.Hour))
	require.Zero(t, sTw.Payable, "a tower that relayed nothing earns nothing")

	// ...and the backstop sweep reclaims the orphaned hold: the consumer is made whole.
	b.releaseStaleHoldsSweepOnce(time.Now().Add(time.Hour))
	balAfter, err := b.db.BalanceOf(consWallet, 0)
	require.NoError(t, err)
	require.InDelta(t, balBefore, balAfter, 1e-9, "the hold is released in full - no charge without service")
}
