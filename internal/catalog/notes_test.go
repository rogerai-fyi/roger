package catalog

import (
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/protocol"
)

func TestBuiltinNotesShip(t *testing.T) {
	n, err := Builtin()
	require.NoError(t, err)
	for _, id := range []string{"wave-nano-chat", "wave-nano-fire-controller", "wave-nano-fire-vision", "wave-pico-293m"} {
		require.True(t, n.Has(id), id)
	}
}

func TestNotesApply(t *testing.T) {
	ns, err := ParseNotes([]byte(`[{"id":"Demo-1B","summary":"S","best_at":"B","not_for":"N","base_model":"base","license":"MIT","page":"/p.html"},
		{"id":"bare","summary":"S2"}]`))
	require.NoError(t, err)

	got := ns.Apply("demo-1b\u200b", protocol.Taxonomy{Summary: "station", License: "X", Category: "language"})
	require.Equal(t, protocol.Taxonomy{Summary: "S", BestAt: "B", NotFor: "N", BaseModel: "base", License: "MIT",
		DetailsSource: protocol.SourceRogerAI, Page: "/p.html", Category: "language"}, got)

	got = ns.Apply("bare", protocol.Taxonomy{License: "X", BaseModel: "Y"})
	require.Equal(t, "X", got.License) // a note without a license keeps the station's
	require.Equal(t, "Y", got.BaseModel)

	// no note: a "rogerai" label is demoted and a page dropped
	got = ns.Apply("other", protocol.Taxonomy{Summary: "s", DetailsSource: protocol.SourceRogerAI, Page: "/p.html"})
	require.Equal(t, protocol.SourceDeclared, got.DetailsSource)
	require.Empty(t, got.Page)
	got = ns.Apply("other", protocol.Taxonomy{})
	require.Empty(t, got.DetailsSource)

	var none Notes
	require.False(t, none.Has("demo-1b"))
	require.Equal(t, "s", none.Apply("demo-1b", protocol.Taxonomy{Summary: "s"}).Summary)
}

func TestPageURLAndDisplay(t *testing.T) {
	require.Equal(t, "", PageURL("https://x/", ""))
	require.Equal(t, "https://x/p.html", PageURL("https://x/", "/p.html"))
	require.Equal(t, "rogerai.fm/p.html#f", PageDisplay("/p.html#f"))
}

func TestNoteRejectsStrayWhitespace(t *testing.T) {
	_, err := ParseNotes([]byte(`[{"id":"a","summary":"s ","page":"/p.html"}]`))
	require.ErrorContains(t, err, "control character or stray whitespace")
	_, err = ParseNotes([]byte(`[{"id":"a","summary":"s","license":"MIT\n"}]`))
	require.ErrorContains(t, err, "control character")
}
