package main

import (
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// This file is the account-hub money views (ACCOUNT-PAYOUTS-DESIGN sections 2,4,5,7):
// data export, account deletion (soft-delete + anonymize, retention-safe), the
// /billing money-in view, and the /usage consumer-spend view. All are thin reads
// over the ledger/receipts behind the signed session cookie.

// accountExport handles POST /account/export: a GDPR/CCPA data dump (profile +
// ledger + receipts) as JSON for the logged-in account.
func (b *broker) accountExport(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	login, gid, wallet, ok := b.sessionOwner(r)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in")
		return
	}
	dump := map[string]any{
		"exported_at":  time.Now().Unix(),
		"github_login": login,
		"github_id":    gid,
		"wallet":       wallet,
	}
	if o, found, _ := b.db.OwnerByLogin(login); found {
		dump["email"] = o.Email
		dump["created_at"] = o.CreatedAt
		dump["connect_status"] = o.ConnectStatus
		if led, err := b.db.LedgerOf(o.Pubkey, nil, 10000); err == nil {
			dump["operator_ledger"] = nonNilLedger(led)
		}
		if pays, err := b.db.PayoutsOf(o.Pubkey, 1000); err == nil {
			dump["payouts"] = pays
		}
	}
	if led, err := b.db.LedgerOf(wallet, nil, 10000); err == nil {
		dump["consumer_ledger"] = nonNilLedger(led)
	}
	if rec, err := b.db.RecentByUser(wallet, 10000); err == nil {
		if rec == nil {
			rec = []store.Entry{}
		}
		dump["receipts"] = rec
	}
	w.Header().Set("Content-Disposition", `attachment; filename="rogerai-export.json"`)
	writeJSON(w, http.StatusOK, dump)
}

// accountDelete handles POST /account/delete: soft-delete + anonymize. BLOCKS when
// the account still holds a positive consumer balance, unswept operator earnings,
// or open disputes (the user must resolve those first). Financial rows are retained
// (de-identified) for the legal retention window; identity is scrubbed.
func (b *broker) accountDelete(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	// Accept EITHER the web session cookie OR a signed device-key request (so the native app
	// can delete in-app — App Store §5.1.1(v) — not just the web console). The signed body is
	// read first so the Ed25519 signature verifies over the same bytes (a delete POST may sign
	// an empty body).
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<12))
	login, wallet, ok := b.deleteIdentity(r, body)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in")
		return
	}
	if login == "" {
		// A bound account with no web login (e.g. Apple-only) — DeleteAccount keys on login, and
		// deleting by an empty login would match the wrong row. Direct them to the web for now.
		jsonErr(w, http.StatusConflict, "this account can't be deleted in-app yet — delete it from rogerai.fyi")
		return
	}
	// Guard 1: positive consumer balance must be spent/withdrawn first.
	if bal, _ := b.db.BalanceOf(wallet, 0); bal > 1e-6 {
		jsonErr(w, http.StatusConflict, "resolve your wallet balance before deleting (balance > 0)")
		return
	}
	// Guard 2: operator earnings + open disputes (only if this login is an operator).
	if o, found, _ := b.db.OwnerByLogin(login); found {
		if split, err := b.db.EarningSplitOf(o.Pubkey, time.Now()); err == nil {
			if split.Held+split.Reserved+split.Payable > 1e-6 {
				jsonErr(w, http.StatusConflict, "you have held/reserved/payable earnings - withdraw or forfeit them before deleting")
				return
			}
		}
		if n, _ := b.db.OpenDisputeCount(o.Pubkey); n > 0 {
			jsonErr(w, http.StatusConflict, "you have open disputes - they must close before deleting")
			return
		}
	}
	done, err := b.db.DeleteAccount(login)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	// Remote-control: end + revoke every live session for this wallet (BASE STATION), so a
	// deleted account leaves no attachable session or valid attach token behind.
	if list, lerr := b.db.RCSessionsByOwner(wallet); lerr == nil {
		for _, s := range list {
			if s.Active() {
				b.rcEndSession(s.ID)
			}
		}
	}
	_, _ = b.db.RevokeRCSessions(wallet)
	// Revoke the web session regardless (so the now-anonymized account can't be read).
	// Clear BOTH the session cookie and the signed-in hint - otherwise the deleted user's
	// browser keeps the stale roger_signed_in flag and goes on probing /account (401).
	clearWebSessionCookies(w)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": done})
}

// deleteIdentity resolves who is deleting: the web session cookie OR a signed device-key request
// bound to a non-anonymized owner. Returns the owner's login (the DeleteAccount key — empty for an
// account with no web login) and account wallet. ok=false when neither auth is usable. This is what
// lets the native app delete with the device key instead of only the browser session.
func (b *broker) deleteIdentity(r *http.Request, body []byte) (login, wallet string, ok bool) {
	if l, _, w, sok := b.sessionOwner(r); sok {
		return l, w, true
	}
	rid, authed, iok := b.identityOf(r, body)
	if !iok || !authed {
		return "", "", false
	}
	w := b.walletOf(r, rid)
	if !walletLoggedIn(w) { // must be a bound (logged-in) account, not an anonymous keypair
		return "", "", false
	}
	if o, found, _ := b.db.OwnerByPubkey(r.Header.Get(protocol.HeaderPubkey)); found && !o.Anonymized {
		return o.Login, w, true
	}
	return "", "", false
}

// billing handles GET /billing: the money-in view (ACCOUNT-PAYOUTS-DESIGN section 4)
// - cached balance + top-up history from the ledger (kind=topup).
func (b *broker) billing(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	user, ok := b.dashIdentity(r)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in")
		return
	}
	bal, _ := b.db.BalanceOf(user, b.seedFunds)
	derived, _ := b.db.DeriveBalance(user)
	topups, _ := b.db.LedgerOf(user, []string{store.KindTopup}, recentLimit(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"balance":        round6(bal),
		"derived":        round6(derived), // ledger re-derivation (drift check)
		"credit_usd":     b.bill.creditUSD,
		"checkout_ready": b.bill.secretKey != "",
		"topups":         nonNilLedger(topups),
	})
}

// usage handles GET /usage?group=model|day: the consumer spend view
// (ACCOUNT-PAYOUTS-DESIGN section 5) - lifetime spend + grouped breakdown over the
// receipts, plus the recent requests table.
func (b *broker) usage(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	user, ok := b.dashIdentity(r)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in")
		return
	}
	spend, _ := b.db.SpendOf(user)
	recent, _ := b.db.RecentByUser(user, 1000)
	group := r.URL.Query().Get("group")
	if group != "day" {
		group = "model"
	}
	buckets := groupSpend(recent, group)
	// Cap the returned recent rows for the table.
	tableLimit := recentLimit(r)
	if len(recent) > tableLimit {
		recent = recent[:tableLimit]
	}
	if recent == nil {
		recent = []store.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"spend":   round6(spend),
		"group":   group,
		"buckets": buckets,
		"recent":  recent,
	})
}

// usageBucket is one grouped spend total (by model or by day).
type usageBucket struct {
	Key   string  `json:"key"`
	Cost  float64 `json:"cost"`
	Count int     `json:"count"`
}

// groupSpend sums receipt cost by model name or by UTC day (YYYY-MM-DD), newest/
// largest first. Returns a non-nil slice.
func groupSpend(entries []store.Entry, group string) []usageBucket {
	sums := map[string]float64{}
	counts := map[string]int{}
	for _, e := range entries {
		var key string
		if group == "day" {
			key = time.Unix(e.TS, 0).UTC().Format("2006-01-02")
		} else {
			key = e.Model
			if key == "" {
				key = "unknown"
			}
		}
		sums[key] += e.Cost
		counts[key]++
	}
	out := make([]usageBucket, 0, len(sums))
	for k, v := range sums {
		out = append(out, usageBucket{Key: k, Cost: round6(v), Count: counts[k]})
	}
	if group == "day" {
		sort.Slice(out, func(i, j int) bool { return out[i].Key > out[j].Key }) // newest day first
	} else {
		sort.Slice(out, func(i, j int) bool { return out[i].Cost > out[j].Cost }) // biggest spend first
	}
	return out
}

// nonNilLedger guarantees a JSON array (not null) for empty ledger results.
func nonNilLedger(rows []store.LedgerRow) []store.LedgerRow {
	if rows == nil {
		return []store.LedgerRow{}
	}
	return rows
}
