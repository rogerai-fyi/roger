package main

import (
	"encoding/json"
	"log"
	"os"
	"strconv"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// strikes.go is the OWNER-KEYED anti-abuse layer. The verify/void/recount stack flags
// three provable abuse signals - an impossible token claim (claimed prompt tokens >
// body bytes), an empty/no-output billing attempt, and a recount over-report. Each
// flag accrues an evidence-bound STRIKE against the OWNER ACCOUNT (the durable GitHub
// owner pubkey), NOT the node_id (a cheap-to-rotate callsign). At a threshold the owner
// is warned, then BANNED durably so the operator cannot return under a fresh node id /
// callsign / grant key. The evidence is non-repudiable (the node's own signed claim vs
// the broker's recount) so the operator can be SHOWN exactly why.

// defaultStrikeWarnAt / defaultStrikeBanAt are the warn + ban thresholds for the
// ACCUMULATING signals (empty-output, recount over-report): tolerant of one-off noise.
// Overridable via env. impossible-input is a ZERO-DOUBT signal and bans on one strike
// regardless of these (claimed tokens > UTF-8 bytes is arithmetically impossible).
const (
	defaultStrikeWarnAt = 3
	defaultStrikeBanAt  = 5
)

func strikeWarnAt() int {
	if v := os.Getenv("ROGERAI_STRIKE_WARN_AT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultStrikeWarnAt
}

func strikeBanAt() int {
	if v := os.Getenv("ROGERAI_STRIKE_BAN_AT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultStrikeBanAt
}

// ownerOf resolves the DURABLE owner account (owner pubkey) for a node. A public /
// unowned node has no binding; we fall back to the node id so the signal is still
// recorded against the best identity available (a public node has nothing to rotate to
// anyway). ok reports whether a real owner binding was found.
func (b *broker) ownerOf(nodeID string) (account string, ok bool) {
	if b.db == nil {
		return nodeID, false
	}
	if acct, found, _ := b.db.AccountOfNode(nodeID); found && acct != "" {
		return acct, true
	}
	return nodeID, false
}

// strike records ONE evidence-bound strike against the node's owner account, holds the
// owner's earning lots from promotion (survives node rotation), and escalates: at the
// warn threshold it logs a warning the dashboard surfaces; at the ban threshold (or
// immediately for a zero-doubt signal) it durably BANS the owner and refreshes the
// in-memory owner-ban cache so pick/settle reject every current+future node under that
// owner. idemKey makes a retried request non-double-striking. zeroDoubt forces an
// immediate ban on the first strike (used for the impossible-input arithmetic proof).
func (b *broker) strike(nodeID, kind, idemKey string, zeroDoubt bool, evidence map[string]any) {
	if b.db == nil {
		return
	}
	acct, _ := b.ownerOf(nodeID)
	ev, _ := json.Marshal(evidence)
	n, err := b.db.OwnerStrike(acct, kind, string(ev), idemKey)
	if err != nil {
		log.Printf("strike: OwnerStrike(acct=%s kind=%s) failed: %v", acct, kind, err)
		return
	}
	// Hold ALL of the owner's earning lots from auto-promotion pending review (the
	// owner-level twin of the node recount hold; survives a node-id rotation).
	if err := b.db.SetAccountRecountHold(acct, true); err != nil {
		log.Printf("strike: SetAccountRecountHold(%s) failed: %v", acct, err)
	}
	log.Printf("STRIKE owner=%s node=%s kind=%s count=%d (warn=%d ban=%d zeroDoubt=%v)",
		acct, nodeID, kind, n, b.strikeWarnAt, b.strikeBanAt, zeroDoubt)
	switch {
	case zeroDoubt || n >= b.strikeBanAt:
		b.banOwner(acct, kind, string(ev))
	case n >= b.strikeWarnAt:
		log.Printf("STRIKE WARNING owner=%s kind=%s count=%d/%d - one more class of violation will ban this account",
			acct, kind, n, b.strikeBanAt)
	}
}

// banOwner durably bans an operator ACCOUNT (owner pubkey) and refreshes the in-memory
// owner-ban cache so pick/settle reject it immediately. The ban blocks register + relay
// pick + settle for every current and future node under that owner, so a banned operator
// cannot return under a fresh node id / callsign / grant key. Idempotent.
func (b *broker) banOwner(accountID, reason, evidenceJSON string) {
	if accountID == "" || b.db == nil {
		return
	}
	if err := b.db.BanOwner(accountID, reason, evidenceJSON); err != nil {
		log.Printf("banOwner: persist failed acct=%s: %v", accountID, err)
	}
	b.metricsMu.Lock()
	if b.bannedOwners == nil {
		b.bannedOwners = map[string]bool{}
	}
	already := b.bannedOwners[accountID]
	b.bannedOwners[accountID] = true
	b.metricsMu.Unlock()
	if !already {
		log.Printf("BAN owner=%s EJECTED (durable, anti-rotation): %s - blocked at register + relay pick + settle for ALL current/future nodes", accountID, reason)
	}
}

// isOwnerBanned reports whether an owner account is durably banned. Reads the in-memory
// cache (re-hydrated at startup + refreshed on a ban) so the hot relay/pick path never
// hits the DB. Concurrency-safe.
func (b *broker) isOwnerBanned(accountID string) bool {
	if accountID == "" {
		return false
	}
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	return b.bannedOwners[accountID]
}

// nodeOwnerBanned reports whether the node's resolved owner account is banned. Used by
// pickFor (a banned owner's nodes are dropped from routing) and settle.
func (b *broker) nodeOwnerBanned(nodeID string) bool {
	// Fast path: no owners are banned -> skip the owner resolution entirely (zero DB
	// hits in the common case). Only pay the AccountOfNode lookup when the ban set is
	// non-empty.
	b.metricsMu.Lock()
	anyBanned := len(b.bannedOwners) > 0
	b.metricsMu.Unlock()
	if !anyBanned {
		return false
	}
	acct, ok := b.ownerOf(nodeID)
	if !ok {
		return false // unowned/public node: no owner to ban (node_id ban handles those)
	}
	return b.isOwnerBanned(acct)
}

// rehydrateOwnerBans loads the durable owner-ban set into the in-memory cache at startup
// so an owner ban survives a restart/redeploy. Non-fatal on error.
func (b *broker) rehydrateOwnerBans() {
	if b.db == nil {
		return
	}
	bans, err := b.db.BannedOwners()
	if err != nil {
		log.Printf("owner-ban: rehydrate failed: %v", err)
		return
	}
	b.metricsMu.Lock()
	if b.bannedOwners == nil {
		b.bannedOwners = map[string]bool{}
	}
	for acct := range bans {
		b.bannedOwners[acct] = true
	}
	n := len(b.bannedOwners)
	b.metricsMu.Unlock()
	if n > 0 {
		log.Printf("owner-ban: re-hydrated %d banned owner account(s) from the store", n)
	}
}

// flagImpossibleInput is the ZERO-DOUBT input-inflation signal: a node claimed MORE
// prompt tokens than the request body has UTF-8 bytes, which no tokenizer can produce.
// It bans the owner on the first strike (arithmetic proof, no false positives).
func (b *broker) flagImpossibleInput(nodeID, requestID string, claimed, bodyLen int) {
	b.strike(nodeID, store.StrikeImpossibleInput, "imposs:"+requestID, true, map[string]any{
		"request_id":     requestID,
		"axis":           "input",
		"claimed_tokens": claimed,
		"body_bytes":     bodyLen,
		"note":           "claimed prompt tokens exceed request body bytes (impossible)",
	})
}

// flagEmptyOutput is the no-usable-output signal: the node billed input but produced no
// usable completion (errored, empty, or claimed-without-text). Accumulates toward the
// warn/ban thresholds (tolerant of one-off noise).
func (b *broker) flagEmptyOutput(nodeID string, rec protocol.UsageReceipt, status int) {
	b.strike(nodeID, store.StrikeEmptyOutput, "empty:"+rec.RequestID, false, map[string]any{
		"request_id":         rec.RequestID,
		"axis":               "output",
		"status":             status,
		"claimed_prompt":     rec.PromptTokens,
		"claimed_completion": rec.CompletionTokens,
		"note":               "billed input but produced no usable output (voided)",
	})
}

// flagRecountOver is the recount over-report signal: the node's claimed token count
// materially exceeded the broker's independent re-count past tolerance. Accumulates.
func (b *broker) flagRecountOver(nodeID, requestID, axis string, claimed, recounted int) {
	b.strike(nodeID, store.StrikeRecountDiscrepancy, "recount:"+axis+":"+requestID, false, map[string]any{
		"request_id":     requestID,
		"axis":           axis,
		"claimed_tokens": claimed,
		"broker_recount": recounted,
		"note":           "node over-reported tokens past the recount tolerance",
	})
}
