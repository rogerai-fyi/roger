package main

import (
	"context"
	"fmt"
	"io"
	"os/signal"
	"strings"
	"syscall"

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
	fmt.Fprintf(out, "LAN mode: sharing %s free on the LAN only; nothing is registered with the network\n", model)
	towers := lanTowerBases(cfg)
	if cfg.lan.EdgeOnly {
		fmt.Fprintf(out, "%s: serving Edge members only\n", edgeOnlyNote)
	} else if len(towers) == 0 {
		fmt.Fprintln(out, "no LAN Tower is configured: serving this Edge's members only (add a Tower with roger lan add <address>)")
	}
	shareToTowers(run, towers, out)
	fmt.Fprintln(out, "stopped sharing on the LAN.")
	return nil
}

// shareToTowers serves this machine's model to standalone Towers through agent.StartLAN, the one
// standalone share loop (features/packaging/one_binary.feature section 7.3), until the operator
// stops it. A single Tower is the same loop with one Tower.
func shareToTowers(run agent.Config, towers []string, out io.Writer) {
	ctx, stop := lanShareContext()
	defer stop()
	s := agent.StartLAN(run, towers, agent.NodeKey(), out)
	<-ctx.Done()
	s.Stop()
	s.Wait()
}

// serveBrokerTower is `roger share` against a broker setting that is a standalone Tower. A Tower
// in lan_towers with no pin yet (a station attached before one-invite joins) is pinned to the key
// it shows on this first TLS contact, once, logged, before the first poll
// (features/join/migration.feature); an older plain-http Tower is served as before.
func serveBrokerTower(cfg config, run agent.Config, base string, out io.Writer) {
	lanFirstUsePins(&cfg)
	shareToTowers(run, []string{base}, out)
}

// brokerTower reports the broker setting's consumer-plane base when it is a standalone Tower
// (lanmode.IsTower, the one Tower check): `roger share` and the TUI and console SHARE serve it.
func brokerTower(cfg config) (string, bool) {
	if !lanmode.IsTower(context.Background(), lanProbeHTTP, cfg.Broker) {
		return "", false
	}
	a, err := lanmode.ParseAddr(cfg.Broker)
	if err != nil {
		return "", false
	}
	return a.Base(), true
}

// foldBrokerTower copies a broker setting that is a standalone Tower into lan_towers, once per
// broker value (features/packaging/one_binary.feature section 7.2, ruled 2026-10-07), so LAN
// Tower discovery reads one list. The broker setting itself is left as it is.
func foldBrokerTower(cfg config, note io.Writer) config {
	if cfg.lan.EdgeOnly || cfg.Broker == "" || cfg.Broker == cfg.LanBrokerChecked {
		return cfg
	}
	cfg.LanBrokerChecked = cfg.Broker // remembered for this process; written only with a fold
	if base, ok := brokerTower(cfg); ok {
		if a, err := lanmode.ParseAddr(base); err == nil && !contains(cfg.LanTowers, a.String()) && !contains(cfg.LanNotTower, a.String()) {
			cfg.LanTowers = append(cfg.LanTowers, a.String())
			fmt.Fprintf(note, "added %s to your LAN Towers (from your broker setting)\n", a)
			// Only a real fold changes the file: a command that finds no Tower behind the
			// broker setting leaves config.json exactly as it was (routing_flags.feature).
			_ = saveConfig(cfg)
		}
	}
	return cfg
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
