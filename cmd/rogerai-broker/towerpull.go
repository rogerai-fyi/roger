package main

// towerpull.go: a Tower pulls its work over the link it already holds
// (features/tower/tower_pull_dispatch.feature, founder approved 2026-11-14).
//
// Core already sits on every Tower-carried request: it seals the request to the Station and hands
// it to the Tower's hub. Before this file it did that by dialing the hub, which is the only reason
// the Core-to-Tower leg needed an address Core could reach. Now, for a Tower whose session carries
// link.CapPull, the hand-over is an HTTP round trip carried the other way: pullTransport turns the
// request Core would have sent the hub into a queued piece of work, the Tower's held pull takes it,
// runs it against its own hub, and posts the hub's answer back. Because the transport sits under an
// ordinary *http.Client, the sealed loop (driveSealedF) and the hub check (towerhub.CheckHub) run
// unchanged; only who opens the connection changes.
//
// Delivery reuses the cancel poll's shape: a per-Tower buffer plus a wake-up channel in the shared
// store, so a pull held on one instance receives work queued on the other. Exactly one party wins
// each piece of work's claim: the Tower that took it, or the sender withdrawing it at the pickup
// deadline. The answer rides the result bus to whichever instance is waiting.

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"rogerai.fm/roger/v7/internal/towerhub"
)

// Founder-accepted numbers (tower_pull_dispatch.feature header). Variables so a test can shrink
// them; production never reassigns them.
var (
	// pullPickupDeadline: work no pull has taken by then is withdrawn and the request fails over.
	pullPickupDeadline = 3 * time.Second
	// pullHold: how long an empty pull is held.
	pullHold = 25 * time.Second
)

// pullMaxHeld is the most pulls one Tower may hold open at once, across every instance.
const pullMaxHeld = 4

// maxPullRequest bounds the request body carried to a Tower: the hub's own submit cap.
const maxPullRequest = 16 << 20

// maxPullAnswer bounds an answer a Tower posts: the hub client's response cap (32 MiB), as
// base64 inside JSON, plus room for the envelope fields.
const maxPullAnswer = 32<<20*4/3 + 64<<10

// pullAnswerRead bounds how long an answer's upload may take: 42 MiB at about 2 Mbit/s.
const pullAnswerRead = 3 * time.Minute

// pullClaimTTL keeps a claim past the longest a piece of work can be in flight, so a late answer
// is still recognised as belonging to the Tower that took it.
const pullClaimTTL = 10 * time.Minute

// pullWithdrawn is the claim value of work its sender withdrew at the pickup deadline.
const pullWithdrawn = "!withdrawn"

// errPullNotTaken is work no pull took before the pickup deadline.
var errPullNotTaken = errors.New("the tower did not pull this work in time")

// errPullUnavailable is Core's own shared store failing mid-hand-over: Core's fault, never a
// strike against the Tower (driveSealedF treats it as a design skip).
var errPullUnavailable = errors.New("work delivery is unavailable on Core's side")

// pullWork and pullAnswer are the shared wire shapes (internal/towerhub/pull.go).
type (
	pullWork   = towerhub.PullWork
	pullAnswer = towerhub.PullAnswer
)

// pullBatchMax and pullBatchBytes bound one pull's answer: whatever does not fit goes back on
// the buffer, unclaimed, for the Tower's next pull.
const (
	pullBatchMax   = 8
	pullBatchBytes = 24 << 20
)

// pullLocal is the single-instance (no shared store) fallback, as cancelLocal is for cancels.
type pullLocal struct {
	mu      sync.Mutex
	buf     map[string][]pullWork      // tower -> queued work
	wake    map[string]chan struct{}   // tower -> closed when work is queued
	claim   map[string]string          // work id -> tower, or pullWithdrawn
	claimAt map[string]time.Time       // when each claim was made, for pruning
	answers map[string]chan pullAnswer // work id -> the waiting sender
	held    map[string]int             // tower -> pulls held
}

func (p *pullLocal) init() {
	if p.buf == nil {
		p.buf, p.wake, p.claim = map[string][]pullWork{}, map[string]chan struct{}{}, map[string]string{}
		p.answers, p.held, p.claimAt = map[string]chan pullAnswer{}, map[string]int{}, map[string]time.Time{}
	}
}

func (p *pullLocal) wakeCh(tower string) chan struct{} {
	ch, ok := p.wake[tower]
	if !ok {
		ch = make(chan struct{})
		p.wake[tower] = ch
	}
	return ch
}

// pullClaim records who wins work id: the first claim stands. It reports whether this one won.
func (b *broker) pullClaim(id, who string) (bool, error) {
	if b.shared != nil {
		return b.shared.setIfAbsent("tpull:"+id, who, pullClaimTTL)
	}
	b.pulls.mu.Lock()
	defer b.pulls.mu.Unlock()
	b.pulls.init()
	if _, taken := b.pulls.claim[id]; taken {
		return false, nil
	}
	b.pulls.pruneLocked(time.Now())
	b.pulls.claim[id], b.pulls.claimAt[id] = who, time.Now()
	return true, nil
}

// pruneLocked drops claims older than pullClaimTTL, as the shared store's TTL does: long past
// any work's deadline and any answer's drive, so nothing that could still arrive needs them.
func (p *pullLocal) pruneLocked(now time.Time) {
	for id, at := range p.claimAt {
		if now.Sub(at) > pullClaimTTL {
			delete(p.claim, id)
			delete(p.claimAt, id)
		}
	}
}

// pullClaimant is who won work id's claim ("" when nobody has).
func (b *broker) pullClaimant(id string) (string, error) {
	if b.shared != nil {
		who, _, err := b.shared.stringGet("tpull:" + id)
		return who, err
	}
	b.pulls.mu.Lock()
	defer b.pulls.mu.Unlock()
	b.pulls.init()
	return b.pulls.claim[id], nil
}

// pullAnsweredOnce reports whether this is the first answer posted for work id.
func (b *broker) pullAnsweredOnce(id string) (bool, error) {
	if b.shared != nil {
		return b.shared.setIfAbsent("tpullans:"+id, "1", pullClaimTTL)
	}
	b.pulls.mu.Lock()
	defer b.pulls.mu.Unlock()
	b.pulls.init()
	if b.pulls.claim["ans:"+id] != "" {
		return false, nil
	}
	b.pulls.claim["ans:"+id], b.pulls.claimAt["ans:"+id] = "1", time.Now()
	return true, nil
}

// pullForgetAnswer undoes pullAnsweredOnce for an answer that could not be delivered.
func (b *broker) pullForgetAnswer(id string) {
	if b.shared != nil {
		_ = b.shared.delIfEqual("tpullans:"+id, "1")
		return
	}
	b.pulls.mu.Lock()
	delete(b.pulls.claim, "ans:"+id)
	delete(b.pulls.claimAt, "ans:"+id)
	b.pulls.mu.Unlock()
}

// pullTransport is an http.RoundTripper that carries a request to a Tower's hub through the
// Tower's own pull instead of a dial.
type pullTransport struct {
	b     *broker
	tower string
}

// pullClient is the hub client for a Tower that pulls: base URL and *http.Client to hand to
// towerhub.Client or towerhub.CheckHub in place of what towerhub.ReachVetted returns.
func (b *broker) pullClient(towerID string) (string, *http.Client) {
	return "http://pulled.tower.invalid", &http.Client{Transport: pullTransport{b: b, tower: towerID}}
}

func (t pullTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	b := t.b
	var body []byte
	if req.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(req.Body, maxPullRequest+1))
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		if len(raw) > maxPullRequest {
			return nil, fmt.Errorf("a request for tower %s is over the hub's submit cap", t.tower)
		}
		body = raw
	}
	deadline := time.Now().Add(pullPickupDeadline)
	work := pullWork{ID: "pw-" + randHex(16), Method: req.Method, Path: req.URL.Path, Query: req.URL.RawQuery,
		Header: towerhub.PickHeaders(req.Header), Body: body, Exp: deadline.UnixMilli()}

	ctx := req.Context()
	answers, stop, err := b.pullAwait(ctx, work.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errPullUnavailable, err)
	}
	defer stop()
	if err := b.pullQueue(t.tower, work); err != nil {
		return nil, fmt.Errorf("%w: %v", errPullUnavailable, err)
	}
	pickup := time.NewTimer(time.Until(deadline))
	defer pickup.Stop()
	for {
		select {
		case a, ok := <-answers:
			if !ok {
				return nil, errors.New("the answer channel closed")
			}
			if a.TowerID != t.tower || a.ID != work.ID {
				continue // not this Tower's answer to this work: never trusted
			}
			if a.Status == 0 {
				return nil, errPullNotTaken // handed over to a pull that hung up, too late to retake
			}
			return &http.Response{StatusCode: a.Status, Header: towerhub.HeaderOf(a.Header), Request: req,
				Body: io.NopCloser(bytes.NewReader(a.Body)), ContentLength: int64(len(a.Body))}, nil
		case <-pickup.C:
			won, cerr := b.pullClaim(work.ID, pullWithdrawn)
			if cerr != nil {
				return nil, fmt.Errorf("%w: %v", errPullUnavailable, cerr)
			}
			if won {
				return nil, errPullNotTaken
			}
			// The Tower took it in time: wait for its answer until the caller gives up.
		case <-ctx.Done():
			// Withdrawn if still untaken, so a pull after the caller left does not run it.
			_, _ = b.pullClaim(work.ID, pullWithdrawn)
			return nil, ctx.Err()
		}
	}
}

// pullAwait subscribes the sender to work id's answer BEFORE the work is queued, so an answer
// cannot outrun the subscription.
func (b *broker) pullAwait(ctx context.Context, id string) (<-chan pullAnswer, func(), error) {
	if b.shared == nil {
		ch := make(chan pullAnswer, 1)
		b.pulls.mu.Lock()
		b.pulls.init()
		b.pulls.answers[id] = ch
		b.pulls.mu.Unlock()
		return ch, func() {
			b.pulls.mu.Lock()
			delete(b.pulls.answers, id)
			b.pulls.mu.Unlock()
		}, nil
	}
	sctx, cancel := context.WithCancel(ctx)
	raws, unsub, err := b.shared.busSubscribeResult(sctx, id)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	out := make(chan pullAnswer, 1)
	go func() {
		defer close(out)
		// The notice is a wake-up, not the delivery: one lost while the subscriber reconnects, or
		// read while the store failed, would strand the parked answer. So the key is also looked
		// at on a ticker until the sender stops waiting.
		tick := time.NewTicker(pullAnswerPoll)
		defer tick.Stop()
		for {
			select {
			case _, ok := <-raws:
				if !ok {
					raws = nil // the subscription ended: the ticker still finds the answer
				}
			case <-tick.C:
			case <-sctx.Done():
				return
			}
			// The look is cheap and on the hot path; the read, possibly megabytes, is on the bulk
			// pool and only once there is something to read.
			if there, eerr := b.shared.cacheExists(pullAnswerKey(id)); eerr != nil || !there {
				continue
			}
			raw, found, gerr := b.shared.bulkGet(pullAnswerKey(id))
			if gerr != nil || !found {
				continue
			}
			_ = b.shared.cacheDel(pullAnswerKey(id))
			var a pullAnswer
			if json.Unmarshal(raw, &a) != nil {
				continue
			}
			select {
			case out <- a:
			case <-sctx.Done():
				return
			}
		}
	}()
	return out, func() { unsub(); cancel() }, nil
}

// pullAnswerTTL bounds how long a parked answer waits for the instance that asked for it.
const pullAnswerTTL = 2 * time.Minute

// pullAnswerPoll is how often a waiting sender looks for its parked answer without a notice.
var pullAnswerPoll = time.Second

func pullAnswerKey(id string) string { return "tpullans-body:" + id }

// pullQueue puts one piece of work on the Tower's buffer and wakes its held pulls.
func (b *broker) pullQueue(tower string, w pullWork) error {
	if b.shared != nil {
		raw, err := json.Marshal(w)
		if err != nil {
			return err
		}
		return b.shared.towerWorkPush(tower, raw, pullPickupDeadline+time.Minute)
	}
	b.pulls.mu.Lock()
	defer b.pulls.mu.Unlock()
	b.pulls.init()
	// Expired work is dropped as new work arrives, so a Tower that never pulls cannot pile it up.
	now := time.Now().UnixMilli()
	live := b.pulls.buf[tower][:0]
	for _, q := range b.pulls.buf[tower] {
		if q.Exp > now {
			live = append(live, q)
		}
	}
	b.pulls.buf[tower] = append(live, w)
	close(b.pulls.wakeCh(tower))
	delete(b.pulls.wake, tower)
	return nil
}

// pullTake drains the Tower's buffer and claims every live piece of work for it. Work past its
// pickup deadline, or already claimed (withdrawn), is dropped: it is never delivered late.
// storeFailed says Core's store failed on the way, so the caller pauses before taking again.
func (b *broker) pullTake(tower string) (work []pullWork, storeFailed bool) {
	var queued []pullWork
	if b.shared != nil {
		raws, err := b.shared.towerWorkDrain(tower)
		if err != nil {
			return nil, true
		}
		for _, raw := range raws {
			var w pullWork
			if json.Unmarshal(raw, &w) == nil {
				queued = append(queued, w)
			}
		}
	} else {
		b.pulls.mu.Lock()
		b.pulls.init()
		queued = b.pulls.buf[tower]
		delete(b.pulls.buf, tower)
		b.pulls.mu.Unlock()
	}
	now := time.Now().UnixMilli()
	var out, rest []pullWork
	size := 0
	for _, w := range queued {
		if w.Exp <= now {
			// Never delivered late. If nobody holds it (its sender has not withdrawn it, as when
			// work given back near its deadline lost the race to the sender's check), the sender
			// is told now so it fails over instead of waiting out its drive.
			if won, err := b.pullClaim(w.ID, pullWithdrawn); err == nil && won {
				_ = b.deliverAnswer(pullAnswer{TowerID: tower, ID: w.ID})
			}
			continue
		}
		if len(out) >= pullBatchMax || (len(out) > 0 && size+len(w.Body) > pullBatchBytes) {
			rest = append(rest, w)
			continue
		}
		won, err := b.pullClaim(w.ID, tower)
		if err != nil {
			// The store may have written the claim and lost the reply: ask who holds it.
			who, werr := b.pullClaimant(w.ID)
			won = werr == nil && who == tower
			if werr != nil || who == "" {
				storeFailed = true
				rest = append(rest, w) // Core's store failed, not the Tower: keep it for the next pull
				continue
			}
		}
		if won {
			out = append(out, w)
			size += len(w.Body)
		}
	}
	for _, w := range rest {
		_ = b.pullQueue(tower, w) // unclaimed: the next pull takes it, within its pickup deadline
	}
	return out, storeFailed
}

// pullStoreRetry is how long a held pull waits before taking again after Core's store failed,
// so work requeued by that failure does not spin the pull against the store.
var pullStoreRetry = 250 * time.Millisecond

// pullUntake releases this Tower's claim on work it never received and queues it again.
//
// Work already past its pickup deadline cannot be taken again, and its sender lost the withdraw
// race to this Tower's claim: it is told the work was not taken (Status 0), so it fails over now
// rather than waiting out its whole drive.
func (b *broker) pullUntake(tower string, work []pullWork) {
	now := time.Now().UnixMilli()
	for _, w := range work {
		if w.Exp <= now {
			b.deliverAnswer(pullAnswer{TowerID: tower, ID: w.ID})
			continue
		}
		if b.shared != nil {
			if err := b.shared.delIfEqual("tpull:"+w.ID, tower); err != nil {
				continue // the store did not answer: leave it claimed, its sender's drive ends it
			}
		} else {
			b.pulls.mu.Lock()
			b.pulls.init()
			if b.pulls.claim[w.ID] == tower {
				delete(b.pulls.claim, w.ID)
				delete(b.pulls.claimAt, w.ID)
			}
			b.pulls.mu.Unlock()
		}
		_ = b.pullQueue(tower, w)
	}
}

// pullHoldSlot reserves one of the Tower's held-pull slots; release frees it. ok is false at the
// cap; err is Core's own store failing, which is not the Tower's cap.
func (b *broker) pullHoldSlot(tower string) (release func(), ok bool, err error) {
	if b.shared != nil {
		token := randHex(8)
		now := time.Now()
		won, err := b.shared.edgeSlotReserve("towerpull:"+tower, token, now.Add(pullHold+10*time.Second), now, pullMaxHeld)
		if err != nil || !won {
			return nil, false, err
		}
		return func() { _ = b.shared.edgeSlotDrop("towerpull:"+tower, token) }, true, nil
	}
	b.pulls.mu.Lock()
	defer b.pulls.mu.Unlock()
	b.pulls.init()
	if b.pulls.held[tower] >= pullMaxHeld {
		return nil, false, nil
	}
	b.pulls.held[tower]++
	return func() {
		b.pulls.mu.Lock()
		b.pulls.held[tower]--
		b.pulls.mu.Unlock()
	}, true, nil
}

// towerWorkPull is POST /tower/work/pull: the Tower's held request for work. Signed by the Tower,
// bound to its current session. 200 {"work":[...]} or 204 after the hold; 429 at the held cap.
func (b *broker) towerWorkPull(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	body := readTowerBody(r)
	var req struct {
		TowerID   string `json:"tower_id"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	tw, _, ok := b.towerCaller(r, body, req.TowerID)
	if !ok {
		jsonErr(w, http.StatusForbidden, "pulling work requires a registered Tower's own signed request")
		return
	}
	// The pull is bound to the Tower's live session (a check, not a heartbeat: no shared write).
	if err := ts.link.Bound(req.SessionID, tw.ID); err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	b.noteLinkAddr(tw.ID, clientIP(r))
	release, ok, err := b.pullHoldSlot(tw.ID)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "work delivery unavailable")
		return
	}
	if !ok {
		w.Header().Set("Retry-After", "1")
		jsonErr(w, http.StatusTooManyRequests, fmt.Sprintf("a tower may hold at most %d pulls at once", pullMaxHeld))
		return
	}
	defer release()

	answer := func(work []pullWork) {
		if len(work) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// The Tower hung up as the work was handed over - before the write, or during it: give it
		// back, unclaimed, so the next pull takes it within its deadline instead of the request
		// waiting out its drive.
		raw, _ := json.Marshal(map[string]any{"work": work})
		if r.Context().Err() != nil {
			b.pullUntake(tw.ID, work)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, werr := w.Write(raw); werr != nil {
			b.pullUntake(tw.ID, work)
		}
	}
	// A Tower revoked or suspended while its pull is held gets nothing more: checked before
	// anything is handed over.
	stillHere := func() bool {
		_, why := b.towerEntitled(tw.ID)
		return why == ""
	}
	hold := time.NewTimer(pullHold)
	defer hold.Stop()
	if b.shared == nil {
		for {
			if !stillHere() {
				answer(nil)
				return
			}
			// The wake channel is taken BEFORE the drain, so work queued between the two closes
			// the channel this pull is about to wait on rather than one nobody holds.
			b.pulls.mu.Lock()
			b.pulls.init()
			ch := b.pulls.wakeCh(tw.ID)
			b.pulls.mu.Unlock()
			if work, _ := b.pullTake(tw.ID); len(work) > 0 {
				answer(work)
				return
			}
			select {
			case <-ch:
			case <-hold.C:
				answer(nil)
				return
			case <-r.Context().Done():
				return
			}
		}
	}
	// Shared: subscribe first, then drain, so work queued in between is either buffered (no
	// subscriber yet) or published to us - never lost.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	wakes, unsub, err := b.shared.towerWorkSubscribe(ctx, tw.ID)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "work delivery unavailable")
		return
	}
	defer unsub()
	for {
		if !stillHere() {
			answer(nil)
			return
		}
		work, storeFailed := b.pullTake(tw.ID)
		if len(work) > 0 {
			answer(work)
			return
		}
		if storeFailed {
			select {
			case <-time.After(pullStoreRetry):
				continue // the wake our own requeue published is taken by this pass, not a spin
			case <-hold.C:
				answer(nil)
				return
			case <-r.Context().Done():
				return
			}
		}
		select {
		case _, ok := <-wakes:
			if !ok {
				answer(nil)
				return
			}
		case <-hold.C:
			answer(nil)
			return
		case <-r.Context().Done():
			return
		}
	}
}

// deliverAnswer hands an answer to whichever instance is waiting for it. Over the shared store
// the answer is PARKED under a key and only a notice is published: an answer can be tens of
// megabytes and a Valkey subscriber is cut off past its output-buffer limit (32 MB by default),
// which would lose exactly the largest answers.
func (b *broker) deliverAnswer(a pullAnswer) error {
	if b.shared != nil {
		raw, _ := json.Marshal(a)
		if err := b.shared.bulkSet(pullAnswerKey(a.ID), raw, pullAnswerTTL); err != nil {
			return err
		}
		_ = b.shared.busPublishResult(a.ID, []byte{1})
		return nil
	}
	b.pulls.mu.Lock()
	ch := b.pulls.answers[a.ID]
	b.pulls.mu.Unlock()
	if ch != nil {
		select {
		case ch <- a:
		default:
		}
	}
	return nil
}

// towerWorkAnswer is POST /tower/work/answer: the hub's answer to one piece of work the Tower
// pulled. Accepted once, only from the Tower that took the work, and delivered to whichever
// instance is waiting. An answer nobody is waiting for any more goes nowhere and bills nothing.
func (b *broker) towerWorkAnswer(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	// WHO BEFORE HOW MUCH: an answer may be ~42 MiB, so the caller is authenticated before a byte
	// of it is read. The Tower signs the body's digest (towerhub.AnswerDigestHeader) and names
	// itself and the work in the URL: its signature, its entitlement and its claim on that work
	// are all checked here, and the body is checked against the digest once read.
	q := r.URL.Query()
	towerID, workID := q.Get("tower_id"), q.Get("work")
	digest := r.Header.Get(towerhub.AnswerDigestHeader)
	tw, _, ok := b.towerCaller(r, []byte(digest), towerID)
	if !ok || len(digest) != 64 {
		jsonErr(w, http.StatusForbidden, "answering work requires a registered Tower's own signed request")
		return
	}
	if r.ContentLength > maxPullAnswer {
		jsonErr(w, http.StatusRequestEntityTooLarge, "that answer is over the hub's response cap")
		return
	}
	who, err := b.pullClaimant(workID)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "work delivery unavailable")
		return
	}
	if workID == "" || who != tw.ID {
		jsonErr(w, http.StatusForbidden, "this tower was not given that work")
		return
	}
	// A large answer from a home uplink needs longer than the server's default read bound; the
	// caller has just proven it is the Tower holding this work.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(pullAnswerRead))
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPullAnswer+1))
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "unreadable body")
		return
	}
	if len(body) > maxPullAnswer {
		jsonErr(w, http.StatusRequestEntityTooLarge, "that answer is over the hub's response cap")
		return
	}
	if subtle.ConstantTimeCompare([]byte(towerhub.AnswerDigest(body)), []byte(digest)) != 1 {
		jsonErr(w, http.StatusForbidden, "the answer does not match its signed digest")
		return
	}
	var a pullAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if a.TowerID != tw.ID || a.ID != workID {
		jsonErr(w, http.StatusForbidden, "answering work requires a registered Tower's own signed request")
		return
	}
	if err := ts.link.Bound(a.SessionID, tw.ID); err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	first, err := b.pullAnsweredOnce(a.ID)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "work delivery unavailable")
		return
	}
	if !first {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": "already received"})
		return
	}
	a.TowerID = tw.ID
	a.Header = towerhub.PickHeaders(towerhub.HeaderOf(a.Header))
	if a.Status < 100 || a.Status > 599 {
		a.Status = http.StatusBadGateway // a Tower's nonsense status is a failure, never a success
	}
	if err := b.deliverAnswer(a); err != nil {
		// Not delivered, so not received: the Tower's retry must be taken, not told "already".
		b.pullForgetAnswer(a.ID)
		jsonErr(w, http.StatusServiceUnavailable, "work delivery unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
