package tower

// clock.go is doctor's clock check, and it is newly load-bearing rather than hygiene.
//
// A Tower's requirements are not a node's. It needs no GPU and runs no model: what it
// needs is a stable address, a port the world can reach, bandwidth, and a clock that
// agrees with everybody else's. Until recently the last of those was the soft one on that
// list. It is not any more.
//
// WHY. Every signed node poll carries a unix timestamp, and `protocol.VerifyRequest`
// refuses one more than `protocol.SigMaxSkew` - five minutes - from the VERIFIER's clock,
// in either direction. The verifier is this Tower. So a Tower whose clock is wrong by more
// than five minutes refuses every correctly signed request from every honest node, with a
// 401 that says nothing about time, and relays nothing at all. And the failure is
// asymmetric in a way that makes it the operator's problem twice over: the node is fine,
// the node's operator sees their earnings stop, and the machine actually at fault is this
// one.
//
// The margin is also SHARED, which is the part that is easy to miss. Five minutes is the
// whole budget for the Tower's error plus the node's, and an unsynchronised clock is the
// ordinary condition of a machine in a spare room - docs/relay-selection-design.md §5.4b
// says exactly that, and refuses to fix clock problems by rejecting nodes for having them.
// A Tower that spends half the budget on itself halves what is left for every node that
// talks to it.
//
// docs/relay-selection-design.md §5.4c is the third reason. The hub's replay defences were
// rebuilt around clock-domain problems: a lagging node was proved unable to make its first
// request to a freshly started hub, and was told it was replaying. The comparison that
// caused it has been removed in favour of a per-process epoch, so no clock is consulted
// there any more - but the skew window in VerifyRequest remains, and doctor is where an
// operator should find out their clock is wrong instead of reading 401s.
//
// TWO INDEPENDENT PROBES, because they answer different questions and either can be
// unavailable:
//
//  1. IS ANYTHING KEEPING THE CLOCK RIGHT? The kernel knows whether a time daemon is
//     disciplining it. This is offline, instantaneous and authoritative about the
//     mechanism, and it is what turns "your clock is 4 minutes out" into a repair.
//  2. HOW WRONG IS IT RIGHT NOW? Only an external reference can answer this, so it is an
//     SNTP query and it is OPT-IN (WithClockSource). It also does not live in this package,
//     and that is not tidiness: TestStandaloneHasNoOutboundNetworkCallAtAll is a Phase 1
//     gate that reads this package's source and fails if any file in it acquires the
//     ability to reach the network, because standalone isolation has to be a proof rather
//     than an omission. The dialer therefore lives in internal/clockprobe, this package
//     holds only the ClockSource function type, and `roger-tower doctor` is what joins
//     them - deliberately, so the outbound path is something a caller chooses rather than
//     something the Tower carries.
//
// Both degrade to "could not determine", which is a fine answer and better than a guess.

import (
	"fmt"
	"time"

	"rogerai.fm/roger/v5/internal/protocol"
)

// ClockFatalSkew is where a Tower stops working rather than merely working badly: at or
// beyond it, VerifyRequest refuses a perfectly signed request from a perfectly synchronised
// node, so the Tower relays nothing. It IS protocol.SigMaxSkew rather than a number chosen
// to look like it, so the two can never drift apart.
const ClockFatalSkew = protocol.SigMaxSkew

// ClockWarnSkew is where a Tower starts spending margin that is not its to spend. The five
// minutes is one budget covering the Tower's error AND the node's, and §5.4b's whole
// argument is that an unsynchronised node is ordinary and must not be refused for it. A
// tenth of the budget is the point where this Tower's own error stops being noise against
// the error it is supposed to be tolerating in others.
const ClockWarnSkew = protocol.SigMaxSkew / 10

// ClockSource returns what an external reference believes the time is, and names itself so
// the report can say what it compared against. An error means the reference could not be
// reached, which is not a clock problem and must not be reported as one.
type ClockSource func() (now time.Time, reference string, err error)

// ClockCheck is doctor's answer about time. Every field is paired with a "known" flag for
// the same reason the hardware preflight's are: a zero offset and an unmeasured offset are
// different facts, and printing the second as the first tells an operator their clock is
// perfect when nobody looked.
type ClockCheck struct {
	// DisciplineKnown / Disciplined: whether a time daemon is keeping this clock right.
	// This is the field that carries a repair, because "install and enable one" is an
	// instruction and "you are four minutes out" is only a symptom.
	DisciplineKnown bool
	Disciplined     bool
	DisciplineNote  string

	// OffsetKnown / Offset: how far this clock is from an external reference, positive
	// when this machine is AHEAD. Reference names what was asked.
	OffsetKnown bool
	Offset      time.Duration
	Reference   string
	OffsetNote  string
}

// Fatal reports a measured skew at or beyond the window VerifyRequest enforces - the state
// in which this Tower refuses every honest node.
func (c ClockCheck) Fatal() bool {
	return c.OffsetKnown && (c.Offset >= ClockFatalSkew || c.Offset <= -ClockFatalSkew)
}

// Marginal reports a measured skew large enough to be eating the tolerance nodes need,
// without yet breaking anything.
func (c ClockCheck) Marginal() bool {
	return c.OffsetKnown && !c.Fatal() && (c.Offset >= ClockWarnSkew || c.Offset <= -ClockWarnSkew)
}

// checkClock runs the offline discipline probe and, when a source is supplied, the measured
// offset.
func checkClock(src ClockSource, refusal string) ClockCheck {
	c := ClockCheck{}
	c.Disciplined, c.DisciplineKnown, c.DisciplineNote = clockDisciplined()
	if src == nil {
		c.OffsetNote = "not measured: doctor was given no time reference to compare against"
		if refusal != "" {
			c.OffsetNote = refusal
		}
		return c
	}
	now, ref, err := src()
	c.Reference = ref
	if err != nil {
		// A blocked UDP 123 is the ordinary case in a hardened network and says nothing
		// about the clock. Reporting it as a clock fault would send an operator to fix
		// something that is not broken.
		c.OffsetNote = fmt.Sprintf("could not reach %s (%v) - this says nothing about your clock, only that it was not checked", ref, err)
		return c
	}
	// Positive means this machine is AHEAD: time.Since(reference) is local-minus-real,
	// which is exactly that, and the sign is spelled out in words when it is printed.
	c.Offset, c.OffsetKnown = time.Since(now).Round(time.Millisecond), true
	return c
}

// String renders the clock section of doctor's report. It leads with the mechanism,
// because that is the half that carries a repair.
func (c ClockCheck) String() string {
	var b []string
	switch {
	case !c.DisciplineKnown:
		b = append(b, "clock: sync state not readable on this platform - "+c.DisciplineNote)
	case c.Disciplined:
		b = append(b, "clock: disciplined by a time daemon")
	default:
		b = append(b, "clock: NOT disciplined - no time daemon is keeping this clock right ("+c.DisciplineNote+")")
	}
	switch {
	case !c.OffsetKnown:
		b = append(b, "clock offset: not determined - "+c.OffsetNote)
	default:
		b = append(b, fmt.Sprintf("clock offset: %s vs %s", signedDuration(c.Offset), c.Reference))
	}
	out := ""
	for _, l := range b {
		out += l + "\n"
	}
	return out
}

// signedDuration prints an offset with its direction spelled out, because "ahead" and
// "behind" are what an operator needs and a leading minus sign is not.
func signedDuration(d time.Duration) string {
	switch {
	case d > 0:
		return d.String() + " AHEAD of real time"
	case d < 0:
		return (-d).String() + " BEHIND real time"
	default:
		return "0s (in step)"
	}
}
