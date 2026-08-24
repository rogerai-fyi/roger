package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/signal"
	"syscall"
	"time"

	"rogerai.fm/roger/v6/internal/agent"
	"strings"
)

// isLocalTowerBroker reports whether the configured broker is a STANDALONE Tower's consumer
// plane rather than the public broker - so `roger share` pointed at a local Tower can serve it
// directly instead of trying to register on the public network.
//
// The tell is /local/poll: a standalone Tower answers it (401 unsigned, or 200/204 when signed),
// while the public broker has no such route and 404s. The probe runs only against a plaintext-
// http broker on a loopback or private-LAN address - the Tower's deployment shape - so the
// public https broker is never probed and this stays a fast, local decision.
func isLocalTowerBroker(broker string) bool {
	u, err := url.Parse(broker)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	// "localhost" is how most people type the loopback host; treat it as loopback so a Tower at
	// http://localhost:8787 is detected the same as http://127.0.0.1:8787. Any OTHER hostname is
	// not resolved (that would be a DNS lookup) - point roger at a literal IP for those.
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || (!ip.IsLoopback() && !ip.IsPrivate()) {
			return false
		}
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Post(broker+"/local/poll", "application/json", nil)
	if err != nil {
		return false // cannot reach it as a Tower; fall back to the ordinary broker path
	}
	defer resp.Body.Close()
	// A standalone Tower answers an unsigned /local/poll with its UNIFORM 401 -
	// {"error":"unauthorized"} - a station route with no valid signature. Matching that exact
	// response (not just "not a 404") is what tells a Tower apart from a public broker (which has
	// no such route) AND from an ordinary local test/dev broker that catch-alls a 200. A false
	// positive would route `roger share` into a poll loop against something that is not a Tower.
	if resp.StatusCode != http.StatusUnauthorized {
		return false
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	return strings.Contains(string(body), `"unauthorized"`)
}

// serveLocalTowerShare serves a standalone Tower's stations by polling it, until the operator
// stops with Ctrl-C. It is the `roger share` path for a local Tower: no registration, no relay
// fabric, no on-air lock - the node connects in and serves the local network for free.
func serveLocalTowerShare(cfg agent.Config, out io.Writer) error {
	fmt.Fprintf(out, "this broker is a standalone Tower - serving its local network directly (free, no login).\n")
	if cfg.Upstream == "" {
		return fmt.Errorf("no local model found to serve: pass --upstream http://127.0.0.1:PORT/v1/chat/completions")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	err := agent.ServeLocalTower(ctx, cfg, agent.NodeKey(), out)
	if ctx.Err() != nil {
		fmt.Fprintln(out, "\nstopped serving the local network.")
		return nil
	}
	return err
}
