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
	// SeedOnce grants `seed` starter credits to a wallet exactly once (idempotent on
	// the wallet id): the first call posts the seed + a ledger row, later calls are
	// no-ops. Used to grant the starter balance to a GitHub account on first login,
	// so the credit lands once per account and is never re-applied on re-login. A
	// wallet that already has any seed/topup is left untouched. Subject to the seed
	// cap (SetSeedLimit): once the limit of distinct seeded wallets is reached, a new
	// wallet is created at 0. `seeded` reports whether this call actually applied a
	// non-zero credit (false on a re-seed no-op OR when the cap blocked the grant).
	SeedOnce(wallet string, seed float64) (newBalance float64, seeded bool, err error)
	// PeekBalance returns a wallet's balance WITHOUT seeding it (0 for an unknown
	// wallet). Used to read an anonymous/unbound wallet that must never be seeded.
	PeekBalance(wallet string) (float64, error)
	// SetSeedLimit caps how many DISTINCT wallets ever receive a non-zero starter
	// seed. After `limit` wallets have been seeded, further new wallets are created
	// with a 0 balance (no seed), bounding total free-credit liability to
	// limit*seedCredits. limit <= 0 disables the cap (every new wallet is seeded, the
	// pre-cap behavior). The seeded-user count is tracked durably and incremented
	// ATOMICALLY with each grant so the cap holds under concurrency (no over-grant).
	SetSeedLimit(limit int)
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

	// --- node registry persistence (survives broker restarts) ---------------

	// UpsertNode persists (or refreshes) a node's registration so the broker's
	// in-memory registry can be RE-HYDRATED after a restart/redeploy. Keyed on
	// NodeID; the full record (pubkey, offers+pricing, HW, region, confidential,
	// bridge token, last_seen) is upserted on every register. registered_at is set
	// once (first insert) and preserved on refresh. This is what stops a redeploy
	// from wiping the registry and 404ing every still-running provider forever.
	UpsertNode(n NodeRecord) error
	// TouchNode bumps a persisted node's last_seen to `seen` WITHOUT a full
	// re-register, so an ongoing heartbeat/poll keeps the durable liveness fresh
	// (and a restart re-hydrates a recent last_seen, not a stale one). No-op if the
	// node was never registered. Cheap: a single indexed UPDATE.
	TouchNode(nodeID string, seen time.Time) error
	// AllNodes returns every persisted node record (for startup re-hydration).
	AllNodes() ([]NodeRecord, error)

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

	// --- monthly spend cap (per-account budget limit, cap.go) --------------

	// MonthlyCapOf returns a wallet's monthly spend cap in credits ($). 0 = unlimited.
	// An un-set wallet resolves to DefaultMonthlyCap (the env default); a wallet that
	// explicitly chose unlimited stores 0 and is not re-defaulted.
	MonthlyCapOf(holder string) (float64, error)
	// SetMonthlyCap durably records a wallet's monthly cap (cap<=0 = unlimited).
	SetMonthlyCap(holder string, cap float64) error
	// MonthSpendOf returns a wallet's captured spend within the CALENDAR month
	// containing `now`, summed from the append-only ledger's posted spend rows
	// (boundary-correct, drift-proof). Drives the cap enforcement + near/at notices.
	MonthSpendOf(holder string, now time.Time) (float64, error)

	// --- operator earnings lifecycle ---------------------------------------

	// EarningSplitOf returns the held/reserved/payable/paid split for an operator
	// account, promoting held -> payable for any lot whose release time has passed
	// as of `now` (sweep-on-read). accountID is the owner pubkey.
	EarningSplitOf(accountID string, now time.Time) (EarningSplit, error)
	// EarningSplitOfNode is EarningSplitOf scoped to a single node (for /earnings?node=).
	EarningSplitOfNode(node string, now time.Time) (EarningSplit, error)
	// RequestPayout debits the operator's payable balance and records a PENDING payout
	// (promoting lots first) in ONE transaction, returning the exact debited amount.
	// The caller creates the Stripe transfer AFTER this (for the returned amount), then
	// finalizes with SettlePayout (money moved) or FailPayout (transfer failed). This
	// ordering guarantees a transfer is never issued without a matching recorded debit,
	// nor for an amount different from what was debited. ok=false (with reason) if below
	// minimum or nothing payable.
	RequestPayout(accountID string, now time.Time, min float64) (payout Payout, ok bool, reason string, err error)
	// SettlePayout marks a pending payout PAID and records its Stripe transfer id.
	// Idempotent (settling an already-paid payout is a no-op).
	SettlePayout(payoutID int64, transferID string) error
	// FailPayout rolls a pending payout back: its debited lots return to PAYABLE, the
	// payout is marked FAILED, and the payout ledger row is reversed. Used when the
	// Stripe transfer fails after a successful debit, so no completed transfer is ever
	// left with payable lots (and no orphan debit remains).
	FailPayout(payoutID int64) error
	// PayoutsOf returns an operator's payout history, newest first.
	PayoutsOf(accountID string, limit int) ([]Payout, error)
	// Chargeback records a consumer dispute: a chargeback ledger row against the
	// consumer wallet, and a clawback against the operator's still-held/payable lots
	// derived from that consumer. Idempotent on the Stripe dispute id. When requestID
	// is non-empty the clawback targets that one request's lots (legacy path); when it
	// is empty the clawback targets lots attributed to `wallet` (via the request
	// receipts) by recency, up to the disputed amount. Returns the credits clawed.
	Chargeback(disputeID, wallet, requestID string, amount float64, now time.Time) (clawed float64, err error)
	// LinkCharge persists the mapping from a Stripe payment_intent / charge id to the
	// (wallet, credits) of a completed checkout, so a later charge.dispute.created
	// (which carries NONE of the checkout metadata) can resolve the wallet to claw
	// back. Idempotent on the session id (Stripe redelivery safe).
	LinkCharge(sessionID, paymentIntent, charge, wallet string, credits float64) error
	// WalletByCharge resolves the wallet + credits a completed checkout credited, keyed
	// by EITHER the Stripe payment_intent or charge id (a dispute object carries one of
	// these). ok=false if no mapping exists.
	WalletByCharge(ref string) (wallet string, credits float64, ok bool, err error)
	// OpenDisputeCount returns how many open disputes touch an operator account
	// (gates account deletion / payout). accountID is the owner pubkey.
	OpenDisputeCount(accountID string) (int, error)

	// --- grant keys (GRANT-KEYS-DESIGN) ------------------------------------

	// CreateGrant persists an owner-issued grant (free or custom-priced private
	// access key). Only the secret HASH is stored; the secret is shown once at create.
	CreateGrant(g Grant) error
	// GrantBySecretHash is the hot auth lookup: resolve a grant from sha256(secret).
	GrantBySecretHash(hash string) (Grant, bool, error)
	// GrantsByOwner lists an owner's grants (dashboard + CLI list).
	GrantsByOwner(owner string) ([]Grant, error)
	// SetGrantRevoked flips a grant's revoked flag, owner-scoped (an owner can never
	// touch another owner's grant). ok=false if the grant doesn't exist for that owner.
	SetGrantRevoked(id, owner string, revoked bool) (bool, error)
	// UpdateGrant applies an owner-scoped patch (caps/scope/price/revoked) and
	// returns the updated grant.
	UpdateGrant(id, owner string, patch GrantPatch) (Grant, bool, error)
	// GrantUsageOf returns a grant's token usage for the current UTC day + month
	// (the cap check + dashboard rollup).
	GrantUsageOf(id string, now time.Time) (GrantUsage, error)
	// AddGrantUsage increments a grant's day + month token rollup at settle time.
	AddGrantUsage(id string, tokens int64, now time.Time) error

	// --- private bands ("frequency codes": private discovery) - BANDS-DESIGN ----

	// CreateBand persists an owner-issued private band. Only the code HASH is stored;
	// the secret frequency code is returned once at mint.
	CreateBand(b Band) error
	// BandByCodeHash is the resolve lookup: a band from sha256(canonical secret tail).
	BandByCodeHash(hash string) (Band, bool, error)
	// BandByNode returns the band bound to a node (idempotent re-register lookup).
	BandByNode(nodeID string) (Band, bool, error)
	// BandsByOwner lists an owner's bands (dashboard + CLI list).
	BandsByOwner(owner string) ([]Band, error)
	// SetBandRevoked flips a band's revoked flag, owner-scoped (an owner can never
	// touch another owner's band). ok=false if the band doesn't exist for that owner.
	SetBandRevoked(id, owner string, revoked bool) (bool, error)
	// CountActiveBands counts an owner's live (non-revoked, non-expired) bands as of
	// now - the free-cap enforcement point (compared against BandQuota at register).
	CountActiveBands(owner string, now time.Time) (int, error)

	// --- safety: CSAM preservation + abuse reports + node bans (safety.go) ----

	// PreserveCSAM records a child-exploitation hit (18 USC 2258A): the broker-ENCRYPTED
	// offending content plus pseudonym/ip/category/timestamp, in the access-controlled
	// rogerai.csam_incidents table. ReportState defaults to "queued" (a CyberTipline
	// report is owed). Returns the new incident id.
	PreserveCSAM(inc CSAMIncident) (int64, error)
	// PendingCSAMReports lists incidents still owing a report ("queued"), newest first,
	// for a follow-up CyberTipline submitter to drain.
	PendingCSAMReports(limit int) ([]CSAMIncident, error)
	// MarkCSAMReported flips an incident's obligation to "reported" once filed.
	MarkCSAMReported(id int64) error

	// AddReport persists an abuse/quality report (POST /report). Returns the report id.
	AddReport(r Report) (int64, error)
	// ReportCountByNode returns how many reports name a node (drives the ban threshold).
	ReportCountByNode(nodeID string) (int, error)
	// ReportsByNode lists a node's reports (admin/dashboard), newest first.
	ReportsByNode(nodeID string, limit int) ([]Report, error)
	// BanNode flips a node OUT of routing (pick/market/discover) with a reason.
	// Idempotent (first reason wins).
	BanNode(nodeID, reason string) error
	// BannedNodes returns the banned node set (id -> reason), re-hydrated at startup so
	// a ban survives a broker restart.
	BannedNodes() (map[string]string, error)

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

// NodeRecord is a persisted node registration - the durable copy of the broker's
// in-memory registry entry, written on every register and re-hydrated on startup
// so a broker restart/redeploy does NOT wipe who is registered. It carries enough
// to reconstruct the live entry (the protocol.NodeRegistration, the confidential
// verdict, and the last_seen for a short liveness grace across the restart window).
// The bridge token is stored so a re-hydrated node can still AUTH its ongoing
// heartbeat/poll without re-registering.
type NodeRecord struct {
	NodeID       string                    `json:"node_id"`
	Reg          protocol.NodeRegistration `json:"reg"` // pubkey, offers+pricing, HW, region, bridge token, attestation
	Confidential bool                      `json:"confidential"`
	LastSeen     int64                     `json:"last_seen"`     // unix seconds (for the restart-window grace)
	RegisteredAt int64                     `json:"registered_at"` // unix seconds (set once, preserved on refresh)
}

// Mem is the in-memory implementation (single-process, non-durable).
type Mem struct {
	mu         sync.Mutex
	wallet     map[string]float64
	earnings   map[string]float64
	spend      map[string]float64
	entries    []Entry
	processed  map[string]bool
	owners     map[string]Owner // keyed by pubkey
	policy     PayoutPolicy
	monthlyCap map[string]float64 // wallet -> explicit monthly spend cap ($); absent = env default

	ledger   []LedgerRow           // append-only money events
	ledgerID int64                 // monotonic ledger id
	idem     map[string]bool       // ledger idem keys seen
	lots     []EarningLot          // operator earning lifecycle lots
	lotID    int64                 // monotonic lot id
	payouts  []Payout              // payout history
	payoutID int64                 // monotonic payout id
	disputes map[string]bool       // seen stripe dispute ids (idempotency)
	nodeAcct map[string]string     // node id -> owner pubkey (TOFU)
	charges  map[string]charge     // stripe payment_intent/charge id -> checkout mapping
	gs       *grantStore           // grant keys + per-grant usage rollups
	bs       *bandStore            // private bands ("frequency codes": private discovery)
	nodes    map[string]NodeRecord // persisted node registry (re-hydrated on restart)

	// safety surfaces (safety.go): preserved CSAM incidents + the abuse/report log +
	// banned-node set. Rare, off the hot path; guarded by the same m.mu.
	csam     []CSAMIncident    // preserved child-exploitation hits (encrypted content)
	csamID   int64             // monotonic incident id
	reports  []Report          // abuse/quality reports (POST /report)
	reportID int64             // monotonic report id
	banned   map[string]string // node id -> ban reason (ejected from pick/market/discover)

	// Seed cap: bound free-credit liability. seedLimit is the max number of distinct
	// wallets ever seeded with non-zero starter credits (<=0 = unlimited); seedCount
	// is how many have been seeded so far. Both are guarded by mu and the count is
	// incremented in the same locked section that applies the seed, so a burst of
	// concurrent first-seeds can never over-grant past the limit.
	seedLimit int
	seedCount int
}

// charge is a persisted checkout->charge mapping, so a later dispute (which carries
// none of the checkout metadata) can resolve the wallet to claw back.
type charge struct {
	sessionID string
	wallet    string
	credits   float64
}

func NewMem() *Mem {
	return &Mem{
		wallet: map[string]float64{}, earnings: map[string]float64{}, spend: map[string]float64{},
		processed: map[string]bool{}, owners: map[string]Owner{}, policy: LoadPayoutPolicy(),
		idem: map[string]bool{}, disputes: map[string]bool{}, nodeAcct: map[string]string{},
		charges: map[string]charge{}, gs: newGrantStore(), bs: newBandStore(), nodes: map[string]NodeRecord{},
		banned: map[string]string{},
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

func (m *Mem) SetSeedLimit(limit int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seedLimit = limit
}

// grantSeedLocked applies the starter seed to a wallet at most once, enforcing the
// seed cap atomically. Caller holds m.mu. It returns granted=true only when THIS call
// actually credited a non-zero seed. It is a no-op (seed already applied) when the
// "seed:<wallet>" idem key is present. When the wallet is new AND the seed cap is not
// yet exhausted, it credits `seed`, posts the seed ledger row, and increments the
// durable seeded-user count - all under the same lock, so concurrent first-seeds can
// never push the count past the limit. Once the cap is hit, a new wallet is left at 0.
func (m *Mem) grantSeedLocked(wallet string, seed float64) bool {
	if m.idem["seed:"+wallet] {
		return false // already seeded (here or via the other seed path)
	}
	if seed == 0 {
		return false
	}
	if m.seedLimit > 0 && m.seedCount >= m.seedLimit {
		return false // cap exhausted: this new wallet gets no seed
	}
	m.wallet[wallet] += seed
	m.seedCount++
	// Seed credits are a real balance, so they get a ledger row too (else the
	// re-derivation drift check would flag every seeded wallet). The idem key also
	// marks this wallet as seeded so neither seed path re-grants it.
	m.appendLedgerLocked(wallet, "consumer", KindAdjustment, seed, "seed:"+wallet, StatePosted, "seed", 0)
	return true
}

func (m *Mem) BalanceOf(user string, seed float64) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.wallet[user]; !ok {
		m.wallet[user] = 0
		m.grantSeedLocked(user, seed)
	}
	return m.wallet[user], nil
}

// SeedOnce grants starter credits to a wallet exactly once, keyed on the same
// "seed:<wallet>" idem key BalanceOf uses, so the seed is applied at most once per
// wallet whichever path touches it first. The seed cap (SetSeedLimit) applies here
// too: once the limit of distinct seeded wallets is reached, a new wallet is created
// at 0. `seeded` reports whether THIS call observed the wallet as not-yet-seeded (so
// a re-login is still a no-op); it does not imply the cap allowed a non-zero grant.
func (m *Mem) SeedOnce(wallet string, seed float64) (float64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idem["seed:"+wallet] {
		return m.wallet[wallet], false, nil // already seeded (here or via BalanceOf)
	}
	if _, ok := m.wallet[wallet]; !ok {
		m.wallet[wallet] = 0
	}
	seeded := m.grantSeedLocked(wallet, seed)
	return m.wallet[wallet], seeded, nil
}

// PeekBalance returns a wallet's balance without ever seeding it.
func (m *Mem) PeekBalance(wallet string) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.wallet[wallet], nil
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

func (m *Mem) UpsertNode(n NodeRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nodes == nil {
		m.nodes = map[string]NodeRecord{}
	}
	if prev, ok := m.nodes[n.NodeID]; ok && prev.RegisteredAt != 0 {
		n.RegisteredAt = prev.RegisteredAt // preserve the first-register time on refresh
	} else if n.RegisteredAt == 0 {
		n.RegisteredAt = time.Now().Unix()
	}
	m.nodes[n.NodeID] = n
	return nil
}

func (m *Mem) TouchNode(nodeID string, seen time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.nodes[nodeID]; ok { // no-op if the node was never registered
		r.LastSeen = seen.Unix()
		m.nodes[nodeID] = r
	}
	return nil
}

func (m *Mem) AllNodes() ([]NodeRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]NodeRecord, 0, len(m.nodes))
	for _, r := range m.nodes {
		out = append(out, r)
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

// RequestPayout debits the operator's payable lots and creates a PENDING payout in
// ONE locked transaction, returning the exact debited amount. The Stripe transfer is
// created by the caller AFTER this returns (for the returned amount), then settled
// via SettlePayout or rolled back via FailPayout - so a transfer can never be issued
// without a matching recorded debit, nor for a different amount than was debited.
func (m *Mem) RequestPayout(accountID string, now time.Time, min float64) (Payout, bool, string, error) {
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
	m.payoutID++
	pid := m.payoutID
	for _, i := range idx {
		m.lots[i].State = LotPaid
		m.lots[i].PayoutID = pid
	}
	p := Payout{
		ID: pid, AccountID: accountID, Amount: amount,
		State: PayoutPending, CreatedAt: now.Unix(),
	}
	m.payouts = append(m.payouts, p)
	m.appendLedgerLocked(accountID, "operator", KindPayout, -amount, "payout:"+strconv.FormatInt(p.ID, 10), StatePosted, "", now.Unix())
	return p, true, "", nil
}

// SettlePayout marks a pending payout PAID and records its Stripe transfer id (the
// money has moved). Idempotent: settling an already-paid payout is a no-op.
func (m *Mem) SettlePayout(payoutID int64, transferID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.payouts {
		if m.payouts[i].ID == payoutID {
			if m.payouts[i].State == PayoutPaid {
				return nil
			}
			m.payouts[i].State = PayoutPaid
			m.payouts[i].StripeTransferID = transferID
			// Stamp the transfer id onto the payout ledger row's ref.
			ref := "payout:" + strconv.FormatInt(payoutID, 10)
			for j := range m.ledger {
				if m.ledger[j].Kind == KindPayout && m.ledger[j].IdemKey == ref {
					m.ledger[j].Ref = transferID
				}
			}
			return nil
		}
	}
	return nil
}

// FailPayout rolls a pending payout back: its debited lots return to PAYABLE, the
// payout is marked FAILED, and the payout ledger row is reversed (so the debit no
// longer counts). Used when the Stripe transfer fails AFTER a successful debit, so
// no completed transfer is ever left with payable lots and no orphan debit remains.
func (m *Mem) FailPayout(payoutID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.payouts {
		if m.payouts[i].ID == payoutID {
			if m.payouts[i].State != PayoutPending {
				return nil // already settled or failed; nothing to roll back
			}
			m.payouts[i].State = PayoutFailed
			break
		}
	}
	for i := range m.lots {
		if m.lots[i].PayoutID == payoutID && m.lots[i].State == LotPaid {
			m.lots[i].State = LotPayable
			m.lots[i].PayoutID = 0
		}
	}
	ref := "payout:" + strconv.FormatInt(payoutID, 10)
	for j := range m.ledger {
		if m.ledger[j].Kind == KindPayout && m.ledger[j].IdemKey == ref {
			m.ledger[j].State = StateReversed
		}
	}
	return nil
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

	// Clawable lots: still held/payable (not yet paid out or already clawed). When a
	// requestID is given we target that one request (legacy path); otherwise we target
	// the request ids attributed to this consumer wallet (via the receipts), newest
	// first, capped at the disputed amount.
	clawable := func(l *EarningLot) bool { return l.State != LotPaid && l.State != LotClawed }
	var order []int // indexes into m.lots to consider, in claw order
	if requestID != "" {
		for i := range m.lots {
			if m.lots[i].RequestID == requestID && clawable(&m.lots[i]) {
				order = append(order, i)
			}
		}
	} else {
		// request ids this consumer paid for, newest first.
		reqTS := map[string]int64{}
		for _, e := range m.entries {
			if e.User == wallet {
				reqTS[e.RequestID] = e.TS
			}
		}
		for i := range m.lots {
			if _, ok := reqTS[m.lots[i].RequestID]; ok && clawable(&m.lots[i]) {
				order = append(order, i)
			}
		}
		sort.SliceStable(order, func(a, b int) bool {
			return reqTS[m.lots[order[a]].RequestID] > reqTS[m.lots[order[b]].RequestID]
		})
	}

	// Claw lots up to the disputed amount (the platform is liable only for what it
	// lost on this charge). With an explicit requestID we claw all its lots.
	var clawed float64
	for _, i := range order {
		if requestID == "" && clawed >= amount {
			break
		}
		l := &m.lots[i]
		clawed += l.Gross
		l.State = LotClawed
		m.appendLedgerLocked(l.AccountID, "operator", KindAdjustment, -l.Gross, "claw:"+disputeID+":"+l.RequestID, StatePosted, disputeID, now.Unix())
	}
	return clawed, nil
}

func (m *Mem) LinkCharge(sessionID, paymentIntent, charge_, wallet string, credits float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := charge{sessionID: sessionID, wallet: wallet, credits: credits}
	if paymentIntent != "" {
		m.charges[paymentIntent] = c
	}
	if charge_ != "" {
		m.charges[charge_] = c
	}
	return nil
}

func (m *Mem) WalletByCharge(ref string) (string, float64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ref == "" {
		return "", 0, false, nil
	}
	c, ok := m.charges[ref]
	return c.wallet, c.credits, ok, nil
}

func (m *Mem) OpenDisputeCount(accountID string) (int, error) {
	// Mem treats a clawed lot as resolved; an "open" dispute is one with held lots
	// still attributable to this account that were clawed in the current window. For
	// the in-memory store we report 0 (no long-lived open-dispute tracking); the
	// delete guard relies primarily on balance > 0. Postgres tracks disputes.state.
	return 0, nil
}

func (m *Mem) Close() error { return nil }
