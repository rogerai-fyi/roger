package main

// `roger edge alert` (features/edge/health.feature @cli): list the rules, their scope and whether
// they are firing, and name where alerts go; `alert set <metric> <gt|lt> <value> [--scope fleet|<node>]`
// stores a rule the next health read evaluates; `alert off|on <id>` flips a rule (a default is only
// ever turned off); `alert rm <id>` removes an owner rule; `alert test <sink>` proves a sink.
// Alerts are evaluated on every health read and every host pass, against the on-disk alert log.

import (
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
)

func edgeAlertLog() edge.AlertLog { return edge.AlertLog{Dir: filepath.Join(edgeAuthDir(), "alerts")} }

// edgeEvaluateAlerts judges the fleet's health against the rules in force and records the
// result. It returns what is firing now, so the caller can show it against the same snapshot.
func edgeEvaluateAlerts(hs []edge.NodeHealth, now time.Time) (edge.AlertState, []edge.Alert, error) {
	return edgeAlertLog().Judge(hs, now)
}

// edgeReadHealth is one health read as the CLI does it: the fleet's snapshot, judged.
func edgeReadHealth(now time.Time) ([]edge.NodeHealth, edge.AlertState, error) {
	st, err := loadEdgeState()
	if err != nil {
		return nil, nil, err
	}
	hs, err := edge.FleetHealth(st.fleet, now)
	if err != nil {
		return nil, nil, err
	}
	firing, _, err := edgeEvaluateAlerts(hs, now)
	return hs, firing, err
}

func cmdEdgeAlert(cfg config, args []string) error {
	leaf, _ := edgeLeaf("alert")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	l := edgeAlertLog()
	if len(argv.pos) == 0 {
		return edgeAlertList(l)
	}
	switch argv.pos[0] {
	case "set":
		return edgeAlertSet(l, argv)
	case "off", "on":
		if len(argv.pos) != 2 {
			return usagef("usage: roger edge alert %s <rule>", argv.pos[0])
		}
		if err := l.SetOff(argv.pos[1], argv.pos[0] == "off"); err != nil {
			return err
		}
		fmt.Printf("rule %s is %s\n", argv.pos[1], argv.pos[0])
		return nil
	case "rm":
		if len(argv.pos) != 2 {
			return usagef("usage: roger edge alert rm <rule>")
		}
		if err := l.Remove(argv.pos[1]); err != nil {
			return err
		}
		fmt.Printf("rule %s removed\n", argv.pos[1])
		return nil
	case "test":
		if len(argv.pos) != 2 {
			return usagef("usage: roger edge alert test <member>")
		}
		// The sink is a member's Roger app reading the trouble feed (trouble_feed.feature): a test
		// call is one clearly-marked transition in that feed, changing no rule state.
		id, err := edgeGrantResolveNode(argv.pos[1], false)
		if err != nil {
			return fmt.Errorf("a test call goes to a member's Roger app: %w", err)
		}
		st, err := loadEdgeState()
		if err != nil {
			return err
		}
		n, _, _ := st.fleet.Get(id)
		name := n.Name
		if name == "" {
			name = argv.pos[1]
		}
		if err := l.TestCall(name, time.Now()); err != nil {
			return err
		}
		fmt.Printf("test call sent to the feed for %s - it appears once there, marked TEST, and raises no real alarm.\n", name)
		return nil
	}
	return usagef("usage: roger edge alert [set <metric> <gt|lt> <value> [--scope fleet|<node>] | off <rule> | on <rule> | rm <rule> | test <sink>]")
}

func edgeAlertList(l edge.AlertLog) error {
	rules, err := l.Rules()
	if err != nil {
		return err
	}
	now := time.Now()
	hs, firing, err := edgeReadHealth(now)
	if err != nil {
		return err
	}
	names, kinds := map[string]string{}, map[string]string{}
	for _, h := range hs {
		names[h.ID], kinds[h.ID] = h.Name, h.Kind
	}
	fmt.Printf("%-12s %-18s %-14s %-8s %s\n", "RULE", "WHEN", "SCOPE", "KIND", "STATE")
	for _, r := range rules {
		fmt.Printf("%-12s %-18s %-14s %-8s %s\n", r.ID, r.Key(), edgeRuleScope(r, names), edgeRuleKind(r), edgeRuleState(r, firing, hs))
	}
	fmt.Printf("\n%-10s %s\n", "sinks", edgeSinkLine(l, names, kinds, now))
	recent, err := l.Recent(8)
	if err != nil {
		return err
	}
	if len(recent) > 0 {
		fmt.Println("\nrecent")
		for _, a := range recent {
			fmt.Printf("  %s  %s\n", a.At.Local().Format("Jan 2 15:04"), a)
		}
	}
	return nil
}

func edgeAlertSet(l edge.AlertLog, argv edgeArgv) error {
	if len(argv.pos) != 4 {
		return usagef("usage: roger edge alert set <metric> <gt|lt> <value> [--scope fleet|<node>]\nmetrics: %s", edgeMetricList())
	}
	msg, err := edgeAlertSetRule(l, argv.pos[1], argv.pos[2], argv.pos[3], argv.flags["scope"])
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

// edgeAlertSetRule stores a rule from words as typed - shared by the CLI and the health screen.
func edgeAlertSetRule(l edge.AlertLog, metric, opWord, value, scope string) (string, error) {
	m := edge.Metric(metric)
	if !edge.ValidMetric(m) {
		return "", usagef("unknown metric %q - metrics: %s", metric, edgeMetricList())
	}
	op := edge.Op(opWord)
	if op != edge.OpGT && op != edge.OpLT {
		return "", usagef("comparison must be gt or lt, not %q", opWord)
	}
	v, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return "", usagef("value must be a number, not %q", value)
	}
	// One rule per metric per scope: the id comes from the RESOLVED node, so "jetson" and its id
	// prefix name the same rule and the second spelling replaces the first.
	r := edge.Rule{ID: string(m) + "-fleet", Metric: m, Op: op, Value: v}
	where := "every node"
	if scope != "" && scope != "fleet" {
		id, err := edgeGrantResolveNode(scope, false)
		if err != nil {
			return "", err
		}
		r.Scope, where, r.ID = id, scope, string(m)+"-"+edgeShortID(id)
	}
	if err := l.SetRule(r); err != nil {
		return "", err
	}
	return fmt.Sprintf("rule %s: %s on %s - it takes effect on the next health read", r.ID, r.Key(), where), nil
}

func edgeMetricList() string {
	out := make([]string, 0, len(edge.Metrics))
	for _, m := range edge.Metrics {
		out = append(out, string(m))
	}
	return strings.Join(out, ", ")
}

// edgeFiringAlerts is what the surfaces draw: the rules firing right now, by node id, each in its
// wording. Read from the alert log the host and the CLI both write, so every window agrees; held
// for a moment so a redraw does not re-read the disk.
func edgeFiringAlerts() map[string][]string {
	edgeFiringMu.Lock()
	defer edgeFiringMu.Unlock()
	if time.Since(edgeFiringAt) < 2*time.Second {
		return edgeFiringVal
	}
	edgeFiringVal, edgeFiringAt = edgeReadFiring(), time.Now()
	return edgeFiringVal
}

var (
	edgeFiringMu  sync.Mutex
	edgeFiringAt  time.Time
	edgeFiringVal map[string][]string
)

func edgeReadFiring() map[string][]string {
	l := edgeAlertLog()
	st, err := l.State()
	if err != nil {
		log.Println("edge: could not read what is firing:", err)
		return nil
	}
	rules, err := l.Rules()
	if err != nil {
		log.Println("edge: could not read the alert rules:", err)
		return nil
	}
	return edgeFiringWords(st, rules)
}

// edgeFiringWords is a firing state as the surfaces draw it: node id -> the rules firing, worded.
func edgeFiringWords(st edge.AlertState, rules edge.RuleSet) map[string][]string {
	byID := map[string]edge.Rule{}
	for _, r := range rules {
		byID[r.ID] = r
	}
	out := map[string][]string{}
	for k, firing := range st {
		id, node, ok := strings.Cut(k, "|")
		if !ok || !firing {
			continue
		}
		if r, known := byID[id]; known && !r.Off {
			out[node] = append(out[node], r.Key())
		}
	}
	for n := range out {
		sort.Strings(out[n])
	}
	return out
}

// edgeRuleScope, edgeRuleKind and edgeRuleState are one rule's columns, shared by the CLI list
// and the live screen so the two never word a rule differently.
func edgeRuleScope(r edge.Rule, names map[string]string) string {
	if r.Scope == "" {
		return "fleet"
	}
	if n := names[r.Scope]; n != "" {
		return n
	}
	return edgeShortID(r.Scope)
}

func edgeRuleKind(r edge.Rule) string {
	if r.Default {
		return "default"
	}
	return "owner"
}

func edgeRuleState(r edge.Rule, firing edge.AlertState, hs []edge.NodeHealth) string {
	if r.Off {
		return "off"
	}
	var on []string
	for _, h := range hs {
		if firing.Firing(r.ID, h.ID) {
			on = append(on, h.Name)
		}
	}
	if len(on) == 0 {
		return "quiet"
	}
	return "firing: " + strings.Join(on, ", ")
}

// edgeSinkLine names where alerts go: the Roger app on each member that has read the trouble
// feed, and when it last looked - or nowhere, until one does.
func edgeSinkLine(l edge.AlertLog, names map[string]string, kinds map[string]string, now time.Time) string {
	reads, err := l.Reads()
	if err != nil || len(reads) == 0 {
		return "none configured - alerts stay on this machine (fleet surfaces and the alert log) until a member's Roger app reads the feed"
	}
	ids := make([]string, 0, len(reads))
	for id := range reads {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		name := names[id]
		if name == "" {
			name = edgeShortID(id)
		}
		who := "the Roger app on " + name
		if kinds[id] != "mobile" { // a machine reading its own feed is a reader, not a phone
			who = "read by " + name
		}
		parts = append(parts, fmt.Sprintf("%s (last read %s ago)", who, edge.Age(now.Sub(time.Unix(reads[id], 0)))))
	}
	return strings.Join(parts, " · ")
}
