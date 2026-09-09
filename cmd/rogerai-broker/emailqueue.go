package main

import (
	"errors"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// emailqueue.go is the ONE paced send queue every outbound email rides (features/ops/
// alert_delivery.feature). Before it, each sendEmail was an independent goroutine POST: an
// alert burst (6 models x 3 recipients x 2 instances in one checker tick) put 36 POSTs
// inside a second against a 10/s provider cap, and the provider DROPPED the excess - those
// pages were lost, not delayed - while sign-in codes queued behind the same burst.
//
// THE CONTRACT:
//   - enqueue never blocks the caller and never errors (sendEmail keeps its old contract);
//   - ONE sender goroutine per process drains at <= rate POSTs in any one-second window (a
//     sliding window over the last `rate` send instants, so two instances at 4/s stay under a
//     10/s provider cap);
//   - TWO LANES: transactional mail (sign-in code, receipts, cap/warn/ban/payout notices) is
//     always sent ahead of ops alerts, so an alert burst delays alerts, never a login;
//   - a 429 / 5xx / transport error is retried up to `retries` times, honoring Retry-After
//     (else 1s, 2s, 4s + jitter), NEVER on the caller's goroutine; then dropped loudly and
//     counted. Other 4xx are permanent and dropped at once;
//   - the queue is bounded: when full, a new ALERT is dropped (counted "queue-full"); a new
//     TRANSACTIONAL mail evicts the OLDEST queued alert instead;
//   - shutdown drains within a budget, transactional first; the rest is counted "shutdown".
//
// The clock is a seam (now/after) so the pacing and backoff are testable without sleeping.

type emailLane int

const (
	laneTransactional emailLane = iota
	laneAlert
	laneCount
)

func (l emailLane) String() string {
	if l == laneAlert {
		return "alert"
	}
	return "transactional"
}

const (
	defaultEmailRate    = 4    // POSTs per second per instance (ROGERAI_EMAIL_RATE)
	defaultEmailQueue   = 1000 // bounded depth across both lanes (ROGERAI_EMAIL_QUEUE)
	defaultEmailRetries = 3    // retries after the first attempt (ROGERAI_EMAIL_RETRIES)
	// emailPaceWindow is the sliding window the rate applies to: one second plus a hair, so
	// timestamps the PROVIDER records (a few ms after ours) still never show rate+1 inside
	// any exact one-second window.
	emailPaceWindow = time.Second + 10*time.Millisecond
	// emailRetryAfterCap bounds an honored Retry-After so a hostile/buggy header cannot
	// park the whole queue for an hour.
	emailRetryAfterCap = 5 * time.Minute
	// emailDrainBudget is how long a stopping broker keeps sending queued mail (transactional
	// first) before counting the rest as dropped{shutdown}.
	emailDrainBudget = 5 * time.Second
)

// errEmailPermanent marks a failure that no retry can fix (a payload we could not even
// build); it is dropped at once with reason "rejected".
var errEmailPermanent = errors.New("permanent email failure")

// emailJob is one queued email. attempts counts POSTs already made; notBefore gates a retry.
type emailJob struct {
	lane                    emailLane
	to, subject, html, text string
	attempts                int
	notBefore               time.Time
}

// emailQueue is the sender state embedded in mailer (kept separate so email.go stays the
// provider-facing half). All fields are guarded by mu unless noted.
type emailQueue struct {
	mu       sync.Mutex
	lanes    [laneCount][]*emailJob // fresh FIFO per lane
	retrying [laneCount][]*emailJob // retries per lane, ordered by notBefore
	sentAt   []time.Time            // pacer: instants of the last <= rate sends
	paused   bool                   // test seam: the sender sits idle while true
	stopping bool                   // drain() called: finish by deadline, then drop the rest
	deadline time.Time
	queued   int64
	sent     int64
	retries  int64
	dropped  map[string]int64 // by reason: queue-full | retries | rejected | shutdown

	startOnce sync.Once
	stopOnce  sync.Once
	wake      chan struct{} // buffered(1): "something was enqueued"
	stopCh    chan struct{} // closed by drain()
	done      chan struct{} // closed when the sender goroutine exits
	parked    atomic.Bool   // the sender is blocked waiting (idle / pacing / backoff)
}

// clock / timer are the seams; nil means the real clock.
func (m *mailer) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func (m *mailer) timer(d time.Duration) <-chan time.Time {
	if m.after != nil {
		return m.after(d)
	}
	return time.After(d)
}

func (m *mailer) effRate() int {
	if m.rate > 0 {
		return m.rate
	}
	return defaultEmailRate
}

func (m *mailer) effCap() int {
	if m.queueCap > 0 {
		return m.queueCap
	}
	return defaultEmailQueue
}

// enqueue appends a job to its lane and wakes the sender. Never blocks, never errors.
func (m *mailer) enqueue(lane emailLane, to, subject, html, text string) {
	q := &m.q
	q.startOnce.Do(m.startSender)
	job := &emailJob{lane: lane, to: to, subject: subject, html: html, text: text}
	q.mu.Lock()
	if q.stopping {
		// A page raised after the drain began (a late onset goroutine) is counted AND named:
		// never silently gone.
		q.dropLocked(job, "shutdown")
		log.Printf("email: DROPPED (shutdown, lane=%s) enqueued after the drain began to=%s subj=%q", job.lane, maskAddr(job.to), job.subject)
		q.mu.Unlock()
		return
	}
	if q.depthLocked() >= m.effCap() {
		// Full. An alert is the thing we can afford to lose; a login is not. A new
		// transactional mail therefore evicts the OLDEST queued alert; a new alert is
		// dropped. If there is no alert to evict, the newcomer is dropped either way.
		victim := job
		if lane == laneTransactional {
			if v := q.popOldestAlertLocked(); v != nil {
				victim = v
			}
		}
		q.dropLocked(victim, "queue-full")
		if victim == job {
			q.mu.Unlock()
			return
		}
	}
	q.lanes[lane] = append(q.lanes[lane], job)
	q.queued++
	q.mu.Unlock()
	m.kick()
}

// kick wakes the sender without blocking (the channel is buffered by one).
func (m *mailer) kick() {
	select {
	case m.q.wake <- struct{}{}:
	default:
	}
}

// startSender runs once per mailer (from the first enqueue). The channels are assigned
// under q.mu because idle()/drain()/emailStats() read them from other goroutines.
func (m *mailer) startSender() {
	q := &m.q
	q.mu.Lock()
	q.wake = make(chan struct{}, 1)
	q.stopCh = make(chan struct{})
	q.done = make(chan struct{})
	q.dropped = map[string]int64{}
	q.mu.Unlock()
	go m.senderLoop()
}

// senderLoop is the single drain goroutine: pick the next ready job (transactional first),
// wait out the pacer, POST, then settle the outcome (sent / retry later / drop).
func (m *mailer) senderLoop() {
	q := &m.q
	defer close(q.done)
	for {
		now := m.clock()
		q.mu.Lock()
		if q.stopping && (!now.Before(q.deadline) || q.depthLocked() == 0) {
			if n := q.depthLocked(); n > 0 {
				q.dropAllLocked("shutdown")
				log.Printf("email: DROPPED %d queued email(s) at shutdown (drain budget exhausted)", n)
			}
			q.mu.Unlock()
			return
		}
		job, wait := q.peekLocked(now)
		stopping, deadline := q.stopping, q.deadline
		q.mu.Unlock()

		// While stopping, the (closed) stop channel must not be selected on again or the
		// loop would spin; the drain deadline timer takes its place.
		stop, dl := q.stopCh, (<-chan time.Time)(nil)
		if stopping {
			stop, dl = nil, m.timer(deadline.Sub(now))
		}
		if job == nil {
			var t <-chan time.Time
			if wait > 0 {
				t = m.timer(wait)
			}
			q.parked.Store(true)
			select {
			case <-q.wake:
			case <-t:
			case <-dl:
			case <-stop:
			}
			q.parked.Store(false)
			continue
		}
		if d := m.paceDelay(now); d > 0 {
			q.parked.Store(true)
			select {
			case <-m.timer(d):
			case <-dl:
			case <-stop:
			}
			q.parked.Store(false)
			continue // re-evaluate: the deadline may have passed, or the head changed
		}

		// Commit: the job is still at the head of its queue (only this goroutine removes,
		// but a transactional enqueue may have evicted an alert head meanwhile).
		q.mu.Lock()
		if !q.removeHeadLocked(job) {
			q.mu.Unlock()
			continue
		}
		q.sentAt = append(q.sentAt, now)
		q.mu.Unlock()

		status, retryAfter, err := m.deliver(job.to, job.subject, job.html, job.text)
		m.settle(job, status, retryAfter, err)
	}
}

// settle records a POST outcome: success, a paced retry, or a counted drop.
func (m *mailer) settle(job *emailJob, status int, retryAfter time.Duration, err error) {
	q := &m.q
	q.mu.Lock()
	defer q.mu.Unlock()
	switch {
	case err == nil && status < 300:
		q.sent++
	case errors.Is(err, errEmailPermanent) || (err == nil && status != http.StatusTooManyRequests && status < 500):
		q.dropLocked(job, "rejected")
	case job.attempts >= m.retries:
		q.dropLocked(job, "retries")
		log.Printf("email: DROPPED after %d retries (to=%s subj=%q): last status=%d err=%v",
			job.attempts, maskAddr(job.to), job.subject, status, err)
	default:
		job.attempts++
		q.retries++
		job.notBefore = m.clock().Add(emailBackoff(job.attempts, retryAfter))
		lane := job.lane
		q.retrying[lane] = append(q.retrying[lane], job)
		// Keep the retry list ordered by notBefore (short lists; insertion sort is enough).
		for i := len(q.retrying[lane]) - 1; i > 0 && q.retrying[lane][i].notBefore.Before(q.retrying[lane][i-1].notBefore); i-- {
			q.retrying[lane][i], q.retrying[lane][i-1] = q.retrying[lane][i-1], q.retrying[lane][i]
		}
		m.kick()
	}
}

// emailBackoff is the delay before retry number `attempt` (1-based): the provider's
// Retry-After when it gave one, else 1s, 2s, 4s, ... plus up to 25% jitter so two instances
// do not retry in lockstep. Both are capped at emailRetryAfterCap, and the doubling stops
// at 256s: an absurd retries knob must never overflow the shift (attempt 35 would turn the
// base negative and panic rand on the sender goroutine, taking the broker down).
func emailBackoff(attempt int, retryAfter time.Duration) time.Duration {
	d := retryAfter
	if d <= 0 {
		shift := min(max(attempt, 1)-1, 8)
		base := time.Second << shift
		d = base + time.Duration(rand.Int63n(int64(base/4)))
	}
	return min(d, emailRetryAfterCap)
}

// parseRetryAfter reads a Retry-After header as delta-seconds or an HTTP-date (shared by the
// email sender and the moderation classifier client); 0 when absent or unparseable.
func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// paceDelay returns how long the sender must wait before the next POST keeps the rate:
// zero when fewer than `rate` sends happened inside the sliding window. Takes q.mu itself.
func (m *mailer) paceDelay(now time.Time) time.Duration {
	q := &m.q
	q.mu.Lock()
	defer q.mu.Unlock()
	keep := q.sentAt[:0]
	for _, t := range q.sentAt {
		if now.Sub(t) < emailPaceWindow {
			keep = append(keep, t)
		}
	}
	q.sentAt = keep
	if len(q.sentAt) < m.effRate() {
		return 0
	}
	return q.sentAt[0].Add(emailPaceWindow).Sub(now)
}

// peekLocked returns the next ready job WITHOUT removing it: transactional before alert,
// and within a lane a due retry before fresh mail. When nothing is ready, wait is the time
// until the earliest retry is due (0 = nothing pending at all).
func (q *emailQueue) peekLocked(now time.Time) (*emailJob, time.Duration) {
	if q.paused {
		return nil, 0
	}
	var wait time.Duration
	for lane := emailLane(0); lane < laneCount; lane++ {
		if r := q.retrying[lane]; len(r) > 0 {
			if !r[0].notBefore.After(now) {
				return r[0], 0
			}
			if d := r[0].notBefore.Sub(now); wait == 0 || d < wait {
				wait = d
			}
		}
		if f := q.lanes[lane]; len(f) > 0 {
			return f[0], 0
		}
	}
	return nil, wait
}

// removeHeadLocked pops job if it is still the head of its (retry or fresh) list.
func (q *emailQueue) removeHeadLocked(job *emailJob) bool {
	lane := job.lane
	if r := q.retrying[lane]; len(r) > 0 && r[0] == job {
		q.retrying[lane] = r[1:]
		return true
	}
	if f := q.lanes[lane]; len(f) > 0 && f[0] == job {
		q.lanes[lane] = f[1:]
		return true
	}
	return false
}

// popOldestAlertLocked removes and returns the oldest queued alert (fresh first, then a
// pending retry), or nil.
func (q *emailQueue) popOldestAlertLocked() *emailJob {
	if f := q.lanes[laneAlert]; len(f) > 0 {
		q.lanes[laneAlert] = f[1:]
		return f[0]
	}
	if r := q.retrying[laneAlert]; len(r) > 0 {
		q.retrying[laneAlert] = r[1:]
		return r[0]
	}
	return nil
}

func (q *emailQueue) depthLocked() int {
	n := 0
	for lane := emailLane(0); lane < laneCount; lane++ {
		n += len(q.lanes[lane]) + len(q.retrying[lane])
	}
	return n
}

func (q *emailQueue) dropLocked(job *emailJob, reason string) {
	q.dropped[reason]++
	if reason == "queue-full" || reason == "rejected" {
		log.Printf("email: DROPPED (%s, lane=%s) to=%s subj=%q", reason, job.lane, maskAddr(job.to), job.subject)
	}
}

func (q *emailQueue) dropAllLocked(reason string) {
	for lane := emailLane(0); lane < laneCount; lane++ {
		for _, j := range q.lanes[lane] {
			q.dropLocked(j, reason)
		}
		for _, j := range q.retrying[lane] {
			q.dropLocked(j, reason)
		}
		q.lanes[lane], q.retrying[lane] = nil, nil
	}
}

// drain is the shutdown flush: keep sending (transactional first, still paced) until the
// queue is empty or the budget elapses, then count whatever is left as dropped{shutdown}.
// A mailer whose sender never started has nothing to drain.
func (m *mailer) drain(budget time.Duration) {
	if m == nil {
		return
	}
	q := &m.q
	q.mu.Lock()
	started := q.done != nil
	q.stopping = true
	q.paused = false
	q.deadline = m.clock().Add(budget)
	q.mu.Unlock()
	if !started {
		return
	}
	q.stopOnce.Do(func() { close(q.stopCh) })
	timeout := m.timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	select {
	case <-q.done:
	case <-time.After(budget + timeout + time.Second): // real-time backstop: never hang a shutdown
		log.Printf("email: drain did not finish within its budget; abandoning the sender")
	}
}

// idle reports whether the sender is parked (nothing to do, or waiting on a timer) or has
// exited. A never-started sender is idle.
func (m *mailer) idle() bool {
	q := &m.q
	q.mu.Lock()
	done := q.done
	q.mu.Unlock()
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	default:
	}
	return q.parked.Load()
}

// emailStats is the /admin/live block: queue counters + depth per lane. Nil-safe.
func (m *mailer) emailStats() map[string]any {
	out := map[string]any{
		"email_queued":  int64(0),
		"email_sent":    int64(0),
		"email_retries": int64(0),
		"email_dropped": map[string]int64{},
		"queue_depth":   map[string]any{"transactional": 0, "alert": 0},
	}
	if m == nil {
		return out
	}
	q := &m.q
	q.mu.Lock()
	defer q.mu.Unlock()
	dropped := map[string]int64{}
	for k, v := range q.dropped {
		dropped[k] = v
	}
	out["email_queued"] = q.queued
	out["email_sent"] = q.sent
	out["email_retries"] = q.retries
	out["email_dropped"] = dropped
	out["queue_depth"] = map[string]any{
		"transactional": len(q.lanes[laneTransactional]) + len(q.retrying[laneTransactional]),
		"alert":         len(q.lanes[laneAlert]) + len(q.retrying[laneAlert]),
	}
	return out
}
