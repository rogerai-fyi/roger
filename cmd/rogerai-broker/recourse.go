package main

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// recourse.go is the OPERATOR-RECOURSE + admin-review surface. The verify/recount/strike
// stack can FREEZE an operator's earnings (a node/account recount hold) and accrue
// strikes, but those were one-way: nothing ever cleared a hold, so a false positive
// could freeze an honest operator forever. This file closes that fairness gap:
//
//  1. GET /owner/strikes (owner-authed) lets an operator SEE their own strikes +
//     evidence, so a freeze is never a black box.
//  2. POST /admin/unhold (broker-key-authed) is the human-review escape hatch: after an
//     operator disputes a freeze, an admin clears the hold and (optionally) forgives the
//     strikes / lifts the ban, and the operator's held lots promote again on schedule.
//  3. recountHoldSweep auto-expires a hold that has sat unreviewed past the window
//     (ROGERAI_RECOUNT_HOLD_DAYS), so even with NO admin action an honest operator is
//     unfrozen - while an actually-abusive one is kept held because each fresh
//     discrepancy refreshes the hold's timestamp above the expiry cutoff.
//
// REVIEW FLOW: discrepancy -> hold + strike (earnings frozen) -> operator sees it via
// GET /owner/strikes and contests it -> admin reviews the evidence -> if exonerated,
// POST /admin/unhold {account_id, forgive:true} clears the hold + forgives strikes +
// lifts any ban, and the next promote sweep releases the held lots; if no review
// happens, recountHoldSweep auto-clears the hold after ROGERAI_RECOUNT_HOLD_DAYS.

// defaultRecountHoldDays is the auto-expiry window for a recount hold. Tuned tolerant:
// long enough for a real review, short enough that a false positive doesn't strand an
// honest operator's earnings. Overridable via ROGERAI_RECOUNT_HOLD_DAYS. <=0 disables
// auto-expiry (holds then clear only via the admin-reviewed unhold).
const defaultRecountHoldDays = 7

func recountHoldDays() int {
	if v := os.Getenv("ROGERAI_RECOUNT_HOLD_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultRecountHoldDays
}

// validAdminKey returns the hex broker seed to gate the admin surface on, but ONLY when
// it is a real BROKER_PRIVATE_KEY hex seed. An unset / malformed key returns "" so the
// admin surface stays CLOSED (requireAdmin 403s everything) rather than accidentally
// gating on an ephemeral key that an attacker could never present anyway but which would
// also lock the legitimate operator out silently.
func validAdminKey(h string) string {
	if h == "" {
		return ""
	}
	if seed, err := hex.DecodeString(h); err == nil && len(seed) == ed25519.SeedSize {
		return h
	}
	return ""
}

// requireAdmin gates an admin op on the broker secret presented in X-Roger-Admin. It is
// CLOSED-by-default: if no admin key is configured (ephemeral / unset broker key) every
// admin request is rejected. The compare is constant-time. Returns true once it has
// already written the 403 to w, so the caller just returns.
func (b *broker) requireAdmin(w http.ResponseWriter, r *http.Request) (denied bool) {
	if b.adminKey == "" {
		jsonErr(w, http.StatusForbidden, "admin surface disabled (set BROKER_PRIVATE_KEY to enable)")
		return true
	}
	got := r.Header.Get("X-Roger-Admin")
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(b.adminKey)) != 1 {
		jsonErr(w, http.StatusForbidden, "admin auth required")
		return true
	}
	return false
}

// ownerStrikes handles GET /owner/strikes: the CALLER's own strike evidence. Owner-authed
// via payoutOwner (web session OR a signed CLI request bound to a non-anonymized GitHub
// owner), so it is account-scoped to the caller - a caller can ONLY ever read their own
// strikes (the lookup key is the authenticated owner pubkey, never a request-supplied
// account), so cross-account access is structurally impossible. Returns the strikes
// (newest first, with evidence), the current hold + ban status, and a count, so an
// operator can see exactly why their earnings are held and contest it.
func (b *broker) ownerStrikes(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	_, o, ok := b.payoutOwner(r, nil)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in - run `rogerai login` to link GitHub")
		return
	}
	if o.Pubkey == "" {
		// Logged in but not (yet) a bound operator: no strikes, well-formed empty body.
		writeJSON(w, http.StatusOK, map[string]any{"strikes": []store.Strike{}, "count": 0, "held": false, "banned": false})
		return
	}
	acct := o.Pubkey
	strikes, err := b.db.StrikesByOwner(acct, 100)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	if strikes == nil {
		strikes = []store.Strike{}
	}
	banned, reason, _ := b.db.IsOwnerBanned(acct)
	writeJSON(w, http.StatusOK, map[string]any{
		"strikes":     strikes,
		"count":       len(strikes),
		"banned":      banned,
		"ban_reason":  reason,
		"warn_at":     b.strikeWarnAt,
		"ban_at":      b.strikeBanAt,
		"appeal_note": "Dispute a hold by contacting support with your request id(s); an admin reviews the evidence above.",
	})
}

// adminUnholdRequest is the POST /admin/unhold body. Either node OR account (or both)
// may be given. forgive=true also deletes the owner's strikes + lifts any owner ban (the
// full exoneration); omit it to ONLY release the hold (e.g. a temporary review pause).
type adminUnholdRequest struct {
	AccountID string `json:"account_id,omitempty"`
	Node      string `json:"node,omitempty"`
	Forgive   bool   `json:"forgive,omitempty"`
}

// adminUnhold handles POST /admin/unhold: the human-review escape hatch (broker-key
// gated). It CLEARS a recount hold so the operator's held lots promote again on the next
// sweep, and - when forgive=true - forgives the owner's strikes and lifts any durable
// owner ban (refreshing the in-memory ban cache). This is the recourse for a false
// positive: an honest operator frozen by a bad recount is unfrozen here after review.
func (b *broker) adminUnhold(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	if b.requireAdmin(w, r) {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var req adminUnholdRequest
	if err := json.Unmarshal(body, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.AccountID == "" && req.Node == "" {
		jsonErr(w, http.StatusBadRequest, "account_id or node required")
		return
	}
	out := map[string]any{"ok": true}
	if req.Node != "" {
		if err := b.db.SetNodeRecountHold(req.Node, false); err != nil {
			jsonErr(w, http.StatusInternalServerError, "could not clear node hold")
			return
		}
		out["node_unheld"] = req.Node
		log.Printf("ADMIN UNHOLD node=%s - recount hold cleared (lots will promote on the next sweep)", req.Node)
	}
	if req.AccountID != "" {
		if err := b.db.SetAccountRecountHold(req.AccountID, false); err != nil {
			jsonErr(w, http.StatusInternalServerError, "could not clear account hold")
			return
		}
		out["account_unheld"] = req.AccountID
		log.Printf("ADMIN UNHOLD account=%s - recount hold cleared (lots will promote on the next sweep)", req.AccountID)
		if req.Forgive {
			n, err := b.db.ForgiveOwner(req.AccountID)
			if err != nil {
				jsonErr(w, http.StatusInternalServerError, "could not forgive owner")
				return
			}
			// Refresh the in-memory owner-ban cache so the lifted ban takes effect on the
			// hot pick/settle path immediately (ForgiveOwner removed the durable row).
			b.metricsMu.Lock()
			delete(b.bannedOwners, req.AccountID)
			b.metricsMu.Unlock()
			out["strikes_forgiven"] = n
			out["ban_lifted"] = true
			log.Printf("ADMIN FORGIVE account=%s - %d strike(s) forgiven, owner ban lifted (in-memory cache refreshed)", req.AccountID, n)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// recountHoldSweep auto-expires recount holds past the review window (operator recourse).
// It runs on a ticker, clearing every node/account hold first placed more than
// recountHoldDays ago that no fresh discrepancy has since refreshed. A no-op (returns
// immediately) when auto-expiry is disabled (recountHoldDays<=0) - holds then clear only
// via the admin-reviewed unhold. The sweep is idempotent and cheap.
func (b *broker) recountHoldSweep() {
	if b.recountHoldDays <= 0 {
		log.Printf("recount-hold: auto-expiry DISABLED (ROGERAI_RECOUNT_HOLD_DAYS<=0) - holds clear only via admin /admin/unhold")
		return
	}
	if b.db == nil {
		return
	}
	window := time.Duration(b.recountHoldDays) * 24 * time.Hour
	// Sweep at a sane cadence relative to the window (at least hourly, at most daily) so
	// expiry is timely without hammering the store.
	interval := window / 24
	if interval < time.Hour {
		interval = time.Hour
	}
	if interval > 24*time.Hour {
		interval = 24 * time.Hour
	}
	log.Printf("recount-hold: auto-expiry ON - holds older than %d day(s) clear if no fresh discrepancy (sweep every %s)", b.recountHoldDays, interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		cutoff := time.Now().Add(-window)
		if n, err := b.db.ExpireRecountHolds(cutoff); err != nil {
			log.Printf("recount-hold: expiry sweep failed: %v", err)
		} else if n > 0 {
			log.Printf("recount-hold: auto-expired %d hold(s) older than %d day(s) (no further discrepancy) - those earnings can promote again", n, b.recountHoldDays)
		}
	}
}
