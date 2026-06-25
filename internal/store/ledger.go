package store

import (
	"os"
	"strconv"
	"time"
)

// This file defines the append-only ledger + the operator earnings lifecycle that
// sit on top of the existing wallet/earnings counters. The counters become caches;
// the ledger is the source of truth (every money event is one append-only row with
// a UNIQUE idem_key). See docs-internal/ACCOUNT-PAYOUTS-DESIGN.md.

// Ledger kinds. A correction is always a NEW compensating row, never an edit.
const (
	KindTopup          = "topup"           // consumer: money in (Stripe checkout)
	KindSpend          = "spend"           // consumer: credits spent on a request
	KindHold           = "hold"            // consumer: pending reservation (-amount)
	KindHoldRelease    = "hold_release"    // consumer: reservation returned (+amount)
	KindEarn           = "earn"            // operator: owner share credited (held)
	KindPayout         = "payout"          // operator: transfer out (-amount)
	KindRefund         = "refund"          // consumer: refunded (+amount)
	KindChargeback     = "chargeback"      // consumer: disputed charge clawed (-amount)
	KindReserveHold    = "reserve_hold"    // operator: rolling reserve kept back
	KindReserveRelease = "reserve_release" // operator: reserve released after the tail
	KindAdjustment     = "adjustment"      // manual/clawback correction (signed)
	KindPayoutReversed = "payout_reversed" // operator: an ALREADY-PAID lot clawed via a Stripe transfer reversal (-amount)
	KindPlatformLoss   = "platform_loss"   // platform: disputed amount NOT recoverable from operator lots (platform eats it)
	KindAdjust         = "adjust"          // audit: broker billed LESS than the node claimed (claim-vs-billed delta, $0 money, platform-favoring)
	KindVoid           = "void"            // audit: request produced no usable output - charged $0, minted no earning, hold refunded
)

// Ledger row states. Rows are append-only; the only mutation is a single state
// transition (pending -> posted/reversed).
const (
	StatePosted   = "posted"
	StatePending  = "pending"
	StateReversed = "reversed"
)

// LedgerRow is one append-only money event.
type LedgerRow struct {
	ID      int64   `json:"id"`
	Holder  string  `json:"holder"` // wallet id (consumer) or account id (operator)
	Side    string  `json:"side"`   // "consumer" | "operator"
	Kind    string  `json:"kind"`
	Amount  float64 `json:"amount"` // signed: +credit to holder, -debit
	IdemKey string  `json:"idem_key,omitempty"`
	State   string  `json:"state"`
	Ref     string  `json:"ref,omitempty"` // request id / stripe id
	TS      int64   `json:"ts"`            // unix seconds
}

// Earning lifecycle states (rogerai.earning_lots).
const (
	LotHeld    = "held"    // accruing, inside the hold window
	LotPayable = "payable" // hold cleared, transferable (KYC permitting)
	LotPaid    = "paid"    // transferred out via a payout
	LotClawed  = "clawed"  // reversed by a dispute/clawback
)

// EarningLot is one request's owner-share, tracked through held -> payable -> paid.
// The reserve sub-amount is released separately at reserve_release_at.
type EarningLot struct {
	ID               int64   `json:"id"`
	Node             string  `json:"node"`
	AccountID        string  `json:"account_id"` // owner pubkey (the operator account)
	RequestID        string  `json:"request_id"`
	Gross            float64 `json:"gross"`   // owner share for this request
	Reserve          float64 `json:"reserve"` // portion kept back past the hold
	State            string  `json:"state"`
	ReleaseAt        int64   `json:"release_at"`         // unix: gross-minus-reserve becomes payable
	ReserveReleaseAt int64   `json:"reserve_release_at"` // unix: reserve becomes payable
	CreatedAt        int64   `json:"created_at"`
	PayoutID         int64   `json:"payout_id,omitempty"` // the payout that paid this lot (0 = none); rollback key
}

// EarningSplit is the held/reserved/payable/paid breakdown an operator sees, derived
// from the lots as of a given clock.
type EarningSplit struct {
	Held        float64 `json:"held"`         // not yet releasable (gross-minus-reserve still inside hold)
	Reserved    float64 `json:"reserved"`     // reserve portion not yet released
	Payable     float64 `json:"payable"`      // releasable now, not yet paid
	Paid        float64 `json:"paid"`         // lifetime transferred out
	NextRelease int64   `json:"next_release"` // unix of the soonest upcoming release (0 = none)
}

// ReleaseBucket is one upcoming earning release: the credits (gross-minus-reserve of
// the still-held lots) clearing on a given calendar day, plus how many lots make up
// that bucket. The Payouts page renders these as a dated release ladder ("$X clears
// Jun 30") instead of only the single soonest date the split's NextRelease carries.
type ReleaseBucket struct {
	Date     int64   `json:"date"`      // unix: midnight UTC of the release day (bucket key)
	Amount   float64 `json:"amount"`    // credits releasing that day (gross-minus-reserve)
	LotCount int     `json:"lot_count"` // number of held lots in this bucket
}

// EarningRollup is a per-model or per-node earnings total across an account's lots
// (held + payable + paid, the full attributed share). It powers the cheap provenance
// rollups on the earnings view (where the money came from, by model / by node).
type EarningRollup struct {
	Key    string  `json:"key"`    // the model id (per-model rollup) or node id (per-node rollup)
	Amount float64 `json:"amount"` // total attributed gross across the account's lots
	Lots   int     `json:"lots"`   // number of lots contributing
}

// PayoutLot is one funding earning lot behind a payout: the request-level receipt that
// the payout's money was drawn from. It is the lineage a payout-history row expands
// into - exactly which requests (model, node, gross, when) funded the transfer.
type PayoutLot struct {
	LotID     int64   `json:"lot_id"`
	RequestID string  `json:"request_id"`
	Node      string  `json:"node"`
	Model     string  `json:"model"` // resolved from the lot's request receipt ("" if unknown)
	Gross     float64 `json:"gross"` // owner share for this request (credits)
	CreatedAt int64   `json:"created_at"`
}

// Payout is one requested transfer (one Stripe Transfer per operator per run).
type Payout struct {
	ID               int64   `json:"id"`
	AccountID        string  `json:"account_id"`
	Amount           float64 `json:"amount"`
	StripeTransferID string  `json:"stripe_transfer_id,omitempty"`
	State            string  `json:"state"` // pending|paid|reversed|failed
	CreatedAt        int64   `json:"created_at"`
}

// Payout states.
const (
	PayoutPending  = "pending"
	PayoutPaid     = "paid"
	PayoutReversed = "reversed"
	PayoutFailed   = "failed"
)

// Reversal is one ALREADY-PAID earning lot that a dispute clawed back: the operator's
// share already left to their connected account via a Stripe Transfer, so it must be
// pulled back with a Stripe Transfer Reversal (ACCOUNT-PAYOUTS-DESIGN 6.4 step 4). The
// store records the ledger clawback + marks the lot clawed atomically and returns these
// so the broker can issue the reversal against the named transfer (idempotent on the
// dispute+lot). AccountID is the owner pubkey; TransferID is the Stripe transfer the
// lot was paid out on; Amount is the operator share to reverse (credits).
type Reversal struct {
	DisputeID  string  `json:"dispute_id"`
	LotID      int64   `json:"lot_id"`
	AccountID  string  `json:"account_id"`  // owner pubkey
	TransferID string  `json:"transfer_id"` // the Stripe transfer to reverse
	Amount     float64 `json:"amount"`      // operator share to reverse (credits)
}

// PendingReversal is a DURABLE record of a Stripe Transfer Reversal the broker still
// owes on a disputed, already-paid lot. The ledger clawback is recorded synchronously
// in the store, but the money rail (the Stripe API call that pulls the operator share
// back) can transiently fail; without a durable intent that failure silently leaks
// money (the clawback stands but the cash is never recovered). One row per (dispute,
// lot) keyed on Key (= "reverse:<disputeID>:<lotID>"), so it is idempotent with the
// Stripe Idempotency-Key the reversal uses: a webhook redelivery or a retry never
// double-records or double-reverses. A background sweep re-attempts each open row until
// it succeeds (Done) or hits MaxAttempts and is parked as a dead-letter for manual
// handling (logged loudly). Amount is the operator share to reverse (credits).
type PendingReversal struct {
	Key         string  `json:"key"`          // "reverse:<disputeID>:<lotID>" (idempotency key)
	DisputeID   string  `json:"dispute_id"`   // the Stripe dispute that triggered the clawback
	LotID       int64   `json:"lot_id"`       // the already-paid earning lot
	AccountID   string  `json:"account_id"`   // owner pubkey (for the reversal email + audit)
	TransferID  string  `json:"transfer_id"`  // the Stripe transfer to reverse
	Amount      float64 `json:"amount"`       // operator share to reverse (credits)
	Attempts    int     `json:"attempts"`     // reversal attempts so far
	Done        bool    `json:"done"`         // the Stripe reversal succeeded (terminal)
	DeadLetter  bool    `json:"dead_letter"`  // exhausted MaxAttempts; parked for manual handling
	LastError   string  `json:"last_error"`   // last failure message (for the dead-letter log)
	CreatedAt   int64   `json:"created_at"`   // unix: when the intent was first recorded
	LastAttempt int64   `json:"last_attempt"` // unix: when the reversal was last attempted
}

// ChargebackResult is the outcome of a lineage-attributed dispute clawback: how much
// was clawed from still-held/payable lots, the set of ALREADY-PAID lots that need a
// Stripe Transfer Reversal, and the platform-loss remainder (disputed amount that no
// operator lot covered - the platform eats it rather than clawing unrelated operators).
type ChargebackResult struct {
	Clawed         float64    `json:"clawed"`          // from held/payable lots (no Stripe action)
	Reversals      []Reversal `json:"reversals"`       // already-paid lots needing a transfer reversal
	PlatformLoss   float64    `json:"platform_loss"`   // unrecovered remainder (platform-liable)
	AlreadyHandled bool       `json:"already_handled"` // true if this dispute id was already processed (idempotent no-op)
}

// PayoutPolicy holds the founder-approved, env-configurable payout knobs.
type PayoutPolicy struct {
	HoldDays  int     // days an earning is held before its non-reserve part is payable
	Reserve   float64 // fraction (0..1) of each earning kept back as a rolling reserve
	MinPayout float64 // minimum payable credits before a payout can be requested
	Schedule  string  // "monthly" | "weekly" - informational (batched, manual request)
}

// LoadPayoutPolicy reads the policy from env with founder-approved defaults
// (payout policy OPTION A): a 120-day hold, NO separate rolling reserve (0), a $25
// minimum, monthly batched manual requests.
//
// HOLD = 120 days (P0-3b): the hold is the FIRST line of defense against the
// chargeback/dispute tail - while an earning is still held/payable, a dispute claws it
// from un-paid earnings (the cheap, common case) instead of needing a Stripe transfer
// reversal against the operator's connected account after the money already left. Card
// disputes can land up to ~120 days after the charge, so a 90-day hold left a ~30-day
// window where a paid-out lot could still be disputed (the "post-payout dispute loss").
// Raising the default to 120 days makes step-3 (claw from held) the common case and
// step-4 (transfer reversal) rare, per ACCOUNT-PAYOUTS-DESIGN 6.4. We chose the longer
// hold over re-enabling a rolling reserve because it is the simpler correct lever (one
// knob, no per-lot reserve accounting) and Option A already chose a hold-not-reserve
// posture; set ROGERAI_PAYOUT_RESERVE to re-enable a reserve slice if a shorter hold is
// ever wanted. Override the hold via ROGERAI_PAYOUT_HOLD_DAYS.
func LoadPayoutPolicy() PayoutPolicy {
	p := PayoutPolicy{HoldDays: 120, Reserve: 0, MinPayout: 25, Schedule: "monthly"}
	if v := os.Getenv("ROGERAI_PAYOUT_HOLD_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			p.HoldDays = n
		}
	}
	if v := os.Getenv("ROGERAI_PAYOUT_RESERVE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f < 1 {
			p.Reserve = f
		}
	}
	if v := os.Getenv("ROGERAI_PAYOUT_MIN"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			p.MinPayout = f
		}
	}
	if v := os.Getenv("ROGERAI_PAYOUT_SCHEDULE"); v != "" {
		p.Schedule = v
	}
	return p
}

// holdDuration / reserveDuration convert the policy to durations. Per the founder
// policy (Option A) the default reserve is 0, so the whole earning releases at
// HoldDays; if a reserve fraction is configured, that slice releases at the same
// point (+90d).
func (p PayoutPolicy) holdDuration() time.Duration {
	return time.Duration(p.HoldDays) * 24 * time.Hour
}
