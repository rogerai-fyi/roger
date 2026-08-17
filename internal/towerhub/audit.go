package towerhub

// audit.go is the hub's AUDIT PLANE: how Core's transcript wants reach a poll-only node and
// how the node's signed transcripts ride back. The classic courier dials a Station's own
// endpoint; a hub node has none - it only polls - so the hub carries the list the other way:
// the tower refreshes each Station's wanted attempts from Core, the node fetches its list on
// its poll cadence, and uploads Station-signed transcripts the tower forwards to Core. The
// hub stays blind to CONTENT it relayed sealed; the transcript is the node choosing to show
// its work to Core, and it crosses this hub only because Core cannot dial the node either.

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sync"
)

// Audit-plane endpoint leaves, mounted beside the job paths.
const (
	PathAuditWanted     = "/audit/wanted"
	PathAuditTranscript = "/audit/transcript"
)

// TranscriptReply is one answered audit: the Station-signed transcript object plus the
// plaintext bytes Core re-hashes against the receipt's digests. Available=false is the
// node saying "not retained" - itself an answer, so Core need not wait out the deadline.
type TranscriptReply struct {
	AttemptID  string `json:"attempt_id"`
	Available  bool   `json:"available"`
	Transcript string `json:"transcript"` // base64 of the Station-signed object
	Request    string `json:"request"`    // base64 plaintext
	Response   string `json:"response"`   // base64 plaintext
}

// auditPlane is the Server's wanted-list state, per Station.
type auditPlane struct {
	mu     sync.Mutex
	wanted map[string][]string // stationID -> attempt ids Core wants
}

// SetWanted replaces a Station's wanted list - the tower's refresher calls it with what Core
// answered. Replacement (not merge) keeps the hub's copy exactly as stale as Core's answer.
func (s *Server) SetWanted(stationID string, attempts []string) {
	s.audit.mu.Lock()
	defer s.audit.mu.Unlock()
	if s.audit.wanted == nil {
		s.audit.wanted = map[string][]string{}
	}
	if len(attempts) == 0 {
		delete(s.audit.wanted, stationID)
		return
	}
	s.audit.wanted[stationID] = attempts
}

// AuditWanted handles GET /audit/wanted?station=: the node's view of what Core wants from it.
// Authenticated exactly as Poll is - only the Station's own node may read its list.
func (s *Server) AuditWanted(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	stationID := r.URL.Query().Get("station")
	if !s.authNode(stationID, bearer(r)) {
		writeErr(w, http.StatusUnauthorized, "not the registered node for this Station")
		return
	}
	s.audit.mu.Lock()
	attempts := append([]string(nil), s.audit.wanted[stationID]...)
	s.audit.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"wanted": attempts})
}

// AuditTranscript handles POST /audit/transcript: the node answers a want. The hub forwards
// via OnTranscript (the tower's courier to Core) and clears the want so the node is not
// asked again; Core's own resolve is the authoritative close either way.
func (s *Server) AuditTranscript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		StationID string `json:"station_id"`
		TranscriptReply
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !s.authNode(req.StationID, bearer(r)) {
		writeErr(w, http.StatusUnauthorized, "not the registered node for this Station")
		return
	}
	if req.AttemptID == "" {
		writeErr(w, http.StatusBadRequest, "a transcript names its attempt")
		return
	}
	// Only an attempt the tower actually listed for THIS station rides to Core - the same
	// discipline as the settle-courier gate: a node cannot use the tower's signature to
	// spray Core with answers to audits nobody asked it for.
	s.audit.mu.Lock()
	listed := false
	kept := s.audit.wanted[req.StationID][:0]
	for _, id := range s.audit.wanted[req.StationID] {
		if id == req.AttemptID {
			listed = true
			continue
		}
		kept = append(kept, id)
	}
	if listed {
		s.audit.wanted[req.StationID] = kept
	}
	s.audit.mu.Unlock()
	if !listed {
		writeJSON(w, http.StatusAccepted, map[string]any{"forwarded": false,
			"note": "this attempt is not on the wanted list for that Station"})
		return
	}
	// Shape sanity before the courier spends a signed call on it.
	for _, b64 := range []string{req.Transcript, req.Request, req.Response} {
		if _, err := base64.StdEncoding.DecodeString(b64); err != nil {
			writeErr(w, http.StatusBadRequest, "transcript fields must be base64")
			return
		}
	}
	if s.OnTranscript != nil {
		go s.OnTranscript(req.StationID, req.TranscriptReply)
	}
	writeJSON(w, http.StatusOK, map[string]any{"forwarded": true})
}
