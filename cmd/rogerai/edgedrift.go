package main

// `roger edge expect` and `roger edge drift` (features/edge/drift.feature): say what the fleet should
// look like, and be told where it does not.
//
//	roger edge expect                                    what is expected, and how many units differ
//	roger edge expect roger 6.13.0 [--scope tag:production]
//	roger edge expect serves gpt-oss-20b [--scope <node>]
//	roger edge expect agent | maintenance [--scope ...]
//	roger edge expect rm <thing> [--scope ...]
//	roger edge drift [--json]
//
// Nothing here writes another machine's configuration. Every expectation is judged against facts the
// fleet already collected from a describe over that node's own certificate, and the owner closes any
// gap themselves.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// edgeExpectScope reads the --scope flag as an expectation scope: "" (the fleet), a tag, or a node id
// resolved from a name. It is the same resolution an alert rule's scope gets.
func edgeExpectScope(want string) (string, string, error) {
	if want == "" || want == "fleet" {
		return "", "every node", nil
	}
	if tag, ok := edge.ScopeTag(want); ok {
		tag = edge.NormalizeTag(tag)
		if err := edge.ValidTag(tag); err != nil {
			return "", "", err
		}
		return edge.TagScope(tag), "every node tagged " + tag, nil
	}
	id, err := edgeGrantResolveNode(want, false)
	if err != nil {
		return "", "", err
	}
	return id, want, nil
}

func cmdEdgeExpect(cfg config, args []string) error {
	leaf, _ := edgeLeaf("expect")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	log := edgeAlertLog()
	if len(argv.pos) == 0 {
		return edgePrintExpectations(os.Stdout, time.Now())
	}
	scope, where, err := edgeExpectScope(argv.flags["scope"])
	if err != nil {
		return err
	}
	// `expect rm <thing>` drops one.
	if argv.pos[0] == "rm" || argv.pos[0] == "remove" {
		if len(argv.pos) < 2 {
			return usagef("usage: roger edge expect rm <%s> [--scope ...]", edge.ExpectWhatList())
		}
		had, err := log.RemoveExpectation(edge.ExpectWhat(argv.pos[1]), scope)
		if err != nil {
			return err
		}
		if !had {
			fmt.Printf("nothing was expected of %s there.\n", argv.pos[1])
			return nil
		}
		fmt.Printf("%s is no longer expected of %s.\n", argv.pos[1], where)
		return nil
	}
	e := edge.Expectation{What: edge.ExpectWhat(argv.pos[0]), Scope: scope}
	if len(argv.pos) > 1 {
		e.Value = argv.pos[1]
	}
	if err := log.SetExpectation(e); err != nil {
		return err
	}
	fmt.Printf("expected of %s: %s\n", where, e.Words())
	fmt.Println("  it is judged against what each unit already reports; `roger edge drift` says where it does not hold.")
	return nil
}

// edgeDriftRead judges the fleet against the expectations as they both stand now.
func edgeDriftRead(now time.Time) (set edge.ExpectSet, out []edge.Drift, checked int, err error) {
	set, err = edgeAlertLog().Expectations()
	if err != nil {
		return nil, nil, 0, err
	}
	st, err := loadEdgeState()
	if err != nil {
		return nil, nil, 0, err
	}
	nodes, err := st.list()
	if err != nil {
		return nil, nil, 0, err
	}
	edgeAttachOwnHousehold(nodes, now)
	out, checked = set.Judge(nodes)
	return set, out, checked, nil
}

func edgePrintExpectations(w io.Writer, now time.Time) error {
	set, drift, checked, err := edgeDriftRead(now)
	if err != nil {
		return err
	}
	if len(set) == 0 {
		edgeSayNothingExpected(w)
		return nil
	}
	off := map[string]int{}
	for _, d := range drift {
		if d.State == edge.DriftOut {
			off[string(d.What)+"|"+d.Expected]++
		}
	}
	fmt.Fprintf(w, "EXPECTED of this Edge · %d unit(s) checked\n", checked)
	fmt.Fprintf(w, "%-24s %-26s %s\n", "expected", "of", "standing")
	names := edgeNodeNames()
	for _, e := range set {
		n := off[string(e.What)+"|"+e.Words()]
		standing := "holds"
		if n > 0 {
			standing = fmt.Sprintf("%d unit(s) differ", n)
		}
		fmt.Fprintf(w, "%-24s %-26s %s\n", e.Words(), edgeExpectScopeWords(e.Scope, names), standing)
	}
	return nil
}

// edgeSayNothingExpected says what an empty set MEANS here. On the authority it means the owner has
// asked for nothing yet. On a member it means no such thing: the expectations are kept where they were
// typed, and this machine simply does not hold them - saying "nothing is expected of this Edge" there
// would be telling the owner something untrue about their own fleet.
func edgeSayNothingExpected(w io.Writer) {
	if self := edgeSelfStatus(nil, edgeDiscoveryFactsFromEnv()); self.Err == "" && !self.AuthorityHere {
		fmt.Fprintln(w, "this machine holds no expectations.")
		fmt.Fprintln(w, "  they are kept on the machine that roots this Edge, where they were typed, and read there.")
		return
	}
	fmt.Fprintln(w, "nothing is expected of this Edge yet.")
	fmt.Fprintf(w, "  say what it should look like: roger edge expect <%s> [value] [--scope fleet|tag:<tag>|<node>]\n", edge.ExpectWhatList())
}

// edgeExpectScopeWords is how a scope reads on a surface: the fleet, a group, or a node's name.
func edgeExpectScopeWords(scope string, names map[string]string) string {
	if scope == "" {
		return "the fleet"
	}
	if tag, ok := edge.ScopeTag(scope); ok {
		return "tag " + tag
	}
	if n := names[scope]; n != "" && n != scope {
		return n
	}
	return edgeShortID(scope)
}

// edgeNodeNames is every node's name by id, for wording a scope.
// edgeNodeLabel is what a sentence calls one unit: the shared lookup's name, or, for a unit not in
// this fleet's records, its own hardware word - never its id.
func edgeNodeLabel(n store.EdgeNode) string {
	if name := edgeNodeNames()[n.ID]; name != "" {
		return name
	}
	return edge.UnitName(n, []store.EdgeNode{n})
}

func edgeNodeNames() map[string]string {
	out := map[string]string{}
	st, err := loadEdgeState()
	if err != nil {
		return out
	}
	nodes, err := st.list()
	if err != nil {
		return out
	}
	self := edgeThisNodeID()
	for _, n := range nodes {
		// A record whose name IS its id was never named by its owner, so every surface calls it what
		// the rack does - its hardware word (edge.UnitName), never an id (node_identity.feature). This
		// map is what the drift report, the schedule listing, the inventory and the console's grant
		// lines all read, so they cannot disagree about what a unit is called.
		out[n.ID] = edge.UnitName(n, nodes)
		if n.ID == self {
			out[n.ID] = edge.SelfLabel(n.Name, edgeStationName())
		}
	}
	return out
}

func cmdEdgeDrift(cfg config, args []string) error {
	leaf, _ := edgeLeaf("drift")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	now := time.Now()
	set, drift, checked, err := edgeDriftRead(now)
	if err != nil {
		return err
	}
	if argv.has("json") {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"expected": set, "drift": drift, "checked": checked})
	}
	edgeWriteDrift(os.Stdout, set, drift, checked)
	return nil
}

func edgeWriteDrift(w io.Writer, set edge.ExpectSet, drift []edge.Drift, checked int) {
	if len(set) == 0 {
		edgeSayNothingExpected(w)
		return
	}
	out, unknown := 0, 0
	for _, d := range drift {
		if d.State == edge.DriftOut {
			out++
		} else {
			unknown++
		}
	}
	if out == 0 {
		fmt.Fprintf(w, "this Edge is as you asked: %d unit(s) checked against %d expectation(s).\n", checked, len(set))
		if unknown > 0 {
			fmt.Fprintf(w, "  %d check(s) could not be made: a unit has not reported the fact they need.\n", unknown)
		}
		return
	}
	fmt.Fprintf(w, "DRIFT · %d of %d unit-checks differ\n", out, out+unknown)
	fmt.Fprintf(w, "%-20s %-24s %-34s %s\n", "unit", "expected", "found", "")
	for _, d := range drift {
		mark := ""
		switch {
		case d.State == edge.DriftUnknown:
			mark = "unknown"
		case d.Dark:
			mark = "dark"
		}
		fmt.Fprintf(w, "%-20s %-24s %-34s %s\n", elideTo(d.Node, 20), elideTo(d.Expected, 24), elideTo(d.Found, 34), mark)
	}
}

// edgeDriftLines is the DRIFT row a unit carries on the fleet screen: one line per thing that does
// not match, worded as the CLI words it. Only real drift, never an unknown - a fact a unit has not
// reported is not something to show an operator as wrong.
func edgeDriftLines(drift []edge.Drift, nodeID string) []string {
	var out []string
	for _, d := range drift {
		if d.NodeID != nodeID || d.State != edge.DriftOut {
			continue
		}
		out = append(out, fmt.Sprintf("%s · %s", d.Expected, d.Found))
	}
	return out
}
