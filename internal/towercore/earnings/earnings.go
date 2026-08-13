// Package earnings is the funding ledger: what each Tower operator is owed for the traffic
// they carried, accrued one entry per settled attempt.
//
// Contract: features/tower/edge_dispatch.feature (the "what the operator is paid for" scenario).
//
// # SCOPE, STATED HONESTLY
//
// This is the ACCRUAL SUBSTRATE that the edge-settlement path drives: record, exactly once and
// durably, what a Station's operator is owed for one settled attempt, and let the operator read
// the total. It is NOT the full compensated-Tower revenue-share program. That program - approved
// and still NOT BUILT in the operator_revenue_share, compensation_state_machines and
// payment_authority specs - layers eligibility, funded-work verification against received
// consumer funds past a maturity window, payout authority, clawback, self-dealing prevention and
// forfeiture on top of a ledger like this one. None of that lives here. A rate set here is a raw
// accrual, not an entitlement those specs would recognise as payable.
//
// # WHY A SEPARATE, IDEMPOTENT LEDGER
//
// The audit warned against building compensation on the best-effort write that mirrors the
// dispatch queue into the attempt chain: a dropped event there means work served and unpayable,
// or the reverse. So earnings are NOT derived from that mirror. Each accrual is a durable row
// keyed by ATTEMPT ID, written after the attempt's one-use settlement has committed. Two
// properties fall out of that key:
//
//	EXACTLY ONCE. Settlement is one-use (a compare-and-swap), so an attempt settles once; the
//	accrual's primary key is the attempt id, so even a retried or raced write accrues once.
//	The money can never be paid twice for one attempt, by construction rather than by luck.
//
//	RECOVERABLE. The amount is a pure function of the settlement's billable usage, which is
//	itself the reconciled receipt/ack figure - never the Tower's own count. So a dropped
//	accrual under-pays (the safe direction for us) and can be re-derived from the receipt that
//	is stored with the attempt. A reconciliation pass can fill a gap; it can never invent one.
//
// # NOTHING HERE MOVES MONEY
//
// This records what is OWED. Disbursing it - the transfer to an operator's account - is a
// separate concern that plugs into the payment rails, behind its own authorization. Keeping
// the two apart means the ledger can be audited, disputed and reconciled without any of that
// being able to move a cent, and a bug here is a wrong number rather than a wrong payment.
package earnings

import (
	"errors"
	"math"
	"time"

	"rogerai.fm/roger/v5/internal/towercore/comp"
)

// Accrual is one attempt's earning for one Tower operator.
type Accrual struct {
	TowerID   string
	Owner     string
	AttemptID string
	// Model and the usage are kept so a rate change, or a dispute, can be re-priced from the
	// same inputs rather than from a number nobody can explain later.
	Model string
	// UsageIn / UsageOut are the BILLABLE usage - the reconciled receipt/ack figure, never the
	// Tower's own count. The amount below is computed from them at a rate the caller supplies,
	// so the ledger records both the inputs and the result.
	UsageIn  int64
	UsageOut int64
	// Micros is the amount owed, in millionths of the settlement currency's minor unit - an
	// integer so accrual is exact and never carries a rounding error forward.
	Micros int64
	// Corroborated marks whether a consumer acknowledgement backed this attempt. Uncorroborated
	// attempts still earn (an operator who lost money to every closed laptop would leave), but
	// the flag is carried so a payout policy can weight or hold them if it chooses.
	Corroborated bool
	// SelfDealing marks an attempt whose consumer account is the SAME account that owns the
	// Station - a wash trade, where an operator routes their own traffic through their own
	// Station to farm a revenue share on their own spend. The row is still RECORDED (the work
	// happened; the usage is evidence) but it earns nothing: OwedTo excludes it from what is
	// owed. This is the account-level first line against self-dealing; the funded-work and
	// linkage checks that catch sybil-account wash trading live in the revenue-share program.
	SelfDealing bool
	At          time.Time
}

// OwedByOwner is what one operator is owed and has been paid.
type OwedByOwner struct {
	Owner string
	// Accrued is the sum of every PAYABLE accrual in the window - self-dealing rows are excluded.
	// Paid is the sum of recorded payouts. Owed is Accrued - Paid, floored at zero: a payout
	// ledger that recorded more than was accrued is a bug to surface, not a debt to the operator.
	Accrued  int64
	Paid     int64
	Attempts int
	// SelfDealt is the amount that WOULD have accrued on self-dealing attempts, surfaced for
	// review rather than paid. A non-zero figure here is an account routing its own traffic
	// through its own Station.
	SelfDealt int64
}

// Owed is Accrued - Paid, never negative.
func (o OwedByOwner) Owed() int64 {
	if o.Accrued <= o.Paid {
		return 0
	}
	return o.Accrued - o.Paid
}

// Store is the funding ledger.
type Store interface {
	// Accrue records one attempt's earning. Idempotent on attempt id: an attempt earns once,
	// no matter how many times the write is attempted.
	Accrue(a Accrual) error
	// RecordPayout records that an amount was disbursed to an owner. Keyed by a caller-supplied
	// idempotency id so a retried disbursement is not double-counted against the debt.
	RecordPayout(owner, payoutID string, micros int64, at time.Time) error
	// OwedTo sums an owner's accruals and payouts.
	//
	// A ZERO `since` means all-time, and that is the ONLY value from which the net Owed() is
	// trustworthy. A non-zero `since` filters both accruals and payouts by timestamp for a
	// "recent activity" view - but a payout is stamped later than the accrual it discharges, so
	// a window can hold a payout while excluding the accrual it paid, netting it against newer
	// unrelated accruals and UNDER-REPORTING the true debt. Any code that decides a
	// disbursement must therefore call this with a zero `since`; the window is for display only.
	OwedTo(owner string, since time.Time) (OwedByOwner, error)
	// Reap drops accruals and payouts older than a cutoff. It is NOT a timer job: an accrual is
	// money owed until a payout discharges it, so pruning on age alone would discard undischarged
	// debt. It exists for a future reconciliation pass that deletes only what has been settled;
	// until disbursement exists, nothing calls it in production.
	Reap(before time.Time) (int64, error)
}

var (
	errPayout         = errors.New("a payout names an owner and a payout id")
	errNegativePayout = errors.New("a payout cannot be negative")
	errPayoutConflict = errors.New("a payout with this id already exists for a different owner or amount")
)

// satAddMicros sums two non-negative micro amounts, saturating at MaxInt64 rather than wrapping.
//
// A summed balance can only overflow if individual accruals were themselves priced up near
// MaxInt64 (a grossly misconfigured rate; the pricing already saturates there), but a wrap would
// turn the memory store's balance NEGATIVE and, once floored, hide the debt entirely - while the
// Postgres store's numeric SUM would error instead. Saturating here keeps the two stores in
// agreement (both cap at MaxInt64) and keeps an overflow visible as an absurd balance rather than
// a silent zero. The checked add lives in the canonical comp arithmetic; this is the read-side's
// deliberate saturate-and-continue wrapper, so a balance display never wedges on one bad rate.
func satAddMicros(a, b int64) int64 {
	sum, err := comp.CheckedAdd(a, b)
	if err != nil {
		return math.MaxInt64
	}
	return sum
}

func checkAccrual(a Accrual) error {
	switch {
	case a.TowerID == "":
		return errors.New("an accrual belongs to a Tower")
	case a.Owner == "":
		return errors.New("an accrual belongs to an owner")
	case a.AttemptID == "":
		return errors.New("an accrual belongs to an attempt")
	case a.Micros < 0:
		return errors.New("an accrual cannot be negative")
	case a.UsageIn < 0 || a.UsageOut < 0:
		return errors.New("an accrual cannot record negative usage")
	case a.At.IsZero():
		return errors.New("an accrual is recorded at a time")
	}
	return nil
}
