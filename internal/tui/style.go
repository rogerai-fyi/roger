// Package tui is the interactive `rogerai` experience - a two-way radio for Local Models,
// and the terminal twin of the website's "Live Operating Manual". Stations
// (providers) go on air; you tune in to a channel and talk. The look is the web's:
// ~95% monochrome + ONE red beacon, the shared instrument glyphs (◉ on air, ○ off
// air, ◆ verified, ▁▂▃▄▅▆▇█ signal bars), flat hairline structure, and a single
// carrier beat driving the beacon, the ((•)) spinner, and the signal-bar shimmer.
// Built on Bubble Tea + Lipgloss.
package tui

import (
	"github.com/charmbracelet/lipgloss"
	"rogerai.fm/roger/v6/internal/glyphs"
)

// frozenFrame is the fixed, well-formed frame the compact "windowshade" mode feeds
// every animation function (beacon arcs, signal shimmer, Ping pose) so motion
// settles to a stable snapshot - the same canonical frame quiet/anim() picks. Used
// by the compact render paths to treat compact as an explicit prefers-reduced-motion.
const frozenFrame = 1

// ---- palette: the web's "Live Operating Manual" tokens ----
//
// ~95% monochrome + ONE red beacon. This mirrors the website exactly (see
// docs-internal/design/direction-foundation.md §3.2): a near-monochrome ink/dim/
// bright ramp plus a SINGLE accent red used only as glints - the on-air beacon,
// the verified ◆, the selection cursor, the pressed preset, and headline accents.
// Everything else is ink-on-paper. The old indigo "volt", green "live", orange
// "ember", and gold "lineage" accents are RETIRED - they collapse into the mono
// ramp (so the binary reads as a terminal twin of the site, not a different app).
//
// lipgloss.AdaptiveColor flips light/dark with the terminal background, matching
// the web's "white room" / "ink room" pair: the live red is the ON-AIR redish-amber
// cLive (#C8391A on light, #FF5636 on dark; design overhaul §3); the ink ramp warms
// toward the page neutrals.
var (
	// The one accent: the live on-air beacon, retinted to the redish-amber cLive of
	// a real ON-AIR neon / nixie (incandescent-behind-ruby-glass), warming the old
	// pure red per the founder (design overhaul §3). Light #C8391A / dark #FF5636.
	// Used as a signal glint (beacon, selection, verified ◆) and the brand lockup;
	// aliased to cLive (see palette.go) so the single warm red does triple duty.
	cRed = lipgloss.AdaptiveColor{Light: "#C8391A", Dark: "#FF5636"}

	// The monochrome ink ramp (warm near-black on paper / warm off-white on ink),
	// tracking the web's --ink-900 / --ink-500 / --ink-400 / --hairline tokens.
	cInk   = lipgloss.AdaptiveColor{Light: "#15140F", Dark: "#F3F1EA"} // headlines / primary
	cBody  = lipgloss.AdaptiveColor{Light: "#33312B", Dark: "#CFCCC4"} // body / values
	cDim   = lipgloss.AdaptiveColor{Light: "#6B685F", Dark: "#9A968B"} // secondary / labels
	cFaint = lipgloss.AdaptiveColor{Light: "#9A968B", Dark: "#6F6C64"} // off-bars / disabled
	cRule  = lipgloss.AdaptiveColor{Light: "#D8D7D2", Dark: "#2A2720"} // the single hairline
	cInkBg = lipgloss.AdaptiveColor{Light: "#FBFBFA", Dark: "#0E0D0B"} // paper (selection text)

	// One voice, five roles - but now they all draw from the SAME mono+red system,
	// so the names are kept (minimal churn across the file) while the COLOR is unified:
	//   stBrand  - the headline / faceplate lettering (bright ink, bold).
	//   stTag    - a quiet brand-tail / secondary (dim).
	//   stDim    - labels, captions, structure (dim).
	//   stLive   - on-air / good / values that were "green" -> now ink (no green).
	//   stEmber  - prices / money that were "orange" -> now ink mono (weight, not hue).
	//   stGold   - lineage ◆ that was "gold" -> now the ONE red (verified is a glint).
	//   stKey    - the load-bearing value (command / endpoint / model) -> bright ink.
	//   stSelText- the selection / focus glint -> red.
	stBrand    = lipgloss.NewStyle().Foreground(cInk).Bold(true)
	stTag      = lipgloss.NewStyle().Foreground(cDim)
	stDim      = lipgloss.NewStyle().Foreground(cDim)
	stLive     = lipgloss.NewStyle().Foreground(cBody)
	stEmber    = lipgloss.NewStyle().Foreground(cBody)
	stGold     = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	stSelBar   = lipgloss.NewStyle().Foreground(cRed)
	stSelText  = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	stHeadRule = lipgloss.NewStyle().Foreground(cRule)
	stPanel    = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(cRule).Padding(0, 1)
	stKey      = lipgloss.NewStyle().Foreground(cInk).Bold(true)
	stPrompt   = lipgloss.NewStyle().Foreground(cInk).Bold(true) // the `rog ›` prompt lockup
	stRed      = lipgloss.NewStyle().Foreground(cRed).Bold(true)

	// k9s-grade selection: a full-width reverse-video (accent-bg) row so the cursor
	// is unmistakable at a glance, exactly like k9s's cursor row (it flips the row's
	// background to its accent so the selected resource pops). The web's one accent is
	// red, so the cursor row is the red bar with paper text; under NO_COLOR lipgloss
	// drops the bg and a leading `>` carat carries the selection instead (see rowSel /
	// selCarat). k9s design refs (cited for the local design record): k9scli.io
	// (cursor/accent row, status columns, contextual key footer) and
	// github.com/derailed/k9s (skin table.cursorColor, reverse-video selected row).
	stRowSel = lipgloss.NewStyle().Foreground(cInkBg).Background(cRed).Bold(true)
)

// Shared iconography (the web's instrument glyphs), used consistently across
// search / share / channel so every surface reads as one designed system:
//
//	glyphOnAir  ◉  on air / online / a live carrier
//	glyphOffAir ○  off air / offline / over-margin
//	glyphConf   ◆  TEE-verified CONFIDENTIAL ONLY - a node that passed real hardware
//	               remote attestation (SEV-SNP quote: signature chain + nonce binding +
//	               allowlisted measurement). NEVER shown for a non-attested node.
//	glyphLineage ✓ signed-lineage / GitHub-verified-operator glint - the IDENTITY mark
//	               on every co-signed channel + on login. Distinct from ◆: lineage
//	               receipts are on ALL channels; ◆ is only the confidential tier.
//	signalGlyphs ▃▄▅▇█ over a ▁ rail  the signal staircase (lit bars = strength)
//
// These degrade to plain runes under NO_COLOR (lipgloss strips the color, the
// glyph itself is still a recognizable Unicode mark) and stay fixed-width. They are
// vars (not consts) because the actual mark is chosen ONCE at startup by
// glyphs.Current(): the rich Unicode set on capable terminals (the default - no
// regression for mac/linux/Windows-Terminal), or an ASCII fallback on a legacy
// Windows console / under ROGERAI_ASCII=1 / NO_UNICODE. See internal/glyphs.
var (
	glyphOnAir = glyphs.Current().OnAir
	// glyphCurated is the NEW mark for a curated (commercial-API proxy) station - founder
	// ruling 2026-09-01: its own badge, reusing none of ◆ / ⌁ / ◪ / ✓. The double
	// guillemet reads as "passes onward": the station relays a distant commercial signal
	// rather than transmitting its own. Single-cell, unambiguous East-Asian width, so it
	// can never re-open the bordered-box misalignment the edit plate documents.
	glyphCurated = "»"
	glyphOffAir  = glyphs.Current().OffAir
	glyphConf    = glyphs.Current().Verify  // TEE-verified confidential ONLY
	glyphLineage = glyphs.Current().Lineage // signed-lineage / verified-operator (identity, not confidential)
	// glyphVerify is retained as an alias for the confidential diamond so existing
	// references keep compiling; new code should use glyphConf or glyphLineage by intent.
	glyphVerify = glyphConf
)

const caratSlideFrames = 2

const toastFrames = 20

// stPreset / stPresetOn render a preset button: a lit (current) preset is a
// pressed, reverse-video red glint (like a depressed station button); the rest are
// dim. Under NO_COLOR the reverse-video is stripped and a leading dot marks the lit
// preset so the active mode is still unmistakable.
var (
	stPreset   = lipgloss.NewStyle().Foreground(cDim)
	stPresetOn = lipgloss.NewStyle().Foreground(cInkBg).Background(cRed).Bold(true)
)

// transcriptContent renders a slice of transcript ENTRIES into the multi-line string a
// viewport scrolls over: each entry's physical lines (entries may carry embedded
// newlines, e.g. a multi-line reply) are indented two spaces to match the rest of the
// view. The viewport itself handles width clipping + height padding, so we don't
// msgRevealFrames is how many ~160ms ticks a freshly-arrived reply block stays dimmed before
// settling to full ink (the message-in "ink-settling"). 2 ticks ≈ 1/3s - subtle, not sluggish.
const msgRevealFrames = 2

// rescanEveryFrames sets the live band re-scan cadence: at the 160ms tick, ~31
// frames is ~5s, comfortably under the broker's ~35s on-air TTL so a node that
// just went on/off air is reflected within one cadence.
const rescanEveryFrames = 31

// worldRescanFrames is the LIVE-towers re-scan cadence while the Ping World screensaver is up:
// ~60 frames (~10s) - slower than the browse rescan, because a screensaver should breathe.
const worldRescanFrames = 60
