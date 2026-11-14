package agent

// tower_move.go: the node's half of a move (features/tower/station_mobility.feature). Core asks
// a node to attach again by putting a nudge on its cancel long-poll; the nudge names no Tower,
// so the worst a forged one can do is cause one extra attach to Core over its pinned address.
// The node finishes what it is serving first, acts at most once a minute, and ignores a nudge
// it could not act on before it expired.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// errMoved ends a tenancy because Core asked this node to attach again.
var errMoved = errors.New("roger core asked this station to attach again")

// nudgeEvery is the most often a node acts on a nudge.
const nudgeEvery = time.Minute

// nudgeExpiry is how long a node waits to be idle before it gives up on a nudge: the broker
// keeps a nudge for the same two minutes.
var nudgeExpiry = 2 * time.Minute

var nudges sync.Map // node id -> chan string

// nudgeChan is the one-slot channel the cancel poll hands a node's nudges to and its relay
// loop listens on. One slot: a nudge waiting already says everything a second one would.
func nudgeChan(nodeID string) chan string {
	ch, _ := nudges.LoadOrStore(nodeID, make(chan string, 1))
	return ch.(chan string)
}

// deliverNudge hands a nudge to the relay loop without ever blocking the cancel poll.
func deliverNudge(nodeID, reason string) {
	select {
	case nudgeChan(nodeID) <- reason:
	default:
	}
}

// moveGate counts the relay work in flight and paces nudges.
type moveGate struct {
	inflight atomic.Int64
	mu       sync.Mutex
	last     time.Time
	now      func() time.Time
}

func (g *moveGate) begin() { g.inflight.Add(1) }
func (g *moveGate) end()   { g.inflight.Add(-1) }

func (g *moveGate) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// allow reports whether a nudge may be acted on now, and if so starts the minute.
func (g *moveGate) allow() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock()
	if !g.last.IsZero() && now.Sub(g.last) < nudgeEvery {
		return false
	}
	g.last = now
	return true
}

// waitIdle waits for the relay work in flight to finish, up to expiry.
func (g *moveGate) waitIdle(ctx context.Context, expiry time.Duration) bool {
	deadline := time.Now().Add(expiry)
	for g.inflight.Load() > 0 {
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(20 * time.Millisecond):
		}
	}
	return true
}

// movedError ends a tenancy for a move and carries the move's trigger.
type movedError struct{ reason string }

func (e movedError) Error() string { return errMoved.Error() + " (" + e.reason + ")" }
func (e movedError) Unwrap() error { return errMoved }

var moveWhy = map[string]string{
	"better_tower":  "a better relay was found for it",
	"tower_failing": "its relay was failing",
	"stage_cap":     "its relay was full for its trust stage",
	"tower_gone":    "its relay was gone",
}

// movedNotice is what the operator sees in `roger share` once a moved Station has attached
// again: where it was, where it is, and why, in plain words. Tower ids only, never the
// operators behind them.
func movedNotice(from, to, reason string) string {
	why, ok := moveWhy[reason]
	if !ok {
		why = "Roger Core asked it to attach again"
	}
	if from == to {
		return "tower: this Station attached again and stayed on relay " + to + " (" + why + ")"
	}
	return "tower: this Station moved from relay " + from + " to relay " + to + " (" + why + ")"
}

// gatedExec counts each served job on the gate, so a nudge waits for it.
type gatedExec struct {
	inner sealedExec
	gate  *moveGate
}

func (g gatedExec) Serve(ctx context.Context, grant, envelope []byte) ([]byte, []byte, string) {
	g.gate.begin()
	defer g.gate.end()
	return g.inner.Serve(ctx, grant, envelope)
}
