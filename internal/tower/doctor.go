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
	// Clock is what doctor found out about time. A Tower needs no GPU and runs no model;
	// its requirements are a stable address, an exposed port, bandwidth and a SYNCHRONISED
	// CLOCK, and the last of those is the one an operator has no other way to discover is
	// wrong. See clock.go for why it is now load-bearing rather than hygiene.
	Clock    ClockCheck
	Problems []string
	Notes    []string
	OK       bool
}

// DoctorOption tunes what doctor is allowed to do. It exists for exactly one reason: the
// measured clock offset needs an external reference, and a library function that dials the
// internet whenever a unit test calls it is a library whose unit tests are about the
// internet. The default is offline; the CLI opts in.
type DoctorOption func(*doctorOpts)

type doctorOpts struct {
	clock ClockSource
	// refusal is why no clock source was supplied, when the caller had a REASON rather
	// than simply not caring. A standalone Tower is the case: not measuring its clock is a
	// deliberate consequence of the isolation promise, and the report should say that
	// instead of the generic "nobody gave doctor a reference".
	refusal string
}

// WithClockSource lets doctor measure how far this machine's clock is from real time.
// Without it the clock section still reports whether a time daemon is disciplining the
// clock - the half that carries a repair - and says plainly that the offset was not
// measured rather than implying it is fine.
func WithClockSource(src ClockSource) DoctorOption {
	return func(o *doctorOpts) { o.clock = src }
}

// WithClockSourceRefused records that the caller deliberately withheld a time reference,
// and why. It exists so the report can distinguish "not measured because nobody asked"
// from "not measured because measuring it would have broken a promise this Tower makes",
// which are the same absence and completely different facts.
func WithClockSourceRefused(why string) DoctorOption {
	return func(o *doctorOpts) { o.refusal = why }
}

// Doctor inspects effective configuration and reports what it will do when started.
func Doctor(c *Config, opts ...DoctorOption) Report {
	var o doctorOpts
	for _, opt := range opts {
		opt(&o)
	}
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

	// THE CLOCK. A measured skew at or beyond protocol.SigMaxSkew is a PROBLEM rather than
	// a note, and it is the only thing doctor calls a problem that is not a configuration
	// mistake - because its consequence is total. Every signed node poll carries a
	// timestamp, VerifyRequest refuses one more than that far from THIS machine's clock,
	// and the Tower therefore refuses every honest node with a 401 that says nothing about
	// time. A Tower in that state is not degraded, it relays nothing.
	r.Clock = checkClock(o.clock, o.refusal)
	switch {
	case r.Clock.Fatal():
		r.Problems = append(r.Problems, fmt.Sprintf(
			"this machine's clock is %s: past the %s signature window, so every correctly "+
				"signed request from every honest node is refused and this Tower relays nothing. %s",
			signedDuration(r.Clock.Offset), ClockFatalSkew, clockRepair(r.Clock)))
	case r.Clock.Marginal():
		r.Notes = append(r.Notes, fmt.Sprintf(
			"this machine's clock is %s. Nothing fails yet, but the %s window is a budget shared "+
				"with the node at the other end - an unsynchronised node is ordinary, and a Tower "+
				"that spends this much of the margin on itself leaves that much less for them. %s",
			signedDuration(r.Clock.Offset), ClockFatalSkew, clockRepair(r.Clock)))
	case r.Clock.DisciplineKnown && !r.Clock.Disciplined:
		// No measured skew, or none worth reporting, but nothing is holding it there. This
		// is the state that becomes the case above on its own schedule.
		r.Notes = append(r.Notes, "no time daemon is disciplining this machine's clock, so "+
			"nothing keeps it inside the signature window as it drifts. "+r.Clock.DisciplineNote)
	}

	r.OK = len(r.Problems) == 0
	return r
}

// clockRepair returns the instruction that goes with a skew finding. When the kernel has
// already told us nothing is disciplining the clock, that IS the repair; otherwise the
// clock is being steered and is still wrong, which is a different and rarer fault worth
// naming as such rather than answering with advice that has already been taken.
func clockRepair(c ClockCheck) string {
	if c.DisciplineKnown && !c.Disciplined {
		return c.DisciplineNote
	}
	if c.DisciplineKnown && c.Disciplined {
		return "a time daemon IS running and the clock is still out, so check what it is " +
			"pointed at (a stale or unreachable NTP server steers nothing) rather than installing another"
	}
	return "enable a time daemon on this host and confirm it is reaching its NTP servers"
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
	// The clock section sits above the notes and below the listeners: it is a property of
	// the MACHINE rather than of the configuration, and an operator scanning this report
	// should meet it as one of the Tower's requirements, not as a footnote.
	fmt.Fprint(&b, r.Clock.String())
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
