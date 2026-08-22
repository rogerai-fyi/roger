package reputation

// evaluate.go turns a Tower's outcomes into a decision: leave it alone, look at it, or take
// it off the network.
//
// It is deliberately the ONLY place a threshold lives. The store records facts; this reads
// them and judges; the caller acts. An operator disputing a decision can be shown the tally
// and the thresholds side by side, and a threshold can move without touching how evidence is
// kept.

// Verdict is what the evidence says should happen to a Tower.
type Verdict string

const (
	// Clean: nothing in the window warrants action.
	Clean Verdict = "clean"
	// Investigate: a rate is unusual enough to look at, but not to punish. The spec's line -
	// "flagged for investigation", and "individual attempts already settled are not reversed
	// by the rate alone".
	Investigate Verdict = "investigate"
	// Quarantine: evidence a Tower is not doing the job - repeated canary failures, or a
	// transcript that did not match what both ends signed. This warrants taking it off.
	Quarantine Verdict = "quarantine"
)

// Policy is the set of thresholds. Zero values are not usable defaults - a policy with a zero
// minimum sample would act on a single attempt, which is the opposite of "the signal is in
// the rate" - so DefaultPolicy exists and callers should start from it.
type Policy struct {
	// MinSettled is how many settled attempts a Tower needs before its uncorroborated rate is
	// judged at all. Below it, one closed laptop is the whole sample and means nothing.
	MinSettled int
	// UncorroboratedMargin is how far above the FLEET's uncorroborated rate a Tower's own may
	// sit before it is flagged. Relative to the fleet, not absolute, because a network where
	// most clients never ack has a high baseline that is nobody's fault.
	UncorroboratedMargin float64
	// MinCanaries and MaxCanaryFailRate govern the quarantine decision: enough canaries to be
	// sure, and a failure share above which a Tower is not carrying work.
	MinCanaries       int
	MaxCanaryFailRate float64
	// DisputeMargin is how far above the FLEET's dispute rate a Tower's own may sit before it
	// is flagged. A dispute cannot be attributed from one attempt - the consumer may be lying -
	// so an unusual RATE is an Investigate (look closer), never an automatic Quarantine.
	DisputeMargin float64
}

// DefaultPolicy is a starting point, not a law. The numbers are conservative: flag readily,
// quarantine only on strong evidence, because a wrong quarantine costs an honest operator
// their livelihood and a wrong flag costs a human five minutes.
func DefaultPolicy() Policy {
	return Policy{
		MinSettled:           20,
		UncorroboratedMargin: 0.30,
		MinCanaries:          5,
		MaxCanaryFailRate:    0.40,
		DisputeMargin:        0.15,
	}
}

// Evaluate judges a Tower's window against the fleet's.
//
// Quarantine is checked FIRST and independently of the uncorroborated rate: a Tower failing
// canaries is not carrying work, full stop, and no amount of otherwise-normal settlement
// buys that back. An audit mismatch is the same kind of evidence - the Tower forwarded, or
// stood behind, material that does not match what was signed - and one is enough to warrant
// taking it off pending a look.
//
// # WHAT IS THE TOWER'S, AND WHAT IS NOT
//
// Tally.StationFault appears nowhere below, and its absence is the policy rather than an
// omission. A suspension takes every honest node behind a Tower off the fabric with it, so the
// evidence that triggers one must be evidence about the TOWER - and on this path the Tower is a
// sufficient cause of almost everything Core can see, because everything Core can see passed
// through its hands. So the default is that a finding is the Tower's, and a finding leaves it
// only on proof the Tower could not have manufactured: material signed by a key the Tower does
// not hold, contradicting other material signed by that same key.
//
// The asymmetry is deliberate and it is the anti-laundering rule. A Tower answers for the
// excuses it forwards - "that Station did not keep the transcript" is unverifiable and is
// therefore its own claim to stand behind - while a Station answers for what it signed. If it
// were the other way around, the cheapest lie a Tower could tell would also be its cheapest
// escape.
//
// CanaryFail in particular stays whole. A canary rides the Tower end to end; a Tower can drop
// it, delay it past the deadline, substitute the sealed answer, or simply report that nobody is
// serving the Station - so no canary failure can be moved to a Station without handing a
// black-holing Tower a way to point at its own victims. What was wrong was never the
// attribution of a single probe but the AMPLIFICATION: probe budget is spent per Station and
// the verdict is read per Tower, so a dead Station soaked the sweep and its Tower paid for
// every failure. That is fixed where it is caused, in the probe rotation, not here.
func (p Policy) Evaluate(tower, fleet Tally) Verdict {
	if tower.AuditMismatch > 0 {
		return Quarantine
	}
	if failRate, known := tower.CanaryFailRate(); known &&
		tower.CanaryPass+tower.CanaryFail >= p.MinCanaries && failRate > p.MaxCanaryFailRate {
		return Quarantine
	}

	towerRate, known := tower.UncorroboratedRate()
	if !known || tower.Corroborated+tower.Uncorroborated < p.MinSettled {
		// Not enough settled attempts to say anything. Not clean-because-good, clean-because-
		// unknown, which for the purpose of taking action is the same: do nothing.
		return Clean
	}
	// The fleet's rate is the baseline. If the fleet has no settled attempts either, there is
	// nothing to be unusual RELATIVE to, so fall back to the absolute margin from zero.
	fleetRate, fleetKnown := fleet.UncorroboratedRate()
	if !fleetKnown {
		fleetRate = 0
	}
	if towerRate-fleetRate > p.UncorroboratedMargin {
		return Investigate
	}
	// A DISPUTE RATE unlike the fleet's is a Tower whose relay may be altering responses. It
	// is an Investigate, not a Quarantine, because a single dispute cannot be attributed - only
	// the pattern is evidence, and even the pattern could be an unusually adversarial set of
	// consumers rather than a bad relay. A human decides; the rate only points.
	if towerDispute, known := tower.DisputeRate(); known {
		fleetDispute, fleetKnown := fleet.DisputeRate()
		if !fleetKnown {
			fleetDispute = 0
		}
		if towerDispute-fleetDispute > p.DisputeMargin {
			return Investigate
		}
	}
	return Clean
}
