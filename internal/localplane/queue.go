package localplane

import (
	"context"
	"sync"
	"time"
)

// The local work queue is how a standalone Tower serves a completion WITHOUT dialing out. A
// consumer's request is enqueued here; a `roger share` station POLLS for work matching its
// models, runs it on its own hardware, and returns the answer. The Tower never opens a
// connection to the station - the station always connects IN - which is what lets the
// handler package hold to its no-outbound-call guarantee.
//
// It is a plain in-memory queue: a standalone plant is one process, and nothing here needs to
// survive a restart (an in-flight request whose Tower restarted is simply retried by the
// client). Every wait is bounded by the caller's context, so neither a consumer nor a station
// blocks forever.

// jobResult is what a station returns for a job: the answer bytes and which station served it,
// or a failure reason the consumer is told about without detail that would identify internals.
type jobResult struct {
	answer    []byte
	stationID string
}

type job struct {
	id      string
	model   string
	body    []byte
	takenBy string         // the station id that polled it; only that station may complete it
	result  chan jobResult // buffered(1): complete never blocks, even if the consumer gave up
}

type queue struct {
	mu       sync.Mutex
	pending  []*job
	inflight map[string]*job
	notify   chan struct{} // a station poll wakes on this when a job arrives
}

func newQueue() *queue {
	return &queue{inflight: map[string]*job{}, notify: make(chan struct{}, 1)}
}

// submit enqueues a job and returns it; the caller waits on job.result. The id is the
// caller's request id, unique per in-flight request.
func (q *queue) submit(id, model string, body []byte) *job {
	j := &job{id: id, model: model, body: body, result: make(chan jobResult, 1)}
	q.mu.Lock()
	q.pending = append(q.pending, j)
	q.mu.Unlock()
	q.wake()
	return j
}

// wake signals pollers that the pending set changed, without blocking if one is already
// pending (the channel is buffered to depth 1 and a poller re-scans the whole set on wake).
func (q *queue) wake() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// take moves the first pending job whose model any of `models` serves into in-flight and
// returns it. It does not block; poll wraps it with waiting.
func (q *queue) take(stationID string, models []string) (*job, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, j := range q.pending {
		if serves(models, j.model) {
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			j.takenBy = stationID
			q.inflight[j.id] = j
			return j, true
		}
	}
	return nil, false
}

// poll blocks until a job matching one of the station's models is available or the context is
// done. A station calls this to fetch work; the Tower dials nobody.
func (q *queue) poll(ctx context.Context, stationID string, models []string) (*job, bool) {
	for {
		if j, ok := q.take(stationID, models); ok {
			return j, true
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-q.notify:
			// A job arrived (or another poller took it); loop and re-scan.
		case <-time.After(250 * time.Millisecond):
			// A backstop wake, so a poll that missed a notify race still re-scans promptly.
		}
	}
}

// complete delivers a station's answer to the waiting consumer. It reports whether the job was
// actually in flight (a late or forged completion for an unknown id changes nothing). Delivery
// never blocks: the result channel is buffered, and a consumer that already gave up leaves the
// buffered value to be garbage-collected with the job.
func (q *queue) complete(id, stationID string, answer []byte) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	j, ok := q.inflight[id]
	// Only the station that took the job may complete it: a forged completion from another
	// station (guessing an id) changes nothing.
	if !ok || j.takenBy != stationID {
		return false
	}
	delete(q.inflight, id)
	// Deliver UNDER the lock: the result channel is buffered to depth 1 and a job completes
	// once, so this never blocks, and holding the lock makes "removed from in-flight" and
	// "answer is in the channel" one atomic step. That is what lets a consumer's abandon-
	// then-drain be correct: after abandon returns, either the answer was already delivered
	// (drain finds it) or a later complete finds the job gone and reports delivered=false -
	// there is no gap in which an answer is both reported delivered and lost.
	j.result <- jobResult{answer: answer, stationID: stationID}
	return true
}

// abandon drops a job whose consumer gave up (timed out or disconnected), from BOTH the
// pending set and the in-flight set. Removing it from pending is what stops a never-taken
// job from leaking there forever and from later being handed to a station to execute as
// stale work the consumer will never read. Idempotent.
func (q *queue) abandon(id string) {
	q.mu.Lock()
	delete(q.inflight, id)
	for i, j := range q.pending {
		if j.id == id {
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			break
		}
	}
	q.mu.Unlock()
}

func serves(models []string, model string) bool {
	for _, m := range models {
		if m == model {
			return true
		}
	}
	return false
}
