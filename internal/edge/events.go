package edge

// THE EVENTS TIMELINE (features/edge/events.feature): what happened on this Edge, in one place,
// newest first. It is a READ, merged from the two places the Edge already writes - each node's own
// history, and the alert log's crossings - so nothing new is recorded for it.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"rogerai.fm/roger/v7/internal/store"
)

// MaxNodeHistory bounds one node's history. Fourteen places append to it and nothing trimmed it, so
// a node on a flaky network grew a record that is re-read on every CLI call. Generous enough that an
// owner looking for what happened last week still finds it.
const MaxNodeHistory = 200

// EventKind is where a line came from: the node's own record, or a crossing in the alert log.
type EventKind string

const (
	EventNode  EventKind = "node"
	EventAlert EventKind = "alert"
)

// Event is one line of the timeline. Count is how many times the SAME thing happened in a row on the
// same unit, collapsed into this line: an activity view that prints one fact forty times hides the
// other thirty-nine. At is the newest of them, Since the oldest.
type Event struct {
	At     time.Time `json:"at"`
	Kind   EventKind `json:"kind"`
	NodeID string    `json:"node_id,omitempty"`
	Node   string    `json:"node"`
	What   string    `json:"what"`
	Detail string    `json:"detail,omitempty"`
	By     string    `json:"by,omitempty"`    // who asked: the caller's node id, for audit (never in Detail)
	Count  int       `json:"count,omitempty"` // >1 when repeats were collapsed
	Since  time.Time `json:"since,omitempty"` // the oldest of the collapsed run
}

// EventQuery narrows a timeline read.
type EventQuery struct {
	NodeID string        // "" = every unit
	Since  time.Duration // 0 = no age bound
	Limit  int           // 0 = DefaultEventLimit
}

// DefaultEventLimit is how many lines a timeline returns when the caller does not say.
const DefaultEventLimit = 100

// Events is the merged timeline. alerts may be nil (an alert log that could not be read is not a
// reason to lose the node events - the caller says so in its own words); nodes is the fleet as read.
func Events(nodes []store.EdgeNode, alerts []Alert, q EventQuery, now time.Time) []Event {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultEventLimit
	}
	var cutoff time.Time
	if q.Since > 0 {
		cutoff = now.Add(-q.Since)
	}
	keep := func(id string, at time.Time) bool {
		if q.NodeID != "" && id != q.NodeID {
			return false
		}
		return cutoff.IsZero() || !at.Before(cutoff)
	}
	out := make([]Event, 0, limit)
	names := UnitNames(nodes) // an unnamed unit is called by what it is, never by its id
	for _, n := range nodes {
		for _, e := range n.History {
			at := time.Unix(e.At, 0)
			if !keep(n.ID, at) {
				continue
			}
			out = append(out, Event{At: at, Kind: EventNode, NodeID: n.ID, Node: names[n.ID], What: e.What,
				Detail: actorDetail(e, names), By: e.By})
		}
	}
	for _, a := range alerts {
		if !keep(a.NodeID, a.At) {
			continue
		}
		out = append(out, Event{At: a.At, Kind: EventAlert, NodeID: a.NodeID, Node: a.Node,
			What: alertEventWhat(a), Detail: alertEventDetail(a)})
	}
	// Newest first, with a stable tie-break so two events in the same second do not swap between
	// reads: unit, then kind, then words.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.At.Equal(b.At) {
			return a.At.After(b.At)
		}
		if a.Node != b.Node {
			return a.Node < b.Node
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.What < b.What
	})
	out = collapseRepeats(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// collapseRepeats folds a RUN of identical events on one unit into a single line with a count. They
// are adjacent because the list is sorted by time, so this never joins two runs that something else
// happened between.
func collapseRepeats(in []Event) []Event {
	out := make([]Event, 0, len(in))
	for _, e := range in {
		if n := len(out) - 1; n >= 0 {
			if p := out[n]; p.NodeID == e.NodeID && p.Kind == e.Kind && p.What == e.What && p.Detail == e.Detail {
				if out[n].Count == 0 {
					out[n].Count = 1
				}
				out[n].Count++
				out[n].Since = e.At // the list is newest-first, so each repeat is older
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// alertEventWhat is the timeline's word for a crossing: the same raised/cleared vocabulary the other
// surfaces use, with the rule in it.
func alertEventWhat(a Alert) string {
	state := "cleared"
	if a.Firing {
		state = "raised"
	}
	if a.Test {
		state += " (test)"
	}
	return state + " " + string(a.Metric)
}

// alertEventDetail is the reading and the line it crossed, in the words the trouble surfaces use.
func alertEventDetail(a Alert) string {
	if a.Metric == MetricOffline {
		if a.Firing {
			return fmt.Sprintf("unheard for %s, past %s", Age(time.Duration(a.DarkForS)*time.Second), FormatValue(a.Metric, a.Threshold))
		}
		return "heard again"
	}
	if a.Unreported {
		return a.Node + " no longer reports " + unreportedReading(a.Metric)
	}
	if !a.Firing {
		return fmt.Sprintf("%s, back under %s", FormatValue(a.Metric, a.Value), FormatValue(a.Metric, a.Threshold))
	}
	return fmt.Sprintf("%s, over %s", FormatValue(a.Metric, a.Value), FormatValue(a.Metric, a.Threshold))
}

// ActorDetail is an event's sentence as shown to people. The sentence names whoever asked as they
// were called then; when that member has been renamed since, it adds what it is called now. A
// member no longer on the Edge adds nothing, and the id is never said.
func ActorDetail(e store.EdgeEvent, nodes []store.EdgeNode) string {
	if e.By == "" {
		return e.Detail
	}
	return actorDetail(e, UnitNames(nodes))
}

func actorDetail(e store.EdgeEvent, names map[string]string) string {
	now, ok := names[e.By]
	if e.By == "" || !ok || now == "" || strings.HasSuffix(e.Detail, " "+now) {
		return e.Detail
	}
	return e.Detail + " (now " + now + ")"
}

// capHistoryOn trims a node's history to the newest MaxNodeHistory events.
func capHistoryOn(n *store.EdgeNode) {
	if len(n.History) > MaxNodeHistory {
		n.History = append([]store.EdgeEvent(nil), n.History[len(n.History)-MaxNodeHistory:]...)
	}
}
