package tui

// THE SESSION LAYER of the Edge screen - "a node is a thing; a session is a thing
// happening". It draws a second layer over the SAME graph edge.go already draws.
//
// THE RULE THE WHOLE LAYER OBEYS: it is a WINDOW. Everything it draws was already
// authorized and already receipted by the relay path; there is no hook, no command and no
// key here that can open a session, spend anything, or reach a station. Looking at the
// Edge costs nothing because there is nothing here to cost.
//
// Three things it will not do:
//   - draw a session to a station that merely COULD serve the band. Same rule the topology
//     already obeys for links: the graph shows what happened, not what is possible.
//   - let a session become a node. Many sessions between the same two participants are one
//     row carrying a COUNT, never new boxes, so a busy fleet cannot redraw itself.
//   - draw an escalation as a fault. ESCALATE is the models' strongest measured skill and
//     renders in the positive style, reading as the right call
//     (features/web/playbox_edge_honesty.feature).
//
// Spec: features/edge/sessions.feature.

import (
	"strconv"
	"strings"

	"rogerai.fm/roger/v6/internal/edge"
)

// The session vocabulary. Three DIFFERENT glyphs, not three colours: the TUI's palette is
// mono, so a session, an escalation and a refusal have to be told apart by shape - which
// is also what keeps them apart under NO_COLOR, in a pipe and on a legacy console.
const (
	edgeGlyphSession  = "▸" // traffic that happened
	edgeGlyphEscalate = "▲" // an escalation: the right call, drawn UP the chain
	edgeGlyphRefused  = "✕" // nothing served it
	edgeGlyphHop      = "→" // one hop of the path a session really took
	edgeGlyphTimes    = "×" // the count on a busy edge
)

// edgeEscalateLabel is the approved wording, matching the Playbox's own verdict copy
// ("escalate · right call"): the models agent's ruling is that an escalation is the RIGHT
// CALL, so the label says so rather than merely not saying "fault".
const edgeEscalateLabel = "escalate · right call"

// edgeSessMaxRows bounds the block. Fifty sessions must stay legible, so the busiest
// edges are shown with their counts and the rest are counted off rather than drawn.
const edgeSessMaxRows = 8

// edgeSessRow is one drawn row: a session, and how many identical ones it stands for.
type edgeSessRow struct {
	s edge.Session
	n int
}

// edgeSessGeom is the measured layout of the session block for one width, derived from the
// graph's own geometry so the two blocks line up.
type edgeSessGeom struct {
	lead  int // the glyph column plus its space
	nameW int
	bandW int
	pathW int
	outW  int
}

func edgeSessGeomFor(g edgeGeom) edgeSessGeom {
	// edgeSessOutMin is what the OUTCOME column must keep. It is reserved FIRST and given
	// up last, because it is the column that says whether anything was served: an elided
	// "REFUSED · over-limit" is the one cell on this screen that must never be a guess.
	const edgeSessOutMin = 20
	sg := edgeSessGeom{lead: 4, nameW: g.nameW, bandW: 14, pathW: 28}
	fits := func() bool {
		return sg.lead+sg.nameW+1+sg.bandW+1+sg.pathW+1+edgeSessOutMin <= g.w
	}
	// Given up in order of what a narrow terminal can most afford to lose: the band
	// abbreviates, then the attribution, and the PATH last - it carries the stations.
	for sg.bandW > 8 && !fits() {
		sg.bandW--
	}
	for sg.nameW > 8 && !fits() {
		sg.nameW--
	}
	for sg.pathW > 12 && !fits() {
		sg.pathW--
	}
	sg.outW = g.w - (sg.lead + sg.nameW + 1 + sg.bandW + 1 + sg.pathW + 1)
	if sg.outW < 0 {
		// Past the last honest degradation. Rather than let a padded cell push the row
		// off the end (where the clip would silently eat the OUTCOME - the one column
		// that says whether anything was served), give the space back from the right.
		over := -sg.outW
		for _, col := range []*int{&sg.pathW, &sg.bandW, &sg.nameW} {
			take := min(*col, over)
			*col, over = *col-take, over-take
			if over == 0 {
				break
			}
		}
		sg.outW = 0
	}
	return sg
}

// edgeSessions is the frame's session snapshot, read once in refreshEdge like everything
// else the view draws: a session recorded mid-frame can never produce half a frame.
func (m model) edgeSessions() []edge.Session {
	if m.hooks.EdgeSessions == nil {
		return nil
	}
	return m.hooks.EdgeSessions.Live()
}

// edgeSessKey is what makes two sessions the SAME edge on the graph: the same participant
// asking the same band of the same station, the same way, ending the same. Anything that
// differs is a different edge and gets its own row; anything identical is a count.
func edgeSessKey(s edge.Session) string {
	return strings.Join([]string{
		string(s.Kind), s.Who, s.Via, s.Left, s.Station, s.Band,
		strconv.FormatBool(s.Escalate), string(s.Outcome), s.Reason,
	}, "\x00")
}

// edgeSessRows groups the snapshot into drawn rows, busiest first. Grouping is the whole
// answer to "the session count is shown on the edge between them, not as new boxes".
func edgeSessRows(list []edge.Session) []edgeSessRow {
	var rows []edgeSessRow
	at := map[string]int{}
	for _, s := range list {
		k := edgeSessKey(s)
		if i, ok := at[k]; ok {
			rows[i].n++
			continue
		}
		at[k] = len(rows)
		rows = append(rows, edgeSessRow{s: s, n: 1})
	}
	// Busiest first, then newest, then by request so the order is deterministic.
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			a, b := rows[j-1], rows[j]
			less := b.n > a.n ||
				(b.n == a.n && b.s.At > a.s.At) ||
				(b.n == a.n && b.s.At == a.s.At && b.s.Request < a.s.Request)
			if !less {
				break
			}
			rows[j-1], rows[j] = b, a
		}
	}
	return rows
}

// edgeSessGlyph is the row's mark. An escalation is NOT a warning: it gets its own
// positive glyph, drawn up the chain.
func edgeSessGlyph(s edge.Session) string {
	switch {
	case s.Outcome == edge.OutcomeRefused:
		return edgeGlyphRefused
	case s.Escalate:
		return edgeGlyphEscalate
	default:
		return edgeGlyphSession
	}
}

// edgeSessPath is the path the session really TOOK, in order: the relay it went through
// (drawn as its own hop, so it is visible rather than implied), the station a failover
// walked away from, and the station that served. A busy edge carries its count here,
// because the count belongs on the edge rather than beside it.
func edgeSessPath(r edgeSessRow) string {
	var hops []string
	if r.s.Via != "" {
		hops = append(hops, r.s.Via)
	}
	if r.s.Left != "" {
		hops = append(hops, r.s.Left+" "+edgeGlyphRefused)
	}
	if r.s.Station != "" {
		hops = append(hops, r.s.Station)
	}
	out := "—"
	if len(hops) > 0 {
		out = edgeGlyphHop + " " + strings.Join(hops, " "+edgeGlyphHop+" ")
	}
	if r.n > 1 {
		out += " " + edgeGlyphTimes + strconv.Itoa(r.n)
	}
	return out
}

// edgeSessOutcome is how it ended, in words. A refusal says WHY; an escalation says it was
// the right call; anything else simply served.
func edgeSessOutcome(s edge.Session) string {
	if s.Outcome == edge.OutcomeRefused {
		out := string(edge.OutcomeRefused)
		if s.Reason != "" {
			out += " · " + s.Reason
		}
		return out
	}
	if s.Escalate {
		return edgeEscalateLabel
	}
	return "served"
}

// edgeSessLine draws one row. Every cell is padded to its measured column and the finished
// line is clipped by the caller, the same discipline the graph rows keep.
func (m model) edgeSessLine(sg edgeSessGeom, r edgeSessRow) string {
	return "  " + edgeSessGlyph(r.s) + " " +
		pad(r.s.Attribution(), sg.nameW) + " " +
		pad(r.s.Band, sg.bandW) + " " +
		pad(edgeSessPath(r), sg.pathW) + " " +
		pad(edgeSessOutcome(r.s), sg.outW)
}

// edgeSessionBlock draws the traffic this Edge carried. With no traffic there is no block
// at all: a still screen means a quiet fleet, exactly as the still graph does.
func (m model) edgeSessionBlock(g edgeGeom, line func(string)) {
	rows := edgeSessRows(m.edge.sessions)
	if len(rows) == 0 {
		return
	}
	sg := edgeSessGeomFor(g)
	line("")
	head := "SESSIONS · " + plural(len(m.edge.sessions), "session")
	if len(rows) > edgeSessMaxRows {
		head += " · showing the " + strconv.Itoa(edgeSessMaxRows) + " busiest of " + strconv.Itoa(len(rows))
	}
	line("  " + stDim.Render(head))
	for i, r := range rows {
		if i >= edgeSessMaxRows {
			break
		}
		line(m.edgeSessLine(sg, r))
	}
}
