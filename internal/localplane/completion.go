package localplane

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
)

// maxPromptBody bounds an authenticated consumer prompt. It is larger than the auth cap (a
// real prompt is bigger than a signature preamble) but still finite, so one client cannot make
// the Tower buffer unbounded work. The resource-limit slice adds concurrency and per-client
// rate on top of this.
const maxPromptBody = 8 << 20 // 8 MiB

// writeJSON writes a JSON body with the right content type and status, the same way the
// uniform refusal does - so no handler leaks a text/plain body where a client expects JSON.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// randID is a fresh request/job identifier. crypto/rand, so a station cannot guess an id it
// was not handed and complete someone else's job.
func randID() string {
	var b [12]byte
	if _, err := crand.Read(b[:]); err != nil {
		// A failed system CSPRNG is not a condition to paper over with predictable ids: a
		// guessable job id would let a station complete a job it never took. Fail loudly.
		panic("localplane: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// authStation verifies a station's signed request and maps it to an ATTACHED station by the
// same canonical rule clients use (protocol.UserIDFromPubkey over its pubkey == the station's
// recorded key hash). Only an attached station may poll for work or return an answer; every
// failure is the same uniform refusal, revealing nothing about the fleet.
func (s *Server) authStation(r *http.Request, body []byte) (tower.Station, bool) {
	pub := r.Header.Get(protocol.HeaderPubkey)
	sig := r.Header.Get(protocol.HeaderSig)
	ts := parseTS(r.Header.Get(protocol.HeaderTS))
	keyHash, verified := protocol.VerifyRequest(pub, sig, ts, r.Method, r.URL.Path, body)
	if !verified {
		return tower.Station{}, false
	}
	stations, err := s.st.Stations()
	if err != nil {
		return tower.Station{}, false
	}
	for _, st := range stations {
		if st.KeyHash == keyHash {
			return st, true
		}
	}
	return tower.Station{}, false
}

// chatRequest is the one field the plane reads from a consumer request: the model. Everything
// else in the body is opaque and passed to the station verbatim. Notably, the plane reads NO
// RogerAI account, wallet, X-Roger-Freq band, or grant key from the request - none of it
// authenticates or routes anything here, and none is echoed back.
type chatRequest struct {
	Model string `json:"model"`
}

// chatCompletions serves one completion by routing it to a LOCAL station and waiting for the
// station to poll, run it, and return the answer. The Tower dials nobody: the answer arrives
// because a station connected in. An Open Market model this Tower does not host is refused
// after authentication, and nothing is dialed - there is no code linked that could.
func (s *Server) chatCompletions(w http.ResponseWriter, r *http.Request) {
	body := readPrompt(r)
	clientKeyHash, status := s.authClient(r, body)
	if status != authOK {
		writeAuthFailure(w, status)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	// Per-CLIENT fairness first: no single admitted client may hold more than its share of
	// concurrent completions, so one client cannot accumulate every global slot (each held for
	// up to the completion timeout) and starve the stations for the others.
	if !s.perClient.acquire(clientKeyHash) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many concurrent requests for this client"})
		return
	}
	defer s.perClient.release(clientKeyHash)
	// Whole-Tower bound: cap total concurrent completions so a burst cannot exhaust the box. A
	// request that cannot get a slot is refused, not queued behind an unbounded backlog.
	if !s.inflight.tryAcquire() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "the Tower is busy; retry shortly"})
		return
	}
	defer s.inflight.release()
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "a model is required"})
		return
	}
	// The model must be offered by one of THIS Tower's own stations. A model only the Open
	// Market sells is refused here - named only to the already-authenticated client - and no
	// outbound connection is attempted, because none can be.
	offered, err := s.offersModel(req.Model)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "unavailable"})
		return
	}
	if !offered {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "model not offered by any local station: " + req.Model})
		return
	}

	jobID := randID()
	j := s.q.submit(jobID, req.Model, body)
	select {
	case res := <-j.result:
		s.writeAnswer(w, clientKeyHash, req.Model, res)
	case <-r.Context().Done():
		// The consumer disconnected. Abandon so the job neither leaks nor is later run as stale
		// work; but if a station delivered in the same instant, still record the receipt (the
		// work happened) - there is just no socket left to write the answer to.
		s.q.abandon(jobID)
		if res, ok := drain(j); ok {
			_, _ = s.st.RecordReceipt(clientKeyHash, res.stationID, req.Model)
		}
	case <-time.After(s.completionTimeout):
		// Abandon FIRST, then drain - the same order as the disconnect branch. complete delivers
		// under the queue lock, so once abandon has removed the job, any answer a station managed
		// to deliver is already in the buffer for drain to find; anything later finds the job gone
		// and reports delivered=false. So a just-served answer is returned rather than lost to a
		// 504 while the station believes it succeeded, with no racy window either way.
		s.q.abandon(jobID)
		if res, ok := drain(j); ok {
			s.writeAnswer(w, clientKeyHash, req.Model, res)
			return
		}
		writeJSON(w, http.StatusGatewayTimeout, map[string]any{"error": "no local station served this request in time"})
	}
}

// drain non-blockingly takes a result a station may have delivered, without waiting.
func drain(j *job) (jobResult, bool) {
	select {
	case res := <-j.result:
		return res, true
	default:
		return jobResult{}, false
	}
}

// writeAnswer records the free local receipt for the serving station and relays the answer
// verbatim with a free cost header - never a billing shape.
func (s *Server) writeAnswer(w http.ResponseWriter, clientKeyHash, model string, res jobResult) {
	// The work happened; a receipt-write failure must not swallow the answer.
	_, _ = s.st.RecordReceipt(clientKeyHash, res.stationID, model)
	w.Header().Set("X-Roger-Cost", "0")
	w.Header().Set("X-Roger-Local", "1")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(res.answer)
}

// offersModel reports whether any attached station serves the model.
func (s *Server) offersModel(model string) (bool, error) {
	stations, err := s.st.Stations()
	if err != nil {
		return false, err
	}
	for _, st := range stations {
		if serves(st.Models, model) {
			return true, nil
		}
	}
	return false, nil
}

// localPoll is the station side of the queue: an attached station long-polls for a job it can
// serve. A job returns 200 with the request to run; no job within the poll window returns 204,
// and the station polls again. The station connects IN; the Tower never dials it.
func (s *Server) localPoll(w http.ResponseWriter, r *http.Request) {
	body := readPrompt(r)
	station, ok := s.authStation(r, body)
	if !ok {
		unauthorized(w)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.pollTimeout)
	defer cancel()
	j, got := s.q.poll(ctx, station.ID, station.Models)
	if !got {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"job_id":  j.id,
		"model":   j.model,
		"request": json.RawMessage(j.body),
	})
}

// completeRequest is a station returning an answer for a job it took.
type completeRequest struct {
	JobID  string          `json:"job_id"`
	Answer json.RawMessage `json:"answer"`
}

// localComplete delivers a station's answer to the waiting consumer. Only the station that
// took the job may complete it (the queue enforces that); a completion for an unknown or
// already-abandoned job is accepted and dropped, so a late station learns nothing.
func (s *Server) localComplete(w http.ResponseWriter, r *http.Request) {
	body := readPrompt(r)
	station, ok := s.authStation(r, body)
	if !ok {
		unauthorized(w)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req completeRequest
	if err := json.Unmarshal(body, &req); err != nil || req.JobID == "" || len(req.Answer) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "a job_id and answer are required"})
		return
	}
	delivered := s.q.complete(req.JobID, station.ID, req.Answer)
	writeJSON(w, http.StatusOK, map[string]any{"delivered": delivered})
}

// readPrompt reads at most the authenticated-prompt cap. The signature is verified over these
// exact bytes, so a body larger than the cap simply fails to verify rather than being acted on.
func readPrompt(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, maxPromptBody))
	return b
}
