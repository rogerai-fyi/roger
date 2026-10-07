package main

// dispatchq.go is multi-instance dispatch that scales (features/multinode/dispatch_inbox.feature).
//
// The old bus opened one shared-store SUBSCRIBE per held node poll and one per in-flight relay,
// so store connections grew with pollers and requests, and dispatch was a fan-out PUBLISH plus
// a claim race. Here store connections are a fixed number per instance:
//
//   rogerai:dq:q:<node>       LIST    the node's queued jobs. A poll LPOPs: exactly one taker,
//                                     no claim key. The origin LREMs to withdraw: exactly one of
//                                     the two wins.
//   rogerai:dq:route:<node>   HASH    instance -> "idle:peak:unixms", the idle pollers each
//                                     instance holds for the node (advisory: a stale entry costs
//                                     a bounce, never a lost or doubled job).
//   rogerai:dq:origin:<job>   STRING  the instance holding the consumer, so a result or stream
//                                     chunk posted to ANY instance finds its way back.
//   rogerai:dq:inbox:<inst>   STREAM  messages addressed to one instance, read by ONE reader on a
//                                     dedicated connection: nudge, bounce, taken, res, chunk, done.
//
// Lost wake-ups are impossible by ordering: the dispatcher PUSHES then reads the idle pollers;
// a poller ADVERTISES itself idle then pops. One of the two always sees the other.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"rogerai.fm/roger/v6/internal/protocol"
)

// dispatchMode is the rollout switch (ROGERAI_DISPATCH). Every mode's pollers serve BOTH the
// legacy bus and the queue, except queue-only, so old and new instances overlap safely.
type dispatchMode int

const (
	dispatchViaBus       dispatchMode = iota // default: dispatch by legacy publish + claim
	dispatchViaQueue                         // dispatch through the queue; pollers still hear the bus
	dispatchViaQueueOnly                     // queue only: no per-poll subscription at all
)

func dispatchModeFromEnv() dispatchMode {
	switch strings.ToLower(strings.TrimSpace(envStr("ROGERAI_DISPATCH", ""))) {
	case "queue":
		return dispatchViaQueue
	case "queue-only":
		return dispatchViaQueueOnly
	}
	return dispatchViaBus
}

const (
	dqListPrefix   = keyPrefix + "dq:q:"
	dqRoutePrefix  = keyPrefix + "dq:route:"
	dqOriginPrefix = keyPrefix + "dq:origin:"
	dqInboxPrefix  = keyPrefix + "dq:inbox:"

	dqKeyTTL      = 10 * time.Minute // list/origin/inbox keys outlive any relay deadline
	dqRouteTTL    = time.Minute
	dqRouteFresh  = 30 * time.Second // > the 25s poll hold: an idle poll re-advertises within it
	dqInboxMaxLen = 10000            // a reader lagging this far behind is broken, not slow
	dqReadBlock   = 5 * time.Second
	dqPeakWindow  = 2 * time.Minute
	dqFrameBuf    = 1024 // stream chunks buffered per ticket before the stream is failed
)

// Tunables (vars so scenarios can shorten them; production never mutates them).
var (
	queueWaitAudio = envDuration("ROGERAI_QUEUE_WAIT_AUDIO", 3*time.Second)
	queueWaitChat  = envDuration("ROGERAI_QUEUE_WAIT_CHAT", 10*time.Second)
	// dqPopCheck is how often a waiting ticket checks its job is still queued (and re-nudges).
	dqPopCheck = time.Second
	// dqTakenGrace is how long a popped job may go without a "taken" before it is declared
	// lost (the popping instance died, or its write to the node failed), or, for a node that
	// acks, put back for another delivery. Atomic so scenarios can shorten it while watchers run.
	dqTakenGrace = newAtomicDuration(5 * time.Second)
	// dqAfterPop is a TEST HOOK run between a poll's pop and its handoff (nil in production);
	// returning true simulates the process dying at that point.
	dqAfterPop func(inst string) (crashed bool)
	// dqBeforeDispatch is a TEST HOOK run as a queue dispatch starts (nil in production): the
	// dispatch-failure scenarios use it to change REAL state at the one moment that matters
	// (the station drops off air, the store fails) between the pick and the dispatch. The
	// returned func, when non-nil, runs as the dispatch call returns.
	dqBeforeDispatch func(nodeID string) (after func())
)

var (
	errOffAir      = errors.New("station off air")
	errStationBusy = errors.New("station busy")
	errHandoffLost = errors.New("station handoff failed")
	errStreamLag   = errors.New("stream consumer too slow")
	errBadResult   = errors.New("undecodable station result")
)

// dqEntry is one queued job, stored as the exact LIST element (the origin LREMs these bytes).
type dqEntry struct {
	Origin   string          `json:"o"`
	Deadline int64           `json:"dl"` // unix ms: the relay has given up after this
	Job      json.RawMessage `json:"job"`
	raw      []byte
}

// pollWaiter is one held node poll on this instance.
type pollWaiter struct {
	node       string
	wake       chan *dqEntry // nil = a nudge (pop the list); non-nil = a direct local handoff
	advertised bool          // in the idle set (guarded by dispatchQueue.mu)
}

type peakIdle struct {
	cur, prev int
	since     time.Time
}

// dispatchQueue is this instance's end of the dispatch plane.
type dispatchQueue struct {
	b    *broker
	self string
	rdb  redis.UniversalClient // pooled ops (the shared store's client)
	blk  redis.UniversalClient // the inbox reader's dedicated connection

	stopCh   chan struct{}
	stopOnce sync.Once

	mu      sync.Mutex
	idle    map[string][]*pollWaiter
	peak    map[string]*peakIdle
	tickets map[string]*dispatchTicket
	svcMs   map[string]float64 // node -> EWMA of taken-to-result time, seen by this origin

	// routeMu serializes route writes: each computes the value inside the lock, so the last
	// write always carries the latest idle count (two writes cannot land out of order).
	routeMu sync.Mutex
}

// dqueue returns this instance's dispatch plane, starting it on first use. nil when there is
// no Valkey-backed shared store (single-instance, or a test double).
func (b *broker) dqueue() *dispatchQueue {
	b.dqOnce.Do(func() {
		vs, ok := b.shared.(*valkeyStore)
		if !ok || vs == nil || vs.rdb == nil || b.instanceID == "" {
			return
		}
		q := &dispatchQueue{
			b: b, self: b.instanceID, rdb: vs.rdb, blk: vs.dial(dqReadBlock + 2*time.Second),
			stopCh: make(chan struct{}),
			idle:   map[string][]*pollWaiter{}, peak: map[string]*peakIdle{}, tickets: map[string]*dispatchTicket{},
			svcMs: map[string]float64{},
		}
		go func() {
			select {
			case <-vs.closed:
				q.stop()
			case <-q.stopCh:
			}
		}()
		go q.run()
		b.dq = q
	})
	return b.dq
}

// stop ends this instance's participation (the store closed, or a scenario kills the instance).
func (q *dispatchQueue) stop() {
	q.stopOnce.Do(func() {
		close(q.stopCh)
		_ = q.blk.Close()
	})
}

func (q *dispatchQueue) stopped() bool {
	select {
	case <-q.stopCh:
		return true
	default:
		return false
	}
}

func opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), sharedOpTimeout)
}

// ---- the inbox ---------------------------------------------------------------------------

type dqMsg struct {
	kind, job, node, from string
	data                  []byte
}

// send delivers a message to an instance: in-process for itself, else onto its inbox.
func (q *dispatchQueue) send(inst string, m dqMsg) error {
	m.from = q.self
	if inst == q.self {
		q.handle(m)
		return nil
	}
	ctx, cancel := opCtx()
	defer cancel()
	err := q.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: dqInboxPrefix + inst, MaxLen: dqInboxMaxLen, Approx: true,
		Values: []any{"k", m.kind, "j", m.job, "n", m.node, "f", m.from, "d", m.data},
	}).Err()
	if err != nil {
		q.noteErr("dq send "+m.kind, err)
	}
	return err
}

func (q *dispatchQueue) noteErr(op string, err error) {
	if vs, ok := q.b.shared.(*valkeyStore); ok {
		vs.noteErr(op, err)
	}
}

// run is the ONE inbox reader: a blocking XREAD from the last id seen, then a trim of what
// was consumed so the stream holds only unread messages.
func (q *dispatchQueue) run() {
	key := dqInboxPrefix + q.self
	last := "0-0"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-q.stopCh; cancel() }()
	for !q.stopped() {
		res, err := q.blk.XRead(ctx, &redis.XReadArgs{Streams: []string{key, last}, Count: 256, Block: dqReadBlock}).Result()
		if err != nil && err != redis.Nil {
			if q.stopped() {
				return
			}
			q.noteErr("dq inbox read", err)
			select {
			case <-q.stopCh:
				return
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		for _, st := range res {
			for _, xm := range st.Messages {
				last = xm.ID
				q.handle(dqMsg{
					kind: xstr(xm.Values["k"]), job: xstr(xm.Values["j"]), node: xstr(xm.Values["n"]),
					from: xstr(xm.Values["f"]), data: []byte(xstr(xm.Values["d"])),
				})
			}
		}
		tctx, tcancel := opCtx()
		if len(res) > 0 {
			q.blk.XTrimMinID(tctx, key, last) // everything before the last consumed id
		}
		q.blk.Expire(tctx, key, dqKeyTTL) // a dead instance's inbox ages out
		tcancel()
	}
}

func xstr(v any) string {
	s, _ := v.(string)
	return s
}

// handle routes one inbox message. It never blocks: the reader serves every ticket and poll.
func (q *dispatchQueue) handle(m dqMsg) {
	switch m.kind {
	case "nudge": // wake one idle local poller of the node, or say we have none
		if q.wakeIdle(m.node, nil) {
			return
		}
		q.b.stats.dqBounce.Add(1)
		go q.send(m.from, dqMsg{kind: "bounce", job: m.job, node: m.node}) // never block the reader
		return
	case "rcf": // a remote-control frame for this instance's viewers (rcinbox.go)
		q.b.rcFrameArrived(q, m.job, m.data)
		return
	case "rcin": // an inbound queued for a host poll waiting here
		q.b.rcHostWake(m.job)
		return
	}
	q.mu.Lock()
	tk := q.tickets[m.job]
	q.mu.Unlock()
	if tk == nil {
		if m.kind == "res" {
			go q.b.lateResultRaw(m.node, m.data) // a result after its 504 is recorded at $0 (§14.10)
		}
		return // the relay already finished
	}
	switch m.kind {
	case "bounce":
		tk.markTried(m.from)
		go q.nudge(tk) // Valkey I/O: never on the reader
	case "taken":
		tk.markTaken()
	case "sent": // written to an ack-capable poll: taken only when the node acks
		tk.mu.Lock()
		tk.sent++
		tk.mu.Unlock()
		select {
		case tk.sentCh <- struct{}{}:
		default:
		}
	case "ack":
		if m.node != tk.node {
			return // an ack can only confirm the acking node's own job
		}
		tk.markTaken()
		q.dropRequeued(tk)
	case "res":
		tk.markTaken()
		q.dropRequeued(tk)
		q.noteServe(tk)
		select {
		case tk.res <- m.data:
		default:
		}
	case "chunk", "done":
		tk.markTaken()
		select {
		case tk.frames <- streamFrame{payload: m.data, isDone: m.kind == "done"}:
		default:
			tk.fail(errStreamLag) // never block the reader on one slow consumer
		}
	}
}

// ---- tickets (the origin's side of one dispatched job) ----------------------------------

// dispatchTicket is one dispatched job awaited by its origin. Exactly one of res (the result
// bytes) or done (a terminal failure, err) resolves it; frames carries a stream's chunks.
type dispatchTicket struct {
	q      *dispatchQueue
	id     string
	node   string
	elem   []byte
	res    chan []byte
	frames chan streamFrame
	done   chan struct{}
	err    error

	once      sync.Once
	takenOnce sync.Once
	taken     chan struct{}
	takenAt   time.Time // set once, before taken closes
	closed    chan struct{}
	closeOnce sync.Once

	mu    sync.Mutex
	tried map[string]bool

	// Node acks (node_ack.feature): raw is the list element (kept even for an in-memory
	// handoff, so the job can be put back); sent counts deliveries to an ack-capable poll.
	raw      []byte
	sent     int
	sentCh   chan struct{} // signalled on each delivery to an ack-capable poll
	requeued bool

	legacyCancel func() // dispatchViaBus: tears down the legacy subscriptions
}

func newTicket(q *dispatchQueue, id, node string) *dispatchTicket {
	return &dispatchTicket{
		q: q, id: id, node: node,
		res: make(chan []byte, 1), frames: make(chan streamFrame, dqFrameBuf),
		done: make(chan struct{}), taken: make(chan struct{}), closed: make(chan struct{}),
		tried: map[string]bool{}, sentCh: make(chan struct{}, 1),
	}
}

func (t *dispatchTicket) fail(err error) {
	t.once.Do(func() { t.err = err; close(t.done) })
}

func (t *dispatchTicket) markTaken() {
	t.takenOnce.Do(func() { t.takenAt = time.Now(); close(t.taken) })
}

func (t *dispatchTicket) isTaken() bool {
	select {
	case <-t.taken:
		return true
	default:
		return false
	}
}

func (t *dispatchTicket) getElem() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.elem
}

func (t *dispatchTicket) setElem(b []byte) {
	t.mu.Lock()
	t.elem = b
	t.mu.Unlock()
}

func (t *dispatchTicket) markTried(inst string) {
	t.mu.Lock()
	t.tried[inst] = true
	t.mu.Unlock()
}

// Err is the terminal failure (valid once done is closed).
func (t *dispatchTicket) Err() error { return t.err }

// close is the caller's "finished": the ticket stops receiving, and a job still queued is
// withdrawn so no node serves it after the consumer is gone.
func (t *dispatchTicket) close() {
	t.closeOnce.Do(func() {
		close(t.closed)
		if t.legacyCancel != nil {
			t.legacyCancel()
			return
		}
		t.q.mu.Lock()
		delete(t.q.tickets, t.id)
		t.q.mu.Unlock()
		if elem := t.getElem(); !t.isTaken() && elem != nil {
			ctx, cancel := opCtx()
			t.q.rdb.LRem(ctx, dqListPrefix+t.node, 1, elem)
			cancel()
		}
	})
}

// awaitResult waits for the result, a terminal failure, or the deadline.
func (t *dispatchTicket) awaitResult(deadline time.Time) ([]byte, error) {
	return t.awaitResultCtx(context.Background(), deadline)
}

// errConsumerGone ends a wait whose consumer disconnected (job_cancel.feature).
var errConsumerGone = errors.New("the consumer disconnected")

// awaitResultCtx is awaitResult that also ends when ctx (the consumer's request) is done, so a
// multi-instance relay notices a disconnect exactly as the local path does.
func (t *dispatchTicket) awaitResultCtx(ctx context.Context, deadline time.Time) ([]byte, error) {
	select {
	case raw := <-t.res:
		return raw, nil
	case <-t.done:
		return nil, t.err
	case <-ctx.Done():
		return nil, errConsumerGone
	case <-time.After(time.Until(deadline)):
		return nil, context.DeadlineExceeded
	}
}

// forward decodes the ticket's result into resCh (buffered 1), so a caller's existing wait on
// resCh serves both the local and the multi-instance path. It ends on a failure or close.
func (t *dispatchTicket) forward(resCh chan protocol.JobResult) {
	go func() {
		select {
		case raw := <-t.res:
			var res protocol.JobResult
			if json.Unmarshal(raw, &res) == nil {
				select {
				case resCh <- res:
				default:
				}
			}
		case <-t.done:
		case <-t.closed:
		}
	}()
}

// ---- dispatch (origin) --------------------------------------------------------------------

// dispatchRemote hands a job to one of the node's pollers on any instance and returns the
// ticket its result (and stream chunks) arrive on. The caller must close the ticket.
func (b *broker) dispatchRemote(ctx context.Context, nodeID string, job protocol.Job, stream bool) (*dispatchTicket, error) {
	if b.dispatchMode == dispatchViaBus {
		tk, err := b.legacyTicket(ctx, nodeID, job, stream)
		if errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("dispatch store: %v", err) // a store timeout, not the station's
		}
		return tk, err
	}
	q := b.dqueue()
	if q == nil {
		return nil, errNoSharedStore
	}
	return q.dispatch(nodeID, job)
}

// legacyTicket wraps the fan-out bus (dispatchViaBus) in a ticket so callers have one shape.
func (b *broker) legacyTicket(ctx context.Context, nodeID string, job protocol.Job, stream bool) (*dispatchTicket, error) {
	tk := newTicket(nil, job.ID, nodeID)
	lctx, lcancel := context.WithCancel(ctx)
	var cancels []func()
	tk.legacyCancel = func() {
		lcancel()
		for _, c := range cancels {
			c()
		}
	}
	if stream {
		frames, scancel, err := b.shared.busSubscribeStream(lctx, job.ID)
		if err != nil {
			tk.legacyCancel()
			return nil, err
		}
		cancels = append(cancels, scancel)
		go func() {
			for fr := range frames {
				select {
				case tk.frames <- fr:
				case <-tk.closed:
					return
				}
				if fr.isDone {
					return
				}
			}
		}()
	}
	ch, cancel, err := b.busDispatchJob(lctx, nodeID, job)
	if err != nil {
		tk.legacyCancel()
		return nil, err
	}
	cancels = append(cancels, cancel)
	go func() {
		if raw, ok := <-ch; ok {
			tk.res <- raw // buffered 1: the one result never blocks
		}
	}()
	return tk, nil
}

// queueWaitFor is how long a job may wait for a poller before it is withdrawn as busy.
func queueWaitFor(job protocol.Job) time.Duration {
	if job.Path != "" { // the audio relay tags the node endpoint; chat leaves it empty
		return queueWaitAudio
	}
	return queueWaitChat
}

func (b *broker) nodeLive(nodeID string) bool {
	b.mu.Lock()
	seen, ok := b.lastSeen[nodeID]
	b.mu.Unlock()
	return ok && time.Since(seen) < nodeTTL
}

func (q *dispatchQueue) dispatch(nodeID string, job protocol.Job) (*dispatchTicket, error) {
	if h := dqBeforeDispatch; h != nil {
		if after := h(nodeID); after != nil {
			defer after()
		}
	}
	if !q.b.nodeLive(nodeID) {
		q.b.stats.dqOffAir.Add(1)
		return nil, errOffAir
	}
	jraw, err := json.Marshal(job)
	if err != nil {
		return nil, err
	}
	wait := queueWaitFor(job)
	// A job popped after this is one whose origin died without withdrawing it: drop it.
	e := dqEntry{Origin: q.self, Deadline: time.Now().Add(wait + dqTakenGrace.get() + 30*time.Second).UnixMilli(), Job: jraw}
	elem, _ := json.Marshal(e)
	e.raw = elem

	tk := newTicket(q, job.ID, nodeID)
	tk.elem, tk.raw = elem, elem
	q.mu.Lock()
	q.tickets[job.ID] = tk
	q.mu.Unlock()
	abort := func(err error) (*dispatchTicket, error) {
		q.mu.Lock()
		delete(q.tickets, job.ID)
		q.mu.Unlock()
		if err != errStationBusy {
			// A store failure, never to be mistaken for the station timing out (a store op
			// that times out returns context.DeadlineExceeded).
			err = fmt.Errorf("dispatch store: %v", err)
		}
		return nil, err
	}

	ctx, cancel := opCtx()
	defer cancel()
	// The origin first, so a result posted anywhere can always find its way back.
	if err := q.rdb.Set(ctx, dqOriginPrefix+job.ID, q.self, dqKeyTTL).Err(); err != nil {
		q.noteErr("dq origin", err)
		return abort(err)
	}
	// Local first: an idle poller on this instance takes the job in memory.
	tk.setElem(nil) // never in the list (unless the poller hands it back: requeue)
	if q.wakeIdle(nodeID, &e) {
		q.b.stats.localDispatch.Add(1)
		go q.watch(tk, wait)
		return tk, nil
	}
	tk.setElem(elem)
	lkey := dqListPrefix + nodeID
	var n *redis.IntCmd
	if _, err := q.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		n = p.RPush(ctx, lkey, elem)
		p.Expire(ctx, lkey, dqKeyTTL)
		return nil
	}); err != nil {
		q.noteErr("dq push", err)
		return abort(err)
	}
	// Admission: a queue that could not drain within the queue wait would only expire.
	if q.tooDeep(nodeID, n.Val(), wait) {
		q.rdb.LRem(ctx, lkey, 1, elem)
		q.b.stats.dqBusy.Add(1)
		return abort(errStationBusy)
	}
	q.b.stats.busDispatch.Add(1)
	q.nudge(tk)
	go q.watch(tk, wait)
	return tk, nil
}

// watch withdraws a job nobody took within its queue wait, and declares a popped job lost
// when its handoff never reports "taken".
func (q *dispatchQueue) watch(tk *dispatchTicket, wait time.Duration) {
	withdraw := time.NewTimer(wait)
	defer withdraw.Stop()
	check := time.NewTicker(dqPopCheck)
	defer check.Stop()
	var lost <-chan time.Time
	grace := dqTakenGrace.get() // read once: fixed for this job's life
	startLost := func() {
		if lost == nil {
			lost = time.After(grace)
		}
	}
	lkey := dqListPrefix + tk.node
	for {
		select {
		case <-tk.sentCh:
			// A poll took it: the queue wait no longer applies; the ack (or its absence) decides.
			withdraw.Stop()
			startLost()
		case <-tk.taken:
			return
		case <-tk.done:
			return
		case <-tk.closed:
			return
		case <-q.stopCh:
			tk.fail(errHandoffLost)
			return
		case <-withdraw.C:
			elem := tk.getElem()
			if elem == nil {
				startLost() // handed in memory; the poller will report taken or not at all
				continue
			}
			ctx, cancel := opCtx()
			removed, err := q.rdb.LRem(ctx, lkey, 1, elem).Result()
			cancel()
			if err == nil && removed == 1 {
				q.b.stats.dqBusy.Add(1)
				tk.fail(errStationBusy) // never taken: withdrawn, never served late
				return
			}
			startLost() // a poller popped it: it must report taken
		case <-check.C:
			if lost != nil {
				continue
			}
			elem := tk.getElem()
			if elem == nil {
				startLost()
				continue
			}
			ctx, cancel := opCtx()
			_, err := q.rdb.LPos(ctx, lkey, string(elem), redis.LPosArgs{}).Result()
			cancel()
			switch err {
			case redis.Nil:
				startLost() // popped: the taker must report within the grace
			case nil:
				q.nudge(tk) // still queued: wake someone else (a dead or busy target)
			}
		case <-lost:
			if tk.isTaken() {
				return
			}
			if elem := tk.getElem(); elem != nil {
				ctx, cancel := opCtx()
				_, err := q.rdb.LPos(ctx, lkey, string(elem), redis.LPosArgs{}).Result()
				cancel()
				if err == nil { // handed back (requeued): it is waiting again, not lost
					lost = nil
					continue
				}
			}
			tk.mu.Lock()
			sent := tk.sent
			tk.mu.Unlock()
			if sent > 0 && sent < dqMaxDeliveries {
				// Written to an ack-capable poll but never acked: the node did not get it (or
				// its ack was lost; the node dedupes). Put it back at the front and wake a poll.
				q.redeliver(tk, wait)
				withdraw.Reset(wait) // nobody takes the copy: busy, not a wait to the relay deadline
				lost = nil
				continue
			}
			q.b.stats.dqLost.Add(1)
			tk.fail(errHandoffLost)
			return
		}
	}
}

// dropRequeued withdraws a put-back copy of a job the node has confirmed (never blocks the reader).
func (q *dispatchQueue) dropRequeued(tk *dispatchTicket) {
	tk.mu.Lock()
	requeued, raw := tk.requeued, tk.raw
	tk.mu.Unlock()
	if !requeued {
		return
	}
	go func() {
		ctx, cancel := opCtx()
		defer cancel()
		q.rdb.LRem(ctx, dqListPrefix+tk.node, 0, raw)
	}()
}

// dqMaxDeliveries bounds how often an unacked job is written before the consumer gets a 503.
const dqMaxDeliveries = 3

// redeliver puts an unacked job back at the front of its node's list and wakes a poll.
func (q *dispatchQueue) redeliver(tk *dispatchTicket, wait time.Duration) {
	tk.mu.Lock()
	var e dqEntry
	if json.Unmarshal(tk.raw, &e) == nil { // a fresh deadline: the copy must not be dropped as expired
		e.Deadline = time.Now().Add(wait + dqTakenGrace.get() + 30*time.Second).UnixMilli()
		if raw, err := json.Marshal(e); err == nil {
			tk.raw = raw
		}
	}
	tk.requeued = true
	tk.elem = tk.raw
	tk.tried = map[string]bool{}
	raw := tk.raw
	tk.mu.Unlock()
	ctx, cancel := opCtx()
	err := q.rdb.LPush(ctx, dqListPrefix+tk.node, raw).Err()
	cancel()
	if err != nil {
		q.noteErr("dq redeliver", err)
		return
	}
	q.b.stats.dqRedeliver.Add(1)
	q.nudge(tk)
}

// nudge wakes one idle poller of the ticket's node: local first, else the instance the route
// says holds the most idle pollers and has not bounced this ticket.
func (q *dispatchQueue) nudge(tk *dispatchTicket) {
	if q.wakeIdle(tk.node, nil) {
		return
	}
	route := q.readRoute(tk.node) // before tk.mu: the reader takes it in markTried
	best, bestIdle := "", 0
	tk.mu.Lock()
	for inst, r := range route {
		if inst != q.self && !tk.tried[inst] && r.idle > bestIdle {
			best, bestIdle = inst, r.idle
		}
	}
	if best != "" {
		tk.tried[best] = true
	}
	tk.mu.Unlock()
	if best == "" {
		return // no idle poller anywhere: the next poll to arrive pops the list
	}
	q.b.stats.dqNudge.Add(1)
	_ = q.send(best, dqMsg{kind: "nudge", job: tk.id, node: tk.node})
}

// ---- the route table ----------------------------------------------------------------------

type routeEntry struct{ idle, peak int }

func (q *dispatchQueue) readRoute(node string) map[string]routeEntry {
	ctx, cancel := opCtx()
	defer cancel()
	all, err := q.rdb.HGetAll(ctx, dqRoutePrefix+node).Result()
	if err != nil {
		q.noteErr("dq route read", err)
		return nil
	}
	out := map[string]routeEntry{}
	now := time.Now().UnixMilli()
	for inst, v := range all {
		f := strings.Split(v, ":")
		if len(f) != 3 {
			continue
		}
		idle, _ := strconv.Atoi(f[0])
		peak, _ := strconv.Atoi(f[1])
		at, _ := strconv.ParseInt(f[2], 10, 64)
		if now-at > dqRouteFresh.Milliseconds() {
			continue
		}
		out[inst] = routeEntry{idle: idle, peak: peak}
	}
	return out
}

// noteServe folds one taken-to-result time into the node's serve-time EWMA.
func (q *dispatchQueue) noteServe(tk *dispatchTicket) {
	ms := float64(time.Since(tk.takenAt).Microseconds()) / 1000
	q.mu.Lock()
	q.svcMs[tk.node] = ewma(q.svcMs[tk.node], ms, 0.3)
	q.mu.Unlock()
}

// tooDeep is admission: refuse at once when the queue could not drain within the job's queue
// wait at the node's measured serve rate. With no measurement yet it admits (the queue wait
// still bounds the job). Advisory: a wrong estimate is a little too strict or loose, never a
// lost or doubled job.
func (q *dispatchQueue) tooDeep(node string, queued int64, wait time.Duration) bool {
	q.mu.Lock()
	svc := q.svcMs[node]
	q.mu.Unlock()
	if svc <= 0 {
		return false
	}
	pollers := q.pollersOf(node)
	if pollers <= 0 {
		return false
	}
	est := time.Duration(float64(queued) / float64(pollers) * svc * float64(time.Millisecond))
	return est > wait
}

// pollersOf estimates the node's pollers fleet-wide: the recent peak of idle pollers each
// instance has held (at rest a node's pollers all sit idle). Advisory, for admission only.
func (q *dispatchQueue) pollersOf(node string) int {
	total := 0
	for inst, r := range q.readRoute(node) {
		if inst != q.self {
			total += r.peak
		}
	}
	q.mu.Lock()
	if p := q.peak[node]; p != nil {
		total += max(p.cur, p.prev)
	}
	q.mu.Unlock()
	return total
}

func (q *dispatchQueue) publishRoute(node string) {
	if q.stopped() {
		return // shutting down: the entry ages out (dqRouteFresh)
	}
	q.routeMu.Lock()
	defer q.routeMu.Unlock()
	q.mu.Lock()
	idle := len(q.idle[node])
	peak := 0
	if p := q.peak[node]; p != nil {
		peak = max(p.cur, p.prev)
	}
	q.mu.Unlock()
	ctx, cancel := opCtx()
	defer cancel()
	key := dqRoutePrefix + node
	val := strconv.Itoa(idle) + ":" + strconv.Itoa(peak) + ":" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	if _, err := q.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, key, q.self, val)
		p.Expire(ctx, key, dqRouteTTL)
		return nil
	}); err != nil {
		q.noteErr("dq route write", err)
	}
}

// ---- the poll side ------------------------------------------------------------------------

func (q *dispatchQueue) newWaiter(node string) *pollWaiter {
	return &pollWaiter{node: node, wake: make(chan *dqEntry, 1)}
}

// advertise puts the poll in the idle set and publishes it. The caller pops the list again
// AFTER this returns: that order is what makes a lost wake-up impossible.
func (q *dispatchQueue) advertise(pw *pollWaiter) {
	q.mu.Lock()
	pw.advertised = true
	q.idle[pw.node] = append(q.idle[pw.node], pw)
	n := len(q.idle[pw.node])
	p := q.peak[pw.node]
	now := time.Now()
	if p == nil {
		p = &peakIdle{since: now}
		q.peak[pw.node] = p
	}
	if now.Sub(p.since) > dqPeakWindow {
		p.prev, p.cur, p.since = p.cur, 0, now
	}
	p.cur = max(p.cur, n)
	q.mu.Unlock()
	q.publishRoute(pw.node)
}

// wakeIdle takes one idle local poller of the node out of the idle set and wakes it with e
// (nil = a nudge: pop the list). The send happens UNDER q.mu, the same lock retire drains
// under, so a wake-up can never land after its poll has gone.
func (q *dispatchQueue) wakeIdle(node string, e *dqEntry) bool {
	q.mu.Lock()
	ws := q.idle[node]
	if len(ws) == 0 {
		q.mu.Unlock()
		return false
	}
	pw := ws[len(ws)-1]
	q.idle[node] = ws[:len(ws)-1]
	pw.advertised = false
	pw.wake <- e // buffered 1 and empty: only an idle poll is ever woken
	q.mu.Unlock()
	go q.publishRoute(node)
	return true
}

// retire removes a finished poll. A job handed to it in memory after it stopped listening is
// put back in the list (it was never written to the node, so this cannot double-serve).
func (q *dispatchQueue) retire(pw *pollWaiter) {
	var pending *dqEntry
	q.mu.Lock()
	was := pw.advertised
	if was {
		ws := q.idle[pw.node]
		for i, w := range ws {
			if w == pw {
				q.idle[pw.node] = append(ws[:i:i], ws[i+1:]...)
				break
			}
		}
		pw.advertised = false
	}
	select {
	case pending = <-pw.wake:
	default:
	}
	q.mu.Unlock()
	if pending != nil {
		q.requeue(pw.node, pending)
	}
	if was {
		q.publishRoute(pw.node)
	}
}

func (q *dispatchQueue) pop(node string) *dqEntry {
	if q.stopped() {
		return nil // shutting down: take nothing another instance could serve
	}
	ctx, cancel := opCtx()
	defer cancel()
	raw, err := q.rdb.LPop(ctx, dqListPrefix+node).Bytes()
	if err != nil {
		if err != redis.Nil {
			q.noteErr("dq pop", err)
		}
		return nil
	}
	var e dqEntry
	if json.Unmarshal(raw, &e) != nil {
		return nil
	}
	e.raw = raw
	return &e
}

// handOver writes a popped job to the node. It reports whether the poll is finished.
func (q *dispatchQueue) handOver(w http.ResponseWriter, r *http.Request, node string, e *dqEntry) bool {
	if time.Now().UnixMilli() > e.Deadline {
		return false // the relay gave up long ago: drop it and keep waiting
	}
	if r.Context().Err() != nil {
		q.requeue(node, e) // the node hung up before we wrote: nobody has it
		return true
	}
	if dqAfterPop != nil && dqAfterPop(q.self) {
		return true // TEST: the process died here; it can do nothing more
	}
	if q.stopped() {
		q.requeue(node, e) // shutting down: hand it back for a live instance
		return true
	}
	var job protocol.Job
	_ = json.Unmarshal(e.Job, &job)
	acked := r.Header.Get(ackHeader) == "1"
	if acked {
		w.Header().Set(ackHeader, "1") // this job wants the node's ack
	}
	if err := writeJob(w, job); err != nil {
		if acked { // the node dedupes by job id, so the origin may safely re-deliver it
			log.Printf("dq handoff write failed node=%s job=%s: %v (re-delivered: the node acks)", node, job.ID, err)
			_ = q.send(e.Origin, dqMsg{kind: "sent", job: job.ID, node: node})
			return true
		}
		log.Printf("dq handoff write failed node=%s job=%s: %v (the origin fails it fast)", node, job.ID, err)
		return true
	}
	q.b.stats.dqHandoff.Add(1)
	kind := "taken"
	if acked {
		kind = "sent" // this node confirms receipt itself: taken only on its ack
	}
	_ = q.send(e.Origin, dqMsg{kind: kind, job: job.ID, node: node})
	return true
}

// requeue puts a job that was never written to a node back at the FRONT of its list. A job
// handed over in memory had no list element; its ticket (on this instance) gets one now.
func (q *dispatchQueue) requeue(node string, e *dqEntry) {
	var job protocol.Job
	_ = json.Unmarshal(e.Job, &job)
	q.mu.Lock()
	tk := q.tickets[job.ID]
	q.mu.Unlock()
	if tk != nil {
		// Under tk.mu so close() (which reads elem under it, then LREMs) either sees the job
		// back in the list and withdraws it, or has already closed and we skip the push.
		tk.mu.Lock()
		defer tk.mu.Unlock()
		select {
		case <-tk.closed:
			return // the consumer is gone: nobody should serve it
		default:
		}
		tk.elem = e.raw
	}
	ctx, cancel := opCtx()
	defer cancel()
	if err := q.rdb.LPush(ctx, dqListPrefix+node, e.raw).Err(); err != nil {
		q.noteErr("dq requeue", err)
	}
}

func writeJob(w http.ResponseWriter, job protocol.Job) error {
	if err := json.NewEncoder(w).Encode(job); err != nil {
		return err
	}
	return http.NewResponseController(w).Flush()
}

// ---- results and stream chunks (any instance) --------------------------------------------

// originOf reports the instance holding job's consumer ("" = not a queue-dispatched job).
func (q *dispatchQueue) originOf(jobID string) (string, error) {
	ctx, cancel := opCtx()
	defer cancel()
	o, err := q.rdb.Get(ctx, dqOriginPrefix+jobID).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		q.noteErr("dq origin read", err)
	}
	return o, err
}

// ackHeader is sent by nodes that POST /agent/ack for every job they receive.
const ackHeader = "X-Roger-Ack"

// agentAck handles POST /agent/ack?node=<id>&job=<id>: the node confirms it received a job.
// Authenticated like a poll. It marks the job taken at its origin; an ack for a job this
// node was not sent, or for an unknown job, changes nothing. Single-instance: accepted, no-op.
func (b *broker) agentAck(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	node, jobID := r.URL.Query().Get("node"), r.URL.Query().Get("job")
	t, tok := b.tunnelFor(node)
	if t == nil {
		jsonErr(w, http.StatusNotFound, "unknown node")
		return
	}
	if !authNode(r, tok) {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if q := b.dqueue(); q != nil && b.multiInstance && jobID != "" {
		origin, err := q.originOf(jobID)
		if err == nil && origin != "" {
			err = q.send(origin, dqMsg{kind: "ack", job: jobID, node: node})
		}
		if err != nil {
			jsonErr(w, http.StatusServiceUnavailable, "ack not delivered") // the node retries it
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// atomicDuration is a tunable read by background goroutines and shortened by scenarios.
type atomicDuration struct{ v atomic.Int64 }

func newAtomicDuration(d time.Duration) *atomicDuration {
	a := &atomicDuration{}
	a.v.Store(int64(d))
	return a
}

func (a *atomicDuration) get() time.Duration  { return time.Duration(a.v.Load()) }
func (a *atomicDuration) set(d time.Duration) { a.v.Store(int64(d)) }
