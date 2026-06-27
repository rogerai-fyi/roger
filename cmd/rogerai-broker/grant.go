package main

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// grantPrefix is the visible marker of a grant bearer token. The broker detects
// it before the signed-identity path so a grant authenticates by its owner-minted
// secret (no login, no signature) - see relay + GRANT-KEYS-DESIGN section 2.1.
const grantPrefix = "rog-grant_"

// grantContext is the resolved state for a grant request: the grant, its
// grant-scoped wallet id ("g_<id>"), and the set of the owner's nodes this grant
// may reach (NodesOfAccount(owner) intersected with the grant's node allow-list).
type grantContext struct {
	grant     store.Grant
	wallet    string          // "g_<id>" - reservedID-protected, server-side only
	nodeAllow map[string]bool // candidate nodes (owner's nodes ∩ grant.Nodes)
}

// modelDenied reports whether the grant restricts models and req is not allowed.
func (gc grantContext) modelDenied(model string) bool {
	if len(gc.grant.Models) == 0 {
		return false // empty = any model the nodes offer
	}
	for _, m := range gc.grant.Models {
		if m == model {
			return false
		}
	}
	return true
}

// grantTokenFromHeader extracts a `Bearer rog-grant_...` secret, or "" if the
// Authorization header is absent or not a grant token.
func grantTokenFromHeader(r *http.Request) string {
	a := r.Header.Get("Authorization")
	if len(a) > 7 && a[:7] == "Bearer " {
		tok := a[7:]
		if strings.HasPrefix(tok, grantPrefix) {
			return tok
		}
	}
	return ""
}

// resolveGrant detects + validates a grant bearer token on r. It returns:
//
//	gc  - the resolved grant context (only meaningful when ok)
//	ok  - true when the request carried a valid, live grant token
//	err - a non-empty 401 message when a grant token was PRESENT but invalid
//	      (unknown / revoked / expired); the caller rejects with it
//
// A request with no grant token returns (zero, false, "") so the caller falls
// through to the normal signed path.
func (b *broker) resolveGrant(r *http.Request) (gc grantContext, ok bool, err string) {
	tok := grantTokenFromHeader(r)
	if tok == "" {
		return grantContext{}, false, ""
	}
	return b.resolveGrantToken(tok)
}

// resolveGrantToken is the core grant resolution shared by the HTTP path
// (resolveGrant) and the concierge dogfood path (which authenticates from the
// CONCIERGE_GRANT_KEY env secret, not a request header). It maps the raw
// `rog-grant_...` secret to its stored grant by sha256 and builds the same grant
// context (grant + grant-scoped wallet + owner nodeAllow) the relay would. The
// caller must have already established that tok is non-empty.
func (b *broker) resolveGrantToken(tok string) (gc grantContext, ok bool, err string) {
	sum := sha256.Sum256([]byte(tok))
	g, found, gerr := b.db.GrantBySecretHash(hex.EncodeToString(sum[:]))
	if gerr != nil {
		return grantContext{}, false, "grant lookup failed"
	}
	if !found || g.Revoked {
		return grantContext{}, false, "grant key invalid or revoked"
	}
	if g.Expired(time.Now()) {
		return grantContext{}, false, "grant key expired"
	}
	// Candidate nodes: the issuing owner's nodes, intersected with the grant's node
	// allow-list (empty list = all of the owner's nodes). Derived server-side, so a
	// grant can never name or reach a node its owner does not own.
	ownerNodes, _ := b.db.NodesOfAccount(g.Owner)
	allow := map[string]bool{}
	restrict := map[string]bool{}
	for _, n := range g.Nodes {
		restrict[n] = true
	}
	for _, n := range ownerNodes {
		if len(restrict) == 0 || restrict[n] {
			allow[n] = true
		}
	}
	return grantContext{grant: g, wallet: "g_" + g.ID, nodeAllow: allow}, true, ""
}

// grantCapCheck enforces the grant's daily/monthly token caps before dispatch.
// Returns (0, "") when within caps, else a 429 status + message.
func (b *broker) grantCapCheck(g store.Grant) (int, string) {
	if g.DailyCap == 0 && g.MonthlyCap == 0 {
		return 0, ""
	}
	u, err := b.db.GrantUsageOf(g.ID, time.Now())
	if err != nil {
		// FAIL CLOSED: a usage-read error must NOT silently uncap a capped grant - that
		// turned a capped grant into free unlimited service on any Postgres bucket-read
		// hiccup. Mirror the monthly spend cap's fail-closed posture: reject until usage is
		// readable again. (Only reached when the grant HAS a cap; uncapped grants returned
		// above without a read.)
		return http.StatusTooManyRequests, "grant cap check unavailable - try again shortly"
	}
	if g.DailyCap > 0 && u.DayTokens >= g.DailyCap {
		return http.StatusTooManyRequests, "grant daily token cap reached"
	}
	if g.MonthlyCap > 0 && u.MonthTokens >= g.MonthlyCap {
		return http.StatusTooManyRequests, "grant monthly token cap reached"
	}
	return 0, ""
}

// pricingPlan is the resolved billing decision for a request.
type pricingPlan struct {
	payer string  // the wallet to charge (owner wallet for a sponsored grant, else `user`)
	in    float64 // billed input price ($/1M)
	out   float64 // billed output price ($/1M)
	free  bool    // true => $0, metering-only (no hold, no ledger money rows)
	fixed bool    // true => use (in,out) as-is (grant/self); false => market price + lockWin
}

// streamBill carries the billing context into relayStream (keeps its signature
// from sprawling).
type streamBill struct {
	user string // the wallet to charge / refund (the payer)
	// consumer is the SIGNED consumer identity used as the price-LOCK key, kept distinct
	// from `user` (the payer wallet): for a logged-in caller the payer is the unified
	// "u_gh_<id>" wallet while the lock keys on the pubkey-derived signed id, exactly as
	// the non-stream relay does (lockedPrice(user,...)). Keying the lock on the payer
	// wallet here instead would mint a SEPARATE lock from the non-stream path, so an
	// owner's mid-engagement price hike would not be held back on the streaming path.
	consumer string
	model    string
	pricing  pricingPlan
	grantID  string
}

// resolvePricing decides who pays and at what price for one request:
//
//   - grant, free or self  -> $0, metering-only, fixed.
//   - grant, custom-priced -> the grant's price, billed to the OWNER's consumer
//     wallet (house-account sponsorship, GRANT-KEYS-DESIGN section 3.2A), fixed.
//   - signed self-use      -> $0 when the caller-owner owns the picked node
//     (identity-match self-use, section 3.4.1), fixed.
//   - public market        -> the offer's active price billed to `user`; NOT fixed
//     (the relay applies the price-lock window).
//
// `user` is the signed pubkey-derived identity (used for the self-use ownership
// match); `wallet` is the resolved MONEY key (the github-scoped wallet when the
// caller is logged in, else the same pubkey-derived id). They differ only for a
// logged-in user, so self-use still keys on the pubkey while public spend bills the
// unified account wallet.
func (b *broker) resolvePricing(gc grantContext, gok bool, user, wallet string, node protocol.NodeRegistration, offer protocol.ModelOffer) pricingPlan {
	if gok {
		in, out := gc.grant.GrantPrice()
		if in == 0 && out == 0 {
			return pricingPlan{payer: gc.wallet, free: true, fixed: true}
		}
		// Custom-priced grant: the owner sponsors it from their UNIFIED account wallet,
		// so sponsored spend draws the same balance they top up and is bound by the same
		// account monthly cap as their own use (one ceiling over everything they pay for).
		return pricingPlan{payer: b.ownerSponsorWallet(gc.grant.Owner), in: in, out: out, fixed: true}
	}
	// Signed self-use: consuming your OWN node is $0, automatically (metering only). W1:
	// the node->owner-account binding is an immutable TOFU mapping, so cache it behind the
	// flag to drop the per-request point read; Postgres stays authoritative on a miss/flag-
	// off, and BindNode invalidates the entry.
	if acct, ok := b.cachedAccountOfNode(node.NodeID, func() (string, bool) {
		a, ok, _ := b.db.AccountOfNode(node.NodeID)
		return a, ok
	}); ok && b.ownsNode(user, acct) {
		return pricingPlan{payer: wallet, free: true, fixed: true}
	}
	// Public market: the relay applies the active price + price-lock window itself
	// (fixed=false), so we only need to name the payer (the unified account wallet).
	_ = offer
	return pricingPlan{payer: wallet}
}

// ownsNode reports whether the signed consumer `user` is the owner account that
// owns the node (`acct` is the owner pubkey). A consumer's wallet id is derived
// from their pubkey; the owner account id IS that pubkey, so self-use is the case
// where the request's pubkey-derived wallet matches the node's owner pubkey.
func (b *broker) ownsNode(user, ownerPubkey string) bool {
	if user == "" || ownerPubkey == "" {
		return false
	}
	return user == protocol.UserIDFromPubkey(ownerPubkey)
}

// ownerSponsorWallet resolves the wallet a sponsored (custom-priced) grant bills:
// the issuing owner's UNIFIED account wallet ("u_gh_<githubID>") when the owner is
// GitHub-linked. This is the same wallet the owner tops up and that /balance + the
// billing dashboard read, so sponsored grant spend (a) draws the owner's real
// balance and (b) counts against the SAME monthly spend cap as the owner's own
// paid use - one ceiling over everything they pay for. Reuses the per-pubkey wallet
// cache that walletOf populates. Falls back to the pubkey-derived wallet when the
// owner has no GitHub link (no unified wallet exists for them).
func (b *broker) ownerSponsorWallet(ownerPubkey string) string {
	if ownerPubkey == "" {
		return ""
	}
	if w, ok := b.cachedOwnerWallet(ownerPubkey, func() (string, bool) {
		if o, ok, err := b.db.OwnerByPubkey(ownerPubkey); err == nil && ok && !o.Anonymized && o.GitHubID != 0 {
			return "u_gh_" + strconv.FormatInt(o.GitHubID, 10), true
		}
		return "", false
	}); ok {
		return w
	}
	return ownerWallet(ownerPubkey)
}

// ownerWallet is the owner's pubkey-derived wallet id - the fallback sponsor wallet
// for an owner with no GitHub-linked account (see ownerSponsorWallet).
func ownerWallet(ownerPubkey string) string {
	return protocol.UserIDFromPubkey(ownerPubkey)
}

// settleRequest captures a request, choosing the money path by `free`. Free/self
// requests are metering-only: they record the receipt + bump grant usage but write
// no ledger money rows (the "never pay yourself" / "free grant costs nobody" rule).
// Priced requests run the normal Hold/Finalize capture. It always increments the
// grant usage rollup (when grantID is set) so caps + the dashboard stay accurate.
func (b *broker) settleRequest(payer, node string, held, cost float64, rec protocol.UsageReceipt, grantID string, free bool) (float64, error) {
	now := time.Now()
	if grantID != "" {
		_ = b.db.AddGrantUsage(grantID, int64(rec.PromptTokens+rec.CompletionTokens), now)
	}
	if free {
		// Metering only: record the receipt (Settle at cost 0, ownerShare 0 writes no
		// earning lot and a $0 spend row) so the owner still sees usage.
		return b.db.Settle(payer, node, 0, 0, rec)
	}
	// Settle-time owner-ban backstop (anti-rotation): if the node's owner was banned
	// between pick and settle, the consumer is still billed for the output they received,
	// but the banned operator mints NO earning (ownerShare 0 -> no lot). The pick filter
	// is the primary gate; this closes the in-flight race so a banned owner can't earn on
	// a request already in progress.
	ownerShare := cost * (1 - b.feeRate)
	if b.nodeOwnerBanned(node) {
		log.Printf("settle: node=%s owner BANNED - billing consumer but minting NO earning", node)
		ownerShare = 0
	}
	bal, ferr := b.db.Finalize(payer, node, held, cost, ownerShare, rec)
	if ferr == nil && cost > 0 {
		// W2b: keep the monthly-spend fast-path counter current with the captured spend.
		// The ledger row Finalize just wrote is the source of truth; this is a best-effort
		// accelerator (a failed/absent increment is reconciled from the ledger SUM on the
		// next cap read, fail-closed). Off when the flag is off / cost is $0.
		b.recordMonthSpend(payer, cost, now)
	}
	return bal, ferr
}
