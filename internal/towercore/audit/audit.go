// Package audit is the set of settled edge attempts Roger Core has selected to check the
// content of, and has not yet seen a transcript for.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # WHY A STORE RATHER THAN A PULL
//
// Core cannot reach a Station, so it cannot pull a transcript on demand: the request has to
// travel the road the Tower already holds. So audit is a WANTED LIST. At settlement Core marks
// a sampled fraction of attempts wanted; the Tower's courier asks what is wanted for it,
// fetches those transcripts from its Stations, and forwards them; Core checks each and resolves
// it off the list. What is left unresolved past its deadline is a Station that could not - or
// would not - show its work, which is a finding in itself.
//
// # WHAT IT REMEMBERS, AND WHY EACH FIELD
//
// The expected digests, because a transcript is checked against what the receipt committed to
// and the receipt is long gone by audit time. The Station id, because the courier has to know
// which Station to ask. The deadline, because "cannot produce" needs a moment to become true -
// a transcript in flight is not yet a failure.
//
// # DURABLE AND SHARED
//
// For the same reason as every other edge store: an attempt is marked wanted on whichever
// instance settled it, and its transcript arrives at whichever instance the Tower reached.
package audit

import (
	"errors"
	"time"
)

// Wanted is one attempt Core wants a transcript for.
type Wanted struct {
	TowerID   string
	AttemptID string
	StationID string
	// RequestDigest and ResponseDigest are what the receipt committed to - what the transcript
	// must match to pass.
	RequestDigest  string
	ResponseDigest string
	// UsageIn and UsageOut are the byte counts the STATION claimed in its receipt. The audit
	// checks them against the true length of the transcript bytes: usage is byte-exact, so a
	// claim that does not equal the length of the bytes the Station also signed for is a usage
	// misreport - the one over-billing an honest-looking, unacknowledged attempt could hide.
	UsageIn  int64
	UsageOut int64
	// Deadline is when an unproduced transcript becomes a "cannot produce" finding.
	Deadline time.Time
}

// Store holds the wanted list.
type Store interface {
	// Want marks an attempt wanted. Idempotent on attempt id: selecting the same attempt twice
	// (a retry, two instances racing) wants it once.
	Want(w Wanted) error
	// Pending returns what is still wanted for a Tower - the courier's work list.
	Pending(towerID string, now time.Time) ([]Wanted, error)
	// Resolve removes an attempt from the list once its transcript has been checked, pass or
	// fail. Removing on fail too is deliberate: the finding is recorded elsewhere, and leaving
	// a failed one on the list would re-audit it forever.
	Resolve(attemptID string) error
	// Overdue returns attempts past their deadline that were never produced - the "cannot
	// produce" cases - and removes them, so each is reported once.
	Overdue(now time.Time) ([]Wanted, error)
}

func check(w Wanted) error {
	switch {
	case w.TowerID == "":
		return errors.New("a wanted audit names its Tower")
	case w.AttemptID == "":
		return errors.New("a wanted audit names its attempt")
	case w.StationID == "":
		return errors.New("a wanted audit names the Station to ask")
	case w.ResponseDigest == "":
		return errors.New("a wanted audit carries the response digest to check against")
	case w.Deadline.IsZero():
		return errors.New("a wanted audit has a deadline")
	}
	return nil
}
