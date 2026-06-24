package main

import (
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
	bal, _ := b.db.BalanceOf(user, b.seedFunds)
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "balance": bal})
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
	bal, _ := b.db.BalanceOf(user, b.seedFunds)
	spend, _ := b.db.SpendOf(user)
	recent, _ := b.db.RecentByUser(user, recentLimit(r))
	if recent == nil {
		recent = []store.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":         user,
		"github_login": login, // "" for a signed-CLI read; set for a logged-in browser
		"balance":      round6(bal),
		"spend":        round6(spend),
		"recent":       recent,
	})
}

// earnings handles GET /earnings?node=<id>: a node owner's dashboard - accrued
// (unpaid) owner credits and recent settled requests for that node. The node id
// is the source of truth (a serving node knows its own id).
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
	accrued, _ := b.db.EarningsOf(node)
	recent, _ := b.db.RecentByNode(node, recentLimit(r))
	if recent == nil {
		recent = []store.Entry{}
	}
	b.mu.Lock()
	online := time.Since(b.lastSeen[node]) < 35*time.Second
	b.mu.Unlock()
	login, _, _ := b.webSession(r)
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
