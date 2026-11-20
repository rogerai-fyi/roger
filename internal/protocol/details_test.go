package protocol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestDetailsText(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{
		{"", 10, ""},
		{"   ", 10, ""},
		{"a\tb\r\nc\u2028d\u2029e", 20, "a b c d e"},
		{"x\x00\x07\x1b[2J\x7f\u0085\u009by", 20, "x[2Jy"},
		{"\u202aa\u202eb\u2066c\u2069\u200bd\u200fe\ufeff", 20, "abcde"},
		{"é combining", 20, "é combining"},
		{"ok \xff ok", 20, "ok � ok"},
		{strings.Repeat("ab ", 10), 5, "ab ab"},
		{strings.Repeat("火", 10), 3, "火火火"},
	} {
		got := DetailsText(c.in, c.max)
		require.Equal(t, c.want, got, "%q", c.in)
		require.True(t, utf8.ValidString(got))
		require.LessOrEqual(t, utf8.RuneCountInString(got), c.max)
	}
}

func TestReservedUseWords(t *testing.T) {
	for in, want := range map[string]string{
		"Patient triage":           "triage", // reported in list order, not text order
		"DIAGNOSIS":                "diagnosis",
		"target-acquisition drone": "target-acquisition",
		"outpatient-building":      "",
		"weaponsmith":              "",
		"gauges, valves":           "",
	} {
		require.Equal(t, want, reservedUse(in), in)
	}
}

func TestSitePath(t *testing.T) {
	for p, ok := range map[string]bool{
		"/research-wave-nano.html": true, "/a#b": true, "/": false, "": false, "//x": false,
		"https://x/y": false, "javascript:1": false, "/a b": false, "/" + strings.Repeat("a", 200): false,
	} {
		require.Equal(t, ok, SitePath(p), p)
	}
}

func TestValidateDetailsMessages(t *testing.T) {
	require.NoError(t, Taxonomy{NotFor: "Not for diagnosis."}.Validate(""))
	require.EqualError(t, Taxonomy{BestAt: "weapons"}.Validate(""), `best at: reserved use "weapons"`)
	require.EqualError(t, Taxonomy{NotFor: strings.Repeat("n", 201)}.Validate(""), "not for: at most 200 characters (got 201)")
}

func TestNormalizePageAndSource(t *testing.T) {
	// a reader (Canonical) keeps the broker's curated label and a site-path page
	c := Taxonomy{Summary: "s", DetailsSource: "RogerAI", Page: "/p.html"}.Canonical("")
	require.Equal(t, SourceRogerAI, c.DetailsSource)
	require.Equal(t, "/p.html", c.Page)
	// but never a page that is not a site path, nor one on declared text
	require.Empty(t, Taxonomy{Summary: "s", DetailsSource: "rogerai", Page: "https://x"}.Canonical("").Page)
	require.Empty(t, Taxonomy{Summary: "s", Page: "/p.html"}.Canonical("").Page)
	// a node never sends either: Normalize strips them before canonicalizing
	o := ModelOffer{Model: "m", Taxonomy: Taxonomy{Summary: "s", DetailsSource: "rogerai", Page: "/p.html"}}
	o.Normalize()
	require.Equal(t, SourceDeclared, o.DetailsSource)
	require.Empty(t, o.Page)
}

func TestOnAirAttachesDetails(t *testing.T) {
	inf := Taxonomy{Category: "language", Subcategory: "chat", TaxonomySource: SourceInferred}
	got := Taxonomy{Summary: "words"}.OnAir("", inf)
	require.Equal(t, "language", got.Category)
	require.Equal(t, SourceInferred, got.TaxonomySource)
	require.Equal(t, "words", got.Summary)
	require.Equal(t, SourceDeclared, got.DetailsSource)
	require.Equal(t, inf, Taxonomy{}.OnAir("", inf))
}

func TestFoldDetails(t *testing.T) {
	tx := func(s, b, n string) Taxonomy { return Taxonomy{Summary: s, BestAt: b, NotFor: n} }
	for _, c := range []struct {
		name    string
		ballots []Ballot
		want    Details
	}{
		{"empty", nil, Details{}},
		{"no text", []Ballot{{T: Taxonomy{License: "MIT"}, Online: true}}, Details{}},
		{"curated wins", []Ballot{{T: tx("a", "", ""), Online: true},
			{T: Taxonomy{Summary: "R", DetailsSource: "rogerai", Page: "/p"}, Online: false}},
			Details{Summary: "R", Source: SourceRogerAI, Page: "/p"}},
		{"curated bad page", []Ballot{{T: Taxonomy{Summary: "R", DetailsSource: "rogerai", Page: "//x"}}},
			Details{Summary: "R", Source: SourceRogerAI}},
		{"majority", []Ballot{{T: tx("A", "x", ""), Online: true}, {T: tx("A", "x", ""), Online: true}, {T: tx("B", "", ""), Online: true}},
			Details{Summary: "A", BestAt: "x", Source: SourceDeclared, DeclaredBy: 2, Stations: 3}},
		{"tie alphabetical", []Ballot{{T: tx("Z", "", ""), Online: true}, {T: tx("A", "", ""), Online: true}},
			Details{Summary: "A", Source: SourceDeclared, DeclaredBy: 1, Stations: 2}},
		{"never mixed", []Ballot{{T: tx("A", "", ""), Online: true}, {T: tx("B", "b", ""), Online: true}},
			Details{Summary: "A", Source: SourceDeclared, DeclaredBy: 1, Stations: 2}},
		{"proxy only when humans silent", []Ballot{{T: Taxonomy{}, Online: true}, {T: tx("P", "", ""), Online: true, Curated: true}},
			Details{Summary: "P", Source: SourceDeclared, DeclaredBy: 1, Stations: 1}},
		{"proxy ignored when a human speaks", []Ballot{{T: tx("H", "", ""), Online: true}, {T: tx("P", "", ""), Online: true, Curated: true}},
			Details{Summary: "H", Source: SourceDeclared, DeclaredBy: 1, Stations: 1}},
		{"offline only when nothing on air", []Ballot{{T: tx("O", "", "")}},
			Details{Summary: "O", Source: SourceDeclared, DeclaredBy: 1, Stations: 1}},
		{"offline ignored when on air", []Ballot{{T: tx("O", "", "")}, {T: Taxonomy{}, Online: true}}, Details{}},
		{"cleaned on read", []Ballot{{T: tx("a\x1b[1mb", "", ""), Online: true}},
			Details{Summary: "a[1mb", Source: SourceDeclared, DeclaredBy: 1, Stations: 1}},
	} {
		t.Run(c.name, func(t *testing.T) { require.Equal(t, c.want, FoldDetails(c.ballots)) })
	}
	require.True(t, Details{}.Empty())
}

// The spec files spell control characters as escapes: none may hold a raw one (a bidi or
// zero-width character in a source file is itself the attack the spec is about).
func TestDetailsSpecHasNoRawControls(t *testing.T) {
	files, err := filepath.Glob("../../features/details/*")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		for i, r := range string(b) {
			bad := (r < 0x20 && r != '\n') || (r >= 0x7f && r <= 0x9f) || spoofing(r) || r == 0x2014 || r == 0x2013
			require.False(t, bad, "%s: raw %U at byte %d", f, r, i)
		}
	}
}
