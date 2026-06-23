package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/bownux/rogerai/internal/store"
)

// balance handles GET /balance: the caller's wallet credits (seeds new users).
func (b *broker) balance(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	user := userOf(r)
	bal, _ := b.db.BalanceOf(user, b.seedFunds)
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "balance": bal})
}

// me handles GET /me: the caller's consumer dashboard - wallet balance, lifetime
// spend, and recent settled requests (newest first). `limit` query caps history
// (default 20, max 100).
func (b *broker) me(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	user := userOf(r)
	bal, _ := b.db.BalanceOf(user, b.seedFunds)
	spend, _ := b.db.SpendOf(user)
	recent, _ := b.db.RecentByUser(user, recentLimit(r))
	if recent == nil {
		recent = []store.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":    user,
		"balance": round6(bal),
		"spend":   round6(spend),
		"recent":  recent,
	})
}

// earnings handles GET /earnings?node=<id>: a node owner's dashboard - accrued
// (unpaid) owner credits and recent settled requests for that node. The node id
// is the source of truth (a serving node knows its own id).
func (b *broker) earnings(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
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
	writeJSON(w, http.StatusOK, map[string]any{
		"node":     node,
		"online":   online,
		"earnings": round6(accrued),
		"recent":   recent,
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
