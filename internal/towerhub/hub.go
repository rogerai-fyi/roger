// Package towerhub is the tower's in-memory data-plane relay for Option C, Topology 2: the
// tower hosts the job queue so the BROKER never touches the payload. It mirrors the broker's
// own nodeTunnel (cmd/rogerai-broker/tunnel.go) - a per-Station job queue a serving node
// long-polls, plus a per-attempt waiter the submitting consumer blocks on - run on the tower
// instead of on Core.
//
// THE TOWER STAYS BLIND. Everything the Hub carries is opaque: the sealed request Envelope is
// encrypted to the serving node's session key, the sealed result Envelope is encrypted to the
// consumer, and the Receipt is signed by the node. The Hub routes by StationID and AttemptID
// only; it never reads, and cannot read, content. Roger Core authorizes (the grant) and settles
// (on the receipt the courier forwards); the Hub is transport.
package towerhub

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Job is one authorized attempt handed to a serving node. Grant is Core's signed edge grant;
// Envelope is the request sealed to the node's session key. Both are opaque to the tower.
type Job struct {
	AttemptID string
	StationID string
	Grant     []byte
	Envelope  []byte
}

// Result is what a node returns for a Job. Envelope is the result sealed to the CONSUMER (so
// neither tower nor broker can read it); Receipt is the node-signed token receipt Core settles
// on. Failure, when set, carries no receipt - a failure never settles an attempt.
type Result struct {
	AttemptID string
	Envelope  []byte
	Receipt   []byte
	Failure   string
}

var (
	// ErrNoStation is returned when a job is submitted for a Station no node is serving here.
	ErrNoStation = errors.New("no node is serving this Station on this tower")
	// ErrDuplicateAttempt is returned when a second job is submitted for an in-flight attempt id.
	ErrDuplicateAttempt = errors.New("this attempt is already in flight")
	// ErrEmptyID is returned when a job names no attempt or no Station. An empty key would let
	// unrelated jobs collide on one waiter/queue slot, so both are required.
	ErrEmptyID = errors.New("a job names exactly one attempt and one Station")
	// ErrBusy is returned when the tower is already carrying maxInFlight concurrent submits.
	ErrBusy = errors.New("the tower is at capacity; retry shortly")
)

// jobQueueDepth bounds a Station's pending-job backlog, mirroring the broker's 64-deep node
// queue. A full queue means the node is not keeping up; submissions then wait on the context
// deadline rather than growing memory without bound.
const jobQueueDepth = 64

type stationQueue struct {
	jobs chan Job
}

// maxInFlight bounds the number of concurrent in-flight Submits across the whole tower. Each
// blocks a goroutine + holds a waiter for up to its context deadline, so without a cap a flood
// of submits would grow goroutines/memory linearly. At the cap, Submit fails fast with ErrBusy
// rather than adding to the pile.
const maxInFlight = 4096

// waiter is a parked submitter: the channel its result is delivered on, and the Station its
// attempt belongs to. Complete is checked against the Station so a node serving one Station
// cannot resolve (and thereby deny) an attempt belonging to another.
type waiter struct {
	ch      chan Result
	station string
}

// dispatchedTTL bounds how long the hub remembers having handed an attempt to a node - long
// enough to outlive the settle window, so a legitimate late completion still couriers.
const dispatchedTTL = 15 * time.Minute

// Hub routes opaque jobs from consumers to serving nodes and results back, keyed only by
// StationID and AttemptID. Safe for concurrent use.
type Hub struct {
	mu       sync.Mutex
	stations map[string]*stationQueue // stationID -> the node's pending-job queue
	waiters  map[string]*waiter       // attemptID -> the parked submitter
	inFlight int                      // count of parked Submits, capped at maxInFlight
	// dispatched remembers which Station each attempt was actually HANDED to (recorded at
	// Poll), so a completion for an attempt this hub never carried - a fabricated id from a
	// hostile node - is refused a courier ride to Core rather than amplified tower-signed.
	dispatched map[string]dispatchRecord
}

type dispatchRecord struct {
	station string
	expires time.Time
}

// New returns an empty Hub.
func New() *Hub {
	return &Hub{
		stations:   map[string]*stationQueue{},
		waiters:    map[string]*waiter{},
		dispatched: map[string]dispatchRecord{},
	}
}

// Register makes a Station servable on this tower: a node calls it before it starts polling.
// Idempotent - registering an already-registered Station keeps its existing queue so in-flight
// jobs are not dropped by a re-register (a node reconnecting). An empty station id is ignored.
//
// THE CALLER ENFORCES ONE NODE PER STATION. The Hub has no node identity of its own; if two
// distinct nodes claimed one StationID they would share this queue and a job would go to
// whichever polled first. The tower's transport layer (which authenticates the polling node
// against the attachment) is responsible for that binding. Even under a collision correctness
// holds, because the request Envelope is sealed to the intended node's session key and a wrong
// node cannot decrypt it - but the binding must still be enforced above.
func (h *Hub) Register(stationID string) {
	if stationID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.stations[stationID]; !ok {
		h.stations[stationID] = &stationQueue{jobs: make(chan Job, jobQueueDepth)}
	}
}

// Unregister drops a Station (a node going away). In-flight submitters are left to time out on
// their own context rather than being force-failed here, matching the broker's behaviour where
// a lost tunnel simply stops delivering.
func (h *Hub) Unregister(stationID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.stations, stationID)
	// Its dispatch records go with it: an unregistered node's token no longer authenticates
	// a Complete, so the records could only linger as dead weight on a quiet tower.
	for id, d := range h.dispatched {
		if d.station == stationID {
			delete(h.dispatched, id)
		}
	}
}

// Submit enqueues a job for its Station and blocks until the serving node Completes it or ctx
// is done. It is the consumer's side. One attempt id may be in flight at a time; a duplicate is
// refused rather than silently sharing a waiter (which would let one result settle two submits).
//
// ctx MUST be bounded/cancellable: cleanup of the waiter is entirely ctx-driven, so a job that
// never completes under a background context would leak one waiter and block one goroutine.
//
// A timeout is "possibly completed", not "definitely not": if ctx fires exactly as the node
// delivers, the result may be dropped while its Receipt still settles downstream via the courier
// + Core one-use enforcement. A caller MUST NOT re-submit under the same attempt id on timeout.
func (h *Hub) Submit(ctx context.Context, job Job) (Result, error) {
	if job.AttemptID == "" || job.StationID == "" {
		return Result{}, ErrEmptyID
	}
	h.mu.Lock()
	sq, ok := h.stations[job.StationID]
	if !ok {
		h.mu.Unlock()
		return Result{}, ErrNoStation
	}
	if _, exists := h.waiters[job.AttemptID]; exists {
		h.mu.Unlock()
		return Result{}, ErrDuplicateAttempt
	}
	if h.inFlight >= maxInFlight {
		h.mu.Unlock()
		return Result{}, ErrBusy
	}
	w := &waiter{ch: make(chan Result, 1), station: job.StationID} // buffered so Complete never blocks
	h.waiters[job.AttemptID] = w
	h.inFlight++
	h.mu.Unlock()

	// Clear OUR waiter on the way out, however this returns - but only if it is still ours.
	// Compare-and-delete so this cannot evict a different Submit that legitimately reused the
	// attempt id after ours completed; the Hub is then self-protecting rather than trusting the
	// caller's id discipline.
	defer func() {
		h.mu.Lock()
		if cur, ok := h.waiters[job.AttemptID]; ok && cur == w {
			delete(h.waiters, job.AttemptID)
		}
		h.inFlight--
		h.mu.Unlock()
	}()

	// Hand the job to the node's queue (or give up if the node is backed up / ctx expires).
	select {
	case sq.jobs <- job:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}

	// Wait for the node's result.
	select {
	case res := <-w.ch:
		return res, nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

// Poll returns the next job for a Station, blocking until one arrives or ctx is done. It is the
// serving node's side - the long-poll it runs in a loop. ok=false means ctx ended (a normal
// long-poll timeout, the node just polls again) or the Station is not registered.
func (h *Hub) Poll(ctx context.Context, stationID string) (Job, bool) {
	h.mu.Lock()
	sq, ok := h.stations[stationID]
	h.mu.Unlock()
	if !ok {
		return Job{}, false
	}
	select {
	case job := <-sq.jobs:
		// Remember the hand-off: this attempt went to THIS station, and only its completion
		// may ride the settle courier. Pruned lazily; TTL outlives the settle window.
		now := time.Now()
		h.mu.Lock()
		for id, d := range h.dispatched {
			if now.After(d.expires) {
				delete(h.dispatched, id)
			}
		}
		h.dispatched[job.AttemptID] = dispatchRecord{station: stationID, expires: now.Add(dispatchedTTL)}
		h.mu.Unlock()
		return job, true
	case <-ctx.Done():
		return Job{}, false
	}
}

// ConsumeDispatched reports whether this hub handed the attempt to the given Station and the
// record has not aged out - the gate on the settle courier - and CONSUMES the record on a
// hit: one carried completion per dispatch, so a node re-posting /complete for 15 minutes
// cannot re-fire the courier per repeat (Core's one-use settle makes a second ride worthless
// anyway). An expired record encountered here is deleted on the spot, so a tower that goes
// quiet does not carry the last busy window's records forever (Poll's sweep only runs while
// jobs still flow). A wrong-Station probe of a live record neither consumes nor confirms.
func (h *Hub) ConsumeDispatched(attemptID, stationID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	d, ok := h.dispatched[attemptID]
	if !ok {
		return false
	}
	if !time.Now().Before(d.expires) {
		delete(h.dispatched, attemptID)
		return false
	}
	if d.station != stationID {
		return false
	}
	delete(h.dispatched, attemptID)
	return true
}

// Complete delivers a node's result to the waiting submitter. stationID is the Station the
// COMPLETING node is authenticated for; the result is delivered only if the attempt actually
// belongs to that Station - so a node serving one Station cannot resolve (and thereby deny) an
// attempt parked for another, even if it learns the attempt id. It is idempotent and safe to
// call for an unknown/already-completed/mismatched attempt (dropped), so a node retrying a
// return never double-settles - one-use is enforced here by the waiter existing at most once,
// and at Core by the one-use settlement.
func (h *Hub) Complete(stationID string, res Result) {
	h.mu.Lock()
	w, ok := h.waiters[res.AttemptID]
	if ok && w.station == stationID {
		delete(h.waiters, res.AttemptID)
	} else {
		ok = false // unknown attempt, or a Station that does not own it: drop.
	}
	h.mu.Unlock()
	if ok {
		w.ch <- res // non-blocking: ch is buffered(1) and used once
	}
}
