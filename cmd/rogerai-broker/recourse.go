package main

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
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

// requireAdmin gates an admin op on EITHER of two single-super-admin credentials, so the
// founder can drive the admin surface from the CLI/curl OR just log into the website:
//
//  1. The broker secret presented in X-Roger-Admin (the BROKER_PRIVATE_KEY hex seed),
//     constant-time compared - the headless/CLI path (/admin/unhold uses this).
//  2. A valid GitHub web SESSION whose github_id equals the configured ADMIN_GITHUB_ID -
//     the browser path, so the founder logs in normally and the admin portal works off
//     the same session cookie every other account page uses (no key paste in the UI).
//
// It is CLOSED-by-default and fail-closed: if NEITHER credential is configured (no
// adminKey AND no adminGitHubID) every admin request is rejected, so the surface can
// never be hit anonymously. A request that presents neither a matching key nor a
// matching admin session is rejected. Returns true once it has written the 403, so the
// caller just returns.
func (b *broker) requireAdmin(w http.ResponseWriter, r *http.Request) (denied bool) {
	if b.adminKey == "" && b.adminGitHubID == 0 {
		jsonErr(w, http.StatusForbidden, "admin surface disabled (set BROKER_PRIVATE_KEY and/or ADMIN_GITHUB_ID to enable)")
		return true
	}
	// Path 1: the broker-key header (CLI/curl).
	if b.adminKey != "" {
		got := r.Header.Get("X-Roger-Admin")
		if got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(b.adminKey)) == 1 {
			return false
		}
	}
	// Path 2: the configured super-admin GitHub session (browser).
	if b.isAdminSession(r) {
		return false
	}
	jsonErr(w, http.StatusForbidden, "admin auth required")
	return true
}

// isAdminSession reports whether the request carries a valid GitHub web session whose
// github_id matches the single configured super-admin (ADMIN_GITHUB_ID). False when no
// admin id is configured, or when there is no valid session / the id does not match - so
// an ordinary logged-in owner is NEVER an admin. This is the browser half of requireAdmin
// and is the ONLY identity check the admin portal page itself needs.
func (b *broker) isAdminSession(r *http.Request) bool {
	if b.adminGitHubID == 0 {
		return false
	}
	_, gid, _, ok := b.sessionOwner(r)
	return ok && gid == b.adminGitHubID
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
		jsonErr(w, http.StatusUnauthorized, "not logged in - run `roger login` to link GitHub")
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
	// Surface each owned node's ban status + reason (3.3.1): a banned operator must be able
	// to SEE why, not just silently fall out of routing. banned_nodes was previously
	// invisible to owners. node_bans maps node_id -> reason for every owned node currently
	// ejected.
	nodeBans := map[string]string{}
	if nodes, err := b.db.NodesOfAccount(acct); err == nil {
		var allBans map[string]string
		for _, n := range nodes {
			if !b.isBanned(n) {
				continue
			}
			if allBans == nil {
				allBans, _ = b.db.BannedNodes() // reason lookup, fetched lazily once
			}
			nodeBans[n] = allBans[n]
		}
	}
	appeals, _ := b.db.AppealsByOwner(acct, 20)
	if appeals == nil {
		appeals = []store.Appeal{}
	}
	appealNote := "You are in good standing - nothing to appeal."
	if banned || len(nodeBans) > 0 || len(strikes) > 0 {
		appealNote = "If you believe this is a mistake, file a self-serve appeal: `roger appeal --reason \"...\"` (or POST /owner/appeal). An admin reviews the evidence above; clear false positives can auto-clear."
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"strikes":     strikes,
		"count":       len(strikes),
		"banned":      banned,
		"ban_reason":  reason,
		"node_bans":   nodeBans,
		"appeals":     appeals,
		"warn_at":     b.strikeWarnAt,
		"ban_at":      b.strikeBanAt,
		"appeal_note": appealNote,
	})
}

// ownerAppealRequest is the POST /owner/appeal body: the operator's free-text reason and
// an OPTIONAL node_id (when appealing a specific node ban). The account is NEVER taken
// from the request - it is the authenticated owner pubkey (payoutOwner), so an appeal can
// only ever be filed for the caller.
type ownerAppealRequest struct {
	NodeID string `json:"node_id,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ownerAppeal handles /owner/appeal: the self-serve appeal flow (3.3). Owner-authed via
// payoutOwner (web session OR a signed CLI request bound to a GitHub owner), strictly
// owner-scoped (the account is the authenticated pubkey, never request-supplied), so a
// caller can only ever appeal for their own account/nodes - cross-account filing is
// structurally impossible.
//
//	GET  -> the caller's own appeals (status surface)
//	POST {node_id?, reason} -> file an appeal. If node_id is given it MUST belong to the
//	     caller (NodesOfAccount); a report-origin node ban that is BELOW the live
//	     corroboration threshold is auto-exonerated (lifted immediately) - a clear false
//	     positive recovers without waiting for a human - and every appeal is enqueued for
//	     admin review with the evidence trail.
func (b *broker) ownerAppeal(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body []byte
	if r.Method == http.MethodPost {
		body, _ = io.ReadAll(io.LimitReader(r.Body, 1<<16))
	}
	_, o, ok := b.payoutOwner(r, body)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in - run `roger login` to link GitHub")
		return
	}
	if o.Pubkey == "" {
		jsonErr(w, http.StatusForbidden, "no operator account for this login (run `roger login` on a node first)")
		return
	}
	acct := o.Pubkey

	// GET: the caller's appeal history / status.
	if r.Method == http.MethodGet {
		appeals, err := b.db.AppealsByOwner(acct, 50)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "store error")
			return
		}
		if appeals == nil {
			appeals = []store.Appeal{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"appeals": appeals, "count": len(appeals)})
		return
	}

	// POST: file an appeal.
	var req ownerAppealRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}
	reason := strings.TrimSpace(req.Reason)
	if len(reason) > 4096 {
		reason = reason[:4096]
	}
	nodeID := strings.TrimSpace(req.NodeID)
	// A node_id, when given, MUST belong to the caller. This is the owner-scoping gate:
	// the caller can never appeal (or auto-lift) another account's node.
	if nodeID != "" {
		nodes, err := b.db.NodesOfAccount(acct)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "store error")
			return
		}
		owned := false
		for _, n := range nodes {
			if n == nodeID {
				owned = true
				break
			}
		}
		if !owned {
			jsonErr(w, http.StatusForbidden, "that node is not bound to your account")
			return
		}
	}
	id, err := b.db.AddAppeal(store.Appeal{AccountID: acct, NodeID: nodeID, Reason: reason})
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not record appeal")
		return
	}
	out := map[string]any{"ok": true, "appeal_id": id, "state": store.AppealOpen}
	log.Printf("APPEAL filed id=%d owner=%s node=%q - queued for admin review", id, acct, nodeID)

	// Auto-exoneration for a CLEAR false positive: a report-origin node suspension that is
	// no longer corroborated (distinct reporters within the window are below the live eject
	// threshold) is lifted immediately, rather than stranding an honest operator until a
	// human looks. An admin/crypto-verified permanent ban (no "report " prefix) is NEVER
	// auto-lifted here - only a human can clear those.
	if nodeID != "" && b.isBanned(nodeID) {
		bans, _ := b.db.BannedNodes()
		reasonStr := bans[nodeID]
		if strings.HasPrefix(reasonStr, "report ") {
			// A report-origin ban auto-exonerates on appeal when it is no longer
			// corroborated. With auto-eject DISABLED (reportEjectAt<=0) there is NO live
			// corroboration threshold the ban can meet, so any leftover report-origin ban
			// is by definition unsustainable -> exonerate. Otherwise lift only when the
			// distinct reporters in the decay window have fallen below the live threshold.
			exonerate := b.reportEjectAt <= 0
			n := 0
			if !exonerate {
				since := int64(0)
				if b.reportDecayDays > 0 {
					since = time.Now().Add(-time.Duration(b.reportDecayDays) * 24 * time.Hour).Unix()
				}
				if cnt, err := b.db.DistinctReporterCountByNode(nodeID, since); err == nil {
					n = cnt
					exonerate = cnt < b.reportEjectAt
				}
			}
			if exonerate {
				if err := b.unbanNode(nodeID); err == nil {
					out["auto_exonerated"] = true
					out["node_unbanned"] = nodeID
					reason := fmt.Sprintf("%d distinct reporters < %d threshold", n, b.reportEjectAt)
					if b.reportEjectAt <= 0 {
						reason = "auto-eject disabled, no live corroboration threshold"
					}
					log.Printf("APPEAL id=%d: node=%s auto-EXONERATED (%s) - routing restored pending review", id, nodeID, reason)
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// adminAppeals handles GET /admin/appeals (admin-authed): the OPEN appeal review queue
// (newest first) - the admin side of the self-serve appeal flow. An admin reviews each
// appeal's evidence here, then resolves it via /admin/unhold (forgive strikes / lift owner
// ban) or /admin/unban-node (lift a node ban). Counts/rows only; no secrets.
func (b *broker) adminAppeals(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	if !allow(w, r, http.MethodGet) {
		return
	}
	if b.requireAdmin(w, r) {
		return
	}
	appeals, err := b.db.PendingAppeals(200)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	if appeals == nil {
		appeals = []store.Appeal{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"appeals": appeals, "count": len(appeals)})
}

// adminUnbanNodeRequest is the POST /admin/unban-node body.
type adminUnbanNodeRequest struct {
	Node string `json:"node"`
}

// adminUnbanNode handles POST /admin/unban-node (admin-authed): lift a node ban - the
// missing node recovery path. It deletes the banned_nodes row and clears the in-memory
// set so the node routes again immediately. Mirrors adminUnhold's auth + CORS shape.
func (b *broker) adminUnbanNode(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	if !allow(w, r, http.MethodPost) {
		return
	}
	if b.requireAdmin(w, r) {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var req adminUnbanNodeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	node := strings.TrimSpace(req.Node)
	if node == "" {
		jsonErr(w, http.StatusBadRequest, "node required")
		return
	}
	if err := b.unbanNode(node); err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not unban node")
		return
	}
	log.Printf("ADMIN UNBAN-NODE node=%s - ban lifted, routing restored", node)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "node_unbanned": node})
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
	// Credentialed CORS so the admin web portal (a logged-in super-admin session, OR a
	// pasted broker key) can POST this cross-origin. The preflight is answered before the
	// admin gate; the gate still rejects any non-admin on the real request.
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
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
	interval := sweepInterval(window)
	log.Printf("recount-hold: auto-expiry ON - holds older than %d day(s) clear if no fresh discrepancy (sweep every %s)", b.recountHoldDays, interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		b.recountHoldSweepOnce(time.Now().Add(-window))
	}
}

// recountHoldSweepOnce expires recount holds older than cutoff (one sweep iteration).
// Split out of the loop so the expiry work is testable without the ticker.
func (b *broker) recountHoldSweepOnce(cutoff time.Time) {
	if n, err := b.db.ExpireRecountHolds(cutoff); err != nil {
		log.Printf("recount-hold: expiry sweep failed: %v", err)
	} else if n > 0 {
		log.Printf("recount-hold: auto-expired %d hold(s) older than %d day(s) (no further discrepancy) - those earnings can promote again", n, b.recountHoldDays)
	}
}

// sweepInterval picks a sane sweep cadence relative to a hold window: ~1/24 of the
// window, clamped to [1h, 24h], so expiry is timely without hammering the store.
func sweepInterval(window time.Duration) time.Duration {
	interval := window / 24
	if interval < time.Hour {
		interval = time.Hour
	}
	if interval > 24*time.Hour {
		interval = 24 * time.Hour
	}
	return interval
}
