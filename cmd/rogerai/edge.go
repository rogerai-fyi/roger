package main

// Roger Edge's command surface: `roger edge` and its subcommands (features/edge/cli.feature).
//
// TWO TRANSPORTS, ONE COMMAND TREE. Every leaf here is written once and reaches a node
// over whichever path answers: LAN-direct first, the relay second, silently. The owner
// never has to know which carried their request, and only --verbose says.
//
// The fleet this reads is the LAST KNOWN one, kept in a small file beside config.json and
// refreshed by `roger edge scan`. That is what makes a machine with no network useful
// rather than blank: the command still answers, and says plainly that what it is showing
// is stale and how old it is.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// --- exit codes -----------------------------------------------------------

// usageError is the caller's mistake rather than a failure of the thing they asked for.
// It exits 2, so a script can tell "you typed it wrong" from "it did not work" - and an
// empty fleet from either, because that is a 0.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return &usageError{msg: fmt.Sprintf(format, a...)} }

// exitCode maps a command's error to the process status: 0 success, 1 a real failure,
// 2 usage.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var u *usageError
	if errors.As(err, &u) {
		return 2
	}
	return 1
}

// --- the surface ----------------------------------------------------------

// edgeSubcommand is one leaf: its name, the arguments it takes, and the one-line purpose
// that is contract. `roger edge <cmd> --help` prints this purpose and the group usage is
// built from the same table, so the two can never drift apart.
type edgeSubcommand struct {
	name    string
	args    string
	purpose string
	// min/max positional arguments. A leaf with no placeholder takes NO arguments: a
	// stray one is refused rather than ignored, because silently dropping an argument
	// is how a command does something other than what was asked.
	min, max int
}

var edgeSubcommands = []edgeSubcommand{
	{"list", "", "show every node on this Edge", 0, 0},
	{"describe", "<node>", "show one node in full", 1, 1},
	{"name", "<node> <name>", "give a node a name the owner chooses", 2, 2},
	{"forget", "<node>", "remove a node from this Edge", 1, 1},
	{"adopt", "<candidate>", "take a discovered candidate into the fleet", 1, 1},
	{"scan", "", "look for nodes on this network now", 0, 0},
	{"enroll", "[name]", "join this machine to your Edge", 0, 1},
	{"authority", "[local <name>|core|allow <key>]", "show or choose what roots this Edge", 0, 2},
}

func edgeLeaf(name string) (edgeSubcommand, bool) {
	for _, c := range edgeSubcommands {
		if c.name == name {
			return c, true
		}
	}
	return edgeSubcommand{}, false
}

// edgeNames lists the valid subcommands, for a usage error that helps.
func edgeNames() string {
	var out []string
	for _, c := range edgeSubcommands {
		out = append(out, c.name)
	}
	return strings.Join(out, " | ")
}

func edgeUsage() string {
	var b strings.Builder
	b.WriteString("roger edge - your fleet: the machines you own\n\n")
	b.WriteString("  roger edge                    show the fleet (same as `roger edge list`)\n")
	for _, c := range edgeSubcommands {
		b.WriteString(fmt.Sprintf("  %-30s%s\n", strings.TrimSpace("roger edge "+c.name+" "+c.args), c.purpose))
	}
	b.WriteString(`
  list    --json  --capability <cap>  --dark  --candidates
  forget  --yes                       skip the confirmation
  any     --verbose                   say which transport carried the request
`)
	return b.String()
}

// edgeHelp is one leaf's help.
func edgeHelp(c edgeSubcommand) string {
	return fmt.Sprintf("roger edge %s %s\n  %s\n\n%s",
		c.name, c.args, c.purpose, edgeUsage())
}

// cmdEdge is the group. Bare `roger edge` shows the fleet, because that is what the
// owner asked for; a group that answers a plain noun with a usage screen is making the
// reader pay for the tree's shape.
func cmdEdge(cfg config, args []string) error {
	if len(args) == 0 {
		return cmdEdgeList(cfg, nil)
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Print(edgeUsage())
		return nil
	}
	leaf, ok := edgeLeaf(args[0])
	if !ok {
		return usagef("unknown edge command %q; the edge commands are: %s", args[0], edgeNames())
	}
	rest := args[1:]
	if edgeWantsHelp(rest) {
		fmt.Print(edgeHelp(leaf))
		return nil
	}
	switch leaf.name {
	case "list":
		return cmdEdgeList(cfg, rest)
	case "describe":
		return cmdEdgeDescribe(cfg, rest)
	case "name":
		return cmdEdgeName(cfg, rest)
	case "forget":
		return cmdEdgeForget(cfg, rest)
	case "adopt":
		return cmdEdgeAdopt(cfg, rest)
	case "enroll":
		return cmdEdgeEnroll(cfg, rest)
	case "authority":
		return cmdEdgeAuthority(cfg, rest)
	default:
		return cmdEdgeScan(cfg, rest)
	}
}

func edgeWantsHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			return true
		}
	}
	return false
}

// --- argument parsing -----------------------------------------------------

// edgeFlags is what one leaf's flags may be: the name, and whether it takes a value.
var edgeFlags = map[string]bool{
	"json": false, "dark": false, "candidates": false, "yes": false, "verbose": false,
	"force":      false,
	"capability": true, "authority": true, "account": true,
}

type edgeArgv struct {
	pos   []string
	flags map[string]string
}

func (a edgeArgv) has(name string) bool { _, ok := a.flags[name]; return ok }

// parseEdgeArgv splits a leaf's argv into positionals and flags. Hand-rolled because a
// leaf takes its node BEFORE its flags (`roger edge describe bench-pi --verbose`) and
// Go's flag package stops at the first positional; and because an unknown flag has to be
// a usage error rather than a silently ignored word.
func parseEdgeArgv(leaf edgeSubcommand, args []string) (edgeArgv, error) {
	out := edgeArgv{flags: map[string]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			out.pos = append(out.pos, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		takesVal, known := edgeFlags[name]
		if !known {
			return out, usagef("roger edge %s: unknown flag %q", leaf.name, a)
		}
		if takesVal && !hasVal {
			if i+1 >= len(args) {
				return out, usagef("roger edge %s: --%s needs a value", leaf.name, name)
			}
			i++
			val = args[i]
		}
		out.flags[name] = val
	}
	if len(out.pos) > leaf.max {
		if leaf.max == 0 {
			return out, usagef("roger edge %s takes no arguments (got %q)", leaf.name, out.pos[0])
		}
		return out, usagef("roger edge %s takes %s, and nothing else (got %q)",
			leaf.name, leaf.args, out.pos[leaf.max])
	}
	if len(out.pos) < leaf.min {
		return out, usagef("usage: roger edge %s %s", leaf.name, leaf.args)
	}
	return out, nil
}

// --- the last known Edge --------------------------------------------------

// edgeStatePath is where this machine remembers its Edge between runs: beside
// config.json, owner-readable only.
func edgeStatePath() string { return filepath.Join(filepath.Dir(configPath()), "edge.json") }

// edgeSnapshot is the file's shape. Candidates are kept beside the fleet, never in it:
// seeing a machine on your network is not deciding it is yours.
type edgeSnapshot struct {
	Account    string           `json:"account"`
	Nodes      []store.EdgeNode `json:"nodes,omitempty"`
	Candidates []store.EdgeNode `json:"candidates,omitempty"`
}

// edgeState is one command's view of the Edge: the real fleet over a real store, loaded
// from the snapshot and written back when a command changed something.
type edgeState struct {
	account    string
	db         *store.Mem
	fleet      *edge.Fleet
	candidates []store.EdgeNode
}

// edgeAccount is the owner this machine's Edge belongs to. Anonymous is a real state and
// not an error: a LAN scan needs no login. Its findings are cached under the anonymous
// account, so a later login never silently inherits them.
func edgeAccount() string {
	// The account of record is the one this machine's CERTIFICATE enrolled it into: an
	// Edge rooted at a designated machine has no Core login behind it at all. A machine
	// that has not enrolled falls back to the login, so a plain LAN scan still works.
	if a, err := edgeIdentityStore().AccountName(); err == nil && a != "" {
		return a
	}
	return client.LinkedLogin()
}

func loadEdgeState() (*edgeState, error) {
	acct := edgeAccount()
	db := store.NewMem()
	st := &edgeState{account: acct, db: db, fleet: edge.NewFleet(db, acct)}
	b, err := os.ReadFile(edgeStatePath())
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil // no cache yet is an empty Edge, not a failure
		}
		return nil, err
	}
	var snap edgeSnapshot
	if json.Unmarshal(b, &snap) != nil || snap.Account != acct {
		// A corrupt cache, or one belonging to another account, is treated as no cache:
		// it is a cache, and showing another account's machines would be worse than
		// showing none.
		return st, nil
	}
	for _, n := range snap.Nodes {
		_, _ = db.EnrollEdgeNode(n)
	}
	st.candidates = snap.Candidates
	return st, nil
}

func (s *edgeState) save() error {
	nodes, err := s.db.EdgeNodesOfAccount(s.account)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(edgeSnapshot{
		Account: s.account, Nodes: nodes, Candidates: s.candidates}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteConfig(edgeStatePath(), b)
}

// list is the fleet as this machine last knew it.
func (s *edgeState) list() ([]store.EdgeNode, error) { return s.fleet.List() }

// --- reaching a node ------------------------------------------------------

// edgeDialTimeout bounds ONE reachability probe. A fleet view must not hang on a node
// that went away.
var edgeDialTimeout = 2 * time.Second

// edgeReach is the outcome of trying to reach one node: which transport carried it, and
// what was tried and failed on the way there.
type edgeReach struct {
	Via      string // "lan" | "relay" | "" when nothing answered
	Addr     string
	Attempts []string
}

// edgeReachOf tries a node's transports in PREFERENCE order - the record is stored
// LAN-first - and stops at the first that answers. A failed LAN attempt is remembered
// rather than announced: the fall-through to the relay is silent unless asked.
//
// A DARK node is not dialed at all: it is already known to have stopped answering, and a
// fleet listing must not cost one timeout per dead node.
func edgeReachOf(cfg config, n store.EdgeNode) edgeReach {
	var r edgeReach
	if n.Presence == string(edge.PresenceDark) {
		return r
	}
	for _, t := range n.Transports {
		addr := t.Addr
		if addr == "" && t.Kind == "relay" {
			addr = edgeBrokerAddr(cfg.Broker)
		}
		if addr == "" {
			r.Attempts = append(r.Attempts, t.Kind+" has no address to try")
			continue
		}
		c, err := net.DialTimeout("tcp", addr, edgeDialTimeout)
		if err != nil {
			r.Attempts = append(r.Attempts, fmt.Sprintf("%s attempt to %s failed: %v", t.Kind, addr, err))
			continue
		}
		_ = c.Close()
		r.Via, r.Addr = t.Kind, addr
		return r
	}
	return r
}

// edgeReachAll probes every node AT ONCE. A fleet view must cost one timeout, not one
// per node: on a network that swallows packets, ten nodes probed in turn is twenty
// seconds of an owner's time for an answer that is already cached. Each probe writes only
// its own slot, so there is nothing here two goroutines share.
func edgeReachAll(cfg config, nodes []store.EdgeNode) map[string]edgeReach {
	got := make([]edgeReach, len(nodes))
	var wg sync.WaitGroup
	for i := range nodes {
		wg.Add(1)
		go func(i int) { defer wg.Done(); got[i] = edgeReachOf(cfg, nodes[i]) }(i)
	}
	wg.Wait()
	out := make(map[string]edgeReach, len(nodes))
	for i, n := range nodes {
		out[n.ID] = got[i]
	}
	return out
}

// edgeBrokerAddr is the relay's host:port, for a relay transport that names no address
// of its own (the ordinary case: the node is reached through the fabric this CLI already
// talks to).
func edgeBrokerAddr(broker string) string {
	u, err := url.Parse(broker)
	if err != nil || u.Host == "" {
		return ""
	}
	if u.Port() != "" {
		return u.Host
	}
	if u.Scheme == "http" {
		return u.Host + ":80"
	}
	return u.Host + ":443"
}

// --- rendering ------------------------------------------------------------

// edgeAge says how long ago something was, in seconds where seconds matter. A fleet
// heartbeat is a matter of seconds, so "now" would hide exactly the difference an owner
// is looking for.
func edgeAge(unix int64) string {
	if unix <= 0 {
		return "never"
	}
	d := time.Since(time.Unix(unix, 0))
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// edgeCapLabel marks a capability with how far its verification got. A CLAIMED
// capability is marked as claimed everywhere it is shown: nothing routes on a claim, so
// nothing should read like it does.
func edgeCapLabel(c store.EdgeCap) string {
	switch c.State {
	case string(edge.Verified):
		return c.Name
	case string(edge.PendingConfirmation):
		return c.Name + "(pending your confirmation)"
	default:
		return c.Name + "(claimed)"
	}
}

func edgeCapsLabel(n store.EdgeNode) string {
	if len(n.Caps) == 0 {
		return "-"
	}
	var out []string
	for _, c := range n.Caps {
		out = append(out, edgeCapLabel(c))
	}
	return strings.Join(out, " ")
}

// edgeSeenLabel is the last-seen cell: a dark node says so here, with its age, because a
// fleet that drops silent members hides the problem it exists to show.
func edgeSeenLabel(n store.EdgeNode) string {
	switch n.Presence {
	case string(edge.PresenceDark):
		return "dark, " + edgeAge(n.LastSeen)
	case string(edge.PresenceReenroll):
		// Not a liveness problem and not sayable as an age: this node is still a member
		// and its credential is no longer one this Edge can check.
		return "NEEDS RE-ENROLL (roger edge enroll)"
	}
	return edgeAge(n.LastSeen)
}

func edgeShortID(id string) string {
	if len(id) <= 10 {
		return id
	}
	return id[:10]
}

// --- list -----------------------------------------------------------------

func cmdEdgeList(cfg config, args []string) error {
	leaf, _ := edgeLeaf("list")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	nodes, err := st.list()
	if err != nil {
		return err
	}
	cands := st.candidates

	if want, ok := argv.flags["capability"]; ok {
		c := edge.Capability(want)
		if !c.Valid() {
			return usagef("%q is not a capability; the vocabulary is: %s", want, edgeCapVocabulary())
		}
		nodes = edgeFilter(nodes, func(n store.EdgeNode) bool { return edge.Declares(n, c) })
		cands = nil
	}
	if argv.has("dark") {
		nodes = edgeFilter(nodes, func(n store.EdgeNode) bool {
			return n.Presence == string(edge.PresenceDark)
		})
		cands = nil
	}
	if argv.has("candidates") {
		nodes = nil
	}
	filtered := argv.has("dark") || argv.has("candidates") || argv.has("capability")

	// Reach every member ONCE, and reuse the answer: the transport column, the staleness
	// banner and --verbose are three views of the same probe.
	reach := edgeReachAll(cfg, nodes)
	reachable := 0
	for _, r := range reach {
		if r.Via != "" {
			reachable++
		}
	}

	if argv.has("json") {
		return edgeWriteJSON(os.Stdout, nodes, cands, reach)
	}
	if len(nodes) == 0 && len(cands) == 0 {
		if filtered {
			fmt.Println("nothing on this Edge matches that.")
			return nil
		}
		fmt.Println("your Edge: this machine is the only node.")
		fmt.Println("  find the others on this network:  roger edge scan")
		fmt.Println("  then take one into the fleet:     roger edge adopt <node>")
		return nil
	}
	// Nothing answered, but the fleet is not empty: say that this is remembered, not
	// observed, and how old the memory is. An owner with no network gets an answer.
	if len(nodes) > 0 && reachable == 0 {
		fmt.Printf("(stale: nothing on your Edge answered just now - this is the last known fleet, as of %s)\n",
			edgeAge(edgeNewestSeen(nodes)))
	}
	if note := edgeTrustNote(time.Now()); note != "" {
		fmt.Println(note)
	}
	if len(nodes) > 0 {
		edgeWriteTable(os.Stdout, nodes, reach)
	}
	if len(cands) > 0 {
		fmt.Println()
		fmt.Println("candidates (seen on this network, not yours until you say so):")
		for _, c := range cands {
			fmt.Printf("  %-52s %-6s %-22s adopt it: roger edge adopt %s\n",
				c.ID, c.Kind, edge.LANAddr(c), c.Name)
		}
	}
	return nil
}

func edgeCapVocabulary() string {
	var out []string
	for _, c := range edge.Capabilities() {
		out = append(out, string(c))
	}
	return strings.Join(out, " ")
}

func edgeFilter(ns []store.EdgeNode, keep func(store.EdgeNode) bool) []store.EdgeNode {
	var out []store.EdgeNode
	for _, n := range ns {
		if keep(n) {
			out = append(out, n)
		}
	}
	return out
}

func edgeNewestSeen(ns []store.EdgeNode) int64 {
	var newest int64
	for _, n := range ns {
		if n.LastSeen > newest {
			newest = n.LastSeen
		}
	}
	return newest
}

// edgeWriteTable is the fleet view: one row per node, the columns the spec names.
func edgeWriteTable(w io.Writer, nodes []store.EdgeNode, reach map[string]edgeReach) {
	fmt.Fprintf(w, "%-18s %-6s %-30s %-10s %s\n", "name", "kind", "capabilities", "transport", "last seen")
	for _, n := range nodes {
		via := reach[n.ID].Via
		if via == "" {
			via = "-"
		}
		fmt.Fprintf(w, "%-18s %-6s %-30s %-10s %s\n",
			n.Name, n.Kind, edgeCapsLabel(n), via, edgeSeenLabel(n))
	}
}

// edgeJSONNode is the scriptable shape. It carries what a script needs to address a node
// and NOTHING that would be worth stealing: no pin, no certificate fingerprint, no
// token, no key. The fields are contract - add, never rename.
type edgeJSONNode struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Kind         string          `json:"kind"`
	Capabilities []edgeJSONCap   `json:"capabilities"`
	Transports   []edgeJSONRoute `json:"transports"`
	LastSeen     int64           `json:"last_seen"`
	State        string          `json:"state"`
}

type edgeJSONCap struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type edgeJSONRoute struct {
	Kind string `json:"kind"`
	Addr string `json:"addr,omitempty"`
	// InUse marks the transport that actually carried this listing's probe.
	InUse bool `json:"in_use,omitempty"`
}

// edgeWriteJSON prints the array. Candidates are in it too, told apart by state, so a
// script gets one stable array rather than two shapes to reconcile.
func edgeWriteJSON(w io.Writer, nodes, cands []store.EdgeNode, reach map[string]edgeReach) error {
	out := make([]edgeJSONNode, 0, len(nodes)+len(cands))
	for _, n := range append(append([]store.EdgeNode{}, nodes...), cands...) {
		j := edgeJSONNode{ID: n.ID, Name: n.Name, Kind: n.Kind, LastSeen: n.LastSeen,
			State: n.Presence, Capabilities: []edgeJSONCap{}, Transports: []edgeJSONRoute{}}
		if j.State == "" {
			j.State = string(edge.PresenceVerified)
		}
		for _, c := range n.Caps {
			j.Capabilities = append(j.Capabilities, edgeJSONCap{Name: c.Name, State: c.State})
		}
		for _, t := range n.Transports {
			j.Transports = append(j.Transports, edgeJSONRoute{
				Kind: t.Kind, Addr: t.Addr, InUse: reach[n.ID].Via == t.Kind})
		}
		out = append(out, j)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// --- resolving a node the owner named -------------------------------------

// edgeResolve finds the node an owner meant: an exact name first, then an unambiguous id
// prefix. An ambiguous prefix is refused with the matches listed - never guessed at,
// because the next command in the script would act on the wrong machine.
func edgeResolve(nodes []store.EdgeNode, want string) (store.EdgeNode, bool, error) {
	for _, n := range nodes {
		if n.Name == want {
			return n, true, nil
		}
	}
	var hits []store.EdgeNode
	for _, n := range nodes {
		if want != "" && strings.HasPrefix(n.ID, want) {
			hits = append(hits, n)
		}
	}
	switch len(hits) {
	case 0:
		return store.EdgeNode{}, false, nil
	case 1:
		return hits[0], true, nil
	}
	var lines []string
	for _, n := range hits {
		lines = append(lines, fmt.Sprintf("  %s  %s", n.ID, n.Name))
	}
	sort.Strings(lines)
	return store.EdgeNode{}, false, fmt.Errorf("%q matches more than one node on this Edge:\n%s\nname one of them exactly",
		want, strings.Join(lines, "\n"))
}

// edgeLookup loads the Edge and finds the node the owner named. Its four callers differ
// only in what they do when it is NOT there - describe and name refuse, forget shrugs,
// adopt looks in the candidates instead - so the loading and the resolving live here once.
func edgeLookup(want string) (*edgeState, store.EdgeNode, bool, error) {
	st, err := loadEdgeState()
	if err != nil {
		return nil, store.EdgeNode{}, false, err
	}
	nodes, err := st.list()
	if err != nil {
		return nil, store.EdgeNode{}, false, err
	}
	n, ok, err := edgeResolve(nodes, want)
	return st, n, ok, err
}

// errNoSuchNode is the ONE answer to "that is not on this Edge", whether the node does
// not exist at all or belongs to somebody else. It names nothing about the other account:
// a message that distinguished the two would turn `roger edge describe` into an oracle
// for whether an id is enrolled somewhere.
func errNoSuchNode(want string) error {
	return fmt.Errorf("no such node on this Edge: %q (run `roger edge list`)", want)
}

// --- describe -------------------------------------------------------------

func cmdEdgeDescribe(cfg config, args []string) error {
	leaf, _ := edgeLeaf("describe")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	st, n, ok, err := edgeLookup(argv.pos[0])
	if err != nil {
		return err
	}
	if !ok {
		return errNoSuchNode(argv.pos[0])
	}
	r := edgeReachOf(cfg, n)
	edgeWriteDetail(os.Stdout, st, n, r, argv.has("verbose"))
	return nil
}

// edgeWriteDetail prints one node in full. The shape is the same whichever transport
// reached it; only --verbose adds the lines that say which one did.
func edgeWriteDetail(w io.Writer, st *edgeState, n store.EdgeNode, r edgeReach, verbose bool) {
	fmt.Fprintln(w, n.Name)
	fmt.Fprintf(w, "  id            %s\n", n.ID)
	fmt.Fprintf(w, "  kind          %s\n", n.Kind)
	fmt.Fprintf(w, "  capabilities  %s\n", edgeDetailCaps(st, n))
	fmt.Fprintf(w, "  transports    %s\n", edgeDetailTransports(n))
	fmt.Fprintf(w, "  presence      %s\n", edgePresence(n))
	if why := edgePresenceReason(n); why != "" {
		fmt.Fprintf(w, "  because       %s\n", why)
	}
	fmt.Fprintf(w, "  last seen     %s\n", edgeAge(n.LastSeen))
	if !verbose {
		return
	}
	for _, a := range r.Attempts {
		fmt.Fprintf(w, "  %s\n", a)
	}
	if r.Via == "" {
		fmt.Fprintln(w, "  nothing answered: this is what was last known about it")
		return
	}
	fmt.Fprintf(w, "  carried over %s (%s)\n", r.Via, r.Addr)
}

func edgePresence(n store.EdgeNode) string {
	if n.Presence == "" {
		return string(edge.PresenceVerified)
	}
	return n.Presence
}

// edgePresenceReason is WHY a node is not simply verified, in the words that were
// recorded when it stopped being. A state with no reason is a state the owner cannot
// act on.
func edgePresenceReason(n store.EdgeNode) string {
	if n.Presence != string(edge.PresenceReenroll) {
		return ""
	}
	for i := len(n.History) - 1; i >= 0; i-- {
		if n.History[i].What == "re-enrollment required" {
			return n.History[i].Detail
		}
	}
	return "the Edge authority changed"
}

// edgeDetailCaps spells a claimed capability out with HOW it would be verified, so an
// owner can see why a claim has not become a capability.
func edgeDetailCaps(st *edgeState, n store.EdgeNode) string {
	if len(n.Caps) == 0 {
		return "none declared"
	}
	var out []string
	for _, c := range n.Caps {
		if c.State == string(edge.Verified) {
			out = append(out, c.Name+" (verified)")
			continue
		}
		how, err := st.fleet.VerificationMethod(n.ID, edge.Capability(c.Name))
		if err != nil || how == "" {
			out = append(out, edgeCapLabel(c))
			continue
		}
		out = append(out, fmt.Sprintf("%s (%s: %s)", c.Name, strings.ToLower(c.State), how))
	}
	return strings.Join(out, ", ")
}

func edgeDetailTransports(n store.EdgeNode) string {
	if len(n.Transports) == 0 {
		return "none known"
	}
	var out []string
	for _, t := range n.Transports {
		if t.Addr == "" {
			out = append(out, t.Kind)
			continue
		}
		out = append(out, t.Kind+" "+t.Addr)
	}
	return strings.Join(out, ", ")
}

// --- name -----------------------------------------------------------------

func cmdEdgeName(cfg config, args []string) error {
	leaf, _ := edgeLeaf("name")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	st, n, ok, err := edgeLookup(argv.pos[0])
	if err != nil {
		return err
	}
	if !ok {
		return errNoSuchNode(argv.pos[0])
	}
	nodes, err := st.list()
	if err != nil {
		return err
	}
	want := argv.pos[1]
	if n.Name == want {
		fmt.Printf("%s is already named %s - nothing to do.\n", edgeShortID(n.ID), want)
		return nil
	}
	for _, other := range nodes {
		if other.Name == want && other.ID != n.ID {
			return fmt.Errorf("the name %q is already taken on this Edge: %s (%s) already holds it",
				want, other.Name, edgeShortID(other.ID))
		}
	}
	if err := st.fleet.Rename(n.ID, want); err != nil {
		return err
	}
	if err := st.save(); err != nil {
		return err
	}
	fmt.Printf("%s is now %s.\n", edgeShortID(n.ID), want)
	return nil
}

// --- forget ---------------------------------------------------------------

// edgeStdin is where a confirmation is read from. A seam only so the spec can answer the
// prompt; production reads the terminal.
var edgeStdin io.Reader = os.Stdin

func cmdEdgeForget(cfg config, args []string) error {
	leaf, _ := edgeLeaf("forget")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	st, n, ok, err := edgeLookup(argv.pos[0])
	if err != nil {
		return err
	}
	if !ok {
		// Forgetting something that is not there is what the owner wanted anyway.
		fmt.Printf("nothing to forget: %q is not on this Edge.\n", argv.pos[0])
		return nil
	}
	if !argv.has("yes") {
		fmt.Printf("forget %s (%s)? it leaves this Edge and its pinned certificate is cleared [y/N]: ",
			n.Name, edgeShortID(n.ID))
		if !edgeConfirmed() {
			fmt.Println("left alone.")
			return nil
		}
	}
	// The certificate goes too. A node whose pin is merely cleared could come straight
	// back; a node whose certificate is revoked is refused by every peer that holds the
	// list, on a LAN with no internet, and after a restart.
	if err := edgeRevokeOnForget(n.ID, n); err != nil {
		return err
	}
	if err := st.fleet.Forget(n.ID); err != nil {
		return err
	}
	if err := st.save(); err != nil {
		return err
	}
	fmt.Printf("forgot %s - it is off this Edge, its certificate is revoked and its pin is cleared.\n", n.Name)
	return nil
}

// edgeConfirmed reads one line and requires a deliberate yes. Anything else - including
// end of input, which is what a script that was not asked gives - is a no.
func edgeConfirmed() bool {
	b := make([]byte, 1)
	var line []byte
	for {
		n, err := edgeStdin.Read(b)
		if n > 0 {
			if b[0] == '\n' {
				break
			}
			line = append(line, b[0])
		}
		if err != nil {
			break
		}
		if len(line) > 16 {
			break
		}
	}
	switch strings.ToLower(strings.TrimSpace(string(line))) {
	case "y", "yes":
		return true
	}
	return false
}

// --- adopt ----------------------------------------------------------------

// edgeDialPeer goes and looks at the certificate a candidate actually serves. Production
// is the real pinned TLS dial from internal/edge.
var edgeDialPeer = edge.DialTLS

func cmdEdgeAdopt(cfg config, args []string) error {
	leaf, _ := edgeLeaf("adopt")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	want := argv.pos[0]
	st, n, ok, err := edgeLookup(want)
	if err != nil {
		return err
	}
	if ok {
		fmt.Printf("%s is already a member of this Edge - nothing to do.\n", n.Name)
		return nil
	}
	c, found, err := edgeResolve(st.candidates, want)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no candidate %q was seen on this network (run `roger edge scan`)", want)
	}
	n, err = edgeAdoptCandidate(st, c, c.Name, want)
	if err != nil {
		return err
	}
	fmt.Printf("adopted %s (%s) - it has no capabilities until it declares them.\n", n.Name, edgeShortID(n.ID))
	return nil
}

// edgeAdoptCandidate is the ONE adoption path, shared by the command leaf and the TUI's
// `a` on the [3] EDGE screen, so both check the same things in the same order and both
// leave the same Edge behind: dial the candidate, compare what it SERVES against what it
// ADVERTISED, enroll it with no capabilities, drop it from the candidate list and write
// the cache. label is what the owner called it, so a refusal names the thing they asked
// for rather than an id they never typed.
func edgeAdoptCandidate(st *edgeState, c store.EdgeNode, name, label string) (store.EdgeNode, error) {
	none := store.EdgeNode{}
	if st.account == "" {
		return none, fmt.Errorf("adopting a node into an Edge requires a login (run `roger login`)")
	}
	// The advertisement is a hint; the certificate is the proof. Look before adopting,
	// and refuse a candidate that does not serve what it advertised.
	addr := edge.LANAddr(c)
	if addr == "" {
		return none, fmt.Errorf("%s has no LAN address to check (run `roger edge scan`)", label)
	}
	peer, err := edgeDialPeer(context.Background(), addr)
	if err != nil {
		return none, fmt.Errorf("could not reach %s at %s: %w", label, addr, err)
	}
	if peer.Fingerprint != c.Pin {
		return none, fmt.Errorf("refused %s: %s - it advertised %s but serves %s; nothing was added",
			label, edge.ReasonFingerprintMismatch, edgeShortFP(c.Pin), edgeShortFP(peer.Fingerprint))
	}
	// A candidate becomes a member with NO capabilities: what it can do is something it
	// declares and the fleet verifies, not something adoption grants.
	c.Caps = nil
	if name != "" {
		c.Name = name
	}
	c.Presence = string(edge.PresenceVerified)
	c.LastSeen = time.Now().Unix()
	n, err := st.fleet.Enroll(c)
	if err != nil {
		return none, err
	}
	st.candidates = edgeFilter(st.candidates, func(x store.EdgeNode) bool { return x.ID != c.ID })
	if err := st.save(); err != nil {
		return n, err
	}
	return n, nil
}

func edgeShortFP(fp string) string {
	if len(fp) <= 12 {
		return fp
	}
	return fp[:12] + "..."
}

// --- scan -----------------------------------------------------------------

// edgeDiscoveryOptions builds what one `roger edge scan` pass runs on. It is a seam ONLY
// so the spec can put real peers on a loopback packet bus (Linux `lo` carries no
// MULTICAST flag); production wires the real multicast plane.
var edgeDiscoveryOptions = defaultEdgeDiscoveryOptions

func defaultEdgeDiscoveryOptions(f *edge.Fleet) edge.Options {
	cfg := edge.ConfigFromEnv(nil)
	// ONE pass, driven by this command, rather than the daemon's ticker: a scan is a
	// thing the owner asked for once.
	cfg.ManualPasses = true
	o := edge.Options{Fleet: f, Config: cfg}
	// The authority is whatever THIS machine was enrolled under - Core's root or a
	// designated machine's, indistinguishably. A machine that has not enrolled holds
	// none, so a peer cannot be certificate-verified and the scan says so rather than
	// pretending.
	if auth, _, err := edgeIdentityStore().Trust(time.Now()); err == nil {
		o.Authority = auth
	}
	return o
}

func cmdEdgeScan(cfg config, args []string) error {
	leaf, _ := edgeLeaf("scan")
	if _, err := parseEdgeArgv(leaf, args); err != nil {
		return err
	}
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	opts := edgeDiscoveryOptions(st.fleet)
	d := edge.New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		return err
	}
	defer d.Stop()
	fmt.Println("scanning this network for Roger nodes...")
	rep := d.RunOnce(ctx)
	cands := d.Candidates()

	for _, s := range rep.Verified {
		fmt.Printf("  verified   %s  %s  at %s\n", s.NodeID, s.Name, s.Addr)
	}
	for _, f := range rep.Refusals {
		fmt.Printf("  refused    %s  at %s: %s (advertised %s, served %s)\n",
			f.NodeID, f.Addr, f.Reason, edgeShortFP(f.Advertised), edgeShortFP(f.Observed))
	}
	for _, c := range cands {
		fmt.Printf("  candidate  %s  %s  at %s\n", c.ID, c.Kind, edge.LANAddr(c))
		fmt.Printf("             adopt it: roger edge adopt %s\n", c.ID)
	}
	for _, n := range rep.Notes {
		if n == "no peers found" {
			continue // said once, below, in this command's own words
		}
		fmt.Printf("  %s\n", n)
	}
	if len(rep.Verified) == 0 && len(rep.Refusals) == 0 && len(cands) == 0 {
		fmt.Println("nothing answered on this network.")
	}
	if opts.Authority == nil && (len(rep.Refusals) > 0 || len(cands) > 0) {
		fmt.Println("  (this machine holds no Edge authority yet, so a member's certificate cannot be checked here)")
	}
	if st.account == "" && len(cands) > 0 {
		fmt.Println("  (adopting a candidate requires a login: run `roger login`, then `roger edge adopt <node>`)")
	}
	st.candidates = edgeMergeCandidates(st.candidates, cands)
	if err := st.save(); err != nil {
		return err
	}
	return nil
}

// edgeMergeCandidates keeps what this pass saw, preferring the fresh record for a
// candidate seen again. A candidate not seen this pass is kept: one missed multicast
// answer is not proof a machine went away, and the TTL is what forgets it.
func edgeMergeCandidates(old, fresh []store.EdgeNode) []store.EdgeNode {
	seen := map[string]bool{}
	out := make([]store.EdgeNode, 0, len(old)+len(fresh))
	for _, c := range fresh {
		seen[c.ID] = true
		out = append(out, c)
	}
	for _, c := range old {
		if !seen[c.ID] {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
