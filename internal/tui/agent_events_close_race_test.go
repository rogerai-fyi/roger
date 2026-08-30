package tui

import (
	"runtime"
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/harness"
)

// TestDrainRetryTurnNeverSendsOnClosedChannel is the permanent guard for the founder's
// 2026-08-30 crash. It FAILED (crashing the test binary) before the fix:
//
//	panic: send on closed channel
//	  internal/tui.model.startAgentTurn.func1.1.1  agent.go:1728
//	  internal/harness.(*Loop).Send.func2           loop.go:400
//	  internal/harness.(*Loop).Send                 loop.go:440
//
// startAgentTurn's goroutine clears running BEFORE it closes events:
//
//	rt.running.Store(false)
//	close(rt.events)
//
// agentDrainRetryMsg (the 120ms parked-queue re-check that fires while a prompt sits
// on STANDBY) starts the next turn on running alone - it does NOT wait for
// agentDoneMsg. A turn launched between those two statements captures the OLD events
// channel, which the previous goroutine then closes underneath it.
//
// This drives that interleaving directly: it spins on running and starts the next turn
// the instant it clears, exactly as the drain timer does. It is deliberately a tight
// loop rather than a 120ms tick - the window was two instructions wide, and a timer
// would almost never land in it.
//
// The fix makes the crash structurally impossible: events is allocated once and never
// closed, end-of-turn travels on a per-turn done channel, and close(done) is published
// BEFORE the running guard drops, so there is no instant at which running reads false
// while the turn can still emit. Run with -race.
func TestDrainRetryTurnNeverSendsOnClosedChannel(t *testing.T) {
	srv := chatBroker(t, "answer")
	base := browseSeed(120)
	base.broker = srv.URL
	base.user = "tester"
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}

	for i := 0; i < 200; i++ {
		nm, _ := base.enterAgent()
		am := asModel(nm)
		am.agentBusy = true
		rt := am.agent

		am.startAgentTurn("first")()

		// The drain timer's job: the moment running clears, dequeue and start the
		// parked prompt. dequeueAgentPrompts does exactly this check.
		deadline := time.Now().Add(10 * time.Second)
		for rt.running.Load() {
			if time.Now().After(deadline) {
				t.Fatal("the first turn's goroutine never finished")
			}
			runtime.Gosched()
		}
		am.startAgentTurn("second (was on STANDBY)")()

		// Let the second turn reach its first emit.
		done := time.Now().Add(10 * time.Second)
		for rt.running.Load() && time.Now().Before(done) {
			runtime.Gosched()
		}
	}
}

// TestEveryEventKindKeepsTheDrainArmed pins the invariant behind the 835s freeze the
// cost-tick fix already records at tui.go:1932 ("a cost tick must NOT stop the stream"):
// onAgentEvent is re-issued from Update after EVERY event, so any case that returns
// without re-arming waitAgentEvent halts the single drain for the rest of the turn.
//
// EventNotice did exactly that. It is emitted MID-TURN and the turn continues - harness
// auto-compaction (loop.go:424, which then `continue`s), the "nothing to compact"
// dead-end (loop.go:440), and the recited-prompt trim (loop.go:460, which fires for any
// band that echoes its instructions. So a perfectly ordinary turn could strand the
// drain: the turn's remaining events pile up unread, agentDoneMsg is never observed,
// agentBusy never clears, and the turn looks hung forever.
//
// The founder's crash screenshot shows the "nothing to compact" notice immediately
// before the panic, which makes this the most likely way in: with the drain stopped, the
// pre-fix code never re-armed rt.events, the goroutine still closed it, and the next
// turn's cost callback sent on a closed channel.
func TestEveryEventKindKeepsTheDrainArmed(t *testing.T) {
	srv := chatBroker(t, "answer")
	base := browseSeed(120)
	base.broker = srv.URL
	base.user = "tester"
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	nm, _ := base.enterAgent()
	am := asModel(nm)
	am.agentBusy = true

	// Every kind the harness can stream mid-turn. None of them ends the turn on its own,
	// so every one of them must leave the drain armed.
	for _, tc := range []struct {
		name string
		kind harness.EventKind
	}{
		{"assistant text", harness.EventAssistant},
		{"a tool call", harness.EventToolCall},
		{"a tool result", harness.EventToolResult},
		{"a notice", harness.EventNotice},
		{"an error", harness.EventError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cmd := am.onAgentEvent(agentEventMsg{Kind: tc.kind, Text: "x", Tool: "read_file"})
			if cmd == nil {
				t.Fatalf("%s left the drain UNARMED: the single reader stops here, so the "+
					"rest of the turn is never drained, agentDoneMsg never arrives and the "+
					"turn hangs with agentBusy stuck on", tc.name)
			}
		})
	}
}
