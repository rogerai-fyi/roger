package localplane

// pair.go is request-and-approve (features/join/approve.feature): a client asks to join by address,
// with no invite. It sends its ephemeral X25519 key and a COMMITMENT to its nonce; the Tower answers
// with its own ephemeral key and nonce; the client then reveals its nonce, and only a reveal that
// matches the commitment is shown to the owner, with the matching phrase (internal/joinsas) over
// both long-term keys (the client's signing key, and this Tower's TLS key as it presents it), both
// ephemeral keys and both nonces. The owner approves or refuses; the client long-polls for the
// outcome. A request carries no secret and grants nothing until approved.
//
// Pending requests live in this serving process: they last PairLifetime, so a restart costing the
// pending ones is fine (the client is told to ask again).

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/joinsas"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
)

const (
	// PairLifetime is how long a request waits for the owner (ruling 2026-10-10).
	PairLifetime = 2 * time.Minute
	// PairPendingCap is how many requests may wait at once.
	PairPendingCap = 8
	// PairPerAddressPerMinute is how many requests one source address may send a minute.
	PairPerAddressPerMinute = 5
)

// PairStartBody opens a request.
type PairStartBody struct {
	Kind      string   `json:"kind"` // "client" or "station"
	Name      string   `json:"name"`
	Models    []string `json:"models,omitempty"`
	ClientEph string   `json:"client_eph"` // X25519 public key, hex
	Commit    string   `json:"commit"`     // joinsas.Commit(client nonce)
}

// PairStartAnswer is the Tower's half of the exchange.
type PairStartAnswer struct {
	ID         string `json:"id"`
	TowerEph   string `json:"tower_eph"`
	TowerNonce string `json:"tower_nonce"`
	Tower      string `json:"tower,omitempty"`
	Seconds    int    `json:"seconds"`
	// AlreadyMember: the asking key is already admitted, so no request was created and the
	// owner is not asked (a client asking again is told so, with no phrase).
	AlreadyMember bool `json:"already_member,omitempty"`
}

// PairRevealBody reveals the client's nonce.
type PairRevealBody struct {
	ID          string `json:"id"`
	ClientNonce string `json:"client_nonce"`
}

// PairWaitBody asks for a request's outcome.
type PairWaitBody struct {
	ID string `json:"id"`
}

// PairWaitAnswer is a request's outcome: pending, approved, refused or expired.
type PairWaitAnswer struct {
	Status string     `json:"status"`
	Answer JoinAnswer `json:"answer,omitempty"`
}

// PairAdmit is what approving a request does on a host that is not a Tower: the request (who
// asked, as what), its key hash and its public key (hex). An error leaves the request waiting.
type PairAdmit func(r tower.PairRequest, keyHash, pubHex string) (JoinAnswer, error)

// NewPairing is request-and-approve alone, for a host that is not a Tower (an Edge authority,
// approve.feature): label is its name in answers, key is the key the phrase covers (the one it
// presents to a device), admit is what an approval does. Its Handler serves only the pairing.
func NewPairing(label string, key func() (string, error), admit PairAdmit) *Server {
	return &Server{label: label, pairKey: key, pairAdmit: admit,
		replay: newReplayGuard(time.Now, replayWindow), logf: log.Printf}
}

type pairReq struct {
	view       tower.PairRequest
	keyHash    string
	pubHex     string
	commit     string
	clientEph  string
	towerEph   string
	towerNonce string
	revealed   bool
	status     string // "pending", "approved", "refused"
	answer     JoinAnswer
	done       chan struct{}
}

type pairing struct {
	mu    sync.Mutex
	byID  map[string]*pairReq
	byKey map[string]string
}

func (s *Server) pairs() *pairing {
	s.pairOnce.Do(func() {
		s.pair = &pairing{byID: map[string]*pairReq{}, byKey: map[string]string{}}
		if s.pairRL == nil {
			s.pairRL = newRateLimiter(time.Now, float64(PairPerAddressPerMinute)/60, PairPerAddressPerMinute)
		}
		if s.pairNow == nil {
			s.pairNow = time.Now
		}
		if s.pairWait == 0 {
			s.pairWait = defaultPollTimeout
		}
	})
	return s.pair
}

// live reports whether a request is still within its lifetime. Callers hold p.mu.
func (s *Server) live(r *pairReq) bool { return s.pairNow().Before(r.view.ExpiresAt) }

// prune drops requests past twice their lifetime (an outcome stays readable for a while).
// Callers hold p.mu.
func (s *Server) prune(p *pairing) {
	for id, r := range p.byID {
		if s.pairNow().After(r.view.ExpiresAt.Add(PairLifetime)) {
			delete(p.byID, id)
			if p.byKey[r.keyHash] == id {
				delete(p.byKey, r.keyHash)
			}
		}
	}
}

func (s *Server) pairStart(w http.ResponseWriter, r *http.Request) {
	p := s.pairs()
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !s.pairRL.allow("pair:" + ip) {
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many join requests from this address - try again in a minute"})
		return
	}
	body := readBody(r)
	id, nonce, ok := s.verifiedIdentity(r, body)
	if !ok || nonce == "" || s.replay.used("pair:"+id+":"+nonce) {
		unauthorized(w)
		return
	}
	var req PairStartBody
	if err := json.Unmarshal(body, &req); err != nil || req.Commit == "" || req.ClientEph == "" ||
		(req.Kind != "client" && req.Kind != "station") || len(req.Name) > 64 {
		unauthorized(w)
		return
	}
	// A client whose key is already admitted has nothing to ask: answer that, create no request
	// and leave the owner's queue alone. Only the asking key's own standing is told.
	if req.Kind == "client" && s.st != nil && s.st.IsAdmitted(id) {
		writeJSON(w, http.StatusOK, PairStartAnswer{Tower: s.label, AlreadyMember: true})
		return
	}
	nonceT := joinsas.NewNonce()
	eph, err := newEphemeral()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not start a pairing"})
		return
	}
	var rid [8]byte
	_, _ = rand.Read(rid[:])
	pr := &pairReq{
		view: tower.PairRequest{ID: hex.EncodeToString(rid[:]), Name: req.Name, Kind: tower.JoinKind(req.Kind),
			Models: req.Models},
		keyHash: id, pubHex: r.Header.Get(protocol.HeaderPubkey), commit: req.Commit,
		clientEph: req.ClientEph, towerEph: eph, towerNonce: nonceT, status: "pending", done: make(chan struct{}),
	}
	p.mu.Lock()
	pr.view.ExpiresAt = s.pairNow().Add(PairLifetime)
	s.prune(p)
	// One pending request per client: a new one replaces the old.
	if old, ok := p.byKey[id]; ok {
		delete(p.byID, old)
	}
	pending := 0
	for _, x := range p.byID {
		if x.status == "pending" && s.live(x) {
			pending++
		}
	}
	if pending >= PairPendingCap {
		p.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": s.name() + " is busy with other join requests, try again in a minute"})
		return
	}
	p.byID[pr.view.ID], p.byKey[id] = pr, pr.view.ID
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, PairStartAnswer{ID: pr.view.ID, TowerEph: eph, TowerNonce: nonceT,
		Tower: s.label, Seconds: int(PairLifetime.Seconds())})
}

func (s *Server) pairReveal(w http.ResponseWriter, r *http.Request) {
	p := s.pairs()
	body := readBody(r)
	id, nonce, ok := s.verifiedIdentity(r, body)
	if !ok || nonce == "" || s.replay.used("pair:"+id+":"+nonce) {
		unauthorized(w)
		return
	}
	var req PairRevealBody
	if err := json.Unmarshal(body, &req); err != nil {
		unauthorized(w)
		return
	}
	key := s.pairKey
	if key == nil {
		key = s.st.JoinFingerprint
	}
	fp, err := key()
	if err != nil {
		unauthorized(w)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pr, ok := p.byID[req.ID]
	if !ok || pr.keyHash != id || pr.revealed || !s.live(pr) || joinsas.CheckCommit(pr.commit, req.ClientNonce) != nil {
		// A reveal that breaks its commitment ends the request: it is never shown to the owner.
		if ok && pr.keyHash == id {
			delete(p.byID, req.ID)
			delete(p.byKey, id)
		}
		unauthorized(w)
		return
	}
	pr.revealed = true
	pr.view.Phrase = joinsas.Phrase(joinsas.Transcript{
		ClientKey: pr.pubHex, TowerKey: fp, ClientEph: pr.clientEph, TowerEph: pr.towerEph,
		ClientNonce: req.ClientNonce, TowerNonce: pr.towerNonce,
	})
	writeJSON(w, http.StatusOK, map[string]any{"waiting": int(time.Until(pr.view.ExpiresAt).Seconds())})
}

func (s *Server) pairWaitHandler(w http.ResponseWriter, r *http.Request) {
	p := s.pairs()
	body := readBody(r)
	id, nonce, ok := s.verifiedIdentity(r, body)
	if !ok || nonce == "" || s.replay.used("pair:"+id+":"+nonce) {
		unauthorized(w)
		return
	}
	var req PairWaitBody
	_ = json.Unmarshal(body, &req)
	p.mu.Lock()
	pr, ok := p.byID[req.ID]
	if !ok || pr.keyHash != id {
		p.mu.Unlock()
		writeJSON(w, http.StatusOK, PairWaitAnswer{Status: "expired"})
		return
	}
	done, hold := pr.done, s.pairWait
	p.mu.Unlock()
	select {
	case <-done:
	case <-time.After(hold):
	case <-r.Context().Done():
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case pr.status != "pending":
		writeJSON(w, http.StatusOK, PairWaitAnswer{Status: pr.status, Answer: pr.answer})
	case !s.live(pr):
		writeJSON(w, http.StatusOK, PairWaitAnswer{Status: "expired"})
	default:
		writeJSON(w, http.StatusOK, PairWaitAnswer{Status: "pending"})
	}
}

// SetPairClock sets the clock request lifetimes are measured on (production keeps time.Now; a
// test says "two minutes later").
func (s *Server) SetPairClock(now func() time.Time) {
	s.pairs()
	s.pair.mu.Lock()
	s.pairNow = now
	s.pair.mu.Unlock()
}

// SetPairPoll sets how long a client's wait for the owner's answer is held open per request.
func (s *Server) SetPairPoll(d time.Duration) {
	s.pairs()
	s.pair.mu.Lock()
	s.pairWait = d
	s.pair.mu.Unlock()
}

// PairRequests are the requests waiting for the owner: revealed, live and undecided, oldest first.
func (s *Server) PairRequests() []tower.PairRequest {
	p := s.pairs()
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []tower.PairRequest
	for _, pr := range p.byID {
		if pr.revealed && pr.status == "pending" && s.live(pr) {
			out = append(out, pr.view)
		}
	}
	slices.SortFunc(out, func(a, b tower.PairRequest) int { return a.ExpiresAt.Compare(b.ExpiresAt) })
	return out
}

// errNoSuchRequest is an approval or refusal for a request that is not waiting.
var errNoSuchRequest = errors.New("no join request with that id is waiting (it may have expired)")

func (s *Server) decide(id string, approve bool) error {
	p := s.pairs()
	p.mu.Lock()
	defer p.mu.Unlock()
	pr, ok := p.byID[id]
	if !ok || !pr.revealed || pr.status != "pending" || !s.live(pr) {
		return errNoSuchRequest
	}
	if !approve {
		pr.status = "refused"
		close(pr.done)
		s.logf("refused the join request from %s", pr.view.Name)
		return nil
	}
	if s.pairAdmit != nil {
		ans, err := s.pairAdmit(pr.view, pr.keyHash, pr.pubHex)
		if err != nil {
			return err
		}
		pr.status, pr.answer = "approved", ans
		close(pr.done)
		return nil
	}
	res, err := s.st.AdmitPaired(pr.view.Kind, pr.view.Name, pr.keyHash, pr.view.Models)
	if err != nil {
		return err
	}
	ans := JoinAnswer{Joined: string(res.Kind), AlreadyMember: res.AlreadyMember, Tower: s.label}
	if res.Kind == tower.JoinStation {
		ans.Models, ans.Network = res.Station.Models, res.Station.NetworkID
	} else {
		ans.Network, ans.Role, ans.Models = res.Credential.NetworkID, res.Credential.Role, s.bands()
	}
	pr.status, pr.answer = "approved", ans
	close(pr.done)
	if !res.AlreadyMember {
		s.noteJoin(pr.view.Name, pr.keyHash, res.Kind, res.Station.Models)
	}
	return nil
}

// ApprovePair admits the request's key (or attaches its Station).
func (s *Server) ApprovePair(id string) error { return s.decide(id, true) }

// RefusePair turns the request down; nothing is admitted.
func (s *Server) RefusePair(id string) error { return s.decide(id, false) }

// name is how the Tower calls itself in a refusal.
func (s *Server) name() string {
	if s.label != "" {
		return s.label
	}
	return "this Tower"
}
