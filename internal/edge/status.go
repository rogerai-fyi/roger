package edge

// SELF STATUS - what is true about THIS machine's place on its Edge, and the next command
// for each fact that is not yet what the owner wants.
//
// An empty Edge is not a blank; it is a status. A fresh machine has three facts that decide
// what to do next - is it enrolled, what roots its Edge, is it scanning - and every surface
// (the TUI's [3] EDGE, the console's EDGE tab, `roger edge`) states the same three, in the
// same words, from this one record. The host fills it from the files the CLI already reads;
// the wording lives here so no surface composes its own.
//
// Spec: features/edge/empty_edge.feature.

import (
	"fmt"
	"strconv"
	"time"
)

// The ROOT badges and the route PREFERENCES (features/edge/mode.feature).
const (
	RootLocal = "LOCAL"
	RootCore  = "CORE"

	PreferLocal     = "local"
	PreferMarket    = "market"
	PreferLocalOnly = "local-only"
)

// ValidPrefer reports whether p is one of the three preferences.
func ValidPrefer(p string) bool {
	return p == PreferLocal || p == PreferMarket || p == PreferLocalOnly
}

// PreferMeaning is the one line each preference means, for `roger edge prefer`.
func PreferMeaning(p string) string {
	switch p {
	case PreferLocal:
		return "try this Edge first, fall out to the market when nothing here serves the band"
	case PreferMarket:
		return "go to the market first, as before this setting existed"
	case PreferLocalOnly:
		return "never leave this Edge: a band nothing here serves is refused, not bought"
	}
	return ""
}

// EffectivePrefer is the preference in force: local when none was ever set.
func (s SelfStatus) EffectivePrefer() string {
	if ValidPrefer(s.Prefer) {
		return s.Prefer
	}
	return PreferLocal
}

// RootBadge is the header's badge for the root: LOCAL names the designated machine.
func (s SelfStatus) RootBadge() string {
	switch {
	case s.Err != "":
		return "ROOT UNKNOWN"
	case s.Root == RootLocal && s.Authority != "":
		return "LOCAL ROOT · " + s.Authority
	case s.Root == RootLocal:
		return "LOCAL ROOT"
	}
	return "CORE"
}

// PreferBadge is the header's badge for the preference; local-only is the one an owner
// turns on to be sure, so it is the one that must be visible.
func (s SelfStatus) PreferBadge() string {
	switch s.EffectivePrefer() {
	case PreferLocalOnly:
		return "LOCAL-ONLY"
	case PreferMarket:
		return "prefers market"
	}
	return "prefers local"
}

// The discovery states a status can report.
const (
	DiscoveryScanning    = "scanning"
	DiscoveryOff         = "off"
	DiscoveryUnavailable = "unavailable"
	// DiscoveryIdle: the switch is on but THIS process runs no engine - a one-shot
	// `roger edge` - so it must not claim to be scanning. The TUI and the console scan.
	DiscoveryIdle = "idle"
)

// SelfStatus is the record. Err set means the status could not be read: a surface shows
// that and claims nothing else, because "rooted at Core" printed over an unreadable record
// would be a confident lie the owner acts on.
type SelfStatus struct {
	Enrolled bool   `json:"enrolled"`
	Name     string `json:"name,omitempty"` // this machine's name on its Edge
	LoggedIn bool   `json:"logged_in"`
	// Authority is "Core" or the designated machine's name.
	Authority      string `json:"authority"`
	AuthorityLocal bool   `json:"authority_local"`
	AuthorityHere  bool   `json:"authority_here"`           // this machine holds the root
	AuthorityAddr  string `json:"authority_addr,omitempty"` // when here: what others enroll against
	Allowed        int    `json:"allowed"`                  // when here: user keys admitted so far
	// Root is what this Edge is rooted at, as a badge: RootLocal or RootCore. Prefer is the
	// owner's standing choice about route (PreferLocal, PreferMarket, PreferLocalOnly).
	// Two words that both mean "local" and must never share a label (mode.feature).
	Root      string `json:"root"`
	Prefer    string `json:"prefer"`
	Discovery string `json:"discovery"`
	IntervalS int64  `json:"interval_s,omitempty"`
	LastPass  int64  `json:"last_pass,omitempty"` // unix; 0 = no pass has completed
	Found     string `json:"found,omitempty"`     // PassSummary of the last pass
	Err       string `json:"error,omitempty"`
}

// PassSummary words one discovery pass: what it found, or that nothing answered.
func PassSummary(verified, candidates int) string {
	if verified == 0 && candidates == 0 {
		return "nothing answered"
	}
	out := ""
	if verified > 0 {
		out = plural(verified, "node") + " verified"
	}
	if candidates > 0 {
		if out != "" {
			out += ", "
		}
		out += plural(candidates, "candidate") + " seen"
	}
	return out
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// MachineLine is the THIS MACHINE fact.
func (s SelfStatus) MachineLine() string {
	if s.Enrolled {
		return s.Name + " · enrolled"
	}
	return "not enrolled · roger edge enroll <name> gives it a name on your Edge"
}

// AuthorityLines is the AUTHORITY fact: what roots this Edge, whether adding a node needs
// the network, and - for a machine that is not yet on an Edge - the two ways in.
func (s SelfStatus) AuthorityLines() []string {
	if s.AuthorityLocal {
		who := s.Authority
		if s.AuthorityHere {
			who += " (this machine)"
		}
		out := []string{who + " · enrolling a new node needs no network beyond this LAN"}
		if s.AuthorityHere {
			out = append(out, "this machine IS the authority: it holds the root's private half · "+
				plural(s.Allowed, "machine")+" may enroll against it · roger edge authority allow <user key> admits another")
		}
		return out
	}
	out := []string{"Core · enrolling a new node needs the network, to reach Core"}
	if s.Enrolled {
		return out
	}
	if !s.LoggedIn {
		out = append(out, "not logged in · roger login first, or roger edge authority local <name> forms an Edge with no internet")
	} else {
		out = append(out, "or roger edge authority local <name> forms an Edge with no internet")
	}
	return out
}

// DiscoveryLine is the DISCOVERY fact, with the last pass measured from now.
func (s SelfStatus) DiscoveryLine(now time.Time) string {
	switch s.Discovery {
	case DiscoveryOff:
		return "off · ROGERAI_EDGE_DISCOVERY=0 in this environment; unset it to scan this network"
	case DiscoveryUnavailable:
		return "unavailable on this network · roger edge scan says why"
	case DiscoveryIdle:
		out := "not scanning in this process · roger edge scan looks now; the TUI and the console scan while open"
		if s.IntervalS > 0 {
			out += " (every " + (time.Duration(s.IntervalS) * time.Second).String() + ")"
		}
		return out
	}
	out := "scanning this network"
	if s.IntervalS > 0 {
		out += " every " + (time.Duration(s.IntervalS) * time.Second).String()
	}
	if s.LastPass == 0 {
		return out + " · no pass has completed yet"
	}
	out += " · last pass " + Age(now.Sub(time.Unix(s.LastPass, 0))) + " ago"
	if s.Found != "" {
		out += ", " + s.Found
	}
	return out
}

// EnrollAgainstLine is the second way to add a node: another machine enrolling against
// this Edge's authority. It carries this machine's own address when it is the authority.
func (s SelfStatus) EnrollAgainstLine() string {
	addr := "<address>"
	if s.AuthorityHere && s.AuthorityAddr != "" {
		addr = s.AuthorityAddr
	}
	return fmt.Sprintf("roger edge enroll <name> --authority %s", addr)
}

// AddNodeLines names BOTH ways to add a node, in the terminal's voice (the console words
// the first line itself, since its adopt is a button rather than a key).
func (s SelfStatus) AddNodeLines() []string {
	return []string{
		"run RogerAI on another machine on this network; it appears here as a CANDIDATE, and a adopts it onto your Edge",
		"or enroll it against this Edge's authority: " + s.EnrollAgainstLine(),
	}
}

// Unavailable reports the pass that could not browse at all (no multicast plane).
func (r Report) Unavailable() bool {
	for _, n := range r.Notes {
		if n == noteUnavailable {
			return true
		}
	}
	return false
}
