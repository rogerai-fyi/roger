// Package attempt is the single authoritative state of one public attempt, and the signed,
// hash-chained history of how it got there.
//
// Spec: features/tower/attempt_lifecycle.feature (founder approved 2026-08-03). Its opening
// line is the design: "Tower, Station, client, and transport messages are EVIDENCE for Roger
// Core; none can write attempt state directly or revive a terminal attempt." Everything here
// is arranged so that remains true no matter who is talking.
//
// # WHY IT EXISTS AT ALL
//
// Tower-backed work is compensated through the shared earning lots, and the reason is this package: money needs a
// state nobody can dispute afterwards. Which attempt executed, exactly once, and what its
// one terminal outcome was, has to be a fact recorded before the money moves rather than
// something reconstructed from logs when somebody complains.
//
// # TWO OBJECTS, AND THE SPLIT BETWEEN THEM IS THE POINT
//
//	AttemptEventV1              Core-private. Carries the hold, the funding reservation, the
//	                            money state. Nobody outside Core ever sees one.
//	AttemptIssueCommitmentV1    disclosure-safe, and the ONLY one a Tower or Station gets.
//	                            It proves an attempt was really issued and anchors which
//	                            ledger position it took - and it contains no hold id, amount,
//	                            currency, price, funding source, or client identity.
//
// A relay that could read the money would learn what every request is worth, whose account
// is paying, and how much room is left on it. So the commitment is not the event with fields
// removed - it is a different object that never had them, which is a property a test can
// check rather than a habit somebody has to maintain.
//
// # THE CHAIN
//
// Revision 1 is `issued` with NO prior. Every later revision is exactly the accepted one plus
// one and binds the immediately prior event's complete hash, under a compare-and-swap. Exact
// replay is idempotent - a retried commit of identical bytes is the same fact stated twice -
// while a skipped revision, a wrong prior, or DIFFERENT bytes at a revision already taken all
// fail before any state or hold moves.
package attempt

import (
	"errors"
	"fmt"
	"time"

	"rogerai.fm/roger/v5/internal/towerobj"
)

// The object identities, and the tags their deterministic IDs derive from. The tags are part
// of the derivation so an id can never be reused as another object's id.
const (
	TypeEvent      = "attempt.event"
	TypeCommitment = "attempt.issue_commitment"
	Version        = 1

	eventIDTag      = "AttemptEventV1-id-v1"
	commitmentIDTag = "AttemptIssueCommitmentV1-id-v1"
)

// The states. One authoritative value per attempt, and four of them are terminal.
const (
	StateIssued           = "issued"
	StateLeased           = "leased"
	StateExecuting        = "executing"
	StateEvidenceComplete = "evidence_complete"
	StateSettled          = "settled"
	StateFailed           = "failed"
	StateExpired          = "expired"
	StateCancelled        = "cancelled"
)

// Terminal reports whether a state can never change again. "settled, failed, expired,
// cancelled | any different state" is the whole of the spec's unlisted-transition table for
// these four: a terminal attempt is not revivable by anyone, including Core.
func Terminal(state string) bool {
	switch state {
	case StateSettled, StateFailed, StateExpired, StateCancelled:
		return true
	}
	return false
}

// HoldEffect is what a transition does to the money held for this attempt.
type HoldEffect string

const (
	// HoldReserved: unchanged and still reserved.
	HoldReserved HoldEffect = "reserved"
	// HoldReleased: released exactly once. Every terminal state but settled.
	HoldReleased HoldEffect = "released"
	// HoldCaptured: the exact cost captured and the exact remainder released.
	HoldCaptured HoldEffect = "captured"
)

// Kind is the closed set of events that may move an attempt.
//
// Named for what Core OBSERVED, not for the state they produce, because that is what they
// are: an event is evidence Core accepted, and the resulting state is the table's answer to
// it rather than the caller's request.
type Kind string

const (
	KindIssued              Kind = "issued"
	KindDispatchAccepted    Kind = "dispatch_accepted"
	KindDispatchFailed      Kind = "dispatch_failed"
	KindClaimObserved       Kind = "claim_observed"
	KindEvidenceObserved    Kind = "evidence_observed"
	KindExecutionFailed     Kind = "execution_failed"
	KindEvidenceInvalid     Kind = "evidence_invalid"
	KindSettlementCommitted Kind = "settlement_committed"
	KindDeadlineSwept       Kind = "deadline_swept"
	KindCancelSwept         Kind = "cancel_swept"
	KindFinalizationCeiling Kind = "finalization_ceiling"
)

type outcome struct {
	to   string
	hold HoldEffect
}

// transitions is the spec's "Nonterminal attempt transitions are exhaustive" outline,
// verbatim, and it is the ONLY thing that decides a state change.
//
// A table rather than a switch, and closed rather than defaulting: the companion outline in
// the spec is "Every unlisted attempt transition fails without authority", so anything absent
// here must be refused. A default branch would quietly invent authority for a row nobody
// approved.
var transitions = map[string]map[Kind]outcome{
	StateIssued: {
		KindDispatchAccepted: {StateLeased, HoldReserved},
		KindDispatchFailed:   {StateFailed, HoldReleased},
		KindDeadlineSwept:    {StateExpired, HoldReleased},
		KindCancelSwept:      {StateCancelled, HoldReleased},
	},
	StateLeased: {
		KindClaimObserved: {StateExecuting, HoldReserved},
		// A complete result observed WITHOUT a prior executing observation is legal: the
		// Station may finish before Core ever sees the claim, and refusing the evidence
		// because an intermediate observation was missed would fail an attempt that worked.
		KindEvidenceObserved: {StateEvidenceComplete, HoldReserved},
		KindExecutionFailed:  {StateFailed, HoldReleased},
		KindDeadlineSwept:    {StateExpired, HoldReleased},
		KindCancelSwept:      {StateCancelled, HoldReleased},
	},
	StateExecuting: {
		KindEvidenceObserved: {StateEvidenceComplete, HoldReserved},
		KindExecutionFailed:  {StateFailed, HoldReleased},
		KindDeadlineSwept:    {StateExpired, HoldReleased},
		KindCancelSwept:      {StateCancelled, HoldReleased},
	},
	StateEvidenceComplete: {
		KindSettlementCommitted: {StateSettled, HoldCaptured},
		KindEvidenceInvalid:     {StateFailed, HoldReleased},
		KindFinalizationCeiling: {StateFailed, HoldReleased},
		// DELIBERATELY NO KindDeadlineSwept. "expired solely because settlement storage was
		// temporarily unavailable after timely evidence" is in the spec's UNLISTED table:
		// evidence arrived in time, and our own storage being slow is not the consumer's
		// fault or the operator's. It fails on the finalization ceiling instead, which is a
		// different clock and a different reason.
	},
}

// Next reports what an event does to an attempt in this state, and whether it may happen.
func Next(from string, k Kind) (string, HoldEffect, bool) {
	out, ok := transitions[from][k]
	return out.to, out.hold, ok
}

// Refusals. Each is a distinct answer because a caller does something different about each.
var (
	ErrNotFound      = errors.New("no such attempt")
	ErrTerminal      = errors.New("this attempt has reached a terminal state and cannot change")
	ErrNotAllowed    = errors.New("that event is not allowed from this state")
	ErrRevision      = errors.New("that is not the next revision for this attempt")
	ErrConflict      = errors.New("a different event is already committed at that revision")
	ErrAlreadyIssued = errors.New("this attempt has already been issued")
)

// Origin is where the Station serving this attempt sits.
type Origin string

const (
	OriginDirect Origin = "direct"
	OriginJoined Origin = "joined"
)

// Hold is the money reserved for one attempt, as the private event records it.
//
// The amount is an integer in the currency's smallest unit with an explicit scale, never a
// float: a rate multiplied by a token count and charged to somebody must not depend on how
// two machines happened to round.
type Hold struct {
	ID       string
	Currency string
	Unit     string
	Scale    int64
	Amount   int64
	State    string
}

// NoHold is the hold on work nobody is charged for.
//
// A truthful statement rather than a placeholder: this attempt reserved nothing (the edge
// path holds against the CONSUMER'S wallet at authorize, not here), so the amount really is
// zero, and the event says so in the same members a real hold uses. The alternative - omitting the hold - would make "this attempt reserved
// nothing" indistinguishable from "somebody forgot to record what it reserved", and only one
// of those is safe to settle against.
//
// The funding reservation hashes stay empty for the same reason and are filled in when the
// funding-source ledger exists; the attempt chain does not wait for it, because the record of
// WHICH attempt executed is worth having before there is any money to attach to it.
func NoHold(attemptID string) Hold {
	return Hold{
		ID: "nohold-" + attemptID, Currency: "USD", Unit: "micro", Scale: 6, Amount: 0,
		State: "none",
	}
}

// IssueSpec is everything an attempt is created from.
//
// The money-bearing members and the disclosure-safe ones are separated in the TYPE rather
// than by convention, so building a commitment cannot accidentally reach a hold.
type IssueSpec struct {
	Network   string
	JobID     string
	RequestID string
	AttemptID string
	Origin    Origin

	// GrantHash and LeaseHash bind the exact authority this attempt was issued under.
	// LeaseHash is empty for a direct Station: the spec calls that a canonical ABSENCE, and
	// it is represented as an omitted member rather than an empty string, so "no lease"
	// cannot be confused with "a lease whose hash is nothing".
	GrantHash string
	LeaseHash string

	// The money. Never reaches the commitment.
	Hold                 Hold
	ReservationHash      string
	ReservationSet       string
	CompensationSnapshot string

	TowerRevision   int64
	StationRevision int64

	Deadline            time.Time
	FinalizationCeiling time.Time

	// LedgerIndex and the commit tuple are assigned by CORE, independently of anything a
	// caller supplied. "key validity, compromise cutoff, deadlines, and event ordering derive
	// from the independently assigned commit tuple, never signer issue time" - a signer's own
	// clock is something a compromised signer controls.
	LedgerIndex int64
	CommitTime  time.Time
	Sequence    int64
}

// Commitment is AttemptIssueCommitmentV1: what a Tower and a Station are given.
type Commitment struct {
	ID     string
	Signed []byte
}

// Event is AttemptEventV1: Core's private, signed, chained state.
type Event struct {
	ID       string
	Revision int64
	State    string
	Kind     Kind
	Hold     HoldEffect
	// Hash is this event's complete hash - what the NEXT revision binds.
	Hash   string
	Signed []byte
}

// EventID derives the deterministic identity of one event.
//
// From strict JCS [tag, network, attempt, revision], exactly as the spec sets out. Derived
// rather than minted so two instances computing it agree without coordinating, and so an id
// cannot be chosen: an attacker who could pick an event id could pick which chain position
// their event appears to occupy.
func EventID(network, attemptID string, revision int64) (string, error) {
	return towerobj.HashList([]string{
		eventIDTag, network, attemptID, towerobj.FormatInt(revision),
	})
}

// CommitmentID derives the deterministic identity of an attempt's commitment. One per
// attempt, so it carries no revision.
func CommitmentID(network, attemptID string) (string, error) {
	return towerobj.HashList([]string{commitmentIDTag, network, attemptID})
}

// buildCommitment produces the disclosure-safe object.
//
// EVERY MEMBER HERE IS ONE THE SPEC NAMES, and the absences are as deliberate as the
// presences: no hold id, no amount, no currency, no client or account identity, no price, no
// funding source. A Tower learning what a request is worth learns what its customers are
// worth, and a Station learning the account learns who to approach off-network.
func buildCommitment(s IssueSpec, id string) (map[string]any, error) {
	if s.Origin != OriginDirect && s.Origin != OriginJoined {
		return nil, fmt.Errorf("an attempt is direct or joined, not %q", s.Origin)
	}
	obj := map[string]any{
		"network":       s.Network,
		"type":          TypeCommitment,
		"version":       towerobj.FormatInt(Version),
		"commitment_id": id,
		"job_id":        s.JobID,
		"attempt_id":    s.AttemptID,
		"origin":        string(s.Origin),
		"grant_hash":    s.GrantHash,
		"deadline":      towerobj.FormatInt(s.Deadline.Unix()),
		"finalization":  towerobj.FormatInt(s.FinalizationCeiling.Unix()),
		"ledger_index":  towerobj.FormatInt(s.LedgerIndex),
		"issued":        towerobj.FormatInt(s.CommitTime.Unix()),
		"sequence":      towerobj.FormatInt(s.Sequence),
	}
	// A direct attempt has no lease, and says so by OMITTING the member. An empty string
	// would be a value, and a schema that accepts one accepts a lease hash of nothing.
	if s.Origin == OriginJoined {
		if s.LeaseHash == "" {
			return nil, errors.New("a joined attempt is dispatched under a lease, and none was given")
		}
		obj["lease_hash"] = s.LeaseHash
	} else if s.LeaseHash != "" {
		return nil, errors.New("a direct attempt has no lease, so it cannot name one")
	}
	return obj, nil
}

// buildEvent produces the private, signed state object.
//
// The closed member set is the spec's, and the four canonical ABSENCES are represented by
// omitting the member rather than by an empty value. That distinction is load-bearing: the
// signed bytes of "there is no prior event" and "the prior event's hash is the empty string"
// must not be the same, or a first event could be replayed as a successor.
func buildEvent(s IssueSpec, ev eventFields) (map[string]any, error) {
	obj := map[string]any{
		"network":          s.Network,
		"type":             TypeEvent,
		"version":          towerobj.FormatInt(Version),
		"event_id":         ev.id,
		"job_id":           s.JobID,
		"request_id":       s.RequestID,
		"attempt_id":       s.AttemptID,
		"revision":         towerobj.FormatInt(ev.revision),
		"kind":             string(ev.kind),
		"state":            ev.state,
		"commitment_id":    ev.commitmentID,
		"grant_hash":       s.GrantHash,
		"reservation":      s.ReservationHash,
		"reservation_set":  s.ReservationSet,
		"hold_id":          s.Hold.ID,
		"hold_currency":    s.Hold.Currency,
		"hold_unit":        s.Hold.Unit,
		"hold_scale":       towerobj.FormatInt(s.Hold.Scale),
		"hold_amount":      towerobj.FormatInt(s.Hold.Amount),
		"hold_state":       string(ev.hold),
		"tower_revision":   towerobj.FormatInt(s.TowerRevision),
		"station_revision": towerobj.FormatInt(s.StationRevision),
		"deadline":         towerobj.FormatInt(s.Deadline.Unix()),
		"finalization":     towerobj.FormatInt(s.FinalizationCeiling.Unix()),
		"committed":        towerobj.FormatInt(ev.commitTime.Unix()),
		"sequence":         towerobj.FormatInt(ev.sequence),
	}
	if s.Origin == OriginJoined {
		obj["lease_hash"] = s.LeaseHash
	}
	if s.CompensationSnapshot != "" {
		obj["compensation_snapshot"] = s.CompensationSnapshot
	}
	// CANONICAL ISSUED ABSENCE. Revision 1 has no prior, and says so by carrying no member.
	if ev.revision > 1 {
		if ev.prevHash == "" {
			return nil, errors.New("every event after the first binds the one before it")
		}
		obj["prev_hash"] = ev.prevHash
	} else if ev.prevHash != "" {
		return nil, errors.New("the first event of an attempt has no prior to bind")
	}
	// Evidence is absent at issue and required afterwards: an event that changed state on the
	// strength of nothing would be Core asserting rather than observing.
	if ev.revision > 1 {
		if ev.evidenceHash == "" {
			return nil, errors.New("an event after issue records the evidence Core observed")
		}
		obj["evidence_hash"] = ev.evidenceHash
	} else if ev.evidenceHash != "" {
		return nil, errors.New("issuing observes nothing, so it names no evidence")
	}
	// A terminal reason, and only on a terminal state.
	if Terminal(ev.state) {
		if ev.reason == "" {
			return nil, errors.New("a terminal attempt records why it ended")
		}
		obj["reason"] = ev.reason
	} else if ev.reason != "" {
		return nil, errors.New("a nonterminal event has no terminal reason")
	}
	// ONLY a released terminal may name a release transition, and settled NEVER may - settled
	// captures the cost and releases the remainder through settlement, not through a release.
	if ev.releaseID != "" {
		if ev.state == StateSettled || !Terminal(ev.state) {
			return nil, errors.New("only a failed, expired or cancelled attempt releases its hold")
		}
		obj["release_id"] = ev.releaseID
		obj["release_index"] = towerobj.FormatInt(ev.releaseIndex)
	}
	return obj, nil
}

// eventFields are the per-revision parts, kept apart from the immutable IssueSpec so a
// successor cannot silently restate the attempt's authority differently from its first event.
type eventFields struct {
	id           string
	revision     int64
	kind         Kind
	state        string
	hold         HoldEffect
	commitmentID string
	prevHash     string
	evidenceHash string
	reason       string
	releaseID    string
	releaseIndex int64
	commitTime   time.Time
	sequence     int64
}
