package tui

// Units for the Edge session layer's drawing: the geometry at every width the screen can
// be, the path a session is drawn along, the outcome wording, and the two degradations
// (a bounded block, and an ASCII console).

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
)

func sess(kind edge.Initiator, who, band, station string) edge.Session {
	return edge.Session{Request: "r-" + who + band + station, Kind: kind, Who: who,
		Band: band, Station: station, Outcome: edge.OutcomeServed}
}

func TestEdgeSessGeomFitsEveryWidth(t *testing.T) {
	for _, w := range []int{200, 120, 100, 80, 72, 60, 56, 40, 20} {
		g := edgeGeomFor(w, 3)
		sg := edgeSessGeomFor(g)
		total := sg.lead + sg.nameW + 1 + sg.bandW + 1 + sg.pathW + 1 + sg.outW
		require.LessOrEqual(t, total, max(w, 7), "the columns overflow the terminal at w=%d", w)
		require.GreaterOrEqual(t, sg.nameW, 0, w)
		require.GreaterOrEqual(t, sg.outW, 0, w)
		if w >= 80 {
			require.Equal(t, w, total, "at a real width the row fills the terminal exactly")
			require.GreaterOrEqual(t, sg.outW, len([]rune("REFUSED · over-limit")),
				"the outcome column must fit what it has to say")
		}
	}
}

func TestEdgeSessGlyphTellsTheThreeApart(t *testing.T) {
	plain := sess(edge.FromUse, "", "m", "st")
	esc := plain
	esc.Escalate = true
	ref := plain
	ref.Outcome, ref.Reason, ref.Station = edge.OutcomeRefused, edge.RefusedNoStation, ""
	refEsc := ref
	refEsc.Escalate = true

	require.Equal(t, edgeGlyphSession, edgeSessGlyph(plain))
	require.Equal(t, edgeGlyphEscalate, edgeSessGlyph(esc))
	require.Equal(t, edgeGlyphRefused, edgeSessGlyph(ref))
	require.Equal(t, edgeGlyphRefused, edgeSessGlyph(refEsc),
		"a refused escalation is a refusal: it must not be drawn as though it reached a model")
	require.NotEqual(t, edgeGlyphSession, edgeGlyphEscalate)
	require.NotEqual(t, edgeGlyphEscalate, edgeGlyphRefused)
	require.NotEqual(t, edgeGlyphSession, edgeGlyphRefused)
}

func TestEdgeSessPathIsThePathItTook(t *testing.T) {
	base := sess(edge.FromUse, "", "m", "house-cb")
	viaS := base
	viaS.Via = "relay-box"
	foS := base
	foS.Left = "cold-cb"
	both := base
	both.Via, both.Left = "relay-box", "cold-cb"
	none := base
	none.Station, none.Outcome, none.Reason = "", edge.OutcomeRefused, edge.RefusedNoStation

	for _, c := range []struct {
		name string
		row  edgeSessRow
		want string
	}{
		{"direct", edgeSessRow{s: base, n: 1}, "→ house-cb"},
		{"through a relay, in order", edgeSessRow{s: viaS, n: 1}, "→ relay-box → house-cb"},
		{"a failover names both", edgeSessRow{s: foS, n: 1}, "→ cold-cb ✕ → house-cb"},
		{"relay and failover, in order", edgeSessRow{s: both, n: 1}, "→ relay-box → cold-cb ✕ → house-cb"},
		{"nothing served it", edgeSessRow{s: none, n: 1}, "—"},
		{"a busy edge carries its count", edgeSessRow{s: base, n: 30}, "→ house-cb ×30"},
	} {
		t.Run(c.name, func(t *testing.T) { require.Equal(t, c.want, edgeSessPath(c.row)) })
	}
}

func TestEdgeSessOutcomeReadsHonestly(t *testing.T) {
	plain := sess(edge.FromUse, "", "m", "st")
	esc := plain
	esc.Escalate = true
	ref := plain
	ref.Outcome, ref.Reason = edge.OutcomeRefused, edge.RefusedOverLimit
	bare := plain
	bare.Outcome = edge.OutcomeRefused

	require.Equal(t, "served", edgeSessOutcome(plain))
	require.Equal(t, edgeEscalateLabel, edgeSessOutcome(esc))
	require.Contains(t, edgeSessOutcome(esc), "right call")
	require.Equal(t, "REFUSED · over-limit", edgeSessOutcome(ref))
	require.Equal(t, "REFUSED", edgeSessOutcome(bare), "a refusal with no reason still says it was refused")
}

func TestEdgeSessRowsGroupAndOrder(t *testing.T) {
	a := sess(edge.FromAgent, "", "m", "st")
	a.At = 10
	b := sess(edge.FromUse, "", "m", "st")
	b.At = 20
	list := []edge.Session{a, a, a, b}
	rows := edgeSessRows(list)
	require.Len(t, rows, 2, "identical sessions are one edge with a count")
	require.Equal(t, 3, rows[0].n, "the busiest edge is drawn first")
	require.Equal(t, edge.FromAgent, rows[0].s.Kind)
	require.Equal(t, 1, rows[1].n)

	// Anything the row SHOWS makes it a different edge.
	c := a
	c.Band = "another-band"
	require.Len(t, edgeSessRows([]edge.Session{a, c}), 2)
	require.Empty(t, edgeSessRows(nil))
}

func TestEdgeSessBlockIsBoundedAndSaysSo(t *testing.T) {
	m := browseSeed(100)
	for i := 0; i < edgeSessMaxRows+5; i++ {
		s := sess(edge.FromGuest, "guest-"+string(rune('a'+i)), "m", "st")
		m.edge.sessions = append(m.edge.sessions, s)
	}
	var out []string
	m.edgeSessionBlock(edgeGeomFor(100, 1), func(s string) { out = append(out, s) })
	joined := stripANSI(strings.Join(out, "\n"))
	require.Contains(t, joined, "busiest of 13")
	marks := strings.Count(joined, edgeGlyphSession)
	require.Equal(t, edgeSessMaxRows, marks, "the block must stop drawing, not scroll the terminal")
}

func TestEdgeSessBlockIsAbsentWithNoTraffic(t *testing.T) {
	m := browseSeed(100)
	var out []string
	m.edgeSessionBlock(edgeGeomFor(100, 1), func(s string) { out = append(out, s) })
	require.Empty(t, out, "a quiet fleet draws no block at all")
	require.Nil(t, m.edgeSessions(), "with no ledger wired there are no sessions")
}

func TestEdgeSessionGlyphsFoldForALegacyConsole(t *testing.T) {
	t.Setenv("ROGERAI_ASCII", "1")
	folded := edgeFold(edgeGlyphSession + edgeGlyphEscalate + edgeGlyphRefused)
	require.NotContains(t, folded, edgeGlyphSession)
	seen := map[rune]bool{}
	for _, r := range folded {
		require.False(t, seen[r], "the three marks folded onto the same character: %q", folded)
		seen[r] = true
	}
}
