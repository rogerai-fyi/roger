# GUEST OPERATORS — Phase 2: the tea.ExecProcess handoff lifecycle (money-adjacent).
#
# The handoff is suspend-and-exec (§2: "whenever the TUI is painting, the DJ has the mic" —
# there is no persistent mode to track). Mechanics specced here; THE DESK view, capability
# gating, and the pre-launch plate are Phase 3.
#
# GROUND TRUTH seams:
#   - preconditions read the LIVE ProxyOptionsHolder (client.go:454): Connected() must be
#     true — Phase 1 ruling 5 already makes a disconnected proxy REFUSE to spend
#     (features/proxy/live_options.feature "disconnected proxy refuses"), so launching
#     while not tuned would only hand the guest a wall of 502s; block it at the desk.
#   - must not be mid-agent-turn: m.agentBusy || m.agent.running (the dequeue guard,
#     agent.go:561) — the DJ's in-flight turn owns the completer and the terminal.
#   - PATCHING YOU THROUGH is staged like connectingView (connectStep, tui.go:4590-4676):
#     ONE staged paint (mic to / on band / wire lines, §3 mockup) is rendered BEFORE the
#     tea.ExecProcess cmd is returned, so the exec never cuts from a stale screen to a
#     foreign TUI ("anti-blank").
#   - budget: SetBudget(client.DefaultSessionBudget) ($2.00, client.go:411, founder ruling
#     2026-07-06) + ResetSpend() + a reset call counter, applied per handoff so the summary
#     and the 402 ceiling are THIS guest's numbers, never a previous session's.
#   - session key: STABLE for the whole TUI session (SetBand keeps it, client.go:488);
#     handoffs rotate budgets and spend, never the bearer.
#   - return: bubbletea delivers the ExecProcess callback for every child outcome; the
#     callback restores the terminal, refreshes the balance, prints the one-line summary,
#     and cleans the scratch config.
#   - exit-code semantics: 0 clean; 130 (SIGINT) and any *exec.ExitError = "the guest
#     dropped off (exit N)" — a guest quitting is NORMAL radio traffic, never a scary
#     red stack; only a SPAWN failure (exec never started) is an error note.
#
# Terminal restore (defensive preamble, empirically needed — a guest TUI can leave any
# combination of modes on): pop the kitty keyboard protocol, disable all mouse reporting
# modes, exit bracketed paste — THEN re-enable what the radio itself uses: mouse cell
# motion only when the user has it on (respect m.mouseOff, tui.go:646/1740-1746).

Feature: Handing the mic — suspend, exec, and the return to the desk
  A handoff only starts when the channel can actually carry it, shows one staged
  PATCHING YOU THROUGH paint, runs the guest on the live band with a fresh $2
  budget, and always returns to a working desk with an honest one-line summary.

  Background:
    Given an AGENT session with a tuned band "qwen3-32b-fp8" and a live proxy holder
    And a detected guest "opencode"

  # ── preconditions ────────────────────────────────────────────────────────────────

  Scenario: No band tuned — the handoff is refused with the tune-in note
    Given the proxy holder is disconnected
    When the user runs "/operator opencode"
    Then no child process is launched
    And the transcript notes "no channel to patch into"
    And the transcript points at tuning in first

  Scenario: Mid-agent-turn — the handoff is refused while the DJ is talking
    Given a DJ turn is in flight
    When the user runs "/operator opencode"
    Then no child process is launched
    And the transcript notes the DJ is mid-turn

  Scenario: A queued prompt also blocks the handoff
    # agentQueued drains into a new turn the moment the current one ends (agent.go:558);
    # execing between them would orphan the queue into a suspended TUI.
    Given a DJ turn is in flight and a prompt is queued
    When the user runs "/operator opencode"
    Then no child process is launched

  # ── the staged transition ────────────────────────────────────────────────────────

  Scenario: One staged PATCHING YOU THROUGH paint precedes the exec
    When the user runs "/operator opencode"
    Then the next paint shows "PATCHING YOU THROUGH"
    And it shows the mic-to line for "opencode"
    And it shows the on-band line for "qwen3-32b-fp8"
    And it shows the wire line "config generated in a scratch dir"
    And only after that paint is the exec command issued

  Scenario: The staged paint shows the real endpoint and model
    When the user runs "/operator opencode"
    Then the staged paint shows the live proxy BASE URL
    And the staged paint shows MODEL "qwen3-32b-fp8"

  # ── the live wiring at exec time ─────────────────────────────────────────────────

  Scenario: The child is composed from the LIVE proxy options at exec time
    Given the band was re-tuned since the proxy first bound
    When the user runs "/operator opencode"
    Then the child wiring carries the CURRENT band model and endpoint
    And never the options frozen at first bind

  Scenario: Each handoff starts with the default $2 budget and zero spend
    Given the previous session spent "$1.37" of its budget
    When the user runs "/operator opencode"
    Then the holder budget is the default session budget
    And the holder spend reads $0.00
    And the holder call counter reads 0

  Scenario: The session bearer key is stable across the handoff
    When the user runs "/operator opencode"
    Then the child env carries the SAME session key the proxy enforces
    And the key was not rotated by the handoff

  # ── the return to the desk ───────────────────────────────────────────────────────

  Scenario: Clean exit — terminal restored, balance refreshed, one-line summary
    When the guest returns after 14 minutes, 42 calls, and $0.19 spend with exit 0
    Then the terminal reset preamble ran: kitty keyboard popped, mouse modes off, bracketed paste exited
    And mouse cell motion is re-enabled because the user has the mouse on
    And a balance refresh is requested
    And the transcript shows "back at the desk · opencode had the mic for 14m · 42 calls · $0.19"

  Scenario: The summary numbers come from the proxy accumulator, not the child
    # The child's own claims are untrusted; duration is measured by the TUI, calls and
    # spend are read from the holder the proxy billed through.
    When the guest returns with exit 0
    Then the summary calls figure equals the holder call counter
    And the summary spend figure equals the holder Spent()

  Scenario: The user had the mouse off — the return respects m.mouseOff
    Given the user disabled the mouse before the handoff
    When the guest returns with exit 0
    Then the defensive reset still disabled the guest's mouse modes
    And mouse reporting is NOT re-enabled

  Scenario: Exit 130 is a clean close, not an error
    When the guest returns with exit 130
    Then the transcript shows "the guest dropped off (exit 130) - back at the desk"
    And the note is calm, with no error styling escalation beyond the house red ✕
    And the terminal restore and summary still run

  Scenario: A non-zero guest exit is a drop-off, same restore path
    When the guest returns with exit 1
    Then the transcript shows "the guest dropped off (exit 1) - back at the desk"
    And the terminal reset preamble ran
    And the scratch config was cleaned up

  Scenario: A guest that crashed (killed by signal) still restores the desk
    When the guest is killed mid-session
    Then the terminal reset preamble ran
    And the summary line still renders with the accumulated calls and spend
    And the scratch config was cleaned up

  Scenario: A spawn failure never leaves the TUI suspended
    Given the guest binary vanishes between detection and exec
    When the user runs "/operator opencode"
    Then the TUI is painting again
    And the transcript shows an error note naming the launch failure
    And no scratch dir remains

  Scenario: The scratch config is cleaned up on every return path
    When the guest returns with any exit
    Then no rogerai-operator scratch dir remains for this session

  # ── money mid-session ────────────────────────────────────────────────────────────

  Scenario: Budget reached mid-session — clean 402s inside, honest summary outside
    # The proxy side is Phase 1 (features/proxy/budget.feature: OpenAI-shaped 402 at the
    # ceiling). Phase 2 owns the RETURN: the summary must say the budget was reached so
    # "the guest went quiet" is never a mystery.
    Given the guest spends up to the session budget during the handoff
    When the guest returns
    Then the summary notes the session budget was reached
    And the summary spend figure is at or just past the default session budget

  Scenario: Band goes off-air mid-handoff — the guest saw 502s, the summary stays honest
    # Phase 1 already shapes relay failures as OpenAI JSON errors. The return summary
    # reports the spend that actually settled; it does not invent an error state.
    Given the band goes off-air while the guest has the mic
    When the guest returns with exit 1
    Then the summary shows the spend accumulated before the drop
    And the desk is fully usable for a re-tune

  # ── sequencing ───────────────────────────────────────────────────────────────────

  Scenario: Re-tune during a handoff is impossible by construction
    # tea.ExecProcess suspends the whole event loop: no key reaches the TUI while the
    # guest has the terminal, so there is no code path that could re-tune mid-handoff.
    # Pinned so nobody adds a background re-tune that breaks the assumption.
    When the guest has the mic
    Then no TUI key handling runs until the exec callback returns

  Scenario: Two rapid handoffs are independent sessions
    When the guest returns after spending "$0.50"
    And the user immediately runs "/operator opencode" again
    Then the second handoff gets a fresh scratch dir
    And the holder spend reads $0.00 again
    And the holder call counter reads 0 again
    And the session key is unchanged

  Scenario: A second handoff to a different guest reuses nothing from the first
    Given a detected guest "aider" as well
    When the guest "opencode" returns
    And the user runs "/operator aider"
    Then the aider wiring carries no opencode artifacts
    And the first session's scratch dir is already gone

  # ── iteration-1 fix-pass regressions (2026-07) ──────────────────────────────────

  Scenario: The band dropping during the staging beat aborts at exec time
    # Finding #3: startOperatorHandoff gates on Connected() but the exec callback only
    # nil-checked the holder - a band drop inside the 450ms staging beat launched the
    # guest into a wall of 502/503s. The channel precondition is re-checked at exec
    # time, aborting with the same styling and an honest note.
    Given the handoff to "opencode" is staged but not yet execed
    When the band drops during the staging beat
    And the staging beat elapses
    Then no child process is launched
    And the transcript notes "the channel dropped while patching"
    And the desk is fully usable for a re-tune

  Scenario: Bracketed paste is re-armed on every return from a guest
    # Finding #2: the defensive reset preamble ends with ESC[?2004l and runs AFTER
    # bubbletea's RestoreTerminal already re-enabled paste - so the first handoff killed
    # bracketed paste for the rest of the radio session (multi-line pastes fired line by
    # line as separate prompts). The radio always runs with paste on, so the return
    # command set re-enables it unconditionally, after the defensive reset.
    When the guest returns with exit 0
    Then the terminal reset preamble ran
    And bracketed paste is re-enabled in the return command set
