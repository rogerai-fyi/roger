package admit

// events.go is the Tower governance record: an append-only list of attributed events per
// Tower. Contract: features/tower/governance_record.feature.
//
// The admission row says what a Tower IS; this says how it got there and who moved it. It
// exists because the row is overwritten in place: the first outside Tower was approved and
// Core could not say whether it ever connected, who approved it, or why an automatic
// suspension happened once the evidence behind it was reaped.
//
// It lives beside the registry, in the same store, so a lifecycle change and its record can
// commit in ONE write (CASTowerEvent). A change Core cannot record is a change Core did not
// make. Telemetry events (sessions, probes, refusals) go through Record and are best effort.

import (
	"errors"
	"strings"
	"time"
	"unicode"
)

// Actors that are not a named person. The rest are "admin:gh:<login>", "admin:broker-key"
// and "operator:<owner>".
const (
	ActorSystem = "system" // Core decided on its own
	ActorTower  = "tower"  // the Tower's own signed request
)

// MaxReason bounds a free-text reason, in characters.
const MaxReason = 280

// Event is one entry in a Tower's governance record.
type Event struct {
	ID       int64
	TowerID  string
	At       time.Time
	Kind     string
	Actor    string
	From, To State // set when a state changed
	Reason   string
	Instance string // the broker instance that wrote it
	// Detail is a small, secret-free object: counts, ids, a tally. Never a key, a token, a
	// signature or a request body.
	Detail map[string]any
}

// ErrAlreadyInState and ErrIllegalTransition classify a refused transition, so a caller can
// record WHY without parsing the message. The message itself is unchanged.
var (
	ErrAlreadyInState    = errors.New("the Tower is already in that state")
	ErrIllegalTransition = errors.New("the transition table does not allow that move")
)

type transitionError struct {
	msg  string
	kind error
}

func (e transitionError) Error() string { return e.msg }
func (e transitionError) Unwrap() error { return e.kind }

// CleanReason makes free text safe to store and to print on one log line: control
// characters become spaces, runs of whitespace collapse, and the result is trimmed and
// capped at MaxReason characters.
func CleanReason(s string) string {
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			if n+1 >= MaxReason {
				break
			}
			b.WriteByte(' ')
			n++
			space = false
		}
		if n >= MaxReason {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// valid reports whether an event may be written at all.
func (ev Event) valid() error {
	switch {
	case ev.TowerID == "":
		return errors.New("a governance event needs a Tower id")
	case ev.Kind == "":
		return errors.New("a governance event needs a kind")
	case ev.Actor == "":
		return errors.New("a governance event needs an actor")
	}
	return nil
}

func (ev Event) normalized() Event {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	ev.Reason = CleanReason(ev.Reason)
	return ev
}

// Record appends a telemetry event. Best effort for the caller: a failure is returned so it
// can be logged, and never blocks a Tower.
func (r *Registry) Record(ev Event) error {
	ev = ev.normalized()
	if err := ev.valid(); err != nil {
		return err
	}
	if err := r.store.AppendEvent(ev); err != nil {
		return unavailable(err)
	}
	return nil
}

// Events reads a Tower's newest events first.
func (r *Registry) Events(towerID string, limit int) ([]Event, error) {
	out, err := r.store.Events(towerID, limit)
	if err != nil {
		return nil, unavailable(err)
	}
	return out, nil
}

// TransitionAs moves a Tower's state and records the move in the same write. The prior state
// is taken from the record, never from the caller. A refusal records nothing: the caller
// knows who asked and records the refusal itself.
func (r *Registry) TransitionAs(id string, to State, ev Event) error {
	ev.TowerID = id
	ev = ev.normalized()
	if err := ev.valid(); err != nil {
		return err
	}
	tw, err := r.transitionTarget(id, to)
	if err != nil {
		return err
	}
	ev.From, ev.To = tw.State, to
	if to == StateQuarantine {
		tw.LeaseExpires = time.Now().Add(r.cfg.LeaseTTL)
	}
	tw.State = to
	won, err := r.store.CASTowerEvent(tw, ev)
	if err != nil {
		return unavailable(err)
	}
	if !won {
		return errors.New("this Tower changed state concurrently; re-read it and retry")
	}
	return nil
}

// transitionTarget reads the Tower and checks the move, classifying a refusal.
func (r *Registry) transitionTarget(id string, to State) (Tower, error) {
	tw, ok, err := r.store.TowerByID(id)
	if err != nil {
		return Tower{}, unavailable(err)
	}
	if !ok {
		return Tower{}, errors.New("no such Tower")
	}
	if !Valid(to) {
		return Tower{}, errInvalidState(to)
	}
	if !CanTransition(tw.State, to) {
		kind := ErrIllegalTransition
		if tw.State == to {
			kind = ErrAlreadyInState
		}
		return Tower{}, transitionError{msg: "a " + string(tw.State) + " Tower cannot become " + string(to), kind: kind}
	}
	return tw, nil
}

// FindEvent returns a Tower's newest (or, with oldest, first) event of any of the kinds.
func (r *Registry) FindEvent(towerID string, kinds []string, oldest bool) (Event, bool, error) {
	ev, ok, err := r.store.FindEvent(towerID, kinds, oldest)
	if err != nil {
		return Event{}, false, unavailable(err)
	}
	return ev, ok, nil
}

// RecordIfLast appends ev only if the newest event of those kinds is still lastID (0 for
// none). It is how one "went quiet" or "lease lapsed" event gets written across instances.
func (r *Registry) RecordIfLast(ev Event, kinds []string, lastID int64) (bool, error) {
	ev = ev.normalized()
	if err := ev.valid(); err != nil {
		return false, err
	}
	won, err := r.store.AppendEventIfLast(ev, kinds, lastID)
	if err != nil {
		return false, unavailable(err)
	}
	return won, nil
}
