package main

// `roger edge health [node]` (features/edge/health.feature @cli): the fleet's health at a glance -
// each node's temperature, memory, disk, uptime and presence, a dash for a metric it does not report
// (never a zero), an offline node marked offline with how long - and one node in full, every metric
// with how old the reading is.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/fleetview"
)

// edgeRunHealthScreen opens the live health screen (a seam: the test records the call).
var edgeRunHealthScreen = func(ctx context.Context, src fleetview.HealthSource) error {
	return fleetview.NewHealth(src).Run(ctx)
}

// edgeHealthSource is the screen's read: the same judged fleet read as the table, plus the
// rules in force as `roger edge alert` lists them.
func edgeHealthSource() (fleetview.HealthSnapshot, error) {
	now := time.Now()
	hs, firing, err := edgeReadHealth(now)
	if err != nil {
		return fleetview.HealthSnapshot{}, err
	}
	rules, err := edgeAlertLog().Rules()
	if err != nil {
		return fleetview.HealthSnapshot{}, err
	}
	names := map[string]string{}
	for _, h := range hs {
		names[h.ID] = h.Name
	}
	snap := fleetview.HealthSnapshot{Nodes: hs, Firing: map[string][]string{}, At: now}
	for _, h := range hs {
		for _, id := range firing.FiringFor(h.ID) {
			for _, r := range rules {
				if r.ID == id {
					snap.Firing[h.ID] = append(snap.Firing[h.ID], r.Key())
				}
			}
		}
	}
	for _, r := range rules {
		snap.Rules = append(snap.Rules, fleetview.RuleLine{ID: r.ID, When: r.Key(),
			Scope: edgeRuleScope(r, names), Kind: edgeRuleKind(r), State: edgeRuleState(r, firing, hs)})
	}
	return snap, nil
}

func cmdEdgeHealth(cfg config, args []string) error {
	leaf, _ := edgeLeaf("health")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	if argv.has("live") {
		return edgeRunHealthScreen(context.Background(), edgeHealthSource)
	}
	now := time.Now()
	hs, firing, err := edgeReadHealth(now) // every health read evaluates the rules
	if err != nil {
		return err
	}
	if argv.has("json") {
		return edgeHealthJSON(hs, firing)
	}
	if len(argv.pos) == 1 {
		return edgeHealthOne(hs, firing, argv.pos[0], now)
	}
	if len(hs) == 0 {
		fmt.Println("no nodes on this Edge yet - nothing to report on")
		return nil
	}
	fmt.Printf("%-18s %-7s %-12s %-6s %-9s %s\n", "NODE", "TEMP", "MEMORY", "DISK", "UPTIME", "PRESENCE")
	for _, h := range hs {
		fmt.Printf("%-18s %-7s %-12s %-6s %-9s %s\n", h.Name, healthTemp(h), healthMem(h), healthDisk(h), healthUptime(h), healthPresence(h))
	}
	return nil
}

// edgeHealthOne shows one node in full, each reading with its age.
func edgeHealthOne(hs []edge.NodeHealth, firing edge.AlertState, want string, now time.Time) error {
	for _, h := range hs {
		if h.Name != want && !strings.HasPrefix(h.ID, want) {
			continue
		}
		age := func() string { return "" }
		if h.Readings != nil {
			age = func() string { return "  " + edge.Age(h.ReadAge) + " ago" }
		}
		fmt.Printf("%s · %s\n", h.Name, h.Kind)
		fmt.Printf("  %-10s %s\n", "presence", healthPresence(h))
		if !h.LastHeard.IsZero() {
			fmt.Printf("  %-10s %s ago\n", "last heard", edge.Age(now.Sub(h.LastHeard)))
		}
		r := h.Readings
		if r == nil {
			fmt.Println("  this node reports no health readings")
			return nil
		}
		if r.CPUTempC != nil {
			fmt.Printf("  %-10s %.1f°C%s\n", "cpu temp", *r.CPUTempC, age())
		}
		if r.CPULoad1 != nil {
			fmt.Printf("  %-10s %.2f%s\n", "cpu load", *r.CPULoad1, age())
		}
		if r.RAMTotalGB > 0 {
			fmt.Printf("  %-10s %.1f / %.0f GB (%.0f%%)%s\n", "memory", r.RAMUsedGB, r.RAMTotalGB, 100*r.RAMUsedGB/r.RAMTotalGB, age())
		} else if r.RAMUsedGB > 0 {
			fmt.Printf("  %-10s %.1f GB used (no total reported)%s\n", "memory", r.RAMUsedGB, age())
		}
		for _, d := range r.Disks {
			fmt.Printf("  %-10s %s %.0f / %.0f GB (%.0f%%)%s\n", "disk", d.Mount, d.UsedGB, d.TotalGB, 100*d.UsedGB/d.TotalGB, age())
		}
		if r.UptimeS > 0 {
			fmt.Printf("  %-10s %s%s\n", "uptime", edge.Age(time.Duration(r.UptimeS)*time.Second), age())
		}
		if r.GPU != "" {
			fmt.Printf("  %-10s %s %.0f%% memory%s\n", "gpu", r.GPU, 100*r.GPUMemUsed, age())
		}
		if r.SDWearPct != nil {
			fmt.Printf("  %-10s %.0f%% worn%s\n", "sd card", *r.SDWearPct, age())
		}
		return edgeHealthAlerts(h, firing)
	}
	return errNoSuchNode(want)
}

// edgeHealthAlerts prints the rules firing for one node, each with the value it is judged on.
func edgeHealthAlerts(h edge.NodeHealth, firing edge.AlertState) error {
	ids := firing.FiringFor(h.ID)
	if len(ids) == 0 {
		fmt.Printf("  %-10s none firing\n", "alerts")
		return nil
	}
	rules, err := edgeAlertLog().Rules()
	if err != nil {
		return err
	}
	for _, id := range ids {
		for _, r := range rules {
			if r.ID != id {
				continue
			}
			line := r.Key()
			if v, ok := h.Reading(r.Metric); ok {
				line += fmt.Sprintf("  (now %.1f)", v)
			}
			fmt.Printf("  %-10s %s\n", "alerts", line)
		}
	}
	return nil
}

// edgeHealthJSON is the fleet's health for scripts: an absent metric is absent (the readings
// struct omits what was not read), a node reporting nothing has null readings.
func edgeHealthJSON(hs []edge.NodeHealth, firing edge.AlertState) error {
	type row struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		Kind      string   `json:"kind"`
		Presence  string   `json:"presence"`
		LastHeard int64    `json:"last_heard,omitempty"`
		DarkForS  int64    `json:"dark_for_s,omitempty"`
		Readings  any      `json:"readings"`
		ReadAgeS  int64    `json:"read_age_s,omitempty"`
		Alerts    []string `json:"alerts"`
	}
	out := make([]row, 0, len(hs))
	for _, h := range hs {
		r := row{ID: h.ID, Name: h.Name, Kind: h.Kind, Presence: h.Presence, Alerts: firing.FiringFor(h.ID)}
		if r.Alerts == nil {
			r.Alerts = []string{}
		}
		if !h.LastHeard.IsZero() {
			r.LastHeard = h.LastHeard.Unix()
		}
		if h.Dark {
			r.DarkForS = int64(h.DarkFor.Seconds())
		}
		if h.Readings != nil {
			r.Readings, r.ReadAgeS = h.Readings, int64(h.ReadAge.Seconds())
		}
		out = append(out, r)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func healthTemp(h edge.NodeHealth) string {
	if h.Readings == nil || h.Readings.CPUTempC == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f°C", *h.Readings.CPUTempC)
}

func healthMem(h edge.NodeHealth) string {
	if h.Readings == nil || h.Readings.RAMUsedGB <= 0 {
		return "-"
	}
	if h.Readings.RAMTotalGB > 0 {
		return fmt.Sprintf("%.1f/%.0f GB", h.Readings.RAMUsedGB, h.Readings.RAMTotalGB)
	}
	return fmt.Sprintf("%.1f GB", h.Readings.RAMUsedGB) // a figure, not a level
}

func healthDisk(h edge.NodeHealth) string {
	if l, ok := h.DiskLevel(); ok {
		return fmt.Sprintf("%.0f%%", 100*l)
	}
	return "-"
}

func healthUptime(h edge.NodeHealth) string {
	if h.Readings == nil || h.Readings.UptimeS <= 0 {
		return "-"
	}
	return edge.Age(time.Duration(h.Readings.UptimeS) * time.Second)
}

func healthPresence(h edge.NodeHealth) string {
	if h.Dark {
		return "offline " + edge.Age(h.DarkFor)
	}
	if h.Readings != nil && h.ReadAge > time.Minute {
		return "online (reading " + edge.Age(h.ReadAge) + " old)"
	}
	return "online"
}
