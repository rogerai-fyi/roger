# Post-v5.4.6 visual/action polish — founder captures + live review 2026-07-29.
#
# This feature intentionally separates decoration from behavior:
# - colored surfaces improve hierarchy but never carry truth by color alone;
# - Right Arrow accepts a deterministic next-action hint but never sends it;
# - one tool invocation owns one stateful transcript card;
# - idle instrumentation and duplicate mascots do not compete with the ask surface.

Feature: Crisp actionable TUI surfaces
  RogerAI should make live state and the next useful action obvious without adding noise,
  stealing editing keys, fabricating work, or hiding information in color.

  Rule: ON AIR is a distinct truthful surface

    Scenario: A live provider panel has a solid background
      Given one shared band has a broker-acknowledged live heartbeat
      When the browse view renders the ON AIR provider panel
      Then every visible row in the panel uses the same solid live-surface background
      And the ON AIR label, band facts, totals, earnings, and stop action remain readable

    Scenario: A rejected heartbeat never receives the live surface
      Given a shared band whose broker heartbeat is rejected
      When the provider panel renders
      Then it does not claim "ON AIR"
      And it does not use the live ON AIR background

    Scenario Outline: The live surface fits without bleeding into surrounding chrome
      Given one shared band has a broker-acknowledged live heartbeat
      When the terminal is <width> columns wide
      Then no ON AIR panel row exceeds <width> cells
      And the background ends at the panel boundary
      And the command prompt and global footer keep their own background

      Examples:
        | width |
        | 40    |
        | 80    |
        | 120   |
        | 190   |

    Scenario: ON AIR remains truthful without color
      Given one shared band has a broker-acknowledged live heartbeat
      And NO_COLOR is active
      Then the panel still says "ON AIR" and exposes "/share off"
      And no ANSI color escapes are emitted

  Rule: The composer is a distinct editing surface

    Scenario Outline: Authored prompt rows share one subtle solid background
      Given the <surface> composer has focus
      And its prompt wraps onto three visual rows
      Then all three authored rows use the same composer background
      And every authored character remains visible
      And the background does not color the transcript or global footer

      Examples:
        | surface |
        | AGENT   |
        | TUNE IN |

    Scenario: Empty AGENT shows a context-derived suggestion as ghost text
      Given an AGENT answer completed successfully after writing a file
      And the ask composer is focused and empty
      Then the composer suggests "run the tests or review the change"
      And the suggestion is visually quieter than authored text

    Scenario: Right Arrow accepts an empty-composer suggestion without sending
      Given an AGENT answer completed successfully after writing a file
      And the ask composer is focused and empty
      When I press Right Arrow
      Then "run the tests or review the change" becomes authored prompt text
      And no model request is sent
      And pressing Enter afterward sends that exact prompt once

    Scenario: Right Arrow never overwrites authored text
      Given the AGENT composer contains "review only the parser"
      And a next-action suggestion exists
      When I press Right Arrow
      Then normal cursor movement occurs
      And the authored prompt remains "review only the parser"
      And the suggestion is not inserted

    Scenario: Right Arrow does not accept outside the ask composer
      Given transcript focus, a pending confirmation, or the operator picker owns focus
      When I press Right Arrow
      Then no suggestion is inserted
      And the focused surface keeps its existing Right Arrow behavior

    Scenario: Suggestions are deterministic consequences of the last turn
      Then a successful file write suggests validation or review
      And an error suggests retrying or fixing the error
      And an answer ending in a question suggests answering the question
      And a generic successful tool result suggests continuing
      And an ordinary answer with no grounded next action shows no fabricated command

    Scenario: A new turn or cleared session retires the stale suggestion
      Given a next-action suggestion is visible
      When the user starts typing, sends a prompt, runs "/clear", or leaves AGENT
      Then that stale suggestion is no longer offered for Right Arrow acceptance

    Scenario: Suggestion acceptance is safe under wrap, paste, NO_COLOR, and narrow width
      Given a context-derived suggestion longer than one narrow terminal row
      When the user accepts it with Right Arrow at 40 columns
      Then the exact suggestion becomes the prompt value
      And it wraps losslessly within the composer cap
      And NO_COLOR emits no ANSI escapes

  Rule: One tool invocation owns one stateful card

    Scenario: A tool call starts as one running card
      When the agent calls run_shell with command "echo hi"
      Then exactly one run_shell activity card is visible
      And it contains "echo hi" and a running state
      And no separate machinery echo repeats the command

    Scenario: Approval changes the same card instead of adding narration
      Given run_shell "echo hi" is waiting for approval
      When the user approves it
      Then the same activity card shows approved and running
      And no separate "WILCO", question-mark, or duplicate command line is appended

    Scenario: Completion changes the same card to success
      Given approved run_shell "echo hi" is running
      When it succeeds with 2 bytes of output
      Then exactly one run_shell activity card is visible
      And it shows approved, successful, and "2 bytes"
      And full output remains available behind the transcript detail toggle

    Scenario: Denial and failure remain distinct truthful terminal states
      Given run_shell "echo hi" is waiting for approval
      When it is denied
      Then its single activity card shows denied and never claims it ran
      When a separately approved run_shell fails
      Then its single activity card shows failed and never claims success

  Rule: Idle landing chrome yields to the work surface

    Scenario: Session rail is absent before the first turn
      Given AGENT is idle on its untouched landing
      Then the deck does not show an all-zero SESSION, STEPS, or SPENT rail

    Scenario: Session rail appears with real activity
      Given the first AGENT turn has started
      Then the deck shows truthful session tokens, step progress, and spend
      And an unknown current step uses "·/8", never "—/8"

    Scenario: The ask seam stays adjacent to the composer
      Given the untouched AGENT landing contains desk availability
      Then no labeled ask seam is orphaned above the desk block
      And the composer remains the first element immediately below any labeled ask seam

    Scenario: The landing shows one Roger mascot
      Given the untouched AGENT landing contains the standing-by corner Ping
      Then the unfocused desk availability block contains no second mascot
      And focusing the operator desk may reveal the selected operator plate

  Rule: Render paths do not mutate shared textarea viewport geometry

    Scenario Outline: Rendering a composer is observational
      Given a <surface> composer with a wrapped value, cursor position, width, height, and viewport
      When its prompt lines are rendered repeatedly
      Then its value, cursor position, width, height, and viewport remain unchanged

      Examples:
        | surface |
        | AGENT   |
        | TUNE IN |
