package catalog

import (
	"encoding/json"
	"os"
	"regexp"
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

// The shipped notes are outward copy: they keep the model pages' rules
// (web/test/model-pages.test.mjs BANNED) and carry no build or run identifier.
func TestBuiltinNotesKeepOutwardCopyRules(t *testing.T) {
	banned := []*regexp.Regexp{
		regexp.MustCompile(`(?i)hugging\s?face`),
		regexp.MustCompile(`(?i)\b(first|novel|unprecedented)\b|state[- ]of[- ]the[- ]art`),
		regexp.MustCompile(`(?i):\d{4}\b|localhost|127\.0\.0\.1|systemd|\.service\b|/mnt/|~/|hermes|hf_serve|serve-wave|workstation`),
		regexp.MustCompile(`(?i)FIgLib|HPWREN|pyro-?sdis|PyroNear|SKAB|\bNAB\b|ALERTCalifornia|SigLIP|Idefics|SmolLM`),
		regexp.MustCompile(`(?i)\bcorpus\b|billion tokens|\b90B\b|\btokens of\b|dataset`),
		regexp.MustCompile(`(?i)\bClaude\b|AI assistant|revenue`),
		regexp.MustCompile(`\b[A-Z]\d{2,}\b|\bv\d+[a-z]\b|\bseed \d|\bR\.\d+`), // run ids, build versions, R-numbers
		regexp.MustCompile(`[\x{2013}\x{2014}]`),                               // no en or em dashes
	}
	b, err := os.ReadFile("notes.json")
	require.NoError(t, err)
	var list []Note
	require.NoError(t, json.Unmarshal(b, &list))
	for _, n := range list {
		for _, v := range []string{n.Summary, n.BestAt, n.NotFor, n.BaseModel, n.License} {
			for _, re := range banned {
				require.False(t, re.MatchString(v), "%s: %q matches %s", n.ID, v, re)
			}
		}
	}
}

// The two fire models are specialized models behind a market guard, not chat models (lab gate,
// 2026-11-20). Their notes must say so, say what input they accept, say they are decision
// support and not a listed fire alarm or a substitute for emergency services, and carry their
// measured numbers with the date they were read.
func TestFireNotesStateSpecializedInputAndDecisionSupport(t *testing.T) {
	ns, err := Builtin()
	require.NoError(t, err)
	date := regexp.MustCompile(`\b20\d\d-\d\d-\d\d\b`)
	number := regexp.MustCompile(`\d+(\.\d+)?%|\b0\.\d+\b`)
	for id, input := range map[string]*regexp.Regexp{
		"wave-nano-fire-controller": regexp.MustCompile(`(?i)\btick\b`),
		"wave-nano-fire-vision":     regexp.MustCompile(`(?i)\bimage\b.*\byes or no\b`),
	} {
		n := ns[NoteID(id)]
		require.Regexp(t, `(?i)\bnot (a )?chat\b`, n.Summary, id)
		require.Regexp(t, input, n.Summary, id)
		require.Regexp(t, `(?i)decision support`, n.NotFor, id)
		require.Regexp(t, `(?i)fire alarm`, n.NotFor, id)
		require.Regexp(t, `(?i)emergency`, n.NotFor, id)
		if id == "wave-nano-fire-vision" { // the module does not carry over to cameras unlike its training ones yet
			require.Regexp(t, `(?i)cameras unlike`, n.NotFor, id)
			require.Regexp(t, `(?i)license`, n.NotFor, id)
		}
		require.Regexp(t, number, n.BestAt, id)
		require.Regexp(t, date, n.BestAt, id)
	}
}
