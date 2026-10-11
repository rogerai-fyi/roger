package main

// LAN mode in the CLI (features/lan/launch_and_config.feature, discovery_and_dial.feature):
// the global --lan/--no-lan flags, the one mode decision per process, `roger lan add|rm|list`
// and `roger status`. The rules themselves live in internal/lanmode; this file only wires them.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edge/wire"
	"rogerai.fm/roger/v6/internal/lanmode"
	"rogerai.fm/roger/v6/internal/onair"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/towerpin"
	"rogerai.fm/roger/v6/internal/tui"
)

// Test seams: where the one-line LAN note goes, how names resolve, and the probe client.
var (
	lanNoteOut   io.Writer        = os.Stderr
	lanResolve   lanmode.Resolver = lanmode.DefaultResolver
	lanProbeHTTP                  = &http.Client{Timeout: 3 * time.Second}
)

// stripLANFlags removes --lan, --no-lan and --edge-only from anywhere in argv, so the flag works
// before or after the subcommand.
func stripLANFlags(argv []string) (rest []string, in lanmode.Inputs) {
	for _, a := range argv {
		switch a {
		case "--lan":
			in.FlagLAN = true
		case "--no-lan":
			in.FlagNoLAN = true
		case "--edge-only":
			in.FlagEdgeOnly = true
		default:
			rest = append(rest, a)
		}
	}
	return rest, in
}

// decideLAN strips the flags and decides this process's mode once (LAN mode and edge-only).
func decideLAN(argv []string, cfg config) ([]string, config, error) {
	rest, in := stripLANFlags(argv)
	in.EnvLAN, in.EnvLANSet = os.LookupEnv("ROGER_LAN")
	in.EnvEdgeOnly, in.EnvEdgeOnlySet = os.LookupEnv("ROGER_LAN_EDGE_ONLY")
	in.Config = cfg.Mode
	m, err := lanmode.Decide(in)
	if err != nil {
		return rest, cfg, err
	}
	cfg.lan = m
	// The Towers this machine joined are pinned to their keys for the life of the process
	// (features/join/tower_identity.feature).
	lanmode.SetPins(cfg.LanTowerPins)
	if m.LAN && !m.EdgeOnly {
		lanFirstUsePins(&cfg)
	}
	return rest, cfg, nil
}

// lanFirstUsePins pins, once, each Tower this machine added before joins were by invite
// (features/join/migration.feature): an entry in lan_towers with no pin is asked for its key over
// TLS, and a Tower that answers is pinned to the key it presented, logged, and recorded as a
// first-use pin. A Tower that answers only plain http (an older roger) is left as it was. Nothing
// outside lan_towers is ever pinned this way, and a pinned Tower is never re-pinned.
func lanFirstUsePins(cfg *config) {
	found := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, addr := range cfg.LanTowers {
		if _, pinned := cfg.LanTowerPins[addr]; pinned || cfg.LanTowerPinDeclined[addr] || contains(cfg.LanNotTower, addr) {
			continue
		}
		wg.Add(1)
		go func(addr string) { // every Tower at once, each bounded: a Tower that is down costs one second
			defer wg.Done()
			if fp, err := lanFirstUseFetch(addr); err == nil {
				mu.Lock()
				found[addr] = fp
				mu.Unlock()
			}
		}(addr)
	}
	wg.Wait()
	if len(found) == 0 {
		return
	}
	release, err := acquireConfigLock()
	if err != nil {
		return
	}
	c := loadConfig()
	if c.LanTowerPins == nil {
		c.LanTowerPins = map[string]string{}
	}
	if c.LanTowerPinFirstUse == nil {
		c.LanTowerPinFirstUse = map[string]bool{}
	}
	var pinned []string
	for addr, fp := range found {
		if _, have := c.LanTowerPins[addr]; have {
			continue // pinned meanwhile: never re-pinned
		}
		c.LanTowerPins[addr], c.LanTowerPinFirstUse[addr] = fp, true
		pinned = append(pinned, addr)
	}
	err = saveConfigLocked(c)
	release()
	if err != nil {
		return
	}
	cfg.LanTowerPins, cfg.LanTowerPinFirstUse = c.LanTowerPins, c.LanTowerPinFirstUse
	lanmode.SetPins(cfg.LanTowerPins)
	for _, addr := range pinned {
		fmt.Printf("pinned the Tower at %s now (first use after upgrade): sha256:%s\n", addr, found[addr])
	}
}

// edgeOnlyNote is how edge-only is named wherever LAN Towers would otherwise appear.
const edgeOnlyNote = "edge-only is on: LAN Towers are not used, only this machine and your Edge members"

// lanOffHint says how to turn LAN mode off, given where it came from.
func lanOffHint(m lanmode.Mode) string {
	switch {
	case m.Source == lanmode.SrcConfig:
		return "turn it off with `roger config unset mode`"
	case m.Source == lanmode.SrcEnv && m.EdgeOnly:
		return "turn it off by unsetting ROGER_LAN_EDGE_ONLY"
	case m.Source == lanmode.SrcEnv:
		return "turn it off by unsetting ROGER_LAN"
	case m.EdgeOnly:
		return "drop --edge-only to turn it off"
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
	case "status", "lan", "config", "version", "help", "-h", "--help", "share":
		return // these say the mode themselves (share opens with it), or are not about bands
	}
	if cfg.lan.EdgeOnly {
		fmt.Fprintf(lanNoteOut, "LAN mode, edge-only (from %s): only bands on this machine and your Edge members, never a LAN Tower; %s\n", cfg.lan.Source, lanOffHint(cfg.lan))
		return
	}
	fmt.Fprintf(lanNoteOut, "LAN mode (from %s): only bands on this machine, your Edge and LAN Towers; %s\n", cfg.lan.Source, lanOffHint(cfg.lan))
}

// cmdStatus is `roger status [--json]`: the mode, where it came from, and the local sources.
func cmdStatus(cfg config, args []string) error {
	mode := "default"
	if cfg.lan.EdgeOnly {
		mode = "lan-edge-only"
	} else if cfg.lan.LAN {
		mode = "lan"
	}
	src := cfg.lan.Source
	if src == "" {
		src = lanmode.SrcDefault
	}
	if len(args) > 0 && args[0] == "--json" {
		b, _ := json.MarshalIndent(map[string]any{
			"mode": mode, "mode_source": src, "broker": cfg.Broker, "lan": cfg.lan.LAN, "edge_only": cfg.lan.EdgeOnly,
			"lan_towers": nonNilStrings(cfg.LanTowers), "edge_prefer": edgePreferOf(cfg),
		}, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if cfg.lan.EdgeOnly {
		fmt.Printf("LAN mode: on (edge-only), from %s\n", src)
		fmt.Println("local sources: this machine and Edge members; LAN Towers are not used")
		fmt.Printf("edge preference: local-only (from LAN mode; your setting is %s)\n", edgePreferOf(cfg))
		fmt.Println(lanOffHint(cfg.lan))
	} else if cfg.lan.LAN {
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

const lanUsage = "usage: roger lan add <address> [--offline] | roger lan rm <address> | roger lan list | roger lan join <invite>"

// cmdLAN is `roger lan add|rm|list`: the standalone Towers LAN mode uses.
func cmdLAN(cfg config, args []string) error {
	if len(args) == 0 {
		return usagef("%s", lanUsage)
	}
	for _, a := range args {
		if a == "-h" || a == "--help" || (a == "help" && a == args[0]) {
			fmt.Println(lanUsage)
			return nil
		}
	}
	if args[0] == "join" {
		return cmdJoin(cfg, args[1:]) // features/join/invite.feature: an alias of roger join
	}
	allowed := map[string][]string{"add": {"--offline"}, "rm": nil, "remove": nil, "list": nil, "ls": nil}
	known, ok := allowed[args[0]]
	if !ok {
		return usagef("unknown lan command %q\n%s", args[0], lanUsage)
	}
	if err := lanFlags(args[1:], known); err != nil {
		return err
	}
	switch args[0] {
	case "add":
		if err := lanAdd(args[1:]); err != nil || !cfg.lan.EdgeOnly {
			return err
		}
		fmt.Println("it will be used in LAN mode, but not while edge-only is on")
		return nil
	case "rm", "remove":
		return lanRemove(args[1:])
	}
	return lanList(cfg)
}

// lanFlags refuses a flag a lan command does not take, in roger's usage style (exit 2), so a
// typo is never read as an address.
func lanFlags(args, known []string) error {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") || a == "-" {
			continue
		}
		name, _, _ := strings.Cut(a, "=")
		if !slices.Contains(known, name) {
			return usagef("flag provided but not defined: %s\n%s", name, lanUsage)
		}
	}
	return nil
}

func lanAdd(args []string) error { return lanAddTo(os.Stdout, args) }

// lanAddTo is `roger lan add`, writing its lines to w: the CLI prints them, the TUI's add from
// the dial shows them in its status line (one function, so both add exactly the same way).
func lanAddTo(w io.Writer, args []string) error {
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
	marked := contains(c.LanNotTower, a.String())
	if contains(c.LanTowers, a.String()) && !marked {
		fmt.Fprintf(w, "%s is already in the list\n", a)
		return nil
	}
	switch lanmode.Check(ctx, lanProbeHTTP, a.Base()) {
	case lanmode.ProbeTowerYes:
		fmt.Fprintf(w, "added %s: it answered as a standalone Tower\n", a)
		if marked {
			// re-added by the owner: it gets turns again
			var keep []string
			for _, v := range c.LanNotTower {
				if v != a.String() {
					keep = append(keep, v)
				}
			}
			c.LanNotTower = keep
			return saveConfig(c)
		}
	case lanmode.ProbeNotTower:
		return fmt.Errorf("%s answers, but it is not a standalone Tower (no standalone answer at /local/poll); not added", a)
	default:
		if !offline {
			return fmt.Errorf("%s did not answer; add it anyway with `roger lan add %s --offline`", a, a)
		}
		fmt.Fprintf(w, "added %s: it did not answer and will be tried at each refresh\n", a)
	}
	if v.CGNAT {
		fmt.Fprintf(w, "note: %s is in the carrier/overlay range (100.64.0.0/10); LAN mode uses it only because you added it by hand\n", a.Host)
	}
	c.LanTowers = append(c.LanTowers, a.String())
	if !lanCarryPin(w, &c, a.String()) {
		lanAskPin(w, &c, a.String())
	}
	fmt.Fprintf(w, "to join a Tower you are not admitted to yet, the new way: roger join <invite>\n")
	if err := saveConfig(c); err != nil {
		return err
	}
	lanmode.SetPins(c.LanTowerPins)
	return nil
}

// lanFirstUseFetch is a seam: the first-use key read, kept short since it runs as LAN mode starts.
var lanFirstUseFetch = func(addr string) (string, error) { return towerpin.Fetch(addr, time.Second) }

// lanFetchFP is a seam: the fingerprint of the key a Tower at addr presents over TLS.
var lanFetchFP = func(addr string) (string, error) { return towerpin.Fetch(addr, 3*time.Second) }

// lanCarryPin keeps a Tower's pin when it moved (features/join/tower_identity.feature, "The pin
// follows the Tower, not its address"): a Tower at a new address presenting a key this machine
// already pinned is pinned again at that address. Nothing new is trusted: a key not already
// pinned is not pinned here.
func lanCarryPin(w io.Writer, c *config, addr string) bool {
	fp, err := lanFetchFP(addr)
	if err != nil {
		return false
	}
	for _, have := range c.LanTowerPins {
		if have == fp {
			if c.LanTowerPins == nil {
				c.LanTowerPins = map[string]string{}
			}
			c.LanTowerPins[addr] = fp
			fmt.Fprintf(w, "it is a Tower you joined (same key %s): it stays verified at %s\n", towerpin.Short(fp), addr)
			return true
		}
	}
	return false
}

// lanAddConfirm is a seam: the owner's answer to "pin this Tower? [y/N]" (y on standard input).
var lanAddConfirm = func() bool {
	var answer string
	_, _ = fmt.Scanln(&answer)
	return strings.EqualFold(strings.TrimSpace(answer), "y")
}

// lanAskPin shows a TLS Tower's short fingerprint and pins it only on y (features/join/migration.feature,
// ruled 2026-10-10): the owner compares it with what `roger tower status` shows on the Tower.
func lanAskPin(w io.Writer, c *config, addr string) {
	fp, err := lanFetchFP(addr)
	if err != nil {
		return // a plain-http Tower has no key to pin
	}
	fmt.Fprintf(w, "it serves TLS under %s; pin this Tower? [y/N] ", towerpin.Short(fp))
	if !lanAddConfirm() {
		if c.LanTowerPinDeclined == nil {
			c.LanTowerPinDeclined = map[string]bool{}
		}
		c.LanTowerPinDeclined[addr] = true
		fmt.Fprintln(w, "\nnot pinned: it stays unverified until you join it with an invite")
		return
	}
	if c.LanTowerPins == nil {
		c.LanTowerPins = map[string]string{}
	}
	delete(c.LanTowerPinDeclined, addr)
	c.LanTowerPins[addr] = fp
	fmt.Fprintf(w, "\npinned %s: it is verified from now on\n", addr)
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

func lanList(cfg config) error {
	c := loadConfig()
	if len(c.LanTowers) == 0 {
		fmt.Println("no LAN Towers: add one with `roger lan add <address>`")
		return nil
	}
	if cfg.lan.EdgeOnly {
		// Under edge-only no Tower is contacted, not even to say whether it answers.
		for _, entry := range c.LanTowers {
			fmt.Printf("%-28s not used (edge-only)%s\n", entry, lanPinNote(c, entry))
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, status := client.LANDiscover(ctx, client.LANInputs{Towers: c.LanTowers, HTTP: lanProbeHTTP, Resolve: lanResolve})
	for i, entry := range c.LanTowers {
		fmt.Println(lanListLine(ctx, entry, status[i]) + lanPinNote(c, entry))
	}
	return nil
}

// lanPinNote says whether a listed Tower is pinned, with the read-aloud form of its fingerprint,
// so the owner can compare it with the Tower's own screen without opening the config.
func lanPinNote(c config, entry string) string {
	if fp, ok := c.LanTowerPins[entry]; ok {
		if hex, ok := towerpin.Parse(fp); ok {
			return " · pinned " + towerpin.Short(hex)
		}
		return " · pinned"
	}
	return " · unverified (plain http)"
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
	switch lanmode.Check(ctx, lanProbeHTTP, a.Base()) {
	case lanmode.ProbeTowerYes:
		return fmt.Sprintf("%-28s answering", entry)
	case lanmode.ProbeNotTower:
		return fmt.Sprintf("%-28s not a standalone Tower", entry)
	}
	return fmt.Sprintf("%-28s not answering", entry)
}

// lanTowerAddrs is every Tower LAN mode uses: the configured list (a broker setting that is a
// standalone Tower is folded into it once, foldBrokerTower), less the ones marked not a Tower.
func lanTowerAddrs(cfg config) []string {
	if cfg.lan.EdgeOnly {
		return nil // features/lan/edge_only.feature: no Tower, anywhere
	}
	var out []string
	for _, a := range cfg.LanTowers {
		if !contains(cfg.LanNotTower, a) {
			out = append(out, a)
		}
	}
	return out
}

// lanEdgeBands reads this Edge's fleet record (never dialing anyone): every member band with
// whether its serve was proven, whether the member is reachable LAN-direct, and whether dark.
// This machine's own node is its household, not a member row.
func lanEdgeBands() ([]client.LANEdgeBand, error) {
	st, err := loadEdgeState()
	if err != nil {
		return nil, err
	}
	nodes, err := st.fleet.List()
	if err != nil {
		return nil, err
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
	return out, nil
}

// lanSelfBands is what this machine serves: its household's bands on air.
func lanSelfBands() []string {
	var out []string
	for _, in := range edge.Household(edgeInstancesDir(), time.Now()) {
		out = append(out, in.Bands...)
	}
	for _, rec := range onair.Shares() {
		out = append(out, rec.Band)
	}
	return out
}

// lanDial is THE merged LAN dial every surface shows (search, `roger lan list`, the TUI, the
// console): one function so no two surfaces can disagree about what is on the LAN.
var lanDial = func(cfg config) client.LANDial {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	towers := lanTowerAddrs(cfg)
	cands := make(chan []client.LANTowerStatus, 1)
	if cfg.lan.EdgeOnly {
		cands <- nil // no Tower is offered, so none is browsed for
	} else {
		go func() { cands <- lanCandidates(ctx, towers) }() // the mDNS browse runs beside the Tower reads
	}
	edgeBands, edgeErr := lanEdgeBands()
	bands, status := client.LANDiscover(ctx, client.LANInputs{
		Self: lanSelfBands(), Voices: lanVoiceNames(), Edge: edgeBands, Towers: towers,
		HTTP: lanProbeHTTP, Resolve: lanResolve,
	})
	status = append(status, <-cands...)
	noteLANServed(bands, status)
	for _, a := range lanTowerNotes(cfg) {
		status = append(status, client.LANTowerStatus{Addr: a, State: client.LANTowerNotStandalone,
			Detail: "it answered without the local marker; re-add it with roger lan add " + a})
	}
	for _, st := range status {
		if st.State == client.LANTowerAnswering {
			lanmode.NoteTower(st.Addr)
		}
	}
	d := client.LANDial{Bands: bands, Towers: status}
	if edgeErr != nil {
		d.EdgeUnread = edgeErr.Error()
	}
	return d
}

// lanTowerNotes is the configured Towers marked "not a standalone Tower", shown on the dial; none
// under edge-only, where no Tower is shown at all.
func lanTowerNotes(cfg config) []string {
	if cfg.lan.EdgeOnly {
		return nil
	}
	return loadConfig().LanNotTower
}

// lanMarkNotTower records that addr answered a turn without the local marker.
func lanMarkNotTower(addr string) {
	c := loadConfig()
	if contains(c.LanNotTower, addr) {
		return
	}
	c.LanNotTower = append(c.LanNotTower, addr)
	_ = saveConfig(c)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// cmdLANSearch is `roger --lan search [--json]`: only local bands, each with its sources.
func cmdLANSearch(cfg config, args []string) error {
	defer edgeScans.Begin(scanLAN)() // this run is the request (scan_on_request.feature)
	d := lanDial(cfg)
	bands, status := d.Bands, d.Towers
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
	checked := 0
	for _, st := range status {
		if st.State != client.LANTowerCandidate {
			checked++
		}
	}
	fmt.Fprintf(&b, "checked: this machine, your Edge, and %d Tower(s)\n", checked)
	for _, st := range status {
		b.WriteString("  " + lanStatusText(st) + "\n")
	}
	b.WriteString("to get one: serve a model here (roger model serve), add an Edge member (roger edge adopt), or add a Tower (roger lan add <address>)")
	return b.String()
}

func lanStatusText(st client.LANTowerStatus) string {
	if st.Addr == "" { // a summary line (more candidates heard than shown)
		return st.Detail
	}
	line := fmt.Sprintf("tower %s: %s", st.Addr, st.State)
	if st.Detail != "" {
		line += " (" + st.Detail + ")"
	}
	if st.More {
		line += "; it lists more than LAN mode shows"
	}
	return line
}

// lanSelfServe is this machine's own serve of a band: a `roger model serve` record, or a band
// this process has on the air through SHARE (the upstream it serves peers from). It returns the
// completions URL and key, or "" when nothing here serves it.
var lanSelfServe = func(band string) (string, string) {
	for _, sv := range modelHome().LiveAll() {
		if sv.Band == band && sv.BaseURL != "" {
			return strings.TrimRight(sv.BaseURL, "/") + "/chat/completions", sv.Key
		}
	}
	if edgeUpstreamOf != nil {
		if u, k, ok := edgeUpstreamOf(band); ok && u != "" {
			return u, k
		}
	}
	for _, rec := range onair.Shares() { // a separate `roger share` process on this machine
		if rec.Band == band && rec.Modality != protocol.ModalityTTS { // a voice is not a chat turn's source
			return rec.ChatURL, rec.Key
		}
	}
	return "", ""
}

// lanSelfQuant is the quant label of this machine's own serve of a band: the `roger model serve`
// record names its weights' quant; a SHARE upstream carries no label ("" = unlabeled).
var lanSelfQuant = func(band string) string {
	for _, sv := range modelHome().LiveAll() {
		if sv.Band == band && sv.BaseURL != "" {
			return sv.Quant
		}
	}
	return ""
}

// applyLAN puts the ladder in LAN mode: this machine, then Edge members, then the LAN
// Towers serving the band, from the one LAN dial; the market is never a rung.
func applyLAN(cfg config, o *client.ProxyOptions) {
	if !cfg.lan.LAN {
		return
	}
	o.LANMode, o.LANEdgeOnly, o.EdgePrefer = true, cfg.lan.EdgeOnly, "local-only"
	o.LANSelf = lanSelfServe
	// provider.quantizations, provider.order and @class/ on the ladder: this machine's quant and
	// name, and the bands every local source serves (the one LAN dial) for a class to expand over.
	o.LANSelfQuant = func(b string) string { return lanSelfQuant(b) }
	if id, _, ok, _ := edgeIdentityStore().LoadIdentity(); ok {
		o.LANSelfName = id.NodeID
	}
	o.LANBands = func() []string {
		var out []string
		for _, b := range lanDial(cfg).Bands {
			if !b.Voice {
				out = append(out, b.Model)
			}
		}
		return out
	}
	o.LANHTTP, o.LANResolve = lanProbeHTTP, lanResolve
	if !cfg.lan.EdgeOnly {
		o.LANTowers = func(band string) []string { return lanTowersServing(cfg, band) }
	}
	o.LANNotLocal = lanMarkNotTower
}

// frontEndLadder is the dispatch hook every front-end that tunes a band installs (the TUI,
// `roger webui`, `roger resume`): the Edge ladder, and in LAN mode the LAN ladder over it.
func frontEndLadder(cfg config) func(*client.ProxyOptions) {
	return func(o *client.ProxyOptions) { applyEdgeLadder(cfg, o); applyLAN(cfg, o) }
}

func init() {
	// Every request this process signs to a standalone Tower it knows by address carries the
	// replay nonce (ruled 2026-10-07, loopback included): the Towers added with `roger lan add`
	// (a CGNAT, loopback or named one too); lanmode itself remembers every Tower this process has
	// confirmed (lanmode.Check, lanmode.NoteTower).
	client.SetLANTowerHosts(func() []string { return loadConfig().LanTowers })
}

// lanDialFor is the LAN dial for a front end in LAN mode, nil outside it.
func lanDialFor(cfg config) func() client.LANDial {
	if !cfg.lan.LAN {
		return nil
	}
	return func() client.LANDial { return lanDial(cfg) }
}

// installLANRelay puts every path that carries a turn in this process (TUI chat, console chat,
// the agent) on the same local ladder `roger use` gets, and gives the TUI and the console the
// LAN dial `roger --lan search` prints. It does nothing outside LAN mode.
func installLANRelay(cfg config) {
	if !cfg.lan.LAN {
		if cfg.EdgePrefer == "local-only" {
			// local-only is structural too (features/edge/mode.feature): chat, console and the
			// agent go through the same one relay as the proxy, which has no market rung.
			lo := client.ProxyOptions{LANSelf: lanSelfServe}
			applyEdgeLadder(cfg, &lo)
			client.SetLANRelay(&lo)
		}
		return
	}
	tui.SetLANDial(lanDialFor(cfg))
	tui.SetLANEdgeOnly(cfg.lan.EdgeOnly)
	tui.SetLANSpeak(lanSpeakLine)
	var lo client.ProxyOptions
	frontEndLadder(cfg)(&lo)
	client.SetLANRelay(&lo)
}

// lanTowersServing lists the LAN Towers whose /discover lists band, in configured order.
func lanTowersServing(cfg config, band string) []string {
	d := lanDial(cfg)
	bands, status := d.Bands, d.Towers
	var out []string
	for _, b := range bands {
		if b.Model != band {
			continue
		}
		for _, s := range b.Sources {
			if s.Kind == client.LANTower {
				out = append(out, s.Addr)
			}
		}
	}
	// A configured Tower that served this band at an earlier refresh and is REFUSED now (its name
	// resolves elsewhere, for one) is still handed to the relay: the relay's connect-time check
	// refuses it again and names why, so the turn's refusal says what happened rather than that
	// nothing ever served the band (features/lan/adversarial.feature). It is never dialed.
	for _, st := range status {
		if st.State == client.LANTowerRefused && lanLastServed(st.Addr, band) && !contains(out, st.Addr) {
			out = append(out, st.Addr)
		}
	}
	return out
}

// lanServed remembers, for this process, which bands each configured Tower listed at the last
// refresh that read it. It is only a hint for naming a refusal; nothing is routed on it.
var (
	lanServedMu sync.Mutex
	lanServed   = map[string]map[string]bool{}
)

func noteLANServed(bands []client.LANBand, status []client.LANTowerStatus) {
	lanServedMu.Lock()
	defer lanServedMu.Unlock()
	for _, st := range status {
		if st.State == client.LANTowerAnswering {
			lanServed[st.Addr] = map[string]bool{}
		}
	}
	for _, b := range bands {
		for _, s := range b.Sources {
			if s.Kind == client.LANTower && lanServed[s.Addr] != nil {
				lanServed[s.Addr][b.Model] = true
			}
		}
	}
}

func lanLastServed(addr, band string) bool {
	lanServedMu.Lock()
	defer lanServedMu.Unlock()
	return lanServed[addr][band]
}

// lanSourcesOf is what serves a band on the LAN, for the `roger use` plate and its refusal.
func lanSourcesOf(cfg config, band string) []client.LANSource {
	bands := lanDial(cfg).Bands
	for _, b := range bands {
		if b.Model == band {
			return b.Sources
		}
	}
	return nil
}

// lanTowerAddress reports whether a `roger use` argument addresses a Tower (host:port at a
// local address, ruled 2026-10-06), returning its normalized address.
func lanTowerAddress(arg string) (string, bool) {
	if !strings.Contains(arg, ":") {
		return "", false
	}
	a, err := lanmode.ParseAddr(arg)
	if err != nil || !a.Literal() {
		return "", false
	}
	if v := lanmode.ClassifyLiteral(a.String()); !v.Local && !v.CGNAT {
		return "", false
	}
	return a.String(), true
}

// lanCandidateCap bounds how many advertised Towers one browse keeps (and the dial shows); a
// flood past it is counted, never stored (features/lan/adversarial.feature).
const lanCandidateCap = 8

// lanBrowseWindow is how long one dial refresh listens for Tower advertisements.
const lanBrowseWindow = 800 * time.Millisecond

// defaultLANBrowseTowers browses the LAN for standalone Towers started with --advertise.
func defaultLANBrowseTowers(ctx context.Context) (wire.BrowseResult, error) {
	tp, err := wire.NewRejoiningMulticastTransport(func() []net.Interface {
		ifaces, _ := net.Interfaces()
		return ifaces
	})
	if err != nil {
		return wire.BrowseResult{}, err
	}
	defer tp.Close()
	return wire.Browse(ctx, tp, wire.TowerService, lanBrowseWindow, lanCandidateCap)
}

// lanBrowseTowers is the browse, as a seam: a test hears scripted advertisements, never the LAN.
var lanBrowseTowers = defaultLANBrowseTowers

// lanCandidates turns what the browse heard into candidate lines (features/lan/discovery_and_dial
// .feature): a Tower advertising a LOCAL address that is not already in lan_towers, named with how
// to add it. A candidate is only a line; nothing is ever read from or sent to it until the owner
// adds it. An advert naming a public address is not offered at all.
func lanCandidates(ctx context.Context, towers []string) []client.LANTowerStatus {
	if !edgeMayBrowse(scanLAN) {
		return nil // outside an Edge with no LAN view open: no browse, no socket
	}
	res, err := lanBrowseTowers(ctx)
	if err != nil {
		return nil
	}
	var out []client.LANTowerStatus
	for _, ad := range res.Adverts {
		if ad.Kind != wire.TowerKind || ad.IP == nil || ad.Port <= 0 || ad.Port > 65535 {
			continue
		}
		a, err := lanmode.ParseAddr(net.JoinHostPort(ad.IP.String(), strconv.Itoa(ad.Port)))
		if err != nil || !lanmode.ClassifyLiteral(a.String()).Local || contains(towers, a.String()) {
			continue
		}
		out = append(out, client.LANTowerStatus{Addr: a.String(), State: client.LANTowerCandidate,
			Detail: fmt.Sprintf("advertised as %q; add it with roger lan add %s", cleanLabel(ad.Name), a)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Addr < out[j].Addr })
	if more := res.Seen - len(res.Adverts); more > 0 {
		out = append(out, client.LANTowerStatus{State: client.LANTowerCandidate,
			Detail: fmt.Sprintf("%d more Tower advertisement(s) heard and not shown", more)})
	}
	return out
}

// cleanLabel keeps an advertised label printable and short: it is whatever a LAN device said.
func cleanLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// lanTrustNote is the plain-words statement of the trust-the-LAN ruling (2026-10-06), shown once
// per machine, the first time a Tower would serve a turn (features/lan/trust_and_honesty.feature).
const lanTrustNote = "note: a LAN Tower speaks plain http with no identity of its own, so any device on this network " +
	"can pose as it, and it can read what you send to it. Edge members are verified by certificate and are not affected. " +
	"(shown once)"

// lanPlainTowerLine asks for the upgrade a plain-http Tower needs before it can be verified
// (features/join/tower_identity.feature section 4). It is shown each time such a Tower is used.
const lanPlainTowerLine = "this Tower uses plain http; ask its owner to upgrade roger so it can be verified"

// lanTrustNoteOnce speaks when a turn may go to a plain-http Tower (one not pinned to its TLS key;
// addressed is a Tower named directly with `roger use <tower-address>`, "" when none). It prints
// the upgrade line every time, and the trust note once per machine, recorded so it is not
// repeated on every command. A pinned Tower is verified, so it gets neither.
func lanTrustNoteOnce(sources []client.LANSource, addressed string) {
	plain := false
	if addressed != "" {
		_, pinned := lanmode.PinFor(addressed)
		plain = !pinned
	}
	for _, s := range sources {
		plain = plain || (s.Kind == client.LANTower && !s.Verified)
	}
	if !plain {
		return
	}
	fmt.Println(lanPlainTowerLine)
	c := loadConfig()
	if c.LanTrustNoted {
		return
	}
	fmt.Println(lanTrustNote)
	c.LanTrustNoted = true
	_ = saveConfig(c)
}
