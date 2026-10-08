package main

// edge_slot_promote_test.go (audit fix 2026-10-04): a reservation that fails to become its
// attempt (edgeSlotPromote errors) must not leave the open attempt uncounted when the
// reservation lapses: the shared per-account cap may never under-count an open attempt.

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"rogerai.fm/roger/v6/internal/store"
)

// promoteFailOnce fails the first edgeSlotPromote and passes everything else through.
type promoteFailOnce struct {
	sharedStore
	fails atomic.Int32
}

func (p *promoteFailOnce) edgeSlotPromote(account, token, attemptID string, deadline time.Time) error {
	if p.fails.Add(1) == 1 {
		return errors.New("transient: connection reset")
	}
	return p.sharedStore.edgeSlotPromote(account, token, attemptID, deadline)
}

func TestFailedSlotPromotionStillCountsTheOpenAttempt(t *testing.T) {
	mr := miniredis.RunT(t)
	vs, err := newValkeyStore("redis://" + mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vs.Close() })
	b := relayBroker(store.NewMem())
	b.shared = &promoteFailOnce{sharedStore: vs}

	const acct = "u_gh_77"
	if ok, err := b.edgeAccountReserveErr(acct); err != nil || !ok {
		t.Fatalf("reserve ok=%v err=%v", ok, err)
	}
	deadline := time.Now().Add(10 * time.Minute) // an attempt that outlives the reservation TTL
	b.edgeEnterInflight("att-1", "node-1", acct, deadline)

	// Let any retry run, then let the reservation's own TTL pass.
	waitUntil := time.Now().Add(3 * time.Second)
	for time.Now().Before(waitUntil) {
		if s, _ := mr.ZScore(edgeSlotsPrefix+acct, "att-1"); s > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mr.FastForward(edgeReserveTTL + time.Second)

	members, _ := mr.ZMembers(edgeSlotsPrefix + acct)
	counted := false
	for _, m := range members {
		if m == "att-1" {
			counted = true
		}
	}
	if !counted {
		t.Fatalf("the open attempt is not counted in the shared cap after its reservation lapsed: members %v", members)
	}
}
