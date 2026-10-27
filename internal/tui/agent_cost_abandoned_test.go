package tui

import (
	"context"
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/harness"
)

// A COST REPORTED FOR A TURN THAT WAS ALREADY ABANDONED IS DROPPED, NOT QUEUED. The cost
// side-channel decides on the turn's cancellation BEFORE it offers the send, so a force-stopped
// turn whose relay still answers with a cost neither blocks on a buffer nobody drains nor
// lands a stale cost in the next turn's transcript.
//
// Until this test the branch ran only when a scenario's cleanup happened to cancel a live turn
// in the instant between the broker's answer arriving and the cost callback firing - three
// coverage runs of ten. Here the turn is abandoned FIRST: its context is cancelled and stored
// as the live one, and a single model call is then driven with a context of its own, exactly
// as the turn goroutine would drive it, so the relay answers (the cost header arrives) while
// the turn it belongs to is already gone. Nothing is timed.
func TestACostReportedAfterTheTurnWasAbandonedIsDropped(t *testing.T) {
	base := browseSeed(120)
	base.broker = brokerReplying(t, "a late answer").URL
	base.user = "tester"
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	nm, _ := base.enterAgent()
	rt := asModel(nm).agent
	if rt == nil {
		t.Fatal("entering AGENT on a tuned band should build a runtime")
	}

	abandoned, cancel := context.WithCancel(context.Background())
	cancel()
	rt.turnCtx.Store(abandoned)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = rt.loop.Send(context.Background(), "go", func(harness.Event) {})
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the model call for an abandoned turn never returned: the cost send blocked")
	}
	select {
	case ev := <-rt.events:
		t.Fatalf("a cost for an abandoned turn reached the events channel: %+v", ev)
	default:
	}
}
