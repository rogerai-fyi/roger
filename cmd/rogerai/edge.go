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
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/look"
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
	{"list", "[--tag <tag>]", "show every node on this Edge, or one tagged group", 0, 0},
	{"describe", "<node>", "show one node in full", 1, 1},
	{"name", "<node>|. <name>", "give a node a name the owner chooses", 2, 2},
	{"forget", "<node>", "remove a node from this Edge", 1, 1},
	{"tag", "<node> [+tag|-tag|tag...] [--clear]", "show or change the owner's words for a unit: a group a rule can cover", 1, 14},
	{"nearby", "", "list the devices pinging on this network that are not on your Edge yet", 0, 0},
	{"approve", "<id>", "let a nearby device onto your Edge: a phone by claim, a roger device verified and enrolled", 1, 1},
	{"adopt", "<candidate>", "take a discovered candidate into the fleet", 1, 1}, // the old name for approve, kept one release (§7)
	{"readmit", "[<node>]", "bring back a device you forgot or revoked (lifts its revocation); alone, lists them", 0, 1},
	{"scan", "", "look for nodes on this network now", 0, 0},
	{"sessions", "", "show the live sessions on this machine's Edge", 0, 0},
	{"prefer", "[local|market|local-only]", "choose where a turn goes first, or show it", 0, 1},
	{"bands", "", "show the models this Edge can serve locally, and who serves each", 0, 0},
	{"health", "[<node>]", "show the fleet's health at a glance, or one node in full", 0, 1},
	{"fleet", "[--select <node>] [--health]", "open the live fleet screen: the map, the readouts, the actions; --health opens the unit's health", 0, 0},
	{"trouble", "[<node>] [--since <cursor>] [--json|--watch]", "read a member's trouble feed, as the phone does; --watch follows it", 0, 1},
	{"inventory", "[--json]", "list every unit's software: roger version, OS, kernel, arch, machine, and who is behind", 0, 0},
	{"events", "[--node <node>] [--since <dur>] [--limit <n>] [--json]", "what happened on this Edge, newest first", 0, 0},
	{"expect", "[<what> [value] [--scope fleet|tag:<tag>|<node>] | rm <what>]", "say what this Edge should look like: roger <version>, serves <band>, agent, maintenance", 0, 2},
	{"drift", "[--json]", "where this Edge does not match what you expect of it", 0, 0},
	{"schedule", "[add <start|stop|restart> <agent> [--scope ...] [--every <dur>|--once] | rm <id>]", "leave a maintenance action standing, run on this machine's own pass", 0, 3},
	{"alert", "[set <metric> <gt|lt> <value> [--scope fleet|<node>|tag:<tag>] | off|on|rm <rule> | test <sink>]", "list the alert rules and what is firing, or change them", 0, 4},
	{"sink", "[add <webhook|discord|telegram> <url> | rm <id> | test <id>]", "say where an alert should go when it leaves this machine, or show what is configured", 0, 3},
	{"actuate", "<node> [--yes]", "confirm that a node may change the physical world - the one capability only you can grant", 1, 1},
	{"enroll", "[name] [--authority <address>] [--code <code> | --root sha256:<fingerprint> | --invite <invite>] [--replace-root] [--yes]", "join this machine to your Edge", 0, 1},
	{"setup", "", "set up this machine's Edge interactively (new, or join one)", 0, 0},
	{"run", "", "keep this machine on its Edge with no screen, until stopped (what the boot service runs)", 0, 0},
	{"service", "[install|remove]", "make roger start at boot, say whether it does, or stop it (systemd)", 0, 1},
	{"agent", "[on|off|resume <name>|remove <name>]", "run this roger as an agent, or resume/remove a persistent one", 0, 2},
	{"maintenance", "[on|off]", "let members with a maintain grant start, stop or restart an agent here and read this machine's log (off by default)", 0, 1},
	{"serve-control", "[on|off]", "let members with a serve-control grant change which model this machine serves (off by default)", 0, 1},
	{"maintain", "<node> start <agent>", "ask a member to start one of its persistent agents, under a maintain grant", 3, 3},
	{"logs", "<node> [--lines <n>]", "read a member's recent log lines, redacted, under a maintain grant", 1, 1},
	{"model", "<agent> <model> [use|share|use,share]", "bind a model to an agent, to use or share on the Edge", 2, 3},
	{"job", "<agent> <serve|watch|relay|none> [targets...]", "give an agent a job, or clear it", 2, 16},
	{"authority", "[local <name>|core|allow <key>]", "show or choose what roots this Edge", 0, 2},
	{"invite", "[--for <node id>] [--at <address>:<port>] [--qr]", "print one `roger join` line that adds a device to this Edge, once; --for binds it to one device", 0, 0},
	{"grant", "[<caller> <cap> <callee> --for 7d | revoke <id>]", "issue, list or revoke an authority-signed grant", 0, 16},
	{"invoke", "<callee> <prompt...> [--cap operate] [--model <band>]", "ask another node to act, under a grant", 1, 32},
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
		use := strings.TrimSpace("roger edge " + c.name + " " + c.args)
		if len(use) >= 30 {
			// A long usage keeps its own line; the purpose goes under it, in the purpose column.
			b.WriteString(fmt.Sprintf("  %s\n  %-30s%s\n", use, "", c.purpose))
			continue
		}
		b.WriteString(fmt.Sprintf("  %-30s%s\n", use, c.purpose))
	}
	b.WriteString(`
  list    --json  --capability <cap>  --dark  --candidates
  sessions --json
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
	case "tag":
		return cmdEdgeTag(cfg, rest)
	case "nearby":
		return cmdEdgeNearby(cfg, rest)
	case "approve":
		return cmdEdgeApprove(cfg, rest)
	case "adopt":
		return cmdEdgeAdopt(cfg, rest)
	case "readmit":
		return cmdEdgeReadmit(cfg, rest)
	case "sessions":
		return cmdEdgeSessions(cfg, rest)
	case "prefer":
		return cmdEdgePrefer(cfg, rest)
	case "bands":
		return cmdEdgeBands(cfg, rest)
	case "health":
		return cmdEdgeHealth(cfg, rest)
	case "fleet":
		return cmdEdgeFleet(cfg, rest)
	case "trouble":
		return cmdEdgeTrouble(cfg, rest)
	case "inventory":
		return cmdEdgeInventory(cfg, rest)
	case "events":
		return cmdEdgeEvents(cfg, rest)
	case "expect":
		return cmdEdgeExpect(cfg, rest)
	case "drift":
		return cmdEdgeDrift(cfg, rest)
	case "schedule":
		return cmdEdgeSchedule(cfg, rest)
	case "alert":
		return cmdEdgeAlert(cfg, rest)
	case "sink":
		return cmdEdgeSink(cfg, rest)
	case "actuate":
		return cmdEdgeActuate(cfg, rest)
	case "enroll":
		return cmdEdgeEnroll(cfg, rest)
	case "setup":
		return cmdEdgeSetup(cfg, rest)
	case "agent":
		return cmdEdgeAgent(cfg, rest)
	case "maintenance":
		return cmdEdgeMaintenance(cfg, rest)
	case "serve-control":
		return cmdEdgeServeControl(cfg, rest)
	case "run":
		return cmdEdgeRun(cfg, rest)
	case "service":
		return cmdEdgeService(cfg, rest)
	case "maintain":
		return cmdEdgeMaintain(cfg, rest)
	case "logs":
		return cmdEdgeLogs(cfg, rest)
	case "model":
		return cmdEdgeModel(cfg, rest)
	case "job":
		return cmdEdgeJob(cfg, rest)
	case "authority":
		return cmdEdgeAuthority(cfg, rest)
	case "invite":
		return cmdEdgeInvite(cfg, rest)
	case "grant":
		return cmdEdgeGrant(cfg, rest)
	case "invoke":
		return cmdEdgeInvoke(cfg, rest)
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
	"json": false, "dark": false, "candidates": false, "yes": false, "verbose": false, "live": false, "watch": false,
	"force": false, "clear": false, "replace-root": false, "qr": false,
	"code": true, "root": true, "invite": true, "at": true, "for": true,
	"capability": true, "authority": true, "account": true, "scope": true, "since": true, "tag": true,
	"node": true, "limit": true, "lines": true, "every": true, "once": false, "select": true, "health": false,
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
	// sessions is this run's live session ledger: the traffic this Edge carried, as the
	// relay's own receipts describe it. It is deliberately NOT part of the snapshot -
	// sessions fade rather than accumulate, so a session that outlived the process it
	// happened in would be a lie about what is happening now.
	sessions *edge.Sessions
	// savedAt is the file's mtime after this state's last save; a newer mtime is another
	// process's edit (the fleet screen, a CLI forget), which is adopted before the next write.
	// base is THE MERGE BASE: every node exactly as this state last read or wrote it. It is what
	// lets a save tell three cases apart - what THIS process changed, what another process changed,
	// and what nobody touched - so a write merges instead of overwriting. It is filled on LOAD as
	// well as on save, because a short-lived CLI's base is the file it read.
	savedAt time.Time
	base    map[string]store.EdgeNode
	// candBase is the candidate ids as last read or written, which is what tells a deliberate drop
	// from a candidate this process has simply never seen.
	candBase map[string]bool
}

// WHAT A MERGE HAS TO KNOW. A node record carries two completely different kinds of field, and the
// first version of this merge treated them alike, which lost data in the very way it was written to
// prevent (review 2026-09-29):
//
//   - INTENT is what an OWNER decided: the name they chose, the words they grouped it by, the
//     capabilities it declares, its classify contract, its pinned certificate. It changes only when
//     somebody says so, so "this process changed it" is a meaningful claim about intent.
//
//   - OBSERVATION is what a MACHINE saw: when it was last heard, whether it is presently dark, the
//     addresses it answered on, the rogers it reported running. It changes on its own, on every
//     pass, in every process that can see the node. "This process changed it" says nothing.
//
// Comparing whole records made every heartbeat look like an edit, so the host - which sees every
// node on every pass - claimed the whole record every time and overwrote a rename typed in another
// terminal, and brought back nodes that had just been forgotten. Intent is therefore merged FIELD BY
// FIELD against the base, and observation is merged FRESHEST-WINS by what each side last heard.
//
// The simpler shape (mutate inside the lock against a freshly read state, and have no base at all)
// was considered and does not fit: the host holds a long-lived view that it updates from discovery
// and probes between saves, and holding this lock across that work would put network calls inside
// it. So the merge stays, and it is narrow and field-grained rather than whole-record.

// edgeSameIntent reports whether the owner-decided fields of two records are the same.
func edgeSameIntent(a, b store.EdgeNode) bool {
	return a.Name == b.Name && a.Kind == b.Kind && a.Pin == b.Pin &&
		edgeSameStrings(a.Tags, b.Tags) && edgeSameJSON(a.Caps, b.Caps) && edgeSameJSON(a.Contract, b.Contract)
}

// edgeSameJSON compares two values by their JSON form. Caps and the classify contract both carry
// slices, so Go will not compare them, and their JSON is exactly "the same declaration" for a
// merge: it is also the form they arrived in and the form they will be written back as.
func edgeSameJSON(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false // unmarshalable is treated as different, so an edit is kept rather than lost
	}
	return string(x) == string(y)
}

func edgeSameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// edgeBaseOf is the merge base for a set of nodes as read or written.
//
// EVERY SLICE IS COPIED, and that is the whole point of this function rather than a plain map
// assignment. A node record carries slices, and a shallow copy SHARES their backing array: an edit
// made in place - which is what confirming a capability is - would change the base as well, the
// merge would compare the edit against itself, conclude nothing had changed, and take the disk's
// older version. The edit would vanish with no error anywhere, which is precisely the failure this
// merge exists to prevent.
func edgeBaseOf(nodes []store.EdgeNode) map[string]store.EdgeNode {
	out := make(map[string]store.EdgeNode, len(nodes))
	for _, n := range nodes {
		out[n.ID] = edgeCloneNode(n)
	}
	return out
}

// edgeCloneNode is a node record that shares nothing with the one it came from.
func edgeCloneNode(n store.EdgeNode) store.EdgeNode {
	n.Caps = append([]store.EdgeCap(nil), n.Caps...)
	n.Tags = append([]string(nil), n.Tags...)
	n.Transports = append([]store.EdgeTransport(nil), n.Transports...)
	n.History = append([]store.EdgeEvent(nil), n.History...)
	n.Instances = append([]store.EdgeInstance(nil), n.Instances...)
	n.Contract.Labels = append([]string(nil), n.Contract.Labels...)
	return n
}

// edgeMergeOne merges ONE node that both sides hold. Each owner-decided field goes to whoever
// changed it since the base; everything a machine merely saw goes to whoever saw it more recently;
// the event history is the union, because an event is a thing that happened and neither side's
// account of it is wrong.
func edgeMergeOne(base, mine, theirs store.EdgeNode, inBase bool) store.EdgeNode {
	out := theirs
	// INTENT, field by field: mine only where I changed it.
	if !inBase || mine.Name != base.Name {
		out.Name = mine.Name
	}
	if !inBase || mine.Kind != base.Kind {
		out.Kind = mine.Kind
	}
	if !inBase || mine.Pin != base.Pin {
		out.Pin = mine.Pin
	}
	if !inBase || !edgeSameStrings(mine.Tags, base.Tags) {
		out.Tags = mine.Tags
	}
	if !inBase || !edgeSameJSON(mine.Caps, base.Caps) {
		out.Caps = mine.Caps
	}
	if !inBase || !edgeSameJSON(mine.Contract, base.Contract) {
		out.Contract = mine.Contract
	}
	// OBSERVATION: whoever heard from it last. A tie goes to this process, whose sighting is at
	// least as fresh and whose transports are the ones it just dialled.
	if mine.LastSeen >= theirs.LastSeen {
		out.LastSeen, out.Presence = mine.LastSeen, mine.Presence
		out.Transports, out.Station = mine.Transports, mine.Station
		out.Instances, out.InstancesTruncated = mine.Instances, mine.InstancesTruncated
		out.Phone = mine.Phone // a phone's vitals are an observation too
	}
	out.History = edgeMergeHistory(mine.History, theirs.History)
	return out
}

// edgeMergeHistory is the union of two event lists, oldest first, bounded the way the record is.
// An event is a fact about the past: a process that did not see it has not decided it did not
// happen, so nothing is dropped for being on only one side.
func edgeMergeHistory(a, b []store.EdgeEvent) []store.EdgeEvent {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	seen := make(map[store.EdgeEvent]bool, len(a)+len(b))
	out := make([]store.EdgeEvent, 0, len(a)+len(b))
	for _, ev := range append(append([]store.EdgeEvent{}, a...), b...) {
		if seen[ev] {
			continue
		}
		seen[ev] = true
		out = append(out, ev)
	}
	sort.SliceStable(out, func(i, k int) bool { return out[i].At < out[k].At })
	if len(out) > edge.MaxNodeHistory {
		out = out[len(out)-edge.MaxNodeHistory:]
	}
	return out
}

// edgeMergeNodes is the three-way merge that makes edge.json safe to share. theirs is the file as
// it stands NOW, mine is this process's memory, base is the file as this process last saw it.
//
//   - held by both                                    -> merged field by field (edgeMergeOne)
//   - added here since the base                       -> kept, the disk has not seen it yet
//   - added on the disk since the base                -> kept, adopted into this process
//   - in the base but gone from memory                -> dropped, this process forgot it and means it
//   - in the base, gone from the disk, untouched here  -> stays gone, forgotten elsewhere
//   - in the base, gone from the disk, INTENT changed here -> comes back
//
// That last case is the one asymmetry, and it is now narrow enough to defend: it fires only when
// this process really did change something an owner decided, never on a heartbeat. Before the review
// it fired on any sighting, so an ordinary pass could resurrect a node whose certificate had just
// been revoked.
func edgeMergeNodes(base map[string]store.EdgeNode, mine, theirs []store.EdgeNode) []store.EdgeNode {
	out := make(map[string]store.EdgeNode, len(theirs)+len(mine))
	order := make([]string, 0, len(theirs)+len(mine))
	add := func(n store.EdgeNode) {
		if _, had := out[n.ID]; !had {
			order = append(order, n.ID)
		}
		out[n.ID] = n
	}
	onDisk := make(map[string]store.EdgeNode, len(theirs))
	for _, n := range theirs {
		onDisk[n.ID] = n
		add(n)
	}
	held := make(map[string]bool, len(mine))
	for _, n := range mine {
		held[n.ID] = true
		was, inBase := base[n.ID]
		d, still := onDisk[n.ID]
		switch {
		case still:
			add(edgeMergeOne(was, n, d, inBase))
		case !inBase:
			add(n) // added here: the disk has not seen it yet
		case !edgeSameIntent(n, was):
			add(n) // forgotten elsewhere, but an owner changed something here: keep the change
		}
	}
	for id := range base {
		if !held[id] {
			delete(out, id) // forgotten HERE, on purpose
		}
	}
	merged := make([]store.EdgeNode, 0, len(out))
	for _, id := range order {
		if n, ok := out[id]; ok {
			merged = append(merged, n)
		}
	}
	return merged
}

// edgeCandidatesToWrite is the candidate list a save writes, merged the same three ways the nodes
// are. Candidates are discovery's own output and they fade on their own, but a write must not lose
// one somebody else just found, and must not RESURRECT one this process deliberately dropped.
//
// A drop is deliberate and there are three of them, all elsewhere and all with their own reasons:
// a candidate this authority has granted a claim to is ADOPTING rather than on offer
// (edgeDropGranted), a LIVE member re-advertising is already on the Edge (edgeDropMembers), and a
// candidate past the discovery TTL has gone (edgeMergeCandidates). None of those rules is repeated
// here: this function only decides whose list to believe about each id.
//
//   - held here                      -> kept, whatever the disk says
//   - on the disk and never seen here -> kept, another process found it
//   - on the disk, seen here at load, and dropped here since -> stays dropped, that was on purpose
func edgeCandidatesToWrite(mine, theirs []store.EdgeNode, base map[string]bool, nodes []store.EdgeNode) []store.EdgeNode {
	out := make([]store.EdgeNode, 0, len(mine)+len(theirs))
	held := make(map[string]bool, len(mine))
	for _, c := range mine {
		held[c.ID] = true
		out = append(out, c)
	}
	for _, c := range theirs {
		if !held[c.ID] && !base[c.ID] {
			out = append(out, c) // found by another process since this one read the file
		}
	}
	// A LIVE member is not waiting to be adopted, whichever list it came from. This is the one rule
	// repeated here, because a stale process holding a phone that has since been adopted would
	// otherwise put it back on the bench, and the map lands the cursor on whatever is waiting - so
	// the screen would open offering to adopt a device that is already on the Edge. A DARK member
	// stays: that stale record is a device that left, and it must be offered so the owner can
	// re-admit it (the rule edgeDropMembers states).
	live := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if edge.Presence(n.Presence) != edge.PresenceDark {
			live[n.ID] = true
		}
	}
	return edgeFilter(out, func(c store.EdgeNode) bool { return !live[c.ID] })
}

// edgeCandBaseOf is the candidate ids as this state last read or wrote them, so a later drop can be
// told from never having seen it.
func edgeCandBaseOf(cands []store.EdgeNode) map[string]bool {
	out := make(map[string]bool, len(cands))
	for _, c := range cands {
		out[c.ID] = true
	}
	return out
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
	st := &edgeState{account: acct, db: db, fleet: edge.NewFleet(db, acct),
		sessions: edge.NewSessions(acct)}
	// A forgotten device the owner re-admitted comes back under the name it had.
	st.fleet.NameReturning(edgeReturningName)
	// The TUI's and the console's ledger merges what `roger use` processes publish, and
	// publishes its own turns for them: one machine, one view of its sessions.
	st.sessions.Mirror(edgeSessionsDir())
	st.sessions.OnError(func(err error) { log.Println(err) })
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
	// THE FILE AS READ IS THIS STATE'S MERGE BASE. A short-lived roger - one CLI command - has no
	// save behind it, and without a base its write could only be an overwrite of whatever another
	// process had done in the meantime. With it, the write is a merge (edgeMergeNodes).
	st.base = edgeBaseOf(snap.Nodes)
	st.candBase = edgeCandBaseOf(snap.Candidates)
	return st, nil
}

// save writes this state to edge.json as a MERGE, never as an overwrite. The file is shared by every
// roger on the machine, so the read-modify-write happens under an exclusive lock and what the file
// says now is merged with what this process holds (edgeMergeNodes). Before this, a process that had
// loaded the file before somebody else adopted a device wrote that device straight out of existence,
// and the host then forgot it for good - the founder's vanished phone (2026-09-27).
func (s *edgeState) save() error {
	unlock, err := edge.FileLock(edgeStatePath())
	if err != nil {
		return err
	}
	defer unlock()
	if !s.savedAt.IsZero() { // a long-lived state (the host): fold in what changed on disk first
		s.adoptExternalEdits()
	}
	nodes, err := s.db.EdgeNodesOfAccount(s.account)
	if err != nil {
		return err
	}
	disk, _ := s.snapshotOnDisk()
	nodes = edgeMergeNodes(s.base, nodes, disk.Nodes)
	cands := edgeCandidatesToWrite(s.candidates, disk.Candidates, s.candBase, nodes)
	b, err := json.MarshalIndent(edgeSnapshot{
		Account: s.account, Nodes: nodes, Candidates: cands}, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWriteConfig(edgeStatePath(), b); err != nil {
		return err
	}
	if fi, err := os.Stat(edgeStatePath()); err == nil {
		s.savedAt = fi.ModTime()
	}
	// What was just written IS the new base, and it is also what this state now knows: a node another
	// process added, or an edit it made, is brought into memory so the screen beside it agrees with
	// the file. Every merged node is enrolled, not only the absent ones - enrolling only what was
	// missing left memory holding the stale record while the base held the new one, and the next save
	// then undid the edit this one had just adopted.
	s.base = edgeBaseOf(nodes)
	s.candidates, s.candBase = cands, edgeCandBaseOf(cands)
	for _, n := range nodes {
		_, _ = s.db.EnrollEdgeNode(n)
	}
	return nil
}

// snapshotOnDisk is the file as it stands now, and whether there was one to read for this account.
// Called under the lock on the write path, so what it returns cannot change before the write.
func (s *edgeState) snapshotOnDisk() (edgeSnapshot, bool) {
	b, err := os.ReadFile(edgeStatePath())
	if err != nil {
		return edgeSnapshot{}, false
	}
	var snap edgeSnapshot
	if json.Unmarshal(b, &snap) != nil || snap.Account != s.account {
		return edgeSnapshot{}, false
	}
	return snap, true
}

// adoptExternalEdits folds in what another process changed on disk since this state last wrote:
// a member forgotten, renamed or adopted by the fleet screen or by a CLI in a second terminal.
// Without it the next save, written from memory, would undo them.
func (s *edgeState) adoptExternalEdits() {
	fi, err := os.Stat(edgeStatePath())
	if err != nil || !fi.ModTime().After(s.savedAt) {
		return
	}
	mine, err := s.fleet.List()
	if err != nil {
		return
	}
	// The SAME merge the write uses, so memory and the file cannot disagree about the rule. The base
	// is deliberately NOT advanced here - an edit made in memory and not yet written is still this
	// process's edit, and folding it into the base would lose it on the next save.
	disk, _ := s.snapshotOnDisk()
	merged := edgeMergeNodes(s.base, mine, disk.Nodes)
	keep := make(map[string]bool, len(merged))
	for _, n := range merged {
		keep[n.ID] = true
		_, _ = s.db.EnrollEdgeNode(n) // an upsert: a rename or an adopt made elsewhere lands here
	}
	for _, n := range mine {
		if !keep[n.ID] {
			_ = s.fleet.Forget(n.ID)
		}
	}
	s.candidates = edgeCandidatesToWrite(s.candidates, disk.Candidates, s.candBase, merged)
	s.savedAt = fi.ModTime()
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
	if want, ok := argv.flags["tag"]; ok {
		t := edge.NormalizeTag(want)
		if err := edge.ValidTag(t); err != nil {
			return err
		}
		nodes = edgeFilter(nodes, func(n store.EdgeNode) bool { return edge.HasTag(n.Tags, t) })
		cands = nil // a candidate is not on your Edge, so it carries none of your words
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
	filtered := argv.has("dark") || argv.has("candidates") || argv.has("capability") || argv.has("tag")

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
	// An explicit face bind overrides the private-interfaces-only default, so it is named
	// (features/lan/regressions.feature): the owner can see this machine listens where they said.
	if v := os.Getenv(envFaceBind); v != "" {
		fmt.Printf("face bind: %s (explicit, from %s; the default listens on private interfaces only)\n", v, envFaceBind)
	}
	// THE MODE, first: what roots this Edge and where turns go first - two words that
	// must never share a label (features/edge/mode.feature).
	if self := edgeSelfStatus(st.fleet, edgeDiscoveryFactsFromEnv()); self.Err == "" {
		fmt.Printf("EDGE · %s · %s\n", self.RootBadge(), self.PreferBadge())
	}
	if len(nodes) == 0 && len(cands) == 0 {
		if filtered {
			fmt.Println("nothing on this Edge matches that.")
			return nil
		}
		self := edgeSelfStatus(st.fleet, edgeDiscoveryFactsFromEnv())
		if self.Err == "" && !self.SetUp() {
			// Nothing set up: the same three things the TUI and the console say
			// (features/edge/empty_edge_simple.feature §7), then the discovery fact.
			fmt.Println(look.EmptyEdgeCLIHead)
			fmt.Println("  " + look.EmptyEdgeSay)
			fmt.Printf("  %-36s%s\n", look.EmptyEdgeStart+":", look.EmptyEdgeCLIStart)
			fmt.Printf("  %-36s%s\n", look.EmptyEdgeJoin+":", look.EmptyEdgeCLIJoin)
			fmt.Printf("  %-14s%s\n", "DISCOVERY", self.DiscoveryLine(time.Now()))
			return nil
		}
		fmt.Println("your Edge: this machine is the only node.")
		// The three facts that decide what to do next, worded once in internal/edge
		// (features/edge/empty_edge.feature) so the TUI and the console say the same.
		fact := func(label string, lines ...string) {
			for i, l := range lines {
				if i > 0 {
					label = ""
				}
				fmt.Printf("  %-14s%s\n", label, l)
			}
		}
		if self.Err != "" {
			fact("STATUS", "could not be read: "+self.Err)
		} else {
			fact("THIS MACHINE", self.MachineLine())
			fact("AUTHORITY", self.AuthorityLines()...)
			fact("DISCOVERY", self.DiscoveryLine(time.Now()))
		}
		fmt.Println("  find the others on this network:  roger edge scan")
		fmt.Println("  then take one into the fleet:     roger edge adopt <node>")
		fmt.Println("  or enroll another machine against this Edge's authority:")
		fmt.Println("                                    " + self.JoinAgainstLine())
		// The interactive way in - the same wizard the TUI opens on `e` (onboard.feature).
		// It points here rather than replacing the named commands above, which stay for
		// scripts and for anyone who prefers them.
		if !self.Enrolled {
			fmt.Println("  or set it up interactively:       roger edge setup")
		}
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
	// This machine's household rides on its own row when it is a row of its fleet (an
	// authority machine is), and is printed on its own line when it is not.
	if !edgeAttachOwnHousehold(nodes, time.Now()) {
		edgeWriteThisMachine(os.Stdout, st, time.Now())
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

// edgeWriteTable is the fleet view: one row per node, the columns the spec names, and the
// node's running instances indented under it (features/edge/instances.feature).
func edgeWriteTable(w io.Writer, nodes []store.EdgeNode, reach map[string]edgeReach) {
	// The tags column appears only when some node carries one, so an Edge that uses no tags reads
	// exactly as it did.
	tagged := false
	for _, n := range nodes {
		tagged = tagged || len(n.Tags) > 0
	}
	head := "%-18s %-6s %-30s %-10s %s\n"
	if tagged {
		head = "%-18s %-6s %-30s %-10s %-18s %s\n"
		fmt.Fprintf(w, head, "name", "kind", "capabilities", "transport", "tags", "last seen")
	} else {
		fmt.Fprintf(w, head, "name", "kind", "capabilities", "transport", "last seen")
	}
	for _, n := range nodes {
		via := reach[n.ID].Via
		if via == "" {
			via = "-"
		}
		// A node nobody named is shown by its hardware word, never its id (node_identity.feature).
		name := edge.UnitName(n, nodes)
		if n.ID == edgeThisNodeID() {
			name = edge.SelfLabel(n.Name, edgeStationName())
		}
		if tagged {
			fmt.Fprintf(w, head, name, n.Kind, edgeCapsLabel(n), via, edgeTagsLabel(n), edgeSeenLabel(n))
		} else {
			fmt.Fprintf(w, head, name, n.Kind, edgeCapsLabel(n), via, edgeSeenLabel(n))
		}
		edgeWriteInstances(w, n.Instances, n.InstancesTruncated)
	}
}

// edgeWriteInstances is the household under a node: name, what it provides, what it serves.
func edgeWriteInstances(w io.Writer, insts []edge.Instance, truncated bool) {
	for _, in := range insts {
		fmt.Fprintf(w, "  %-16s %-6s %-30s %s\n", "/"+in.Name, "roger", edgeInstanceCapsLabel(in), edgeInstanceServes(in))
		if words := edge.SoftwareWords(in.Software); words != "" {
			fmt.Fprintf(w, "  %-16s %s\n", "", words) // the inventory line (features/edge/inventory.feature)
		}
	}
	if truncated {
		fmt.Fprintf(w, "  %-16s (this node runs more instances than are shown)\n", "")
	}
}

func edgeInstanceCapsLabel(in edge.Instance) string {
	if len(in.Caps) == 0 {
		return "-"
	}
	var out []string
	for _, c := range in.Caps {
		mk := c.Name
		if c.State == string(edge.Verified) {
			mk = strings.ToUpper(mk)
		}
		out = append(out, mk)
	}
	return strings.Join(out, " ")
}

func edgeInstanceServes(in edge.Instance) string {
	if len(in.Bands) == 0 {
		return ""
	}
	return "serves " + strings.Join(in.Bands, ", ")
}

// edgeWriteThisMachine is the household of THIS node, which is not a row in its own fleet:
// the rogers running here right now, from the registrations they keep.
func edgeWriteThisMachine(w io.Writer, st *edgeState, now time.Time) {
	id, _, ok, err := edgeIdentityStore().LoadIdentity()
	if err != nil || !ok {
		return
	}
	name := id.NodeID
	if n, found, err := st.fleet.Get(id.NodeID); err == nil && found && n.Name != "" {
		name = n.Name
	}
	insts := edge.Household(edgeInstancesDir(), now)
	if len(insts) == 0 {
		fmt.Fprintf(w, "this machine: %s · no roger running on it right now\n", name)
		return
	}
	fmt.Fprintf(w, "this machine: %s · %s\n", name, plural(len(insts), "instance"))
	edgeWriteInstances(w, insts, false)
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
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
	// An exact NAME match, but names are not unique - two devices can share "iPhone". More than one
	// is ambiguous and must be refused, not silently resolved to the first, or a destructive verb
	// (forget) could hit the wrong device (audit 2026-09-24).
	var nameHits []store.EdgeNode
	for _, n := range nodes {
		if n.Name == want {
			nameHits = append(nameHits, n)
		}
	}
	if len(nameHits) == 1 {
		return nameHits[0], true, nil
	}
	if len(nameHits) > 1 {
		return store.EdgeNode{}, false, edgeAmbiguous(want, nameHits)
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
	return store.EdgeNode{}, false, edgeAmbiguous(want, hits)
}

// edgeAmbiguous is the refusal when a name or id prefix matches more than one node: it lists them so
// the owner can name one exactly.
func edgeAmbiguous(want string, hits []store.EdgeNode) error {
	var lines []string
	for _, n := range hits {
		lines = append(lines, fmt.Sprintf("  %s  %s", n.ID, n.Name))
	}
	sort.Strings(lines)
	return fmt.Errorf("%q matches more than one node on this Edge:\n%s\nname one of them exactly",
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
	if strings.Contains(argv.pos[0], "/") {
		return cmdEdgeDescribeInstance(argv.pos[0])
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
	if argv.pos[0] == "." {
		return cmdEdgeNameInstance(cfg, argv.pos[1])
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
	was := edgeDisplayName(n.ID) // what it was called, never its id
	if n.Name == want {
		fmt.Printf("%s is already named %s - nothing to do.\n", was, want)
		return nil
	}
	for _, other := range nodes {
		if other.Name == want && other.ID != n.ID {
			return fmt.Errorf("the name %q is already taken on this Edge: %s already holds it", want, other.Name)
		}
	}
	if err := st.fleet.Rename(n.ID, want); err != nil {
		return err
	}
	if err := st.save(); err != nil {
		return err
	}
	fmt.Printf("%s is now %s.\n", was, want)
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
		// A device not in the fleet may still have a live relationship with this authority: a claim
		// GRANT standing (adopted, not yet claimed) OR an issued CERTIFICATE (claimed, not yet
		// checked in) - and a raw node id may be THIS machine's own identity. Resolve what would be
		// forgotten WITHOUT touching anything yet, so the same y/N confirmation the in-fleet path
		// uses gates this destructive action too (revoking a certificate, giving up an identity)
		// (audit 2026-09-24).
		ids, who, err := edgeResolveNotInFleet(argv.pos[0])
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			// Forgetting something that is not there is what the owner wanted anyway.
			fmt.Printf("nothing to forget: %q is not on this Edge.\n", argv.pos[0])
			fmt.Println("  (a device that claimed but has not checked in is forgotten by its node id.)")
			return nil
		}
		if !argv.has("yes") {
			fmt.Printf("forget %s? its claim grant is cleared and any certificate it holds is revoked [y/N]: ", who)
			if !edgeConfirmed() {
				fmt.Println("left alone.")
				return nil
			}
		}
		anyRevoked, anyEvicted := false, false
		for id := range ids {
			// The short record first, by the name its grant carried (node_identity.feature).
			if err := edgeRecordDeparted(store.EdgeNode{ID: id, Name: edgeGrantName(id)}); err != nil {
				return err
			}
			revoked, evictedKey, err := edgeRevokeOnForget(id)
			if err != nil {
				return err
			}
			if len(revoked) > 0 {
				anyRevoked = true
			}
			if evictedKey != "" {
				anyEvicted = true
			}
		}
		if anyRevoked {
			fmt.Printf("forgot %s: its claim grant is cleared and its certificate is revoked.\n", who)
		} else {
			// It had a standing grant but no certificate yet (or nothing this authority issued): the
			// grant is gone, and there is no certificate to revoke.
			fmt.Printf("forgot %s: its claim grant is cleared.\n", who)
		}
		if anyEvicted {
			fmt.Println("  its enrollment key was removed from the allow-list; run `roger edge authority allow <key>` to re-admit a machine under it.")
		}
		return nil
	}
	if !argv.has("yes") {
		fmt.Printf("forget %s? it leaves this Edge and its pinned certificate is cleared [y/N]: ", edgeDisplayName(n.ID))
		if !edgeConfirmed() {
			fmt.Println("left alone.")
			return nil
		}
	}
	msg, err := edgeForgetMember(st, n)
	if err != nil {
		return err
	}
	fmt.Println(msg)
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

// cmdEdgeAdopt is the old name for approve (features/edge/empty_edge_simple.feature §7): it does
// exactly what approve does, and says once what the commands are called now.
func cmdEdgeAdopt(cfg config, args []string) error {
	fmt.Println(look.AdoptDeprecated)
	return cmdEdgeApprove(cfg, args)
}

// edgeAdoptWant adopts the candidate the owner named and returns the line that says what
// happened - shared by the CLI and the fleet screen, so both do exactly the same thing.
func edgeAdoptWant(want string) (string, error) {
	st, n, ok, err := edgeLookup(want)
	if err != nil {
		return "", err
	}
	if ok {
		// A member re-joining by CLAIM (no servable cert, its node KEY proves identity) is RE-ADMITTED
		// below when it is not live. But a LIVE member, or a re-adopt via the SERVING path (which would
		// re-pin from an unauthenticated advert - an identity-takeover risk), is refused here. Whether
		// this is a claim re-admit is known only once the candidate is resolved, so gate precisely: if
		// there is a matching non-serving candidate and the member is not live, fall through; else stop.
		c, cok, _ := edgeResolve(st.candidates, want)
		reAdmitByClaim := cok && c.Pin == "" && edge.Presence(n.Presence) != edge.PresenceVerified
		if !reAdmitByClaim {
			if edge.Presence(n.Presence) != edge.PresenceVerified {
				return fmt.Sprintf("%s is on this Edge but offline and not on the network right now - it re-admits when it re-pings, or run `roger edge readmit %s`.", n.Name, want), nil
			}
			return fmt.Sprintf("%s is already a member of this Edge - nothing to do (forget it first to re-adopt).", n.Name), nil
		}
	}
	c, found, err := edgeResolve(st.candidates, want)
	if err != nil {
		return "", err
	}
	if !found {
		// An adopted device that has not joined yet is not a candidate any more; adopting it again
		// is how the owner gets a fresh code when the first one expired.
		if msg, ok, err := edgeFreshCodeForAdopting(want); err != nil || ok {
			return msg, err
		}
		return "", fmt.Errorf("no candidate %q was seen on this network (run `roger edge scan`)", want)
	}
	// A candidate that advertises no certificate (a phone) joins by CLAIM, not by being dialed.
	if c.Pin == "" {
		if err := edgeAdoptByClaim(c.ID, c.Name); err != nil {
			return "", err
		}
		// Auto-clear: it leaves DISCOVERED at once (it is adopting now, not to-adopt).
		st.candidates = edgeFilter(st.candidates, func(x store.EdgeNode) bool { return x.ID != c.ID })
		_ = st.save()
		name := edge.UnitName(c, append(append([]store.EdgeNode(nil), st.candidates...), c)) // what it is, never its id
		if ok {                                                                              // it was already on this Edge (offline) - this is a RE-ADMIT, which revokes its old cert
			return fmt.Sprintf("re-admitted %s - its previous certificate is revoked; it claims a fresh one and reappears as a member when it checks in.\n%s", name, edgeReadmitCodeLine(c.ID, name)), nil
		}
		return fmt.Sprintf("adopted %s - it can now claim its certificate; it appears as a member when it checks in.\n%s", name, edgeNodeCodeLine(c.ID, name)), nil
	}
	// A serving candidate is verified and enrolled directly; it is NOT granted a claim (a grant that
	// outlives revoke/forget would let a removed node re-claim a fresh certificate - audit 2026-09-23).
	n, err = edgeAdoptCandidate(st, c, c.Name, want)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("adopted %s.", edge.UnitName(n, nil)), nil
}

// edgeForgetMember takes a member off the Edge and returns the line that says what happened -
// shared by the CLI and the fleet screen. The certificate goes too, when THIS machine is the
// authority: a node whose pin is merely cleared could come straight back; a node whose
// certificate is revoked is refused by every peer that holds the list, on a LAN with no
// internet, and after a restart.
func edgeForgetMember(st *edgeState, n store.EdgeNode) (string, error) {
	// The short record that it left is kept FIRST, so a forget never leaves a device gone with no
	// way to bring it back by its name (features/edge/node_identity.feature, RE-ADMITTING).
	if err := edgeRecordDeparted(n); err != nil {
		return "", err
	}
	revoked, evictedKey, err := edgeRevokeOnForget(n.ID)
	if err != nil {
		return "", err
	}
	if err := st.fleet.Forget(n.ID); err != nil {
		return "", err
	}
	if err := st.save(); err != nil {
		return "", err
	}
	_ = edgeAlertLog().ForgetReader(n.ID) // the sink line must not keep naming it
	msg := fmt.Sprintf("forgot %s - it is off this Edge, its certificate is revoked and its pin is cleared.", n.Name)
	if len(revoked) == 0 {
		// This machine did not issue that certificate, so it cannot revoke it - only drop its pin.
		// Say so plainly rather than claim a revocation that did not happen (audit 2026-09-24).
		msg = fmt.Sprintf("forgot %s - it is off this Edge and its pin is cleared; the authority that issued its certificate must revoke it.", n.Name)
	}
	// Only claim a key was removed when one actually was (protected/claim-joined/unrecorded keep it).
	if evictedKey != "" {
		msg += "\n  its enrollment key was removed from the allow-list; run `roger edge authority allow <key>` to re-admit a machine under it."
	}
	return msg, nil
}

// cmdEdgeReadmit is the DELIBERATE re-admission of a device the owner forgot or revoked - the one
// path that lifts a revocation (a routine adopt refuses that). It revokes any certificate the device
// still holds and grants a fresh claim, so it can rejoin with a NEW certificate while a stolen or
// abandoned old copy stays refused. Confirmed, because it undoes a revocation. With no device named
// it lists what can be re-admitted (features/edge/node_identity.feature, RE-ADMITTING).
func cmdEdgeReadmit(cfg config, args []string) error {
	leaf, _ := edgeLeaf("readmit")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	departed, err := edgeLoadDeparted()
	if err != nil {
		return err
	}
	if len(argv.pos) == 0 {
		return edgeListDeparted(st, departed)
	}
	want := argv.pos[0]
	// Resolve the device the owner means. A device this Edge FORGOT is looked for first, by the name
	// it had or its id: a candidate's name is only what a device announces about itself, so a LAN
	// device calling itself "attic" must never be what `readmit attic` re-admits, and a member that
	// has since taken the name must never have its live certificate revoked by it (review
	// 2026-10-02). When a different device carries that name now, the name is refused as ambiguous
	// with the way to pick. Then a re-pinging candidate, then a fleet member, then a raw node id this
	// Edge has no record of. name is what the fresh claim is granted under: never an id.
	now := time.Now()
	gone, goneOK, err := edge.FindDeparted(departed, want, now)
	if err != nil {
		return err
	}
	if goneOK && gone.ID != strings.TrimSpace(want) {
		if err := edgeReadmitNameClash(st, gone, now); err != nil {
			return err
		}
	}
	id, name, question, done := "", "", "", ""
	consequence := "its previous certificate is revoked and it may claim a fresh one to rejoin"
	after := "its old certificate is revoked; it can claim a fresh one and rejoin."
	named := func(n store.EdgeNode) {
		id, name = n.ID, ""
		if !edge.LooksLikeID(n.Name) {
			name = n.Name
		}
		if d, ok, _ := edge.FindDeparted(departed, n.ID, time.Now()); ok {
			// It is a device this Edge forgot: said, and brought back, as it was known.
			if d.Name != "" {
				name = d.Name
			}
			question, done = edge.ReAdmitQuestion(d, time.Now()), edgeDepartedSaid(d)
			return
		}
		said := edge.UnitName(n, nil)
		question, done = "re-admit "+said+"?", said
	}
	var c store.EdgeNode
	cok := false
	if !goneOK {
		var cerr error
		c, cok, cerr = edgeResolve(st.candidates, want)
		if cerr != nil {
			return cerr
		}
	}
	if goneOK {
		id, name = gone.ID, gone.Name
		question, done = edge.ReAdmitQuestion(gone, now), edgeDepartedSaid(gone)
	} else if cok {
		named(c)
	} else if nodes, lerr := st.list(); lerr == nil {
		n, nok, nerr := edgeResolve(nodes, want)
		if nerr != nil {
			return nerr
		}
		if nok {
			named(n)
		}
	}
	if id == "" {
		switch {
		case strings.HasPrefix(want, "n_"):
			// An id this Edge has no record of: quoted as typed, and the device is given no name.
			id, name = want, ""
			question = edge.ReAdmitUnknownQuestion(want) + ";"
			done = fmt.Sprintf("the device you typed as %q", want)
			consequence = "any certificate it holds from this Edge is revoked and it may claim a fresh one to rejoin"
			after = "any certificate it held is revoked; it can claim a fresh one and rejoin, and it has no name until you give it one."
		default:
			return fmt.Errorf("no forgotten device is called %q (run `roger edge readmit` to list the ones that can be re-admitted)", want)
		}
	}
	if !argv.has("yes") {
		fmt.Printf("%s %s [y/N]: ", question, consequence)
		if !edgeConfirmed() {
			fmt.Println("left alone.")
			return nil
		}
	}
	if err := edgeReAdmitDeliberate(id, name); err != nil {
		return err
	}
	st.candidates = edgeFilter(st.candidates, func(x store.EdgeNode) bool { return x.ID != id })
	_ = st.save()
	fmt.Printf("re-admitted %s - %s\n", done, after)
	if line := edgeReadmitCodeLine(id, done); line != "" {
		fmt.Println(line)
	}
	return nil
}

// edgeReadmitNameClash refuses a forgotten device's old name when a DIFFERENT device carries it now -
// a member that took it, or a device on the network announcing it - because the name no longer says
// which device is meant. The refusal says the forgotten one by what it is and when it left, with its
// id as the argument that picks it; the other device's id is never said.
func edgeReadmitNameClash(st *edgeState, d edge.Departed, now time.Time) error {
	members, err := st.list()
	if err != nil {
		return err
	}
	pick := edge.ReAdmitPick(d, now)
	if len(edge.NameHeldElsewhere(d, members)) > 0 {
		return fmt.Errorf("%q is also the name of a member of this Edge now, so it does not say which device you mean - to re-admit the forgotten one, %s", d.Name, pick)
	}
	if len(edge.NameHeldElsewhere(d, st.candidates)) > 0 {
		return fmt.Errorf("%q is also what a device on this network calls itself, so it does not say which device you mean - to re-admit the forgotten one, %s", d.Name, pick)
	}
	return nil
}

// edgeDepartedSaid is how a forgotten device is named in the line that says it was re-admitted.
func edgeDepartedSaid(d edge.Departed) string {
	if d.Name != "" {
		return d.Name
	}
	return "the " + d.What()
}

// edgeListDeparted is `roger edge readmit` with no device: what can be re-admitted, newest first.
func edgeListDeparted(st *edgeState, departed []edge.Departed) error {
	members, err := st.list()
	if err != nil {
		return err
	}
	list := edge.NewestDeparted(departed, members)
	// A name a member or an announcing device carries now is not offered: it would resolve to them.
	others := append(append([]store.EdgeNode(nil), members...), st.candidates...)
	if len(list) == 0 {
		fmt.Println("no forgotten devices on this Edge - nothing to re-admit.")
		return nil
	}
	now := time.Now()
	fmt.Println("forgotten devices that can be re-admitted, newest first:")
	for _, d := range list {
		fmt.Printf("  %s    %s\n", edge.DepartedWords(d, now), edge.ReAdmitHow(d, departed, others))
	}
	fmt.Println("re-admit one with `roger edge readmit <name>`; it claims a fresh certificate and rejoins under that name.")
	return nil
}

// edgeGrantName is the name a standing claim grant carries for a node, or "" (no grant, no
// authority here, or a grant named by an id).
func edgeGrantName(id string) string {
	local, hasRoot, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil || !hasRoot {
		return ""
	}
	grants, err := local.Claims()
	if err != nil {
		return ""
	}
	for _, g := range grants {
		if g.NodeID == id && !edge.LooksLikeID(g.Name) {
			return g.Name
		}
	}
	return ""
}

// edgeForgetNotInFleet forgets a device that is not (yet) in the fleet but still has a relationship
// with this authority: a standing claim grant (adopted, not yet claimed) and/or an issued
// certificate (claimed, not yet checked in). want may be a node id or the friendly name a grant
// carried. It resolves the matching node ids and runs edgeRevokeOnForget on EACH - which clears the
// grant and revokes every issued serial under one lock - so neither a pending claim nor a live cert
// survives. It returns how many nodes it forgot and a label for them. Only an authority has grants;
// elsewhere a raw node id can still be revoked (audit 2026-09-24).
func edgeResolveNotInFleet(want string) (map[string]string, string, error) {
	ids := map[string]string{} // node id -> label
	local, hasRoot, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil {
		// The authority is here but unreadable - do NOT report "nothing to forget" and exit 0,
		// leaving a grant or certificate live (audit 2026-09-24).
		return nil, "", fmt.Errorf("could not open the Edge authority to forget %q: %w", want, err)
	}
	if hasRoot {
		// A standing GRANT (adopted, not yet claimed): resolvable by node id or the name it carries.
		grants, err := local.Claims()
		if err != nil {
			return nil, "", err
		}
		var byName []string // grants matched by NAME (not the exact node id), for ambiguity check
		for _, g := range grants {
			byID := g.NodeID == want
			if byID || (g.Name != "" && g.Name == want) {
				who := g.Name
				if who == "" || edge.LooksLikeID(who) {
					who = "a device adopted but not yet claimed"
				}
				ids[g.NodeID] = who
				if !byID {
					byName = append(byName, g.NodeID)
				}
			}
		}
		// A name that matches more than one pending adoption is ambiguous - refuse and list the node
		// ids, the same way edgeResolve does for the fleet, rather than silently forgetting them all.
		if !strings.HasPrefix(want, "n_") && len(byName) > 1 {
			return nil, "", fmt.Errorf("%q names %d adopted devices - forget one by its node id: %s",
				want, len(byName), strings.Join(byName, ", "))
		}
		// A raw node id that actually holds an issued CERTIFICATE (claimed, addressed by id). The
		// HasIssued read fails CLOSED: an unreadable issued state is an error, not "nothing here".
		if _, seen := ids[want]; !seen && strings.HasPrefix(want, "n_") {
			issued, err := local.HasIssued(want)
			if err != nil {
				return nil, "", fmt.Errorf("could not read this Edge's issued certificates: %w", err)
			}
			if issued {
				ids[want] = "a device this Edge issued a certificate to"
			}
		}
	}
	// Even on a machine that roots NOTHING, a raw node id may be THIS machine's own held identity -
	// forgetting it gives that identity up. edgeRevokeOnForget handles the self-forget and is a no-op
	// when the id is neither issued here nor held here.
	if _, seen := ids[want]; !seen && strings.HasPrefix(want, "n_") {
		held, _, ok, err := edgeIdentityStore().LoadIdentity()
		if err != nil {
			// An unreadable identity must not be reported as "nothing to forget" (fail closed).
			return nil, "", fmt.Errorf("could not read this machine's Edge identity to forget %q: %w", want, err)
		}
		if ok && held.NodeID == want {
			ids[want] = "this machine"
		}
	}
	label := ""
	for _, who := range ids {
		if label == "" {
			label = who
		}
	}
	if len(ids) > 1 {
		label = fmt.Sprintf("%s and %d more", label, len(ids)-1)
	}
	return ids, label, nil
}

// edgeAdoptByClaim adopts a device that joins by CLAIM (a phone: no servable face). A device this
// authority has NEVER issued a certificate to is granted a fresh claim. A device it HAS issued one to
// before - one that left the Edge, or that the owner forgot and is re-pinging - is RE-ADMITTED
// instead: its old certificate is revoked (a stolen or abandoned copy stays refused by every peer,
// offline, after a restart), its issued history is cleared so those revoked serials do not block the
// fresh claim, and a new claim is granted. Only the machine that roots this Edge can do it.
func edgeAdoptByClaim(nodeID, name string) error {
	local, hasRoot, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil {
		return err
	}
	if !hasRoot {
		return fmt.Errorf("only the machine that roots this Edge can adopt a device that joins by claim")
	}
	issued, err := local.HasIssued(nodeID)
	if err != nil {
		return err
	}
	if !issued {
		if err := local.GrantClaim(nodeID, name); err != nil { // a brand-new device
			return err
		}
		// The one-time code the device enters to keep this Edge's root
		// (features/edge/enrollment_root_trust.feature).
		_, err := local.MintNodeCode(nodeID)
		return err
	}
	// A device previously issued a cert is rejoining. A ROUTINE adopt re-admits it ONLY if its cert is
	// not currently revoked (a device that merely LEFT); if it was REVOKED (the owner forgot or revoked
	// it), re-admitting would lift a deliberate revocation - a stolen key must not rejoin on one routine
	// click - so it is refused and sent to the explicit, confirmed `roger edge readmit`. The check and
	// the re-admit are ONE atomic lock in the authority, so a revoke landing mid-adopt cannot be lifted.
	revoked, err := local.ReAdmitRoutine(nodeID, name)
	if errors.Is(err, edgeauth.ErrRevokedNeedsReadmit) {
		// It is said by the name it had when this Edge kept one, and the command takes that name;
		// else the id, which is an argument, not a name.
		said, arg := edgeDepartedLabel(nodeID)
		if arg == nodeID && name != "" && !edge.LooksLikeID(name) {
			arg = name
		}
		return fmt.Errorf("%s was removed and its certificate revoked; re-admitting lifts that revocation - run `roger edge readmit %s` to do it deliberately", said, arg)
	}
	if err != nil {
		return edgeRecordRevoked(revoked, err)
	}
	if err := edgeRecordRevoked(revoked, nil); err != nil {
		return err
	}
	_, err = local.MintNodeCode(nodeID)
	return err
}

// edgeAdoptCode is the live enrollment code an adopted device enters, and when it expires - for
// the [3] adopting panel and the console. ok=false when there is none, or this is not the authority.
func edgeAdoptCode(nodeID string) (string, time.Time, bool) {
	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil || !ok {
		return "", time.Time{}, false
	}
	code, until, ok := local.NodeCode(nodeID)
	if !ok {
		return "", time.Time{}, false
	}
	return edgeauth.FormatCode(code), until, true
}

// edgeNodeCodeLine says, after an adopt, the code the device enters to finish joining.
func edgeNodeCodeLine(nodeID, name string) string {
	code, _, ok := edgeAdoptCode(nodeID)
	if !ok {
		return ""
	}
	line := fmt.Sprintf("  on %s, enter the enrollment code %s (good for %d minutes, for one join).",
		name, code, int(edgeauth.CodeLifetime.Minutes()))
	checks, heard := edgeNodeChecksRoot(nodeID)
	return line + edgeChecksCodeWarning(name, checks, heard)
}

// edgeNodeChecksRoot is what this authority has heard the node say about checking codes (ruling D9,
// fixed 2026-10-13): a phone polls its claim before it is adopted, so by the adopt it has usually
// spoken. heard is false when it has not.
func edgeNodeChecksRoot(nodeID string) (checks, heard bool) {
	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil || !ok {
		return false, false
	}
	return local.NodeChecksRoot(nodeID)
}

// edgeChecksCodeWarning is the D9 line for one joiner, worded by what it said: nothing for a joiner
// that said it checks codes, a plain statement for one that spoke without saying so, and the general
// warning for one not heard yet. Each non-empty answer starts on a new line.
func edgeChecksCodeWarning(name string, checks, heard bool) string {
	switch {
	case heard && checks:
		return ""
	case heard:
		return "\n  " + name + " runs a roger or app from before root proofs: it will not check the code. update it before it joins."
	}
	return "\n  a device running a roger or app from before root proofs does not check it: update it before it joins."
}

// edgeReadmitCodeLine is edgeNodeCodeLine for a re-admitted device, which may not need it.
func edgeReadmitCodeLine(nodeID, name string) string {
	line := edgeNodeCodeLine(nodeID, name)
	if line == "" {
		return ""
	}
	return line + "\n  a device that still holds this Edge's root will not need it."
}

// edgeFreshCodeForAdopting mints a fresh code for a device the owner already adopted that has not
// joined yet - its code expired, or was lost - voiding the old one. It answers ok=false when want
// names no such device.
func edgeFreshCodeForAdopting(want string) (string, bool, error) {
	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil || !ok {
		return "", false, err
	}
	grants, err := local.Claims()
	if err != nil {
		return "", false, err
	}
	for _, g := range grants {
		if g.NodeID != want && (g.Name == "" || g.Name != want) {
			continue
		}
		if _, err := local.MintNodeCode(g.NodeID); err != nil {
			return "", true, err
		}
		name := g.Name
		if name == "" {
			name = "that device"
		}
		return fmt.Sprintf("%s is adopted and has not joined yet - here is a fresh code; the old one is void.\n%s",
			name, edgeNodeCodeLine(g.NodeID, name)), true, nil
	}
	return "", false, nil
}

// edgeRecordRevoked mirrors revoked serials into THIS machine's own trust store so its verification
// refuses the old cert at once, not at the next refresh (like edgeRevokeOnForget). The serials are
// recorded even when the re-admit returned an error partway, since whatever it revoked is already
// final on the authority and the local mirror must not lag; that error is then returned.
func edgeRecordRevoked(revoked []string, readmitErr error) error {
	st := edgeIdentityStore()
	now := time.Now()
	for _, srl := range revoked {
		if err := st.Revoke(srl, now); err != nil {
			return err
		}
	}
	return readmitErr
}

// edgeReAdmitDeliberate is the explicit `roger edge readmit`: it lifts a revocation on purpose, so a
// forgotten or revoked device can be brought back. It is the ONE path that re-admits a revoked
// device; a routine adopt refuses that (edgeAdoptByClaim). Only the machine that roots this Edge.
func edgeReAdmitDeliberate(nodeID, name string) error {
	local, hasRoot, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil {
		return err
	}
	if !hasRoot {
		return fmt.Errorf("only the machine that roots this Edge can re-admit a device")
	}
	revoked, err := local.ReAdmit(nodeID, name)
	if err := edgeRecordRevoked(revoked, err); err != nil {
		return err
	}
	_, err = local.MintNodeCode(nodeID)
	return err
}

// edgeDropMembers removes candidates that are already members of the fleet. A node's advert carries
// its own account field, so a phone that has joined can keep showing up with an empty account (a
// candidate) until its advertiser catches up; adopting it again would mint a second certificate. It
// is not on offer, so it does not belong in the DISCOVERED band (audit 2026-09-23).
func edgeDropMembers(cands []store.EdgeNode, fleet *edge.Fleet) []store.EdgeNode {
	if fleet == nil {
		return cands
	}
	return edgeFilter(cands, func(x store.EdgeNode) bool {
		// A node advertising as a CANDIDATE (account empty) is signalling it is not currently a member -
		// a phone that left the Edge does exactly this. Keep it when the fleet says it is NOT a member,
		// AND keep it when the fleet's record for it is DARK (offline): that stale record is a device
		// that left, and it must be offered so the owner can RE-ADMIT it (h.adopt re-admits a known,
		// dark, claim-only member). A LIVE member re-advertising is dropped, to avoid cluttering the
		// DISCOVERED band with a machine that is already on the Edge. If the read fails we cannot
		// confirm anything, so we do not offer it (fail closed).
		n, known, err := fleet.Get(x.ID)
		if err != nil {
			return false
		}
		if !known {
			return true
		}
		return edge.Presence(n.Presence) == edge.PresenceDark
	})
}

// edgeDropGranted removes candidates this authority has already granted a claim to: they are
// ADOPTING (the owner acted), not still on offer, so they should not linger in the DISCOVERED band
// while the device fetches its certificate. Only an authority has grants; elsewhere it is a no-op.
func edgeDropGranted(cands []store.EdgeNode) []store.EdgeNode {
	local, hasRoot, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil || !hasRoot {
		return cands
	}
	granted, err := local.Claims()
	if err != nil || len(granted) == 0 {
		return cands
	}
	set := make(map[string]bool, len(granted))
	for _, g := range granted {
		set[g.NodeID] = true
	}
	return edgeFilter(cands, func(c store.EdgeNode) bool { return !set[c.ID] })
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
	o := edge.Options{Fleet: f, Config: cfg, CallerCert: edgeCallerCert()} // the certificate the serving probe presents
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
	// Keeping a previously-seen candidate across a pass makes it resilient to a single missed
	// announcement - but only until the discovery TTL. Past that it is STALE (the device left, or
	// stopped pinging), so drop it rather than show "last seen an hour ago" forever. A candidate
	// that is still around is re-seen and re-stamped every pass and so never crosses the cutoff.
	cutoff := time.Now().Add(-edge.DefaultTTL).Unix()
	for _, c := range old {
		if !seen[c.ID] && c.LastSeen >= cutoff {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// --- sessions -------------------------------------------------------------

// edgeSessionRow is the JSON shape of one drawn session: the same fields the console's
// EDGE tab serves, so a script and the browser read one snapshot.
type edgeSessionRow struct {
	Request  string `json:"request"`
	Kind     string `json:"kind"`
	Who      string `json:"who"`
	Band     string `json:"band"`
	Station  string `json:"station,omitempty"`
	Left     string `json:"left,omitempty"`
	Via      string `json:"via,omitempty"`
	Escalate bool   `json:"escalate"`
	Outcome  string `json:"outcome"`
	Route    string `json:"route,omitempty"` // local | market, from the evidence
	Count    int    `json:"count"`
	At       int64  `json:"at"`
}

// cmdEdgeSessions is the CLI's window on the traffic this machine's Edge is carrying: the
// same mirrored ledger the TUI's [3] EDGE and the console's EDGE tab merge (every process
// on the machine that records sessions publishes to it), grouped and worded by the same
// rules. It reads; it cannot open a session, the same as the screens.
func cmdEdgeSessions(cfg config, args []string) error {
	leaf, _ := edgeLeaf("sessions")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	groups := edge.GroupSessions(st.sessions.Live())
	if argv.has("json") {
		rows := make([]edgeSessionRow, 0, len(groups))
		for _, g := range groups {
			x := g.Session
			rows = append(rows, edgeSessionRow{Request: x.Request, Kind: string(x.Kind), Who: x.Attribution(),
				Band: x.Band, Station: x.Station, Left: x.Left, Via: x.Via, Escalate: x.Escalate,
				Outcome: x.OutcomeLabel(), Route: x.Where, Count: g.Count, At: x.At})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	if len(groups) == 0 {
		fmt.Printf("the Edge is quiet: no sessions in the last %d seconds.\n", int(edge.SessionLife.Seconds()))
		fmt.Println("  a session appears here once a turn through roger use, the TUI, a guest or the console is receipted.")
		return nil
	}
	fmt.Printf("SESSIONS · %d in the last %d seconds\n", len(st.sessions.Live()), int(edge.SessionLife.Seconds()))
	fmt.Printf("  %-18s %-22s %-7s %-40s %s\n", "WHO", "BAND", "ROUTE", "PATH", "OUTCOME")
	for _, g := range groups {
		x := g.Session
		var hops []string
		if x.Via != "" {
			hops = append(hops, x.Via)
		}
		if x.Left != "" {
			hops = append(hops, x.Left+" ✗")
		}
		if x.Station != "" {
			hops = append(hops, x.Station)
		}
		path := "—"
		if len(hops) > 0 {
			path = "→ " + strings.Join(hops, " → ")
		}
		if g.Count > 1 {
			path += fmt.Sprintf(" ×%d", g.Count)
		}
		route := x.Where
		if route == "" {
			route = "-"
		}
		fmt.Printf("  %-18s %-22s %-7s %-40s %s\n", x.Attribution(), x.Band, route, path, x.OutcomeLabel())
	}
	return nil
}

// cmdEdgeDescribeInstance describes ONE running roger: node/instance. This machine's own
// household is read from its registrations; a peer's from what its face last reported.
func cmdEdgeDescribeInstance(addr string) error {
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	nodeName, instName := edge.SplitAddress(addr)
	var (
		found edge.Instance
		on    string
		ok    bool
	)
	if id, _, enrolled, _ := edgeIdentityStore().LoadIdentity(); enrolled {
		selfName := id.NodeID
		if n, f, err := st.fleet.Get(id.NodeID); err == nil && f && n.Name != "" {
			selfName = n.Name
		}
		if nodeName == selfName || nodeName == id.NodeID || nodeName == "." {
			for _, in := range edge.Household(edgeInstancesDir(), time.Now()) {
				if in.Name == instName {
					found, on, ok = in, selfName, true
				}
			}
		}
	}
	if !ok {
		l, rerr := st.fleet.Resolve(addr)
		if rerr != nil {
			return rerr
		}
		found, on, ok = l.Instance, l.Node.Name, true
	}
	fmt.Printf("%s/%s\n", on, found.Name)
	fmt.Printf("  node          %s\n", on)
	fmt.Printf("  capabilities  %s\n", edgeInstanceCapsLabel(found))
	if len(found.Bands) > 0 {
		fmt.Printf("  serving       %s\n", strings.Join(found.Bands, ", "))
	} else {
		fmt.Printf("  serving       nothing on air\n")
	}
	if found.Started > 0 {
		fmt.Printf("  started       %s ago\n", edge.Age(time.Since(time.Unix(found.Started, 0))))
	}
	if r := found.Resources; r != nil {
		if r.GPU != "" {
			fmt.Printf("  gpu           %s · %.0f%% memory used\n", r.GPU, r.GPUMemUsed*100)
		}
		if r.RAMTotalGB > 0 {
			fmt.Printf("  ram           %.1f of %.1f GB used\n", r.RAMUsedGB, r.RAMTotalGB)
		}
	}
	fmt.Printf("  certificate   none of its own: the node %q vouches for it\n", on)
	return nil
}

// cmdEdgeNameInstance renames THIS roger on its Edge: the choice is saved in the config and
// a running roger picks it up on its next pass (edgeinstance.go). The node's name is
// untouched - that is `roger edge name <node> <name>`.
func cmdEdgeNameInstance(cfg config, want string) error {
	if err := edge.ValidInstanceName(want); err != nil {
		return usagef("%v", err)
	}
	for _, in := range edge.Household(edgeInstancesDir(), time.Now()) {
		if in.Name == want && in.Name != cfg.EdgeInstance {
			return fmt.Errorf("%w: %s", edge.ErrInstanceNameTaken, want)
		}
	}
	cfg.EdgeInstance = want
	if err := saveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("this roger is now %q on its Edge.\n", want)
	return nil
}

// edgeAttachOwnHousehold puts this machine's running rogers on its own fleet row, and
// reports whether there was one to put them on.
func edgeAttachOwnHousehold(nodes []store.EdgeNode, now time.Time) bool {
	id, _, ok, err := edgeIdentityStore().LoadIdentity()
	if err != nil || !ok {
		return false
	}
	for i := range nodes {
		if nodes[i].ID == id.NodeID {
			nodes[i].Instances = edge.Household(edgeInstancesDir(), now)
			return true
		}
	}
	return false
}

// cmdEdgePrefer shows or sets where a turn goes first. It is a standing choice, not a
// report: the ROUTE drawn on each session is where the turn really went.
// cmdEdgeAgent is the agent verb group (features/edge/agents.feature): show, on/off (the role +
// persistence), resume a dark persistent agent, or remove one. Implementations live in
// edgeagentcmd.go so this stays a small router.
func cmdEdgeAgent(cfg config, args []string) error {
	leaf, _ := edgeLeaf("agent")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	if len(argv.pos) == 0 {
		return edgeAgentShow(cfg)
	}
	switch strings.ToLower(argv.pos[0]) {
	case "resume":
		if len(argv.pos) < 2 {
			return usagef("roger edge agent resume <name>")
		}
		return edgeAgentResume(cfg, argv.pos[1])
	case "remove":
		if len(argv.pos) < 2 {
			return usagef("roger edge agent remove <name>")
		}
		return edgeAgentRemove(cfg, argv.pos[1])
	}
	on, ok := edgeParseOnOff(argv.pos[0])
	if !ok {
		return usagef("roger edge agent takes on, off, resume <name> or remove <name>; %q is none of them", argv.pos[0])
	}
	return edgeAgentSetRole(cfg, on)
}

// edgeParseOnOff reads a human on/off word.
func edgeParseOnOff(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "yes", "true", "1":
		return true, true
	case "off", "no", "false", "0":
		return false, true
	}
	return false, false
}

func cmdEdgePrefer(cfg config, args []string) error {
	leaf, _ := edgeLeaf("prefer")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	if len(argv.pos) == 0 {
		cur := edge.SelfStatus{Prefer: cfg.EdgePrefer}.EffectivePrefer()
		fmt.Printf("%s · %s\n", cur, edge.PreferMeaning(cur))
		fmt.Println("  roger edge prefer local | market | local-only")
		return nil
	}
	want := argv.pos[0]
	if !edge.ValidPrefer(want) {
		return usagef("roger edge prefer takes local, market or local-only; %q is none of them", want)
	}
	cfg.EdgePrefer = want
	if err := saveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("%s · %s\n", want, edge.PreferMeaning(want))
	return nil
}

// cmdEdgeBands answers "what can my Edge serve without the market", from the fleet record
// alone: every band an instance has on air, who serves it, and whether it is reachable from
// here right now (features/edge/local_inference.feature). A band nothing here serves is one
// that would have to go to the market.
func cmdEdgeBands(cfg config, args []string) error {
	leaf, _ := edgeLeaf("bands")
	if _, err := parseEdgeArgv(leaf, args); err != nil {
		return err
	}
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	nodes, err := st.fleet.List()
	if err != nil {
		return err
	}
	// this machine's own household serves too, and is not a row of its own fleet
	self := edge.Household(edgeInstancesDir(), time.Now())
	byBand := map[string][]string{}
	order := []string{}
	add := func(band, who string, reachable bool) {
		if _, seen := byBand[band]; !seen {
			order = append(order, band)
		}
		mark := who
		if !reachable {
			mark += " (unreachable)"
		}
		byBand[band] = append(byBand[band], mark)
	}
	for _, in := range self {
		for _, b := range in.Bands {
			add(b, "this machine/"+in.Name, true)
		}
	}
	for _, n := range nodes {
		reachable := n.Presence != string(edge.PresenceDark) && edge.HasLANTransport(n)
		for _, in := range n.Instances {
			for _, b := range in.Bands {
				add(b, n.Name+"/"+in.Name, reachable)
			}
		}
	}
	if len(order) == 0 {
		fmt.Println("no instance on this Edge serves any band right now.")
		fmt.Println("  put a model on air here (roger share), or on another machine you own.")
		fmt.Println("  anything you ask for that no one here serves goes to the market.")
		return nil
	}
	sort.Strings(order)
	fmt.Println("bands this Edge can serve locally:")
	for _, b := range order {
		fmt.Printf("  %-24s %s\n", b, strings.Join(byBand[b], ", "))
	}
	return nil
}
