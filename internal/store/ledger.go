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

// PayoutPolicy holds the founder-approved, env-configurable payout knobs.
type PayoutPolicy struct {
	HoldDays  int     // days an earning is held before its non-reserve part is payable
	Reserve   float64 // fraction (0..1) of each earning kept back as a rolling reserve
	MinPayout float64 // minimum payable credits before a payout can be requested
	Schedule  string  // "monthly" | "weekly" - informational (batched, manual request)
}

// LoadPayoutPolicy reads the policy from env with founder-approved defaults:
// 90-day hold, 10% reserve, $25 minimum, monthly batched manual requests.
func LoadPayoutPolicy() PayoutPolicy {
	p := PayoutPolicy{HoldDays: 90, Reserve: 0.10, MinPayout: 25, Schedule: "monthly"}
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
// policy both the hold and the reserve release at HoldDays (the reserve is the 10%
// slice of the same earning, released at +90d).
func (p PayoutPolicy) holdDuration() time.Duration {
	return time.Duration(p.HoldDays) * 24 * time.Hour
}
