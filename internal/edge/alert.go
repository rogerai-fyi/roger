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
	"slices"
	"sort"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/store"
)

// Metric is what a rule watches. The unit is fixed per metric so a rule reads plainly.
type Metric string

const (
	MetricCPUTemp   Metric = "cpu_temp"  // degrees C
	MetricCPULoad   Metric = "cpu_load"  // 1-minute load average
	MetricMemory    Metric = "memory"    // percent of a reported total
	MetricDisk      Metric = "disk"      // percent, the fullest mounted disk
	MetricSDWear    Metric = "sd_wear"   // percent worn
	MetricThrottled Metric = "throttled" // 1 while a Pi is under-volted, capped or throttled NOW
	MetricOffline   Metric = "offline"   // minutes unheard, judged by the authority
)

// Metrics is every metric a rule may watch, in display order.
var Metrics = []Metric{MetricCPUTemp, MetricCPULoad, MetricMemory, MetricDisk, MetricSDWear, MetricThrottled, MetricOffline}

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
	if t, ok := ScopeTag(r.Scope); ok {
		if err := ValidTag(t); err != nil {
			return err
		}
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
		{ID: "throttled", Metric: MetricThrottled, Op: OpGT, Value: 0, Default: true},
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

// applying is the rule in force for node on metric (features/edge/tags.feature): its own scoped rule
// if it has one, else the TIGHTEST rule on any tag it carries, else the fleet rule. ok is false when
// nothing applies (or what applies is off).
//
// The tightest wins among tag rules because a node in two groups must not be judged by the looser of
// them: a bench rule cannot quietly raise a production limit. Tightest means the threshold that
// fires soonest - the lowest for "gt", the highest for "lt" - and a tie is broken by rule id so the
// same rule is chosen on every pass.
func (rs RuleSet) applying(m Metric, nodeID string, tags []string) (Rule, bool) {
	var fleet, tag Rule
	haveFleet, haveTag := false, false
	for _, r := range rs {
		if r.Metric != m || r.Off {
			continue
		}
		switch {
		case r.Scope != "" && r.Scope == nodeID:
			return r, true
		case r.Scope == "":
			fleet, haveFleet = r, true
		default:
			t, isTag := ScopeTag(r.Scope)
			if !isTag || !HasTag(tags, t) {
				continue
			}
			if !haveTag || tighter(r, tag) {
				tag, haveTag = r, true
			}
		}
	}
	if haveTag {
		return tag, true
	}
	return fleet, haveFleet
}

// tighter reports whether a fires sooner than b on the same metric: the lower threshold for "gt",
// the higher for "lt". Equal thresholds are ordered by rule id, so the choice is stable.
func tighter(a, b Rule) bool {
	if a.Value != b.Value {
		if a.Op == OpLT {
			return a.Value > b.Value
		}
		return a.Value < b.Value
	}
	return a.ID < b.ID
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

// alertUnit is the unit an alert names: its owner's name, or - for a unit nobody named, whose record
// name IS its id - what it is (UnitName). An alert is a sentence, and no id is ever a name
// (features/edge/node_identity.feature).
func alertUnit(h NodeHealth) string {
	return UnitName(store.EdgeNode{ID: h.ID, Name: h.Name, Kind: h.Kind}, nil)
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
	if a.Metric == MetricThrottled {
		if a.Firing {
			return fmt.Sprintf("%s: %s is short of power or throttled right now", state, a.Node)
		}
		return fmt.Sprintf("%s: %s is no longer throttled", state, a.Node)
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
	case MetricThrottled:
		if r.PiPower != nil {
			if now, _ := PowerWords(*r.PiPower); len(now) > 0 {
				return 1, true
			}
			return 0, true
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
			r, ok := rules.applying(m, h.ID, h.Tags)
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
				a := Alert{At: now, RuleID: r.ID, NodeID: h.ID, Node: alertUnit(h), Metric: m,
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
		names[h.ID] = alertUnit(h)
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
			a.Node = "a unit no longer on this Edge" // its id is not a name (node_identity.feature)
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

// powerBits are the Pi firmware's get_throttled flags, in the order an owner should read them. The
// same four meanings appear twice: bits 0-3 are NOW, bits 16-19 are "has happened since boot".
var powerBits = []struct {
	bit  uint
	word string
}{{0, "under-voltage"}, {1, "frequency capped"}, {2, "throttled"}, {3, "soft temperature limit"}}

// PowerWords splits a Pi's power word into what is happening now and what has happened since boot.
// Since-boot stays set until the Pi restarts, which is the point: a supply that sagged at 3am is
// still the reason a card got corrupted at 3am.
func PowerWords(flags uint32) (now, since []string) {
	for _, b := range powerBits {
		if flags&(1<<b.bit) != 0 {
			now = append(now, b.word)
		}
		if flags&(1<<(b.bit+16)) != 0 {
			since = append(since, b.word)
		}
	}
	return now, since
}

// PowerLine is a Pi's power in one line every surface prints the same way.
func PowerLine(flags uint32) string {
	now, since := PowerWords(flags)
	switch {
	case len(now) == 0 && len(since) == 0:
		return "good"
	case len(now) == 0:
		return "good now · " + strings.Join(since, ", ") + " since boot"
	case len(since) == 0:
		return strings.Join(now, ", ") + " NOW"
	}
	// What is happening now first; since-boot only adds what "now" did not already say.
	var extra []string
	for _, w := range since {
		if !slices.Contains(now, w) {
			extra = append(extra, w)
		}
	}
	line := strings.Join(now, ", ") + " NOW"
	if len(extra) > 0 {
		line += " · " + strings.Join(extra, ", ") + " since boot"
	}
	return line
}
