package edge

// ALERT RULES (features/edge/health.feature §3-5): a rule is a metric, a comparison and a value,
// fleet-wide or for one node. Sensible defaults exist so it works out of the box and can be turned
// off. A rule fires ONCE when crossed and clears ONCE when it recovers past a small margin, so a
// wobble on the threshold is not a stream. Offline is judged here, on the authority, from the
// missing heartbeat - a dead node cannot send its own alarm. An alert names the node, the metric,
// the real value and the threshold it crossed, and whether it is firing or clearing.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Metric is what a rule watches. The unit is fixed per metric so a rule reads plainly.
type Metric string

const (
	MetricCPUTemp Metric = "cpu_temp" // degrees C
	MetricCPULoad Metric = "cpu_load" // 1-minute load average
	MetricMemory  Metric = "memory"   // percent of a reported total
	MetricDisk    Metric = "disk"     // percent, the fullest mounted disk
	MetricSDWear  Metric = "sd_wear"  // percent worn
	MetricOffline Metric = "offline"  // minutes unheard, judged by the authority
)

// Metrics is every metric a rule may watch, in display order.
var Metrics = []Metric{MetricCPUTemp, MetricCPULoad, MetricMemory, MetricDisk, MetricSDWear, MetricOffline}

func metricLabel(m Metric) string { return strings.ReplaceAll(string(m), "_", " ") }

func metricUnit(m Metric) string {
	switch m {
	case MetricCPUTemp:
		return "°C"
	case MetricMemory, MetricDisk, MetricSDWear:
		return "%"
	case MetricOffline:
		return "m"
	}
	return ""
}

// ValidMetric reports whether m is a metric a rule may watch.
func ValidMetric(m Metric) bool {
	for _, k := range Metrics {
		if k == m {
			return true
		}
	}
	return false
}

// Op is a rule's comparison.
type Op string

const (
	OpGT Op = "gt"
	OpLT Op = "lt"
)

func (o Op) String() string {
	if o == OpLT {
		return "<"
	}
	return ">"
}

// Rule is one threshold. Scope is a node id, or empty for fleet-wide. A node-scoped rule REPLACES
// the fleet rule on the same metric for that node (§3: "nas" uses its own 75, everyone else 90).
type Rule struct {
	ID      string  `json:"id"`
	Metric  Metric  `json:"metric"`
	Op      Op      `json:"op"`
	Value   float64 `json:"value"`
	Scope   string  `json:"scope,omitempty"`
	Margin  float64 `json:"margin,omitempty"` // recovery margin; 0 means the metric's default
	Default bool    `json:"default,omitempty"`
	Off     bool    `json:"off,omitempty"`
}

// Key is how the rule is worded: "cpu_temp > 80".
func (r Rule) Key() string {
	return fmt.Sprintf("%s %s %s", r.Metric, r.Op, fmtThreshold(r.Value))
}

// ValidateRule refuses a rule that cannot mean what it says. An offline rule only counts UPWARDS:
// "offline lt 5" would be true for every node that is not dark at all - a false alarm on the whole
// fleet - so it is refused where the rule is made, not judged.
func ValidateRule(r Rule) error {
	if !ValidMetric(r.Metric) {
		return fmt.Errorf("unknown metric %q", r.Metric)
	}
	if r.Op != OpGT && r.Op != OpLT {
		return fmt.Errorf("comparison must be gt or lt, not %q", r.Op)
	}
	if r.Metric == MetricOffline && r.Op != OpGT {
		return errors.New("an offline rule only counts upwards: use offline gt <minutes>")
	}
	if r.ID == "" {
		return errors.New("a rule needs an id")
	}
	return nil
}

func fmtThreshold(v float64) string {
	return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.1f", v), "0"), ".")
}

// margin is how far past the threshold a metric must recover before the rule clears. Small on
// purpose: enough to swallow sensor jitter, not enough to hide a real recovery.
func (r Rule) margin() float64 {
	if r.Margin > 0 {
		return r.Margin
	}
	switch r.Metric {
	case MetricCPUTemp:
		return 2
	case MetricCPULoad:
		return 0.25
	case MetricMemory, MetricDisk, MetricSDWear:
		return 2
	}
	return 0
}

// DefaultRules are the rules an owner who set none gets: a hot CPU, a nearly full disk, and a node
// offline past the liveness window. Each is documented by Describe and can be turned Off.
func DefaultRules() []Rule {
	return []Rule{
		{ID: "cpu_temp", Metric: MetricCPUTemp, Op: OpGT, Value: 85, Default: true},
		{ID: "disk", Metric: MetricDisk, Op: OpGT, Value: 90, Default: true},
		{ID: "offline", Metric: MetricOffline, Op: OpGT, Value: DefaultTTL.Minutes(), Default: true},
	}
}

// RuleSet is the rules in force.
type RuleSet []Rule

// Off returns the set with rule id turned off (a default) - it is never removed.
func (rs RuleSet) Off(id string) RuleSet {
	out := make(RuleSet, len(rs))
	copy(out, rs)
	for i := range out {
		if out[i].ID == id {
			out[i].Off = true
		}
	}
	return out
}

// applying is the rule in force for node on metric: its own scoped rule if it has one, else the
// fleet rule; ok is false when nothing applies (or what applies is off).
func (rs RuleSet) applying(m Metric, nodeID string) (Rule, bool) {
	var fleet Rule
	haveFleet := false
	for _, r := range rs {
		if r.Metric != m || r.Off {
			continue
		}
		if r.Scope != "" && r.Scope == nodeID {
			return r, true
		}
		if r.Scope == "" {
			fleet, haveFleet = r, true
		}
	}
	return fleet, haveFleet
}

// AlertState is which (rule, node) pairs are firing now. It is what makes a crossing one event.
type AlertState map[string]bool

func stateKey(ruleID, nodeID string) string { return ruleID + "|" + nodeID }

// Firing reports whether rule id is firing for node.
func (s AlertState) Firing(ruleID, nodeID string) bool { return s[stateKey(ruleID, nodeID)] }

// FiringFor is the ids of the rules firing for node.
func (s AlertState) FiringFor(nodeID string) []string {
	var out []string
	for k, v := range s {
		if id, n, ok := strings.Cut(k, "|"); ok && v && n == nodeID {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Alert is one state change: a rule started firing for a node, or cleared.
type Alert struct {
	At        time.Time `json:"at"`
	RuleID    string    `json:"rule"`
	NodeID    string    `json:"node_id"`
	Node      string    `json:"node"`
	Metric    Metric    `json:"metric"`
	Value     float64   `json:"value"`
	Threshold float64   `json:"threshold"`
	Op        Op        `json:"op"`
	Firing    bool      `json:"firing"`
	DarkForS  int64     `json:"dark_for_s,omitempty"` // offline only: how long unheard, seconds
	LastHeard int64     `json:"last_heard,omitempty"` // unix seconds; absent when never heard
	Test      bool      `json:"test,omitempty"`
}

// String is the alert as a person reads it: node, metric, the real value, the threshold crossed.
func (a Alert) String() string {
	state := "firing"
	if !a.Firing {
		state = "cleared"
	}
	if a.Metric == MetricOffline {
		if a.Firing {
			return fmt.Sprintf("%s: %s is offline for %s (last heard %s, threshold %s)",
				state, a.Node, Age(time.Duration(a.DarkForS)*time.Second), time.Unix(a.LastHeard, 0).Format("15:04 Jan 2"), fmtThreshold(a.Threshold)+"m")
		}
		return fmt.Sprintf("%s: %s is back online", state, a.Node)
	}
	unit := metricUnit(a.Metric)
	return fmt.Sprintf("%s: %s %s %s%s (threshold %s %s%s)", state, a.Node, metricLabel(a.Metric),
		fmtThreshold(a.Value), unit, a.Op, fmtThreshold(a.Threshold), unit)
}

// Reading is the node's current value for m. ok is false when the node does not report it, or
// its report is older than the liveness window - a stale reading is never judged as current.
func (h NodeHealth) Reading(m Metric) (float64, bool) {
	if m == MetricOffline {
		if h.Dark {
			return h.DarkFor.Minutes(), true
		}
		return 0, true
	}
	r := h.Readings
	if r == nil || h.ReadAge > DefaultTTL {
		return 0, false
	}
	switch m {
	case MetricCPUTemp:
		if r.CPUTempC != nil {
			return *r.CPUTempC, true
		}
	case MetricCPULoad:
		if r.CPULoad1 != nil {
			return *r.CPULoad1, true
		}
	case MetricMemory:
		if l, ok := h.MemoryLevel(); ok {
			return 100 * l, true
		}
	case MetricDisk:
		if l, ok := h.DiskLevel(); ok {
			return 100 * l, true
		}
	case MetricSDWear:
		if r.SDWearPct != nil {
			return *r.SDWearPct, true
		}
	}
	return 0, false
}

// Evaluate judges every node against the rules in force, given what was firing before, and
// returns the new state plus one Alert per state CHANGE. Over stays over silently; recovery
// must clear the threshold by the rule's margin. A firing that can no longer be judged - its
// rule turned off or removed, its node forgotten - is CLOSED with a clearing alert, so the log
// never holds a firing that nothing ever clears.
func Evaluate(rules RuleSet, hs []NodeHealth, prev AlertState, now time.Time) (AlertState, []Alert) {
	next := AlertState{}
	var alerts []Alert
	judged := map[string]bool{}
	for _, h := range hs {
		for _, m := range Metrics {
			r, ok := rules.applying(m, h.ID)
			if !ok {
				continue
			}
			judged[stateKey(r.ID, h.ID)] = true
			v, have := h.Reading(m)
			was := prev.Firing(r.ID, h.ID)
			if !have {
				if was {
					next[stateKey(r.ID, h.ID)] = true // no reading is no verdict: hold what we knew
				}
				continue
			}
			firing := was
			switch {
			case r.Op == OpLT && v < r.Value, r.Op != OpLT && v > r.Value:
				firing = true
			case r.Op == OpLT && v >= r.Value+r.margin(), r.Op != OpLT && v <= r.Value-r.margin():
				firing = false
			}
			if firing {
				next[stateKey(r.ID, h.ID)] = true
			}
			if firing != was {
				a := Alert{At: now, RuleID: r.ID, NodeID: h.ID, Node: h.Name, Metric: m,
					Value: v, Threshold: r.Value, Op: r.Op, Firing: firing}
				if m == MetricOffline && firing {
					a.DarkForS = int64(h.DarkFor.Seconds())
				}
				if !h.LastHeard.IsZero() {
					a.LastHeard = h.LastHeard.Unix()
				}
				alerts = append(alerts, a)
			}
		}
	}
	names := map[string]string{}
	for _, h := range hs {
		names[h.ID] = h.Name
	}
	keys := make([]string, 0, len(prev))
	for k := range prev {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !prev[k] || judged[k] {
			continue
		}
		ruleID, nodeID, _ := strings.Cut(k, "|")
		a := Alert{At: now, RuleID: ruleID, NodeID: nodeID, Node: names[nodeID]}
		if a.Node == "" {
			a.Node = nodeID
		}
		for _, r := range rules {
			if r.ID == ruleID {
				a.Metric, a.Op, a.Threshold = r.Metric, r.Op, r.Value
			}
		}
		alerts = append(alerts, a)
	}
	return next, alerts
}
