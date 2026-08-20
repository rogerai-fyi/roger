package towerhub

// audit.go is the hub's AUDIT PLANE: how Core's transcript wants reach a poll-only node and
// how the node's signed transcripts ride back. The classic courier dials a Station's own
// endpoint; a hub node has none - it only polls - so the hub carries the list the other way:
// the tower refreshes each Station's wanted attempts from Core, the node fetches its list on
// its poll cadence, and uploads Station-signed transcripts the tower forwards to Core. The
// hub stays blind to CONTENT it relayed sealed; the transcript is the node choosing to show
// its work to Core, and it crosses this hub only because Core cannot dial the node either.

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
)

// Audit-plane endpoint leaves, mounted beside the job paths.
const (
	PathAuditWanted     = "/audit/wanted"
	PathAuditTranscript = "/audit/transcript"
)

// TranscriptReply is one answered audit. On the hub path the whole payload rides SEALED to
// Roger Core's envelope key: the sealed submit path promised the tower never reads content,
// and an audit answer is content - handing it over plaintext (as the classic dial-out
// courier did) would un-blind exactly the attempts Core watches. Available=false is the node
// saying "not retained" - itself an answer, so Core need not wait out the deadline. The
// plaintext fields remain for the classic courier's shape and stay empty on the hub path.
type TranscriptReply struct {
	AttemptID string `json:"attempt_id"`
	Available bool   `json:"available"`
	// SealedBundle is base64 of an envelope sealed to Core's envelope key (AAD = attempt id)
	// holding {"transcript","request","response"} as base64 strings. Opaque to the tower.
	SealedBundle string `json:"sealed_bundle,omitempty"`
	Transcript   string `json:"transcript,omitempty"` // base64 of the Station-signed object (classic path)
	Request      string `json:"request,omitempty"`    // base64 plaintext (classic path)
	Response     string `json:"response,omitempty"`   // base64 plaintext (classic path)
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

// AuditWanted handles GET /audit/wanted?station=&nonce=: the node's view of what Core wants
// from it. Authenticated exactly as Poll is - a signature over this request with the Station's
// assertion key, so only the Station's own node may read its list.
func (s *Server) AuditWanted(w http.ResponseWriter, r *http.Request) {
	s.stampEpoch(w, r)
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	stationID := r.URL.Query().Get("station")
	body, berr := readGETBody(w, r)
	if berr != nil {
		writeErr(w, http.StatusBadRequest, "a hub GET carries no body")
		return
	}
	if auth := s.authNode(r, stationID, body); !auth.ok {
		writeErr(w, http.StatusUnauthorized, auth.why)
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
	s.stampEpoch(w, r)
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		StationID string `json:"station_id"`
		TranscriptReply
	}
	// 8MB: Roger Core's own tower-body cap. Accepting more here would take uploads that can
	// never survive the forward, and the node would burn bandwidth re-answering a want that
	// cannot close until its deadline lapses (audit M4). An attempt whose plaintext exceeds
	// this simply misses its audit - the miss rules decide what that means.
	//
	// Read whole rather than streamed into the decoder, because the request signature covers a
	// digest of these exact bytes - see Complete for why a re-serialization will not do. And
	// refused before that read if the caller is nobody we have registered - see knownCredential,
	// which is what stops a stranger making this tower buffer eight megabytes for free.
	if !s.knownCredential(r) {
		writeErr(w, http.StatusUnauthorized,
			"this request presents no credential this tower has registered for any Station")
		return
	}
	raw, rerr := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if rerr != nil {
		writeErr(w, http.StatusBadRequest, "unreadable request body")
		return
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if auth := s.authNode(r, req.StationID, raw); !auth.ok {
		writeErr(w, http.StatusUnauthorized, auth.why)
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
	// The payload is forwarded as-is: Core validates the base64 AND the signatures, and
	// refuses garbage without resolving the want - decoding megabytes here just to throw the
	// bytes away was pure allocation (audit L1).
	if s.OnTranscript != nil {
		go s.OnTranscript(req.StationID, req.TranscriptReply)
	}
	writeJSON(w, http.StatusOK, map[string]any{"forwarded": true})
}
