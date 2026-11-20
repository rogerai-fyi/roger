package about

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/protocol"
)

func TestHeading(t *testing.T) {
	for _, c := range []struct {
		name string
		d    protocol.Details
		want string
	}{
		{"nothing", protocol.Details{}, ""},
		{"curated", protocol.Details{Summary: "s", Source: protocol.SourceRogerAI}, "RogerAI's description of this model"},
		{"declared many", protocol.Details{Summary: "s", Source: protocol.SourceDeclared, DeclaredBy: 2, Stations: 3}, "the station's own words · 2 of 3 stations"},
		{"declared one", protocol.Details{BestAt: "b", Source: protocol.SourceDeclared, DeclaredBy: 1, Stations: 1}, "the station's own words · 1 of 1 station"},
	} {
		t.Run(c.name, func(t *testing.T) { require.Equal(t, c.want, Heading(c.d)) })
	}
}

func TestKind(t *testing.T) {
	for _, c := range []struct {
		modality     string
		vision       bool
		tuning, base string
		want         string
	}{
		{"", false, "", "", "text in, text out"},
		{"chat", true, "", "", "text and image in, text out"},
		{"tts", true, "", "", "text in, speech out"},
		{"stt", false, "", "", "speech in, text out"},
		{"chat", false, "adapter", "demo-1b", "text in, text out  ·  adapter on demo-1b"},
		{"chat", false, "adapter", "", "text in, text out  ·  adapter"},
		{"chat", false, "instruct", "base", "text in, text out  ·  instruct"},
	} {
		require.Equal(t, c.want, Kind(c.modality, c.vision, c.tuning, c.base))
	}
}

func TestLines(t *testing.T) {
	d := protocol.Details{Summary: "Sum.", BestAt: "Best.", NotFor: "Not.", Source: protocol.SourceRogerAI, Page: "/p.html"}
	page := func(p string) string { return "x" + p }
	t.Run("adapter: base on the kind line, page shown", func(t *testing.T) {
		got := Lines(Model{Tuning: "adapter", BaseModel: "b", License: "MIT", Details: d, Siblings: []string{"s1"}}, page)
		require.Equal(t, []Line{{"", "Sum."}, {"best at", "Best."}, {"not for", "Not."},
			{"kind", "text in, text out  ·  adapter on b"}, {"same base", "s1"}, {"license", "MIT"}, {"page", "x/p.html"}}, got)
	})
	t.Run("not an adapter: a base line", func(t *testing.T) {
		got := Lines(Model{Tuning: "instruct", BaseModel: "b", Details: protocol.Details{Summary: "s"}}, nil)
		require.Contains(t, got, Line{"base", "b"})
		require.NotContains(t, got, Line{"page", ""})
	})
	t.Run("siblings capped", func(t *testing.T) {
		got := Lines(Model{BaseModel: "b", Siblings: []string{"a", "b", "c", "d", "e", "f"}}, nil)
		require.Contains(t, got, Line{"same base", "a, b, c, d +2 more"})
	})
	t.Run("no page func", func(t *testing.T) {
		for _, l := range Lines(Model{Details: d}, nil) {
			require.NotEqual(t, "page", l.Label)
		}
	})
}

func TestPad(t *testing.T) {
	require.Equal(t, "", Pad(""))
	require.Equal(t, "best at   ", Pad("best at"))
	require.Equal(t, "same base ", Pad("same base"))
	require.Equal(t, strings.Repeat(" ", LabelWidth), Pad("      "))
	require.Equal(t, "a-very-long-label ", Pad("a-very-long-label"))
}

func TestFactsAndBlock(t *testing.T) {
	ballots := []protocol.Ballot{
		{T: protocol.Taxonomy{Summary: "Other.", Tuning: "base", BaseModel: "x", License: "X"}, Online: true},
		{T: protocol.Taxonomy{Summary: "Win.", Tuning: "adapter", BaseModel: "demo", License: "MIT"}, Online: true},
		{T: protocol.Taxonomy{Summary: "Win.", Tuning: "adapter", BaseModel: "demo", License: "MIT"}, Online: true},
	}
	d := protocol.FoldDetails(ballots)
	tu, base, lic := Facts(ballots, d)
	require.Equal(t, []string{"adapter", "demo", "MIT"}, []string{tu, base, lic})

	// no text anywhere: the first station that states each fact
	tu, base, lic = Facts([]protocol.Ballot{{T: protocol.Taxonomy{License: "L"}}, {T: protocol.Taxonomy{Tuning: "Merge", BaseModel: "B"}}}, protocol.Details{})
	require.Equal(t, []string{"merge", "B", "L"}, []string{tu, base, lic})

	// an unknown tuning (or an escape sequence in one) is never printed
	tu, _, _ = Facts([]protocol.Ballot{{T: protocol.Taxonomy{Tuning: "\x1b[2Jadapter"}}}, protocol.Details{})
	require.Empty(t, tu)

	blk := Block(d, ballots, "https://site/")
	require.Equal(t, map[string]any{"source": "declared", "summary": "Win.", "tuning": "adapter", "base_model": "demo",
		"license": "MIT", "declared_by": 2, "stations": 3}, blk)

	cur := protocol.Details{Summary: "S", Source: protocol.SourceRogerAI, Page: "/p.html"}
	blk = Block(cur, []protocol.Ballot{{T: protocol.Taxonomy{Summary: "S", DetailsSource: "rogerai", Page: "/p.html"}, Online: true}}, "https://site/")
	require.Equal(t, "https://site/p.html", blk["page"])
	require.NotContains(t, blk, "declared_by")
}

func TestSameBase(t *testing.T) {
	require.Equal(t, "demo-1b", SameBase(" DEMO-1B "))
	require.Equal(t, "", SameBase("\u200b"))
}
