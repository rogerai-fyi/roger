package main

// rcinbox.go carries remote-control sessions across instances on the dispatch plane
// (features/multinode/rc_inbox.feature), with no store connection per viewer or per host poll.
//
//   rogerai:rc:{sid}:inq    LIST    viewer -> host inbounds; the host's poll LPOPs (exactly once,
//                                   in order). Expires rcInboundBufTTL after the last push.
//   rogerai:rc:{sid}:seq    STRING  the session's frame seq.
//   rogerai:rc:{sid}:ring   LIST    the last rcRingFrames frames as "<seq>|<json>", in seq order:
//                                   one script assigns the seq and appends, so the ring order IS
//                                   the seq order. Serves Last-Event-ID replay on any instance and
//                                   fills a gap when two frames arrive out of order.
//   rogerai:rc:{sid}:route  HASH    "h:<inst>" = a host poll waits there, "v:<inst>" = viewers
//                                   stream there (unix ms, refreshed while true).
//
// The {sid} hash tag keeps a session's keys on one shard, so the seq+ring script is legal on a
// ring or a cluster. Lost wake-ups are impossible by the same ordering as job dispatch: a
// sender pushes then reads the route; a host poll writes the route then pops.

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"rogerai.fm/roger/v6/internal/protocol"
)

const (
	rcRouteFresh   = 90 * time.Second // a viewer instance refreshes its entry every 30 s
	rcHostFresh    = 30 * time.Second // > the 25 s poll hold
	rcRouteRefresh = 30 * time.Second
	rcKeysTTL      = 7 * 24 * time.Hour // the seq and ring live as long as the old seq did
)

func rcKey(sid, part string) string { return keyPrefix + "rc:{" + sid + "}:" + part }

// rcFrameScript assigns the next seq and appends the frame to the ring atomically.
var rcFrameScript = redis.NewScript(`
local s = redis.call('INCR', KEYS[1])
redis.call('EXPIRE', KEYS[1], ARGV[3])
redis.call('RPUSH', KEYS[2], s .. '|' .. ARGV[1])
redis.call('LTRIM', KEYS[2], -tonumber(ARGV[2]), -1)
redis.call('EXPIRE', KEYS[2], ARGV[3])
return s
`)

// rcInbox returns the dispatch plane when remote control rides it: multi-instance, a queue
// rollout mode, and a live plane. nil = the legacy bus (or single-instance) path.
func (b *broker) rcInbox() *dispatchQueue {
	if !b.rcMultiInstance() || b.dispatchMode == dispatchViaBus {
		return nil
	}
	return b.dqueue()
}

// ---- outbound: host frames -> viewers ------------------------------------------------------

// rcFanOutInbox assigns the frame's seq (and appends it to the ring), then sends it once to
// each instance that holds viewers of the session.
func (b *broker) rcFanOutInbox(q *dispatchQueue, sid string, f protocol.RCFrame) {
	if f.TS == 0 {
		f.TS = time.Now().Unix()
	}
	f.Seq = 0
	body, _ := json.Marshal(f)
	ctx, cancel := opCtx()
	defer cancel()
	seq, err := rcFrameScript.Run(ctx, q.rdb, []string{rcKey(sid, "seq"), rcKey(sid, "ring")},
		string(body), rcRingFrames, int64(rcKeysTTL/time.Second)).Int64()
	if err != nil {
		q.noteErr("rc frame", err)
		return
	}
	f.Seq = uint64(seq)
	raw, _ := json.Marshal(f)
	for _, inst := range b.rcRoute(q, sid, "v:", rcRouteFresh) {
		_ = q.send(inst, dqMsg{kind: "rcf", job: sid, data: raw})
	}
}

// rcRoute lists the instances with a fresh route entry of the given prefix ("v:" or "h:").
func (b *broker) rcRoute(q *dispatchQueue, sid, prefix string, fresh time.Duration) []string {
	ctx, cancel := opCtx()
	defer cancel()
	all, err := q.rdb.HGetAll(ctx, rcKey(sid, "route")).Result()
	if err != nil {
		q.noteErr("rc route", err)
		return nil
	}
	var out []string
	now := time.Now().UnixMilli()
	for field, v := range all {
		inst, ok := strings.CutPrefix(field, prefix)
		ms, _ := strconv.ParseInt(v, 10, 64)
		if ok && now-ms <= fresh.Milliseconds() {
			out = append(out, inst)
		}
	}
	return out
}

func (b *broker) rcSetRoute(q *dispatchQueue, sid, field string, on bool) {
	if q.stopped() {
		return
	}
	ctx, cancel := opCtx()
	defer cancel()
	key := rcKey(sid, "route")
	var err error
	if on {
		_, err = q.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
			p.HSet(ctx, key, field, time.Now().UnixMilli())
			p.Expire(ctx, key, 5*time.Minute)
			return nil
		})
	} else {
		err = q.rdb.HDel(ctx, key, field).Err()
	}
	if err != nil {
		q.noteErr("rc route write", err)
	}
}

// rcRingSince returns the ring's frames with lo < seq <= hi, in seq order.
func rcRingSince(q *dispatchQueue, sid string, lo, hi uint64) []protocol.RCFrame {
	ctx, cancel := opCtx()
	defer cancel()
	items, err := q.rdb.LRange(ctx, rcKey(sid, "ring"), 0, -1).Result()
	if err != nil {
		q.noteErr("rc ring", err)
		return nil
	}
	var out []protocol.RCFrame
	for _, it := range items {
		seqStr, js, ok := strings.Cut(it, "|")
		seq, perr := strconv.ParseUint(seqStr, 10, 64)
		if !ok || perr != nil || seq <= lo || seq > hi {
			continue
		}
		var f protocol.RCFrame
		if json.Unmarshal([]byte(js), &f) == nil {
			f.Seq = seq
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// rcRemote is a hub's multi-instance state on this instance.
type rcRemote struct {
	once  sync.Once
	work  chan protocol.RCFrame // frames for this instance's viewers, in arrival order
	vlast map[string]uint64     // viewerID -> last seq delivered (guarded by the hub's mu)
	hosts []chan struct{}       // idle host polls here (guarded by the hub's mu)
}

// rcFrameArrived queues a frame for this instance's viewers. It never blocks the inbox reader:
// a frame dropped on a full queue is recovered from the ring by the next frame's gap fill.
func (b *broker) rcFrameArrived(q *dispatchQueue, sid string, raw []byte) {
	var f protocol.RCFrame
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	b.stats.rcFrames.Add(1)
	h := b.rcHubFor(sid)
	r := h.remote(b, q, sid)
	select {
	case r.work <- f:
	default:
	}
}

// remote returns the hub's multi-instance state, starting its delivery worker on first use.
func (h *rcHub) remote(b *broker, q *dispatchQueue, sid string) *rcRemote {
	h.mu.Lock()
	if h.rem == nil {
		h.rem = &rcRemote{work: make(chan protocol.RCFrame, 1024), vlast: map[string]uint64{}}
	}
	r := h.rem
	h.mu.Unlock()
	r.once.Do(func() { go h.deliverLoop(b, q, sid, r) })
	return r
}

// deliverLoop delivers frames to local viewers in seq order, exactly once each, filling any
// gap from the ring, and keeps this instance's viewer route entry fresh while it has viewers.
func (h *rcHub) deliverLoop(b *broker, q *dispatchQueue, sid string, r *rcRemote) {
	refresh := time.NewTicker(rcRouteRefresh)
	defer refresh.Stop()
	for {
		select {
		case <-q.stopCh:
			return
		case <-refresh.C:
			b.rcMu.Lock()
			dropped := b.rcHubs[sid] != h
			b.rcMu.Unlock()
			if dropped && len(r.work) == 0 {
				return // the session ended and its last frames are delivered
			}
			h.mu.Lock()
			n := len(h.viewers)
			h.mu.Unlock()
			if n > 0 {
				b.rcSetRoute(q, sid, "v:"+q.self, true)
			}
		case f := <-r.work:
			h.mu.Lock()
			behind := f.Seq
			for id := range h.viewers {
				if l := r.vlast[id]; l < behind {
					behind = l
				}
			}
			h.mu.Unlock()
			frames := []protocol.RCFrame{f}
			if behind+1 < f.Seq { // a viewer is missing frames before this one: fill from the ring
				if filled := rcRingSince(q, sid, behind, f.Seq); len(filled) > 0 {
					frames = filled
				}
			}
			h.mu.Lock()
			for _, fr := range frames {
				for id, ch := range h.viewers {
					if fr.Seq <= r.vlast[id] {
						continue // already delivered
					}
					r.vlast[id] = fr.Seq
					select {
					case ch <- fr:
					default: // a slow viewer drops the frame rather than stalling the session
					}
				}
			}
			h.mu.Unlock()
		}
	}
}

// rcStreamInbox is the viewer SSE loop on the inbox plane.
func (b *broker) rcStreamInbox(q *dispatchQueue, ctx context.Context, sid string, h *rcHub, viewerID string, since uint64, emit func(protocol.RCFrame) bool) {
	r := h.remote(b, q, sid)
	// Route FIRST, then read the seq: a frame assigned after the read is sent here; one assigned
	// before it counts as "before this viewer attached" (or is replayed for Last-Event-ID).
	b.rcSetRoute(q, sid, "v:"+q.self, true)
	cctx, cancel := opCtx()
	cur, err := q.rdb.Get(cctx, rcKey(sid, "seq")).Uint64()
	cancel()
	if err != nil && err != redis.Nil {
		q.noteErr("rc seq", err)
	}
	ch := make(chan protocol.RCFrame, 256)
	var replay []protocol.RCFrame
	if since > 0 && since < cur {
		replay = rcRingSince(q, sid, since, cur)
	}
	h.mu.Lock()
	h.viewers[viewerID] = ch
	r.vlast[viewerID] = cur
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.viewers, viewerID)
		delete(r.vlast, viewerID)
		empty := len(h.viewers) == 0
		h.mu.Unlock()
		if empty {
			b.rcSetRoute(q, sid, "v:"+q.self, false)
		}
	}()
	for _, f := range replay {
		if !emit(f) {
			return
		}
	}
	b.rcDeliverInbound(sid, h, protocol.RCInbound{Kind: protocol.RCInBackfill, Viewer: viewerID, TS: time.Now().Unix()})
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-ch:
			if !emit(f) {
				return
			}
		}
	}
}

// ---- inbound: viewer -> host ---------------------------------------------------------------

// rcSendInbox queues an inbound for the host and wakes the instance where its poll waits.
func (b *broker) rcSendInbox(q *dispatchQueue, sid string, in protocol.RCInbound) {
	raw, _ := json.Marshal(in)
	ctx, cancel := opCtx()
	key := rcKey(sid, "inq")
	_, err := q.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.RPush(ctx, key, raw)
		p.Expire(ctx, key, rcInboundBufTTL)
		return nil
	})
	cancel()
	if err != nil {
		q.noteErr("rc inbound", err)
		return
	}
	for _, inst := range b.rcRoute(q, sid, "h:", rcHostFresh) {
		_ = q.send(inst, dqMsg{kind: "rcin", job: sid})
	}
}

// rcHostWake wakes this instance's idle host polls of the session (they pop the queue).
func (b *broker) rcHostWake(sid string) {
	h := b.rcHubFor(sid)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rem == nil {
		return
	}
	for _, w := range h.rem.hosts {
		select {
		case w <- struct{}{}:
		default:
		}
	}
}

// rcPollInbox is the host long-poll on the inbox plane: pop, else advertise and pop again,
// then wait for a wake-up. Returns when it wrote an inbound, answered 204, or the host left.
func (b *broker) rcPollInbox(q *dispatchQueue, w http.ResponseWriter, r context.Context, sid string, h *rcHub) {
	rem := h.remote(b, q, sid)
	wake := make(chan struct{}, 1)
	advertised := false
	defer func() {
		if !advertised {
			return
		}
		h.mu.Lock()
		for i, c := range rem.hosts {
			if c == wake {
				rem.hosts = append(rem.hosts[:i:i], rem.hosts[i+1:]...)
				break
			}
		}
		none := len(rem.hosts) == 0
		h.mu.Unlock()
		if none {
			b.rcSetRoute(q, sid, "h:"+q.self, false)
		}
	}()
	hold := time.NewTimer(rcPollHold)
	defer hold.Stop()
	for {
		ctx, cancel := opCtx()
		raw, err := q.rdb.LPop(ctx, rcKey(sid, "inq")).Bytes()
		cancel()
		if err == nil {
			var in protocol.RCInbound
			if json.Unmarshal(raw, &in) == nil {
				_ = json.NewEncoder(w).Encode(in)
				return
			}
			continue
		}
		if err != redis.Nil {
			q.noteErr("rc poll", err)
		}
		if !advertised {
			h.mu.Lock()
			rem.hosts = append(rem.hosts, wake)
			h.mu.Unlock()
			b.rcSetRoute(q, sid, "h:"+q.self, true)
			advertised = true
			continue // pop once more AFTER advertising: an inbound pushed meanwhile is seen
		}
		select {
		case <-wake:
		case msg := <-h.in: // mixed-mode safety across a flag flip
			_ = json.NewEncoder(w).Encode(msg)
			return
		case <-hold.C:
			w.WriteHeader(http.StatusNoContent)
			return
		case <-r.Done():
			return
		}
	}
}
