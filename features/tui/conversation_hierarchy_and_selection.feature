# Founder captures + independent review, 2026-07-29.
#
# This spec makes conversation roles scannable, closes the over-cap composer
# viewport regression, and adds an honest application-owned select-to-copy mode
# without taking native terminal selection away by default.

Feature: Clean conversation hierarchy and deliberate text selection
  RogerAI should make a prompt, its answer, and its metadata instantly distinguishable,
  keep the active editing row visible at every draft size, and offer exact select-to-copy
  feedback only when Roger actually owns the selection.

  Rule: User turns and station answers have distinct, consistent shapes

    Scenario: A short TUNE IN exchange exposes both speaker roles
      Given a user sent "count the words in this sentence"
      When the station replies "There are six words."
      Then the user turn is a warm authored band labeled "YOU"
      And the answer begins with a cool "ROGER" role marker and a continuous answer gutter
      And at least one structural separator distinguishes the two blocks
      And the answer text is never mistaken for reply metadata

    Scenario: Answer metadata belongs to the answer without competing with it
      Given a station answer has provider, token, speed, elapsed, and cost facts
      Then the prose appears before the dim metadata
      And metadata is indented under the answer gutter or footer
      And session totals are visually quieter than the prose
      And no fact is repeated in both the answer header and footer

    Scenario: Consecutive turns remain scannable
      Given the transcript contains three user and station turn pairs
      Then every user block has the same warm authored shape
      And every station block has the same cool answer shape
      And a turn boundary separates one answer footer from the next user block
      And the transcript does not add a full box around every message

    Scenario: Multiline prose and code stay inside one station block
      Given a station answer contains prose, a fenced code block, and a diff
      Then one continuous answer gutter groups the entire answer
      And code and diff colors remain readable inside that block
      And the role marker appears once, not on every wrapped row
      And copying the raw reply excludes decorative role and gutter glyphs

    Scenario Outline: Conversation hierarchy remains usable at responsive widths
      Given a user turn and a multiline station answer
      When the terminal is <width> columns wide
      Then every authored character and answer character remains visible
      And no rendered row exceeds <width> cells
      And role labels degrade before content is clipped

      Examples:
        | width |
        | 40    |
        | 80    |
        | 120   |
        | 190   |

    Scenario: Conversation hierarchy survives NO_COLOR and mono palette
      Given a user turn and a station answer
      And NO_COLOR or the mono palette is active
      Then "YOU", "ROGER", gutters, and spacing still distinguish the roles
      And no ANSI color escapes are emitted under NO_COLOR

    Scenario: AGENT and TUNE IN share the same answer grammar
      Given equivalent plain-text answers appear in AGENT and TUNE IN
      Then both use the same assistant role, gutter, prose, code, and metadata hierarchy
      And AGENT tool cards remain separate from assistant prose

  Rule: A draft taller than the composer cap follows the active cursor row

    Scenario Outline: A long pasted draft keeps its tail visible
      Given the <surface> composer cap is six visual rows
      When a ten-row draft is pasted with the cursor on visual row ten
      Then the rendered composer shows the cursor row
      And the rendered window shows the six-row tail of the draft
      And the hidden first rows remain intact in the prompt value

      Examples:
        | surface |
        | AGENT   |
        | TUNE IN |

    Scenario Outline: Cursor navigation moves the over-cap window
      Given the <surface> composer contains ten visual rows
      When the cursor moves from the final row to the first row
      Then the visible six-row window follows upward until the first row is visible
      When the cursor moves back to the final row
      Then the visible six-row window follows downward until the final row is visible

      Examples:
        | surface |
        | AGENT   |
        | TUNE IN |

    Scenario: Over-cap rendering is lossless for narrow and wide characters
      Given a ten-row draft containing ASCII, emoji, CJK, tabs, and explicit newlines
      When it renders at 40 columns and at 190 columns
      Then the cursor row remains visible at both widths
      And joining the logical prompt value reproduces every original character
      And no wide character is split into invalid output

    Scenario: Shrinking an over-cap draft heals the visible window
      Given a ten-row draft has scrolled its composer window to the tail
      When editing reduces the draft to two visual rows
      Then both remaining rows are visible
      And no stale viewport offset hides the first row

    Scenario: Rendering remains observational above the cap
      Given an over-cap composer with a value, cursor, width, height, and viewport
      When its prompt lines render repeatedly
      Then the live editor value, cursor, width, height, and viewport remain unchanged

  Rule: Grounded suggestions advertise their keyboard action

    Scenario: An armed suggestion teaches Right Arrow without becoming authored text
      Given a grounded next-action suggestion is visible in an empty focused AGENT composer
      Then a quiet "→ accept" affordance is visible beside or below the ghost suggestion
      And neither the affordance nor suggestion is part of the prompt value

    Scenario: Suggestion affordance retires with the suggestion
      Given "→ accept" is visible
      When the user types, accepts, sends, clears, leaves AGENT, or changes focus
      Then "→ accept" disappears

    Scenario: Suggestion discoverability remains safe at narrow width and NO_COLOR
      Given a long grounded suggestion at 40 columns
      Then the suggestion keeps priority over the "→ accept" affordance
      And the affordance may fold to the next row but never clips authored content
      And NO_COLOR emits no ANSI escapes

  Rule: Approval recovery still owns one stateful tool card

    Scenario: Missing activity bookkeeping creates one recoverable card
      Given a tool approval resolves after its activity index became unavailable
      When the tool is approved
      Then exactly one approved-running card is created
      And that card becomes the eventual success or failure card in place
      And no WILCO or second result card is appended

  Rule: Native selection remains the safe default

    Scenario: Default mouse ownership remains with the terminal
      Given Roger starts with native selection enabled
      Then ordinary terminal drag selection remains available in every view
      And Roger does not claim it copied text it cannot observe
      And ctrl+o or "/mouse" offers smart mouse mode

    Scenario: Idle rendering does not erase native terminal selection
      Given native terminal text is selected while Roger is idle
      Then animation and discovery ticks do not repaint the frame
      And the native selection remains highlighted until the terminal clears it

  Rule: Smart mouse mode copies an application-owned selection on release

    Scenario: Dragging transcript text copies the exact visible selection
      Given smart mouse mode is enabled
      And the transcript contains "There are six words."
      When the user drags from "There" through "words." and releases
      Then Roger highlights the selected cells during the drag
      And "There are six words." is written to the clipboard exactly once
      And a toast says "Copied 20 characters to clipboard"

    Scenario: Selection may span wrapped rows and message blocks
      Given smart mouse mode is enabled
      And visible transcript text wraps across three terminal rows
      When the user drags forward across those rows
      Then copied text follows terminal reading order
      And soft-wrap boundaries do not inject newlines
      And explicit source newlines are preserved
      And decorative gutters, role labels, ANSI escapes, and padding are excluded

    Scenario: Reverse dragging produces the same ordered text
      Given smart mouse mode is enabled
      When the user drags from a later cell to an earlier cell and releases
      Then the normalized copied text is ordered from the earlier cell to the later cell
      And the character count describes the copied Unicode characters

    Scenario: Wide characters and combining marks count as text, not terminal cells
      Given smart mouse mode is enabled
      And the selection contains emoji, CJK, and combining characters
      When the selection is released
      Then no character is split or corrupted
      And the toast count uses Unicode characters rather than occupied terminal cells

    Scenario: Dragging outside transcript text does not fabricate content
      Given smart mouse mode is enabled
      When a drag begins or ends in panel padding, borders, or empty space
      Then only real selectable text is copied
      And an empty normalized selection performs no clipboard write
      And an empty selection shows no success toast

    Scenario: A click without a drag does not copy
      Given smart mouse mode is enabled
      When the user presses and releases on one cell without selecting a character
      Then no clipboard write occurs
      And normal click behavior for that surface remains available

    Scenario: Mouse wheel scrolling and keyboard editing survive smart selection
      Given smart mouse mode is enabled
      Then the wheel still scrolls the active transcript
      And dragging in the transcript never changes composer text
      And keyboard input, paste, history, and confirmations retain their existing behavior

    Scenario: Smart selection is cancelled safely
      Given a smart selection drag is in progress
      When the user presses escape, changes mode, resizes, or loses mouse capture
      Then the transient highlight is cleared
      And no partial selection is copied

    Scenario: Native selection can always be restored
      Given smart mouse mode is enabled
      When the user presses ctrl+o or runs "/mouse" again
      Then mouse reporting is disabled
      And ordinary terminal drag selection works again
      And the status line says native selection is on

    Scenario: Clipboard failure is honest
      Given smart mouse mode owns a non-empty selection
      And no clipboard mechanism is available
      When the selection is released
      Then Roger never shows a copied-success toast
      And the selected text remains visibly recoverable
      And the status explains that no clipboard tool or OSC 52 path succeeded
