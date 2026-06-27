package store

import (
	"sort"
	"strings"
	"time"
)

// admin.go is the SUPER-ADMIN (founder) aggregate surface: platform-wide rollups the
// per-account endpoints deliberately never expose. Every method here reads ACROSS all
// accounts/wallets/lots/ledger, so the HTTP handlers that call them are admin-gated
// (see cmd/rogerai-broker/admin.go). The numbers are derived from the SAME source of
// truth the per-account views use (the ledger, earning_lots, receipts, payouts, the
// safety tables), so an admin overview never drifts from what an operator sees.
//
// Kept to a small, focused method set so Mem + Postgres parity stays maintainable:
//
//   AdminFinancials  - the money rollup (revenue/spend/earned + the lot lifecycle totals)
//   AdminMarketTotals - all-time + windowed request/token totals served (receipt-derived)
//   AdminPayoutQueue - every operator's payout history + pending/payable, newest first
//   AdminAbuse       - banned owners, owner-strike counts, CSAM queue, dispute counts
//   AdminActivity    - the recent cross-account ledger event stream (lineage)

// AdminFinancials is the platform money rollup as of a clock. All amounts are credits
// ($ at credit_usd=1). The lot lifecycle (held/payable/paid/clawed) is summed across
// EVERY account; the revenue is the platform fee (consumer spend minus operator earned).
type AdminFinancials struct {
	ConsumerSpend  float64 `json:"consumer_spend"`  // lifetime gross consumer spend (sum of settled cost)
	OperatorEarned float64 `json:"operator_earned"` // lifetime operator gross share (the 70%), all non-clawed lots
	PlatformFee    float64 `json:"platform_fee"`    // the platform's 30%: consumer_spend - operator_earned (>=0)
	TopupVolume    float64 `json:"topup_volume"`    // lifetime real money in (Stripe topups)
	Held           float64 `json:"held"`            // not-yet-releasable operator earnings (gross-minus-reserve)
	Reserved       float64 `json:"reserved"`        // reserve portion not yet released
	Payable        float64 `json:"payable"`         // releasable now, not yet paid
	Paid           float64 `json:"paid"`            // lifetime transferred out (paid lots, gross)
	Clawed         float64 `json:"clawed"`          // lifetime clawed back by disputes (gross)
	PlatformLoss   float64 `json:"platform_loss"`   // disputed amount no operator lot covered (platform ate it)
	WalletCount    int     `json:"wallet_count"`    // distinct consumer wallets that exist
	WalletBalance  float64 `json:"wallet_balance"`  // total outstanding consumer credit liability
	OwnerCount     int     `json:"owner_count"`     // distinct non-anonymized operator accounts
	NodeBindings   int     `json:"node_bindings"`   // distinct node->account bindings
}

// AdminMarketTotals is the receipt-derived request/token volume the platform has served,
// both all-time and within a trailing [since,until) window. Receipt-derived so it agrees
// with the per-account metrics rollups.
type AdminMarketTotals struct {
	Requests        int64 `json:"requests"`          // all-time settled requests
	TokensIn        int64 `json:"tokens_in"`         // all-time prompt tokens (billed counts)
	TokensOut       int64 `json:"tokens_out"`        // all-time completion tokens (billed counts)
	WindowRequests  int64 `json:"window_requests"`   // settled requests in [since,until)
	WindowTokensIn  int64 `json:"window_tokens_in"`  // prompt tokens in window
	WindowTokensOut int64 `json:"window_tokens_out"` // completion tokens in window
}

// AdminPayoutQueueRow is one operator's payout posture for the admin payouts view: the
// account, its currently-payable + held balances (as of the clock), and its lifetime
// paid. The HTTP layer joins the login/connect status from the owner record.
type AdminPayoutQueueRow struct {
	AccountID string  `json:"account_id"` // owner pubkey
	Payable   float64 `json:"payable"`    // releasable now, not yet paid out
	Held      float64 `json:"held"`       // still inside the hold window
	Paid      float64 `json:"paid"`       // lifetime transferred out
	Pending   float64 `json:"pending"`    // sum of this account's PENDING payouts (in flight)
}

// AdminAbuse is the platform safety rollup: banned operators (with reason + strike
// count), the count of distinct struck accounts, the CSAM report queue depth, the
// recent report count, and the dispute count.
type AdminAbuse struct {
	BannedOwners   []AdminBannedOwner `json:"banned_owners"`   // every durably-banned operator account
	StruckAccounts int                `json:"struck_accounts"` // distinct accounts with >=1 strike
	TotalStrikes   int                `json:"total_strikes"`   // total strike rows across all accounts
	CSAMQueued     int                `json:"csam_queued"`     // CSAM incidents still owing a CyberTipline report
	CSAMTotal      int                `json:"csam_total"`      // total preserved CSAM incidents
	ReportCount    int                `json:"report_count"`    // total abuse/quality reports filed
	BannedNodes    int                `json:"banned_nodes"`    // ejected node count
	DisputeCount   int                `json:"dispute_count"`   // total disputes/chargebacks recorded
	AccountHolds   int                `json:"account_holds"`   // operator accounts currently under a recount hold
}

// AdminBannedOwner is one durably-banned operator account, with its ban reason and how
// many evidence strikes it accrued.
type AdminBannedOwner struct {
	AccountID string `json:"account_id"` // owner pubkey
	Reason    string `json:"reason"`
	Strikes   int    `json:"strikes"`
}

// AdminFinancials sums the platform money rollup across every account/wallet/lot. It
// promotes held->payable as of `now` (sweep-on-read) so the split agrees with what the
// per-account views report at the same clock.
func (m *Mem) AdminFinancials(now time.Time) (AdminFinancials, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.promoteLocked(now)
	var f AdminFinancials
	for _, v := range m.spend {
		f.ConsumerSpend += v
	}
	for _, bal := range m.wallet {
		f.WalletBalance += bal
	}
	f.WalletCount = len(m.wallet)
	for _, o := range m.owners {
		if !o.Anonymized {
			f.OwnerCount++
		}
	}
	f.NodeBindings = len(m.nodeAcct)
	for _, l := range m.lots {
		switch l.State {
		case LotHeld:
			f.Held += l.Gross - l.Reserve
			f.Reserved += l.Reserve
			f.OperatorEarned += l.Gross
		case LotPayable:
			f.Payable += l.Gross - l.Reserve
			if now.Unix() >= l.ReserveReleaseAt {
				f.Payable += l.Reserve
			} else {
				f.Reserved += l.Reserve
			}
			f.OperatorEarned += l.Gross
		case LotPaid:
			f.Paid += l.Gross
			f.OperatorEarned += l.Gross
		case LotClawed:
			f.Clawed += l.Gross
		}
	}
	for _, r := range m.ledger {
		if r.State == StateReversed {
			continue
		}
		switch r.Kind {
		case KindTopup:
			f.TopupVolume += r.Amount
		case KindPlatformLoss:
			f.PlatformLoss += -r.Amount // stored negative
		}
	}
	// Platform fee = the 30% the platform keeps = consumer spend that became earnings
	// minus the operator gross. Clamp at 0 (free/seed traffic earns no operator share but
	// also costs the consumer nothing, so the fee can't go negative).
	f.PlatformFee = f.ConsumerSpend - f.OperatorEarned
	if f.PlatformFee < 0 {
		f.PlatformFee = 0
	}
	return roundFinancials(f), nil
}

func roundFinancials(f AdminFinancials) AdminFinancials {
	f.ConsumerSpend = round6(f.ConsumerSpend)
	f.OperatorEarned = round6(f.OperatorEarned)
	f.PlatformFee = round6(f.PlatformFee)
	f.TopupVolume = round6(f.TopupVolume)
	f.Held = round6(f.Held)
	f.Reserved = round6(f.Reserved)
	f.Payable = round6(f.Payable)
	f.Paid = round6(f.Paid)
	f.Clawed = round6(f.Clawed)
	f.PlatformLoss = round6(f.PlatformLoss)
	f.WalletBalance = round6(f.WalletBalance)
	return f
}

// AdminMarketTotals sums settled-request/token volume across every receipt, all-time
// and within the [since,until) window.
func (m *Mem) AdminMarketTotals(since, until int64) (AdminMarketTotals, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var t AdminMarketTotals
	for _, e := range m.entries {
		t.Requests++
		t.TokensIn += int64(e.PromptTokens)
		t.TokensOut += int64(e.CompletionTokens)
		if e.TS >= since && e.TS < until {
			t.WindowRequests++
			t.WindowTokensIn += int64(e.PromptTokens)
			t.WindowTokensOut += int64(e.CompletionTokens)
		}
	}
	return t, nil
}

// AdminPayoutQueue returns every operator account's payable/held/paid/pending posture as
// of `now`, sorted by payable desc then held desc (the accounts most owed first). It
// promotes held->payable on read so the numbers match the per-account split.
func (m *Mem) AdminPayoutQueue(now time.Time, limit int) ([]AdminPayoutQueueRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.promoteLocked(now)
	rows := map[string]*AdminPayoutQueueRow{}
	row := func(acct string) *AdminPayoutQueueRow {
		r := rows[acct]
		if r == nil {
			r = &AdminPayoutQueueRow{AccountID: acct}
			rows[acct] = r
		}
		return r
	}
	for _, l := range m.lots {
		switch l.State {
		case LotHeld:
			row(l.AccountID).Held += l.Gross - l.Reserve
		case LotPayable:
			r := row(l.AccountID)
			r.Payable += l.Gross - l.Reserve
			if now.Unix() >= l.ReserveReleaseAt {
				r.Payable += l.Reserve
			}
		case LotPaid:
			row(l.AccountID).Paid += l.Gross
		}
	}
	for _, p := range m.payouts {
		if p.State == PayoutPending {
			row(p.AccountID).Pending += p.Amount
		}
	}
	out := make([]AdminPayoutQueueRow, 0, len(rows))
	for _, r := range rows {
		r.Payable = round6(r.Payable)
		r.Held = round6(r.Held)
		r.Paid = round6(r.Paid)
		r.Pending = round6(r.Pending)
		out = append(out, *r)
	}
	sortPayoutQueue(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func sortPayoutQueue(rows []AdminPayoutQueueRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Payable != rows[j].Payable {
			return rows[i].Payable > rows[j].Payable
		}
		if rows[i].Held != rows[j].Held {
			return rows[i].Held > rows[j].Held
		}
		return rows[i].AccountID < rows[j].AccountID
	})
}

// AdminAllPayouts returns the most-recent payouts ACROSS all accounts, newest first
// (the platform payout history). Capped by limit (0 = all).
func (m *Mem) AdminAllPayouts(limit int) ([]Payout, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Payout, 0, len(m.payouts))
	for i := len(m.payouts) - 1; i >= 0; i-- {
		out = append(out, m.payouts[i])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// AdminAbuse rolls up the platform safety state across every account.
func (m *Mem) AdminAbuse() (AdminAbuse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var a AdminAbuse
	strikesByAcct := map[string]int{}
	for _, s := range m.strikes {
		if strings.HasPrefix(s.Kind, "ban:") {
			continue // terminal ban marker, not an evidence strike (matches Postgres + OwnerStrikeStats)
		}
		strikesByAcct[s.AccountID]++
		a.TotalStrikes++
	}
	a.StruckAccounts = len(strikesByAcct)
	a.BannedOwners = make([]AdminBannedOwner, 0, len(m.bannedOwners))
	for acct, reason := range m.bannedOwners {
		a.BannedOwners = append(a.BannedOwners, AdminBannedOwner{
			AccountID: acct, Reason: reason, Strikes: strikesByAcct[acct],
		})
	}
	sort.SliceStable(a.BannedOwners, func(i, j int) bool {
		return a.BannedOwners[i].AccountID < a.BannedOwners[j].AccountID
	})
	for _, c := range m.csam {
		a.CSAMTotal++
		if c.ReportState == CSAMQueued {
			a.CSAMQueued++
		}
	}
	a.ReportCount = len(m.reports)
	a.BannedNodes = len(m.banned)
	a.DisputeCount = len(m.disputes)
	a.AccountHolds = len(m.accountHold)
	return a, nil
}

// AdminActivity returns the most-recent ledger rows ACROSS every holder, newest first
// (the platform-wide event stream / lineage). Capped by limit. This is the only place
// the ledger is read un-scoped to a holder; it is admin-gated.
func (m *Mem) AdminActivity(limit int) ([]LedgerRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]LedgerRow, 0, limit)
	for i := len(m.ledger) - 1; i >= 0; i-- {
		out = append(out, m.ledger[i])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}
