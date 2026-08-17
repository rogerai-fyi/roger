package main

// The canary, ported to the hub path (the P9 recon's blocking item): Core probes a
// self-attached node with a SEALED submit exactly as a real consumer would. A serving node
// passes; a dark one fails; and the raw-TLS dial is never pointed at a hub node again.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/agent"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/link"
	"rogerai.fm/roger/v5/internal/towercore/reputation"
	"rogerai.fm/roger/v5/internal/towerhub"
)

func TestSealedCanaryJudgesAHubNode(t *testing.T) {
	b, srv := towerTestBroker(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"pong"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(upstream.Close)

	hubLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = hubLn.Close() })
	endpoint := hubLn.Addr().String()
	tw := liveEdgeTower(t, b, srv, "canary-tower-op", endpoint)

	hub := towerhub.New()
	hubServer := towerhub.NewServer(hub, func(grant []byte) (string, string, error) {
		att, station, _, gerr := dispatch.EdgeGrantMeta(grant, b.tower.dispatchPub, link.PublicNetwork, tw.id, time.Now())
		return att, station, gerr
	}, 10*time.Second, 500*time.Millisecond)
	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathSubmit, hubServer.Submit)
	mux.HandleFunc(towerhub.PathPoll, hubServer.Poll)
	mux.HandleFunc(towerhub.PathComplete, hubServer.Complete)
	go func() { _ = http.Serve(hubLn, mux) }()

	nodeOp := signedInOperator(t, b, "canary-node-op")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = agent.ServeTower(ctx, agent.Config{
			Broker: srv.URL, Model: "canary-model", Modality: "chat",
			PriceIn: 0, PriceOut: 0, Upstream: upstream.URL, Parallel: 1,
		}, nodeOp.priv, t.TempDir(), io.Discard)
	}()
	var stationID string
	require.Eventually(t, func() bool {
		ats, aerr := b.tower.stations.ByTower(tw.id)
		if aerr != nil || len(ats) == 0 {
			return false
		}
		stationID = ats[0].StationID
		hubServer.RegisterNode(ats[0].StationID, ats[0].HubToken)
		return true
	}, 10*time.Second, 50*time.Millisecond)

	// The target is the SELF- row, and the probe rides the sealed path: a serving node passes.
	_, _, sealedPath, ok := b.canaryTargetFor(tw.id)
	require.True(t, ok, "the hub node is a canary target")
	require.True(t, sealedPath, "a self-attached row is probed sealed, never raw-TLS")
	require.Equal(t, reputation.CanaryPass, b.RunCanary(tw.id), "a healthy hub node passes")

	// The node goes dark: the same probe fails - the 'serving nothing' finding.
	hubServer.UnregisterNode(stationID)
	require.Equal(t, reputation.CanaryFail, b.RunCanary(tw.id), "a dark hub node fails")
}
