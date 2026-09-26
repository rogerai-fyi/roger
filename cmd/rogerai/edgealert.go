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
			return usagef("usage: roger edge alert test <sink>")
		}
		// No sink kind is configurable yet: alerts stay on this machine. Say so rather than
		// pretend a message went somewhere.
		return fmt.Errorf("no sink named %q is configured - alerts stay on this machine (fleet surfaces and the alert log)", argv.pos[1])
	}
	return usagef("usage: roger edge alert [set <metric> <gt|lt> <value> [--scope fleet|<node>] | off <rule> | on <rule> | rm <rule> | test <sink>]")
}

func edgeAlertList(l edge.AlertLog) error {
	rules, err := l.Rules()
	if err != nil {
		return err
	}
	hs, firing, err := edgeReadHealth(time.Now())
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, h := range hs {
		names[h.ID] = h.Name
	}
	fmt.Printf("%-12s %-18s %-14s %-8s %s\n", "RULE", "WHEN", "SCOPE", "KIND", "STATE")
	for _, r := range rules {
		scope := "fleet"
		if r.Scope != "" {
			scope = names[r.Scope]
			if scope == "" {
				scope = edgeShortID(r.Scope)
			}
		}
		kind := "owner"
		if r.Default {
			kind = "default"
		}
		state := "quiet"
		switch {
		case r.Off:
			state = "off"
		default:
			var on []string
			for _, h := range hs {
				if firing.Firing(r.ID, h.ID) {
					on = append(on, h.Name)
				}
			}
			if len(on) > 0 {
				state = "firing: " + strings.Join(on, ", ")
			}
		}
		fmt.Printf("%-12s %-18s %-14s %-8s %s\n", r.ID, r.Key(), scope, kind, state)
	}
	fmt.Printf("\n%-10s none configured - alerts stay on this machine (fleet surfaces and the alert log)\n", "sinks")
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
	m := edge.Metric(argv.pos[1])
	if !edge.ValidMetric(m) {
		return usagef("roger edge alert set: unknown metric %q\nmetrics: %s", argv.pos[1], edgeMetricList())
	}
	op := edge.Op(argv.pos[2])
	if op != edge.OpGT && op != edge.OpLT {
		return usagef("roger edge alert set: comparison must be gt or lt, not %q", argv.pos[2])
	}
	v, err := strconv.ParseFloat(argv.pos[3], 64)
	if err != nil {
		return usagef("roger edge alert set: value must be a number, not %q", argv.pos[3])
	}
	// One rule per metric per scope: the id comes from the RESOLVED node, so "jetson" and its id
	// prefix name the same rule and the second spelling replaces the first.
	r := edge.Rule{ID: string(m) + "-fleet", Metric: m, Op: op, Value: v}
	scope, where := argv.flags["scope"], "every node"
	if scope != "" && scope != "fleet" {
		id, err := edgeGrantResolveNode(scope, false)
		if err != nil {
			return err
		}
		r.Scope, where, r.ID = id, scope, string(m)+"-"+edgeShortID(id)
	}
	if err := l.SetRule(r); err != nil {
		return err
	}
	fmt.Printf("rule %s: %s on %s - it takes effect on the next health read\n", r.ID, r.Key(), where)
	return nil
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
