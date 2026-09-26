package fleetview

import lm "github.com/thebanri/limoni"

// The Edge palette. Red stays THE signal (live, selected, alert); around it a warm, deliberate set
// so a glance tells a machine from a board from a phone, a verified capability from a claimed one,
// online from dark. Chosen on a dark ground; every color is also legible on the terminal default.
var (
	ember  = lm.RGB(255, 138, 61)  // this machine's own rogers
	gold   = lm.RGB(242, 193, 78)  // agents, and a discovered device asking to be adopted
	teal   = lm.RGB(63, 184, 175)  // phones, and models on air
	sky    = lm.RGB(111, 183, 255) // host machines
	violet = lm.RGB(167, 139, 250) // boards
	mint   = lm.RGB(126, 217, 166) // online, verified
	rose   = lm.RGB(255, 120, 140) // actuate - needs the owner's yes
	paper  = lm.RGB(240, 236, 226) // titles
	ash    = lm.RGB(120, 116, 108) // dark, offline
)

// kindColor is a node's color by what it is.
func kindColor(kind string) lm.Color {
	switch kind {
	case "board":
		return violet
	case "mobile":
		return teal
	default:
		return sky
	}
}

// kindLabel is the word the readout uses for a node's kind.
func kindLabel(kind string) string {
	switch kind {
	case "board":
		return "board"
	case "mobile":
		return "phone"
	case "host":
		return "machine"
	}
	return "device"
}

// capColor is a capability's color, the same in every chip.
func capColor(name string) lm.Color {
	switch name {
	case "serve":
		return teal
	case "classify":
		return violet
	case "sense":
		return mint
	case "actuate":
		return rose
	case "relay":
		return sky
	case "operate":
		return live
	}
	return muted
}
