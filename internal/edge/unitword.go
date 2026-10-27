package edge

// UNIT WORDS (features/edge/fleet_window.feature): what a unit's state is, in one word every surface
// uses, with how long ago it was last heard. Two channels, because they decide different things: a
// unit can be HEARD (its heartbeat arrives) and still not be CONTROLLABLE (there is no face this
// machine can dial), and what the owner can do to it differs.

import (
	"fmt"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/store"
)

// The state words.
const (
	WordOnAir    = "ON AIR"    // heard, and controllable
	WordHeard    = "HEARD"     // heard, with no control path
	WordTrouble  = "TROUBLE"   // a rule is firing on it
	WordDark     = "DARK"      // not heard within the liveness window
	WordReenroll = "RE-ENROLL" // a member whose certificate this Edge can no longer verify
	WordAsleep   = "ASLEEP"    // a phone whose app the system put in the background
)

// nowWindow is how fresh "now" is: closer than the eye can tell apart from this instant.
const nowWindow = 5 * time.Second

// Controllable reports whether this machine has a face it can dial for the unit: a LAN address with
// the pinned fingerprint. A relay or an unpinned address is not a control path today.
func Controllable(n store.EdgeNode) bool {
	for _, t := range n.Transports {
		if t.Kind == "lan" && t.Addr != "" && t.Fingerprint != "" {
			return true
		}
	}
	return false
}

// UnitWord is the unit's state word and the line a surface prints: the word and its age, with the
// missing channel named when it is heard but cannot be controlled.
func UnitWord(n store.EdgeNode, alerts int, now time.Time) (word, line string) {
	age := ""
	if n.LastSeen > 0 {
		d := now.Sub(time.Unix(n.LastSeen, 0))
		age = "now"
		if d >= nowWindow {
			age = Age(d)
		}
	}
	with := func(w string) (string, string) {
		if age == "" {
			return w, w + " · never directly"
		}
		return w, w + " " + age
	}
	switch Presence(n.Presence) {
	case PresenceDark:
		return with(WordDark)
	case PresenceReenroll:
		return with(WordReenroll)
	}
	if alerts > 0 {
		return with(WordTrouble)
	}
	if p := n.Phone; Kind(n.Kind) == Mobile && p != nil && p.Foreground != nil && !*p.Foreground && age != "" &&
		now.Sub(time.Unix(p.ReadAt, 0)) <= DefaultTTL {
		return with(WordAsleep) // backgrounded by the system: not broken, and not dark until the window says so
	}
	if !Controllable(n) || age == "" {
		w, l := with(WordHeard)
		if age != "" {
			l += " · no control path"
		}
		return w, l
	}
	return with(WordOnAir)
}

// UnitName is what a unit is called on every screen: its owner's name, or - for a unit nobody named,
// which carries its id as its record name - what it IS: its hardware word ("Pi 4"), else its kind
// word ("phone"). A short suffix from its id is added only when another unit in all would otherwise
// read the same. Its full id is for its own page, where every command that takes one is described.
func UnitName(n store.EdgeNode, all []store.EdgeNode) string {
	if ownerNamed(n) {
		return strings.TrimSpace(n.Name)
	}
	base := unnamedBase(n)
	for _, o := range all {
		if o.ID == n.ID {
			continue
		}
		if ownerNamed(o) {
			if strings.TrimSpace(o.Name) == base {
				return base + " " + idSuffix(n.ID)
			}
			continue
		}
		if unnamedBase(o) == base {
			return base + " " + idSuffix(n.ID)
		}
	}
	return base
}

// ownerNamed reports whether the unit carries a name its owner gave it rather than its own id.
func ownerNamed(n store.EdgeNode) bool {
	name := strings.TrimSpace(n.Name)
	return name != "" && name != n.ID && !LooksLikeID(name)
}

func unnamedBase(n store.EdgeNode) string {
	if hw := MachineShort(MachineOf(n)); hw != "" {
		return hw
	}
	if w := KindWord(n.Kind); w != "" {
		return w
	}
	return "device"
}

// idSuffix is four characters of the id after its prefix: enough to tell two units apart, short
// enough that nobody mistakes it for the name.
func idSuffix(id string) string {
	s := strings.TrimPrefix(id, "n_")
	if len(s) > 4 {
		s = s[:4]
	}
	return s
}

// UnitNames names every unit in all at once, by id, so a surface draws one consistent set.
func UnitNames(all []store.EdgeNode) map[string]string {
	out := make(map[string]string, len(all))
	for _, n := range all {
		out[n.ID] = UnitName(n, all)
	}
	return out
}

// PhoneAsleep reports whether a phone's last signed beat said the system had put its app in the
// background. Only a phone, and only when it said so: silence alone is not sleep.
func PhoneAsleep(kind string, p *store.EdgePhone) bool {
	return Kind(kind) == Mobile && p != nil && p.Foreground != nil && !*p.Foreground
}

// NodePower is a node's newest power report: its own readings when it is dialed (an instance's
// Resources), its signed v3 presence when it dials out. nil when it reports none.
func NodePower(n store.EdgeNode) *store.EdgePower {
	best := n.Power
	for _, in := range n.Instances {
		if r := in.Resources; r != nil && r.Power != nil && (best == nil || r.Power.ReadAt > best.ReadAt) {
			best = r.Power
		}
	}
	return best
}

// SupplyLine is any node's power in words, absent readings left out: "battery 64% · charging · on AC
// power · low power mode · about 3h12m left".
func SupplyLine(p *store.EdgePower) string {
	if p == nil {
		return ""
	}
	var parts []string
	if p.BatteryPct != nil {
		parts = append(parts, fmt.Sprintf("battery %d%%", *p.BatteryPct))
	}
	if p.Charging != nil {
		if *p.Charging {
			parts = append(parts, "charging")
		} else {
			parts = append(parts, "not charging")
		}
	}
	switch p.Source {
	case "ac":
		parts = append(parts, "on external power")
	case "battery":
		parts = append(parts, "on battery")
	}
	if p.LowPower != nil && *p.LowPower {
		parts = append(parts, "low power mode")
	}
	if p.MinutesLeft != nil {
		parts = append(parts, "about "+Age(time.Duration(*p.MinutesLeft)*time.Minute)+" left")
	}
	return strings.Join(parts, " · ")
}

// PhoneLine is a phone's own vitals in words, absent readings left out: iOS gives no processor load
// or temperature, and nothing here pretends it does.
func PhoneLine(p *store.EdgePhone) string {
	if p == nil {
		return ""
	}
	var parts []string
	if p.BatteryPct != nil {
		parts = append(parts, fmt.Sprintf("battery %d%%", *p.BatteryPct))
	}
	if p.Charging != nil {
		if *p.Charging {
			parts = append(parts, "charging")
		} else {
			parts = append(parts, "on battery")
		}
	}
	if p.Thermal != "" {
		parts = append(parts, "thermal "+p.Thermal)
	}
	if p.Foreground != nil {
		if *p.Foreground {
			parts = append(parts, "app in front")
		} else {
			parts = append(parts, "app in the background")
		}
	}
	return strings.Join(parts, " · ")
}
