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
	// ReserveReleased marks that the reserve_release audit row for this lot was
	// emitted (once, when the tail cleared) - bookkeeping for the ledger, not money.
	ReserveReleased bool  `json:"reserve_released,omitempty"`
	CreatedAt       int64 `json:"created_at"`
	PayoutID        int64 `json:"payout_id,omitempty"` // the payout that paid this lot (0 = none); rollback key
	// SelfRelayed records that the two earnings this request minted - the serving Station's
	// 90% and the relaying Tower's 5% - were determined at settle time to belong to ONE
	// account. It is EVIDENCE, not enforcement: nothing here withholds, scales or refuses the
	// lot, and no read path treats a flagged lot differently from any other.
	//
	// WHY IT IS A STORED FACT RATHER THAN A QUERY. For the literal case the pair is already
	// recoverable - both lots carry the same request_id, and both account_ids are canonical
	// account keys, so a self-join finds them. What a self-join cannot recover is the LINKAGE
	// determination: two device keys under one GitHub id, one Apple subject, or one verified
	// email are one account to the self-dealing checks and two different strings here. This
	// field is that verdict, taken once, by the code that already had to take it, at the only
	// moment the inputs were all in hand.
	SelfRelayed bool `json:"self_relayed,omitempty"`
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
	HoldDays int     // days an earning is held before its non-reserve part is payable
	Reserve  float64 // fraction (0..1) of each earning kept back as a rolling reserve
	// ReserveDays is the reserve TAIL: days from earning until the reserve slice
	// becomes payable. Clamped to at least HoldDays (a reserve releasing before the
	// lot it belongs to would be meaningless).
	ReserveDays int
	MinPayout   float64 // minimum payable credits before a payout can be requested
	Schedule    string  // "monthly" | "weekly" - informational (batched, manual request)
}

// LoadPayoutPolicy reads the policy from env with founder-approved defaults
// (payout policy OPTION B, ruled 2026-09-01, superseding Option A's 120-day hold):
// a 30-DAY HOLD, a 10% ROLLING RESERVE ON A 90-DAY TAIL, a $25 minimum, monthly
// batched manual requests.
//
// WHY (the researched basis lives in the curated-review doc): most card disputes land
// inside 30-60 days, while the network dispute window runs 120 days from the TOP-UP
// and stretches to 540 on some reason codes - so Option A's blanket 120-day hold never
// covered the true tail anyway and made operators wait a quarter for money that was
// rarely at risk. Option B releases the PRINCIPAL (90% of each lot) at day 30 and
// keeps the 10% reserve slice back until day 90, covering the realistic dispute tail;
// past that, the clawback + transfer-reversal machinery remains the last line of
// defense exactly as before. Overrides: ROGERAI_PAYOUT_HOLD_DAYS,
// ROGERAI_PAYOUT_RESERVE (fraction), ROGERAI_PAYOUT_RESERVE_DAYS (the tail),
// ROGERAI_PAYOUT_MIN.
func LoadPayoutPolicy() PayoutPolicy {
	p := PayoutPolicy{HoldDays: 30, Reserve: 0.10, ReserveDays: 90, MinPayout: 25, Schedule: "monthly"}
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
	if v := os.Getenv("ROGERAI_PAYOUT_RESERVE_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			p.ReserveDays = n
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

// holdDuration converts the policy hold to a duration: when the PRINCIPAL of a lot
// promotes to payable.
func (p PayoutPolicy) holdDuration() time.Duration {
	return time.Duration(p.HoldDays) * 24 * time.Hour
}

// reserveDuration is the reserve TAIL: when the reserve slice of a lot becomes
// payable. Never earlier than the hold itself - a reserve is a slice kept back PAST
// the release, and a tail shorter than the hold would invert that into nonsense.
func (p PayoutPolicy) reserveDuration() time.Duration {
	d := time.Duration(p.ReserveDays) * 24 * time.Hour
	if h := p.holdDuration(); d < h {
		return h
	}
	return d
}
