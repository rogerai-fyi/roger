package fleetview

import (
	lm "github.com/thebanri/limoni"

	"rogerai.fm/roger/v6/internal/edge"
)

// The Edge palette is the radio TUI's instrument set on the warm ink ground: a 1950s receiver's
// magic-eye green, its dial's amber glow, its dial's blue-white, and the ONE red. Red is the
// signal: selection, on air, alarm. A unit's kind is told by its word and its glyph, never by a
// hue of its own - three rail hues per kind is a chart legend, not a rack.
var (
	live   = lm.RGB(255, 68, 56)   // selection rail, alarm, the ON AIR lamp, a red iris, a firing rule
	lamp   = lm.RGB(140, 40, 32)   // the lamp between breaths: red, dimmed - never another hue
	signal = lm.RGB(132, 194, 85)  // the magic eye's iris, a healthy meter, online, VERIFIED, a good outcome
	shade  = lm.RGB(66, 97, 42)    // the iris closing
	glow   = lm.RGB(245, 166, 35)  // a device asking to be adopted, the bench, claimed chips, this machine's rogers
	dial   = lm.RGB(126, 166, 216) // member machines, models on air, sparklines
	paper  = lm.RGB(243, 241, 234) // the hub's name, key caps, the values that matter
	ink    = lm.RGB(215, 211, 200) // prose and readings
	muted  = lm.RGB(154, 150, 139) // eyebrows, verbs, kinds
	dim    = lm.RGB(85, 81, 72)    // off cells, rules, unselected rails
	ash    = lm.RGB(135, 131, 120) // dark units
	ground = lm.RGB(14, 13, 11)    // the confirm box's fill
)

// kindColor is a node's rail: one hue for every kind (its word and glyph tell them apart).
func kindColor(string) lm.Color { return dial }

// kindLabel is the word the readout uses for a node's kind.
func kindLabel(kind string) string {
	if w := edge.KindWord(kind); w != "" {
		return w
	}
	return "device"
}

// capColor is a capability's color, the same in every chip: what a node can do is signal, what
// needs the owner's yes (actuate) or acts on others (operate) is red.
func capColor(name string) lm.Color {
	switch name {
	case "actuate", "operate":
		return live
	}
	return signal
}
