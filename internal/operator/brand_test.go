package operator

// brand_test.go - golden pins for the guest-operator BRAND PLATES
// (rogerai-internal-docs/GUEST-OPERATOR-PLATES.md, founder-approved 2026-07-06,
// "ONE HUE, ONE BEAT"). The art below is transcribed byte-exact from the doc's
// code blocks (span boundaries machine-verified against the doc before landing);
// these tests exist so a stray edit can never corrupt a shipped wordmark: every
// row's exact bytes, every span boundary, every hue pair, and the §6 "no picker
// glyphs" ruling are pinned here. Data-only against the Guest.Brand (BrandArt) seam -
// transition logic is untouched and untested here (handoff specs own it).

import (
	"reflect"
	"strings"
	"testing"
)

// TestBrandLockupsHaveNoRawEllipsis is PREVENTIVE (iteration-2 finding): glyphs.Fold expands
// a raw "…" to "..." (one rune -> three cells) under ASCII, which would silently shift a
// lockup's column-based spans and misalign the wordmark. No shipped lockup may carry one - a
// future guest that wants an ellipsis must spell it out. This guards the whole registry,
// including the dormant claude/codex drafts.
func TestBrandLockupsHaveNoRawEllipsis(t *testing.T) {
	for name, art := range BrandArts() {
		if strings.Contains(art.Lockup.Text, "…") {
			t.Errorf("%s lockup %q carries a raw ellipsis '…' - glyphs.Fold expands it to '...' and shifts the spans; spell it out instead", name, art.Lockup.Text)
		}
	}
}

// The doc's hue registrations (§8): dark canonical / light collapse pairs.
var (
	wantGold1 = BrandInk{Dark: "#FFD700", Light: "#B8860B", Bold: true} // hermes rows 1-2
	wantGold2 = BrandInk{Dark: "#FFBF00", Light: "#B8860B"}             // hermes rows 3-4
	wantGold3 = BrandInk{Dark: "#CD7F32", Light: "#B8860B"}             // hermes rows 5-6
	wantGreen = BrandInk{Dark: "#14B014", Light: "#0E7A0E"}             // aider
	wantClay  = BrandInk{Dark: "#D97757", Light: "#B85F41"}             // claude
	wantClayB = BrandInk{Dark: "#D97757", Light: "#B85F41", Bold: true} // claude wordmark
)

// TestBrandArtsExactBytes pins EVERY art row of EVERY plate character-exact to the
// approved doc (§1a, §2a, §3a, §4a, §5a) plus each one-line lockup (§1c-§5c).
func TestBrandArtsExactBytes(t *testing.T) {
	arts := BrandArts()
	cases := []struct {
		name   string
		rows   []string
		lockup string
	}{
		{
			name: "opencode",
			rows: []string{
				"⠀                                ▄",
				"█▀▀█ █▀▀█ █▀▀█ █▀▀▄ █▀▀▀ █▀▀█ █▀▀█ █▀▀█  ▄",
				"█  █ █  █ █▀▀▀ █  █ █    █  █ █  █ █▀▀▀  █",
				"▀▀▀▀ █▀▀▀ ▀▀▀▀ ▀  ▀ ▀▀▀▀ ▀▀▀▀ ▀▀▀▀ ▀▀▀▀  ▀",
			},
			lockup: "opencode _",
		},
		{
			name: "hermes",
			rows: []string{
				"██╗  ██╗███████╗██████╗ ███╗   ███╗███████╗███████╗",
				"██║  ██║██╔════╝██╔══██╗████╗ ████║██╔════╝██╔════╝",
				"███████║█████╗  ██████╔╝██╔████╔██║█████╗  ███████╗",
				"██╔══██║██╔══╝  ██╔══██╗██║╚██╔╝██║██╔══╝  ╚════██║",
				"██║  ██║███████╗██║  ██║██║ ╚═╝ ██║███████╗███████║",
				"╚═╝  ╚═╝╚══════╝╚═╝  ╚═╝╚═╝     ╚═╝╚══════╝╚══════╝",
				"                                      nous research",
			},
			lockup: "H E R M E S · nous research",
		},
		{
			name: "aider",
			rows: []string{
				"      _    _",
				" __ _(_)__| |___ _ _",
				"/ _` | / _` / -_) '_|",
				"\\__,_|_\\__,_\\___|_|",
				"ai pair programming in your terminal",
			},
			lockup: "aider",
		},
		{
			name: "claude",
			// Character-exact to what `claude` 2.1.220 prints on its own welcome, captured
			// from the real binary. The earlier draft carried a fourth "ears" row that the
			// shipped art does not have (▗ appears nowhere in it).
			rows: []string{
				" ▐▛███▜▌   Claude Code",
				"▝▜█████▛▘  anthropic",
				"  ▘▘ ▝▝",
			},
			lockup: "* Claude Code",
		},
		{
			name: "codex",
			rows: []string{
				"█▄",
				" ▀█▄     codex",
				" ▄█▀     openai",
				"█▀ ▄▄▄▄",
			},
			lockup: ">_ codex · openai",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			art := arts[tc.name]
			if art == nil {
				t.Fatalf("BrandArts() must carry %q", tc.name)
			}
			if len(art.Rows) != len(tc.rows) {
				t.Fatalf("%s: %d rows, want %d", tc.name, len(art.Rows), len(tc.rows))
			}
			for i, want := range tc.rows {
				if art.Rows[i].Text != want {
					t.Fatalf("%s row %d corrupted:\n got %q\nwant %q", tc.name, i+1, art.Rows[i].Text, want)
				}
			}
			if art.Lockup.Text != tc.lockup {
				t.Fatalf("%s lockup: got %q, want %q", tc.name, art.Lockup.Text, tc.lockup)
			}
		})
	}
	if len(arts) != 5 {
		t.Fatalf("BrandArts() carries exactly the five doc plates, got %d", len(arts))
	}
}

// TestBrandArtWidths pins each plate's narrow-swap width (§7: full art renders
// whenever termWidth >= 2 + artWidth; opencode 42, hermes 51, aider 36 (the tagline
// is the WIDEST row, so it gates the swap - iteration-2 fix so the tagline never
// clips), claude 22 (row 1: 8-cell mascot + gap + the "Claude Code" wordmark), codex 15).
func TestBrandArtWidths(t *testing.T) {
	want := map[string]int{"opencode": 42, "hermes": 51, "aider": 36, "claude": 22, "codex": 15}
	arts := BrandArts()
	for name, w := range want {
		if got := arts[name].Width; got != w {
			t.Fatalf("%s width: got %d, want %d", name, got, w)
		}
	}
}

// TestBrandOpencodeSpans pins §1a's per-row style table: `open` cDim cols 0-18,
// `code` cInk(stBrand) cols 20-38, the red block-cursor glint at col 41 in cRed
// NON-BOLD (a glint, not stRed), row 1's d-ascender at col 33 in stBrand, and the
// §1c lockup spans (open dim / code key / `_` stRed).
func TestBrandOpencodeSpans(t *testing.T) {
	art := BrandArts()["opencode"]
	if got, want := art.Rows[0].Spans, []BrandSpan{{From: 33, To: 34, Ink: BrandInk{Token: InkBrand}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("row 1 spans: got %+v, want %+v", got, want)
	}
	wantLetterRows := []BrandSpan{
		{From: 0, To: 19, Ink: BrandInk{Token: InkDim}},
		{From: 20, To: 39, Ink: BrandInk{Token: InkBrand}},
		{From: 41, To: 42, Ink: BrandInk{Token: InkRed}}, // the ONE red: non-bold cursor glint
	}
	for i := 1; i <= 3; i++ {
		if !reflect.DeepEqual(art.Rows[i].Spans, wantLetterRows) {
			t.Fatalf("row %d spans: got %+v, want %+v", i+1, art.Rows[i].Spans, wantLetterRows)
		}
	}
	wantLockup := []BrandSpan{
		{From: 0, To: 4, Ink: BrandInk{Token: InkDim}},
		{From: 4, To: 8, Ink: BrandInk{Token: InkKey}},
		{From: 9, To: 10, Ink: BrandInk{Token: InkRedBold}},
	}
	if !reflect.DeepEqual(art.Lockup.Spans, wantLockup) {
		t.Fatalf("lockup spans: got %+v, want %+v", art.Lockup.Spans, wantLockup)
	}
	if art.ASCIIArt {
		t.Fatal("opencode half-blocks do not survive ASCII - must swap to the lockup")
	}
}

// TestBrandHermesGradient pins §2a: rows 1-2 bold #FFD700, rows 3-4 #FFBF00, rows
// 5-6 #CD7F32, ALL collapsing to #B8860B on light terminals (rows 1-2 keep bold),
// and the byline `nous research` right-aligned cols 38-50 in stDim.
func TestBrandHermesGradient(t *testing.T) {
	art := BrandArts()["hermes"]
	wantInks := []BrandInk{wantGold1, wantGold1, wantGold2, wantGold2, wantGold3, wantGold3}
	for i, want := range wantInks {
		if got := art.Rows[i].Ink; got != want {
			t.Fatalf("row %d ink: got %+v, want %+v", i+1, got, want)
		}
		if len(art.Rows[i].Spans) != 0 {
			t.Fatalf("row %d: the gradient is whole-row ink, not spans", i+1)
		}
	}
	wantByline := []BrandSpan{{From: 38, To: 51, Ink: BrandInk{Token: InkDim}}}
	if !reflect.DeepEqual(art.Rows[6].Spans, wantByline) {
		t.Fatalf("byline spans: got %+v, want %+v", art.Rows[6].Spans, wantByline)
	}
	wantLockup := []BrandSpan{
		{From: 0, To: 11, Ink: wantGold1},
		{From: 11, To: 27, Ink: BrandInk{Token: InkDim}},
	}
	if !reflect.DeepEqual(art.Lockup.Spans, wantLockup) {
		t.Fatalf("lockup spans: got %+v, want %+v", art.Lockup.Spans, wantLockup)
	}
	if art.ASCIIArt {
		t.Fatal("hermes box runes are non-ASCII - must swap to the lockup")
	}
}

// TestBrandAiderInks pins §3a: the whole wordmark in one hue #14B014 (light
// #0E7A0E, derived), the tagline in stDim, NO cursor glint anywhere (the doc's
// explicit "do not add" ruling), and §3c: the art IS its own ASCII fallback.
func TestBrandAiderInks(t *testing.T) {
	art := BrandArts()["aider"]
	for i := 0; i < 4; i++ {
		if got := art.Rows[i].Ink; got != wantGreen {
			t.Fatalf("row %d ink: got %+v, want %+v", i+1, got, wantGreen)
		}
	}
	if got := art.Rows[4].Ink; got != (BrandInk{Token: InkDim}) {
		t.Fatalf("tagline ink: got %+v, want stDim", got)
	}
	for i, row := range art.Rows {
		for _, sp := range row.Spans {
			if sp.Ink.Token == InkRed || sp.Ink.Token == InkRedBold {
				t.Fatalf("row %d: no red glint on the aider plate (doc §3a)", i+1)
			}
		}
	}
	if !art.ASCIIArt {
		t.Fatal("aider is pure ASCII by construction - the full plate survives ROGERAI_ASCII")
	}
	if got := art.Lockup.Ink; got != wantGreen {
		t.Fatalf("narrow lockup keeps the phosphor green, got %+v", got)
	}
}

// TestBrandClaudeLockupSpans pins §4a: the mascot in #D97757 (#B85F41 light) across all
// three shipped rows, the "Claude Code" wordmark bold beside row 1 (cols 11-22), the dim
// "anthropic" byline beside row 2 (cols 11-20), and the §4c `* Claude Code` lockup (the
// ✳ spark pre-folded to * per the house asciiFold idiom).
func TestBrandClaudeLockupSpans(t *testing.T) {
	art := BrandArts()["claude"]
	if len(art.Rows) != 3 {
		t.Fatalf("claude art has %d rows, want the 3 the shipped welcome prints", len(art.Rows))
	}
	// Row 3 is the whole-row hue; rows 1-2 carry the mascot plus their text spans.
	if got := art.Rows[2].Ink; got != wantClay {
		t.Fatalf("row 3 ink: got %+v, want %+v", got, wantClay)
	}
	wantRow1 := []BrandSpan{
		{From: 0, To: 8, Ink: wantClay},
		{From: 11, To: 22, Ink: wantClayB}, // the wordmark, bold
	}
	if !reflect.DeepEqual(art.Rows[0].Spans, wantRow1) {
		t.Fatalf("row 1 spans: got %+v, want %+v", art.Rows[0].Spans, wantRow1)
	}
	wantRow2 := []BrandSpan{
		{From: 0, To: 9, Ink: wantClay},
		{From: 11, To: 20, Ink: BrandInk{Token: InkDim}}, // the vendor byline, dim
	}
	if !reflect.DeepEqual(art.Rows[1].Spans, wantRow2) {
		t.Fatalf("row 2 spans: got %+v, want %+v", art.Rows[1].Spans, wantRow2)
	}
	if got := art.Lockup.Ink; got != wantClay {
		t.Fatalf("lockup ink: got %+v, want %+v", got, wantClay)
	}
	if art.ASCIIArt {
		t.Fatal("quadrant blocks do not survive ASCII - must swap to the lockup")
	}
}

// TestBrandCodexSpans pins §5a: the chevron in stBrand, wordmark stKey, credit
// stDim, and the `▄▄▄▄` underscore as the plate's ONE red beat in stRed - plus the
// plain-text §5c lockup (no ink: OpenAI's brand is honestly hueless).
func TestBrandCodexSpans(t *testing.T) {
	art := BrandArts()["codex"]
	want := [][]BrandSpan{
		{{From: 0, To: 2, Ink: BrandInk{Token: InkBrand}}},
		{{From: 1, To: 4, Ink: BrandInk{Token: InkBrand}}, {From: 9, To: 14, Ink: BrandInk{Token: InkKey}}},
		{{From: 1, To: 4, Ink: BrandInk{Token: InkBrand}}, {From: 9, To: 15, Ink: BrandInk{Token: InkDim}}},
		{{From: 0, To: 2, Ink: BrandInk{Token: InkBrand}}, {From: 3, To: 7, Ink: BrandInk{Token: InkRedBold}}},
	}
	for i, w := range want {
		if !reflect.DeepEqual(art.Rows[i].Spans, w) {
			t.Fatalf("row %d spans: got %+v, want %+v", i+1, art.Rows[i].Spans, w)
		}
	}
	if art.Lockup.Ink != (BrandInk{}) || len(art.Lockup.Spans) != 0 {
		t.Fatalf("codex lockup is plain text, got ink %+v spans %+v", art.Lockup.Ink, art.Lockup.Spans)
	}
}

// TestRegistryCarriesBrandArts: every launchable guest carries its plate as registry data.
func TestRegistryCarriesBrandArts(t *testing.T) {
	arts := BrandArts()
	byName := map[string]Guest{}
	for _, g := range Registry() {
		byName[g.Name] = g
	}
	for _, name := range []string{"opencode", "hermes", "aider", "claude", "codex"} {
		g, ok := byName[name]
		if !ok || g.Brand == nil {
			t.Fatalf("%s must carry its BrandArt in the registry", name)
		}
		if !reflect.DeepEqual(g.Brand, arts[name]) {
			t.Fatalf("%s registry plate must equal BrandArts()[%q]", name, name)
		}
	}
}

// §6 "no picker glyphs" is now enforced structurally: the vestigial single-accent
// BrandGlyph seam was deleted (iteration-2 minimization), so there is no field a guest
// could ever carry a picker glyph on, and the picker renderer has no glyph slot. The
// identity moment lives only on the PATCHING plate - one hue, one beat, once.

// TestBrandSpansWithinRows pins a cross-plate invariant the per-plate tests cannot: every
// span must address runes that actually exist in its row. A From/To computed against the
// wrong index base (these rows are block characters, 3 bytes each) or left behind after an
// edit would otherwise silently mis-colour, or index past the end of, a shipped plate.
func TestBrandSpansWithinRows(t *testing.T) {
	for name, art := range BrandArts() {
		if art == nil {
			t.Fatalf("%s: nil plate", name)
		}
		rows := append(append([]BrandRow{}, art.Rows...), art.Lockup)
		for i, r := range rows {
			n := len([]rune(r.Text))
			for j, sp := range r.Spans {
				switch {
				case sp.From < 0 || sp.To < 0:
					t.Errorf("%s row %d span %d: negative bounds %+v", name, i+1, j, sp)
				case sp.From > sp.To:
					t.Errorf("%s row %d span %d: From %d is past To %d", name, i+1, j, sp.From, sp.To)
				case sp.To > n:
					t.Errorf("%s row %d span %d: To %d is past the row's %d runes (%q)",
						name, i+1, j, sp.To, n, r.Text)
				}
			}
		}
		// The declared Width gates the narrow swap, so it must be the widest row - a Width
		// under it would let the plate render clipped rather than swapping to the lockup.
		widest := 0
		for _, r := range art.Rows {
			if n := len([]rune(r.Text)); n > widest {
				widest = n
			}
		}
		if art.Width != widest {
			t.Errorf("%s: Width %d but the widest row is %d runes", name, art.Width, widest)
		}
	}
}
