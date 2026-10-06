package main

// LAN mode in the CLI (features/lan/launch_and_config.feature, discovery_and_dial.feature):
// the global --lan/--no-lan flags, the one mode decision per process, `roger lan add|rm|list`
// and `roger status`. The rules themselves live in internal/lanmode; this file only wires them.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/lanmode"
)

// Test seams: where the one-line LAN note goes, how names resolve, and the probe client.
var (
	lanNoteOut   io.Writer        = os.Stderr
	lanResolve   lanmode.Resolver = lanmode.DefaultResolver
	lanProbeHTTP                  = &http.Client{Timeout: 3 * time.Second}
)

// stripLANFlags removes --lan and --no-lan from anywhere in argv, so the flag works before or
// after the subcommand.
func stripLANFlags(argv []string) (rest []string, lan, noLAN bool) {
	for _, a := range argv {
		switch a {
		case "--lan":
			lan = true
		case "--no-lan":
			noLAN = true
		default:
			rest = append(rest, a)
		}
	}
	return rest, lan, noLAN
}

// decideLAN strips the flags and decides this process's mode once.
func decideLAN(argv []string, cfg config) ([]string, config, error) {
	rest, lan, noLAN := stripLANFlags(argv)
	env, envSet := os.LookupEnv("ROGER_LAN")
	m, err := lanmode.Decide(lan, noLAN, env, envSet, cfg.Mode)
	if err != nil {
		return rest, cfg, err
	}
	cfg.lan = m
	return rest, cfg, nil
}

// lanOffHint says how to turn LAN mode off, given where it came from.
func lanOffHint(m lanmode.Mode) string {
	switch m.Source {
	case lanmode.SrcConfig:
		return "turn it off with `roger config unset mode`"
	case lanmode.SrcEnv:
		return "turn it off by unsetting ROGER_LAN"
	default:
		return "drop --lan to turn it off"
	}
}

// noteLAN prints the one-line LAN note to stderr, so stdout stays the command's own output.
func noteLAN(cfg config, cmd string) {
	if !cfg.lan.LAN {
		return
	}
	switch cmd {
	case "status", "lan", "config", "version", "help", "-h", "--help":
		return // these say the mode themselves, or are not about bands
	}
	fmt.Fprintf(lanNoteOut, "LAN mode (from %s): only bands on this machine, your Edge and LAN Towers; %s\n", cfg.lan.Source, lanOffHint(cfg.lan))
}

// cmdStatus is `roger status [--json]`: the mode, where it came from, and the local sources.
func cmdStatus(cfg config, args []string) error {
	mode := "default"
	if cfg.lan.LAN {
		mode = "lan"
	}
	src := cfg.lan.Source
	if src == "" {
		src = lanmode.SrcDefault
	}
	if len(args) > 0 && args[0] == "--json" {
		b, _ := json.MarshalIndent(map[string]any{
			"mode": mode, "mode_source": src, "broker": cfg.Broker,
			"lan_towers": nonNilStrings(cfg.LanTowers), "edge_prefer": edgePreferOf(cfg),
		}, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if cfg.lan.LAN {
		fmt.Printf("mode: LAN (from %s)\n", src)
		fmt.Printf("local sources: this machine, Edge members, Towers (%d configured)\n", len(cfg.LanTowers))
		fmt.Printf("edge preference: local-only (from LAN mode; your setting is %s)\n", edgePreferOf(cfg))
		fmt.Println(lanOffHint(cfg.lan))
	} else {
		fmt.Printf("mode: default (from %s)\n", src)
		fmt.Printf("edge preference: %s\n", edgePreferOf(cfg))
	}
	fmt.Printf("broker: %s\n", cfg.Broker)
	return nil
}

func edgePreferOf(cfg config) string {
	if cfg.EdgePrefer == "" {
		return "local"
	}
	return cfg.EdgePrefer
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

const lanUsage = "usage: roger lan add <address> [--offline] | roger lan rm <address> | roger lan list"

// cmdLAN is `roger lan add|rm|list`: the standalone Towers LAN mode uses.
func cmdLAN(cfg config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", lanUsage)
	}
	switch args[0] {
	case "add":
		return lanAdd(args[1:])
	case "rm", "remove":
		return lanRemove(args[1:])
	case "list", "ls":
		return lanList()
	}
	return fmt.Errorf("unknown lan command %q\n%s", args[0], lanUsage)
}

func lanAdd(args []string) error {
	var addr string
	offline := false
	for _, a := range args {
		if a == "--offline" {
			offline = true
		} else if addr == "" {
			addr = a
		}
	}
	a, err := lanmode.ParseAddr(addr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v := lanmode.Classify(ctx, a, lanResolve)
	if !v.Local && !(v.CGNAT && a.Literal()) {
		return fmt.Errorf("%s is not added: %s. LAN mode only uses local addresses (%s)", a, v.Reason, lanmode.LocalRanges)
	}
	c := loadConfig()
	for _, have := range c.LanTowers {
		if have == a.String() {
			fmt.Printf("%s is already in the list\n", a)
			return nil
		}
	}
	switch lanmode.Probe(ctx, lanProbeHTTP, a.Base()) {
	case lanmode.ProbeTowerYes:
		fmt.Printf("added %s: it answered as a standalone Tower\n", a)
	case lanmode.ProbeNotTower:
		return fmt.Errorf("%s answers, but it is not a standalone Tower (no standalone answer at /local/poll); not added", a)
	default:
		if !offline {
			return fmt.Errorf("%s did not answer; add it anyway with `roger lan add %s --offline`", a, a)
		}
		fmt.Printf("added %s: it did not answer and will be tried at each refresh\n", a)
	}
	if v.CGNAT {
		fmt.Printf("note: %s is in the carrier/overlay range (100.64.0.0/10); LAN mode uses it only because you added it by hand\n", a.Host)
	}
	c.LanTowers = append(c.LanTowers, a.String())
	return saveConfig(c)
}

func lanRemove(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", lanUsage)
	}
	want := args[0]
	if a, err := lanmode.ParseAddr(want); err == nil {
		want = a.String()
	}
	c := loadConfig()
	kept := c.LanTowers[:0:0]
	found := false
	for _, have := range c.LanTowers {
		if have == want || have == args[0] {
			found = true
			continue
		}
		kept = append(kept, have)
	}
	if !found {
		fmt.Printf("%s is not in the list\n", args[0])
		return nil
	}
	c.LanTowers = kept
	if err := saveConfig(c); err != nil {
		return err
	}
	fmt.Printf("removed %s\n", want)
	return nil
}

func lanList() error {
	c := loadConfig()
	if len(c.LanTowers) == 0 {
		fmt.Println("no LAN Towers: add one with `roger lan add <address>`")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, status := client.LANDiscover(ctx, client.LANInputs{Towers: c.LanTowers, HTTP: lanProbeHTTP, Resolve: lanResolve})
	for i, entry := range c.LanTowers {
		fmt.Println(lanListLine(ctx, entry, status[i]))
	}
	return nil
}

// lanListLine is one `roger lan list` row: the address, whether it answered, and its bands.
func lanListLine(ctx context.Context, entry string, st client.LANTowerStatus) string {
	switch st.State {
	case client.LANTowerAnswering:
		return fmt.Sprintf("%-28s answering, %d band(s)", entry, st.Bands)
	case client.LANTowerNotAdmitted:
		return fmt.Sprintf("%-28s answering, not admitted: %s", entry, st.Detail)
	case client.LANTowerSameAs:
		return fmt.Sprintf("%-28s %s", entry, st.Detail)
	case client.LANTowerNotAnswering, client.LANTowerWaiting:
		return lanTowerLine(ctx, entry)
	}
	return fmt.Sprintf("%-28s %s: %s", entry, st.State, st.Detail)
}

// lanTowerLine is one `roger lan list` row: the address and what it is right now.
func lanTowerLine(ctx context.Context, entry string) string {
	a, err := lanmode.ParseAddr(entry)
	if err != nil {
		return fmt.Sprintf("%-28s refused: %v", entry, err)
	}
	v := lanmode.Classify(ctx, a, lanResolve)
	if !v.Local && !(v.CGNAT && a.Literal()) {
		return fmt.Sprintf("%-28s refused: %s", entry, v.Reason)
	}
	switch lanmode.Probe(ctx, lanProbeHTTP, a.Base()) {
	case lanmode.ProbeTowerYes:
		return fmt.Sprintf("%-28s answering", entry)
	case lanmode.ProbeNotTower:
		return fmt.Sprintf("%-28s not a standalone Tower", entry)
	}
	return fmt.Sprintf("%-28s not answering", entry)
}

// lanTowerAddrs is every Tower LAN mode uses: the configured list, plus the broker setting
// when it is a local standalone Tower.
func lanTowerAddrs(cfg config) []string {
	out := append([]string(nil), cfg.LanTowers...)
	if isLocalTowerBroker(cfg.Broker) {
		if a, err := lanmode.ParseAddr(cfg.Broker); err == nil {
			out = append(out, a.String())
		}
	}
	return out
}

// lanEdgeBands reads this Edge's fleet record (never dialing anyone): every member band with
// whether its serve was proven, whether the member is reachable LAN-direct, and whether dark.
// This machine's own node is its household, not a member row.
func lanEdgeBands() []client.LANEdgeBand {
	st, err := loadEdgeState()
	if err != nil {
		return nil
	}
	nodes, err := st.fleet.List()
	if err != nil {
		return nil
	}
	self := ""
	if id, _, ok, _ := edgeIdentityStore().LoadIdentity(); ok {
		self = id.NodeID
	}
	var out []client.LANEdgeBand
	for _, n := range nodes {
		if n.ID == self {
			continue
		}
		for _, in := range n.Instances {
			loc := edge.Located{Node: n, Instance: in}
			for _, b := range in.Bands {
				out = append(out, client.LANEdgeBand{Node: n.Name, Band: b, Verified: loc.ServesBand(b),
					LANDirect: edge.HasLANTransport(n), Dark: n.Presence == string(edge.PresenceDark)})
			}
		}
	}
	return out
}

// lanSelfBands is what this machine serves: its household's bands on air.
func lanSelfBands() []string {
	var out []string
	for _, in := range edge.Household(edgeInstancesDir(), time.Now()) {
		out = append(out, in.Bands...)
	}
	return out
}

// lanDial is THE merged LAN dial every surface shows (search, `roger lan list`, the TUI, the
// console): one function so no two surfaces can disagree about what is on the LAN.
var lanDial = func(cfg config) ([]client.LANBand, []client.LANTowerStatus) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return client.LANDiscover(ctx, client.LANInputs{
		Self: lanSelfBands(), Edge: lanEdgeBands(), Towers: lanTowerAddrs(cfg),
		HTTP: lanProbeHTTP, Resolve: lanResolve,
	})
}

// cmdLANSearch is `roger --lan search [--json]`: only local bands, each with its sources.
func cmdLANSearch(cfg config, args []string) error {
	bands, status := lanDial(cfg)
	if len(args) > 0 && args[0] == "--json" {
		b, _ := json.MarshalIndent(map[string]any{"bands": nonNilBands(bands), "towers": status}, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if len(bands) == 0 {
		fmt.Println(lanEmptyText(status))
		return nil
	}
	fmt.Printf("%-28s %-6s %s\n", "BAND", "PRICE", "SOURCE")
	for _, b := range bands {
		fmt.Printf("%-28s %-6s %s\n", b.Model, "free", b.SourceLabel())
	}
	for _, st := range status {
		if st.State != client.LANTowerAnswering {
			fmt.Println(lanStatusText(st))
		}
	}
	return nil
}

func nonNilBands(b []client.LANBand) []client.LANBand {
	if b == nil {
		return []client.LANBand{}
	}
	return b
}

// lanEmptyText is the empty LAN as a status: what was checked, and how to get a band.
func lanEmptyText(status []client.LANTowerStatus) string {
	var b strings.Builder
	b.WriteString("no local band is on air.\n")
	fmt.Fprintf(&b, "checked: this machine, your Edge, and %d Tower(s)\n", len(status))
	for _, st := range status {
		b.WriteString("  " + lanStatusText(st) + "\n")
	}
	b.WriteString("to get one: serve a model here (roger model serve), add an Edge member (roger edge adopt), or add a Tower (roger lan add <address>)")
	return b.String()
}

func lanStatusText(st client.LANTowerStatus) string {
	line := fmt.Sprintf("tower %s: %s", st.Addr, st.State)
	if st.Detail != "" {
		line += " (" + st.Detail + ")"
	}
	if st.More {
		line += "; it lists more than LAN mode shows"
	}
	return line
}
