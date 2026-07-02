package store

import (
	"errors"
	"sort"
	"strings"
	"time"
)

// errEmptyReportID rejects a CyberTipline submission with no report id (nothing to record).
var errEmptyReportID = errors.New("cybertipline report id required")

// safety.go adds the two access-controlled safety tables the broker writes when its
// moderation screen fires: csam_incidents (preserved child-exploitation hits, kept for
// a mandated CyberTipline report, 18 USC 2258A) and reports (the public abuse/report
// endpoint + the per-node ban flow). Both mirror the additive grant/store patterns
// (a Mem map set + a Postgres table). The broker stays content-blind for ordinary
// traffic: only a CSAM hit preserves content, and that content is ENCRYPTED by the
// broker before it ever reaches the store (Content holds ciphertext, not plaintext).

// CSAMIncident is one preserved child-exploitation hit. It is deliberately minimal and
// access-restricted: the offending prompt is stored ENCRYPTED-AT-REST (Content is the
// broker-encrypted blob, never plaintext), alongside the per-(user,node) pseudonym, the
// caller IP, the matched policy category, and a timestamp. ReportState tracks the
// CyberTipline obligation: it starts "queued" (a report is owed) and a follow-up
// submitter flips it to "reported". Retention is bounded (RetentionCutoff): rows past
// the window are purged once their report obligation is satisfied.
type CSAMIncident struct {
	ID          int64  `json:"id"`
	Pseudonym   string `json:"pseudonym"`    // opaque per-(user,node) id; never the real user
	IP          string `json:"ip"`           // caller IP (for a CyberTipline report)
	Category    string `json:"category"`     // matched CSAM policy category (e.g. "S4")
	Content     []byte `json:"-"`            // broker-ENCRYPTED offending prompt (ciphertext); never serialized
	ReportState string `json:"report_state"` // "queued" -> "reported"
	// ReportID is the CyberTipline report id recorded when the incident is submitted (the
	// permanent proof the 18 USC 2258A obligation was met); ReportedAt/ReportedBy are the
	// submission time + the admin identity that filed it (the durable audit trail).
	ReportID   string `json:"report_id,omitempty"`
	ReportedAt int64  `json:"reported_at,omitempty"`
	ReportedBy string `json:"reported_by,omitempty"`
	CreatedAt  int64  `json:"created_at"` // unix seconds
}

// CSAM report obligation states.
const (
	CSAMQueued   = "queued"   // a CyberTipline report is owed (preserved, not yet filed)
	CSAMReported = "reported" // the report has been filed (submitted, with a CyberTipline id)
)

// Report is one abuse/quality report submitted to POST /report. Reports may be
// anonymous (the public surface), so no identity is required; the per-node count drives
// the auto-eject/ban threshold. Category is one of abuse|csam|spam|quality|other.
type Report struct {
	ID        int64  `json:"id"`
	Category  string `json:"category"`
	NodeID    string `json:"node_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	Detail    string `json:"detail,omitempty"`
	IP        string `json:"ip,omitempty"` // reporter IP (abuse-of-reporting forensics)
	CreatedAt int64  `json:"created_at"`
}

// --- Mem safety storage ---------------------------------------------------
//
// Mirrors owners/nodeAcct: small maps under m.mu (these ops are rare and off the hot
// path, so sharing the main lock is fine).

func (m *Mem) PreserveCSAM(inc CSAMIncident) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inc.CreatedAt == 0 {
		inc.CreatedAt = time.Now().Unix()
	}
	if inc.ReportState == "" {
		inc.ReportState = CSAMQueued
	}
	m.csamID++
	inc.ID = m.csamID
	m.csam = append(m.csam, inc)
	return inc.ID, nil
}

func (m *Mem) PendingCSAMReports(limit int) ([]CSAMIncident, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []CSAMIncident
	for i := len(m.csam) - 1; i >= 0; i-- {
		if m.csam[i].ReportState == CSAMQueued {
			out = append(out, m.csam[i])
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *Mem) MarkCSAMReported(id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.csam {
		if m.csam[i].ID == id {
			m.csam[i].ReportState = CSAMReported
			return nil
		}
	}
	return nil
}

// MarkCSAMSubmitted records that incident `id` was filed with CyberTipline report id
// `reportID` by admin `adminID`. Idempotent + monotonic: an already-submitted incident is
// a no-op that returns its EXISTING report id (never a second report / never un-submits).
// found=false means no such incident (caller 404s). An empty reportID is an error.
func (m *Mem) MarkCSAMSubmitted(id int64, reportID, adminID string, now time.Time) (inc CSAMIncident, found bool, err error) {
	if strings.TrimSpace(reportID) == "" {
		return CSAMIncident{}, false, errEmptyReportID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.csam {
		if m.csam[i].ID != id {
			continue
		}
		if m.csam[i].ReportState == CSAMReported {
			return redactCSAM(m.csam[i]), true, nil // idempotent: keep the original report id
		}
		m.csam[i].ReportState = CSAMReported
		m.csam[i].ReportID = reportID
		m.csam[i].ReportedAt = now.Unix()
		m.csam[i].ReportedBy = adminID
		return redactCSAM(m.csam[i]), true, nil
	}
	return CSAMIncident{}, false, nil
}

// CSAMQueueStats returns the number of incidents still owing a report and the age (seconds)
// of the OLDEST queued one - the backlog signal for the admin surface + the boot warning.
func (m *Mem) CSAMQueueStats(now time.Time) (depth int, oldestAgeSecs int64, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var oldest int64
	for _, inc := range m.csam {
		if inc.ReportState != CSAMQueued {
			continue
		}
		depth++
		if oldest == 0 || inc.CreatedAt < oldest {
			oldest = inc.CreatedAt
		}
	}
	if oldest > 0 {
		oldestAgeSecs = now.Unix() - oldest
	}
	return depth, oldestAgeSecs, nil
}

// CSAMContentRetained reports whether incident `id`'s preserved (encrypted) content is
// still on file - the retention job's read (evidence must outlive the report for the legal
// window, 18 USC 2258A(h)).
func (m *Mem) CSAMContentRetained(id int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inc := range m.csam {
		if inc.ID == id {
			return len(inc.Content) > 0, nil
		}
	}
	return false, nil
}

// redactCSAM copies an incident WITHOUT its encrypted content - the admin surface returns
// metadata only, never the preserved material or any decryption input.
func redactCSAM(inc CSAMIncident) CSAMIncident {
	inc.Content = nil
	return inc
}

func (m *Mem) AddReport(r Report) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.CreatedAt == 0 {
		r.CreatedAt = time.Now().Unix()
	}
	m.reportID++
	r.ID = m.reportID
	m.reports = append(m.reports, r)
	return r.ID, nil
}

func (m *Mem) ReportCountByNode(nodeID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.reports {
		if r.NodeID == nodeID {
			n++
		}
	}
	return n, nil
}

// DistinctReporterCountByNode counts DISTINCT non-empty reporter IPs that named a node at
// or after `since` (the corroboration-and-decay count: one IP counts once, stale reports
// age out). See the interface doc.
func (m *Mem) DistinctReporterCountByNode(nodeID string, since int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	for _, r := range m.reports {
		if r.NodeID != nodeID || r.IP == "" {
			continue
		}
		if since > 0 && r.CreatedAt < since {
			continue
		}
		seen[r.IP] = true
	}
	return len(seen), nil
}

func (m *Mem) BanNode(nodeID, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.banned == nil {
		m.banned = map[string]string{}
	}
	if m.bannedAt == nil {
		m.bannedAt = map[string]int64{}
	}
	if _, ok := m.banned[nodeID]; !ok {
		m.banned[nodeID] = reason
		m.bannedAt[nodeID] = time.Now().Unix()
	}
	return nil
}

// UnbanNode lifts a node ban (admin node-unban / appeal auto-exoneration). Idempotent.
func (m *Mem) UnbanNode(nodeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.banned, nodeID)
	delete(m.bannedAt, nodeID)
	return nil
}

// ExpireNodeBans auto-lifts report-origin node suspensions placed at or before olderThan
// (the node twin of ExpireRecountHolds). Only report-origin bans (reason prefix "report ")
// auto-clear; an admin/crypto-verified permanent ban is never lifted here.
func (m *Mem) ExpireNodeBans(olderThan time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cut := olderThan.Unix()
	var cleared []string
	for id, reason := range m.banned {
		if !strings.HasPrefix(reason, "report ") {
			continue // permanent (admin/crypto) ban: never auto-lifted
		}
		if at, ok := m.bannedAt[id]; ok && at <= cut {
			cleared = append(cleared, id)
		}
	}
	for _, id := range cleared {
		delete(m.banned, id)
		delete(m.bannedAt, id)
	}
	return cleared, nil
}

func (m *Mem) BannedNodes() (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.banned))
	for k, v := range m.banned {
		out[k] = v
	}
	return out, nil
}

// --- owner-keyed durable bans + strikes (anti-rotation) -------------------

func (m *Mem) OwnerStrike(accountID, kind, evidenceJSON, idemKey string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if accountID == "" {
		return 0, nil
	}
	// Idempotency: a retried request must not double-strike. The idem key is recorded
	// in the same map the ledger uses, so a duplicate is a no-op (the count is returned
	// as-is so callers stay deterministic).
	if idemKey != "" {
		if m.idem["strike:"+idemKey] {
			return m.ownerStrikeCountLocked(accountID), nil
		}
		m.idem["strike:"+idemKey] = true
	}
	m.strikeID++
	m.strikes = append(m.strikes, Strike{
		ID: m.strikeID, AccountID: accountID, Kind: kind, Evidence: evidenceJSON,
		CreatedAt: time.Now().Unix(),
	})
	return m.ownerStrikeCountLocked(accountID), nil
}

func (m *Mem) ownerStrikeCountLocked(accountID string) int {
	n := 0
	for _, s := range m.strikes {
		if s.AccountID == accountID {
			n++
		}
	}
	return n
}

func (m *Mem) StrikesByOwner(accountID string, limit int) ([]Strike, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Strike
	for i := len(m.strikes) - 1; i >= 0; i-- {
		if m.strikes[i].AccountID == accountID {
			out = append(out, m.strikes[i])
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *Mem) BanOwner(accountID, reason, evidenceJSON string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if accountID == "" {
		return nil
	}
	if m.bannedOwners == nil {
		m.bannedOwners = map[string]string{}
	}
	if _, ok := m.bannedOwners[accountID]; !ok {
		m.bannedOwners[accountID] = reason // first ban wins; evidence preserved in strikes
		// Record the ban itself as a (terminal) strike so the evidence trail shows it.
		m.strikeID++
		m.strikes = append(m.strikes, Strike{
			ID: m.strikeID, AccountID: accountID, Kind: "ban:" + reason,
			Evidence: evidenceJSON, CreatedAt: time.Now().Unix(),
		})
	}
	return nil
}

func (m *Mem) IsOwnerBanned(accountID string) (bool, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if accountID == "" {
		return false, "", nil
	}
	r, ok := m.bannedOwners[accountID]
	return ok, r, nil
}

func (m *Mem) BannedOwners() (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.bannedOwners))
	for k, v := range m.bannedOwners {
		out[k] = v
	}
	return out, nil
}

func (m *Mem) SetAccountRecountHold(accountID string, held bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if accountID == "" {
		return nil
	}
	if m.accountHold == nil {
		m.accountHold = map[string]int64{}
	}
	if held {
		// Record (or refresh) the held-at time so a re-flagged owner re-arms auto-expiry.
		m.accountHold[accountID] = time.Now().Unix()
	} else {
		delete(m.accountHold, accountID)
	}
	return nil
}

// ForgiveOwner reverses all durable anti-abuse state against an owner after admin
// review: deletes its strikes, lifts the owner ban, and clears the account hold.
// Returns the number of strikes forgiven. Idempotent.
func (m *Mem) ForgiveOwner(accountID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if accountID == "" {
		return 0, nil
	}
	kept := m.strikes[:0]
	forgiven := 0
	for _, s := range m.strikes {
		if s.AccountID == accountID {
			forgiven++
			continue
		}
		kept = append(kept, s)
	}
	m.strikes = kept
	delete(m.bannedOwners, accountID)
	delete(m.accountHold, accountID)
	return forgiven, nil
}

// OwnerStrikeStats returns the decay-windowed strike count + distinct signal classes for
// an owner (the reliability inputs to strike(): decay + corroboration). Terminal "ban:*"
// marker strikes are excluded. since<=0 counts all strikes.
func (m *Mem) OwnerStrikeStats(accountID string, since int64) (windowed, distinctKinds int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kinds := map[string]bool{}
	for _, s := range m.strikes {
		if s.AccountID != accountID || strings.HasPrefix(s.Kind, "ban:") {
			continue
		}
		if since > 0 && s.CreatedAt < since {
			continue
		}
		windowed++
		kinds[s.Kind] = true
	}
	return windowed, len(kinds), nil
}

// AddAppeal records one owner-filed appeal (state "open"). Owner-scoped by AccountID.
func (m *Mem) AddAppeal(a Appeal) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.CreatedAt == 0 {
		a.CreatedAt = time.Now().Unix()
	}
	if a.State == "" {
		a.State = AppealOpen
	}
	m.appealID++
	a.ID = m.appealID
	m.appeals = append(m.appeals, a)
	return a.ID, nil
}

// AppealsByOwner lists an owner's appeals, newest first (owner-scoped status surface).
func (m *Mem) AppealsByOwner(accountID string, limit int) ([]Appeal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Appeal
	for i := len(m.appeals) - 1; i >= 0; i-- {
		if m.appeals[i].AccountID == accountID {
			out = append(out, m.appeals[i])
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

// PendingAppeals lists OPEN appeals across all accounts, newest first (admin queue).
func (m *Mem) PendingAppeals(limit int) ([]Appeal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Appeal
	for i := len(m.appeals) - 1; i >= 0; i-- {
		if m.appeals[i].State == AppealOpen {
			out = append(out, m.appeals[i])
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

// ReportsByNode lists reports for a node, newest first (admin/dashboard helper).
func (m *Mem) ReportsByNode(nodeID string, limit int) ([]Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Report
	for i := len(m.reports) - 1; i >= 0; i-- {
		if m.reports[i].NodeID == nodeID {
			out = append(out, m.reports[i])
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}
