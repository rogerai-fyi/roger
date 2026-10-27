package fleetview

import (
	lm "github.com/thebanri/limoni"

	"rogerai.fm/roger/v7/internal/look"

	"rogerai.fm/roger/v7/internal/edge"
)

// The Edge palette is the radio TUI's instrument set on the warm ink ground: a 1950s receiver's
// magic-eye green, its dial's amber glow, its dial's blue-white, and the ONE red. Red is the
// signal: selection, on air, alarm. A unit's kind is told by its word and its glyph, never by a
// hue of its own - three rail hues per kind is a chart legend, not a rack.
var (
	live   = c(look.Live)   // selection rail, alarm, the ON AIR lamp, a red iris, a firing rule
	lamp   = c(look.Lamp)   // the lamp between breaths: red, dimmed - never another hue
	signal = c(look.Signal) // the magic eye's iris, a healthy meter, online, VERIFIED, a good outcome
	shade  = c(look.Shade)  // the iris closing
	glow   = c(look.Glow)   // a device asking to be adopted, the bench, claimed chips, this machine's rogers
	dial   = c(look.Dial)   // member machines, models on air, sparklines
	paper  = c(look.Paper)  // the hub's name, key caps, the values that matter
	ink    = c(look.Ink)    // prose and readings
	muted  = c(look.Muted)  // eyebrows, verbs, kinds
	dim    = c(look.Dim)    // off cells, rules, unselected rails
	ash    = c(look.Ash)    // dark units
	ground = c(look.Ground) // the confirm box's fill
)

// c is a shared palette color (internal/look) as a limoni color.
func c(x look.RGB) lm.Color { return lm.RGB(x.R, x.G, x.B) }

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
	// What a node can DO is signal; what needs the owner's yes, or lets one machine act on
	// another, is the one red. The set is asked of internal/edge rather than listed here, so a
	// capability added to the fabric cannot quietly arrive in the gentle colour: `maintain` did
	// exactly that, and read as harmless while it could restart somebody's agent.
	c := edge.Capability(name)
	if c == edge.Actuate || edge.DeclarationGated(c) {
		return live
	}
	return signal
}
