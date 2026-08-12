package tower

import (
	"fmt"
	"strings"
)

// Report is doctor's answer: what this Tower is, whether it can reach the public
// network, and what an operator should know before starting it.
//
// The reachability fields exist because "standalone makes no RogerAI connection" is a
// claim an operator has to be able to CHECK. Doctor answers it from the effective
// configuration; the Phase 1 gate then proves it again with a packet capture.
type Report struct {
	Mode                 Mode
	ReachesPublicNetwork bool
	PublicAuthority      string
	AllListenersLoopback bool
	Listeners            []string
	// Unenforced is every configured field this build ignores. Reported so an operator is
	// never silently overruled by their own configuration file.
	Unenforced []string
	Problems   []string
	Notes      []string
	OK         bool
}

// Doctor inspects effective configuration and reports what it will do when started.
func Doctor(c *Config) Report {
	r := Report{
		Mode:                 c.Mode,
		PublicAuthority:      c.PublicAuthority(),
		Listeners:            c.ListenAddresses(),
		AllListenersLoopback: true,
	}
	r.ReachesPublicNetwork = r.PublicAuthority != "" || c.AdvertisesPublicly()
	r.Unenforced = c.Unenforced()

	if len(r.Listeners) == 0 {
		r.Notes = append(r.Notes, "no data plane configured: this Tower relays no consumer "+
			"traffic, so it takes no load off Roger Core and earns nothing for its operator")
	}
	for _, addr := range r.Listeners {
		if !isLoopback(addr) {
			r.AllListenersLoopback = false
			r.Notes = append(r.Notes, fmt.Sprintf(
				"%s is not loopback: this Tower will be reachable from other hosts", addr))
		}
	}

	// No standalone-reachability check here: PublicAuthority and AdvertisesPublicly are
	// themselves gated on mode, so a standalone Tower cannot report reachability at all.
	// A branch that can never fire would imply a check that is not real.
	if c.Mode == ModeJoined && r.PublicAuthority == "" {
		r.Problems = append(r.Problems, "joined mode has no authority to connect to")
	}

	r.OK = len(r.Problems) == 0
	return r
}

func isLoopback(addr string) bool {
	return strings.HasPrefix(addr, "127.") || strings.HasPrefix(addr, "[::1]:") || strings.HasPrefix(addr, "localhost:")
}

// String renders the report for a terminal. It leads with the two things an operator
// most needs: which mode this is, and whether it talks to RogerAI.
func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "mode: %s\n", r.Mode)
	if r.ReachesPublicNetwork {
		fmt.Fprintf(&b, "public network: connects to %s\n", r.PublicAuthority)
	} else {
		fmt.Fprintf(&b, "public network: no connection (this Tower is fully local)\n")
	}
	switch {
	case len(r.Listeners) == 0:
		fmt.Fprintf(&b, "listeners: none\n")
	case r.AllListenersLoopback:
		fmt.Fprintf(&b, "listeners: loopback only\n")
	default:
		fmt.Fprintf(&b, "listeners: some are NOT loopback\n")
	}
	for _, l := range r.Listeners {
		fmt.Fprintf(&b, "  - %s\n", l)
	}
	// LOUD, and above the notes. An operator who configured a limit and is not getting one
	// has to trip over it rather than find it in a list they skim.
	for _, u := range r.Unenforced {
		fmt.Fprintf(&b, "IGNORED: %s\n", u)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "note: %s\n", n)
	}
	for _, p := range r.Problems {
		fmt.Fprintf(&b, "PROBLEM: %s\n", p)
	}
	if r.OK {
		fmt.Fprintf(&b, "doctor: OK\n")
	} else {
		fmt.Fprintf(&b, "doctor: NOT OK\n")
	}
	return b.String()
}
