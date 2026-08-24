// Package localplane is the standalone Tower's CONSUMER-facing surface, and it is Core-free
// by construction. It imports internal/tower (local admission and routing), internal/protocol
// (the same request-signature rule roger already uses), and the standard library - and NONE
// of towerjoin, towercore, or towerhub. A dependency-graph test on the binary that hosts it,
// and a source-scan gate on this package's files, hold that true: there is no line here that
// can dial Roger Core, so "a standalone Tower never bridges to the Open Market" is a property
// of the linkage, not of a runtime flag one bug could flip.
//
// Contract: features/tower/standalone_consumer_plane.feature.
//
// The handler reads a request and writes a reply; it opens no socket of its own (the listener
// lives in the Core-free binary's main and hands connections in), and it makes no outbound
// call. It serves authentication, discovery, and the completion loop: a consumer's request is
// enqueued and a `roger share` station POLLS for it, runs it, and returns the answer - so the
// Tower dials nobody. Each served request writes a free, persisted local receipt.
package localplane

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
)

// maxAuthBody bounds how much of a request body the plane reads to verify a signature. A
// consumer prompt is small; a multi-megabyte body on the auth path would be a cheap way to
// make the Tower do work before it has admitted anyone. The completion slice sets its own
// (larger) body cap for an authenticated prompt.
const maxAuthBody = 1 << 20 // 1 MiB

// Default timeouts. completionTimeout bounds how long a consumer waits for a local station to
// serve before the plane gives up; pollTimeout bounds a station's long-poll for work. Both are
// generous for a real prompt yet finite, so neither side blocks forever.
const (
	defaultCompletionTimeout = 120 * time.Second
	defaultPollTimeout       = 25 * time.Second
)

// Server is the standalone consumer plane over one Tower's local state. It is safe for
// concurrent use: tower.State serialises its own admission and routing, and the work queue,
// replay guard, and rate limiter are internally locked.
type Server struct {
	st                *tower.State
	q                 *queue
	rl                *rateLimiter
	stationRL         *rateLimiter
	inflight          semaphore
	perClient         *clientInflight
	replay            *replayGuard
	completionTimeout time.Duration
	pollTimeout       time.Duration
}

// New builds a consumer plane over a standalone Tower's state.
func New(st *tower.State) *Server {
	return &Server{
		st:                st,
		q:                 newQueue(),
		rl:                newRateLimiter(time.Now, defaultPerClientRate, defaultPerClientBurst),
		stationRL:         newRateLimiter(time.Now, defaultStationRate, defaultStationBurst),
		inflight:          newSemaphore(defaultMaxInFlight),
		perClient:         newClientInflight(defaultMaxInFlightPerClient),
		replay:            newReplayGuard(time.Now, replayWindow),
		completionTimeout: defaultCompletionTimeout,
		pollTimeout:       defaultPollTimeout,
	}
}

// verifiedIdentity verifies a request signature the nonce-aware way and returns the derived id
// plus the nonce that was bound in (empty for a plain V1 request). It is the one place both the
// client and the station auth paths verify a signature, so the two agree on the rule.
//
// A request that carries an X-Roger-Nonce is verified WITH that nonce bound in and becomes
// eligible for the replay guard; a request without one is verified the plain way and relies on
// the 5-minute freshness window alone. New roger clients pointed at a local Tower always send a
// nonce (so the plane gets full replay defense); the plain path stays for the public broker's
// own callers, which never reach this plane.
func (s *Server) verifiedIdentity(r *http.Request, body []byte) (id, nonce string, ok bool) {
	pub := r.Header.Get(protocol.HeaderPubkey)
	sig := r.Header.Get(protocol.HeaderSig)
	ts := parseTS(r.Header.Get(protocol.HeaderTS))
	nonce = r.Header.Get(protocol.HeaderNonce)
	if nonce != "" {
		id, ok = protocol.VerifyRequestNonce(pub, sig, ts, r.Method, r.URL.Path, body, nonce)
	} else {
		id, ok = protocol.VerifyRequest(pub, sig, ts, r.Method, r.URL.Path, body)
	}
	return id, nonce, ok
}

// authStatus is the outcome of authenticating a client request.
type authStatus int

const (
	authOK          authStatus = iota // an admitted client, fresh signature, within rate
	authDenied                        // bad signature, unadmitted, revoked, OR a replay: uniform 401
	authRateLimited                   // an admitted client sending too fast: 429
)

// Handler returns the consumer-plane routes. The binary mounts this on a listener it owns;
// this package never listens or dials. The /local/* routes are the station side of the work
// queue - a station connects IN to poll for work and return answers, so the Tower dials out
// for nothing.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/discover", s.discover)
	mux.HandleFunc("/v1/chat/completions", s.chatCompletions)
	mux.HandleFunc("/local/poll", s.localPoll)
	mux.HandleFunc("/local/complete", s.localComplete)
	return mux
}

// unauthorized writes the ONE refusal every authentication failure returns. It is
// byte-identical whether the signature was bad, the key was never admitted, or the client
// was revoked - a caller learns only that it was refused, never which door was locked. No
// model name, no key state, nothing an unauthenticated prober could turn into an oracle.
func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
}

// authClient verifies the request signature and maps it to an admitted local client by the
// ONE canonical rule protocol.UserIDFromPubkey defines - the same rule admission recorded, so
// a signature can actually be checked against the admitted set. It returns the client key hash
// and whether the caller is an admitted client. Every failure path returns ok=false with no
// distinguishing detail; the caller writes the uniform refusal.
func (s *Server) authClient(r *http.Request, body []byte) (clientKeyHash string, status authStatus) {
	userID, nonce, verified := s.verifiedIdentity(r, body)
	if !verified {
		return "", authDenied
	}
	if !s.st.IsAdmitted(userID) {
		return "", authDenied
	}
	// Replay CHECK first (no record yet): a captured request replayed within the freshness window
	// carries the same nonce and is refused as the uniform 401 - no oracle. Checking before the
	// rate limiter means a replay drains no rate bucket. A request with no nonce cannot be
	// replay-guarded (its signature is not per-request unique), so it relies on the 5-minute
	// window - the older path new clients skip.
	if nonce != "" && s.replay.isReplay(userID+":"+nonce) {
		return "", authDenied
	}
	// An admitted client sending too fast is throttled - a distinct, honest 429, since it IS
	// authenticated and just needs to slow down. Per-client, so one flood never starves another.
	if !s.rl.allow(userID) {
		return userID, authRateLimited
	}
	// RECORD the nonce only now, on the way to OK: a throttled request records nothing, so the
	// guard's memory stays bounded by rate x window per admitted key rather than by how fast an
	// attacker sends. (A 429'd request not recording also means a legitimate byte-identical retry
	// is not misread as a replay.)
	if nonce != "" {
		s.replay.record(userID + ":" + nonce)
	}
	return userID, authOK
}

// writeAuthFailure writes the response for a non-OK auth status: the uniform 401 for a denial
// (including a replay), or a 429 for an admitted-but-throttled client.
func writeAuthFailure(w http.ResponseWriter, status authStatus) {
	if status == authRateLimited {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded - slow down"})
		return
	}
	unauthorized(w)
}

// readBody reads at most the auth cap and returns the bytes, so the signature is verified over
// exactly what a handler would act on.
func readBody(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, maxAuthBody))
	return b
}

// localOffer is one entry in the plane's /discover feed - a subset of the public broker's
// offer shape, so roger parses it with no change beyond its broker address. It advertises the
// local station's model as free, online, and local; it never carries a price, an account, a
// band, or anything that would read as a billable Open Market offer.
type localOffer struct {
	NodeID   string  `json:"node_id"`
	Model    string  `json:"model"`
	Modality string  `json:"modality"`
	PriceIn  float64 `json:"price_in"`
	PriceOut float64 `json:"price_out"`
	Online   bool    `json:"online"`
	FreeNow  bool    `json:"free_now"`
	Local    bool    `json:"local"`
}

// discover answers GET /discover with THIS Tower's own attached stations and nothing else -
// no public market, no other network. Admitted clients only: discovery is not an anonymous
// surface, so an unauthenticated caller learns nothing about what the network hosts.
func (s *Server) discover(w http.ResponseWriter, r *http.Request) {
	// Authenticate FIRST, before the method check: an unauthenticated prober must not be able
	// to tell a real route from a 404 by getting a 405, so every unauthenticated request -
	// whatever its method - gets the same 401 as any other auth failure.
	if _, st := s.authClient(r, readBody(r)); st != authOK {
		writeAuthFailure(w, st)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	stations, err := s.st.Stations()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "unavailable"})
		return
	}
	offers := make([]localOffer, 0)
	for _, st := range stations {
		for _, m := range st.Models {
			offers = append(offers, localOffer{
				NodeID: st.ID, Model: m, Modality: "chat",
				PriceIn: 0, PriceOut: 0, Online: true, FreeNow: true, Local: true,
			})
		}
	}
	sort.Slice(offers, func(i, j int) bool {
		if offers[i].Model != offers[j].Model {
			return offers[i].Model < offers[j].Model
		}
		return offers[i].NodeID < offers[j].NodeID
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"offers": offers})
}

// parseTS reads the unix-seconds timestamp header, returning 0 (which the signature check
// treats as far outside any freshness window) when it is missing or unparseable.
func parseTS(s string) int64 {
	if s == "" {
		return 0
	}
	var v int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		v = v*10 + int64(c-'0')
	}
	return v
}
