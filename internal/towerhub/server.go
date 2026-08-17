package towerhub

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"
)

// GrantCheck verifies a consumer's submitted grant and returns the attempt id + Station it
// authorizes, or an error. The tower injects one bound to Roger Core's public key (backed by
// dispatch.EdgeGrantMeta); it reads only the grant's PUBLIC metadata - signature, attempt,
// Station, deadline - never the sealed request the grant protects. This is what lets the tower
// reject unauthorized/expired submits (abuse control) while staying blind to content.
type GrantCheck func(grant []byte) (attemptID, stationID string, err error)

// Default timeouts. Submit blocks up to submitTTL for a serving node to answer; Poll is a long
// poll bounded by pollTTL, after which the node simply polls again.
const (
	defaultSubmitTTL = 90 * time.Second
	defaultPollTTL   = 25 * time.Second
)

// Canonical endpoint sub-paths, shared by the Server (mount) and the Client (call) so the two
// sides cannot drift. The mount decides the prefix; these are the leaves under it.
const (
	PathSubmit   = "/submit"
	PathPoll     = "/poll"
	PathComplete = "/complete"
)

// Server exposes a Hub over HTTP: consumers submit sealed jobs, serving nodes long-poll for
// them and return sealed results. It is the tower's data-plane face - the broker never appears
// in it. The tower authorizes a submit by the Core-signed grant and authenticates a polling
// node by the per-registration bearer token, but reads no content.
type Server struct {
	hub       *Hub
	check     GrantCheck
	submitTTL time.Duration
	pollTTL   time.Duration
	// OnComplete, when set, observes every completed result AFTER delivery is attempted - the
	// tower's settle courier hangs here (it forwards the opaque receipt to Roger Core). It
	// receives only what the tower may see: the station, the attempt, and the sealed/signed
	// blobs it cannot read. Called on its own goroutine; it must not block the handler.
	// Fires ONLY for attempts this hub actually dispatched to that Station (audit L2): a node
	// fabricating attempt ids cannot ride the tower's signature to Core.
	OnComplete func(stationID string, res Result)
	// OnUnknownStation, when set, observes a Submit refused with ErrNoStation (audit M3): the
	// tower hangs an immediate node-registration refresh here so a freshly self-attached node
	// becomes servable without waiting out the periodic refresh. Called on its own goroutine;
	// the caller is responsible for rate-limiting.
	OnUnknownStation func(stationID string)

	mu     sync.RWMutex
	tokens map[string]string // stationID -> the serving node's bearer token
}

// NewServer wires a Server over a Hub with a grant checker. Zero TTLs fall back to the defaults.
func NewServer(hub *Hub, check GrantCheck, submitTTL, pollTTL time.Duration) *Server {
	if submitTTL <= 0 {
		submitTTL = defaultSubmitTTL
	}
	if pollTTL <= 0 {
		pollTTL = defaultPollTTL
	}
	return &Server{hub: hub, check: check, submitTTL: submitTTL, pollTTL: pollTTL, tokens: map[string]string{}}
}

// RegisterNode makes a Station servable and binds the bearer token its serving node authenticates
// with. It is the tower's one-node-per-Station enforcement point (the Hub itself has no node
// identity): re-registering a Station replaces its token, so only the current node can poll it.
func (s *Server) RegisterNode(stationID, token string) {
	s.hub.Register(stationID)
	s.mu.Lock()
	s.tokens[stationID] = token
	s.mu.Unlock()
}

// UnregisterNode removes a Station and its token.
func (s *Server) UnregisterNode(stationID string) {
	s.hub.Unregister(stationID)
	s.mu.Lock()
	delete(s.tokens, stationID)
	s.mu.Unlock()
}

func (s *Server) authNode(stationID, token string) bool {
	s.mu.RLock()
	want, ok := s.tokens[stationID]
	s.mu.RUnlock()
	if !ok || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(token)) == 1
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) > len(p) && h[:len(p)] == p {
		return h[len(p):]
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

type submitReq struct {
	Grant    string `json:"grant"`    // base64 of the Core-signed edge grant
	Envelope string `json:"envelope"` // base64 of the request sealed to the node's session key
}
type submitResp struct {
	Envelope string `json:"envelope,omitempty"` // base64, sealed to the consumer
	Receipt  string `json:"receipt,omitempty"`  // base64, the node-signed token receipt
	Failure  string `json:"failure,omitempty"`
}

// Submit handles POST /submit: a consumer hands the tower a Core-signed grant + a sealed request,
// and blocks until the serving node answers or the deadline passes. The tower verifies the grant
// (Core-signed, names a Station, not expired) and routes by the grant's OWN attempt/Station - never
// values the client supplies alongside - so a forged or mismatched claim cannot misroute or settle.
func (s *Server) Submit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req submitReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	grant, err := base64.StdEncoding.DecodeString(req.Grant)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "grant is not valid base64")
		return
	}
	envelope, err := base64.StdEncoding.DecodeString(req.Envelope)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "envelope is not valid base64")
		return
	}
	// AUTHORIZE + ROUTE BY THE GRANT ITSELF. The attempt id and Station come from the verified
	// grant, not the request body, so a consumer cannot point a real grant at another Station or
	// claim an attempt id the grant does not authorize.
	attemptID, stationID, cerr := s.check(grant)
	if cerr != nil {
		writeErr(w, http.StatusForbidden, "this grant is not a valid authorization")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.submitTTL)
	defer cancel()
	res, serr := s.hub.Submit(ctx, Job{AttemptID: attemptID, StationID: stationID, Grant: grant, Envelope: envelope})
	switch {
	case serr == nil:
		writeJSON(w, http.StatusOK, submitResp{
			Envelope: base64.StdEncoding.EncodeToString(res.Envelope),
			Receipt:  base64.StdEncoding.EncodeToString(res.Receipt),
			Failure:  res.Failure,
		})
	case errors.Is(serr, ErrNoStation):
		if s.OnUnknownStation != nil {
			go s.OnUnknownStation(stationID)
		}
		writeErr(w, http.StatusNotFound, "no node is serving this Station on this tower")
	case errors.Is(serr, ErrDuplicateAttempt):
		writeErr(w, http.StatusConflict, "this attempt is already in flight")
	case errors.Is(serr, ErrEmptyID):
		writeErr(w, http.StatusBadRequest, "the grant names no attempt or Station")
	default: // context deadline / cancel
		writeErr(w, http.StatusGatewayTimeout, "the serving node did not answer in time")
	}
}

type pollResp struct {
	AttemptID string `json:"attempt_id"`
	StationID string `json:"station_id"`
	Grant     string `json:"grant"`    // base64
	Envelope  string `json:"envelope"` // base64
}

// Poll handles GET /poll?station=<id>: the serving node long-polls for a job. It authenticates
// the node by its per-registration bearer token, so only the node that owns a Station can pull
// its work. 204 means "no job yet, poll again" (a normal long-poll timeout).
func (s *Server) Poll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	stationID := r.URL.Query().Get("station")
	if !s.authNode(stationID, bearer(r)) {
		writeErr(w, http.StatusUnauthorized, "not the registered node for this Station")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.pollTTL)
	defer cancel()
	job, ok := s.hub.Poll(ctx, stationID)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, pollResp{
		AttemptID: job.AttemptID, StationID: job.StationID,
		Grant:    base64.StdEncoding.EncodeToString(job.Grant),
		Envelope: base64.StdEncoding.EncodeToString(job.Envelope),
	})
}

type completeReq struct {
	AttemptID string `json:"attempt_id"`
	StationID string `json:"station_id"`
	Envelope  string `json:"envelope,omitempty"` // base64, sealed to the consumer
	Receipt   string `json:"receipt,omitempty"`  // base64
	Failure   string `json:"failure,omitempty"`
}

// Complete handles POST /complete: the serving node returns a sealed result + receipt for an
// attempt it pulled. Authenticated by the node's token for the named Station. Delivering a wrong
// result cannot steal money - it is sealed to the consumer (who fails to open a forgery) and the
// receipt is node-signed and settled one-use at Core - but the token still binds a completion to
// the Station's own node.
func (s *Server) Complete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req completeReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !s.authNode(req.StationID, bearer(r)) {
		writeErr(w, http.StatusUnauthorized, "not the registered node for this Station")
		return
	}
	env, err := base64.StdEncoding.DecodeString(req.Envelope)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "envelope is not valid base64")
		return
	}
	rec, err := base64.StdEncoding.DecodeString(req.Receipt)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "receipt is not valid base64")
		return
	}
	// Bound to the authenticated Station: hub.Complete drops a result whose attempt does not
	// belong to req.StationID, so a node cannot resolve another Station's attempt.
	res := Result{AttemptID: req.AttemptID, Envelope: env, Receipt: rec, Failure: req.Failure}
	// THE COURIER GATE (audit L2): only completions for attempts this hub actually HANDED to
	// this Station ride to Core. Checked before Complete (which consumes the waiter but not the
	// dispatch record) so a consumer who gave up waiting still gets the node paid - the node
	// honestly did the work - while a fabricated attempt id goes nowhere.
	// Consumed only when there is a receipt to courier: a failure completion must not burn
	// the record a node's later receipt-bearing retry would need.
	carried := false
	if len(rec) > 0 {
		carried = s.hub.ConsumeDispatched(req.AttemptID, req.StationID)
	}
	s.hub.Complete(req.StationID, res)
	if s.OnComplete != nil && len(rec) > 0 && carried {
		go s.OnComplete(req.StationID, res)
	}
	if len(rec) > 0 && !carried {
		// HONEST ANSWER (audit H-2): a receipt for an attempt this hub has no record of
		// handing out - a fabrication, or a hub that restarted between Poll and Complete -
		// is accepted but NOT couriered, and a plain 200 would tell the node its pay is on
		// its way when it is not. 202 lets an honest node's serve loop log the risk loudly.
		writeJSON(w, http.StatusAccepted, map[string]any{
			"carried": false,
			"note": "this hub has no dispatch record for that attempt - the receipt was not " +
				"forwarded for settlement (a fabricated id, or the hub restarted mid-job)",
		})
		return
	}
	w.WriteHeader(http.StatusOK)
}
