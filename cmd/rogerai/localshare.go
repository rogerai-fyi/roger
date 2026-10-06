package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os/signal"
	"syscall"

	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/lanmode"
	"strings"
)

// isLocalTowerBroker reports whether the configured broker is a STANDALONE Tower's consumer
// plane rather than the public broker, so `roger share` pointed at a local Tower serves it
// directly. Only a plaintext-http broker at a local address is probed (the Tower's deployment
// shape), so the public https broker is never probed. The probe itself is LAN mode's one
// shared standalone-Tower check (lanmode.ProbeTower). "localhost" is how most people type the
// loopback host, so it counts; any other name is not resolved here.
func isLocalTowerBroker(broker string) bool {
	u, err := url.Parse(broker)
	if err != nil || u.Scheme != "http" {
		return false
	}
	if !strings.EqualFold(u.Hostname(), "localhost") && !lanmode.ClassifyLiteral(u.Host).Local {
		return false
	}
	return lanmode.ProbeTower(context.Background(), lanProbeHTTP, broker)
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
