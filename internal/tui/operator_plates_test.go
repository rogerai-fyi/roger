package tui

// operator_plates_test.go - render pins for the guest-operator BRAND PLATES
// (rogerai-internal-docs/GUEST-OPERATOR-PLATES.md, "ONE HUE, ONE BEAT", founder-
// approved 2026-07-06) against the Phase 2 BrandArt seam. Every assertion is
// self-computing (expected segments are composed with the SAME house styles the
// renderer uses, the voicebooth TestBadgeRenderSitesNotRed idiom) so no escape
// codes are hard-coded. Covers the doc's §7 fallback matrix: color/full,
// NO_COLOR (plain), ROGERAI_ASCII (lockup swap - never a folded wordmark),
// narrow (swap-to-lockup, never crop), and the compact windowshade one-liner.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/rogerai-fyi/roger/internal/operator"
)

// colorOn forces a TrueColor profile + a chosen background so lipgloss emits SGR
// (off a TTY every style strips to plain), restoring both on cleanup.
func colorOn(t *testing.T, dark bool) {
	t.Helper()
	r := lipgloss.DefaultRenderer()
	oldP, oldD := r.ColorProfile(), r.HasDarkBackground()
	r.SetColorProfile(termenv.TrueColor)
	r.SetHasDarkBackground(dark)
	t.Cleanup(func() { r.SetColorProfile(oldP); r.SetHasDarkBackground(oldD) })
}

// plateGuest fetches a live registry entry by name (the REAL shipped data).
func plateGuest(t *testing.T, name string) operator.Guest {
	t.Helper()
	for _, g := range operator.Registry() {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("%s not in registry", name)
	return operator.Guest{}
}

// dormantGuest wraps a dormant BrandArts() draft (claude/codex) in a Guest so the
// render seam can be exercised without a registry row.
func dormantGuest(t *testing.T, name string) operator.Guest {
	t.Helper()
	art := operator.BrandArts()[name]
	if art == nil {
		t.Fatalf("%s draft missing from BrandArts()", name)
	}
	return operator.Guest{Name: name, Brand: art}
}

// plainLines strips ANSI + the trailing newline and splits a rendered block.
func plainLines(block string) []string {
	return strings.Split(strings.TrimRight(stripANSI(block), "\n"), "\n")
}

// TestPlateOpencodeExactBytesAndStyles: §1a - the shipped wordmark renders byte-
// exact (2-space indent), `open` in stDim, `code` in stBrand, and the ONE red on
// the plate is the block-cursor glint in cRed NON-BOLD (a glint, not stRed).
func TestPlateOpencodeExactBytesAndStyles(t *testing.T) {
	colorOn(t, true)
	block := operatorBrandBlock(plateGuest(t, "opencode"), 120)
	want := []string{
		"  ⠀                                ▄",
		"  █▀▀█ █▀▀█ █▀▀█ █▀▀▄ █▀▀▀ █▀▀█ █▀▀█ █▀▀█  ▄",
		"  █  █ █  █ █▀▀▀ █  █ █    █  █ █  █ █▀▀▀  █",
		"  ▀▀▀▀ █▀▀▀ ▀▀▀▀ ▀  ▀ ▀▀▀▀ ▀▀▀▀ ▀▀▀▀ ▀▀▀▀  ▀",
	}
	if got := plainLines(block); !equalLines(got, want) {
		t.Fatalf("opencode plate bytes corrupted:\n got %q\nwant %q", got, want)
	}
	cursorRed := lipgloss.NewStyle().Foreground(cRed) // non-bold: a glint, not a surface
	wantRow2 := "  " + stDim.Render("█▀▀█ █▀▀█ █▀▀█ █▀▀▄") + " " + stBrand.Render("█▀▀▀ █▀▀█ █▀▀█ █▀▀█") + "  " + cursorRed.Render("▄")
	if !strings.Contains(block, wantRow2) {
		t.Fatalf("row 2 styling must be dim/brand/red-non-bold:\nwant segment %q\nin block %q", wantRow2, block)
	}
	if strings.Contains(block, stRed.Render("█")) {
		t.Fatal("the cursor stack must NOT use bold stRed - cRed non-bold only (doc §1a)")
	}
	if !strings.Contains(block, stBrand.Render("▄")) {
		t.Fatal("row 1's d-ascender at col 33 renders in stBrand")
	}
}

// TestPlateHermesGradientAndByline: §2a - rows 1-2 bold #FFD700, rows 3-4 #FFBF00,
// rows 5-6 #CD7F32, byline `nous research` right-aligned in stDim.
func TestPlateHermesGradientAndByline(t *testing.T) {
	colorOn(t, true)
	art := operator.BrandArts()["hermes"]
	block := operatorBrandBlock(plateGuest(t, "hermes"), 120)
	gold1 := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B8860B", Dark: "#FFD700"}).Bold(true)
	gold2 := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B8860B", Dark: "#FFBF00"})
	gold3 := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B8860B", Dark: "#CD7F32"})
	for i, st := range []lipgloss.Style{gold1, gold1, gold2, gold2, gold3, gold3} {
		if !strings.Contains(block, "  "+st.Render(art.Rows[i].Text)) {
			t.Fatalf("hermes row %d must carry its shipped gradient step", i+1)
		}
	}
	if !strings.Contains(block, stDim.Render("nous research")) {
		t.Fatal("the byline signs off in stDim")
	}
	if lines := plainLines(block); lines[6] != "  "+strings.Repeat(" ", 38)+"nous research" {
		t.Fatalf("byline must right-align ending art col 50 (starts 38), got %q", lines[6])
	}
}

// TestPlateHermesLightCollapse: §2a - on a light background ALL six rows collapse
// to the single #B8860B dim-gold (their own ramp token); rows 1-2 keep bold. The
// 3-step ramp is a dark-room effect and #FFD700 on paper is unreadable.
func TestPlateHermesLightCollapse(t *testing.T) {
	colorOn(t, false)
	block := operatorBrandBlock(plateGuest(t, "hermes"), 120)
	if !strings.Contains(block, "184;134;11") { // #B8860B
		t.Fatal("light terminals must collapse the gradient to #B8860B")
	}
	for _, banned := range []string{"255;215;0", "255;191;0", "205;127;50"} { // FFD700 FFBF00 CD7F32
		if strings.Contains(block, banned) {
			t.Fatalf("no dark-ramp step may leak onto a light terminal (found %s)", banned)
		}
	}
}

// TestPlateAiderGreenAndTagline: §3a - the whole wordmark in #14B014 on dark
// (#0E7A0E light), tagline in stDim, and NO red glint anywhere on the plate.
func TestPlateAiderGreenAndTagline(t *testing.T) {
	colorOn(t, true)
	art := operator.BrandArts()["aider"]
	block := operatorBrandBlock(plateGuest(t, "aider"), 120)
	green := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#0E7A0E", Dark: "#14B014"})
	for i := 0; i < 4; i++ {
		if !strings.Contains(block, "  "+green.Render(art.Rows[i].Text)) {
			t.Fatalf("aider row %d must render in the phosphor green", i+1)
		}
	}
	if !strings.Contains(block, stDim.Render("ai pair programming in your terminal")) {
		t.Fatal("the tagline reads as a dim sentence under the art")
	}
	if strings.Contains(stripANSI(block), "▄") || strings.Contains(block, "255;68;56") { // cRed dark #FF4438
		t.Fatal("no cursor glint on the aider plate (doc §3a ruling)")
	}
}

// TestPlateClaudeCodexDormantDrafts: §4a/§5a - the shim-era drafts render through
// the same seam when given a Guest wrapper: claude's mascot+wordmark lockup in one
// hue (#D97757, bold wordmark), codex's mono chevron with the stRed underscore beat.
func TestPlateClaudeCodexDormantDrafts(t *testing.T) {
	colorOn(t, true)
	clay := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B85F41", Dark: "#D97757"})
	clayB := clay.Bold(true)
	claude := operatorBrandBlock(dormantGuest(t, "claude"), 120)
	if got, want := plainLines(claude), []string{
		"    ▗   ▖",
		"   ▐▛███▜▌",
		"  ▝▜█████▛▘   claude",
		"    ▘▘ ▝▝",
	}; !equalLines(got, want) {
		t.Fatalf("claude mascot bytes corrupted:\n got %q\nwant %q", got, want)
	}
	if !strings.Contains(claude, clay.Render("▝▜█████▛▘")) || !strings.Contains(claude, clayB.Render("claude")) {
		t.Fatal("claude: art in #D97757, wordmark same hue bold")
	}
	codex := operatorBrandBlock(dormantGuest(t, "codex"), 120)
	if got, want := plainLines(codex), []string{
		"  █▄",
		"   ▀█▄     codex",
		"   ▄█▀     openai",
		"  █▀ ▄▄▄▄",
	}; !equalLines(got, want) {
		t.Fatalf("codex chevron bytes corrupted:\n got %q\nwant %q", got, want)
	}
	if !strings.Contains(codex, stBrand.Render("▀█▄")) || !strings.Contains(codex, stKey.Render("codex")) ||
		!strings.Contains(codex, stDim.Render("openai")) || !strings.Contains(codex, stRed.Render("▄▄▄▄")) {
		t.Fatal("codex: chevron stBrand · wordmark stKey · credit stDim · underscore stRed")
	}
}

// TestPlateNoColorPlain: the NO_COLOR column of §7's matrix - the same art,
// uncolored, byte-exact. No substitute art, no dropped rows.
func TestPlateNoColorPlain(t *testing.T) {
	r := lipgloss.DefaultRenderer()
	oldP := r.ColorProfile()
	r.SetColorProfile(termenv.Ascii) // what NO_COLOR resolves to
	t.Cleanup(func() { r.SetColorProfile(oldP) })
	for _, name := range []string{"opencode", "hermes", "aider"} {
		g := plateGuest(t, name)
		block := operatorBrandBlock(g, 120)
		if strings.Contains(block, "\x1b[") {
			t.Fatalf("%s: NO_COLOR must strip every SGR sequence", name)
		}
		for i, row := range g.Brand.Rows {
			if want := "  " + row.Text; plainLines(block)[i] != want {
				t.Fatalf("%s row %d must survive mono byte-exact:\n got %q\nwant %q", name, i+1, plainLines(block)[i], want)
			}
		}
	}
}

// TestPlateASCIILockups: the ROGERAI_ASCII column of §7's matrix - half-blocks and
// box runes NEVER render folded/garbled: opencode and hermes (and the dormant
// drafts) swap to their one-line text lockups; aider keeps its full plate (it is
// pure ASCII by construction). Non-ASCII lockup separators fold per glyphs.Fold.
func TestPlateASCIILockups(t *testing.T) {
	t.Setenv("ROGERAI_ASCII", "1")
	cases := []struct {
		guest operator.Guest
		want  []string
	}{
		{plateGuest(t, "opencode"), []string{"  opencode _"}},
		{plateGuest(t, "hermes"), []string{"  H E R M E S . nous research"}},
		{plateGuest(t, "aider"), []string{
			"        _    _",
			"   __ _(_)__| |___ _ _",
			"  / _` | / _` / -_) '_|",
			"  \\__,_|_\\__,_\\___|_|",
			"  ai pair programming in your terminal",
		}},
		{dormantGuest(t, "claude"), []string{"  * claude"}},
		{dormantGuest(t, "codex"), []string{"  >_ codex . openai"}},
	}
	for _, tc := range cases {
		got := plainLines(operatorBrandBlock(tc.guest, 120))
		if !equalLines(got, tc.want) {
			t.Fatalf("%s ASCII fallback:\n got %q\nwant %q", tc.guest.Name, got, tc.want)
		}
	}
}

// TestPlateASCIILockupStyles: §1c - under ASCII (color kept) the opencode lockup
// carries open/code/cursor as stDim/stKey/stRed; §2d - hermes keeps gold bold caps.
func TestPlateASCIILockupStyles(t *testing.T) {
	t.Setenv("ROGERAI_ASCII", "1")
	colorOn(t, true)
	oc := operatorBrandBlock(plateGuest(t, "opencode"), 120)
	if !strings.Contains(oc, stDim.Render("open")+stKey.Render("code")+" "+stRed.Render("_")) {
		t.Fatalf("opencode lockup styling must be dim/key + the honest stRed ASCII cursor, got %q", oc)
	}
	gold1 := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B8860B", Dark: "#FFD700"}).Bold(true)
	hm := operatorBrandBlock(plateGuest(t, "hermes"), 120)
	if !strings.Contains(hm, gold1.Render("H E R M E S")) || !strings.Contains(hm, stDim.Render(" . nous research")) {
		t.Fatalf("hermes lockup: letter-spaced caps in gold bold, credit stDim, got %q", hm)
	}
}

// TestPlateNarrowSwap pins §7's narrow rule per guest: full art renders whenever
// termWidth >= 2 + artWidth; ONE column below, the art block is REPLACED by the
// lockup line - shipped brand art is never cropped or re-wrapped.
func TestPlateNarrowSwap(t *testing.T) {
	cases := []struct {
		guest     operator.Guest
		threshold int    // the doc's 2+artWidth
		artProbe  string // a byte only the full art contains
		lockup    string
	}{
		{plateGuest(t, "opencode"), 44, "█▀▀█", "opencode _"},
		{plateGuest(t, "hermes"), 53, "██╗", "H E R M E S · nous research"},
		{plateGuest(t, "aider"), 23, "__ _(_)__", "aider"},
		{dormantGuest(t, "claude"), 20, "▐▛███▜▌", "* claude"},
		// codex's full lockup (19 cells with indent) is itself clamped at width 16,
		// so the probe is the un-cropped head of the lockup.
		{dormantGuest(t, "codex"), 17, "▀█▄", ">_ codex"},
	}
	for _, tc := range cases {
		full := stripANSI(operatorBrandBlock(tc.guest, tc.threshold))
		if !strings.Contains(full, tc.artProbe) {
			t.Fatalf("%s: at width %d the full art must render", tc.guest.Name, tc.threshold)
		}
		narrow := stripANSI(operatorBrandBlock(tc.guest, tc.threshold-1))
		if strings.Contains(narrow, tc.artProbe) {
			t.Fatalf("%s: below width %d the art must be dropped, never cropped", tc.guest.Name, tc.threshold)
		}
		if !strings.Contains(narrow, tc.lockup) {
			t.Fatalf("%s: narrow swaps to the text lockup %q, got %q", tc.guest.Name, tc.lockup, narrow)
		}
	}
}

// TestPatchViewCarriesPlate: integration - the real opencode registry plate sits on
// the PATCHING YOU THROUGH screen between the header and the mic-to line (the §1a
// frame idiom), through the untouched Phase 2 transition.
func TestPatchViewCarriesPlate(t *testing.T) {
	m := asModel(agentReady(t))
	m.operatorHandoff = &operatorHandoff{det: operator.Detection{Guest: plateGuest(t, "opencode"), Path: "/fake/opencode"}}
	view := stripANSI(m.operatorPatchView(120))
	head := strings.Index(view, "PATCHING YOU THROUGH")
	plate := strings.Index(view, "█▀▀█ █▀▀█ █▀▀█ █▀▀▄")
	mic := strings.Index(view, "mic to")
	if head < 0 || plate < 0 || mic < 0 || !(head < plate && plate < mic) {
		t.Fatalf("plate must sit between the header and the wire plate (head=%d plate=%d mic=%d):\n%s", head, plate, mic, view)
	}
}

// TestPatchViewCompactOneLiner: §1b - the windowshade renders ONE static line,
// `(•) patching <guest> through on <band>…`, beacon in stRed, name in stKey, no
// art (at one line the guest's name IS the brand); ASCII folds to `(*) … ...`.
func TestPatchViewCompactOneLiner(t *testing.T) {
	m := asModel(agentReady(t))
	m.compact = true
	m.operatorHandoff = &operatorHandoff{det: operator.Detection{Guest: plateGuest(t, "opencode"), Path: "/fake/opencode"}}
	view := stripANSI(m.operatorPatchView(120))
	if lines := strings.Split(strings.TrimRight(view, "\n"), "\n"); len(lines) != 1 {
		t.Fatalf("compact is ONE line, got %d: %q", len(lines), lines)
	}
	if !strings.Contains(view, "(•) patching opencode through on") || !strings.HasSuffix(strings.TrimRight(view, "\n"), "…") {
		t.Fatalf("compact line must read `(•) patching opencode through on <band>…`, got %q", view)
	}
	if strings.Contains(view, "█") || strings.Contains(view, "PATCHING YOU THROUGH") {
		t.Fatalf("no art and no full header at one line, got %q", view)
	}
	t.Setenv("ROGERAI_ASCII", "1")
	av := stripANSI(m.operatorPatchView(120))
	if !strings.Contains(av, "(*) patching opencode through on") || !strings.HasSuffix(strings.TrimRight(av, "\n"), "...") {
		t.Fatalf("ASCII compact folds the beacon and the ellipsis, got %q", av)
	}
}

// TestPatchViewCompactStyles: the compact line's styling contract (§1b): beacon
// stRed · verb stDim · guest name stKey.
func TestPatchViewCompactStyles(t *testing.T) {
	colorOn(t, true)
	m := asModel(agentReady(t))
	m.compact = true
	m.operatorHandoff = &operatorHandoff{det: operator.Detection{Guest: plateGuest(t, "opencode"), Path: "/fake/opencode"}}
	view := m.operatorPatchView(120)
	if !strings.Contains(view, stRed.Render("(•)")) || !strings.Contains(view, stDim.Render("patching ")) || !strings.Contains(view, stKey.Render("opencode")) {
		t.Fatalf("compact styling must be stRed beacon · stDim verb · stKey name, got %q", view)
	}
}

// TestBrandRowOutOfRangeSpansSafe: defense in depth (pre-push audit minor) - a
// span pointing past the row's runes must clamp, never panic, and never drop the
// row's text. Unreachable with the shipped golden-pinned data; pinned so a future
// hand-edited plate can't crash the PATCHING screen.
func TestBrandRowOutOfRangeSpansSafe(t *testing.T) {
	rows := []operator.BrandRow{
		{Text: "ok", Spans: []operator.BrandSpan{{From: 5, To: 9, Ink: operator.BrandInk{Token: operator.InkDim}}}},
		{Text: "ok", Spans: []operator.BrandSpan{{From: 0, To: 99, Ink: operator.BrandInk{Token: operator.InkDim}}}},
		{Text: "ok", Spans: []operator.BrandSpan{{From: 1, To: 1, Ink: operator.BrandInk{Token: operator.InkDim}}}},
	}
	for i, row := range rows {
		got := stripANSI(operatorBrandRow(row))
		if got != "ok" {
			t.Fatalf("case %d: out-of-range spans must clamp and keep the text, got %q", i, got)
		}
	}
}

// equalLines is a tiny slice comparison (no reflect import churn).
func equalLines(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
