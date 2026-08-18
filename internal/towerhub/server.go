package towerhub

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
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
// node by a SIGNATURE over each request, made with the Station's assertion key (nodeauth.go),
// but reads no content.
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
	// OnTranscript, when set, observes a node's answered audit (see audit.go) - the tower's
	// courier forwards it to Core, tower-signed. Called on its own goroutine.
	OnTranscript func(stationID string, reply TranscriptReply)

	// towerID is THIS hub's tower id, as Core assigned it, and every signed request must name
	// it in its target. See nodeauth.go: the canonical string binds no host, and the nonce ring
	// lives in one process's memory, so without this a signature captured here was good at any
	// other hub process that had the same Station registered.
	//
	// It is set once at construction and never written again, which is why it is read without
	// the lock. An empty id is only reachable from a test or an embedded hub, and it is
	// compared with plain equality: a hub with no id is servable only by a client that names
	// no tower, and both halves come from Core in production.
	towerID string

	// allowLegacyBearer accepts the pre-signature bearer token from a node that does not sign
	// yet. It is a TRANSITION affordance with an end date, not a mode.
	//
	// The two programs update separately: `roger` is a provider's binary and `roger-tower` is
	// an operator's, so a v5.7.1 node that still presents a token can meet a hub built after
	// signatures landed. Refusing it would take a provider who did nothing off the fabric and
	// stop paying them, for a defect on our side of the wire. `roger-tower serve` exposes it as
	// --hub-legacy-bearer / hub.allowLegacyBearer, default on, so an operator who knows their
	// fleet has updated can end the tolerance early on their own tower.
	//
	// PER STATION IT ENDS SOONER THAN THAT, and has to: this being on does NOT mean a
	// registered token opens a queue. The moment a Station signs, its token stops working here
	// - see the latch in authNode, which is the fix for the hole this flag used to leave open
	// for every already-upgraded node on the tower.
	//
	// UNEXPORTED AND IMMUTABLE, deliberately. It was an exported field read under s.mu.RLock()
	// and written by nobody but a test, which is the shape of a data race waiting for the first
	// person to add a runtime toggle. There is no runtime toggle: it is a serve-time decision,
	// like every other listener setting, so it is fixed at construction and read without a
	// lock at all.
	//
	// DELETE IT, and NodeAuth.LegacyToken with it, one release after signed polls ship.
	allowLegacyBearer bool

	audit  auditPlane
	nonces nonceGate

	mu    sync.RWMutex
	nodes map[string]NodeAuth // stationID -> how its serving node authenticates
	// signed records the Stations this tower has seen produce a valid SIGNATURE. It is what
	// retires the legacy bearer per Station rather than per release (nodeauth.go), and it is
	// deliberately in-memory and per-process: a tower restart re-opens the tolerance until the
	// node's next poll, which is seconds away, and no operator should have to migrate a
	// database row for a credential that is being deleted.
	signed map[string]bool
}

// ServerOptions is everything a hub Server is configured with beyond its Hub and its grant
// checker. It is a struct rather than four more positional arguments because two of the four
// are security decisions - which tower this is, and whether a pre-signature node is tolerated -
// and a bool in the seventh position is how those get set wrong.
type ServerOptions struct {
	// TowerID is this hub's tower id, bound into every signed request. See Server.towerID.
	TowerID string
	// SubmitTTL and PollTTL are the consumer's wait and the node's long poll. Zero takes the
	// defaults.
	SubmitTTL time.Duration
	PollTTL   time.Duration
	// AllowLegacyBearer opts IN to the transition tolerance. The zero value refuses bearer
	// tokens, which is the state this whole change is heading for; `roger-tower` passes true
	// unless the operator turned it off. See Server.allowLegacyBearer.
	AllowLegacyBearer bool
}

// NewServer wires a Server over a Hub with a grant checker. Zero TTLs fall back to the defaults.
func NewServer(hub *Hub, check GrantCheck, opt ServerOptions) *Server {
	if opt.SubmitTTL <= 0 {
		opt.SubmitTTL = defaultSubmitTTL
	}
	if opt.PollTTL <= 0 {
		opt.PollTTL = defaultPollTTL
	}
	return &Server{hub: hub, check: check, submitTTL: opt.SubmitTTL, pollTTL: opt.PollTTL,
		towerID: opt.TowerID, allowLegacyBearer: opt.AllowLegacyBearer,
		// The replay gate refuses anything signed before this process began - see
		// nonceGate.since. A hub that restarts inside the skew window would otherwise accept
		// every signature captured before it went down, and a redeploy is not an attack an
		// operator should have to think of as one.
		// Truncated to the second because that is the resolution of what it is compared
		// against: protocol stamps a request in unix SECONDS, so a sub-second floor would
		// refuse every request signed in the second this process started.
		nonces: nonceGate{since: time.Now().Truncate(time.Second)},
		nodes:  map[string]NodeAuth{}, signed: map[string]bool{}}
}

// RegisterNode makes a Station servable and binds the credential its serving node
// authenticates with - now the Station's ASSERTION KEY (and, for one transition release, the
// bearer token an older node still presents). It is the tower's one-node-per-Station
// enforcement point (the Hub itself has no node identity): re-registering a Station replaces
// what authenticates it, so only the current node can poll it.
//
// It took a token string before. The signature changed rather than gaining an overload
// because there is no version of this call that should still be reachable with a secret and
// nothing else: the compiler finding every caller is the point.
func (s *Server) RegisterNode(stationID string, auth NodeAuth) {
	s.hub.Register(stationID)
	s.mu.Lock()
	// The refresher re-registers every Station every thirty seconds with the same answer, so
	// the "this Station signs" latch must SURVIVE a re-registration or it would be cleared
	// before it could ever protect anything. It is cleared on the one event that means a
	// different node is behind the Station: a different assertion key.
	if prior, had := s.nodes[stationID]; had && !prior.AssertionKey.Equal(auth.AssertionKey) {
		delete(s.signed, stationID)
	}
	s.nodes[stationID] = auth
	s.mu.Unlock()
}

// UnregisterNode removes a Station, its credential, its replay memory, and its audit wanted
// list (a station Core dropped must not keep a list a later re-registration could answer
// stale - audit M5).
func (s *Server) UnregisterNode(stationID string) {
	s.hub.Unregister(stationID)
	s.mu.Lock()
	delete(s.nodes, stationID)
	delete(s.signed, stationID)
	s.mu.Unlock()
	s.nonces.forget(stationID)
	s.audit.mu.Lock()
	delete(s.audit.wanted, stationID)
	s.audit.mu.Unlock()
}

// constantTimeEqual compares two secrets without leaking their divergence point through
// timing. Only the legacy bearer path needs it - a signature comparison is a public-key
// operation over public material - and it goes when that path does.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// maxGETBody is all the body a hub GET may carry, which is none - the cap exists so that
// reading it is free. It is read rather than ignored because the signature covers a digest of
// the body, and passing nil regardless of what arrived would make "the signature covers the
// body" true of only half the routes: a signed GET would carry any unsigned payload an on-path
// party cared to attach. Nothing reads that payload today, and it stays that way by being
// refused rather than by nobody having written the line yet.
const maxGETBody = 4 << 10

func readGETBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	return io.ReadAll(http.MaxBytesReader(w, r.Body, maxGETBody))
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

// Poll handles GET /poll?station=<id>&nonce=<hex>: the serving node long-polls for a job. It
// authenticates the node by a SIGNATURE over this exact request, made with the Station's
// assertion key, so only the node that owns a Station can pull its work - and so nothing an
// on-path observer captures can be used twice (nodeauth.go). 204 means "no job yet, poll
// again" (a normal long-poll timeout).
//
// This is the route replay protection exists for. Every other hub route is idempotent or a
// read; this one DEQUEUES, so a reused signature would take a job the attacker cannot open
// and the honest node therefore never serves - the denial-of-earnings attack, rebuilt on top
// of the fix for it.
func (s *Server) Poll(w http.ResponseWriter, r *http.Request) {
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
// attempt it pulled. Authenticated by the node's SIGNATURE over this request, body included, for
// the named Station. Delivering a wrong result cannot steal money - it is sealed to the consumer
// (who fails to open a forgery) and the receipt is node-signed and settled one-use at Core - but
// the signature still binds a completion to the Station's own node.
func (s *Server) Complete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	// THE CHEAP DOOR FIRST. The read below has to happen before authentication (see the note
	// on the raw bytes), which handed an unauthenticated stranger sixteen megabytes of this
	// tower's memory and two minutes of its read timeout for the price of one connection.
	// knownCredential answers what the headers alone can answer - is this anybody we have
	// registered - and refuses before a byte of body is buffered.
	if !s.knownCredential(r) {
		writeErr(w, http.StatusUnauthorized,
			"this request presents no credential this tower has registered for any Station")
		return
	}
	// THE RAW BYTES, KEPT. The signature covers a digest of the body exactly as it arrived, so
	// this reads once and hands the same slice to both the verifier and the decoder. Decoding
	// straight off the stream and re-serializing to check the signature would verify a
	// reconstruction rather than the request, and any encoder difference - field order, escaping,
	// whitespace - would show up as an authentication failure nobody could reproduce.
	raw, rerr := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if rerr != nil {
		writeErr(w, http.StatusBadRequest, "unreadable request body")
		return
	}
	var req completeReq
	if err := json.Unmarshal(raw, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if auth := s.authNode(r, req.StationID, raw); !auth.ok {
		writeErr(w, http.StatusUnauthorized, auth.why)
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
	carried, wireIn := false, 0
	if len(rec) > 0 {
		carried, wireIn = s.hub.ConsumeDispatched(req.AttemptID, req.StationID)
	}
	res.WireIn = wireIn // the hub's own count of the sealed request it relayed
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
