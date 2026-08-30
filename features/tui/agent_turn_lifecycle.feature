# TUI Agent turn lifecycle - the events channel and the running guard
#
# Red-to-green for the crash the founder hit 2026-08-30:
#
#   panic: send on closed channel
#     internal/tui.model.startAgentTurn.func1.1.1   agent.go:1728
#     internal/harness.(*Loop).Send.func2            loop.go:400
#     internal/harness.(*Loop).Send                  loop.go:440
#     internal/tui.model.startAgentTurn.func1.1      agent.go:1727
#
# ROOT CAUSE. startAgentTurn's goroutine cleared the guard BEFORE closing the channel
# the guard protects:
#
#     rt.running.Store(false)   // agent.go:1733
#     close(rt.events)          // agent.go:1734
#
# which contradicts the invariant the agentRuntime struct documents at agent.go:68
# ("running is true ... until it returns (after Send + close)"). Every turn-start path
# except one is serialised by agentDoneMsg, which fires only AFTER the drain observes
# the close. The exception is agentDrainRetryMsg - the 120ms parked-queue re-check armed
# whenever a prompt lands on STANDBY - which starts a turn on running.Load() alone. A
# turn launched between those two statements captured the OLD channel, which the
# previous goroutine then closed underneath it.
#
# SECOND DEFECT (same class, independent trigger). `events` was a plain chan field that
# the DRAIN goroutine reassigned (`rt.events = make(...)`, agent.go:1757) while three
# other goroutines read it (the emit closure :1728, the cost side-channel :598, and the
# closer :1734). Unsynchronised, so a sender could read a stale channel pointer without
# needing to win the two-instruction race at all.
#
# THE FIX (founder-approved 2026-08-30). Stop closing and re-arming. `events` is
# allocated once per runtime, never closed and never reassigned; end-of-turn is signalled
# by a per-turn `done` channel the goroutine closes. "send on closed channel" becomes
# structurally impossible, and the cross-goroutine field write disappears.

Feature: Agent turn lifecycle is safe across turns
  As an operator running back-to-back agent turns
  I want a finished turn to hand off to the next one without ever crashing the TUI
  So that a queued prompt, a cancel, or a force-stop can never take the session down.

  Background:
    Given I have entered AGENT mode with a band tuned in

  # -------------------------------------------------------------------------
  # Structural invariants - the properties that make the crash impossible
  # -------------------------------------------------------------------------

  Scenario: The events channel is never closed
    When a turn runs to completion
    Then the runtime's events channel is still open
    And no code path closes the events channel

  Scenario: The events channel is never reassigned after the runtime is built
    Given the runtime's events channel is captured at build time
    When three turns run back to back
    Then the runtime's events channel is the same channel throughout
    And only the UI goroutine ever writes runtime fields

  Scenario: End of turn is signalled by a per-turn done channel
    When a turn's goroutine returns from Send
    Then it closes that turn's done channel
    And the drain reports agentDoneMsg exactly once for that turn

  Scenario: The running guard clears no earlier than the turn's done signal
    When a turn's goroutine finishes
    Then observing running as false implies that turn can no longer emit

  # -------------------------------------------------------------------------
  # The reported crash and its immediate neighbourhood
  # -------------------------------------------------------------------------

  Scenario: A drain-retry turn started the instant running clears does not panic
    Given a prompt is parked on STANDBY
    And the drain-retry timer is armed
    When the previous turn's goroutine clears running
    And the retry starts the parked turn in that same instant
    Then the new turn emits onto a live channel
    And the session does not panic

  # A notice is emitted MID-TURN and the turn keeps going, so it must not end the stream.
  # onAgentEvent returned early for this one kind, which stopped the single drain dead:
  # the rest of the turn was never read, agentDoneMsg never arrived, and the turn hung
  # with agentBusy stuck on. The founder's crash shows this exact notice immediately
  # before the panic - with the drain stopped, the old code never re-armed the events
  # channel, the goroutine closed it anyway, and the next turn sent on it.
  Scenario: The compaction dead-end notice cannot crash or strand the turn
    Given the conversation fills the window with conversation, not old tool output
    When the loop emits the "nothing to compact" notice mid-turn
    Then the notice reaches the transcript
    And the drain is still armed so the rest of the turn is read
    And the turn still reaches agentDoneMsg
    And the session does not panic

  Scenario Outline: No mid-turn event kind may stop the stream
    When the loop emits a <kind> mid-turn
    Then the drain is still armed so the rest of the turn is read

    Examples:
      | kind            |
      | assistant text  |
      | tool call       |
      | tool result     |
      | notice          |
      | error           |

  Scenario: The cost side-channel cannot send on a dead channel
    Given a turn is starting as the previous turn's goroutine exits
    When the broker returns a per-turn cost header
    Then the cost event reaches the drain
    And the session does not panic

  Scenario: Back-to-back turns under the race detector stay clean
    When 200 turns start the instant the previous turn clears running
    Then no data race is reported on the runtime's channels
    And no send happens on a closed channel

  # -------------------------------------------------------------------------
  # Ordinary turn ends - no regression in what already worked
  # -------------------------------------------------------------------------

  Scenario: A turn that answers cleanly reports done after its last event
    When a turn produces an assistant answer
    Then the answer lands in the transcript before agentDoneMsg
    And the prompt is re-enabled

  Scenario: Buffered events still render before the turn reports done
    Given a turn emits more events than the UI drains in one frame
    When the turn's goroutine finishes
    Then every buffered event is delivered before agentDoneMsg
    And no event is dropped

  Scenario: A turn that emits nothing still reports done
    When a turn ends without emitting a single event
    Then the drain reports agentDoneMsg
    And the prompt is re-enabled

  Scenario: A turn that ends in an error reports the error then done
    When the station returns an error
    Then the error lands in the transcript
    And agentDoneMsg follows it

  Scenario: The runtime is reusable across turns within one session
    When three turns run back to back
    Then each turn reports exactly one agentDoneMsg
    And no turn's answer appears in another turn's transcript

  # -------------------------------------------------------------------------
  # Cancel and force-stop - the paths that decouple the UI from the goroutine
  # -------------------------------------------------------------------------

  Scenario: A cancelled turn unwinds without crashing
    Given a turn is in flight
    When I press esc once
    Then the in-flight model call is aborted
    And the turn reports "turn cancelled" then agentDoneMsg

  Scenario: A force-stopped turn's goroutine emits safely while the UI moves on
    Given a turn is in flight
    When I press esc twice
    Then the prompt is handed back immediately
    And the still-unwinding goroutine's remaining emits do not panic

  Scenario: A prompt submitted while a force-stopped goroutine unwinds is queued
    Given a force-stopped turn's goroutine is still alive
    When I submit a new prompt
    Then it parks on STANDBY
    And it starts only once that goroutine has finished

  # FOUND while proving the crash fix, 2026-08-30. The force-stop deliberately splits
  # the UI's agentBusy from the goroutine's rt.running - that split is the whole point of
  # it - but the slash-command gate was reading agentBusy alone. In the window it opens,
  # a "/clear" typed by hand ran loop.Reset() on the UI goroutine while the abandoned
  # goroutine was still inside Send, mutating l.messages and l.spill underneath it.
  # Same class as the panic above (UI touching shared loop state while a force-stopped
  # goroutine unwinds), a different site, and a data race rather than a crash.
  Scenario: A loop-mutating command cannot run while a force-stopped goroutine unwinds
    Given a force-stopped turn's goroutine is still alive
    When I type "/clear" and press enter
    Then it parks on STANDBY
    And the loop is not reset while that goroutine is alive
    And it runs once that goroutine has finished

  Scenario: A command that touches nothing shared still runs in that window
    Given a force-stopped turn's goroutine is still alive
    When I type "/perms" and press enter
    Then it runs immediately
    And it never touches the shared loop

  Scenario: A loop-mutating command typed during an ordinary turn still parks
    Given a turn is in flight
    When I type "/clear" and press enter
    Then it parks on STANDBY

  Scenario: A new turn starts on an empty channel
    Given a force-stopped turn left its tail in the buffer
    When the next turn starts
    Then the abandoned turn's steps never reach the transcript

  Scenario: The STANDBY line does not offer esc as a cancel where esc leaves
    Given a force-stopped turn's goroutine is still alive
    When I submit a new prompt
    Then the status says the previous turn is still unwinding
    And it does not offer esc as a way to cancel

  Scenario: A queue that could not drain is not reported as ready
    Given a force-stopped turn's goroutine is still alive
    And a prompt is parked on STANDBY
    When that turn's done is handled
    Then the status does not claim the agent is ready

  Scenario: A tool that ignores cancellation cannot crash the next turn
    Given a force-stopped turn whose tool runs past its context cancellation
    When the next turn has already started
    And the abandoned tool emits its result
    Then the emit is delivered or dropped harmlessly
    And the session does not panic

  # -------------------------------------------------------------------------
  # The STANDBY queue - the path that walked into the window
  # -------------------------------------------------------------------------

  Scenario: A prompt typed mid-turn parks and drains when the turn finishes
    Given a turn is in flight
    When I submit another prompt
    Then it parks on STANDBY
    And it runs when the turn finishes

  Scenario: The parked queue drains FIFO
    Given a turn is in flight
    When I submit three prompts
    Then they run in the order I submitted them

  Scenario: The drain-retry never drops a parked prompt
    Given a prompt parks in the window where the drain has already fired
    When the retry beat lands
    Then the prompt runs
    And the queue does not sit forever

  # THE WINDOW THE ORDERING OPENS. close(done) precedes the guard clearing - that is what
  # removes the instant a turn can look finished while it can still emit - so agentDoneMsg
  # legitimately arrives while rt.running is still true. The dequeue must hand the queue
  # to something before it gives up, because the done that would have drained it has
  # already been spent and no tick looks at the queue.
  Scenario: A done that arrives before the guard clears still comes back for the queue
    Given a prompt is parked on STANDBY
    And the turn has signalled done but its goroutine has not returned
    When the queue is drained
    Then a re-check is armed rather than the prompt being abandoned
    And the prompt is still queued

  Scenario: The drain-retry stops early while a goroutine is still alive
    Given a force-stopped turn's goroutine is still alive
    When the retry beat lands
    Then no turn starts
    And another beat is armed

  Scenario Outline: A queued prompt keeps its origin through the retry
    Given a turn is in flight
    When a <origin> prompt "<text>" is queued
    And the queue drains
    Then it is treated as <treatment>

    Examples:
      | origin | text               | treatment         |
      | local  | /clear             | an inline command |
      | local  | what is up         | a chat turn       |
      | remote | /clear             | a chat turn       |
      | remote | /operator opencode | a chat turn       |
      | remote | what is up         | a chat turn       |

  # -------------------------------------------------------------------------
  # The confirm channel - the drain's other input
  # -------------------------------------------------------------------------

  Scenario: A mutating-tool confirm still reaches the UI mid-turn
    Given a turn calls a mutating tool
    When the loop requests confirmation
    Then the y/N prompt reaches the UI
    And answering it resumes the turn

  # Stranding means the drain PARKS with a gate outstanding and never reports anything
  # again - not that it prefers one ready message over the other. Reporting the confirm
  # first is correct: the gate is shown, answering it re-arms the drain, and the turn's
  # done is still there to be read.
  Scenario: A confirm pending at turn end does not strand the drain
    Given a confirm is outstanding
    When the turn's goroutine exits
    Then the drain returns promptly rather than parking
    And the turn still reaches agentDoneMsg
