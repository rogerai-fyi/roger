package station

// outbox_test.go covers the evidence queue between serving and settlement.
//
// Contract: features/tower/edge_dispatch.feature.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func timeUnix() time.Time      { return time.Unix(1_700_000_000, 0) }
func ctxTODO() context.Context { return context.Background() }

func ev(id string) Evidence {
	return Evidence{AttemptID: id, StationID: "st-1", Receipt: []byte("signed-" + id)}
}

// COLLECT IS NOT REMOVE. A Tower that crashes between collecting and forwarding must find
// the evidence still here - a receipt is money, and money does not ride at-most-once.
func TestCollectingLeavesTheEvidenceInPlace(t *testing.T) {
	o := NewOutbox(10)
	o.Add(ev("a"))
	o.Add(ev("b"))

	first := o.Collect(10)
	require.Len(t, first, 2)
	second := o.Collect(10)
	require.Len(t, second, 2, "collection must hand out copies, not drain the queue")
	require.Equal(t, first, second)
}

func TestOnlyAConfirmationRemoves(t *testing.T) {
	o := NewOutbox(10)
	o.Add(ev("a"))
	o.Add(ev("b"))
	o.Add(ev("c"))

	o.Settled([]string{"b"})
	left := o.Collect(10)
	require.Len(t, left, 2)
	require.Equal(t, "a", left[0].AttemptID)
	require.Equal(t, "c", left[1].AttemptID)

	// Confirming something absent, or nothing at all, is a no-op rather than a fault: the
	// courier retries, and a retry must be boring.
	o.Settled([]string{"nope"})
	o.Settled(nil)
	require.Len(t, o.Collect(10), 2)
}

func TestCollectHonoursItsCap(t *testing.T) {
	o := NewOutbox(10)
	for i := 0; i < 5; i++ {
		o.Add(ev(fmt.Sprintf("e%d", i)))
	}
	require.Len(t, o.Collect(3), 3)
	require.Len(t, o.Collect(0), 5, "no cap means everything")
}

// Bounded, dropping the OLDEST: an outbox that only grows is a leak, and the oldest entries
// are the ones closest to their settlement window closing anyway. The drop count is the
// operator's view of pay walking out the door.
func TestAFullOutboxDropsTheOldestAndCounts(t *testing.T) {
	o := NewOutbox(3)
	for i := 0; i < 5; i++ {
		o.Add(ev(fmt.Sprintf("e%d", i)))
	}
	got := o.Collect(10)
	require.Len(t, got, 3)
	require.Equal(t, "e2", got[0].AttemptID, "the oldest must be the ones dropped")
	pending, dropped := o.Stats()
	require.Equal(t, 3, pending)
	require.Equal(t, int64(2), dropped)
}

func TestEvidenceOfNothingIsRefused(t *testing.T) {
	o := NewOutbox(10)
	o.Add(Evidence{})
	o.Add(Evidence{AttemptID: "a"})
	o.Add(Evidence{Receipt: []byte("x")})
	require.Empty(t, o.Collect(10))
}

func TestTheDefaultBoundIsGenerousButFinite(t *testing.T) {
	o := NewOutbox(0)
	for i := 0; i < 2000; i++ {
		o.Add(ev(fmt.Sprintf("e%d", i)))
	}
	pending, dropped := o.Stats()
	require.Equal(t, 1024, pending)
	require.Equal(t, int64(976), dropped)
}

// The executor queues a copy of every receipt it signs, and no copy of any refusal.
func TestServingQueuesTheEvidenceAndRefusalsDoNot(t *testing.T) {
	e, _, grant := edgeSetup(t, timeUnix(), nil)
	o := NewOutbox(10)
	e.Outbox = o

	resp := e.Serve(ctxTODO(), EdgeRequest{Grant: grant, Body: []byte(`{"p":"x"}`)})
	require.Equal(t, 200, resp.Status)
	got := o.Collect(10)
	require.Len(t, got, 1)
	require.Equal(t, e.Station.StationID, got[0].StationID)
	require.NotEmpty(t, got[0].Receipt)

	// A refusal produces NO evidence: it is not a result and must never settle one.
	resp = e.Serve(ctxTODO(), EdgeRequest{Body: []byte("x")})
	require.NotEqual(t, 200, resp.Status)
	require.Len(t, o.Collect(10), 1)
}
