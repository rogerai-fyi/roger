package station

// outbox.go is how a Station's evidence gets home.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # THE PROBLEM
//
// On the edge path the receipt travels to the CONSUMER, inside a TLS session the Tower
// cannot read - that blindness is the whole point of the relay. But settlement happens at
// Roger Core, and a Station cannot reach Core: its only channel is its Tower. So the receipt
// needs a second copy that travels the other road: held here, collected by the Tower,
// forwarded to Core.
//
// # WHY LETTING THE TOWER CARRY IT IS SAFE
//
// The Tower cannot FORGE a receipt - it is signed with the assertion key the Tower has never
// held - and it cannot ALTER one for the same reason. All it can do is withhold, and a
// withheld receipt is an attempt that never settles, which costs exactly one party: the
// operator who would have been paid for it. The incentive points the right way without a
// single additional mechanism.
//
// # COLLECT IS NOT REMOVE
//
// Collection hands out copies; only a confirmation removes. A Tower that crashes between
// collecting and forwarding must find the evidence still here on its next pass - the receipt
// is money, and money does not ride an at-most-once protocol. Core's settlement is one-use,
// so a receipt forwarded twice loses the swap and nothing double-settles.
//
// # BOUNDED, DROPPING THE OLDEST
//
// An outbox that only grows is a memory leak with a deadline attached. When it overflows,
// the OLDEST entries go: they are the ones closest to their settlement window closing, so
// they are the ones a drop costs least. An overflow means the Tower is not collecting, and
// the count of drops is reported so an operator can see pay walking out the door.

import (
	"sync"
)

// Evidence is one receipt waiting to go home.
type Evidence struct {
	AttemptID string `json:"attempt_id"`
	StationID string `json:"station_id"`
	// Receipt is the canonical signed object, exactly as the consumer's copy.
	Receipt []byte `json:"receipt"`
}

// Outbox holds evidence until the Tower confirms Core has it.
type Outbox struct {
	mu      sync.Mutex
	pending []Evidence
	limit   int
	dropped int64
}

// NewOutbox builds an outbox holding at most limit entries.
func NewOutbox(limit int) *Outbox {
	if limit <= 0 {
		limit = 1024
	}
	return &Outbox{limit: limit}
}

// Add queues one receipt.
func (o *Outbox) Add(e Evidence) {
	if e.AttemptID == "" || len(e.Receipt) == 0 {
		// Evidence of nothing is not evidence. Refusing silently is fine here: the caller
		// just signed this receipt, so an empty one is a programming error a test catches.
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for len(o.pending) >= o.limit {
		o.pending = o.pending[1:]
		o.dropped++
	}
	o.pending = append(o.pending, e)
}

// Collect returns up to max pending entries WITHOUT removing them.
func (o *Outbox) Collect(max int) []Evidence {
	o.mu.Lock()
	defer o.mu.Unlock()
	if max <= 0 || max > len(o.pending) {
		max = len(o.pending)
	}
	out := make([]Evidence, max)
	copy(out, o.pending[:max])
	return out
}

// Settled removes entries whose attempts Core has answered for.
//
// "Answered" includes refused: a receipt Core has terminally rejected is not going to settle
// on a retry, and holding it forever would wedge the queue behind it.
func (o *Outbox) Settled(attemptIDs []string) {
	if len(attemptIDs) == 0 {
		return
	}
	done := make(map[string]bool, len(attemptIDs))
	for _, id := range attemptIDs {
		done[id] = true
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	kept := o.pending[:0]
	for _, e := range o.pending {
		if !done[e.AttemptID] {
			kept = append(kept, e)
		}
	}
	o.pending = kept
}

// Stats reports how the outbox is doing, for the operator's eyes.
func (o *Outbox) Stats() (pending int, dropped int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.pending), o.dropped
}
