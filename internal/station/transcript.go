package station

// transcript.go keeps, for a sampled fraction of attempts, the exact bytes a Station received
// and returned - so Roger Core can audit content it never saw.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # WHY THE STATION KEEPS THIS AND CORE ASKS FOR IT
//
// On the edge path Core sees neither the request nor the response. That is the whole point,
// and it is also why moderation has to move to a POST-HOC sample: Core cannot screen what it
// never received, so instead it checks a fraction afterwards. The Station is the one party
// that had the plaintext, so it is the one that keeps the transcript, and it hands one over
// only when Core asks with a signed audit request naming the attempt.
//
// # WHY A TRANSCRIPT PROVES SOMETHING
//
// Both ends signed a digest of the exact bytes - the Station in its receipt, the consumer in
// its acknowledgement - so NEITHER can produce a different transcript afterwards. A stored
// transcript that hashes to those digests is the real content; one that does not is
// attributable, and to the Station, because it is the Station's own store and its own
// signature it fails to match.
//
// # BOUNDED AND SAMPLED
//
// Keeping every transcript forever would defeat the point of not carrying them - the Station
// would become the content warehouse the edge path exists to avoid. So a Station keeps a
// bounded, self-sampled fraction: enough that Core's random audit lands on something often
// enough to matter, few enough that it is not storage anybody has to reason about. A Station
// that is asked for an attempt it did not sample cannot produce it, which the audit treats as
// the same kind of failure as a mismatch - see the broker side.

import (
	"hash/fnv"
	"sync"
)

// Transcript is the exact bytes of one attempt.
type Transcript struct {
	AttemptID string `json:"attempt_id"`
	Request   []byte `json:"request"`
	Response  []byte `json:"response"`
}

// Transcripts is a bounded, sampled store of recent transcripts.
type Transcripts struct {
	mu      sync.Mutex
	by      map[string]Transcript
	order   []string
	limit   int
	sampleN uint32 // keep 1 in sampleN; 1 means keep all
}

// NewTranscripts builds a store keeping at most `limit` transcripts, sampling 1 in `sampleN`.
//
// sampleN of 0 or 1 keeps everything, which is the right default for a small private fleet
// and for tests; a large public Station lowers its sample so the store stays a rounding error
// against the traffic.
func NewTranscripts(limit int, sampleN uint32) *Transcripts {
	if limit <= 0 {
		limit = 256
	}
	if sampleN == 0 {
		sampleN = 1
	}
	return &Transcripts{by: map[string]Transcript{}, limit: limit, sampleN: sampleN}
}

// sampled decides, deterministically from the attempt id, whether to keep this one. Determinism
// matters: the SAME attempt is kept or dropped identically no matter which instance of the
// Station code runs, and an attacker cannot make a request land outside the sample by retrying,
// because the attempt id is Core's to mint.
func (s *Transcripts) sampled(attemptID string) bool {
	if s.sampleN <= 1 {
		return true
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(attemptID))
	return h.Sum32()%s.sampleN == 0
}

// Keep records a transcript if this attempt is in the sample.
func (s *Transcripts) Keep(t Transcript) {
	if t.AttemptID == "" || !s.sampled(t.AttemptID) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.by[t.AttemptID]; exists {
		return
	}
	for len(s.order) >= s.limit {
		// Drop the oldest: an attempt old enough to have aged out is old enough that its audit
		// window has almost certainly passed, so it is the cheapest one to lose.
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.by, oldest)
	}
	s.by[t.AttemptID] = t
	s.order = append(s.order, t.AttemptID)
}

// Get returns a kept transcript. The bool is false both for an attempt that was never sampled
// and one that has aged out - the caller (an audit) treats "cannot produce" the same either
// way, because from Core's side a Station that will not show its work is a Station to suspect.
func (s *Transcripts) Get(attemptID string) (Transcript, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.by[attemptID]
	return t, ok
}

// Len is how many transcripts are held, for an operator's eyes.
func (s *Transcripts) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.order)
}
