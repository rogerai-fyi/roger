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
	// SeedStatus reports the durable seed-grant accounting: `seeded` distinct wallets
	// have received a non-zero starter seed, out of `limit` (0 = unlimited). `remaining`
	// is the seeds left before the cap (max(limit-seeded,0); -1 when unlimited). It reads
	// the authoritative seed_counter (Postgres) / seedCount (Mem) so a homepage promo can
	// show "free credits remaining" and auto-hide at 0.
	SeedStatus() (seeded, limit, remaining int, err error)
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
	// EntriesByUser returns a user's settled requests within the [since,until) unix
	// window (newest first). Powers the consumer time-series + savings rollups, which
	// bucket the receipts by day/hour and model in the handler. Receipt-derived.
	EntriesByUser(user string, since, until int64) ([]Entry, error)
	// EntriesByAccount returns the settled requests served by ALL nodes bound to an
	// operator account (owner pubkey) within the [since,until) unix window (newest
	// first). Powers the provider earnings time-series + the owner console feed.
	EntriesByAccount(accountID string, since, until int64) ([]Entry, error)
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
	// DeleteNode removes a node's persisted REGISTRATION (the rogerai.nodes row) so a
	// long-dead node stops being re-hydrated into the registry/market. It touches ONLY
	// the registration - earnings (the ledger) and the node->owner binding are separate
	// and are deliberately left intact, so historical attribution/payouts are unaffected.
	// No-op (nil) if the node has no record. A still-running provider that is pruned
	// simply re-registers on its next heartbeat. Used by the stale-node prune sweep.
	DeleteNode(nodeID string) error

	// --- owner-authored price/schedule overrides (web console pricing) -------
	//
	// An OfferOverride is the EFFECTIVE PUBLISHED price/schedule an OWNER set from the
	// web console for one (node, model). The broker SEEDS the node's in-memory offer
	// from it on every register (so it survives node re-registration AND a broker
	// restart), and ActivePrice reads it at serve time. It records only a FUTURE
	// (published) price - it NEVER mutates a past receipt or any ledger row.

	// SetOfferOverride upserts an owner-authored override (keyed on node+model, stamped
	// with the owner pubkey). The caller stamps UpdatedAt. Owner-scoped: a stored
	// override carries the authoring owner so it can never shadow another account's node.
	SetOfferOverride(ov OfferOverride) error
	// OfferOverride returns the override for (node,model), ok=false if none. Used at
	// register time to seed the node's effective published price.
	OfferOverride(node, model string) (OfferOverride, bool, error)
	// OverridesByOwner lists all of an owner's authored overrides (the console list).
	OverridesByOwner(owner string) ([]OfferOverride, error)
	// ClearOfferOverride removes an owner's override for (node,model), OWNER-SCOPED:
	// it deletes only when the stored override's owner matches `owner` (so an owner can
	// never clear another account's override). ok=false if there is no such override
	// for that owner. After a clear, the node's NEXT registration restores its own
	// node-supplied price/schedule.
	ClearOfferOverride(owner, node, model string) (bool, error)

	// --- account hub (ACCOUNT-PAYOUTS-DESIGN) -------------------------------

	// OwnerByLogin returns the owner with the given GitHub login, ok=false if none.
	// A login resolved to an anonymized (deleted) account reports ok=false.
	OwnerByLogin(login string) (Owner, bool, error)
	// UpdateAccount applies user-editable profile fields (email) to the owner with
	// the given login. Returns the updated owner.
	UpdateAccount(login, email string) (Owner, bool, error)
	// ClaimWelcome atomically stamps the owner's WelcomedAt (now) IFF it is unset,
	// returning whether THIS call claimed it. It is the once-only guard for the welcome
	// email: a true result means the caller (and only the caller) should send it.
	ClaimWelcome(pubkey string) (bool, error)
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

	// --- per-model metrics (metrics.go) ------------------------------------

	// ProviderMetrics returns the per-(model,node) breakdown of what the account's
	// node(s) SERVED over the trailing [since,until) unix window: requests, tokens
	// in/out, a free-vs-paid split (free = no owner earnings on the request), and the
	// owner's earnings (the 70% net share). accountID is the owner pubkey; only nodes
	// bound to that account are counted. Receipt-derived (no drift from earnings).
	ProviderMetrics(accountID string, since, until int64) ([]ProviderModelMetric, error)
	// UsageMetrics returns the per-model breakdown of what the wallet CONSUMED over the
	// trailing [since,until) unix window: requests, tokens in/out, a free-vs-paid split
	// (free = a $0 request), and total spend. Receipt-derived (no drift from spend).
	UsageMetrics(wallet string, since, until int64) ([]UsageModelMetric, error)

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
	// ReleaseSchedule returns the operator's UPCOMING earning releases as a dated ladder:
	// the still-held lots (gross-minus-reserve) grouped by their release calendar day, so
	// the Payouts page can render "$X clears Jun 30, $Y clears Jul 15" instead of only the
	// single soonest date EarningSplit.NextRelease carries. Sweeps held->payable first (so
	// a lot whose hold already cleared is not shown as upcoming), then buckets the
	// remaining held lots by UTC midnight of release_at, ascending. accountID is the owner
	// pubkey; reads off earning_lots (indexed lots_account).
	ReleaseSchedule(accountID string, now time.Time) ([]ReleaseBucket, error)
	// EarningRollups returns the account's earnings attributed per MODEL and per NODE
	// across all its non-clawed lots (held+payable+paid gross), so the earnings view can
	// show where the money came from. Cheap rollup off the same lots+receipts the split
	// reads. accountID is the owner pubkey.
	EarningRollups(accountID string) (byModel, byNode []EarningRollup, err error)
	// PayoutLots returns the funding earning lots behind a payout (the request-level
	// lineage a payout-history row expands into): {request_id, node, model, gross,
	// created_at} per lot. Owner-scoped: ok=false if the payout id is not the caller's
	// (cross-account access is rejected, never leaking another operator's receipts).
	// Reads off earning_lots by payout_id, joining the request receipt for the model.
	// accountID is the owner pubkey.
	PayoutLots(accountID string, payoutID int64) (lots []PayoutLot, ok bool, err error)
	// SetNodeRecountHold flags (held=true) or clears (held=false) a node as having an
	// OPEN L1 re-count discrepancy. While held, the sweep-on-read promotion holds that
	// node's earning lots in `held` instead of auto-promoting them to `payable` (P0-2):
	// an over-reporting node's earnings are kept un-cashable pending review rather than
	// becoming payable on schedule. Idempotent. The flag is broker-fed from observeRecount.
	SetNodeRecountHold(node string, held bool) error
	// RecountHeldNodes returns the set of nodes currently flagged with an open re-count
	// discrepancy, so the broker can re-hydrate the in-memory view after a restart.
	RecountHeldNodes() (map[string]bool, error)
	// ExpireRecountHolds clears every node AND account recount hold first placed at or
	// before `olderThan` (OPERATOR RECOURSE / auto-expiry): a hold is a freeze pending
	// review, not a permanent sentence, so a held-for-review state auto-clears after a
	// configurable window if no further discrepancy re-arms it. It returns how many holds
	// (nodes+accounts) it cleared. The broker re-arms a hold the instant a fresh
	// discrepancy lands, so an actually-abusive operator never escapes - only an honest
	// operator hit by a false positive is unfrozen. Idempotent (clearing a clear hold is
	// a no-op).
	ExpireRecountHolds(olderThan time.Time) (cleared int, err error)
	// Chargeback records a consumer dispute: a chargeback ledger row against the
	// consumer wallet, and a clawback against the operator's still-held/payable lots
	// derived from that consumer. Idempotent on the Stripe dispute id. When requestID
	// is non-empty the clawback targets that one request's lots (legacy path); when it
	// is empty the clawback targets lots attributed to `wallet` (via the request
	// receipts) by recency, up to the disputed amount. Returns the credits clawed.
	Chargeback(disputeID, wallet, requestID string, amount float64, now time.Time) (clawed float64, err error)
	// ChargebackLineage is the lineage-attributed dispute clawback (P0-3 + P0-4). It
	// resolves the disputed charge to its consumer wallet's OWN earning lots (the
	// checkout_charges -> receipts -> earning_lots link) and claws those EXACT lots up
	// to the disputed amount, newest first - never unrelated honest operators' lots:
	//   - held/payable lots are clawed in-place (adjustment -gross), counted in Clawed;
	//   - ALREADY-PAID lots are marked clawed with a payout_reversed ledger row and
	//     RETURNED as Reversals so the broker can issue the Stripe Transfer Reversal
	//     against the operator's connected account (6.4 step 4);
	//   - any disputed amount NOT covered by this consumer's lots is recorded as a
	//     platform_loss ledger row (the platform is liable) instead of clawing other
	//     operators.
	// All store mutations happen in ONE transaction, idempotent on the dispute id
	// (a redelivery returns AlreadyHandled=true and does nothing). With an explicit
	// requestID it targets that one request's lots (legacy/precise path).
	ChargebackLineage(disputeID, wallet, requestID string, amount float64, now time.Time) (ChargebackResult, error)
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
	// DistinctReporterCountByNode returns how many DISTINCT reporters (distinct non-empty
	// reporter IP) named a node at or after `since` (unix seconds). This is the
	// corroboration-and-decay count the auto-eject uses INSTEAD of a raw all-time
	// COUNT(*): one source can no longer stack N reports to ban a node (it counts once),
	// and stale reports outside the trailing window age out (a node that fixed its issue
	// recovers automatically). A report with no reporter IP does not count toward
	// corroboration.
	DistinctReporterCountByNode(nodeID string, since int64) (int, error)
	// ReportsByNode lists a node's reports (admin/dashboard), newest first.
	ReportsByNode(nodeID string, limit int) ([]Report, error)
	// BanNode flips a node OUT of routing (pick/market/discover) with a reason.
	// Idempotent (first reason wins).
	BanNode(nodeID, reason string) error
	// BannedNodes returns the banned node set (id -> reason), re-hydrated at startup so
	// a ban survives a broker restart.
	BannedNodes() (map[string]string, error)
	// UnbanNode lifts a node ban (the missing node recovery path): deletes the
	// banned_nodes row so the node can route again. Idempotent (unbanning a clean node is
	// a no-op). Used by the admin node-unban + the self-serve appeal auto-exoneration.
	UnbanNode(nodeID string) error
	// ExpireNodeBans auto-lifts TEMPORARY report-origin node suspensions first placed at
	// or before `olderThan` (the node twin of ExpireRecountHolds): a report-threshold
	// eject is a time-boxed suspension pending review, not a permanent sentence, so it
	// auto-clears after a configurable window unless fresh corroboration / an admin keeps
	// it. It ONLY clears report-origin bans (reason starts with "report ") - an admin or
	// crypto-verified permanent ban is never auto-lifted. Returns the node ids it cleared
	// so the broker can refresh its in-memory ban cache. Idempotent.
	ExpireNodeBans(olderThan time.Time) (cleared []string, err error)

	// --- owner-keyed durable bans + strikes (anti-abuse, OWNER not node_id) ----
	//
	// A node_id is a cheap-to-rotate callsign; enforcement that must SURVIVE rotation
	// binds to the OWNER ACCOUNT (the GitHub-bound owner pubkey, AccountOfNode). These
	// accrue evidence-bound strikes and, at a threshold, durably ban the owner so a
	// banned operator cannot return under a fresh node id / callsign / grant key.

	// OwnerStrike appends ONE evidence-bound strike to an owner account and returns the
	// owner's resulting TOTAL strike count. `kind` is the violation class
	// (impossible-input | empty-output | recount-discrepancy); `evidenceJSON` is the
	// provable record (the signed receipt's claim vs the broker recount, request id,
	// axis, delta) the operator can be SHOWN. Append-only (every strike is kept as
	// evidence). Idempotent on idemKey when non-empty (so the same request can't
	// double-strike on a retry); pass "" to always append.
	OwnerStrike(accountID, kind, evidenceJSON, idemKey string) (count int, err error)
	// StrikesByOwner returns an owner's strike evidence rows, newest first (the
	// surface that SHOWS the operator exactly why they were warned/banned).
	StrikesByOwner(accountID string, limit int) ([]Strike, error)
	// BanOwner durably bans an operator account (owner pubkey) with a reason +
	// evidence. The ban blocks register + relay pick + settle for EVERY current and
	// future node under that owner. Idempotent (first ban wins, evidence preserved).
	BanOwner(accountID, reason, evidenceJSON string) error
	// IsOwnerBanned reports whether an owner account is durably banned, with the reason.
	IsOwnerBanned(accountID string) (banned bool, reason string, err error)
	// BannedOwners returns the banned owner set (account id -> reason), re-hydrated at
	// startup so an owner ban survives a broker restart.
	BannedOwners() (map[string]string, error)
	// SetAccountRecountHold flags (held=true) or clears (held=false) an OWNER ACCOUNT
	// as under review: while held, ALL of the owner's earning lots are kept from
	// promoting held->payable (the owner-level twin of SetNodeRecountHold, surviving
	// node-id rotation). Idempotent.
	SetAccountRecountHold(accountID string, held bool) error
	// ForgiveOwner is the ADMIN-reviewed recourse primitive (OPERATOR RECOURSE): it
	// reverses ALL durable anti-abuse state against an owner account after a human
	// review clears them - it deletes the owner's strikes, lifts the durable owner ban,
	// and clears the account recount hold, in one call. It returns how many strikes were
	// forgiven (for the audit log). Idempotent: forgiving a clean account is a no-op.
	// The broker also refreshes its in-memory owner-ban cache after calling this.
	ForgiveOwner(accountID string) (forgiven int, err error)
	// OwnerStrikeStats returns the RECENT (decay-windowed) anti-abuse posture for an
	// owner account: how many strikes were accrued at or after `since` (unix seconds) and
	// across how many DISTINCT signal classes (kinds). It drives the reliability rules in
	// strike(): decay (only strikes inside the trailing window count toward a ban, so old
	// resolved noise ages out) and corroboration (an accumulating-signal ban requires
	// MORE THAN ONE distinct signal class, so a single noisy class can never ban alone).
	// Terminal "ban:*" marker strikes are excluded (they are an audit record of the ban,
	// not an independent signal). `since`<=0 counts all strikes.
	OwnerStrikeStats(accountID string, since int64) (windowed, distinctKinds int, err error)

	// --- self-serve appeals (ban hardening 3.3) ----------------------------
	//
	// A banned/struck operator files an appeal here; it lands in the admin review queue.
	// Owner-scoped: the account_id is the AUTHENTICATED owner pubkey, never a
	// request-supplied account, so an appeal can only ever be filed for the caller.

	// AddAppeal records one owner-filed appeal (node ban and/or account strike/ban) with
	// the operator's note, state "open". Returns the appeal id.
	AddAppeal(a Appeal) (int64, error)
	// AppealsByOwner lists an owner account's appeals, newest first (the caller's own
	// appeal history / status surface). Owner-scoped by account_id.
	AppealsByOwner(accountID string, limit int) ([]Appeal, error)
	// PendingAppeals lists OPEN appeals across all accounts, newest first (the admin
	// review queue). Admin-gated at the handler.
	PendingAppeals(limit int) ([]Appeal, error)

	// --- failed-reversal retry (silent-money-leak guard) ---------------------
	// RecordPendingReversal durably records the intent to reverse an already-paid,
	// disputed lot's Stripe Transfer. Idempotent on pr.Key (= "reverse:<dispute>:<lot>"):
	// a re-record of an existing key is a no-op (it never resurrects a Done row nor resets
	// attempts), so a webhook redelivery is safe. The ledger clawback is already recorded
	// synchronously; this captures the money-rail intent so a transient Stripe failure is
	// retried instead of silently dropped.
	RecordPendingReversal(pr PendingReversal) error
	// OpenPendingReversals returns the reversals still owed (not Done, not dead-lettered),
	// up to limit (0 = all), oldest first, so the background sweep can re-attempt them.
	OpenPendingReversals(limit int) ([]PendingReversal, error)
	// MarkReversalAttempt records ONE reversal attempt's outcome for key: it bumps the
	// attempt count + last-attempt time, sets done=true on success (terminal), or records
	// the error and parks the row as a dead-letter once attempts reach maxAttempts (so it
	// stops being swept and is surfaced for manual handling). Idempotent per call.
	MarkReversalAttempt(key string, success bool, errMsg string, maxAttempts int, now time.Time) error

	// --- super-admin aggregates (admin.go) ---------------------------------
	//
	// Platform-wide rollups read ACROSS all accounts/wallets/lots/ledger. Every caller
	// is admin-gated (cmd/rogerai-broker/admin.go); these are the only un-account-scoped
	// reads in the store. Derived from the SAME source of truth the per-account views
	// use, so an admin overview never drifts from what an operator/consumer sees.

	// AdminFinancials sums the platform money rollup (consumer spend, operator earned,
	// platform fee, topup volume, the held/payable/paid/clawed lot lifecycle totals,
	// wallet+owner counts) as of `now`, promoting held->payable on read.
	AdminFinancials(now time.Time) (AdminFinancials, error)
	// AdminMarketTotals sums settled-request/token volume across every receipt, all-time
	// and within the trailing [since,until) window (receipt-derived, drift-free).
	AdminMarketTotals(since, until int64) (AdminMarketTotals, error)
	// AdminPayoutQueue returns every operator account's payable/held/paid/pending posture
	// as of `now`, sorted most-owed first, capped by limit (0 = all).
	AdminPayoutQueue(now time.Time, limit int) ([]AdminPayoutQueueRow, error)
	// AdminAllPayouts returns the most-recent payouts ACROSS all accounts, newest first,
	// capped by limit (the platform payout history).
	AdminAllPayouts(limit int) ([]Payout, error)
	// AdminAbuse rolls up platform safety state: banned owners (+ strike counts), struck
	// account + total strike counts, the CSAM report queue depth, report/dispute/ban
	// counts, and accounts currently under a recount hold.
	AdminAbuse() (AdminAbuse, error)
	// AdminActivity returns the most-recent ledger rows ACROSS every holder, newest
	// first (the platform-wide event stream), capped by limit.
	AdminActivity(limit int) ([]LedgerRow, error)

	// Healthy is a cheap liveness/readiness probe of the store backend: nil = reachable.
	// Mem always returns nil; Postgres pings the connection. Used by the /ready endpoint
	// so the load balancer only routes to a broker whose store is actually answering.
	Healthy() error

	Close() error
}

// Strike is one evidence-bound anti-abuse mark against an owner account. The
// evidence is provable (the operator's own node-signed claim vs the broker's recount
// / the empty body / the impossible byte-floor) so the operator can be SHOWN exactly
// why they were warned or banned (non-repudiable: node signature vs broker signature).
type Strike struct {
	ID        int64  `json:"id"`
	AccountID string `json:"account_id"` // owner pubkey (the durable identity)
	Kind      string `json:"kind"`       // impossible-input | empty-output | recount-discrepancy
	Evidence  string `json:"evidence"`   // JSON: claim-vs-billed, request id, axis, delta
	CreatedAt int64  `json:"created_at"`
}

// Strike kinds (the violation classes that accrue evidence + drive the owner ban).
const (
	StrikeImpossibleInput    = "impossible-input"    // claimed prompt tokens > body bytes (zero-doubt)
	StrikeEmptyOutput        = "empty-output"        // billed input but produced no usable output (voided)
	StrikeRecountDiscrepancy = "recount-discrepancy" // node over-reported past the recount tolerance
)

// Appeal is one operator-filed self-serve appeal against an anti-abuse action (a node
// report-ban and/or an account strike/ban). It is owner-scoped (AccountID is the
// authenticated owner pubkey, never a request-supplied account) and lands in the admin
// review queue. NodeID is optional (set when appealing a specific node ban).
type Appeal struct {
	ID        int64  `json:"id"`
	AccountID string `json:"account_id"` // owner pubkey (the authenticated caller)
	NodeID    string `json:"node_id,omitempty"`
	Reason    string `json:"reason"`         // the operator's note/evidence
	State     string `json:"state"`          // open | resolved
	Note      string `json:"note,omitempty"` // outcome note set on review (e.g. auto-exonerated)
	CreatedAt int64  `json:"created_at"`
}

// Appeal states.
const (
	AppealOpen     = "open"
	AppealResolved = "resolved"
)

// Owner is a monetizing account: a GitHub identity bound to the CLI's signing
// pubkey. Consumers never need one; it gates earning (priced node registration,
// future withdraws). Additive - the consume/wallet paths ignore it.
type Owner struct {
	GitHubID  int64  `json:"github_id"`
	Login     string `json:"login"`
	Pubkey    string `json:"pubkey"` // hex ed25519 user pubkey (the binding key)
	CreatedAt int64  `json:"created_at"`
	// Name is the GitHub display name captured at bind (may be empty if the user has
	// none). Used only to personalize the welcome email; never a security boundary.
	Name string `json:"name,omitempty"`
	// WelcomedAt is the unix time the one-time welcome email was sent (0 = never). It is
	// the durable idempotency guard for maybeSendWelcome: the welcome fires exactly once,
	// the first time the account has an email AND has not yet been welcomed.
	WelcomedAt int64 `json:"welcomed_at,omitempty"`
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
	seedRemain map[string]float64 // per-wallet UNSPENT seed (free) credits; drained first on spend
	earnings   map[string]float64
	spend      map[string]float64
	entries    []Entry
	processed  map[string]bool
	owners     map[string]Owner // keyed by pubkey
	policy     PayoutPolicy
	monthlyCap map[string]float64 // wallet -> explicit monthly spend cap ($); absent = env default

	ledger      []LedgerRow              // append-only money events
	ledgerID    int64                    // monotonic ledger id
	idem        map[string]bool          // ledger idem keys seen
	lots        []EarningLot             // operator earning lifecycle lots
	lotID       int64                    // monotonic lot id
	payouts     []Payout                 // payout history
	payoutID    int64                    // monotonic payout id
	disputes    map[string]bool          // seen stripe dispute ids (idempotency)
	settled     map[string]bool          // requestIDs already Settled/Finalized (idempotency: a 2nd settle is a no-op, no double-credit / lot drift)
	recountHold map[string]int64         // node id -> unix when the open L1 re-count hold was placed (holds promotion, P0-2; auto-expires)
	nodeAcct    map[string]string        // node id -> owner pubkey (TOFU)
	charges     map[string]charge        // stripe payment_intent/charge id -> checkout mapping
	gs          *grantStore              // grant keys + per-grant usage rollups
	bs          *bandStore               // private bands ("frequency codes": private discovery)
	nodes       map[string]NodeRecord    // persisted node registry (re-hydrated on restart)
	overrides   map[string]OfferOverride // owner-authored price/schedule overrides, keyed node\x00model

	// safety surfaces (safety.go): preserved CSAM incidents + the abuse/report log +
	// banned-node set. Rare, off the hot path; guarded by the same m.mu.
	csam     []CSAMIncident    // preserved child-exploitation hits (encrypted content)
	csamID   int64             // monotonic incident id
	reports  []Report          // abuse/quality reports (POST /report)
	reportID int64             // monotonic report id
	banned   map[string]string // node id -> ban reason (ejected from pick/market/discover)
	bannedAt map[string]int64  // node id -> unix when the ban was placed (report-ban auto-expiry)
	appeals  []Appeal          // owner-filed self-serve appeals (admin review queue)
	appealID int64             // monotonic appeal id

	// owner-keyed durable anti-abuse (anti-rotation): strikes carry provable evidence
	// bound to the OWNER ACCOUNT (owner pubkey), bannedOwners is the durable owner ban
	// set, accountHold holds ALL of an owner's lots from promotion. Rare, off the hot
	// path; guarded by m.mu.
	strikes      []Strike          // append-only evidence-bound owner strikes
	strikeID     int64             // monotonic strike id
	bannedOwners map[string]string // owner pubkey -> ban reason (durable, anti-rotation)
	accountHold  map[string]int64  // owner pubkey -> unix when all-lots hold was placed (auto-expires)

	// pendingReversals are the durable Stripe Transfer Reversal intents still owed on
	// disputed already-paid lots, keyed on "reverse:<dispute>:<lot>". The background
	// sweep retries each open row until it succeeds or dead-letters. Guarded by m.mu.
	pendingReversals map[string]PendingReversal

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
		wallet: map[string]float64{}, seedRemain: map[string]float64{}, earnings: map[string]float64{}, spend: map[string]float64{},
		processed: map[string]bool{}, owners: map[string]Owner{}, policy: LoadPayoutPolicy(),
		idem: map[string]bool{}, disputes: map[string]bool{}, settled: map[string]bool{}, recountHold: map[string]int64{}, nodeAcct: map[string]string{},
		charges: map[string]charge{}, gs: newGrantStore(), bs: newBandStore(), nodes: map[string]NodeRecord{},
		overrides: map[string]OfferOverride{},
		banned:    map[string]string{}, bannedAt: map[string]int64{}, bannedOwners: map[string]string{}, accountHold: map[string]int64{},
		pendingReversals: map[string]PendingReversal{},
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
		// ReserveReleaseAt is set EQUAL to ReleaseAt: the reserve (if any) releases together
		// with the lot, not on a later tail. promoteLocked + RequestPayout rely on this
		// coupling; a separate tail is unimplemented (see holdDuration / promoteLocked).
		ReleaseAt: rel.Unix(), ReserveReleaseAt: rel.Unix(), CreatedAt: now.Unix(),
	})
	m.appendLedgerLocked(acct, "operator", KindEarn, ownerShare, "earn:"+requestID, StatePending, requestID, now.Unix())
	if reserve > 0 {
		m.appendLedgerLocked(acct, "operator", KindReserveHold, -reserve, "reserve:"+requestID, StatePending, requestID, now.Unix())
	}
}

// SeedLotsForTest replaces the in-memory earning lots wholesale. It is a deliberate
// test seam (exported so cross-package handler tests in package main can stage lots
// with precise release dates the time.Now()-stamped Finalize path can't produce); it is
// never called in production.
func (m *Mem) SeedLotsForTest(lots []EarningLot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lots = append([]EarningLot(nil), lots...)
	for _, l := range lots {
		if l.ID > m.lotID {
			m.lotID = l.ID
		}
	}
}

func (m *Mem) SetSeedLimit(limit int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seedLimit = limit
}

// SeedStatus reports the in-memory seed accounting (the Mem twin of the Postgres
// seed_counter read). remaining is -1 when unlimited.
func (m *Mem) SeedStatus() (seeded, limit, remaining int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seeded, limit = m.seedCount, m.seedLimit
	if limit <= 0 {
		return seeded, limit, -1, nil
	}
	remaining = limit - seeded
	if remaining < 0 {
		remaining = 0
	}
	return seeded, limit, remaining, nil
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
	// Track the seed-funded portion of the balance separately so the earning path can
	// tell free (seed) spend from real (cleared-topup) spend: an operator must NOT be
	// able to mint a payable earning from another account's free seed credits (P0-1).
	if m.seedRemain == nil {
		m.seedRemain = map[string]float64{}
	}
	m.seedRemain[wallet] += seed
	m.seedCount++
	// Seed credits are a real balance, so they get a ledger row too (else the
	// re-derivation drift check would flag every seeded wallet). The idem key also
	// marks this wallet as seeded so neither seed path re-grants it.
	m.appendLedgerLocked(wallet, "consumer", KindAdjustment, seed, "seed:"+wallet, StatePosted, "seed", 0)
	return true
}

// consumeSeedLocked draws `cost` against the wallet's UNSPENT seed credits first and
// returns the portion funded by seed. Caller holds m.mu. Seed is spent BEFORE real
// (cleared-topup) credits so the operator earning path only accrues on the real
// remainder (seed-funded traffic earns the operator nothing - it is treated like a
// free request on the operator side). This does NOT change the consumer's spend.
func (m *Mem) consumeSeedLocked(wallet string, cost float64) float64 {
	if cost <= 0 {
		return 0
	}
	rem := m.seedRemain[wallet]
	if rem <= 0 {
		return 0
	}
	used := cost
	if used > rem {
		used = rem
	}
	m.seedRemain[wallet] = rem - used
	return used
}

// realEarnShare scales an owner share down to the REAL (non-seed) funded fraction of
// the cost: if part of the cost was paid from seed credits, that part earns the
// operator nothing. cost<=0 (free/self) earns nothing. Caller holds m.mu.
func (m *Mem) realEarnShare(wallet string, cost, ownerShare float64) float64 {
	if cost <= 0 || ownerShare <= 0 {
		return 0
	}
	seedUsed := m.consumeSeedLocked(wallet, cost)
	realFrac := (cost - seedUsed) / cost
	if realFrac <= 0 {
		return 0
	}
	return ownerShare * realFrac
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

// billedTokens returns the token counts to RECORD for a settled request: the broker's
// own re-count when present (nonzero), else the node's claimed count. Settlement bills
// (and earns) on these adjusted, platform-favoring numbers, so dashboards and clawback
// reflect the verified counts, not the node's unverified claim. The broker re-count is
// only ever <= the claim on each axis (we never inflate a claim), so this can only
// lower a count, never raise it.
func billedTokens(rec protocol.UsageReceipt) (promptTok, completionTok int) {
	promptTok = rec.PromptTokens
	if rec.BrokerPromptTokens > 0 && rec.BrokerPromptTokens < promptTok {
		promptTok = rec.BrokerPromptTokens
	}
	completionTok = rec.CompletionTokens
	if rec.BrokerCompletionTokens > 0 && rec.BrokerCompletionTokens < completionTok {
		completionTok = rec.BrokerCompletionTokens
	}
	return promptTok, completionTok
}

// appendAdjustLocked writes the KindAdjust AUDIT row when the broker billed less than
// the node claimed on EITHER axis - the audit trail the enforcement mandate requires.
// It records, for `requestID`, the claimed-vs-billed counts on BOTH axes and the dollar
// the platform (and consumer) saved by billing the lesser count. The money delta is 0
// (the consumer was already charged only the adjusted `cost`); the row exists purely as
// the provable, queryable record of the adjustment. Caller holds m.mu.
func (m *Mem) appendAdjustLocked(holder string, rec protocol.UsageReceipt, cost float64) {
	bpt, bct := billedTokens(rec)
	if bpt >= rec.PromptTokens && bct >= rec.CompletionTokens {
		return // no downward adjustment on either axis: nothing to audit
	}
	// $0 money delta (the consumer was already charged only the adjusted `cost`); the
	// row IS the audit trail. The Entry written by the caller carries the adjusted
	// (broker) counts, and the per-request strike evidence carries the full
	// claimed-vs-billed-vs-saved detail (saved = claimCost - cost, recorded there).
	m.appendLedgerLocked(holder, "consumer", KindAdjust, 0, "adjust:"+rec.RequestID, StatePosted, rec.RequestID, rec.TS)
}

func (m *Mem) Settle(user, node string, cost, ownerShare float64, rec protocol.UsageReceipt) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rec.RequestID != "" {
		if m.settled[rec.RequestID] {
			return m.wallet[user], nil // already settled: idempotent no-op (no double-debit / lot drift)
		}
		m.settled[rec.RequestID] = true
	}
	m.wallet[user] -= cost
	m.spend[user] += cost
	// Only the REAL (non-seed) funded portion of this cost earns the operator a payable
	// lot: free seed credits must never mint a payout (P0-1). consumeSeed is called
	// EXACTLY ONCE here, via realEarnShare. The consumer's spend (cost) is unchanged.
	earnShare := m.realEarnShare(user, cost, ownerShare)
	m.earnings[node] += earnShare
	bpt, bct := billedTokens(rec)
	m.entries = append(m.entries, Entry{
		RequestID: rec.RequestID, User: user, Node: node, Model: rec.Model,
		PromptTokens: bpt, CompletionTokens: bct,
		Cost: cost, OwnerShare: earnShare, TS: rec.TS,
	})
	m.appendLedgerLocked(user, "consumer", KindSpend, -cost, "spend:"+rec.RequestID, StatePosted, rec.RequestID, rec.TS)
	m.appendAdjustLocked(user, rec, cost)
	m.addLotLocked(node, rec.RequestID, earnShare, time.Now())
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
	if rec.RequestID != "" {
		if m.settled[rec.RequestID] {
			return m.wallet[user], nil // already settled: idempotent no-op (no double refund / lot drift)
		}
		m.settled[rec.RequestID] = true
	}
	m.wallet[user] += held - cost // refund the unused reservation
	m.spend[user] += cost
	// Only the REAL (non-seed) funded portion of this cost earns the operator a payable
	// lot (P0-1): seed-funded spend records the metering receipt but mints no earning.
	// consumeSeed runs EXACTLY ONCE here via realEarnShare. Consumer spend is unchanged.
	earnShare := m.realEarnShare(user, cost, ownerShare)
	m.earnings[node] += earnShare
	bpt, bct := billedTokens(rec)
	m.entries = append(m.entries, Entry{
		RequestID: rec.RequestID, User: user, Node: node, Model: rec.Model,
		PromptTokens: bpt, CompletionTokens: bct,
		Cost: cost, OwnerShare: earnShare, TS: rec.TS,
	})
	// Capture the hold into ledger: release the full reservation, then debit the
	// actual spend. Net wallet delta == held-cost, matching the cache above.
	m.appendLedgerLocked(user, "consumer", KindHoldRelease, held, "", StatePosted, rec.RequestID, rec.TS)
	m.appendLedgerLocked(user, "consumer", KindSpend, -cost, "spend:"+rec.RequestID, StatePosted, rec.RequestID, rec.TS)
	m.appendAdjustLocked(user, rec, cost)
	m.addLotLocked(node, rec.RequestID, earnShare, time.Now())
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

// windowed returns the entries matching pred whose ts is in [since,until), newest
// first. The window is half-open (the same convention the metrics rollups use).
func (m *Mem) windowed(pred func(Entry) bool, since, until int64) []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Entry
	for _, e := range m.entries {
		if e.TS < since || e.TS >= until {
			continue
		}
		if pred(e) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS > out[j].TS })
	return out
}

func (m *Mem) EntriesByUser(user string, since, until int64) ([]Entry, error) {
	return m.windowed(func(e Entry) bool { return e.User == user }, since, until), nil
}

func (m *Mem) EntriesByAccount(accountID string, since, until int64) ([]Entry, error) {
	m.mu.Lock()
	owned := map[string]bool{}
	for n, a := range m.nodeAcct {
		if a == accountID {
			owned[n] = true
		}
	}
	m.mu.Unlock()
	return m.windowed(func(e Entry) bool { return owned[e.Node] }, since, until), nil
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
		// Email: NEVER clobber a user-set email on re-login. GitHub only fills it when
		// the account has none on file yet (existing empty); a value the user set via
		// PATCH /account always wins over whatever GitHub hands us at the next login.
		if existing.Email != "" {
			o.Email = existing.Email
		}
		// Name: same fill-if-empty so a once-captured display name is stable across
		// logins (and a later GitHub name change doesn't silently overwrite it).
		if existing.Name != "" {
			o.Name = existing.Name
		}
		// preserve account-hub state a fresh GitHub login wouldn't carry
		o.WelcomedAt = existing.WelcomedAt // durable: the welcome fires exactly once, ever
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

// ClaimWelcome atomically stamps WelcomedAt=now for the owner IFF it is currently
// unset, reporting whether THIS call claimed it. It is the idempotency primitive behind
// maybeSendWelcome: with concurrent binds/patches racing, exactly one caller gets
// claimed=true (and therefore sends exactly one welcome email).
func (m *Mem) ClaimWelcome(pubkey string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.owners[pubkey]
	if !ok || o.WelcomedAt != 0 {
		return false, nil
	}
	o.WelcomedAt = time.Now().Unix()
	m.owners[pubkey] = o
	return true, nil
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

func (m *Mem) DeleteNode(nodeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.nodes, nodeID)
	return nil
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
// Caller holds m.mu. A lot on a node with an OPEN L1 re-count discrepancy
// (recountHold) is NOT promoted (P0-2): an over-reporting node's earnings stay held
// pending review instead of auto-promoting to payable on schedule.
func (m *Mem) promoteLocked(now time.Time) {
	for i := range m.lots {
		l := &m.lots[i]
		if _, held := m.recountHold[l.Node]; held {
			continue // node under re-count review: hold this lot, don't promote
		}
		if _, held := m.accountHold[l.AccountID]; held {
			continue // OWNER under review (survives node-id rotation): hold this lot
		}
		if l.State == LotHeld && now.Unix() >= l.ReleaseAt {
			l.State = LotPayable
			payable := l.Gross - l.Reserve
			if payable > 0 {
				m.appendLedgerLocked(l.AccountID, "operator", KindHoldRelease, 0, "promote:"+l.RequestID, StatePosted, l.RequestID, now.Unix())
			}
			// The reserve currently releases TOGETHER with the lot: addLotLocked sets
			// ReserveReleaseAt == ReleaseAt, so by the time a lot promotes its reserve is due
			// too. Emit the reserve_release audit row HERE, at the single promotion - not
			// behind a separate now>=ReserveReleaseAt gate. A promoted lot is never revisited
			// by this sweep (the LotHeld guard above), so a later reserve time would silently
			// drop this row; keeping it coupled to promotion means it is always recorded. A
			// real reserve TAIL (ReserveReleaseAt > ReleaseAt) is NOT implemented and would
			// also require RequestPayout to pay the reserve separately instead of marking the
			// lot fully paid - build both together if that policy is ever wanted.
			if l.Reserve > 0 {
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

func (m *Mem) SetNodeRecountHold(node string, held bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recountHold == nil {
		m.recountHold = map[string]int64{}
	}
	if held {
		// Record (or refresh) the held-at time. A re-flagged discrepancy re-arms the
		// auto-expiry window, so an actually-abusive node never ages out of its hold.
		m.recountHold[node] = time.Now().Unix()
	} else {
		delete(m.recountHold, node)
	}
	return nil
}

func (m *Mem) RecountHeldNodes() (map[string]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]bool, len(m.recountHold))
	for n := range m.recountHold {
		out[n] = true
	}
	return out, nil
}

// ExpireRecountHolds clears every node + account hold first placed at or before
// olderThan (auto-expiry recourse): an honest operator hit by a false-positive hold is
// unfrozen after the window, while an abusive one is kept held because every fresh
// discrepancy refreshes its held-at time above the cutoff. Returns the count cleared.
func (m *Mem) ExpireRecountHolds(olderThan time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cut := olderThan.Unix()
	n := 0
	for node, at := range m.recountHold {
		if at <= cut {
			delete(m.recountHold, node)
			n++
		}
	}
	for acct, at := range m.accountHold {
		if at <= cut {
			delete(m.accountHold, acct)
			n++
		}
	}
	return n, nil
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

// dayUTC returns the unix midnight (UTC) of the day containing the unix instant ts -
// the bucket key for the release ladder so lots clearing the same day group together.
func dayUTC(ts int64) int64 {
	t := time.Unix(ts, 0).UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).Unix()
}

func (m *Mem) ReleaseSchedule(accountID string, now time.Time) ([]ReleaseBucket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.promoteLocked(now) // sweep first: an already-cleared lot is no longer "upcoming"
	type agg struct {
		amount float64
		count  int
	}
	buckets := map[int64]*agg{}
	for _, l := range m.lots {
		if l.AccountID != accountID || l.State != LotHeld {
			continue
		}
		payable := l.Gross - l.Reserve
		if payable <= 0 {
			continue
		}
		key := dayUTC(l.ReleaseAt)
		b := buckets[key]
		if b == nil {
			b = &agg{}
			buckets[key] = b
		}
		b.amount += payable
		b.count++
	}
	out := make([]ReleaseBucket, 0, len(buckets))
	for day, b := range buckets {
		out = append(out, ReleaseBucket{Date: day, Amount: b.amount, LotCount: b.count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out, nil
}

func (m *Mem) EarningRollups(accountID string) (byModel, byNode []EarningRollup, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// request id -> model, from the receipts (the source of truth for the model served).
	modelOf := map[string]string{}
	for _, e := range m.entries {
		if e.RequestID != "" {
			modelOf[e.RequestID] = e.Model
		}
	}
	type agg struct {
		amount float64
		lots   int
	}
	mAgg := map[string]*agg{}
	nAgg := map[string]*agg{}
	bump := func(t map[string]*agg, key string, gross float64) {
		a := t[key]
		if a == nil {
			a = &agg{}
			t[key] = a
		}
		a.amount += gross
		a.lots++
	}
	for _, l := range m.lots {
		if l.AccountID != accountID || l.State == LotClawed {
			continue
		}
		bump(mAgg, modelOf[l.RequestID], l.Gross)
		bump(nAgg, l.Node, l.Gross)
	}
	flat := func(t map[string]*agg) []EarningRollup {
		out := make([]EarningRollup, 0, len(t))
		for k, a := range t {
			out = append(out, EarningRollup{Key: k, Amount: a.amount, Lots: a.lots})
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Amount != out[j].Amount {
				return out[i].Amount > out[j].Amount
			}
			return out[i].Key < out[j].Key
		})
		return out
	}
	return flat(mAgg), flat(nAgg), nil
}

func (m *Mem) PayoutLots(accountID string, payoutID int64) ([]PayoutLot, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Owner scope: the payout must belong to this account, else reject (no leak).
	found := false
	for _, p := range m.payouts {
		if p.ID == payoutID {
			if p.AccountID != accountID {
				return nil, false, nil
			}
			found = true
			break
		}
	}
	if !found {
		return nil, false, nil
	}
	modelOf := map[string]string{}
	for _, e := range m.entries {
		if e.RequestID != "" {
			modelOf[e.RequestID] = e.Model
		}
	}
	var out []PayoutLot
	for _, l := range m.lots {
		if l.PayoutID != payoutID {
			continue
		}
		out = append(out, PayoutLot{
			LotID: l.ID, RequestID: l.RequestID, Node: l.Node,
			Model: modelOf[l.RequestID], Gross: l.Gross, CreatedAt: l.CreatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].LotID > out[j].LotID
	})
	return out, true, nil
}

// Chargeback is the back-compat wrapper: it runs the lineage clawback and returns just
// the amount clawed from still-held/payable lots (the legacy return). It does NOT issue
// Stripe transfer reversals - callers that need to reverse already-paid lots must use
// ChargebackLineage and act on the returned Reversals.
func (m *Mem) Chargeback(disputeID, wallet, requestID string, amount float64, now time.Time) (float64, error) {
	res, err := m.ChargebackLineage(disputeID, wallet, requestID, amount, now)
	return res.Clawed, err
}

func (m *Mem) ChargebackLineage(disputeID, wallet, requestID string, amount float64, now time.Time) (ChargebackResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.disputes[disputeID] {
		return ChargebackResult{AlreadyHandled: true}, nil // idempotent on the stripe dispute id
	}
	m.disputes[disputeID] = true
	m.wallet[wallet] -= amount
	m.appendLedgerLocked(wallet, "consumer", KindChargeback, -amount, "dispute:"+disputeID, StatePosted, disputeID, now.Unix())

	// Lineage: target THIS consumer wallet's OWN lots (via the receipts/entries link),
	// never unrelated operators'. With an explicit requestID we target that one request
	// (precise path); otherwise the wallet's lots newest-first, capped at the disputed
	// amount. Already-clawed lots are skipped; held/payable AND paid lots are eligible
	// (a paid lot is reversed via Stripe rather than escaping the clawback).
	notClawed := func(l *EarningLot) bool { return l.State != LotClawed }
	// reqCost maps a request to the CONSUMER cost it was billed (entry.Cost), so the claw
	// loop can stop once the clawed lots cover the disputed amount in CONSUMER dollars - the
	// units `amount` is in. Stopping on operator GROSS instead would over-claw by a factor
	// of 1/(1-feeRate): clawing into lots funded by the consumer's OTHER (non-disputed)
	// top-ups and making an honest operator absorb the platform's fee. Empty for the
	// explicit-requestID path (which claws the one request and never caps on amount).
	reqCost := map[string]float64{}
	var order []int
	if requestID != "" {
		for i := range m.lots {
			if m.lots[i].RequestID == requestID && notClawed(&m.lots[i]) {
				order = append(order, i)
			}
		}
	} else {
		reqTS := map[string]int64{}
		for _, e := range m.entries {
			if e.User == wallet {
				reqTS[e.RequestID] = e.TS
				reqCost[e.RequestID] = e.Cost
			}
		}
		for i := range m.lots {
			if _, ok := reqTS[m.lots[i].RequestID]; ok && notClawed(&m.lots[i]) {
				order = append(order, i)
			}
		}
		sort.SliceStable(order, func(a, b int) bool {
			return reqTS[m.lots[order[a]].RequestID] > reqTS[m.lots[order[b]].RequestID]
		})
	}

	// transfer id a paid lot was paid out on (for the reversal).
	transferOf := func(payoutID int64) string {
		for _, p := range m.payouts {
			if p.ID == payoutID {
				return p.StripeTransferID
			}
		}
		return ""
	}

	var res ChargebackResult
	recovered := 0.0    // operator GROSS clawed/reversed - what is actually recovered from operators
	remaining := amount // CONSUMER cost still to recover (wallet-recency path); caps the claw
	for _, i := range order {
		if requestID == "" && remaining <= 1e-9 {
			break
		}
		l := &m.lots[i]
		// PRO-RATA on the lot that would overshoot: if this lot's consumer cost exceeds the
		// disputed cost still remaining, recover only the operator's PROPORTIONAL share
		// (gross * remaining/cost) so the operator is NEVER clawed beyond the disputed
		// amount; the rest of the lot stays theirs. A full dispute claws whole lots (frac=1)
		// exactly as before. Explicit-requestID path always claws whole (no amount cap).
		frac := 1.0
		cost := reqCost[l.RequestID]
		if requestID == "" && cost > 0 && cost > remaining {
			frac = remaining / cost
		}
		clawGross := l.Gross * frac
		switch l.State {
		case LotPaid:
			// Already paid out: reverse the (proportional) operator share via Stripe (6.4
			// step 4) + a payout_reversed ledger row.
			m.appendLedgerLocked(l.AccountID, "operator", KindPayoutReversed, -clawGross, "reverse:"+disputeID+":"+l.RequestID, StatePosted, disputeID, now.Unix())
			res.Reversals = append(res.Reversals, Reversal{
				DisputeID: disputeID, LotID: l.ID, AccountID: l.AccountID,
				TransferID: transferOf(l.PayoutID), Amount: clawGross,
			})
		default: // held / payable: claw in place, no Stripe action.
			m.appendLedgerLocked(l.AccountID, "operator", KindAdjustment, -clawGross, "claw:"+disputeID+":"+l.RequestID, StatePosted, disputeID, now.Unix())
			res.Clawed += clawGross
		}
		recovered += clawGross
		if frac >= 1.0 {
			l.State = LotClawed
		} else {
			// Partial claw: keep the lot, reduce its gross + reserve by the clawed fraction.
			l.Gross -= clawGross
			l.Reserve -= l.Reserve * frac
		}
		remaining -= cost * frac // == min(cost, remaining); reaches 0 on the partial lot
	}

	// Any disputed amount NOT covered by this consumer's lots is a PLATFORM LOSS - the
	// platform eats it rather than clawing unrelated, honest operators' earnings.
	if remainder := amount - recovered; remainder > 1e-9 {
		res.PlatformLoss = remainder
		m.appendLedgerLocked("platform", "platform", KindPlatformLoss, -remainder, "loss:"+disputeID, StatePosted, disputeID, now.Unix())
	}
	return res, nil
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

// Healthy is always nil for the in-memory store (no backend to be unreachable).
func (m *Mem) Healthy() error { return nil }

// RecordPendingReversal durably records a Stripe Transfer Reversal intent. Idempotent
// on pr.Key: a re-record of an existing key is a no-op (never resurrects a Done row nor
// resets attempts), so a webhook redelivery is safe.
func (m *Mem) RecordPendingReversal(pr PendingReversal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingReversals == nil {
		m.pendingReversals = map[string]PendingReversal{}
	}
	if pr.Key == "" {
		return nil
	}
	if _, ok := m.pendingReversals[pr.Key]; ok {
		return nil // already recorded; do not reset attempts/done
	}
	if pr.CreatedAt == 0 {
		pr.CreatedAt = time.Now().Unix()
	}
	m.pendingReversals[pr.Key] = pr
	return nil
}

// OpenPendingReversals returns reversals still owed (not Done, not dead-lettered),
// oldest first, capped at limit (0 = all).
func (m *Mem) OpenPendingReversals(limit int) ([]PendingReversal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []PendingReversal
	for _, pr := range m.pendingReversals {
		if pr.Done || pr.DeadLetter {
			continue
		}
		out = append(out, pr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// MarkReversalAttempt records one reversal attempt outcome for key: bump attempts +
// last-attempt, mark done on success, or record the error and dead-letter once attempts
// reach maxAttempts. A no-op if the key is unknown or already terminal.
func (m *Mem) MarkReversalAttempt(key string, success bool, errMsg string, maxAttempts int, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	pr, ok := m.pendingReversals[key]
	if !ok || pr.Done || pr.DeadLetter {
		return nil
	}
	pr.Attempts++
	pr.LastAttempt = now.Unix()
	if success {
		pr.Done = true
		pr.LastError = ""
	} else {
		pr.LastError = errMsg
		if maxAttempts > 0 && pr.Attempts >= maxAttempts {
			pr.DeadLetter = true
		}
	}
	m.pendingReversals[key] = pr
	return nil
}
