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
	"rogerai.fm/roger/v6/internal/protocol"
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
	// TWO COLUMNS ARE PROTECTED, and a live run at 80 columns is what proved it. An
	// earlier ladder gave the band up first and drew "gpt-oss…", which makes gpt-oss-120b
	// and gpt-oss-20b the same string on a screen whose whole job is naming the band the
	// owner is watching. It also clipped the outcome to "escalate · right ca…", which is
	// the one label the approved framing says must read as the right call.
	//
	// So: the OUTCOME keeps room for the longest thing it ever says, the BAND is given up
	// last, and the attribution and the path are what a narrow terminal spends.
	edgeSessOutMin := max(len([]rune(edgeEscalateLabel)), len([]rune("REFUSED · over-limit")))
	// The attribution column is NOT the graph's name column: it holds "roger use",
	// "agent", "console" or a guest/board name, none of which need a node column's width.
	// Found live at 88 columns, where the graph's 22 left the PATH too narrow to name a
	// real broker station ("house-or-wave-pico-29…"), and the station that served is the
	// thing the row exists to say.
	sg := edgeSessGeom{lead: 4, nameW: min(g.nameW, 16), bandW: 14, pathW: 32}
	fits := func() bool {
		return sg.lead+sg.nameW+1+sg.bandW+1+sg.pathW+1+edgeSessOutMin <= g.w
	}
	for _, step := range []struct {
		col   *int
		floor int
	}{
		{&sg.pathW, 26}, {&sg.nameW, 12}, {&sg.pathW, 12}, {&sg.nameW, 10}, {&sg.bandW, 13},
	} {
		for *step.col > step.floor && !fits() {
			*step.col--
		}
	}
	// Past the last honest degradation the outcome takes its floor back by force, from
	// the path first and the band last. Nothing here can push the row off the end: what
	// the clip would eat is the right-hand column, which is the one that must survive.
	remaining := func() int {
		return g.w - (sg.lead + sg.nameW + 1 + sg.bandW + 1 + sg.pathW + 1)
	}
	for _, col := range []*int{&sg.pathW, &sg.nameW, &sg.bandW} {
		need := edgeSessOutMin - remaining()
		if need <= 0 {
			break
		}
		take := min(*col, need)
		*col -= take
	}
	sg.outW = max(0, remaining())
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

// edgeSessHops is the path the session really TOOK, in order: the relay it went through
// (drawn as its own hop, so it is visible rather than implied), the station a failover
// walked away from, and the station that served.
func edgeSessHops(s edge.Session) string {
	var hops []string
	if s.Via != "" {
		hops = append(hops, s.Via)
	}
	if s.Left != "" {
		hops = append(hops, s.Left+" "+edgeGlyphRefused)
	}
	if s.Station != "" {
		hops = append(hops, s.Station)
	}
	if len(hops) == 0 {
		return "—"
	}
	return edgeGlyphHop + " " + strings.Join(hops, " "+edgeGlyphHop+" ")
}

// edgeSessCount is the count a busy edge carries. It belongs ON the edge rather than
// beside it, so it lives in the path cell.
func edgeSessCount(n int) string {
	if n <= 1 {
		return ""
	}
	return " " + edgeGlyphTimes + strconv.Itoa(n)
}

// edgeSessPath is the whole path cell's text: the hops, then the count.
func edgeSessPath(r edgeSessRow) string { return edgeSessHops(r.s) + edgeSessCount(r.n) }

// edgeSessPathCell pins the count to the RIGHT of the column and elides only the hops.
//
// Found live at 64 columns: padding the whole string cut "→ house-cb ×31" down to
// "→ house-cb …", which makes an edge that carried thirty-one sessions read as one. The
// count is the load-bearing half of a busy edge, so it is the half that cannot move.
func edgeSessPathCell(r edgeSessRow, w int) string {
	c := edgeSessCount(r.n)
	return pad(edgeSessHops(r.s), max(0, w-len([]rune(c)))) + c
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
		edgeSessPathCell(r, sg.pathW) + " " +
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

// recordEdgeSession puts a turn the TUI's own agent just took onto the Edge.
//
// It is DERIVED, not bookkept: the only input is the broker's own signed receipt, which
// the client already decodes for the reply footer. A turn with no receipt is not drawn -
// not dimmed, not pending, not drawn - because the Edge is a window onto receipted
// traffic. This is the whole write path from the TUI into the ledger, and it cannot run
// unless a relay already happened and was already billed.
func (m *model) recordEdgeSession(rec protocol.UsageReceipt) {
	if m.hooks.EdgeSessions == nil || rec.RequestID == "" {
		return
	}
	_, _ = m.hooks.EdgeSessions.Record(edge.Traffic{
		Account:  m.hooks.EdgeSessions.Account(),
		Kind:     edge.FromAgent,
		Request:  rec.RequestID,
		Receipts: []protocol.UsageReceipt{rec},
	})
}
