package operator

// brand.go - the guest-operator BRAND PLATES as pure registry DATA
// (rogerai-internal-docs/GUEST-OPERATOR-PLATES.md, founder-approved 2026-07-06).
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
// Claude Code 2.1.202 mascot + binary hue · codex has no shipped art, so a
// shape-only `>_` motif). The only non-shipped values are the two derived
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

// BrandArts returns all five doc plates keyed by guest name. opencode, hermes
// and aider are wired into Registry(); claude and codex are the doc's shim-era
// DRAFTS kept here as DORMANT data only - the registry deliberately has no home
// for a non-detectable guest, and adding a row would change detection/picker
// behavior (this pass is data-only). Returned fresh per call (the Registry()
// idiom) so callers can never corrupt the shared art.
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
			Width:    21, // the wordmark (the doc sets the narrow threshold at 23, not the tagline's 38)
			Lockup:   BrandRow{Text: "aider", Ink: inkGreen},
			ASCIIArt: true,
		},
		// §4 claude - shim-era DRAFT (dormant until the /v1/messages shim lands):
		// the SHIPPED Claude Code mascot pose, mascot + wordmark one lockup, one hue.
		"claude": {
			Rows: []BrandRow{
				{Text: "  ▗   ▖", Ink: inkClay},
				{Text: " ▐▛███▜▌", Ink: inkClay},
				{Text: "▝▜█████▛▘   claude", Spans: []BrandSpan{
					{From: 0, To: 9, Ink: inkClay},
					{From: 12, To: 18, Ink: inkClayB},
				}},
				{Text: "  ▘▘ ▝▝", Ink: inkClay},
			},
			Width:  18,
			Lockup: BrandRow{Text: "* claude", Ink: inkClay}, // ✻ pre-folded to * (house asciiFold idiom)
		},
		// §5 codex - shim-era DRAFT (shape-only): no shipped terminal art exists and
		// OpenAI's brand is hueless, so 100% mono + the red beat - their `>_` motif
		// as a chunky half-block chevron, the ▄▄▄▄ underscore IS a cursor (stRed).
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
