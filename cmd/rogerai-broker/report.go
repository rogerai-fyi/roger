package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// report.go is the safety surface: the CSAM preserve+queue path (18 USC 2258A), the
// public POST /report abuse endpoint, and the report-threshold node ban/eject that
// reuses the probe-eject idea (a banned node is treated as not-serving in pick).

// defaultReportEjectAt is the number of DISTINCT corroborating reporters (distinct
// reporter IPs, within the decay window) that auto-suspends a node from routing. It is no
// longer a raw all-time COUNT(*): one source can no longer stack N reports to ban a node
// (H2), and stale reports age out. Override with ROGERAI_REPORT_EJECT_AT (0 disables
// auto-eject; a node can still be manually banned).
const defaultReportEjectAt = 5

// defaultReportDecayDays is the trailing window the distinct-reporter corroboration count
// is taken over (DECAY): reports older than this no longer count toward an eject, so a
// node that fixed its issue recovers automatically. Override ROGERAI_REPORT_DECAY_DAYS.
const defaultReportDecayDays = 30

// defaultNodeBanDays is the auto-lift window for a report-origin node suspension: a
// report-eject is a TIME-BOXED suspension (reversible, appealable), not a permanent ban -
// it auto-clears after this many days unless fresh corroboration re-arms it or an admin
// confirms it. Permanent bans come only from admin action / crypto-verified abuse, never
// raw report count. Override ROGERAI_NODE_BAN_DAYS (<=0 disables auto-lift).
const defaultNodeBanDays = 3

// reportBanReasonPrefix marks a ban as report-origin (a temporary, auto-lifting
// suspension). ExpireNodeBans only auto-clears bans whose reason starts with "report " -
// an admin/crypto-verified permanent ban never carries this prefix, so it is never
// auto-lifted. Keep in sync with the store's ExpireNodeBans filter.
const reportBanReasonPrefix = "report threshold"

func reportEjectThreshold() int {
	if v := os.Getenv("ROGERAI_REPORT_EJECT_AT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return defaultReportEjectAt
}

func reportDecayDays() int {
	if v := os.Getenv("ROGERAI_REPORT_DECAY_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultReportDecayDays
}

func nodeBanDays() int {
	if v := os.Getenv("ROGERAI_NODE_BAN_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultNodeBanDays
}

// csamKey derives the AES-256 key for encrypting preserved CSAM content from the
// broker's stable signing seed. Tying it to the seed means an ephemeral-key boot can't
// read incidents written under the real key (and the ROGERAI_REQUIRE_BROKER_KEY guard
// keeps prod on the stable seed). The store only ever holds the ciphertext.
func (b *broker) csamKey() [32]byte {
	return sha256.Sum256(append([]byte("rogerai-csam-v1|"), b.priv.Seed()...))
}

// encryptCSAM AES-GCM-encrypts the offending content for at-rest storage. The nonce is
// prepended to the ciphertext. On any failure it returns nil (the caller still records
// the incident metadata; losing the body is preferable to storing plaintext).
func (b *broker) encryptCSAM(plaintext []byte) []byte {
	key := b.csamKey()
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil
	}
	return gcm.Seal(nonce, nonce, plaintext, nil)
}

// preserveCSAM PRESERVES a child-exploitation hit and QUEUES the CyberTipline report
// obligation. The offending content is encrypted-at-rest before it touches the store
// (the broker stays content-blind otherwise). A clear, loud log line records the
// obligation; a real CyberTipline API submission is a follow-up that drains
// PendingCSAMReports. Never blocks the response path on a store error.
func (b *broker) preserveCSAM(pseudonym, ip, category string, content []byte) {
	enc := b.encryptCSAM(content)
	id, err := b.db.PreserveCSAM(store.CSAMIncident{
		Pseudonym: pseudonym, IP: ip, Category: category, Content: enc,
		ReportState: store.CSAMQueued,
	})
	if err != nil {
		log.Printf("CSAM: PRESERVE FAILED (category=%s pseudonym=%s ip=%s): %v - report obligation NOT recorded, INVESTIGATE", category, pseudonym, ip, err)
		return
	}
	log.Printf("CSAM: incident #%d PRESERVED + report QUEUED (category=%s pseudonym=%s ip=%s) - CyberTipline report owed (18 USC 2258A)", id, category, pseudonym, ip)
}

// rehydrateBans loads the persisted banned-node set into the in-memory cache at startup
// so a ban survives a restart/redeploy. Failure is non-fatal (logged): the broker still
// boots; bans re-apply when reports cross the threshold again.
func (b *broker) rehydrateBans() {
	bans, err := b.db.BannedNodes()
	if err != nil {
		log.Printf("ban: rehydrate failed: %v", err)
		return
	}
	b.metricsMu.Lock()
	for id := range bans {
		b.banned[id] = true
	}
	n := len(b.banned)
	b.metricsMu.Unlock()
	if n > 0 {
		log.Printf("ban: re-hydrated %d ejected node(s) from the store", n)
	}
}

// banRevKey is the shared monotonic ban-revision counter (under the rogerai:ctr: keyspace).
// Every ban/unban — node OR owner — bumps it; each instance compares it on the existing
// liveness sync tick and re-pulls the durable banned sets when it changes. This is the
// lightest cross-instance ban propagation: ONE counter, checked on a loop that already
// runs, with NO Valkey round-trip on the hot pick/discover/settle path (those keep reading
// the in-memory sets exactly as before). Reuses the shared-store counter primitives.
const banRevKey = "ban:rev"

// bumpBanRev increments the shared ban-revision so PEER instances re-pull the banned sets on
// their next sync tick. Called on every ban/unban STATE CHANGE (node + owner). A guarded
// no-op when no shared backend is wired (single-instance: the local map flip is already the
// whole truth); best-effort on a Valkey error — the ban already persisted to the store and
// flipped this instance's set, so a blip only delays cross-instance propagation to the next
// ban event / restart (the peer still rehydrates from the store on boot).
func (b *broker) bumpBanRev() {
	if b.shared == nil {
		return
	}
	if _, err := b.shared.counterIncr(banRevKey, 1); err != nil {
		log.Printf("ban: rev bump failed (cross-instance propagation delayed): %v", err)
	}
}

// syncBanRev re-pulls the durable banned-node + banned-owner sets from the store into the
// in-memory caches when a PEER instance has changed them, detected via the shared ban-rev
// counter. Called on the existing liveness sync tick (syncLivenessOnce). The common case is
// ONE cheap counter read that matches the last-applied rev → a no-op. On a change it
// REPLACES (not merges) both local sets with the store truth, so an UNBAN/auto-lift on a
// peer propagates too. A no-op when no shared backend is wired. On a store read error it
// leaves the rev unrecorded so the re-pull retries next tick (fail-safe: never silently drop
// a ban). The store reads run OUTSIDE metricsMu (no DB call under the hot-path lock).
func (b *broker) syncBanRev() {
	if b.shared == nil {
		return
	}
	rev, found, err := b.shared.counterGet(banRevKey)
	if err != nil || !found {
		return // no ban has ever been issued (clean miss) or a transient backend error
	}
	b.metricsMu.Lock()
	unchanged := rev == b.banRev
	b.metricsMu.Unlock()
	if unchanged {
		return
	}
	nodes, nerr := b.db.BannedNodes()
	owners, oerr := b.db.BannedOwners()
	if nerr != nil || oerr != nil {
		log.Printf("ban: cross-instance re-pull failed (nodes=%v owners=%v) - retrying next tick", nerr, oerr)
		return // leave b.banRev unchanged so the next tick retries; local sets stay as-is
	}
	nb := make(map[string]bool, len(nodes))
	for id := range nodes {
		nb[id] = true
	}
	ob := make(map[string]bool, len(owners))
	for acct := range owners {
		ob[acct] = true
	}
	b.metricsMu.Lock()
	b.banned = nb
	b.bannedOwners = ob
	b.banRev = rev
	b.metricsMu.Unlock()
	log.Printf("ban: cross-instance sync applied rev %.0f - %d node ban(s), %d owner ban(s)", rev, len(nb), len(ob))
}

// isBanned reports whether a node is ejected from routing. Concurrency-safe.
func (b *broker) isBanned(nodeID string) bool {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	return b.banned[nodeID]
}

// banNode ejects a node: persists the ban + flips the in-memory set so pick/discover/
// market stop routing to it (the probe-eject mechanism treats it as not-serving).
func (b *broker) banNode(nodeID, reason string) {
	if nodeID == "" {
		return
	}
	if err := b.db.BanNode(nodeID, reason); err != nil {
		log.Printf("ban: persist failed node=%s: %v", nodeID, err)
	}
	b.metricsMu.Lock()
	already := b.banned[nodeID]
	b.banned[nodeID] = true
	b.metricsMu.Unlock()
	if !already {
		// Cross-instance: bump the shared rev so the PEER instance re-pulls this ban on its
		// next sync tick (out of the lock — bumpBanRev does a Valkey round-trip).
		b.bumpBanRev()
		log.Printf("ban: node=%s EJECTED from routing (%s)", nodeID, reason)
	}
}

// unbanNode lifts a node ban: clears the durable row + the in-memory set so the node can
// route again immediately. The recovery path for a report-eject (admin node-unban + the
// self-serve appeal auto-exoneration). Idempotent.
func (b *broker) unbanNode(nodeID string) error {
	if nodeID == "" {
		return nil
	}
	if err := b.db.UnbanNode(nodeID); err != nil {
		return err
	}
	b.metricsMu.Lock()
	was := b.banned[nodeID]
	delete(b.banned, nodeID)
	b.metricsMu.Unlock()
	if was {
		// Cross-instance: bump the shared rev so the PEER re-pulls (the re-pull REPLACES its
		// set, so this unban clears the node there too — not just adds).
		b.bumpBanRev()
		log.Printf("ban: node=%s UN-banned - routing restored", nodeID)
	}
	return nil
}

// nodeBanSweep auto-lifts TEMPORARY report-origin node suspensions past the review window
// (the node twin of recountHoldSweep): a report-eject is a time-boxed suspension, not a
// permanent sentence, so it auto-clears after nodeBanDays unless fresh corroboration /
// admin keeps it. A no-op when auto-lift is disabled (nodeBanDays<=0) - report-bans then
// clear only via admin /admin/unban-node or the appeal flow. The in-memory ban cache is
// refreshed for every node the sweep clears, so routing restores without a restart.
// stop is the nil-in-production test seam (a nil channel case never fires, so the loop
// waits on the ticker exactly as before).
func (b *broker) nodeBanSweep(stop <-chan struct{}) {
	if b.nodeBanDays <= 0 {
		log.Printf("node-ban: auto-lift DISABLED (ROGERAI_NODE_BAN_DAYS<=0) - report-bans clear only via admin /admin/unban-node or an appeal")
		return
	}
	if b.db == nil {
		return
	}
	window := time.Duration(b.nodeBanDays) * 24 * time.Hour
	interval := sweepInterval(window)
	log.Printf("node-ban: auto-lift ON - report-origin suspensions older than %d day(s) clear if no fresh corroboration (sweep every %s)", b.nodeBanDays, interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			b.nodeBanSweepOnce(time.Now().Add(-window))
		}
	}
}

// nodeBanSweepOnce auto-lifts report-origin node bans older than cutoff and drops the
// lifted ids from the in-memory ban set (one sweep iteration). Split out of the loop so
// the expiry + cache-eviction work is testable without the ticker.
func (b *broker) nodeBanSweepOnce(cutoff time.Time) {
	cleared, err := b.db.ExpireNodeBans(cutoff)
	if err != nil {
		log.Printf("node-ban: expiry sweep failed: %v", err)
		return
	}
	if len(cleared) == 0 {
		return
	}
	b.metricsMu.Lock()
	for _, id := range cleared {
		delete(b.banned, id)
	}
	b.metricsMu.Unlock()
	// Cross-instance: the sweep is a ban WRITE too — bump the shared rev so the peer re-pulls
	// and restores routing for the auto-lifted nodes (cleared is non-empty here).
	b.bumpBanRev()
	log.Printf("node-ban: auto-lifted %d report-origin suspension(s) older than %d day(s) (no further corroboration) - those nodes can route again", len(cleared), b.nodeBanDays)
}

// reportRequest is the POST /report contract (a web agent builds the UI to this exact
// shape). All fields except category are optional; category is one of the enumerated
// values (unknown values are accepted as "other" so the surface never hard-rejects a
// well-meant report).
type reportRequest struct {
	Category  string `json:"category"`
	NodeID    string `json:"node_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// validReportCategories is the enumerated set; anything else is normalized to "other".
var validReportCategories = map[string]bool{
	"abuse": true, "csam": true, "spam": true, "quality": true, "other": true,
}

// report (POST /report) is the public abuse/quality report endpoint. Anonymous is
// ALLOWED (the public surface), so no auth is required; it is rate-limited per IP
// (reusing the relay limiter) to prevent report-spam/abuse-of-reporting. It persists
// the report, maintains a per-node count, and auto-ejects a node once its report count
// crosses the configured threshold (reusing the ban/eject mechanism). Contract:
//
//	request : {"category":"abuse|csam|spam|quality|other","node_id":"<opt>","request_id":"<opt>","detail":"<free text>"}
//	response: 200 {"received":true}
func (b *broker) report(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	if !allow(w, r, http.MethodPost) {
		return
	}
	// Per-IP rate limit (reuse the relay limiter bucket map) to stop report-spam.
	ip := clientIP(r)
	if ok, retry := b.rl.allow("report:" + ip); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		jsonErr(w, http.StatusTooManyRequests, "too many reports - slow down")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	var req reportRequest
	if err := json.Unmarshal(body, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	cat := strings.ToLower(strings.TrimSpace(req.Category))
	if !validReportCategories[cat] {
		cat = "other"
	}
	// Bound the free-text detail so a report can't be used as a storage-abuse channel.
	detail := req.Detail
	if len(detail) > 4096 {
		detail = detail[:4096]
	}
	nodeID := strings.TrimSpace(req.NodeID)
	if _, err := b.db.AddReport(store.Report{
		Category: cat, NodeID: nodeID, RequestID: strings.TrimSpace(req.RequestID),
		Detail: detail, IP: ip,
	}); err != nil {
		log.Printf("report: persist failed: %v", err)
		jsonErr(w, http.StatusInternalServerError, "could not record report")
		return
	}
	log.Printf("report: category=%s node=%q request=%q ip=%s", cat, nodeID, strings.TrimSpace(req.RequestID), ip)

	// Per-node CORROBORATED auto-eject: a node is suspended only once enough DISTINCT
	// reporters (distinct reporter IPs) name it WITHIN the decay window - never on a raw
	// all-time count, so one source can't stack N reports (H2) and stale reports age out.
	// csam/quality/spam are evidence, not an auto-ban trigger (csam preserves+queues for
	// human review; quality/spam downrank via trust, not eject); only "abuse" corroborates
	// an auto-suspension. The resulting suspension is TIME-BOXED + appealable (banNode tags
	// it report-origin so nodeBanSweep auto-lifts it). Threshold 0 disables.
	if nodeID != "" && cat == "abuse" && b.reportEjectAt > 0 && !b.isBanned(nodeID) {
		since := int64(0)
		if b.reportDecayDays > 0 {
			since = time.Now().Add(-time.Duration(b.reportDecayDays) * 24 * time.Hour).Unix()
		}
		if n, err := b.db.DistinctReporterCountByNode(nodeID, since); err == nil && n >= b.reportEjectAt {
			b.banNode(nodeID, reportBanReasonPrefix+" ("+strconv.Itoa(n)+" distinct reporters)")
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"received": true})
}
