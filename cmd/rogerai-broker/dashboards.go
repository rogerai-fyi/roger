package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// balance handles GET /balance: the caller's wallet credits (seeds new users).
// Identity comes from a signed request OR a logged-in browser session cookie.
func (b *broker) balance(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	user, ok := b.dashIdentity(r)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "invalid request signature")
		return
	}
	// A logged-in caller (github-scoped wallet) has a real balance; an anonymous
	// keypair has NO wallet/balance - free models + grant keys only. We never seed an
	// anonymous wallet, and we tell the client it is not logged in so the CLI/TUI can
	// say "log in to use your wallet" instead of showing a bogus 0.
	if !walletLoggedIn(user) {
		writeJSON(w, http.StatusOK, map[string]any{"user": user, "logged_in": false})
		return
	}
	bal, _ := b.db.BalanceOf(user, b.seedFunds)
	cap := b.monthlyCapState(user, time.Now())
	setCapHeaders(w, cap)
	writeJSON(w, http.StatusOK, map[string]any{
		"user": user, "balance": bal, "logged_in": true,
		// Monthly spend cap (a budget limit): the per-account ceiling + month-to-date
		// captured spend, so `roger balance` + the TUI show "MTD vs cap" (0 cap =
		// unlimited, the opt-in default).
		"monthly_cap":   round6(cap.cap),
		"monthly_spend": round6(cap.spend),
	})
}

// accountLimit handles GET/PATCH /account/limit: read or set the per-account MONTHLY
// SPEND CAP ($ ceiling per calendar month; 0 = unlimited). Per GitHub-linked wallet,
// so it REQUIRES a signed/logged-in identity (an anonymous keypair has no wallet to
// cap). GET returns the cap + month-to-date spend; PATCH {"monthly_cap": X} sets it
// (0 / negative = clear to unlimited).
func (b *broker) accountLimit(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPatch {
		w.Header().Set("Allow", "GET, PATCH, OPTIONS")
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	corsCreds(w, r)
	// Read the body BEFORE resolving identity: a signed PATCH's Ed25519 signature
	// covers the body, so the verify must see the same bytes (a GET sends none).
	var body []byte
	if r.Method == http.MethodPatch {
		body, _ = io.ReadAll(io.LimitReader(r.Body, 1<<12))
	}
	user, ok := b.dashIdentityBody(r, body)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "invalid request signature")
		return
	}
	if !walletLoggedIn(user) {
		jsonErr(w, http.StatusUnauthorized, "log in to set a monthly spend limit - run `roger login` (the cap is per account)")
		return
	}
	if r.Method == http.MethodPatch {
		var req struct {
			MonthlyCap *float64 `json:"monthly_cap"`
		}
		_ = json.Unmarshal(body, &req)
		if req.MonthlyCap == nil {
			jsonErr(w, http.StatusBadRequest, "missing monthly_cap")
			return
		}
		cap := *req.MonthlyCap
		if cap < 0 {
			cap = 0
		}
		if err := b.db.SetMonthlyCap(user, cap); err != nil {
			jsonErr(w, http.StatusInternalServerError, "store error")
			return
		}
	}
	st := b.monthlyCapState(user, time.Now())
	writeJSON(w, http.StatusOK, map[string]any{
		"monthly_cap":   round6(st.cap),
		"monthly_spend": round6(st.spend),
	})
}

// walletLoggedIn reports whether a resolved wallet id belongs to a logged-in
// account (the "u_gh_" / "u_apple_" namespaces, which back a real balance) versus
// an anonymous pubkey-derived id (no wallet by design). This gates the dashboard
// balance path; grant keys authenticate on the relay path, not this dashboard.
func walletLoggedIn(wallet string) bool {
	return isAccountWallet(wallet)
}

// me handles GET /me: the caller's consumer dashboard - wallet balance, lifetime
// spend, and recent settled requests (newest first). `limit` query caps history
// (default 20, max 100).
func (b *broker) me(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	login, _, _ := b.webSession(r)
	user, ok := b.dashIdentity(r)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "invalid request signature")
		return
	}
	// An anonymous (unbound) keypair has no wallet: report logged_in=false and no
	// balance/spend, so the client surfaces "log in to use your wallet" rather than a
	// seeded-looking 0. A logged-in caller reads the github-scoped wallet.
	if !walletLoggedIn(user) {
		writeJSON(w, http.StatusOK, map[string]any{
			"user": user, "logged_in": false, "recent": []store.Entry{},
		})
		return
	}
	bal, _ := b.db.BalanceOf(user, b.seedFunds)
	spend, _ := b.db.SpendOf(user)
	recent, _ := b.db.RecentByUser(user, recentLimit(r))
	if recent == nil {
		recent = []store.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":         user,
		"github_login": login, // "" for a signed-CLI read; set for a logged-in browser
		"logged_in":    true,
		"providers":    b.linkedProviders(r, login), // ["github"], ["apple"], or both - for the app's link-another-sign-in UI
		"balance":      round6(bal),
		"spend":        round6(spend),
		"recent":       recent,
	})
}

// linkedProviders reports which sign-in providers this account has linked ("github" and/or
// "apple"), so the app's "Link another sign-in" can show a check on the linked one and target
// the missing one. Provider-agnostic: it resolves the owner by the request's SIGNING PUBKEY
// (the iOS app + CLI path, which works for a github-only, apple-only, or dual-linked account)
// and falls back to the web-session github login for a browser. Order is stable (github, apple).
func (b *broker) linkedProviders(r *http.Request, login string) []string {
	o, ok := b.requireOwner(r)
	if !ok && login != "" {
		o, _, _ = b.db.OwnerByLogin(login)
	}
	provs := []string{}
	if o.GitHubID != 0 {
		provs = append(provs, "github")
	}
	if o.AppleSub != "" {
		provs = append(provs, "apple")
	}
	return provs
}

// earnings handles GET /earnings?node=<id>: a node owner's dashboard - accrued
// (unpaid) owner credits and recent settled requests for that node.
//
// AUTHENTICATED (ACCOUNT-PAYOUTS-DESIGN section 6.7 / AUTH-DESIGN section 3): node
// ids are PUBLIC (they appear in the market view + receipts), so this endpoint is NOT
// a public read - it would leak any operator's earnings and customer history. The
// caller MUST be the OWNER of the node: we resolve the signed/session owner via the
// same payoutOwner path the rest of the payout surface uses, then require the
// node->owner binding (AccountOfNode) to name that owner's pubkey. An unauthenticated
// request gets 401; an authenticated request for a node it does not own gets 403.
func (b *broker) earnings(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	node := r.URL.Query().Get("node")
	if node == "" {
		jsonErr(w, http.StatusBadRequest, "node query param required")
		return
	}
	// Resolve the authenticated owner (web session cookie OR signed CLI request). A GET
	// carries no body, so the signature is verified over nil (matching payoutOwner).
	login, o, ok := b.payoutOwner(r, nil)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in - run `roger login` to view earnings")
		return
	}
	if o.GitHubID == 0 {
		jsonErr(w, http.StatusForbidden, "no operator account for this login")
		return
	}
	// Ownership gate: the node must be bound to THIS owner's account (pubkey). Node ids
	// are public, so without this an operator could read any node's earnings + customer
	// activity. A node with no binding, or bound to a different account, is 403.
	acct, bound, _ := b.db.AccountOfNode(node)
	if !bound || acct != o.Pubkey {
		jsonErr(w, http.StatusForbidden, "you do not own this node")
		return
	}
	accrued, _ := b.db.EarningsOf(node)
	recent, _ := b.db.RecentByNode(node, recentLimit(r))
	if recent == nil {
		recent = []store.Entry{}
	}
	b.mu.Lock()
	online := time.Since(b.lastSeen[node]) < nodeTTL
	b.mu.Unlock()
	// Earnings lifecycle split (held -> reserved -> payable -> paid) for this node,
	// promoting any lots whose hold has cleared as of now (sweep-on-read).
	split, _ := b.db.EarningSplitOfNode(node, time.Now())
	writeJSON(w, http.StatusOK, map[string]any{
		"node":         node,
		"online":       online,
		"earnings":     round6(accrued), // legacy accrued counter (unchanged)
		"held":         round6(split.Held),
		"reserved":     round6(split.Reserved),
		"payable":      round6(split.Payable),
		"paid":         round6(split.Paid),
		"next_release": split.NextRelease,
		"recent":       recent,
		"github_login": login, // "" unless read by a logged-in browser
	})
}

// recentLimit reads the `limit` query param, clamped to [1,100] with a default of 20.
func recentLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return 20
	}
	if n > 100 {
		return 100
	}
	return n
}
