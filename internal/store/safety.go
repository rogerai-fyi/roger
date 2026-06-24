package store

import (
	"sort"
	"time"
)

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
	Content     []byte `json:"-"`            // broker-ENCRYPTED offending prompt (ciphertext)
	ReportState string `json:"report_state"` // "queued" -> "reported"
	CreatedAt   int64  `json:"created_at"`   // unix seconds
}

// CSAM report obligation states.
const (
	CSAMQueued   = "queued"   // a CyberTipline report is owed (preserved, not yet filed)
	CSAMReported = "reported" // the report has been filed
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

func (m *Mem) BanNode(nodeID, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.banned == nil {
		m.banned = map[string]string{}
	}
	if _, ok := m.banned[nodeID]; !ok {
		m.banned[nodeID] = reason
	}
	return nil
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
