// Package look is the Health/Fleet look shared by every front-end that wears it: the palette,
// the glyphs and their ASCII folds, and the text of its pieces (key caps, eyebrows, rules, the
// action bar). internal/fleetview paints it with limoni; internal/tui paints the welcome card
// and the empty [3] EDGE with lipgloss. Neither front-end embeds the other: they share this.
package look

import (
	"fmt"
	"strings"

	"rogerai.fm/roger/v6/internal/glyphs"
)

// RGB is one palette color.
type RGB struct{ R, G, B uint8 }

// Hex is the color as #rrggbb, the form lipgloss and CSS take.
func (c RGB) Hex() string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// The palette: the radio TUI's instrument set on the warm ink ground. Red is the signal:
// selection, on air, alarm. A unit's kind is told by its word and glyph, never by a hue.
var (
	Live   = RGB{255, 68, 56}   // selection rail, alarm, the ON AIR lamp, the one red
	Lamp   = RGB{140, 40, 32}   // the lamp between breaths: red, dimmed
	Signal = RGB{132, 194, 85}  // the magic eye's iris, healthy, online, VERIFIED
	Shade  = RGB{66, 97, 42}    // the iris closing
	Glow   = RGB{245, 166, 35}  // a device asking to be adopted, the bench, claimed chips
	Dial   = RGB{126, 166, 216} // member machines, models on air, counts
	Paper  = RGB{243, 241, 234} // names, key caps, the values that matter
	Ink    = RGB{215, 211, 200} // prose and readings
	Muted  = RGB{154, 150, 139} // eyebrows, verbs, kinds, descriptions
	Dim    = RGB{85, 81, 72}    // off cells, rules, unselected rails
	Ash    = RGB{135, 131, 120} // dark units
	Ground = RGB{14, 13, 11}    // the key cap's ink, the confirm box's fill
)

// Palette is every color above, for a test that checks a frame wears nothing else.
func Palette() []RGB {
	return []RGB{Live, Lamp, Signal, Shade, Glow, Dial, Paper, Ink, Muted, Dim, Ash, Ground}
}

// Glyphs of the look. Each one folds through glyphs.FoldASCII, so a frame with no color (or a
// legacy console) reads in ASCII without a second table.
const (
	Rail     = "▌"  // the selected line's red rail
	EyeWait  = "◌◌" // the magic eye before a pass has completed
	EyeSeen  = "◉◎" // the magic eye once a pass has completed
	EyeOff   = "○○" // the magic eye with discovery switched off
	Hairline = "─"
)

// Fold is the ASCII form of a glyph for a frame with no color.
func Fold(s string, mono bool) string {
	if mono || glyphs.ASCII() {
		return glyphs.FoldASCII(s)
	}
	return s
}

// KeyCap is a key as it is drawn: a paper cap (" k ") in color, a bracketed key ("[k]")
// without, so a key still reads as a key with no color at all.
func KeyCap(k string, mono bool) string {
	if mono {
		return "[" + k + "]"
	}
	return " " + k + " "
}

// Eyebrow is a section label as it is drawn: upper case.
func Eyebrow(s string) string { return strings.ToUpper(s) }

// Rule is a hairline w cells wide.
func Rule(w int, mono bool) string {
	if w <= 0 {
		return ""
	}
	return strings.Repeat(Fold(Hairline, mono), w)
}

// Action is one key in the action bar at the foot of a screen.
type Action struct{ Key, Label string }

// ActionGap separates the action bar's items.
const ActionGap = "   "
