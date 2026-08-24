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
// This slice serves authentication and discovery. The handler reads a request and writes a
// reply; it opens no socket of its own (the listener lives in the Core-free binary's main and
// hands connections in), and it makes no outbound call. The completion loop - the local work
// queue a station polls - is the next slice.
package localplane

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
)

// maxAuthBody bounds how much of a request body the plane reads to verify a signature. A
// consumer prompt is small; a multi-megabyte body on the auth path would be a cheap way to
// make the Tower do work before it has admitted anyone. The completion slice sets its own
// (larger) body cap for an authenticated prompt.
const maxAuthBody = 1 << 20 // 1 MiB

// Server is the standalone consumer plane over one Tower's local state. It is safe for
// concurrent use: tower.State serialises its own admission and routing.
type Server struct {
	st *tower.State
}

// New builds a consumer plane over a standalone Tower's state.
func New(st *tower.State) *Server { return &Server{st: st} }

// Handler returns the consumer-plane routes. The binary mounts this on a listener it owns;
// this package never listens or dials.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/discover", s.discover)
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
func (s *Server) authClient(r *http.Request, body []byte) (clientKeyHash string, ok bool) {
	pub := r.Header.Get(protocol.HeaderPubkey)
	sig := r.Header.Get(protocol.HeaderSig)
	ts := parseTS(r.Header.Get(protocol.HeaderTS))
	userID, verified := protocol.VerifyRequest(pub, sig, ts, r.Method, r.URL.Path, body)
	if !verified {
		return "", false
	}
	if !s.st.IsAdmitted(userID) {
		return "", false
	}
	return userID, true
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
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if _, ok := s.authClient(r, readBody(r)); !ok {
		unauthorized(w)
		return
	}
	stations, err := s.st.Stations()
	if err != nil {
		http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
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
	var v int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		v = v*10 + int64(c-'0')
	}
	if s == "" {
		return 0
	}
	return v
}
