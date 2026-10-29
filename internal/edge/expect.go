package edge

// WHAT THE FLEET SHOULD LOOK LIKE (features/edge/drift.feature): the owner's expectations, and the
// drift that is where the fleet does not match them.
//
// Every expectation is about a fact the fleet ALREADY collects from a describe over the node's own
// certificate, so this needs no new data and no new path. Nothing here writes another machine's
// configuration: it reports, and the owner closes the gap. Making a member pull the intended state
// and apply it to itself is the follow-on slice, and it is not pretended here.

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"rogerai.fm/roger/v7/internal/store"
)

// Expectation is one thing the owner holds a unit to.
type Expectation struct {
	What  ExpectWhat `json:"what"`
	Value string     `json:"value,omitempty"` // a version for roger, a band for serves; empty otherwise
	Scope string     `json:"scope,omitempty"` // "" = the fleet, "tag:x" = the group, else a node id
}

// ExpectWhat is the vocabulary, and it is closed: each is computable from what a node reports.
type ExpectWhat string

const (
	// ExpectRoger is a version floor: the unit runs at least this version of roger.
	ExpectRoger ExpectWhat = "roger"
	// ExpectServes is a band the unit has on air, VERIFIED by the serving probe.
	ExpectServes ExpectWhat = "serves"
	// ExpectAgent is that the unit runs a roger declared an agent.
	ExpectAgent ExpectWhat = "agent"
	// ExpectMaintenance is that the unit has maintenance on, so the owner can act on it from elsewhere.
	ExpectMaintenance ExpectWhat = "maintenance"
)

// ExpectWhats lists the vocabulary, in the order the surfaces show it.
func ExpectWhats() []ExpectWhat {
	return []ExpectWhat{ExpectRoger, ExpectServes, ExpectAgent, ExpectMaintenance}
}

// ExpectWhatList is the vocabulary as a sentence, for a refusal that has to name it.
func ExpectWhatList() string {
	out := make([]string, 0, 4)
	for _, w := range ExpectWhats() {
		out = append(out, string(w))
	}
	return strings.Join(out, ", ")
}

// Key is how one expectation is identified: one per thing per scope, so setting it again replaces it.
func (e Expectation) Key() string { return string(e.What) + "|" + e.Scope }

// Words is the expectation as the owner wrote it: "roger 6.13.0", "serves qwen", "agent".
func (e Expectation) Words() string {
	if e.Value == "" {
		return string(e.What)
	}
	return string(e.What) + " " + e.Value
}

// ValidateExpectation refuses an expectation that cannot mean what it says, where it is made rather
// than where it would be judged. takesValue says whether the thing needs one.
func ValidateExpectation(e Expectation) error {
	known := false
	for _, w := range ExpectWhats() {
		known = known || e.What == w
	}
	if !known {
		return fmt.Errorf("%q is not something to expect; there is: %s", string(e.What), ExpectWhatList())
	}
	switch e.What {
	case ExpectRoger:
		if e.Value == "" {
			return errors.New("expecting a roger version names the version, e.g. `roger 6.13.0`")
		}
		if _, ok := parseVersion(e.Value); !ok {
			return fmt.Errorf("%q is not a version", e.Value)
		}
	case ExpectServes:
		if e.Value == "" {
			return errors.New("expecting a band names it, e.g. `serves gpt-oss-20b`")
		}
	default:
		if e.Value != "" {
			return fmt.Errorf("%q takes no value", string(e.What))
		}
	}
	if tag, ok := ScopeTag(e.Scope); ok {
		return ValidTag(tag)
	}
	return nil
}

// ExpectSet is the owner's expectations.
type ExpectSet []Expectation

// applying is the expectation in force for a unit on one thing: its own if it has one, else the
// TIGHTEST of its groups', else the fleet's. The precedence is an alert rule's, and for the same
// reason - a group must not be able to loosen what the owner asked of one machine.
func (es ExpectSet) applying(w ExpectWhat, nodeID string, tags []string) (Expectation, bool) {
	var fleet, tag Expectation
	haveFleet, haveTag := false, false
	for _, e := range es {
		if e.What != w {
			continue
		}
		switch {
		case e.Scope != "" && e.Scope == nodeID:
			return e, true
		case e.Scope == "":
			fleet, haveFleet = e, true
		default:
			t, isTag := ScopeTag(e.Scope)
			if !isTag || !HasTag(tags, t) {
				continue
			}
			if !haveTag || tighterExpectation(e, tag) {
				tag, haveTag = e, true
			}
		}
	}
	if haveTag {
		return tag, true
	}
	return fleet, haveFleet
}

// tighterExpectation reports whether a asks MORE than b. Only a version floor can be compared; for
// everything else two group expectations say the same thing, so the choice is stable by scope.
func tighterExpectation(a, b Expectation) bool {
	if a.What == ExpectRoger && a.Value != b.Value {
		return VersionLess(b.Value, a.Value) // the higher floor asks more
	}
	return a.Scope < b.Scope
}

// DriftState is what is known about one expectation on one unit.
type DriftState string

const (
	// DriftOK is a unit that matches.
	DriftOK DriftState = "ok"
	// DriftOut is a unit that is known not to match.
	DriftOut DriftState = "drift"
	// DriftUnknown is a unit that has not reported the fact this needs. Unknown is NOT wrong.
	DriftUnknown DriftState = "unknown"
)

// Drift is one unit judged against one expectation.
type Drift struct {
	NodeID   string     `json:"node_id"`
	Node     string     `json:"node"`
	Dark     bool       `json:"dark,omitempty"`
	What     ExpectWhat `json:"what"`
	Expected string     `json:"expected"` // the expectation in the owner's words
	Found    string     `json:"found"`    // what the unit reports, or why it is unknown
	State    DriftState `json:"state"`
}

// Judge measures a fleet against the expectations. It returns one Drift per (unit, expectation) that
// is NOT ok, so a fleet in order returns nothing, and checked is how many units were looked at.
func (es ExpectSet) Judge(nodes []store.EdgeNode) (out []Drift, checked int) {
	for _, n := range nodes {
		checked++
		dark := n.Presence == string(PresenceDark)
		for _, w := range ExpectWhats() {
			e, ok := es.applying(w, n.ID, n.Tags)
			if !ok {
				continue
			}
			state, found := judgeOne(e, n)
			if state == DriftOK {
				continue
			}
			out = append(out, Drift{NodeID: n.ID, Node: n.Name, Dark: dark, What: w,
				Expected: e.Words(), Found: found, State: state})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Node != out[j].Node {
			return out[i].Node < out[j].Node
		}
		return out[i].What < out[j].What
	})
	return out, checked
}

// judgeOne measures one unit against one expectation. A fact the unit has not reported is UNKNOWN,
// never drift: "we have not heard" is not "it is wrong", and an owner must be able to tell them apart.
func judgeOne(e Expectation, n store.EdgeNode) (DriftState, string) {
	switch e.What {
	case ExpectRoger:
		newest, any := "", false
		for _, in := range n.Instances {
			if in.Software == nil || in.Software.Roger == "" {
				continue
			}
			any = true
			if newest == "" || VersionLess(newest, in.Software.Roger) {
				newest = in.Software.Roger
			}
		}
		if !any {
			return DriftUnknown, "it has not reported which roger it runs"
		}
		if VersionLess(newest, e.Value) {
			return DriftOut, "runs " + newest
		}
		return DriftOK, "runs " + newest
	case ExpectServes:
		if len(n.Instances) == 0 {
			return DriftUnknown, "it has not reported what it runs"
		}
		for _, in := range n.Instances {
			if (Located{Node: n, Instance: in}).ServesBand(e.Value) {
				return DriftOK, "serves it"
			}
		}
		return DriftOut, "does not serve it"
	case ExpectAgent:
		if len(n.Instances) == 0 {
			return DriftUnknown, "it has not reported what it runs"
		}
		for _, in := range n.Instances {
			if in.Agent {
				return DriftOK, "runs an agent"
			}
		}
		return DriftOut, "runs no agent"
	case ExpectMaintenance:
		if len(n.Caps) == 0 && len(n.Instances) == 0 {
			return DriftUnknown, "it has not reported its capabilities"
		}
		if Declares(n, Maintain) {
			return DriftOK, "maintenance is on"
		}
		return DriftOut, "maintenance is off"
	}
	return DriftUnknown, ""
}

// CleanExpectations orders a set and drops duplicates, keeping the last of each key so a set written
// twice reads as it was last written.
func CleanExpectations(in ExpectSet) ExpectSet {
	byKey := map[string]Expectation{}
	for _, e := range in {
		byKey[e.Key()] = e
	}
	out := make(ExpectSet, 0, len(byKey))
	for _, e := range byKey {
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].What != out[j].What {
			return out[i].What < out[j].What
		}
		return out[i].Scope < out[j].Scope
	})
	return out
}
