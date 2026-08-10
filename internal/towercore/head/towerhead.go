// Package towerhead remembers where each Tower's inventory chain got to, durably, so a
// reconnect can be answered by any Core instance rather than only the one that was holding
// the session.
//
// WHY THIS IS NOT JUST A CACHE. towerinv keeps the accepted head in process, which is
// correct while one Tower holds one connection to one instance. The moment that Tower
// reconnects to a DIFFERENT instance, the new instance knows nothing and must demand a full
// snapshot - correct, but expensive: the measured ceiling is ~5.4 MB per Tower, and a
// deploy reconnects the whole fleet at once. Worse, an instance with no history cannot tell
// an honest resume from a REPLAY or a FORK, because it has nothing to compare against.
//
// Only the revision and the hash are stored. Not the body: it is large, it changes often,
// and it is fully reconstructible by asking the Tower to resync. Storing it would turn every
// fleet change into a large write for data we can always re-request.
//
// THE THREE THINGS A STORED HEAD LETS US SEE, which an empty instance cannot:
//
//   - RESUME. Same revision, same hash. Nothing changed while we were away, and the Tower
//     sends ~100 bytes instead of a snapshot. This is the case that pays for the table.
//
//   - REPLAY. The Tower claims a revision at or below one we already accepted, with
//     different bytes, or claims to be behind where we know it was. Either it lost state, or
//     something is re-presenting an old chain. We do not guess which - we demand a full
//     snapshot, which re-validates everything from scratch.
//
//   - FORK. The Tower claims OUR revision number with a DIFFERENT hash. That is not drift:
//     it means the Tower signed two different objects as the same revision, which the hash
//     chain exists to make impossible to do quietly. It is recorded as evidence, because one
//     fork is a bug and a pattern of them is an operator worth removing.
//
// Every outcome except an exact match ends in a full snapshot. The distinctions matter for
// what we RECORD, not for what we accept: treating a fork as ordinary drift would throw away
// the only signal that a Tower is signing conflicting history.
//
// Spec: features/tower/inventory_and_routing.feature (delta ambiguity forces resync) and
// docs/tower-relay-link-design.md section 3 (tower_inventory_head: hash and revision only).
package head

import (
	"errors"
	"fmt"
	"time"
)

// ErrUnavailable is a store that could not answer. It is deliberately distinct from any
// decision: an instance that cannot read a head must ask for a full snapshot, not invent an
// answer, and the caller has to be able to tell those apart to log the difference.
var ErrUnavailable = errors.New("the inventory head store is temporarily unavailable")

// Head is the whole durable record: which revision, and the complete-object hash of it.
type Head struct {
	TowerID   string
	Revision  int64
	Hash      string
	UpdatedAt time.Time
}

// Outcome is what a reconnecting Tower must be told.
type Outcome int

const (
	// Resume: the Tower's head is exactly ours. It may continue with deltas.
	Resume Outcome = iota
	// NeedFull: we cannot place the Tower's claim, so it must resend everything. This is the
	// safe answer and the destination of every non-matching case.
	NeedFull
	// Replay: NeedFull, plus the observation that the Tower presented a chain position at or
	// behind one we already accepted.
	Replay
	// Fork: NeedFull, plus the observation that the Tower presented OUR revision number under
	// a DIFFERENT hash - it signed conflicting history.
	Fork
)

func (o Outcome) String() string {
	switch o {
	case Resume:
		return "resume"
	case NeedFull:
		return "need-full"
	case Replay:
		return "replay"
	case Fork:
		return "fork"
	}
	return "unknown"
}

// NeedsFullInventory reports whether this outcome requires a full snapshot. Everything
// except Resume does; the method exists so a caller cannot accidentally treat Fork as
// benign by forgetting a case.
func (o Outcome) NeedsFullInventory() bool { return o != Resume }

// Suspicious reports whether the outcome is evidence about the Tower rather than ordinary
// bookkeeping. A first connect is not suspicious; conflicting history is.
func (o Outcome) Suspicious() bool { return o == Replay || o == Fork }

// Store is the durable half.
type Store interface {
	// Record advances a Tower's head. It MUST refuse to move a head backwards - see
	// Reconciler.Accept for why that matters across instances.
	Record(h Head) (bool, error)
	// Head reads one back.
	Head(towerID string) (Head, bool, error)
	// Forget drops a Tower's chain entirely, on revocation or detach.
	Forget(towerID string) error
}

// Reconciler answers reconnects and records accepted revisions.
type Reconciler struct {
	store Store
	now   func() time.Time
}

// New builds a Reconciler. now may be nil.
func New(s Store, now func() time.Time) *Reconciler {
	if now == nil {
		now = time.Now
	}
	return &Reconciler{store: s, now: now}
}

// Reconcile decides what a Tower reconnecting with the claimed head must do.
//
// The claim is UNVERIFIED input from the Tower, so nothing here trusts it beyond comparing
// it with what we recorded. In particular a Tower claiming to be AHEAD of us is not evidence
// that it is ahead - it is evidence that it is claiming something we never accepted.
func (r *Reconciler) Reconcile(towerID string, claimedRevision int64, claimedHash string) (Outcome, error) {
	ours, ok, err := r.store.Head(towerID)
	if err != nil {
		// Unreadable: ask for everything. An instance that cannot check its own record must
		// never resume on the Tower's say-so.
		return NeedFull, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !ok {
		return NeedFull, nil // first time we have ever seen this Tower, or it was forgotten
	}
	// A Tower that claims nothing is starting clean; that is honest, not suspicious.
	if claimedRevision <= 0 || claimedHash == "" {
		return NeedFull, nil
	}

	switch {
	case claimedRevision == ours.Revision && claimedHash == ours.Hash:
		return Resume, nil
	case claimedRevision == ours.Revision:
		// Same number, different bytes. The hash chain exists precisely so this cannot happen
		// quietly, so it is recorded rather than smoothed over.
		return Fork, nil
	case claimedRevision < ours.Revision:
		return Replay, nil
	default:
		// The Tower claims to be ahead of anything we accepted. We have no record of that
		// revision, so there is nothing to resume from.
		return NeedFull, nil
	}
}

// Accept records a revision Core has just accepted. It refuses to move a head backwards.
//
// The refusal is what makes this safe across instances. Two instances can briefly both hold
// a session for one Tower - during a failover, or a network partition that resolves - and
// the slower one finishing an older revision must not rewind the chain. A rewound head would
// make the next reconnect look like a fork to everyone.
func (r *Reconciler) Accept(towerID string, revision int64, hash string) (bool, error) {
	if towerID == "" || revision <= 0 || hash == "" {
		return false, errors.New("a head needs a Tower, a positive revision and a hash")
	}
	advanced, err := r.store.Record(Head{
		TowerID: towerID, Revision: revision, Hash: hash, UpdatedAt: r.now(),
	})
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return advanced, nil
}

// Head exposes what we recorded, for operations and for tests.
func (r *Reconciler) Head(towerID string) (Head, bool, error) {
	h, ok, err := r.store.Head(towerID)
	if err != nil {
		return Head{}, false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return h, ok, nil
}

// Forget drops a Tower's chain. Used on revocation: a Tower that is gone must not leave a
// head that would let a later impostor "resume" it.
func (r *Reconciler) Forget(towerID string) error {
	if err := r.store.Forget(towerID); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}
