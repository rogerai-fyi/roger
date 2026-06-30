package main

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"time"

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
	// defaultStrikeDecayDays is the trailing window strikes are counted over for the ban
	// decision (DECAY): a strike older than this no longer counts toward warn/ban, so an
	// operator who fixed their issue is not banned on months-old, already-stale noise. The
	// append-only evidence row is KEPT (StrikesByOwner still shows it); only its weight in
	// the live ban decision ages out. <=0 disables decay (count all strikes, the old
	// behavior). Overridable via ROGERAI_STRIKE_DECAY_DAYS.
	defaultStrikeDecayDays = 30
	// defaultStrikeCorroborateKinds is the CORROBORATION floor for an accumulating-signal
	// ban: an accumulating ban requires strikes across at least this many DISTINCT signal
	// classes (e.g. empty-output AND recount-discrepancy), so one noisy signal class can
	// never auto-ban an account on its own (a single misbehaving check / tokenizer quirk
	// is contained). 1 disables corroboration (any single class can ban at the threshold).
	// The ZERO-DOUBT path (impossible-input arithmetic proof) bypasses this entirely.
	// Overridable via ROGERAI_STRIKE_CORROBORATE_KINDS.
	defaultStrikeCorroborateKinds = 2
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

// strikeDecayDays is the trailing-window (in days) the ban decision counts strikes over.
// <=0 disables decay. ROGERAI_STRIKE_DECAY_DAYS overrides (a 0 there explicitly disables).
func strikeDecayDays() int {
	if v := os.Getenv("ROGERAI_STRIKE_DECAY_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultStrikeDecayDays
}

// strikeCorroborateKinds is the minimum number of DISTINCT signal classes required before
// an accumulating-signal ban. <=1 disables corroboration. ROGERAI_STRIKE_CORROBORATE_KINDS
// overrides.
func strikeCorroborateKinds() int {
	if v := os.Getenv("ROGERAI_STRIKE_CORROBORATE_KINDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultStrikeCorroborateKinds
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
	if _, err := b.db.OwnerStrike(acct, kind, string(ev), idemKey); err != nil {
		log.Printf("strike: OwnerStrike(acct=%s kind=%s) failed: %v", acct, kind, err)
		return
	}
	// Hold ALL of the owner's earning lots from auto-promotion pending review (the
	// owner-level twin of the node recount hold; survives a node-id rotation). This is the
	// conservative, REVERSIBLE freeze (auto-expires via recountHoldSweep, cleared by admin
	// unhold) - distinct from the durable ban below, which we gate far more tightly.
	if err := b.db.SetAccountRecountHold(acct, true); err != nil {
		log.Printf("strike: SetAccountRecountHold(%s) failed: %v", acct, err)
	}
	// Ban decision inputs. zeroDoubt (the impossible-input arithmetic proof) bans on the
	// first strike, bypassing decay + corroboration. For the ACCUMULATING signals we count
	// only RECENT strikes (DECAY) and require MORE THAN ONE distinct signal class
	// (CORROBORATION) before a durable ban, so a single noisy signal class can never ban an
	// account on its own. The append-only evidence is always kept; only its weight in the
	// live ban decision ages out.
	var since int64
	if b.strikeDecayDays > 0 {
		since = time.Now().Add(-time.Duration(b.strikeDecayDays) * 24 * time.Hour).Unix()
	}
	windowed, distinctKinds, statErr := b.db.OwnerStrikeStats(acct, since)
	if statErr != nil {
		log.Printf("strike: OwnerStrikeStats(%s) failed: %v - using conservative single-class count", acct, statErr)
		windowed, distinctKinds = 1, 1 // fail SOFT: never escalate to a ban on a read error
	}
	corroborated := distinctKinds >= b.strikeCorroborateKinds
	log.Printf("STRIKE owner=%s node=%s kind=%s windowed=%d kinds=%d (warn=%d ban=%d corroborate=%d decayDays=%d zeroDoubt=%v)",
		acct, nodeID, kind, windowed, distinctKinds, b.strikeWarnAt, b.strikeBanAt, b.strikeCorroborateKinds, b.strikeDecayDays, zeroDoubt)
	switch {
	case zeroDoubt:
		// Zero-doubt (impossible-input): arithmetic proof, immediate durable ban.
		b.banOwner(acct, kind, string(ev))
		b.emailAccountBanned(b.emailOf(acct), kind, string(ev))
	case windowed >= b.strikeBanAt && corroborated:
		// Accumulating ban: enough RECENT strikes AND corroborated across signal classes.
		b.banOwner(acct, kind, string(ev))
		// Flag-gated transactional notice (async, best-effort): tell the owner the
		// account was suspended, with the evidence that tripped it. No-op when
		// RESEND_API_KEY is unset or the owner has no email on file.
		b.emailAccountBanned(b.emailOf(acct), kind, string(ev))
	case windowed >= b.strikeBanAt && !corroborated:
		// At the count threshold but only ONE signal class: do NOT ban (corroboration
		// guard). The earnings are still held above pending review; a second distinct
		// signal class - or admin review - is required to escalate to a durable ban.
		log.Printf("STRIKE owner=%s kind=%s windowed=%d/%d but only %d/%d distinct signal class(es) - HELD (earnings frozen) but NOT banned (corroboration guard); needs a second signal class or admin review",
			acct, kind, windowed, b.strikeBanAt, distinctKinds, b.strikeCorroborateKinds)
		b.emailAccountWarning(b.emailOf(acct), kind, string(ev), windowed, b.strikeBanAt)
	case windowed >= b.strikeWarnAt:
		log.Printf("STRIKE WARNING owner=%s kind=%s windowed=%d/%d - more violations across another signal class will ban this account",
			acct, kind, windowed, b.strikeBanAt)
		// Flag-gated transactional warning (async, best-effort). No-op when disabled.
		b.emailAccountWarning(b.emailOf(acct), kind, string(ev), windowed, b.strikeBanAt)
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
	persistErr := b.db.BanOwner(accountID, reason, evidenceJSON)
	if persistErr != nil {
		log.Printf("banOwner: persist failed acct=%s: %v", accountID, persistErr)
	}
	b.metricsMu.Lock()
	if b.bannedOwners == nil {
		b.bannedOwners = map[string]bool{}
	}
	already := b.bannedOwners[accountID]
	b.bannedOwners[accountID] = true
	b.metricsMu.Unlock()
	if !already {
		// Cross-instance: bump the shared rev so the PEER re-pulls this owner ban on its next
		// sync tick (so a banned operator stops being picked + settled on B too). ONLY when the
		// durable write SUCCEEDED: a bump after a failed write would make this instance re-pull
		// the ban-less DB and drop its own in-memory flip within a tick. On failure keep the
		// local best-effort flip (single-instance parity) + skip propagation.
		if persistErr == nil {
			b.bumpBanRev()
		}
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

// flagImpossibleInput is the ZERO-DOUBT input-inflation signal: a node claimed prompt
// tokens GROSSLY beyond the request body's UTF-8 bytes - past impossibleInputBanMargin, the
// headroom that absorbs legitimate chat-template preamble overhead - which no tokenizer can
// produce. It bans the owner on the first strike (arithmetic proof, no false positives).
// The caller (settleRecountPrompt) applies the margin gate; billing is clamped to body
// bytes for ANY overage, so this fires only for abuse beyond doubt.
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
