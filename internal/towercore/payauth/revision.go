package payauth

// revision.go is the only thing in the system that can turn "a provider says so" into
// authority: Core's own authenticated read of a named source, range-checked, canonicalized,
// and committed as one monotonic revision.
//
// Contract: features/tower/payment_authority.feature ("Authenticated provider fetch creates
// one authoritative payment revision", "An authenticated fetch response still fails closed on
// context", "Push and pull disagreement has one authority").
//
// # FAIL CLOSED, ALWAYS IN THE SAME DIRECTION
//
// Every refusal here leaves the source PENDING rather than guessing a value. That asymmetry is
// deliberate: a source stuck pending pays nobody and can be retried, while a guessed value
// pays somebody and cannot be un-paid once it has cleared a rail. So an inconsistent response
// is never partially believed - not the fields that looked fine, not the amount that seemed
// plausible.

import (
	"errors"
	"fmt"
	"time"
)

// SourceKind is what a payment source is. A closed set: an unexpected kind is a refusal, not
// a shrug, because a kind we do not model is a kind whose amounts we cannot reason about.
type SourceKind string

const (
	KindPaymentIntent SourceKind = "payment_intent"
	KindCharge        SourceKind = "charge"
)

func knownKind(k SourceKind) bool { return k == KindPaymentIntent || k == KindCharge }

// FeeState is whether the provider's fee accounting for a source has stopped moving.
type FeeState string

const (
	FeePending FeeState = "pending"
	FeeFinal   FeeState = "final"
)

// DisputeState is the closed set of dispute outcomes that bear on payout.
type DisputeState string

const (
	DisputeNone DisputeState = "none"
	DisputeOpen DisputeState = "open"
	DisputeWon  DisputeState = "won"
	DisputeLost DisputeState = "lost"
)

// Money is an integer amount in a named currency at a named scale. There are no floats in
// this package and no bare integers crossing a boundary: an amount without its scale is a
// number waiting to be misread by a factor of a hundred.
type Money struct {
	Currency string
	Scale    int32 // decimal places in the currency's minor unit; 2 for USD, 0 for JPY
	Amount   int64 // in minor units
}

// Reading is one provider response, already parsed but NOT yet trusted.
type Reading struct {
	Adapter    string
	Merchant   string
	SourceID   string
	SourceKind SourceKind
	Currency   string
	Scale      int32
	// Cumulative figures, all in the currency's minor unit.
	OriginalPrincipal int64
	CapturedPrincipal int64
	RefundedTotal     int64
	FeeTotal          int64
	FeeState          FeeState
	Dispute           DisputeState
	// ProviderRevision is the provider's own monotonic marker for this source.
	ProviderRevision int64
	// EventIDs is the provider event lineage this reading accounts for. Required: a reading
	// that cannot say which events produced it cannot be reconciled against our ingress
	// records, and an unreconcilable authority is not one.
	EventIDs []string
	// ObservedAt is the provider's CLAIM about time. Never used to decide a deadline - see
	// FeeDeadline - because a provider that can move our clock can extend its own liability.
	ObservedAt time.Time
}

// Revision is a committed, authoritative statement about a payment source. Only Fetch
// produces one.
type Revision struct {
	Reading
	// Sequence is CORE's monotonic counter for this source, independent of the provider's.
	// Ours is what ordering and replay use: a provider that reissues or rewinds its own
	// revision numbers cannot reorder our history.
	Sequence int64
	// CommittedAt is Core's own time, from Core's own clock.
	CommittedAt time.Time
}

// Refusals from reconciliation. Each names the exact context defect the spec lists, because
// "the fetch failed" tells an operator nothing about whether to retry or to investigate.
var (
	ErrMerchantMismatch   = errors.New("payauth: merchant or platform account mismatch")
	ErrSourceMismatch     = errors.New("payauth: payment source ID mismatch")
	ErrUnexpectedKind     = errors.New("payauth: unexpected source kind")
	ErrCurrencyMismatch   = errors.New("payauth: currency or scale mismatch")
	ErrAmountRange        = errors.New("payauth: negative or overflowing amount")
	ErrRefundAboveCapture = errors.New("payauth: cumulative refund above cumulative captured principal")
	ErrCaptureAboveAuth   = errors.New("payauth: capture above original authorized principal")
	ErrFeeInconsistent    = errors.New("payauth: fee declared final with an inconsistent amount")
	ErrRevisionRewind     = errors.New("payauth: provider revision lower than the committed revision")
	ErrRevisionForked     = errors.New("payauth: equal revision with different canonical bytes")
	ErrNoLineage          = errors.New("payauth: missing required provider event lineage")
	ErrEndpointNotPinned  = errors.New("payauth: resolved address outside the pinned adapter endpoint policy")
)

// Expectation is what Core already knows about this source and requires the reading to agree
// with. It comes from Core's own records, never from the response being checked.
type Expectation struct {
	Merchant string
	SourceID string
	Currency string
	Scale    int32
	// Prior is the last committed revision for this source, if any.
	Prior *Revision
}

// Reconcile range-checks a reading against what Core already knows and returns the revision
// to commit, or refuses. It is pure: no I/O, no clock, no store - so every refusal in the
// spec's table is reachable in a test without a provider.
func Reconcile(exp Expectation, r Reading, seq int64, now time.Time) (Revision, error) {
	if r.Merchant == "" || r.Merchant != exp.Merchant {
		return Revision{}, ErrMerchantMismatch
	}
	if r.SourceID == "" || r.SourceID != exp.SourceID {
		return Revision{}, ErrSourceMismatch
	}
	if !knownKind(r.SourceKind) {
		return Revision{}, ErrUnexpectedKind
	}
	if r.Currency != exp.Currency || r.Scale != exp.Scale {
		return Revision{}, ErrCurrencyMismatch
	}
	for _, v := range []int64{r.OriginalPrincipal, r.CapturedPrincipal, r.RefundedTotal, r.FeeTotal} {
		if v < 0 {
			return Revision{}, ErrAmountRange
		}
	}
	if r.RefundedTotal > r.CapturedPrincipal {
		return Revision{}, ErrRefundAboveCapture
	}
	if r.CapturedPrincipal > r.OriginalPrincipal {
		return Revision{}, ErrCaptureAboveAuth
	}
	// A fee state outside the closed set is a reading we cannot act on: FeeState is a GATE on
	// compensation, so an unrecognised value must fail closed rather than default to one side.
	//
	// Deliberately NOT checked: fee greater than captured principal. It looks wrong and is
	// legitimate - operator_revenue_share specifies flat dispute fees "unrelated to principal",
	// which routinely exceed a small charge, and it accounts for the excess as platform
	// expense. Refusing those would strand real money in pending forever. Fee MAGNITUDE has no
	// bearing on compensation anyway under the gross basis: fees are the platform's cost, not
	// a reduction of the operator's base, so only finality matters here.
	if r.FeeState != FeePending && r.FeeState != FeeFinal {
		return Revision{}, ErrFeeInconsistent
	}
	switch r.Dispute {
	case DisputeNone, DisputeOpen, DisputeWon, DisputeLost:
	default:
		return Revision{}, ErrFeeInconsistent
	}
	if len(r.EventIDs) == 0 {
		return Revision{}, ErrNoLineage
	}
	if p := exp.Prior; p != nil {
		if r.ProviderRevision < p.ProviderRevision {
			return Revision{}, ErrRevisionRewind
		}
		// EQUAL revision, different content: the provider is telling us two different things
		// under one name. Believing either is guessing.
		if r.ProviderRevision == p.ProviderRevision && !sameReading(p.Reading, r) {
			return Revision{}, ErrRevisionForked
		}
	}
	return Revision{Reading: r, Sequence: seq, CommittedAt: now}, nil
}

// sameReading compares the fields a revision asserts. Deliberately field-by-field rather than
// by hashing a struct: a new field must force a decision here, not silently start or stop
// counting as "different".
func sameReading(a, b Reading) bool {
	return a.Adapter == b.Adapter && a.Merchant == b.Merchant && a.SourceID == b.SourceID &&
		a.SourceKind == b.SourceKind && a.Currency == b.Currency && a.Scale == b.Scale &&
		a.OriginalPrincipal == b.OriginalPrincipal && a.CapturedPrincipal == b.CapturedPrincipal &&
		a.RefundedTotal == b.RefundedTotal && a.FeeTotal == b.FeeTotal &&
		a.FeeState == b.FeeState && a.Dispute == b.Dispute &&
		a.ProviderRevision == b.ProviderRevision && sameIDs(a.EventIDs, b.EventIDs)
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// MatureCash is the externally funded money a source actually represents right now: captured
// principal less cumulative refunds. It is the ONLY figure compensation may be computed from,
// and it is a function of a committed revision rather than of anything a webhook said.
//
// Note what it does NOT subtract: processor fees. Under the founder's 2026-08-17 basis the
// operator's share is computed on gross externally funded revenue and met from the platform's
// margin, so fees are the platform's cost, not a reduction of the operator's base.
func (r Revision) MatureCash() Money {
	net := r.CapturedPrincipal - r.RefundedTotal
	if net < 0 {
		net = 0
	}
	return Money{Currency: r.Currency, Scale: r.Scale, Amount: net}
}

// PayoutHeld reports whether this source must not pay out yet, whatever its cash says. An
// open dispute is money that may be taken back.
func (r Revision) PayoutHeld() bool { return r.Dispute == DisputeOpen }

// CompensationEligible reports whether a positive compensation delta may be derived. Fees must
// be FINAL: a pending fee can still move, and a share accrued against a moving figure would
// have to be clawed back from an operator who did nothing wrong.
func (r Revision) CompensationEligible() bool {
	return r.FeeState == FeeFinal && r.Dispute != DisputeOpen && r.MatureCash().Amount > 0
}

// FeeDeadline is when a captured source's fee must be final by, derived from CORE's capture
// commit time plus the adapter's signed policy interval.
//
// The provider's own timestamps are deliberately not an input. A deadline derived from
// provider-claimed time is a deadline the provider can extend by claiming a later time, which
// is precisely the party the deadline exists to bound.
func FeeDeadline(coreCaptureCommit time.Time, interval time.Duration) (time.Time, error) {
	if interval <= 0 {
		return time.Time{}, fmt.Errorf("payauth: fee-finality interval must be positive, got %s", interval)
	}
	return coreCaptureCommit.Add(interval), nil
}

// FeeDeadlineReached reports whether the deadline has been reached, using Core's authority
// clock. Equality COUNTS as reached: the spec makes the boundary deterministic rather than
// leaving two sweeps a tick apart to disagree about the same instant.
func FeeDeadlineReached(deadline, coreNow time.Time) bool { return !coreNow.Before(deadline) }
