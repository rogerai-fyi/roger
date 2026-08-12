// Package reputation records what became of each edge attempt, per Tower, so a pattern can be
// seen that no single attempt shows.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # WHY A LEDGER RATHER THAN A COUNTER
//
// The spec's rule is "the signal is in the RATE, not the single attempt": a customer closing
// a laptop settles uncorroborated, and one of those is nothing, while a Tower whose
// uncorroborated share is unlike the fleet's is worth a look. A bare counter cannot answer
// "unlike the fleet's" - it needs the denominator too - and it cannot answer "over the last
// while" once old evidence should age out. So each outcome is recorded with its time, and the
// questions are asked over a window.
//
// # OUTCOMES ARE FACTS, NOT VERDICTS
//
// Recording an uncorroborated attempt is not an accusation, and recording a failed canary is
// not a sentence. This package stores what happened and computes rates; whether a rate is bad
// enough to act on is a policy decision made elsewhere (admit.Transition to quarantine), on
// evidence this provides. Keeping the two apart is what lets the threshold move without
// rewriting how evidence is kept, and lets the evidence be shown to an operator disputing a
// decision.
//
// # NOTHING HERE IS MONEY
//
// A reputation rate influences whether a Tower keeps getting work; it does not reverse a
// settlement or claw back pay. "Individual attempts already settled are not reversed by the
// rate alone" is the spec's line and it is a property of this package: it only ever reads the
// outcomes settlement already wrote, and never writes back into settlement.
package reputation

import (
	"errors"
	"time"
)

// Outcome is what became of one edge attempt.
type Outcome string

const (
	// Corroborated: a Station receipt and a matching consumer acknowledgement.
	Corroborated Outcome = "corroborated"
	// Uncorroborated: settled on the receipt alone, no acknowledgement. Ordinary, not a fault.
	Uncorroborated Outcome = "uncorroborated"
	// Disputed: the two ends signed different response digests. Attributable to the relay.
	Disputed Outcome = "disputed"
	// CanaryPass / CanaryFail: a Core-originated probe the Tower did or did not carry.
	CanaryPass Outcome = "canary_pass"
	CanaryFail Outcome = "canary_fail"
	// AuditMismatch: a sampled transcript did not hash to what both ends signed.
	AuditMismatch Outcome = "audit_mismatch"
)

func (o Outcome) valid() bool {
	switch o {
	case Corroborated, Uncorroborated, Disputed, CanaryPass, CanaryFail, AuditMismatch:
		return true
	}
	return false
}

// Event is one recorded outcome.
type Event struct {
	TowerID   string
	AttemptID string
	Outcome   Outcome
	At        time.Time
}

// Tally is what a window of a Tower's outcomes adds up to.
type Tally struct {
	TowerID string
	// Total is every recorded attempt in the window - the denominator that makes a rate mean
	// something. Without it "ten uncorroborated" cannot be told apart between a Tower that
	// served ten and one that served ten thousand.
	Total int
	Corroborated,
	Uncorroborated,
	Disputed,
	CanaryPass,
	CanaryFail,
	AuditMismatch int
}

// Without subtracts another tally from this one, component by component. It exists so a
// Tower can be judged against the REST of the fleet rather than a fleet that includes itself:
// comparing a Tower to a baseline it is part of dilutes exactly when it matters most - a
// single bad Tower on a small network is most of its own baseline, and would never look
// unusual relative to itself.
func (t Tally) Without(other Tally) Tally {
	return Tally{
		TowerID:        t.TowerID,
		Total:          t.Total - other.Total,
		Corroborated:   t.Corroborated - other.Corroborated,
		Uncorroborated: t.Uncorroborated - other.Uncorroborated,
		Disputed:       t.Disputed - other.Disputed,
		CanaryPass:     t.CanaryPass - other.CanaryPass,
		CanaryFail:     t.CanaryFail - other.CanaryFail,
		AuditMismatch:  t.AuditMismatch - other.AuditMismatch,
	}
}

// UncorroboratedRate is the share of settled attempts with no acknowledgement.
//
// Over SETTLED attempts only - corroborated plus uncorroborated - because canaries and audits
// are a different question and would dilute the very rate the spec names. A Tower with no
// settled attempts has no rate rather than a zero one: dividing by nothing is not a clean
// bill, it is no evidence, and the two must not look alike.
func (t Tally) UncorroboratedRate() (rate float64, known bool) {
	settled := t.Corroborated + t.Uncorroborated
	if settled == 0 {
		return 0, false
	}
	return float64(t.Uncorroborated) / float64(settled), true
}

// CanaryFailRate is the share of canaries this Tower did not carry.
func (t Tally) CanaryFailRate() (rate float64, known bool) {
	canaries := t.CanaryPass + t.CanaryFail
	if canaries == 0 {
		return 0, false
	}
	return float64(t.CanaryFail) / float64(canaries), true
}

// DisputeRate is the share of settled attempts the consumer's account of the bytes disagreed
// with the Station's. It is deliberately a RATE, not a count: a single dispute is a consumer
// who may be lying, and cannot be attributed; a Tower whose dispute share is unlike the
// fleet's is a Tower whose relay may be altering responses. Over settled attempts plus the
// disputes themselves, because a dispute IS a settled attempt (on the receipt alone).
func (t Tally) DisputeRate() (rate float64, known bool) {
	settled := t.Corroborated + t.Uncorroborated + t.Disputed
	if settled == 0 {
		return 0, false
	}
	return float64(t.Disputed) / float64(settled), true
}

// Store is where outcomes live.
type Store interface {
	// Record appends one outcome. Idempotent on (tower, attempt, outcome): an attempt has one
	// terminal settlement outcome, and a retry of the write that records it must not count
	// twice - double-counting is how a reliable Tower acquires a reputation it did not earn.
	Record(e Event) error
	// Tally sums a Tower's outcomes at or after `since`.
	Tally(towerID string, since time.Time) (Tally, error)
	// FleetTally sums every Tower's outcomes at or after `since`, so "unlike the fleet's" has
	// a fleet to compare against.
	FleetTally(since time.Time) (Tally, error)
	// Reap drops outcomes older than a cutoff. Reputation is a moving window; evidence that
	// has aged out of every window anyone asks about is a table that only grows.
	Reap(before time.Time) (int64, error)
}

func checkEvent(e Event) error {
	switch {
	case e.TowerID == "":
		return errors.New("an outcome belongs to a Tower")
	case e.AttemptID == "":
		return errors.New("an outcome belongs to an attempt")
	case !e.Outcome.valid():
		return errors.New("that is not a recognized outcome")
	case e.At.IsZero():
		return errors.New("an outcome is recorded at a time")
	}
	return nil
}
