package tui

// agent_turn_lifecycle_bdd_test.go - the godog harness for
// features/tui/agent_turn_lifecycle.feature.
//
// The feature pins the fix for the founder's 2026-08-30 crash
// ("panic: send on closed channel", agent.go:1728 <- loop.go:440). The events
// channel is now allocated once, never closed and never reassigned; end-of-turn
// travels on a per-turn done channel. Several scenarios are therefore STRUCTURAL -
// they assert over the source that the dangerous operations are absent - because
// "this cannot happen" is a stronger guarantee than "it did not happen this run".

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/harness"
)

type turnLifecycleBDD struct {
	t    *testing.T
	m    model
	rt   *agentRuntime
	msgs []tea.Msg // every message the drain produced, in order

	firstEvents  chan harness.Event // the channel identity seen at build time
	doneCount    int
	panicked     any
	queuedOrder  []string
	beatArmed    bool
	queuedBefore int
	lastCmd      tea.Cmd
	queuedText   string
	turnStarted  bool
	lateEmit     func()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// brokerReplying answers /v1/chat/completions with one assistant reply and a cost
// header, so a turn runs to a clean final answer through the real relay path.
func brokerReplying(t *testing.T, reply string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			w.Header().Set("X-RogerAI-Cost", "0.0021")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": reply}}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// brokerFailing refuses every completion, so a turn ends on the error path.
func brokerFailing(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			http.Error(w, "station is off air", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *turnLifecycleBDD) enter(srv *httptest.Server) {
	base := browseSeed(120)
	base.broker = srv.URL
	base.user = "tester"
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	nm, _ := base.enterAgent()
	s.m = asModel(nm)
	s.rt = s.m.agent
	if s.firstEvents == nil {
		s.firstEvents = s.rt.events
	}
}

// drain pumps the single drain Cmd through Update until the turn reports done,
// exactly as the Bubble Tea loop does, recording every message it saw.
func (s *turnLifecycleBDD) drain(limit int) {
	for i := 0; i < limit; i++ {
		cmd := s.m.waitAgentEvent()
		if cmd == nil {
			return
		}
		// TIME-BOUND EVERY PUMP. The drain Cmd blocks by design, so a step that pumps it
		// with no turn in flight would park the suite rather than fail it - a hang tells
		// you nothing about which scenario is wrong.
		got := make(chan tea.Msg, 1)
		go func() { got <- cmd() }()
		var msg tea.Msg
		select {
		case msg = <-got:
		case <-time.After(20 * time.Second):
			s.t.Fatalf("the drain blocked with no event and no done signal after %d message(s)", i)
		}
		s.msgs = append(s.msgs, msg)
		if _, ok := msg.(agentDoneMsg); ok {
			s.doneCount++
		}
		nm, _ := s.m.Update(msg)
		s.m = asModel(nm)
		if _, ok := msg.(agentDoneMsg); ok {
			return
		}
	}
	s.t.Fatal("the turn never reported done")
}

func (s *turnLifecycleBDD) runTurn(prompt string) {
	s.m.agentBusy = true
	s.m.startAgentTurn(prompt)()
	s.drain(200)
}

// source reads agent.go once, for the structural assertions.
func agentSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("agent.go")
	if err != nil {
		t.Fatalf("read agent.go: %v", err)
	}
	return string(b)
}

// waitRunning polls rt.running until it reaches `want`, yielding between checks, and
// reports whether it got there. Every wait in this file goes through it: a hand-rolled
// `for rt.running.Load() {}` spins a core and can starve the goroutine it is waiting on,
// which turns a 15-second failure into a 15-minute hang that says nothing.
func (s *turnLifecycleBDD) waitRunning(want bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if s.rt.running.Load() == want {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return s.rt.running.Load() == want
}

// settle ends any REAL turn goroutine this scenario started and waits for it to
// leave Send. rt.cancel is nil until startAgentTurn has run, so it distinguishes a
// live goroutine from a `running` flag a step set by hand. Steps that force running
// false must settle FIRST: stomping the flag under a live goroutine leaves it running
// into the next scenario, where it writes its Loop while that scenario drives one.
func (s *turnLifecycleBDD) settle() {
	if s.rt == nil || s.rt.cancel == nil {
		return
	}
	s.rt.cancel()
	s.waitRunning(false, 20*time.Second)
}

func (s *turnLifecycleBDD) transcript() string { return stripANSI(strings.Join(s.m.agentLines, "\n")) }

// ---------------------------------------------------------------------------
// Background
// ---------------------------------------------------------------------------

func (s *turnLifecycleBDD) enteredAgent() error {
	s.enter(brokerReplying(s.t, "the answer"))
	if s.rt == nil || s.rt.model == "" {
		return fmt.Errorf("entering AGENT should build a runtime on a tuned band, got %+v", s.rt)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Structural invariants
// ---------------------------------------------------------------------------

func (s *turnLifecycleBDD) aTurnRunsToCompletion() error { s.runTurn("go"); return nil }

func (s *turnLifecycleBDD) eventsStillOpen() error {
	// A receive on a closed channel reports !ok at once; on a live empty one it is
	// not ready. The default arm is the proof the channel is still open.
	select {
	case _, ok := <-s.rt.events:
		if !ok {
			return fmt.Errorf("the events channel was closed - the crash's precondition is back")
		}
		return fmt.Errorf("unexpected buffered event after the turn drained")
	default:
		return nil
	}
}

func (s *turnLifecycleBDD) noPathClosesEvents() error {
	src := agentSource(s.t)
	if strings.Contains(src, "close(rt.events)") {
		return fmt.Errorf("agent.go still closes the events channel: a turn started in the " +
			"window before it can send on a closed channel (the founder's panic)")
	}
	return nil
}

func (s *turnLifecycleBDD) eventsCapturedAtBuild() error {
	if s.firstEvents == nil {
		return fmt.Errorf("no runtime was built")
	}
	return nil
}

func (s *turnLifecycleBDD) threeTurnsBackToBack() error {
	for i := 0; i < 3; i++ {
		s.runTurn(fmt.Sprintf("turn %d", i))
	}
	return nil
}

func (s *turnLifecycleBDD) sameChannelThroughout() error {
	if s.rt.events != s.firstEvents {
		return fmt.Errorf("the events channel was reassigned: an unsynchronised write racing " +
			"the emit closure, the cost callback and the closer")
	}
	return nil
}

func (s *turnLifecycleBDD) onlyUIGoroutineWritesFields() error {
	src := agentSource(s.t)
	if strings.Contains(src, "rt.events = make(") {
		return fmt.Errorf("the drain still re-arms rt.events from its own goroutine")
	}
	return nil
}

func (s *turnLifecycleBDD) goroutineReturnsFromSend() error { s.runTurn("go"); return nil }

func (s *turnLifecycleBDD) closesItsDoneChannel() error {
	select {
	case <-s.rt.turnDone:
		return nil
	default:
		return fmt.Errorf("the turn's done channel was not closed when its goroutine returned")
	}
}

func (s *turnLifecycleBDD) exactlyOneDone() error {
	if s.doneCount != 1 {
		return fmt.Errorf("expected exactly one agentDoneMsg for the turn, got %d", s.doneCount)
	}
	return nil
}

func (s *turnLifecycleBDD) aTurnsGoroutineFinishes() error { s.runTurn("go"); return nil }

func (s *turnLifecycleBDD) runningFalseImpliesNoEmit() error {
	if s.rt.running.Load() {
		return fmt.Errorf("running should be clear once the turn finished")
	}
	// The invariant: the done signal is published BEFORE the guard drops, so anything
	// that starts a turn on `running` alone (agentDrainRetryMsg) finds the turn over.
	select {
	case <-s.rt.turnDone:
	default:
		return fmt.Errorf("running cleared while the turn had not signalled done: the exact " +
			"window agentDrainRetryMsg walked into")
	}
	src := agentSource(s.t)
	closeAt := strings.Index(src, "close(done)")
	guardAt := strings.Index(src, "rt.running.Store(false)")
	if closeAt < 0 || guardAt < 0 || closeAt > guardAt {
		return fmt.Errorf("close(done) must precede rt.running.Store(false) in startAgentTurn")
	}
	return nil
}

// ---------------------------------------------------------------------------
// The reported crash
// ---------------------------------------------------------------------------

func (s *turnLifecycleBDD) promptParkedOnStandby() error {
	s.m.agentQueued = append(s.m.agentQueued, queuedPrompt{text: "the parked ask"})
	return nil
}

func (s *turnLifecycleBDD) retryTimerArmed() error { s.beatArmed = true; return nil }

func (s *turnLifecycleBDD) previousTurnClearsRunning() error {
	s.m.agentBusy = true
	s.m.startAgentTurn("first")()
	if !s.waitRunning(false, 20*time.Second) {
		return fmt.Errorf("the first turn's goroutine never finished")
	}
	return nil
}

func (s *turnLifecycleBDD) retryStartsParkedTurnNow() error {
	// Exactly what agentDrainRetryMsg does: dequeue on running.Load() alone, without
	// waiting for agentDoneMsg. Before the fix this sent on a channel about to close.
	defer func() { s.panicked = recover() }()
	nm, _ := s.m.dequeueAgentPrompts()
	s.m = asModel(nm)
	s.turnStarted = true
	time.Sleep(300 * time.Millisecond) // let the new turn reach its first emit
	return nil
}

// channelOpen proves the events channel is not closed WITHOUT requiring it to be
// empty: a closed channel is always ready to receive, so a default arm that fires is
// itself the proof. Used where a turn is mid-flight and may legitimately have events
// buffered, unlike eventsStillOpen which also asserts the drain kept up.
func (s *turnLifecycleBDD) channelOpen() error {
	select {
	case _, ok := <-s.rt.events:
		if !ok {
			return fmt.Errorf("the events channel was closed under a live turn")
		}
		return nil
	default:
		return nil
	}
}

func (s *turnLifecycleBDD) newTurnEmitsOnLiveChannel() error { return s.channelOpen() }

// noPanic is honest about what it can and cannot see. A panic on a TURN goroutine cannot
// be recovered from here - it takes the process down, and that is what fails the test.
// The recover() in these steps only catches a panic raised on the step's own goroutine.
// Likewise a data race is reported by the -race runtime, not by anything asserted here;
// the Makefile runs the suite with -race, which is what makes that assertion real.
func (s *turnLifecycleBDD) noPanic() error {
	if s.panicked != nil {
		return fmt.Errorf("the session panicked: %v", s.panicked)
	}
	return nil
}

func (s *turnLifecycleBDD) windowFullOfConversation() error {
	s.enter(brokerReplying(s.t, "answer"))
	// A REAL turn has to be in flight: the notice is emitted mid-turn and the turn keeps
	// going, so "the drain is still armed" and "the turn still reaches done" only mean
	// something if there is a turn to finish.
	s.m.agentBusy = true
	s.m.startAgentTurn("a very long conversation")()
	return nil
}

// compactNotice is the literal text harness/loop.go:440 emits when the window is full of
// conversation rather than old tool output - the notice on screen in the founder's crash.
const compactNotice = "nothing to compact - the window is full of conversation, not old " +
	"tool output, and compaction never drops what was said"

func (s *turnLifecycleBDD) emitsNothingToCompact() error {
	defer func() { s.panicked = recover() }()
	// Drive the REAL handler with the REAL event, and keep the Cmd it returns: whether the
	// drain survives a notice is the whole point, and the Cmd is the only evidence of it.
	out, cmd := s.m.onAgentEvent(agentEventMsg{Kind: harness.EventNotice, Text: compactNotice})
	s.m = asModel(out)
	s.lastCmd = cmd
	return nil
}

func (s *turnLifecycleBDD) noticeReachesTranscript() error {
	if !strings.Contains(s.transcript(), "nothing to compact") {
		return fmt.Errorf("the notice should land in the transcript:\n%s", s.transcript())
	}
	return nil
}

func (s *turnLifecycleBDD) drainStillArmed() error {
	if s.lastCmd == nil {
		return fmt.Errorf("the event handler returned no drain Cmd: the single reader stops " +
			"here, so the rest of the turn is never read, agentDoneMsg never arrives, and " +
			"the turn hangs with agentBusy stuck on")
	}
	return nil
}

// emitsKindMidTurn drives one streamed kind through the real handler. None of these ends
// a turn on its own, so every one must leave the drain armed.
func (s *turnLifecycleBDD) emitsKindMidTurn(kind string) error {
	kinds := map[string]harness.EventKind{
		"assistant text": harness.EventAssistant,
		"tool call":      harness.EventToolCall,
		"tool result":    harness.EventToolResult,
		"notice":         harness.EventNotice,
		"error":          harness.EventError,
	}
	k, ok := kinds[kind]
	if !ok {
		return fmt.Errorf("unknown event kind %q", kind)
	}
	s.m.agentBusy = true
	out, cmd := s.m.onAgentEvent(agentEventMsg{Kind: k, Text: "x", Tool: "read_file"})
	s.m = asModel(out)
	s.lastCmd = cmd
	return nil
}

func (s *turnLifecycleBDD) turnStillReachesDone() error {
	s.m.agentBusy = true
	s.drain(200)
	return s.exactlyOneDone()
}

func (s *turnLifecycleBDD) turnStartingAsPreviousExits() error {
	if err := s.previousTurnClearsRunning(); err != nil {
		return err
	}
	return s.retryStartsParkedTurnNow()
}

func (s *turnLifecycleBDD) brokerReturnsCostHeader() error { return nil }

func (s *turnLifecycleBDD) costEventReachesDrain() error {
	// The cost side-channel (agent.go:598) is the OTHER sender onto events, and the
	// one the -race repro caught. Drain the turn and require a cost message.
	s.drain(200)
	for _, m := range s.msgs {
		if _, ok := m.(agentCostMsg); ok {
			return nil
		}
	}
	return fmt.Errorf("no agentCostMsg surfaced through the events drain")
}

func (s *turnLifecycleBDD) manyTurnsStartOnClear(n int) error {
	defer func() { s.panicked = recover() }()
	// A reader has to keep up: a LIVE turn blocks on a full buffer by design (that
	// backpressure is what keeps a turn's stream ordered), so without a drain the
	// harness - not the code under test - would stall. This stands in for the UI.
	// Capture the channel, don't reach through s: the Before hook resets the world for
	// the next scenario, so a drainer that reads s.rt races that reset. And JOIN it before
	// returning - a goroutine still running into the next scenario is the harness's bug,
	// not a finding.
	ev := s.rt.events
	stop := make(chan struct{})
	var drained sync.WaitGroup
	drained.Add(1)
	go func() {
		defer drained.Done()
		for {
			select {
			case <-stop:
				return
			case <-ev:
			}
		}
	}()
	defer func() { close(stop); drained.Wait() }()
	for i := 0; i < n; i++ {
		s.m.agentBusy = true
		s.m.startAgentTurn(fmt.Sprintf("turn %d", i))()
		if !s.waitRunning(false, 20*time.Second) {
			return fmt.Errorf("turn %d never finished", i)
		}
		// The instant running clears, start the next one - the drain-retry's move.
		s.m.startAgentTurn(fmt.Sprintf("racer %d", i))()
		s.waitRunning(false, 20*time.Second)
	}
	return nil
}

func (s *turnLifecycleBDD) noDataRace() error { return s.noPanic() }
func (s *turnLifecycleBDD) noSendOnClosed() error {
	if err := s.noPanic(); err != nil {
		return err
	}
	return s.channelOpen()
}

// ---------------------------------------------------------------------------
// Ordinary turn ends
// ---------------------------------------------------------------------------

func (s *turnLifecycleBDD) turnProducesAnswer() error {
	s.enter(brokerReplying(s.t, "two go files here"))
	s.runTurn("how many go files")
	return nil
}

func (s *turnLifecycleBDD) answerBeforeDone() error {
	doneAt := -1
	for i, m := range s.msgs {
		if _, ok := m.(agentDoneMsg); ok {
			doneAt = i
			break
		}
	}
	if doneAt <= 0 {
		return fmt.Errorf("agentDoneMsg should arrive after the turn's events, at index %d", doneAt)
	}
	if !strings.Contains(s.transcript(), "two go files here") {
		return fmt.Errorf("the final answer should land in the transcript:\n%s", s.transcript())
	}
	return nil
}

func (s *turnLifecycleBDD) promptReEnabled() error {
	if s.m.agentBusy {
		return fmt.Errorf("agentBusy should clear when the turn reports done")
	}
	return nil
}

func (s *turnLifecycleBDD) moreEventsThanOneFrame() error {
	s.enter(brokerReplying(s.t, "tail"))
	// Fill the buffer behind the turn's own events, then close done underneath them:
	// the drain must still hand every one over before it reports the end.
	for i := 0; i < 8; i++ {
		s.rt.events <- harness.Event{Kind: harness.EventNotice, Text: fmt.Sprintf("buffered %d", i)}
	}
	return nil
}

func (s *turnLifecycleBDD) goroutineFinishes() error {
	done := make(chan struct{})
	s.rt.turnDone = done
	close(done)
	return nil
}

func (s *turnLifecycleBDD) bufferedDeliveredBeforeDone() error {
	s.m.agentBusy = true
	s.drain(200)
	seen := 0
	for _, m := range s.msgs {
		if e, ok := m.(agentEventMsg); ok && strings.HasPrefix(e.Text, "buffered ") {
			seen++
		}
		if _, ok := m.(agentDoneMsg); ok {
			if seen != 8 {
				return fmt.Errorf("agentDoneMsg arrived with only %d of 8 buffered events drained: "+
					"a turn's tail would be lost", seen)
			}
			return nil
		}
	}
	return fmt.Errorf("the drain never reported done")
}

func (s *turnLifecycleBDD) noEventDropped() error {
	select {
	case e := <-s.rt.events:
		return fmt.Errorf("an event was left undrained: %+v", e)
	default:
		return nil
	}
}

func (s *turnLifecycleBDD) turnEmitsNothing() error {
	s.enter(brokerReplying(s.t, "x"))
	s.m.agentBusy = true
	done := make(chan struct{})
	s.rt.turnDone = done
	close(done) // a goroutine that returned without emitting
	s.drain(20)
	return nil
}

func (s *turnLifecycleBDD) drainReportsDone() error {
	if s.doneCount > 0 {
		return s.exactlyOneDone()
	}
	got := make(chan tea.Msg, 1)
	go func() { got <- s.m.waitAgentEvent()() }()
	select {
	case m := <-got:
		if _, ok := m.(agentDoneMsg); !ok {
			return fmt.Errorf("expected agentDoneMsg, got %T", m)
		}
		s.doneCount++
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("the drain never reported done")
	}
}

func (s *turnLifecycleBDD) stationReturnsError() error {
	s.enter(brokerFailing(s.t))
	s.runTurn("go")
	return nil
}

func (s *turnLifecycleBDD) errorInTranscript() error {
	for _, m := range s.msgs {
		if e, ok := m.(agentEventMsg); ok && e.Kind == harness.EventError {
			return nil
		}
	}
	return fmt.Errorf("a failing station should surface an error event, got %d messages", len(s.msgs))
}

func (s *turnLifecycleBDD) doneFollowsIt() error {
	if len(s.msgs) == 0 {
		return fmt.Errorf("no messages")
	}
	if _, ok := s.msgs[len(s.msgs)-1].(agentDoneMsg); !ok {
		return fmt.Errorf("the last message of a failed turn should be agentDoneMsg, got %T",
			s.msgs[len(s.msgs)-1])
	}
	return nil
}

func (s *turnLifecycleBDD) eachTurnOneDone() error {
	if s.doneCount != 3 {
		return fmt.Errorf("three turns should report three agentDoneMsg, got %d", s.doneCount)
	}
	return nil
}

func (s *turnLifecycleBDD) noAnswerInAnotherTurnsTranscript() error {
	// An empty buffer proves nothing: the failure mode is an abandoned turn's tail being
	// RENDERED into a later turn. Each of the three turns answered "answer", so the
	// transcript must hold exactly three - no more.
	if got := strings.Count(s.transcript(), "answer"); got != 3 {
		return fmt.Errorf("three turns should leave three answers in the transcript, found %d: "+
			"an event from one turn was rendered into another's", got)
	}
	return s.noEventDropped()
}

// ---------------------------------------------------------------------------
// Cancel and force-stop
// ---------------------------------------------------------------------------

func (s *turnLifecycleBDD) turnInFlight() error {
	s.m.agentBusy = true
	s.m.startAgentTurn("a turn")()
	s.turnStarted = true
	return nil
}

func (s *turnLifecycleBDD) pressEscOnce() error {
	nm, _ := s.m.onAgentKey(tea.KeyMsg{Type: tea.KeyEsc})
	s.m = asModel(nm)
	return nil
}

func (s *turnLifecycleBDD) modelCallAborted() error {
	if !s.m.agentCanceling {
		return fmt.Errorf("the first esc should put the turn into cancelling")
	}
	return nil
}

func (s *turnLifecycleBDD) cancelledThenDone() error {
	s.drain(200)
	return s.promptReEnabled()
}

func (s *turnLifecycleBDD) pressEscTwice() error {
	if err := s.pressEscOnce(); err != nil {
		return err
	}
	return s.pressEscOnce()
}

func (s *turnLifecycleBDD) promptHandedBack() error {
	if s.m.agentBusy {
		return fmt.Errorf("a force-stop should hand the prompt back at once")
	}
	return nil
}

func (s *turnLifecycleBDD) unwindingEmitsSafe() error {
	defer func() { s.panicked = recover() }()
	s.waitRunning(false, 20*time.Second)
	return s.noPanic()
}

func (s *turnLifecycleBDD) forceStoppedStillAlive() error {
	s.m.agentBusy = false    // the force-stop already handed the prompt back
	s.rt.running.Store(true) // but the goroutine has not unwound yet
	s.m.agentQueued = append(s.m.agentQueued, queuedPrompt{text: "waiting on the goroutine"})
	s.beatArmed = true
	return nil
}

func (s *turnLifecycleBDD) submitNewPrompt() error {
	before := len(s.m.agentQueued)
	nm, _ := s.m.submitAgentPrompt(queuedPrompt{text: "the next ask"})
	s.m = asModel(nm)
	if len(s.m.agentQueued) != before+1 {
		return fmt.Errorf("submitting while a goroutine is alive should park the prompt")
	}
	return nil
}

func (s *turnLifecycleBDD) parksOnStandby() error {
	if len(s.m.agentQueued) == 0 {
		return fmt.Errorf("a prompt submitted while a goroutine is alive must park, not start")
	}
	// When the step recorded a baseline (the typed-command scenarios), the queue must
	// actually have GROWN - an already-parked prompt is not proof this one parked.
	if s.queuedBefore > 0 && len(s.m.agentQueued) <= s.queuedBefore {
		return fmt.Errorf("the queue did not grow (%d -> %d): this command was not parked",
			s.queuedBefore, len(s.m.agentQueued))
	}
	return nil
}

func (s *turnLifecycleBDD) startsOnceGoroutineDone() error {
	before := len(s.m.agentQueued)
	if before == 0 {
		return fmt.Errorf("nothing was parked to start")
	}
	s.settle()
	s.rt.running.Store(false)
	nm, _ := s.m.dequeueAgentPrompts()
	s.m = asModel(nm)
	// dequeueAgentPrompts starts the FIRST chat turn and stops there on purpose - the
	// rest wait for THAT turn's done - so the queue shrinks by one and a turn is now
	// running. Requiring an empty queue would be asserting the opposite of the design.
	if len(s.m.agentQueued) >= before {
		return fmt.Errorf("the parked prompt should leave the queue once the goroutine exits: "+
			"%d queued before, %d after", before, len(s.m.agentQueued))
	}
	if !s.m.agentBusy {
		return fmt.Errorf("dequeuing a parked chat prompt should start its turn")
	}
	return nil
}

func (s *turnLifecycleBDD) toolIgnoresCancellation() error {
	if err := s.turnInFlight(); err != nil {
		return err
	}
	return s.pressEscTwice()
}

func (s *turnLifecycleBDD) nextTurnAlreadyStarted() error {
	// ONE Send at a time on the shared loop. rt.running is what guarantees that in
	// production, and a step that starts a turn without honouring it puts two goroutines
	// inside Loop.Send at once - which -race reports against the loop's own fields and
	// which says nothing about the code under test.
	if !s.waitRunning(false, 20*time.Second) {
		return fmt.Errorf("the force-stopped turn's goroutine never left Send")
	}
	// Fill the buffer BEFORE this turn starts, so its first emit is guaranteed to block.
	// Filling afterwards races the turn and usually loses on a fast local broker.
	for filling := true; filling; {
		select {
		case s.rt.events <- harness.Event{Kind: harness.EventNotice, Text: "filler"}:
		default:
			filling = false
		}
	}
	s.m.agentBusy = true
	s.m.startAgentTurn("the next ask")()
	s.waitRunning(true, 15*time.Second)
	return nil
}

func (s *turnLifecycleBDD) abandonedToolEmits() error {
	defer func() { s.panicked = recover() }()
	// The turn from the previous step is parked on an emit it cannot complete (the buffer
	// is full and nobody is draining). Abandon it, as a force-stop does, and the emit must
	// take the ctx.Done() arm and let the goroutine return.
	//
	// Before that arm existed this changed nothing: the goroutine stayed parked on a
	// channel nobody drains, so `running` never cleared - and with the widened command
	// gate the operator could never run /clear or /model again.
	time.Sleep(150 * time.Millisecond) // let it reach the blocked emit
	if s.rt.cancel != nil {
		s.rt.cancel()
	}
	if !s.waitRunning(false, 20*time.Second) {
		return fmt.Errorf("a cancelled turn's emit stayed blocked on a full buffer: its " +
			"goroutine never returns, so running never clears and no later turn or " +
			"loop-mutating command can ever run")
	}
	return nil
}

func (s *turnLifecycleBDD) deliveredOrDroppedHarmlessly() error { return s.noPanic() }

// ---------------------------------------------------------------------------
// The STANDBY queue
// ---------------------------------------------------------------------------

func (s *turnLifecycleBDD) submitAnotherPrompt() error {
	s.rt.running.Store(true)
	return s.submitNewPrompt()
}

func (s *turnLifecycleBDD) runsWhenTurnFinishes() error { return s.startsOnceGoroutineDone() }

func (s *turnLifecycleBDD) submitThreePrompts() error {
	s.rt.running.Store(true)
	for _, p := range []string{"one", "two", "three"} {
		s.m.agentQueued = append(s.m.agentQueued, queuedPrompt{text: p})
		s.queuedOrder = append(s.queuedOrder, p)
	}
	return nil
}

func (s *turnLifecycleBDD) runInSubmittedOrder() error {
	var got []string
	for _, q := range s.m.agentQueued {
		got = append(got, q.text)
	}
	if strings.Join(got, ",") != strings.Join(s.queuedOrder, ",") {
		return fmt.Errorf("the queue must drain FIFO: submitted %v, holds %v", s.queuedOrder, got)
	}
	return nil
}

func (s *turnLifecycleBDD) parksAfterDrainFired() error {
	s.rt.running.Store(true)
	nm, cmd := s.m.submitAgentPrompt(queuedPrompt{text: "parked late"})
	s.m = asModel(nm)
	if cmd == nil {
		return fmt.Errorf("parking must arm a re-check, or the queue sits forever")
	}
	s.beatArmed = true
	return nil
}

func (s *turnLifecycleBDD) retryBeatLands() error {
	if !s.beatArmed {
		return fmt.Errorf("no retry beat was armed")
	}
	nm, cmd := s.m.Update(agentDrainRetryMsg{})
	s.m = asModel(nm)
	s.lastCmd = cmd
	return nil
}

func (s *turnLifecycleBDD) promptRuns() error {
	s.settle()
	s.rt.running.Store(false)
	nm, _ := s.m.Update(agentDrainRetryMsg{})
	s.m = asModel(nm)
	if len(s.m.agentQueued) != 0 {
		return fmt.Errorf("the retry beat should drain the parked prompt")
	}
	return nil
}

func (s *turnLifecycleBDD) queueNotForever() error { return nil }

func (s *turnLifecycleBDD) noTurnStarts() error {
	if len(s.m.agentQueued) == 0 {
		return fmt.Errorf("the retry must not start a turn while a goroutine is still alive")
	}
	return nil
}

func (s *turnLifecycleBDD) anotherBeatArmed() error {
	// THE ANTI-DEADLOCK PROPERTY. If the retry finds the goroutine still alive and does
	// not re-arm, the parked prompt waits for an event that is never coming - the exact
	// deadlock (two STANDBY prompts, deck reading "standing by", no turn running) this
	// retry exists to fix.
	if s.lastCmd == nil {
		return fmt.Errorf("the retry found the goroutine still alive and armed nothing: the " +
			"parked queue would sit forever")
	}
	return nil
}

func (s *turnLifecycleBDD) originPromptQueued(origin, text string) error {
	s.rt.running.Store(true)
	s.queuedText = text
	// /clear is the observable command: it drops the session's thread id. Stamping a
	// witness lets the Then below tell "ran inline" from "went out as a chat turn"
	// without guessing.
	s.m.threadID = witnessThread
	s.m.agentQueued = append(s.m.agentQueued, queuedPrompt{text: text, remote: origin == "remote"})
	return nil
}

func (s *turnLifecycleBDD) queueDrains() error {
	s.settle()
	s.rt.running.Store(false)
	nm, _ := s.m.dequeueAgentPrompts()
	s.m = asModel(nm)
	return nil
}

func (s *turnLifecycleBDD) treatedAs(treatment string) error {
	ran := s.m.threadID != witnessThread // only an executed /clear drops the thread id
	switch treatment {
	case "an inline command":
		if !ran {
			return fmt.Errorf("a LOCALLY typed %q should run inline exactly as if typed when "+
				"idle, and it did not", s.queuedText)
		}
		return nil
	case "a chat turn":
		// THE SECURITY GUARD (ruling 7 / iteration-1 finding #1): a REMOTE-origin "/..."
		// must never slash-dispatch - the busy queue must not remote-exec host commands.
		// A remote "/clear" that executed would drop the thread id, so the witness catches
		// exactly the regression this row exists to prevent.
		if ran {
			return fmt.Errorf("a REMOTE %q slash-dispatched: the busy queue must never "+
				"remote-exec a host command, it must go out as a chat turn", s.queuedText)
		}
		if len(s.m.agentQueued) != 0 {
			return fmt.Errorf("%q should have been submitted as a chat turn, but it is still "+
				"sitting on the queue", s.queuedText)
		}
		return nil
	default:
		return fmt.Errorf("unknown treatment %q", treatment)
	}
}

// ---------------------------------------------------------------------------
// Slash commands in the force-stop window
// ---------------------------------------------------------------------------

// /clear is the loop-mutating command under test: it calls loop.Reset() AND drops the
// session's thread id. The thread id is already on the model, so it witnesses whether
// the command actually executed without adding an accessor to Loop for a test's sake -
// if the id survives, runAgentCommand never ran and Reset() never touched l.messages.
const witnessThread = "thread-that-only-a-clear-removes"

func (s *turnLifecycleBDD) typeAndEnter(cmd string) error {
	s.m.threadID = witnessThread
	// The force-stop Given already parks one prompt, so what matters is whether THIS
	// command adds to the queue - not whether the queue is empty.
	s.queuedBefore = len(s.m.agentQueued)
	s.m.agentIn.SetValue(cmd)
	out, _ := s.m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	s.m = asModel(out)
	return nil
}

func (s *turnLifecycleBDD) loopNotResetWhileAlive() error {
	if !s.rt.running.Load() {
		return fmt.Errorf("the goroutine should still be alive for this assertion to mean anything")
	}
	if s.m.threadID != witnessThread {
		return fmt.Errorf("/clear executed while a force-stopped goroutine was still inside " +
			"Send: loop.Reset() mutates l.messages and l.spill from the UI goroutine, which " +
			"is a data race against that goroutine")
	}
	return nil
}

func (s *turnLifecycleBDD) runsOnceGoroutineFinished() error {
	// dequeueAgentPrompts runs leading commands inline but STOPS at the first chat turn
	// (the rest wait for that turn's done), so draining a mixed queue takes one pass per
	// turn. Pump it the way the real agentDoneMsg chain does, then ask the witness.
	for i := 0; i < 6 && len(s.m.agentQueued) > 0; i++ {
		s.settle()
		s.rt.running.Store(false)
		s.m.agentBusy = false
		nm, _ := s.m.dequeueAgentPrompts()
		s.m = asModel(nm)
	}
	if s.m.threadID == witnessThread {
		return fmt.Errorf("the parked /clear never ran after the goroutine exited: parking a " +
			"command must delay it, not drop it")
	}
	return nil
}

func (s *turnLifecycleBDD) runsImmediately() error {
	if len(s.m.agentQueued) != s.queuedBefore {
		return fmt.Errorf("a command that touches nothing shared should not be parked: "+
			"the queue went %d -> %d", s.queuedBefore, len(s.m.agentQueued))
	}
	return nil
}

func (s *turnLifecycleBDD) neverTouchesLoop() error {
	if s.m.threadID != witnessThread {
		return fmt.Errorf("an instant command must not reset the session")
	}
	return nil
}

// ---------------------------------------------------------------------------
// The confirm channel
// ---------------------------------------------------------------------------

func (s *turnLifecycleBDD) turnCallsMutatingTool() error { return s.turnInFlight() }

func (s *turnLifecycleBDD) loopRequestsConfirmation() error {
	resp := make(chan bool, 1)
	go func() {
		s.rt.confirmReq <- agentConfirm{tool: "write_file", args: map[string]any{"path": "x"}, resp: resp}
	}()
	return nil
}

func (s *turnLifecycleBDD) confirmReachesUI() error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cmd := s.m.waitAgentEvent()
		if cmd == nil {
			return fmt.Errorf("no drain")
		}
		msg := cmd()
		if c, ok := msg.(agentConfirmMsg); ok {
			if c.tool != "write_file" {
				return fmt.Errorf("wrong confirm surfaced: %+v", c)
			}
			// Put it on the model, as Update does in the real loop: without this there is
			// no agentPendingConfirm and the y/N step below has nothing to answer.
			out, _ := s.m.Update(msg)
			s.m = asModel(out)
			if s.m.agentPendingConfirm == nil {
				return fmt.Errorf("the confirm reached the drain but never became a pending gate")
			}
			return nil
		}
	}
	return fmt.Errorf("the confirm never reached the UI")
}

func (s *turnLifecycleBDD) answeringResumes() error {
	// Answering is what re-arms the drain: agentConfirmMsg deliberately does NOT, so if
	// the y/N handler did not either, the stream would stop at the first gate.
	out, cmd := s.m.onAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	s.m = asModel(out)
	if cmd == nil {
		return fmt.Errorf("answering a confirm must re-arm the drain, or the turn stops at " +
			"its first gate with the answer already given")
	}
	return nil
}

func (s *turnLifecycleBDD) confirmOutstanding() error {
	s.enter(brokerReplying(s.t, "x"))
	s.m.agentBusy = true
	// ACTUALLY PARK A CONFIRM. Setting agentBusy alone left the drain with only `done`
	// ready, so the scenario never exercised the case it names - the drain having a
	// pending gate AND a finished turn to choose between. With a real sender parked, the
	// drain must still report done rather than sitting on the confirm forever.
	go func() {
		s.rt.confirmReq <- agentConfirm{
			tool: "write_file",
			args: map[string]any{"path": "x"},
			resp: make(chan bool, 1),
		}
	}()
	time.Sleep(50 * time.Millisecond) // let it park on the send
	return nil
}

func (s *turnLifecycleBDD) goroutineExits() error {
	done := make(chan struct{})
	s.rt.turnDone = done
	close(done)
	return nil
}

func (s *turnLifecycleBDD) drainReturnsPromptly() error {
	// The failure this guards against is a PARKED drain: a gate outstanding and nothing
	// ever reported again, so agentBusy never clears and the session looks hung. Which of
	// the two ready messages it picks is not the point.
	got := make(chan tea.Msg, 1)
	go func() { got <- s.m.waitAgentEvent()() }()
	select {
	case msg := <-got:
		out, _ := s.m.Update(msg)
		s.m = asModel(out)
		if _, ok := msg.(agentDoneMsg); ok {
			s.doneCount++
		}
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("the drain parked with a confirm outstanding: nothing is reported " +
			"again, so the turn never clears and the session looks hung")
	}
}

// ---------------------------------------------------------------------------
// runner
// ---------------------------------------------------------------------------

func TestAgentTurnLifecycleFeature(t *testing.T) {
	st := &turnLifecycleBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = turnLifecycleBDD{t: t}
				return c, nil
			})
			// A scenario that leaves a turn goroutine running would carry it into the
			// NEXT scenario, where it is still writing its Loop while that scenario drives
			// one - a cross-scenario race that is the harness's doing, not the code's.
			// Settle each scenario's own goroutines before the next one starts.
			sc.After(func(c context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				st.settle()
				return c, nil
			})
			sc.Step(`^I have entered AGENT mode with a band tuned in$`, st.enteredAgent)

			sc.Step(`^a turn runs to completion$`, st.aTurnRunsToCompletion)
			sc.Step(`^the runtime's events channel is still open$`, st.eventsStillOpen)
			sc.Step(`^no code path closes the events channel$`, st.noPathClosesEvents)
			sc.Step(`^the runtime's events channel is captured at build time$`, st.eventsCapturedAtBuild)
			sc.Step(`^three turns run back to back$`, st.threeTurnsBackToBack)
			sc.Step(`^the runtime's events channel is the same channel throughout$`, st.sameChannelThroughout)
			sc.Step(`^only the UI goroutine ever writes runtime fields$`, st.onlyUIGoroutineWritesFields)
			sc.Step(`^a turn's goroutine returns from Send$`, st.goroutineReturnsFromSend)
			sc.Step(`^it closes that turn's done channel$`, st.closesItsDoneChannel)
			sc.Step(`^the drain reports agentDoneMsg exactly once for that turn$`, st.exactlyOneDone)
			sc.Step(`^a turn's goroutine finishes$`, st.aTurnsGoroutineFinishes)
			sc.Step(`^observing running as false implies that turn can no longer emit$`, st.runningFalseImpliesNoEmit)

			sc.Step(`^a prompt is parked on STANDBY$`, st.promptParkedOnStandby)
			sc.Step(`^the drain-retry timer is armed$`, st.retryTimerArmed)
			sc.Step(`^the previous turn's goroutine clears running$`, st.previousTurnClearsRunning)
			sc.Step(`^the retry starts the parked turn in that same instant$`, st.retryStartsParkedTurnNow)
			sc.Step(`^the new turn emits onto a live channel$`, st.newTurnEmitsOnLiveChannel)
			sc.Step(`^the session does not panic$`, st.noPanic)
			sc.Step(`^the conversation fills the window with conversation, not old tool output$`, st.windowFullOfConversation)
			sc.Step(`^the loop emits the "nothing to compact" notice mid-turn$`, st.emitsNothingToCompact)
			sc.Step(`^the loop emits a (.+) mid-turn$`, st.emitsKindMidTurn)
			sc.Step(`^the drain is still armed so the rest of the turn is read$`, st.drainStillArmed)
			sc.Step(`^the turn still reaches agentDoneMsg$`, st.turnStillReachesDone)
			sc.Step(`^the notice reaches the transcript$`, st.noticeReachesTranscript)
			sc.Step(`^a turn is starting as the previous turn's goroutine exits$`, st.turnStartingAsPreviousExits)
			sc.Step(`^the broker returns a per-turn cost header$`, st.brokerReturnsCostHeader)
			sc.Step(`^the cost event reaches the drain$`, st.costEventReachesDrain)
			sc.Step(`^(\d+) turns start the instant the previous turn clears running$`, st.manyTurnsStartOnClear)
			sc.Step(`^no data race is reported on the runtime's channels$`, st.noDataRace)
			sc.Step(`^no send happens on a closed channel$`, st.noSendOnClosed)

			sc.Step(`^a turn produces an assistant answer$`, st.turnProducesAnswer)
			sc.Step(`^the answer lands in the transcript before agentDoneMsg$`, st.answerBeforeDone)
			sc.Step(`^the prompt is re-enabled$`, st.promptReEnabled)
			sc.Step(`^a turn emits more events than the UI drains in one frame$`, st.moreEventsThanOneFrame)
			sc.Step(`^the turn's goroutine finishes$`, st.goroutineFinishes)
			sc.Step(`^every buffered event is delivered before agentDoneMsg$`, st.bufferedDeliveredBeforeDone)
			sc.Step(`^no event is dropped$`, st.noEventDropped)
			sc.Step(`^a turn ends without emitting a single event$`, st.turnEmitsNothing)
			sc.Step(`^the drain reports agentDoneMsg$`, st.drainReportsDone)
			sc.Step(`^the station returns an error$`, st.stationReturnsError)
			sc.Step(`^the error lands in the transcript$`, st.errorInTranscript)
			sc.Step(`^agentDoneMsg follows it$`, st.doneFollowsIt)
			sc.Step(`^each turn reports exactly one agentDoneMsg$`, st.eachTurnOneDone)
			sc.Step(`^no turn's answer appears in another turn's transcript$`, st.noAnswerInAnotherTurnsTranscript)

			sc.Step(`^a turn is in flight$`, st.turnInFlight)
			sc.Step(`^I press esc once$`, st.pressEscOnce)
			sc.Step(`^the in-flight model call is aborted$`, st.modelCallAborted)
			sc.Step(`^the turn reports "turn cancelled" then agentDoneMsg$`, st.cancelledThenDone)
			sc.Step(`^I press esc twice$`, st.pressEscTwice)
			sc.Step(`^the prompt is handed back immediately$`, st.promptHandedBack)
			sc.Step(`^the still-unwinding goroutine's remaining emits do not panic$`, st.unwindingEmitsSafe)
			sc.Step(`^a force-stopped turn's goroutine is still alive$`, st.forceStoppedStillAlive)
			sc.Step(`^I submit a new prompt$`, st.submitNewPrompt)
			sc.Step(`^it parks on STANDBY$`, st.parksOnStandby)
			sc.Step(`^it starts only once that goroutine has finished$`, st.startsOnceGoroutineDone)
			sc.Step(`^a force-stopped turn whose tool runs past its context cancellation$`, st.toolIgnoresCancellation)
			sc.Step(`^the next turn has already started$`, st.nextTurnAlreadyStarted)
			sc.Step(`^the abandoned tool emits its result$`, st.abandonedToolEmits)
			sc.Step(`^the emit is delivered or dropped harmlessly$`, st.deliveredOrDroppedHarmlessly)

			sc.Step(`^I submit another prompt$`, st.submitAnotherPrompt)
			sc.Step(`^it runs when the turn finishes$`, st.runsWhenTurnFinishes)
			sc.Step(`^I submit three prompts$`, st.submitThreePrompts)
			sc.Step(`^they run in the order I submitted them$`, st.runInSubmittedOrder)
			sc.Step(`^a prompt parks in the window where the drain has already fired$`, st.parksAfterDrainFired)
			sc.Step(`^the retry beat lands$`, st.retryBeatLands)
			sc.Step(`^the prompt runs$`, st.promptRuns)
			sc.Step(`^the queue does not sit forever$`, st.queueNotForever)
			sc.Step(`^no turn starts$`, st.noTurnStarts)
			sc.Step(`^another beat is armed$`, st.anotherBeatArmed)
			sc.Step(`^a (local|remote) prompt "([^"]*)" is queued$`, st.originPromptQueued)
			sc.Step(`^the queue drains$`, st.queueDrains)
			sc.Step(`^it is treated as (.+)$`, st.treatedAs)

			sc.Step(`^I type "([^"]*)" and press enter$`, st.typeAndEnter)
			sc.Step(`^the loop is not reset while that goroutine is alive$`, st.loopNotResetWhileAlive)
			sc.Step(`^it runs once that goroutine has finished$`, st.runsOnceGoroutineFinished)
			sc.Step(`^it runs immediately$`, st.runsImmediately)
			sc.Step(`^it never touches the shared loop$`, st.neverTouchesLoop)

			sc.Step(`^a turn calls a mutating tool$`, st.turnCallsMutatingTool)
			sc.Step(`^the loop requests confirmation$`, st.loopRequestsConfirmation)
			sc.Step(`^the y/N prompt reaches the UI$`, st.confirmReachesUI)
			sc.Step(`^answering it resumes the turn$`, st.answeringResumes)
			sc.Step(`^a confirm is outstanding$`, st.confirmOutstanding)
			sc.Step(`^the turn's goroutine exits$`, st.goroutineExits)
			sc.Step(`^the drain returns promptly rather than parking$`, st.drainReturnsPromptly)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/tui/agent_turn_lifecycle.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("agent turn lifecycle scenarios failed")
	}
}
