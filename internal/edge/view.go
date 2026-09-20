package edge

// THE VIEW RULES, SHARED. The TUI's [3] EDGE screen and the console's EDGE tab are two
// windows on one fleet, and the rules for HOW the fleet is arranged and how a session is
// worded live here so the two can never disagree. A relay drawn under one node in the
// terminal and under another in the browser would be two claims about one path.
//
// Nothing here draws. These functions take records and return an order and words; the
// surfaces do the painting.
//
// Specs: features/edge/topology_view.feature, features/edge/sessions.feature,
// features/edge/console_view.feature.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/store"
)

// Row is one node's place in the drawing. Child/Via record that the node is reached
// THROUGH the relay drawn above it - the hop the view refuses to imply.
type Row struct {
	Node  store.EdgeNode
	Relay bool   // other nodes are reached through this one
	Child bool   // this node is reached through the relay above it
	Via   string // that relay's name
}

// HasLANTransport reports a LAN-direct transport - the one a solid edge means.
func HasLANTransport(n store.EdgeNode) bool {
	for _, t := range n.Transports {
		if t.Kind == "lan" {
			return true
		}
	}
	return false
}

// Via names the relay a node is reached THROUGH, or "" when it is reached directly. A
// node with both transports is NOT relayed: the fleet keeps them in preference order, and
// the preferred one is the LAN link, so the node is drawn once on the edge it actually
// uses.
func Via(n store.EdgeNode) string {
	if HasLANTransport(n) {
		return ""
	}
	for _, t := range n.Transports {
		if t.Kind == "relay" {
			return t.Addr
		}
	}
	return ""
}

// Arrange orders the fleet for drawing: every node hangs off self, and a node reached
// through a relay that is ITSELF on this Edge is drawn immediately under that relay, so
// the hop is a thing you can see rather than a thing you have to know.
//
// It draws EVERY member exactly once, which is the half of the rule that is easy to lose:
// a relay chain, or two nodes that name each other as their relay, must not make a node
// disappear off the fleet view - so anything the walk did not reach is drawn on its own
// dim edge at the end. A relay that is NOT a member is not named at all: the node is
// drawn on a plain relayed edge, because a hop through a node the view cannot show is a
// hop it has not seen.
func Arrange(list []store.EdgeNode) []Row {
	member := make(map[string]bool, len(list))
	for _, n := range list {
		member[n.Name] = true
	}
	via := map[string]string{}
	children := map[string][]store.EdgeNode{}
	for _, n := range list {
		v := Via(n)
		if v == "" || v == n.Name || !member[v] {
			continue
		}
		via[n.ID] = v
		children[v] = append(children[v], n)
	}
	rows := make([]Row, 0, len(list))
	drawn := make(map[string]bool, len(list))
	var emit func(n store.EdgeNode, parent string)
	emit = func(n store.EdgeNode, parent string) {
		if drawn[n.ID] {
			return // a cycle, or a node already placed under its relay
		}
		drawn[n.ID] = true
		rows = append(rows, Row{Node: n, Relay: len(children[n.Name]) > 0, Child: parent != "", Via: parent})
		for _, c := range children[n.Name] {
			emit(c, n.Name)
		}
	}
	for _, n := range list {
		if via[n.ID] == "" {
			emit(n, "")
		}
	}
	for _, n := range list {
		emit(n, "") // whatever a cycle stranded, drawn rather than dropped
	}
	return rows
}

// EscalateLabel is the approved wording, matching the Playbox's own verdict copy: the
// models agent's ruling is that an escalation is the RIGHT CALL, so the label says so
// rather than merely not saying "fault".
const EscalateLabel = "escalate · right call"

// OutcomeLabel is how a session ended, in words. A refusal says WHY; an escalation says it
// was the right call; anything else simply served.
func (s Session) OutcomeLabel() string {
	if s.Outcome == OutcomeRefused {
		out := string(OutcomeRefused)
		if s.Reason != "" {
			out += " · " + s.Reason
		}
		return out
	}
	if s.Escalate {
		return EscalateLabel
	}
	return "served"
}

// SessionGroup is one drawn row: a session and how many identical ones it stands for.
// Grouping is the whole answer to "the count is shown on the edge between them, not as
// new boxes".
type SessionGroup struct {
	Session Session
	Count   int
}

func groupKey(s Session) string {
	return strings.Join([]string{
		string(s.Kind), s.Who, s.Via, s.Left, s.Station, s.Band,
		strconv.FormatBool(s.Escalate), string(s.Outcome), s.Reason, s.Where,
	}, "\x00")
}

// GroupSessions folds identical sessions into rows, busiest first, then newest, then by
// request id so the order is deterministic.
func GroupSessions(list []Session) []SessionGroup {
	var rows []SessionGroup
	at := map[string]int{}
	for _, s := range list {
		k := groupKey(s)
		if i, ok := at[k]; ok {
			rows[i].Count++
			continue
		}
		at[k] = len(rows)
		rows = append(rows, SessionGroup{Session: s, Count: 1})
	}
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			a, b := rows[j-1], rows[j]
			less := b.Count > a.Count ||
				(b.Count == a.Count && b.Session.At > a.Session.At) ||
				(b.Count == a.Count && b.Session.At == a.Session.At && b.Session.Request < a.Session.Request)
			if !less {
				break
			}
			rows[j-1], rows[j] = b, a
		}
	}
	return rows
}

// Age is how long ago, in the one word every Edge surface uses: "6m", "3h", "2d". A
// negative duration (a clock that ran backwards) reads as now rather than as nonsense.
func Age(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
