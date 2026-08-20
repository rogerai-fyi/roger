package towerhub

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
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

	// epoch names THIS RUN of this hub, and every signed request must carry it. It is minted
	// once at construction, never written again, and read without the lock for the same reason
	// towerID is.
	//
	// It exists because a tower id is stable across a redeploy and the nonce ring is not. A hub
	// that restarts inside the five-minute skew window remembers no nonce, so every signature
	// an attacker captured before it went down was good again - the hole the gate used to
	// paper over with a wall-clock floor that could not work (see nodeauth.go). Naming the
	// process in the signature closes it without consulting a clock: the captured request is
	// signed for a run that has ended, and no timestamp can make it otherwise.
	//
	// It is PUBLIC. Every node-facing response carries it in HubEpochHeader, because a client
	// has no other way to learn it - Core assigns tower ids and knows nothing about restarts -
	// and because knowing it buys an attacker nothing. What an attacker cannot do is sign over
	// it.
	//
	// A note the doc's §5.4b table left open: two hub processes behind one endpoint now have
	// two epochs, so a signature made for one is refused by the other rather than silently
	// replayable at it. That configuration is still unsupported - it will flap - but it fails
	// closed instead of failing open, which is the better of the two ways to be unsupported.
	epoch string

	// epochKey is THIS TOWER'S ADMITTED IDENTITY KEY, and it is what makes the epoch above
	// worth having.
	//
	// The epoch is published on an unauthenticated 401 over a plaintext channel, so a client
	// that believed it was believing whoever answered the socket - and re-signing over an
	// attacker's chosen value, which is a genuine unconsumed signature rather than a replay.
	// This key signs the epoch (hubEpochStatement, bound to the refused request's own nonce),
	// and Core hands the node this key's fingerprint in the attach response, so the node checks
	// the epoch against material it got from Core rather than from the relay. See
	// HubKeyHeader.
	//
	// It is the SAME key Core admitted this tower under - roger-tower passes
	// tower.State.IdentityKey() - deliberately, because that is the only key both ends already
	// have a trusted path to. Nothing new is enrolled, distributed or rotated, exactly as
	// nothing was when node authentication moved onto the Station's assertion key.
	//
	// NewServer mints an ephemeral one when the caller supplies none, rather than leaving the
	// hub unable to prove itself: an embedded hub or a test then still exercises the real path,
	// and a caller that forgot the key gets a hub whose epoch no node will adopt (loudly, on
	// the node's notice channel) instead of a hub that silently reopens the hole.
	epochKey    ed25519.PrivateKey
	epochPubHex string

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
	//
	// IT IS SET-ONLY WITHIN A PROCESS. Nothing deletes from it - not a re-registration, not an
	// unregistration - because every event that used to was a registration FLAP rather than
	// evidence that the node behind the Station had changed, and un-latching on a flap hands
	// the bearer back to whoever captured it. See UnregisterNode and RegisterNode.
	signed map[string]bool
	// keyHex indexes the registered assertion keys by their lowercase hex, so the cheap door
	// (knownCredential) can answer "does this tower know this key" with one map lookup instead
	// of hex-encoding every registered Station under the lock authNode also needs. The value is
	// a refcount rather than a bool: two Stations sharing an assertion key is refused at Core,
	// not here, and an index that assumed uniqueness would silently un-register a live key the
	// first time that assumption broke.
	keyHex map[string]int
	// tokens is the same index for the legacy bearer. It is a set of the tokens registered for
	// Stations that have not signed, and it disappears with the bearer path itself.
	tokens map[string]int
	// indexed remembers which strings each Station contributed to the two indexes above, so a
	// re-registration releases exactly what it added. Without it the indexes could only ever
	// grow, and a rotated credential would keep opening the cheap door forever.
	indexed map[string]credentialIndex
}

// setKeyIndexLocked moves stationID's entry in the credential indexes to auth, which may be the
// zero NodeAuth to remove it. Caller holds s.mu for writing.
//
// It reads the PREVIOUS registration to know what to release, which is why the two maps and
// s.nodes are written under one lock hold: an index that drifts from s.nodes either refuses a
// live node's body read (visible as a station that mysteriously cannot complete) or admits a
// credential that is no longer registered.
func (s *Server) setKeyIndexLocked(stationID string, auth NodeAuth) {
	if s.keyHex == nil {
		s.keyHex, s.tokens = map[string]int{}, map[string]int{}
	}
	if prior, had := s.indexed[stationID]; had {
		if prior.key != "" {
			if s.keyHex[prior.key]--; s.keyHex[prior.key] <= 0 {
				delete(s.keyHex, prior.key)
			}
		}
		if prior.token != "" {
			if s.tokens[prior.token]--; s.tokens[prior.token] <= 0 {
				delete(s.tokens, prior.token)
			}
		}
		delete(s.indexed, stationID)
	}
	cur := credentialIndex{token: auth.LegacyToken}
	if len(auth.AssertionKey) == ed25519.PublicKeySize {
		cur.key = hex.EncodeToString(auth.AssertionKey)
	}
	if cur.key == "" && cur.token == "" {
		return
	}
	if s.indexed == nil {
		s.indexed = map[string]credentialIndex{}
	}
	s.indexed[stationID] = cur
	if cur.key != "" {
		s.keyHex[cur.key]++
	}
	if cur.token != "" {
		s.tokens[cur.token]++
	}
}

// credentialIndex is what setKeyIndexLocked has to give back when a Station is re-registered:
// the exact strings it put into the two indexes last time.
type credentialIndex struct {
	key   string
	token string
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
	// EpochKey is the tower's admitted identity key, used to PROVE this hub's epoch to a node.
	// See Server.epochKey. Nil mints an ephemeral one.
	EpochKey ed25519.PrivateKey
}

// NewServer wires a Server over a Hub with a grant checker. Zero TTLs fall back to the defaults.
func NewServer(hub *Hub, check GrantCheck, opt ServerOptions) *Server {
	if opt.SubmitTTL <= 0 {
		opt.SubmitTTL = defaultSubmitTTL
	}
	if opt.PollTTL <= 0 {
		opt.PollTTL = defaultPollTTL
	}
	if len(opt.EpochKey) != ed25519.PrivateKeySize {
		// A hub with no way to prove its epoch would be refused by every current node, so one
		// is minted here rather than left nil. It is per process, like the epoch it signs, and
		// a node holding Core's fingerprint for the real key will refuse it - which is the
		// correct outcome for a hub that was wired without its identity.
		_, eph, err := ed25519.GenerateKey(nil)
		if err != nil {
			panic("crypto/rand unavailable: " + err.Error())
		}
		opt.EpochKey = eph
	}
	return &Server{hub: hub, check: check, submitTTL: opt.SubmitTTL, pollTTL: opt.PollTTL,
		towerID: opt.TowerID, allowLegacyBearer: opt.AllowLegacyBearer,
		epochKey:    opt.EpochKey,
		epochPubHex: hex.EncodeToString(opt.EpochKey.Public().(ed25519.PublicKey)),
		// THE PROCESS EPOCH. Random rather than a timestamp: a wall clock is what the thing
		// this replaces got wrong, and a hub restarted twice inside one second must not mint
		// the same epoch twice. See Server.epoch.
		epoch: newEpoch(),
		nodes: map[string]NodeAuth{}, signed: map[string]bool{},
		keyHex: map[string]int{}, tokens: map[string]int{},
		indexed: map[string]credentialIndex{}}
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
	s.nodes[stationID] = auth
	s.setKeyIndexLocked(stationID, auth)
	s.mu.Unlock()
}

// UnregisterNode removes a Station, its credential, its replay memory, and its audit wanted
// list (a station Core dropped must not keep a list a later re-registration could answer
// stale - audit M5).
//
// THE SIGNED LATCH IS NOT AMONG THEM, and the reason is the same one that put a tombstone in
// the nonce ring rather than deleting it. The refresher unregisters any Station missing from a
// SINGLE answer from Core, so a transient omission - which nodeauth.go's own forget() calls
// "entirely outside anybody's control" - and a re-registration seconds later used to clear the
// latch and re-open the bearer path for an upgraded node. Core never rotates LegacyToken, so
// the stolen bearer came straight back, and the sentence in nodeauth.go promising that "an
// attacker can neither produce the signature that flips the latch nor unflip it" was false: an
// attacker who could not do either could simply wait for one bad refresh.
//
// So the latch outlives the registration, for the life of the process. It costs a bool per
// Station id this tower has ever verified a signature from - a set only the holder of a
// Station's assertion private key can add to, bounded by Core's own fleet, and gone on
// restart like the rest of this map.
func (s *Server) UnregisterNode(stationID string) {
	s.hub.Unregister(stationID)
	s.mu.Lock()
	delete(s.nodes, stationID)
	s.setKeyIndexLocked(stationID, NodeAuth{})
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

// stampEpoch publishes this hub run's epoch on a node-facing response, TOGETHER WITH THE PROOF
// THAT IT IS THIS TOWER'S. It is called before anything else in each node route so that EVERY
// answer carries all three - a 401 most of all, since that is the one a client gets when it does
// not know the epoch yet and these headers are how it finds out. Set before any WriteHeader,
// which is the only ordering that works.
//
// THE PROOF IS ON EVERY ANSWER, NOT ONLY ON THE EPOCH REFUSAL, and that is a deliberate choice
// over a narrower one. A node adopts an epoch whenever a response names one it did not send, and
// that response is not always the epoch refusal - the door refusal and the unknown-Station
// refusal reach a client with a stale epoch too. Emitting the proof from one place means the
// next route added here cannot forget it, which is the same argument the nonce gate makes for
// applying to every route rather than only to the one that dequeues.
//
// It costs one Ed25519 signature per node-facing request. At the real cadence - a poll per
// worker per twenty-five seconds - that is a third of a signature a second on a fully loaded
// eight-worker node, and the pathological case (a stranger spraying the route) is bounded by the
// listener's connection cap rather than by this being cheap.
func (s *Server) stampEpoch(w http.ResponseWriter, r *http.Request) {
	if s.epoch == "" {
		return
	}
	w.Header().Set(HubEpochHeader, s.epoch)
	if len(s.epochKey) != ed25519.PrivateKeySize {
		return
	}
	// The nonce of the request being answered, so the proof is a response to THIS challenge and
	// cannot be stockpiled and replayed into a later one. A request that carries no nonce (a
	// stranger, or a pre-signature node) gets a proof over the empty string, which is exactly
	// as useful to it as no proof at all.
	nonce := r.URL.Query().Get(nonceParam)
	sig := ed25519.Sign(s.epochKey, hubEpochStatement(s.towerID, s.epoch, nonce))
	w.Header().Set(HubKeyHeader, s.epochPubHex)
	w.Header().Set(HubProofHeader, hex.EncodeToString(sig))
}

// EpochKeyHash is the fingerprint a node checks this hub's epoch proof against - hex
// sha256 of the raw identity public key, the same string Core keeps in its admission registry
// and hands the node at attach. Exposed for a caller that mounts the Server itself and has to
// give a client the value Core would have given it.
func (s *Server) EpochKeyHash() string {
	if len(s.epochKey) != ed25519.PrivateKeySize {
		return ""
	}
	sum := sha256.Sum256(s.epochKey.Public().(ed25519.PublicKey))
	return hex.EncodeToString(sum[:])
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
	s.stampEpoch(w, r)
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
