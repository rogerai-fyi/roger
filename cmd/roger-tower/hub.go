package main

// hub.go mounts the Tower's DATA-PLANE HUB (Option C, Topology 2): the HTTP surface where
// consumers submit sealed jobs and this Tower's self-attached `roger share` nodes poll for
// them. The broker never touches the payload - it authorized the attempt (the grant) and will
// settle the receipt; everything between is this hub, and it is blind: it verifies only the
// grant's Core signature + metadata, and relays ciphertext it cannot read.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"rogerai.fm/roger/v5/internal/tower"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/link"
	"rogerai.fm/roger/v5/internal/towerhub"
	"rogerai.fm/roger/v5/internal/towerjoin"
)

// hubNodeRefresh is how often the hub re-fetches its node registrations from Core, picking up
// freshly self-attached nodes and dropping revoked ones.
const hubNodeRefresh = 30 * time.Second

// runHubInBackground starts the hub server and its node-registration refresher, returning a
// waiter that blocks until both have wound down. It fails fast (before serving anything) if
// Core's grant key cannot be fetched - a hub that cannot verify grants would either refuse
// everything or, worse, be tempted to skip the check.
func runHubInBackground(st *tower.State, addr string, out io.Writer, stop <-chan struct{}) (func(), error) {
	// THE GRANT CHECK CONTRACT (from the security audit): a REAL clock, so expired grants are
	// refused, and THIS tower's own ID, so a grant minted for another tower is refused here.
	coreKey, err := towerjoin.DispatchKey()
	if err != nil {
		return nil, fmt.Errorf("cannot fetch Roger Core's grant key: %w", err)
	}
	hub := towerhub.New()
	server := towerhub.NewServer(hub, func(grant []byte) (string, string, error) {
		att, station, _, gerr := dispatch.EdgeGrantMeta(grant, coreKey, link.PublicNetwork,
			st.TowerID, time.Now())
		return att, station, gerr
	}, 0, 0)
	// THE SETTLE COURIER: every completed result's receipt is forwarded to Core, tower-signed,
	// so the node is paid without holding its own line to Core. Opaque both ways; Core's
	// one-use settlement makes a duplicate forward a harmless 409.
	server.OnComplete = func(stationID string, res towerhub.Result) {
		if err := towerjoin.SettleEdgeReceipt(st, stationID, res.AttemptID, res.Receipt); err != nil {
			fmt.Fprintf(out, "hub: settle forward for %s failed: %v\n", res.AttemptID, err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathSubmit, server.Submit)
	mux.HandleFunc(towerhub.PathPoll, server.Poll)
	mux.HandleFunc(towerhub.PathComplete, server.Complete)
	httpSrv := &http.Server{
		Addr:    addr,
		Handler: mux,
		// Slow-loris bounds (the audit's mount-site contract). ReadTimeout covers the whole
		// request read - generous enough for a 16MB sealed submit on a slow uplink, small
		// enough that a trickled body cannot hold a connection all day. Poll responses are
		// long-held WRITES, which these do not bound.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
	}

	// The refresher: keep the hub's node registrations in step with Core's attachment
	// registry. RegisterNode also rotates tokens, and nodes that disappear are unregistered
	// so a revoked node's token stops polling within one refresh.
	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		known := map[string]bool{}
		refresh := func() {
			nodes, nerr := towerjoin.HubNodes(st)
			if nerr != nil {
				fmt.Fprintf(out, "hub: could not refresh node registrations: %v\n", nerr)
				return
			}
			seen := map[string]bool{}
			for _, n := range nodes {
				server.RegisterNode(n.StationID, n.HubToken)
				seen[n.StationID] = true
			}
			for id := range known {
				if !seen[id] {
					server.UnregisterNode(id)
				}
			}
			known = seen
		}
		refresh()
		t := time.NewTicker(hubNodeRefresh)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				refresh()
			case <-stop:
				return
			}
		}
	}()

	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		fmt.Fprintf(out, "hub: serving the data plane on %s\n", addr)
		if serr := httpSrv.ListenAndServe(); serr != nil && serr != http.ErrServerClosed {
			fmt.Fprintf(out, "hub: server stopped: %v\n", serr)
		}
	}()
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
	}()

	return func() {
		<-serveDone
		<-refreshDone
	}, nil
}
