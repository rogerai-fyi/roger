package operator

// brand.go - the guest-operator BRAND PLATES as pure registry DATA
// (docs-internal/GUEST-OPERATOR-PLATES.md, founder-approved 2026-07-06).
//
// Policy: "ONE HUE, ONE BEAT." During the PATCHING YOU THROUGH transition ONLY,
// the guest WORDMARK may carry its single canonical hue; everything else on the
// plate stays mono + RogerAI red. THE DESK roster + /operator picker stay 100%
// mono+red (no picker glyphs for any guest, §6). NO_COLOR / ROGERAI_ASCII
// collapse per the doc's §7 fallback matrix; narrow widths SWAP to a one-line
// text lockup - shipped brand art is never cropped or re-wrapped.
//
// Provenance: every art block is re-derived byte-exact from the guest's own
// shipped artifacts (opencode --help wordmark v1.17.x · hermes banner.py 0.16.x
// incl. their gradient hexes · pyfiglet small `aider` + logo.svg green · the
// Claude Code 2.1.202 mascot + binary hue · Codex's terminal-native `>_` coding
// motif, given dimensional half-block planes). The only non-shipped values are the two derived
// light-mode hexes #0E7A0E (aider) and #B85F41 (claude) - contrast-driven
// darkenings of the canonical hue, flagged for founder taste (doc §8).
//
// This package stays render-free (zero lipgloss/bubbletea deps): inks are named
// tokens + adaptive hex pairs; internal/tui maps them to the house styles.

// Ink tokens: the house styles a span may reference (resolved by internal/tui).
const (
	InkDim     = "dim"     // stDim (cDim) - secondary / labels
	InkBrand   = "brand"   // stBrand (cInk bold) - headline lettering
	InkKey     = "key"     // stKey (cInk bold) - the load-bearing value
	InkRed     = "red"     // cRed NON-BOLD - a glint (the opencode cursor stack)
	InkRedBold = "redBold" // stRed (cRed bold) - the reserved red beat
)

// BrandInk is one named ink: either a house token (Token set) or a custom
// adaptive hue (Dark/Light hex pair) with an optional Bold weight. The zero
// value renders plain (unstyled).
type BrandInk struct {
	Token string // one of the Ink* tokens; "" = custom hue or plain
	Dark  string // canonical hex on a dark terminal ("" with empty Token = plain)
	Light string // light-terminal collapse/derivation ("" = reuse Dark)
	Bold  bool
}

// BrandSpan styles the half-open rune-column range [From, To) of a row.
type BrandSpan struct {
	From, To int
	Ink      BrandInk
}

// BrandRow is one art row: exact text plus either a whole-row Ink (Spans empty)
// or per-segment Spans (columns not covered render plain - they are spaces in
// every shipped plate).
type BrandRow struct {
	Text  string
	Ink   BrandInk
	Spans []BrandSpan
}

// BrandArt is one guest's finished plate: the full-color/unicode art rows, the
// one-line text lockup (the §*c/§7 ASCII + narrow fallback), the wordmark width
// that gates the narrow swap (full art renders whenever termWidth >= 2 + Width),
// and whether the art itself survives a pure-ASCII terminal (aider only).
type BrandArt struct {
	Rows     []BrandRow
	Width    int      // the wordmark width in cells (narrow threshold = 2 + Width)
	Lockup   BrandRow // the one-line text lockup (ASCII mode + narrow widths)
	ASCIIArt bool     // true = the art is pure ASCII by construction (no lockup swap in ASCII mode)
}

// The custom hues the doc registers (§8): dark canonical / light pair.
var (
	inkGold1 = BrandInk{Dark: "#FFD700", Light: "#B8860B", Bold: true} // hermes rows 1-2 (shipped step 1; light = their banner_dim)
	inkGold2 = BrandInk{Dark: "#FFBF00", Light: "#B8860B"}             // hermes rows 3-4 (step 2)
	inkGold3 = BrandInk{Dark: "#CD7F32", Light: "#B8860B"}             // hermes rows 5-6 (step 3)
	inkGreen = BrandInk{Dark: "#14B014", Light: "#0E7A0E"}             // aider logo.svg green (light derived)
	inkClay  = BrandInk{Dark: "#D97757", Light: "#B85F41"}             // claude binary hue (light derived)
	inkClayB = BrandInk{Dark: "#D97757", Light: "#B85F41", Bold: true} // claude wordmark
)

// BrandArts returns all five live desk plates keyed by guest name. Returned fresh per
// call (the Registry() idiom) so callers can never corrupt the shared art.
func BrandArts() map[string]*BrandArt {
	return map[string]*BrandArt{
		// §1 opencode - the exact wordmark `opencode --help` prints (v1.17.x),
		// leading braille-blank U+2800 kept on row 1 for character-exactness.
		// Two-tone: `open` cDim / `code` cInk - their real grey/white brand mapped
		// to the house ink ramp (the "honestly mono two-tone" policy line). The
		// ONE red is the block-cursor glint at col 41 (▄/█/▀, cRed NON-bold).
		"opencode": {
			Rows: []BrandRow{
				{Text: "⠀                                ▄", // the d ascender, col 33
					Spans: []BrandSpan{{From: 33, To: 34, Ink: BrandInk{Token: InkBrand}}}},
				{Text: "█▀▀█ █▀▀█ █▀▀█ █▀▀▄ █▀▀▀ █▀▀█ █▀▀█ █▀▀█  ▄", Spans: opencodeLetterSpans()},
				{Text: "█  █ █  █ █▀▀▀ █  █ █    █  █ █  █ █▀▀▀  █", Spans: opencodeLetterSpans()},
				{Text: "▀▀▀▀ █▀▀▀ ▀▀▀▀ ▀  ▀ ▀▀▀▀ ▀▀▀▀ ▀▀▀▀ ▀▀▀▀  ▀", Spans: opencodeLetterSpans()},
			},
			Width: 42,
			Lockup: BrandRow{Text: "opencode _", Spans: []BrandSpan{ // §1c: the honest ASCII cursor
				{From: 0, To: 4, Ink: BrandInk{Token: InkDim}},
				{From: 4, To: 8, Ink: BrandInk{Token: InkKey}},
				{From: 9, To: 10, Ink: BrandInk{Token: InkRedBold}},
			}},
		},
		// §2 hermes - the 51-col ANSI Shadow HERMES (their full HERMES-AGENT lockup
		// is 101 cols and busts the 96-col budget), top-lit 3-step gold exactly as
		// banner.py maps it; light terminals collapse to their own #B8860B dim-gold
		// via the adaptive pairs. Byline right-aligned like a signature (cols 38-50).
		"hermes": {
			Rows: []BrandRow{
				{Text: "██╗  ██╗███████╗██████╗ ███╗   ███╗███████╗███████╗", Ink: inkGold1},
				{Text: "██║  ██║██╔════╝██╔══██╗████╗ ████║██╔════╝██╔════╝", Ink: inkGold1},
				{Text: "███████║█████╗  ██████╔╝██╔████╔██║█████╗  ███████╗", Ink: inkGold2},
				{Text: "██╔══██║██╔══╝  ██╔══██╗██║╚██╔╝██║██╔══╝  ╚════██║", Ink: inkGold2},
				{Text: "██║  ██║███████╗██║  ██║██║ ╚═╝ ██║███████╗███████║", Ink: inkGold3},
				{Text: "╚═╝  ╚═╝╚══════╝╚═╝  ╚═╝╚═╝     ╚═╝╚══════╝╚══════╝", Ink: inkGold3},
				{Text: "                                      nous research",
					Spans: []BrandSpan{{From: 38, To: 51, Ink: BrandInk{Token: InkDim}}}},
			},
			Width: 51,
			Lockup: BrandRow{Text: "H E R M E S · nous research", Spans: []BrandSpan{
				{From: 0, To: 11, Ink: inkGold1},
				{From: 11, To: 27, Ink: BrandInk{Token: InkDim}},
			}},
		},
		// §3 aider - figlet `small` lowercase, pure ASCII by construction (its own
		// ASCII fallback). One hue, no gradient, NO cursor glint (explicit ruling:
		// adding red here would double the accents). Tagline reads as a sentence.
		"aider": {
			Rows: []BrandRow{
				{Text: "      _    _", Ink: inkGreen},
				{Text: " __ _(_)__| |___ _ _", Ink: inkGreen},
				{Text: "/ _` | / _` / -_) '_|", Ink: inkGreen},
				{Text: "\\__,_|_\\__,_\\___|_|", Ink: inkGreen},
				{Text: "ai pair programming in your terminal", Ink: BrandInk{Token: InkDim}},
			},
			// The tagline (36 cells) is the WIDEST art row, so IT gates the narrow swap -
			// not the 21-cell wordmark. Threshold 2+36=38: below it the plate swaps whole to
			// the "aider" lockup rather than hard-truncating the tagline mid-word (truncVisible
			// cuts with no ellipsis - a clipped "ai pair programming i" reads as broken and
			// breaks §7's "shipped art is never cropped" promise). Iteration-2 fix (carried c).
			Width:    36,
			Lockup:   BrandRow{Text: "aider", Ink: inkGreen},
			ASCIIArt: true,
		},
		// §4 claude - LIVE since v5.4.4 (the context-only guest). CHARACTER-EXACT to the
		// mascot Claude Code 2.1.220 prints on its own welcome, captured from the real
		// binary: three rows, no more (the earlier draft carried an invented fourth "ears"
		// row - ▗ appears nowhere in the shipped art). The wordmark sits beside row 1 and
		// the vendor byline beside row 2, at the same column the real welcome aligns its
		// version/model lines to, so the plate reads the way the guest's own banner does.
		// Byline in dim, the hermes "nous research" pattern. One hue throughout.
		"claude": {
			Rows: []BrandRow{
				{Text: " ▐▛███▜▌   Claude Code", Spans: []BrandSpan{
					{From: 0, To: 8, Ink: inkClay},
					{From: 11, To: 22, Ink: inkClayB},
				}},
				{Text: "▝▜█████▛▘  anthropic", Spans: []BrandSpan{
					{From: 0, To: 9, Ink: inkClay},
					{From: 11, To: 20, Ink: BrandInk{Token: InkDim}},
				}},
				{Text: "  ▘▘ ▝▝", Ink: inkClay},
			},
			Width:  22,
			Lockup: BrandRow{Text: "* Claude Code", Ink: inkClay}, // ✳ pre-folded to * (house asciiFold idiom)
		},
		// §5 codex - a terminal-native dimensional `>_` coding motif. OpenAI's brand is
		// hueless, so highlight/body/shadow depth stays on the house ink ramp and the
		// ▄▄▄▄ underscore is the single Roger-red cursor beat.
		"codex": {
			Rows: []BrandRow{
				{Text: "█▄", Spans: []BrandSpan{{From: 0, To: 2, Ink: BrandInk{Token: InkBrand}}}},
				{Text: " ▀█▄     codex", Spans: []BrandSpan{
					{From: 1, To: 4, Ink: BrandInk{Token: InkBrand}},
					{From: 9, To: 14, Ink: BrandInk{Token: InkKey}},
				}},
				{Text: " ▄█▀     openai", Spans: []BrandSpan{
					{From: 1, To: 4, Ink: BrandInk{Token: InkBrand}},
					{From: 9, To: 15, Ink: BrandInk{Token: InkDim}},
				}},
				{Text: "█▀ ▄▄▄▄", Spans: []BrandSpan{
					{From: 0, To: 2, Ink: BrandInk{Token: InkBrand}},
					{From: 3, To: 7, Ink: BrandInk{Token: InkRedBold}},
				}},
			},
			Width:  15,
			Lockup: BrandRow{Text: ">_ codex · openai"}, // plain: no hue, honestly
		},
		// §6 dsh - the DeepSeek Harness (founder 2026-08-21: add it to the desk).
		//
		// HONESTY NOTE, because it breaks this file's rule. Every plate above is a
		// character-exact reproduction of the wordmark that tool actually prints:
		// opencode's `--help` mark, hermes's banner.py, codex's own glyph. dsh prints
		// NO banner - its identity is a whale glyph and a "deepseek HARNESS" lockup in
		// the web UI, neither of which is an ASCII artifact to copy. So this one is
		// COMPOSED, in the same block family as its neighbours, from their real name.
		// It is the house's drawing of their name, not their drawing, and the next
		// reader should know that rather than assume it was traced like the rest.
		//
		// Two-tone ink rather than their blue (#4D6BFE): the desk maps every guest to
		// the house ramp so five plates read as one shelf, and hermes's gold is the
		// single exception because its banner IS the gold.
		"dsh": {
			Rows: []BrandRow{
				{Text: "██████╗ ███████╗██╗  ██╗", Ink: BrandInk{Token: InkKey}},
				{Text: "██╔══██╗██╔════╝██║  ██║", Ink: BrandInk{Token: InkKey}},
				{Text: "██║  ██║███████╗███████║", Ink: BrandInk{Token: InkKey}},
				{Text: "██║  ██║╚════██║██╔══██║", Ink: BrandInk{Token: InkDim}},
				{Text: "██████╔╝███████║██║  ██║", Ink: BrandInk{Token: InkDim}},
				{Text: "╚═════╝ ╚══════╝╚═╝  ╚═╝", Ink: BrandInk{Token: InkDim}},
				// Right-aligned under the mark like hermes's byline: the wordmark is 24
				// runes and "deepseek harness" is 16, so it starts at col 8 and the plate
				// stays exactly 24 wide. (First pass had it 28 wide against a declared 27 -
				// caught by the span lock, which is what that lock is for.)
				{Text: "        deepseek harness",
					Spans: []BrandSpan{{From: 8, To: 24, Ink: BrandInk{Token: InkDim}}}},
			},
			Width: 24,
			Lockup: BrandRow{Text: "dsh · deepseek harness", Spans: []BrandSpan{
				{From: 0, To: 3, Ink: BrandInk{Token: InkKey}},
				{From: 6, To: 22, Ink: BrandInk{Token: InkDim}},
			}},
		},
	}
}

// opencodeLetterSpans is the §1a per-row style table for rows 2-4: `open` cols
// 0-18 in cDim, `code` cols 20-38 in cInk(stBrand), and the red cursor glint at
// col 41 in cRed NON-BOLD (a glint, not a surface - never stRed). A fresh slice
// per row keeps BrandArts() free of shared mutable state.
func opencodeLetterSpans() []BrandSpan {
	return []BrandSpan{
		{From: 0, To: 19, Ink: BrandInk{Token: InkDim}},
		{From: 20, To: 39, Ink: BrandInk{Token: InkBrand}},
		{From: 41, To: 42, Ink: BrandInk{Token: InkRed}},
	}
}
