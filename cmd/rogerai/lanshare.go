package main

import (
	"context"
	"fmt"
	"io"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/lanmode"
	"rogerai.fm/roger/v6/internal/onair"
)

// lanShareContext is how long a LAN share runs: until the operator stops it (a test seam).
var lanShareContext = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

// lanShareRefusal is the reason a `roger share` flag cannot apply in LAN mode, checked before the
// flags are parsed so `--price` (which only exists as --price-in/--price-out) is refused with the
// LAN reason, not a generic unknown-flag error ("" when the share may go ahead).
func lanShareRefusal(args []string) string {
	for _, a := range args {
		name := strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-")
		if !strings.HasPrefix(a, "-") {
			continue
		}
		switch {
		case name == "private":
			return "private bands are a network feature and LAN mode is on: share without --private, or run without --lan"
		case name == "price" || name == "price-in" || name == "price-out":
			return "LAN sharing is free and has no price: drop the price flag, or run without --lan to earn on the network"
		}
	}
	return ""
}

// shareLAN serves this machine's model on the LAN only (features/lan/share_in_lan_mode.feature):
// every LAN Tower it is configured with, polled as a standalone station, and the Edge through
// the local share record. It never registers with the broker, never heartbeats it and never joins
// the relay fabric. A share of the same node id already on the market refuses it: one registrant
// per node id.
func shareLAN(cfg config, run agent.Config, station, model string, out io.Writer) error {
	release, err := onair.Acquire(run.NodeID, station, model)
	if err != nil {
		return fmt.Errorf("LAN mode: %v - stop that share of %s before sharing it on the LAN", err, model)
	}
	defer release()
	ctx, stop := lanShareContext()
	defer stop()
	fmt.Fprintf(out, "LAN mode: sharing %s free on the LAN only; nothing is registered with the network\n", model)
	towers := lanTowerBases(cfg)
	if len(towers) == 0 {
		fmt.Fprintln(out, "no LAN Tower is configured: serving this Edge's members only (add a Tower with roger lan add <address>)")
	}
	var wg sync.WaitGroup
	for _, base := range towers {
		c := run
		c.Broker = base
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = agent.ServeLocalTower(ctx, c, agent.NodeKey(), out)
		}()
	}
	<-ctx.Done()
	wg.Wait()
	fmt.Fprintln(out, "stopped sharing on the LAN.")
	return nil
}

// lanTowerBases is the LAN Towers' consumer-plane base URLs, for SHARE in LAN mode.
func lanTowerBases(cfg config) []string {
	var out []string
	for _, t := range lanTowerAddrs(cfg) {
		if a, err := lanmode.ParseAddr(t); err == nil {
			out = append(out, a.Base())
		}
	}
	return out
}

// lanTowerBasesCached is lanTowerBases behind a short cache: the TUI renders SHARE every frame and
// the list can probe the network (a local broker setting is checked for a Tower), so a render
// reuses the last list for ttl rather than probing again.
func lanTowerBasesCached(cfg config, ttl time.Duration) func() []string {
	var mu sync.Mutex
	var at time.Time
	var last []string
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		if at.IsZero() || time.Since(at) > ttl {
			last, at = lanTowerBases(cfg), time.Now()
		}
		return last
	}
}
