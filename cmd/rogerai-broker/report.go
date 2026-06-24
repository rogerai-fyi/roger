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

	"github.com/rogerai-fyi/roger/internal/store"
)

// report.go is the safety surface: the CSAM preserve+queue path (18 USC 2258A), the
// public POST /report abuse endpoint, and the report-threshold node ban/eject that
// reuses the probe-eject idea (a banned node is treated as not-serving in pick).

// defaultReportEjectAt is the per-node report count that auto-ejects a node from
// routing. Override with ROGERAI_REPORT_EJECT_AT (0 disables auto-eject; a node can
// still be manually banned).
const defaultReportEjectAt = 5

func reportEjectThreshold() int {
	if v := os.Getenv("ROGERAI_REPORT_EJECT_AT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return defaultReportEjectAt
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
		log.Printf("ban: node=%s EJECTED from routing (%s)", nodeID, reason)
	}
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

	// Per-node threshold auto-eject: once a node accumulates enough reports it is flipped
	// OUT of pick/market/discover (reuses the ban/eject mechanism). Threshold 0 disables.
	if nodeID != "" && b.reportEjectAt > 0 && !b.isBanned(nodeID) {
		if n, err := b.db.ReportCountByNode(nodeID); err == nil && n >= b.reportEjectAt {
			b.banNode(nodeID, "report threshold ("+strconv.Itoa(n)+" reports)")
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"received": true})
}
