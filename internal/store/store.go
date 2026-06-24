// Package store is the broker's persistence boundary - deliberately tiny so the
// backend is swappable (in-memory now; Postgres for DO; anything later). Only the
// money/audit state persists; live node/tunnel state stays in the broker's memory
// (nodes re-register on reconnect).
package store

import (
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// Entry is one settled request, as surfaced to dashboards. It carries the real
// (un-pseudonymized) user + node, the billed cost, and the owner's share, so a
// consumer can see spend and an owner can see earnings from the same record.
type Entry struct {
	RequestID        string  `json:"request_id"`
	User             string  `json:"user"`
	Node             string  `json:"node"`
	Model            string  `json:"model"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	Cost             float64 `json:"cost"`        // credits the consumer paid
	OwnerShare       float64 `json:"owner_share"` // credits credited to the node owner
	TS               int64   `json:"ts"`
}

type Store interface {
	// BalanceOf returns the user's credit balance, seeding a new user with `seed`.
	BalanceOf(user string, seed float64) (float64, error)
	// Settle atomically debits the user by cost, credits the node's owner share,
	// and appends the lineage receipt. Returns the user's new balance.
	Settle(user, node string, cost, ownerShare float64, rec protocol.UsageReceipt) (newBalance float64, err error)
	// EarningsOf returns a node's accrued (unpaid) owner credits.
	EarningsOf(node string) (float64, error)
	// SpendOf returns a user's lifetime total spend (sum of settled costs).
	SpendOf(user string) (float64, error)
	// RecentByUser returns a user's most-recent settled requests (newest first).
	RecentByUser(user string, limit int) ([]Entry, error)
	// RecentByNode returns a node's most-recent settled requests (newest first).
	RecentByNode(node string, limit int) ([]Entry, error)
	// AddCredits tops a user up (Stripe webhook in P1).
	AddCredits(user string, amount float64) (float64, error)
	// MarkProcessed records an idempotency key (e.g. a Stripe session id) and
	// reports whether it was newly added (true) vs already seen (false) - makes
	// the Stripe webhook safe against at-least-once redelivery (no double-credit).
	MarkProcessed(key string) (firstTime bool, err error)
	// CreditOnce atomically records the idempotency key AND credits the user in a
	// single transaction: returns credited=true only the first time. Prevents both
	// double-credit (redelivery) and lost-credit (mark succeeds, credit fails).
	CreditOnce(key, user string, amount float64) (credited bool, newBalance float64, err error)
	// Hold atomically reserves `amount` from the user's balance (conditional debit);
	// ok=false if the balance can't cover it. This authorize-then-capture flow makes
	// concurrent spend safe - a wallet can never go negative. Settle the reservation
	// with Finalize, or return it untouched with ReleaseHold.
	Hold(user string, amount float64) (ok bool, err error)
	// Finalize captures a held reservation: charges `cost` (the caller caps it at the
	// held amount), refunds held-cost to the user, credits the owner share, and
	// records the receipt. Returns the new balance.
	Finalize(user, node string, held, cost, ownerShare float64, rec protocol.UsageReceipt) (newBalance float64, err error)
	// ReleaseHold returns a full reservation to the user (request failed, no charge).
	ReleaseHold(user string, held float64) (newBalance float64, err error)
	// BindOwner records (or refreshes) an owner binding: a verified GitHub identity
	// linked to the signing pubkey of the logged-in CLI. Earning operations require
	// this binding; it never affects the free/consume paths. Idempotent per pubkey.
	BindOwner(o Owner) error
	// OwnerByPubkey returns the owner bound to a signing pubkey, ok=false if none.
	OwnerByPubkey(pubkey string) (Owner, bool, error)
	// BindNode records the operator (owner pubkey) that owns a serving node, so a
	// node's earning lots can be attributed to an account at payout/Connect time.
	// Idempotent; TOFU (a node id belongs to the first account that binds it).
	BindNode(node, accountID string) error
	// AccountOfNode returns the owner pubkey bound to a node, ok=false if none.
	AccountOfNode(node string) (string, bool, error)
	// NodesOfAccount returns the node ids bound to an operator account (owner pubkey).
	NodesOfAccount(accountID string) ([]string, error)

	// --- account hub (ACCOUNT-PAYOUTS-DESIGN) -------------------------------

	// OwnerByLogin returns the owner with the given GitHub login, ok=false if none.
	// A login resolved to an anonymized (deleted) account reports ok=false.
	OwnerByLogin(login string) (Owner, bool, error)
	// UpdateAccount applies user-editable profile fields (email) to the owner with
	// the given login. Returns the updated owner.
	UpdateAccount(login, email string) (Owner, bool, error)
	// SetConnect persists Stripe Connect onboarding state on the owner's account.
	SetConnect(login, connectID, status string) error
	// DeleteAccount soft-deletes + anonymizes the owner: scrubs email/login, marks
	// deleted_at/anonymized, and reports ok. Financial rows are retained (de-identified).
	DeleteAccount(login string) (ok bool, err error)

	// --- ledger (append-only source of truth) ------------------------------

	// LedgerOf returns a holder's ledger rows of the given kinds (all kinds if none),
	// newest first, capped by limit.
	LedgerOf(holder string, kinds []string, limit int) ([]LedgerRow, error)
	// DeriveBalance re-sums a consumer holder's posted ledger rows. Used by the
	// drift check: it must equal the cached wallet balance.
	DeriveBalance(holder string) (float64, error)

	// --- operator earnings lifecycle ---------------------------------------

	// EarningSplitOf returns the held/reserved/payable/paid split for an operator
	// account, promoting held -> payable for any lot whose release time has passed
	// as of `now` (sweep-on-read). accountID is the owner pubkey.
	EarningSplitOf(accountID string, now time.Time) (EarningSplit, error)
	// EarningSplitOfNode is EarningSplitOf scoped to a single node (for /earnings?node=).
	EarningSplitOfNode(node string, now time.Time) (EarningSplit, error)
	// RequestPayout creates a payout from the operator's payable balance (promoting
	// lots first). It debits payable lots, writes a payout row + a payout ledger row,
	// all in one transaction. ok=false (with reason) if below minimum or nothing payable.
	RequestPayout(accountID string, now time.Time, min float64, transferID string) (payout Payout, ok bool, reason string, err error)
	// PayoutsOf returns an operator's payout history, newest first.
	PayoutsOf(accountID string, limit int) ([]Payout, error)
	// Chargeback records a consumer dispute: a chargeback ledger row against the
	// consumer wallet, and a clawback against the operator's still-held/reserved lots
	// derived from the same request. Idempotent on the Stripe dispute id.
	Chargeback(disputeID, wallet, requestID string, amount float64, now time.Time) (clawed float64, err error)
	// OpenDisputeCount returns how many open disputes touch an operator account
	// (gates account deletion / payout). accountID is the owner pubkey.
	OpenDisputeCount(accountID string) (int, error)

	Close() error
}

// Owner is a monetizing account: a GitHub identity bound to the CLI's signing
// pubkey. Consumers never need one; it gates earning (priced node registration,
// future withdraws). Additive - the consume/wallet paths ignore it.
type Owner struct {
	GitHubID  int64  `json:"github_id"`
	Login     string `json:"login"`
	Pubkey    string `json:"pubkey"` // hex ed25519 user pubkey (the binding key)
	CreatedAt int64  `json:"created_at"`
	// Account-hub fields (ACCOUNT-PAYOUTS-DESIGN). Additive; the consume/wallet
	// paths ignore them.
	Email         string `json:"email,omitempty"`
	ConnectID     string `json:"stripe_connect_id,omitempty"`
	ConnectStatus string `json:"connect_status,omitempty"` // none|onboarding|active|restricted
	DeletedAt     int64  `json:"deleted_at,omitempty"`
	Anonymized    bool   `json:"anonymized,omitempty"`
}

// Mem is the in-memory implementation (single-process, non-durable).
type Mem struct {
	mu        sync.Mutex
	wallet    map[string]float64
	earnings  map[string]float64
	spend     map[string]float64
	entries   []Entry
	processed map[string]bool
	owners    map[string]Owner // keyed by pubkey
	policy    PayoutPolicy

	ledger   []LedgerRow       // append-only money events
	ledgerID int64             // monotonic ledger id
	idem     map[string]bool   // ledger idem keys seen
	lots     []EarningLot      // operator earning lifecycle lots
	lotID    int64             // monotonic lot id
	payouts  []Payout          // payout history
	payoutID int64             // monotonic payout id
	disputes map[string]bool   // seen stripe dispute ids (idempotency)
	nodeAcct map[string]string // node id -> owner pubkey (TOFU)
}

func NewMem() *Mem {
	return &Mem{
		wallet: map[string]float64{}, earnings: map[string]float64{}, spend: map[string]float64{},
		processed: map[string]bool{}, owners: map[string]Owner{}, policy: LoadPayoutPolicy(),
		idem: map[string]bool{}, disputes: map[string]bool{}, nodeAcct: map[string]string{},
	}
}

// appendLedgerLocked records one append-only money event. Caller holds m.mu. A
// duplicate idem_key is a no-op (idempotency for free on every money event).
func (m *Mem) appendLedgerLocked(holder, side, kind string, amount float64, idemKey, state, ref string, ts int64) {
	if idemKey != "" {
		if m.idem[idemKey] {
			return
		}
		m.idem[idemKey] = true
	}
	m.ledgerID++
	if ts == 0 {
		ts = time.Now().Unix()
	}
	m.ledger = append(m.ledger, LedgerRow{
		ID: m.ledgerID, Holder: holder, Side: side, Kind: kind, Amount: amount,
		IdemKey: idemKey, State: state, Ref: ref, TS: ts,
	})
}

// addLotLocked creates an operator earning lot for a node's owner-share, splitting
// out the rolling reserve. Caller holds m.mu. No-op if the node has no bound account.
func (m *Mem) addLotLocked(node, requestID string, ownerShare float64, now time.Time) {
	acct, ok := m.nodeAcct[node]
	if !ok || ownerShare <= 0 {
		return
	}
	reserve := ownerShare * m.policy.Reserve
	rel := now.Add(m.policy.holdDuration())
	m.lotID++
	m.lots = append(m.lots, EarningLot{
		ID: m.lotID, Node: node, AccountID: acct, RequestID: requestID,
		Gross: ownerShare, Reserve: reserve, State: LotHeld,
		ReleaseAt: rel.Unix(), ReserveReleaseAt: rel.Unix(), CreatedAt: now.Unix(),
	})
	m.appendLedgerLocked(acct, "operator", KindEarn, ownerShare, "earn:"+requestID, StatePending, requestID, now.Unix())
	if reserve > 0 {
		m.appendLedgerLocked(acct, "operator", KindReserveHold, -reserve, "reserve:"+requestID, StatePending, requestID, now.Unix())
	}
}

func (m *Mem) BalanceOf(user string, seed float64) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.wallet[user]; !ok {
		m.wallet[user] = seed
		if seed != 0 {
			// Seed credits are a real balance, so they get a ledger row too (else the
			// re-derivation drift check would flag every seeded wallet).
			m.appendLedgerLocked(user, "consumer", KindAdjustment, seed, "seed:"+user, StatePosted, "seed", 0)
		}
	}
	return m.wallet[user], nil
}

func (m *Mem) Settle(user, node string, cost, ownerShare float64, rec protocol.UsageReceipt) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wallet[user] -= cost
	m.earnings[node] += ownerShare
	m.spend[user] += cost
	m.entries = append(m.entries, Entry{
		RequestID: rec.RequestID, User: user, Node: node, Model: rec.Model,
		PromptTokens: rec.PromptTokens, CompletionTokens: rec.CompletionTokens,
		Cost: cost, OwnerShare: ownerShare, TS: rec.TS,
	})
	m.appendLedgerLocked(user, "consumer", KindSpend, -cost, "spend:"+rec.RequestID, StatePosted, rec.RequestID, rec.TS)
	m.addLotLocked(node, rec.RequestID, ownerShare, time.Now())
	return m.wallet[user], nil
}

func (m *Mem) Hold(user string, amount float64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.wallet[user] < amount {
		return false, nil
	}
	m.wallet[user] -= amount
	m.appendLedgerLocked(user, "consumer", KindHold, -amount, "", StatePending, "", 0)
	return true, nil
}

func (m *Mem) Finalize(user, node string, held, cost, ownerShare float64, rec protocol.UsageReceipt) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wallet[user] += held - cost // refund the unused reservation
	m.earnings[node] += ownerShare
	m.spend[user] += cost
	m.entries = append(m.entries, Entry{
		RequestID: rec.RequestID, User: user, Node: node, Model: rec.Model,
		PromptTokens: rec.PromptTokens, CompletionTokens: rec.CompletionTokens,
		Cost: cost, OwnerShare: ownerShare, TS: rec.TS,
	})
	// Capture the hold into ledger: release the full reservation, then debit the
	// actual spend. Net wallet delta == held-cost, matching the cache above.
	m.appendLedgerLocked(user, "consumer", KindHoldRelease, held, "", StatePosted, rec.RequestID, rec.TS)
	m.appendLedgerLocked(user, "consumer", KindSpend, -cost, "spend:"+rec.RequestID, StatePosted, rec.RequestID, rec.TS)
	m.addLotLocked(node, rec.RequestID, ownerShare, time.Now())
	return m.wallet[user], nil
}

func (m *Mem) ReleaseHold(user string, held float64) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wallet[user] += held
	m.appendLedgerLocked(user, "consumer", KindHoldRelease, held, "", StatePosted, "", 0)
	return m.wallet[user], nil
}

func (m *Mem) EarningsOf(node string) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.earnings[node], nil
}

func (m *Mem) SpendOf(user string) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spend[user], nil
}

func (m *Mem) RecentByUser(user string, limit int) ([]Entry, error) {
	return m.recent(func(e Entry) bool { return e.User == user }, limit), nil
}

func (m *Mem) RecentByNode(node string, limit int) ([]Entry, error) {
	return m.recent(func(e Entry) bool { return e.Node == node }, limit), nil
}

// recent returns the most-recent entries matching pred, newest first, capped.
func (m *Mem) recent(pred func(Entry) bool, limit int) []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Entry
	for _, e := range m.entries {
		if pred(e) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS > out[j].TS })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (m *Mem) AddCredits(user string, amount float64) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wallet[user] += amount
	m.appendLedgerLocked(user, "consumer", KindTopup, amount, "", StatePosted, "", 0)
	return m.wallet[user], nil
}

func (m *Mem) MarkProcessed(key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.processed[key] {
		return false, nil
	}
	m.processed[key] = true
	return true, nil
}

func (m *Mem) CreditOnce(key, user string, amount float64) (bool, float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.processed[key] {
		return false, m.wallet[user], nil
	}
	m.processed[key] = true
	m.wallet[user] += amount
	m.appendLedgerLocked(user, "consumer", KindTopup, amount, key, StatePosted, key, 0)
	return true, m.wallet[user], nil
}

func (m *Mem) BindOwner(o Owner) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.owners == nil {
		m.owners = map[string]Owner{}
	}
	if existing, ok := m.owners[o.Pubkey]; ok {
		if existing.CreatedAt != 0 {
			o.CreatedAt = existing.CreatedAt // preserve the original bind time on refresh
		}
		// preserve account-hub state a fresh GitHub login wouldn't carry
		o.Email = existing.Email
		o.ConnectID = existing.ConnectID
		o.ConnectStatus = existing.ConnectStatus
		o.DeletedAt = existing.DeletedAt
		o.Anonymized = existing.Anonymized
	}
	m.owners[o.Pubkey] = o
	return nil
}

func (m *Mem) OwnerByPubkey(pubkey string) (Owner, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.owners[pubkey]
	return o, ok, nil
}

func (m *Mem) OwnerByLogin(login string) (Owner, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.owners {
		if o.Login == login && !o.Anonymized {
			return o, true, nil
		}
	}
	return Owner{}, false, nil
}

func (m *Mem) UpdateAccount(login, email string) (Owner, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for pk, o := range m.owners {
		if o.Login == login && !o.Anonymized {
			o.Email = email
			m.owners[pk] = o
			return o, true, nil
		}
	}
	return Owner{}, false, nil
}

func (m *Mem) SetConnect(login, connectID, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for pk, o := range m.owners {
		if o.Login == login && !o.Anonymized {
			o.ConnectID = connectID
			o.ConnectStatus = status
			m.owners[pk] = o
			return nil
		}
	}
	return nil
}

func (m *Mem) DeleteAccount(login string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for pk, o := range m.owners {
		if o.Login == login && !o.Anonymized {
			o.Email = ""
			o.Login = "deleted_" + pk[:min(8, len(pk))]
			o.Anonymized = true
			o.DeletedAt = time.Now().Unix()
			m.owners[pk] = o
			return true, nil
		}
	}
	return false, nil
}

func (m *Mem) BindNode(node, accountID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.nodeAcct[node]; !ok { // TOFU: first account wins
		m.nodeAcct[node] = accountID
	}
	return nil
}

func (m *Mem) AccountOfNode(node string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.nodeAcct[node]
	return a, ok, nil
}

func (m *Mem) NodesOfAccount(accountID string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for n, a := range m.nodeAcct {
		if a == accountID {
			out = append(out, n)
		}
	}
	return out, nil
}

func (m *Mem) LedgerOf(holder string, kinds []string, limit int) ([]LedgerRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	var out []LedgerRow
	for i := len(m.ledger) - 1; i >= 0; i-- {
		r := m.ledger[i]
		if r.Holder != holder {
			continue
		}
		if len(want) > 0 && !want[r.Kind] {
			continue
		}
		out = append(out, r)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// walletKinds are the consumer ledger kinds that represent a real wallet delta.
// Hold + hold_release both mutate the wallet, so both count (a transient pending
// hold reduces the cached balance too); reversed rows are excluded by the caller.
var walletKinds = map[string]bool{
	KindTopup: true, KindSpend: true, KindHold: true, KindHoldRelease: true,
	KindRefund: true, KindChargeback: true, KindAdjustment: true,
}

func (m *Mem) DeriveBalance(holder string) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var sum float64
	for _, r := range m.ledger {
		if r.Holder == holder && r.State != StateReversed && walletKinds[r.Kind] {
			sum += r.Amount
		}
	}
	return sum, nil
}

// promoteLocked sweeps held lots to payable when their release time has passed.
// Caller holds m.mu.
func (m *Mem) promoteLocked(now time.Time) {
	for i := range m.lots {
		l := &m.lots[i]
		if l.State == LotHeld && now.Unix() >= l.ReleaseAt {
			l.State = LotPayable
			payable := l.Gross - l.Reserve
			if payable > 0 {
				m.appendLedgerLocked(l.AccountID, "operator", KindHoldRelease, 0, "promote:"+l.RequestID, StatePosted, l.RequestID, now.Unix())
			}
			if l.Reserve > 0 && now.Unix() >= l.ReserveReleaseAt {
				m.appendLedgerLocked(l.AccountID, "operator", KindReserveRelease, l.Reserve, "reserve_rel:"+l.RequestID, StatePosted, l.RequestID, now.Unix())
			}
		}
	}
}

func (m *Mem) splitLocked(match func(EarningLot) bool, now time.Time) EarningSplit {
	m.promoteLocked(now)
	var s EarningSplit
	for _, l := range m.lots {
		if !match(l) {
			continue
		}
		switch l.State {
		case LotHeld:
			s.Held += l.Gross - l.Reserve
			s.Reserved += l.Reserve
			if s.NextRelease == 0 || l.ReleaseAt < s.NextRelease {
				s.NextRelease = l.ReleaseAt
			}
		case LotPayable:
			s.Payable += l.Gross - l.Reserve
			if now.Unix() >= l.ReserveReleaseAt {
				s.Payable += l.Reserve
			} else {
				s.Reserved += l.Reserve
				if s.NextRelease == 0 || l.ReserveReleaseAt < s.NextRelease {
					s.NextRelease = l.ReserveReleaseAt
				}
			}
		case LotPaid:
			s.Paid += l.Gross // the full lot (gross incl. released reserve) was paid out
		}
	}
	return s
}

func (m *Mem) EarningSplitOf(accountID string, now time.Time) (EarningSplit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.splitLocked(func(l EarningLot) bool { return l.AccountID == accountID }, now), nil
}

func (m *Mem) EarningSplitOfNode(node string, now time.Time) (EarningSplit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.splitLocked(func(l EarningLot) bool { return l.Node == node }, now), nil
}

func (m *Mem) RequestPayout(accountID string, now time.Time, min float64, transferID string) (Payout, bool, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.promoteLocked(now)
	var amount float64
	var idx []int
	for i, l := range m.lots {
		if l.AccountID != accountID || l.State != LotPayable {
			continue
		}
		payable := l.Gross - l.Reserve
		if now.Unix() >= l.ReserveReleaseAt {
			payable += l.Reserve
		}
		if payable <= 0 {
			continue
		}
		amount += payable
		idx = append(idx, i)
	}
	if amount < min {
		return Payout{}, false, "below minimum payout", nil
	}
	for _, i := range idx {
		m.lots[i].State = LotPaid
	}
	m.payoutID++
	p := Payout{
		ID: m.payoutID, AccountID: accountID, Amount: amount,
		StripeTransferID: transferID, State: PayoutPending, CreatedAt: now.Unix(),
	}
	if transferID != "" {
		p.State = PayoutPaid
	}
	m.payouts = append(m.payouts, p)
	m.appendLedgerLocked(accountID, "operator", KindPayout, -amount, "payout:"+strconv.FormatInt(p.ID, 10), StatePosted, transferID, now.Unix())
	return p, true, "", nil
}

func (m *Mem) PayoutsOf(accountID string, limit int) ([]Payout, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Payout
	for i := len(m.payouts) - 1; i >= 0; i-- {
		if m.payouts[i].AccountID == accountID {
			out = append(out, m.payouts[i])
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *Mem) Chargeback(disputeID, wallet, requestID string, amount float64, now time.Time) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.disputes[disputeID] {
		return 0, nil // idempotent on the stripe dispute id
	}
	m.disputes[disputeID] = true
	m.wallet[wallet] -= amount
	m.appendLedgerLocked(wallet, "consumer", KindChargeback, -amount, "dispute:"+disputeID, StatePosted, disputeID, now.Unix())
	// Claw back the operator earnings derived from the same request while still
	// held/reserved/payable (not yet paid out).
	var clawed float64
	for i := range m.lots {
		l := &m.lots[i]
		if l.RequestID != requestID || l.State == LotPaid || l.State == LotClawed {
			continue
		}
		clawed += l.Gross
		l.State = LotClawed
		m.appendLedgerLocked(l.AccountID, "operator", KindAdjustment, -l.Gross, "claw:"+disputeID+":"+l.RequestID, StatePosted, disputeID, now.Unix())
	}
	return clawed, nil
}

func (m *Mem) OpenDisputeCount(accountID string) (int, error) {
	// Mem treats a clawed lot as resolved; an "open" dispute is one with held lots
	// still attributable to this account that were clawed in the current window. For
	// the in-memory store we report 0 (no long-lived open-dispute tracking); the
	// delete guard relies primarily on balance > 0. Postgres tracks disputes.state.
	return 0, nil
}

func (m *Mem) Close() error { return nil }
