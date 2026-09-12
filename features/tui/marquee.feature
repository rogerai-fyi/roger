# SELECTED-CELL MARQUEE — read the whole name without widening the column.
#
# The band table elides a long model name to fit its column ("deepseek/deepseek-v…").
# The founder's ask: when a row is HIGHLIGHTED, scroll that cell horizontally so the
# whole name can eventually be read. Everything else stays exactly as it is.
#
# The five invariants this spec exists to hold, in order of how much damage breaking
# them would do:
#
#   1. FRAME ZERO IS TODAY'S RENDER, BYTE FOR BYTE. The marquee starts from the very
#      string pad() / truncVisible() already produce and only then begins to move. Dozens
#      of existing tests assert exact padded row text at fixed widths; not one of them may
#      need editing. Frame zero DELEGATES to the static builder rather than re-deriving it,
#      so the identity is true by construction, not by coincidence.
#   2. THE COLUMN NEVER GROWS. Only the window onto the text moves. No frame, at any
#      width, may render more (or fewer) columns than the static cell did.
#   3. IT ONLY MOVES WHEN IT HAS TO. A name that fits never twitches; an unselected row
#      never twitches; a screen you are not looking at, and a list that does not have
#      focus, never twitch. A jittering list is worse than a truncated one.
#   4. NOTHING IS EVER CUT MID-CHARACTER. The window steps in GRAPHEME CLUSTERS, so a
#      multi-byte rune or an emoji sequence is never sliced in half at any offset.
#   5. THE CLOCK IS A SEAM. The scroll is a pure function of the carrier-beat frame
#      counter (m.frame) and the frame the current cell became selected (m.marqFrame).
#      Tests advance the counter; nothing sleeps, nothing reads the wall clock. This repo
#      has already paid for three wall-clock-racing tests once.
#
# The moving form keeps the static form's own tail marker, which is why there are two
# flavours: pad() ends an elided cell in "…", truncVisible() hard-cuts with no marker at
# all (the band-name half of a quanted "name Q4_K_M" cell, where the quant must stay
# pinned on the right because it is half the row's identity). Each flavour's frame zero
# matches its own static builder.

Feature: The selected row's elided cell marquees
  A highlighted cell whose text does not fit scrolls it into view, one column at a
  time, and the rest of the table stands perfectly still.

  # ── frame zero: the identity that protects every existing assertion ──────────

  Scenario: Frame zero of an overflowing cell is exactly what pad renders today
    Given the name "deepseek/deepseek-v3.1-terminus" in a 20-column cell
    Then frame 0 is byte-identical to the static padded cell
    And frame 0 ends in an ellipsis

  Scenario: Frame zero of a fitting cell is exactly what pad renders today
    Given the name "gpt-oss-20b" in a 20-column cell
    Then frame 0 is byte-identical to the static padded cell
    And frame 0 is padded with trailing spaces to 20 columns

  Scenario: A name that fits never moves, at any offset
    Given the name "gpt-oss-20b" in a 20-column cell
    Then every offset from 0 to 40 renders the static padded cell

  Scenario: A name exactly as long as its column never moves
    Given the name "qwen3-coder-30b-a3b0" in a 20-column cell
    Then the cell has 0 columns to travel
    And every offset from 0 to 40 renders the static padded cell

  # ── the boundary: exactly one column too long ────────────────────────────────

  Scenario: One column too long has exactly one column to travel
    Given the name "qwen3-coder-30b-a3b01" in a 20-column cell
    Then the cell has 1 columns to travel

  Scenario: One column too long elides at frame zero and reads whole at its last offset
    Given the name "qwen3-coder-30b-a3b01" in a 20-column cell
    Then frame 0 is byte-identical to the static padded cell
    And offset 1 renders "wen3-coder-30b-a3b01"

  # ── the window: width, direction, and the end of the road ────────────────────

  Scenario: Every offset renders exactly the column width
    Given the name "meta-llama/llama-3.1-70b-instruct-turbo" in a 20-column cell
    Then every offset from 0 to 60 renders exactly 20 columns

  Scenario: The window advances exactly one column per offset
    Given the name "meta-llama/llama-3.1-70b-instruct-turbo" in a 20-column cell
    Then offset 1 renders "eta-llama/llama-3.1…"
    And offset 2 renders "ta-llama/llama-3.1-…"
    And offset 3 renders "a-llama/llama-3.1-7…"

  Scenario: The last offset shows the tail in full, with no ellipsis
    Given the name "meta-llama/llama-3.1-70b-instruct-turbo" in a 20-column cell
    Then the last offset renders "3.1-70b-instruct-turbo" trimmed to 20 columns
    And the last offset does not end in an ellipsis

  Scenario: An offset past the end clamps to the tail and never runs off it
    Given the name "meta-llama/llama-3.1-70b-instruct-turbo" in a 20-column cell
    Then every offset from 19 to 200 renders the same cell as the last offset

  # ── geometry: the narrow end, where this app has been bitten before ──────────

  Scenario: A zero-width column renders nothing rather than panicking
    Given the name "deepseek/deepseek-v3.1-terminus" in a 0-column cell
    Then every offset from 0 to 40 renders exactly 0 columns

  Scenario: A negative-width column renders nothing rather than panicking
    Given the name "deepseek/deepseek-v3.1-terminus" in a -3-column cell
    Then every offset from 0 to 40 renders exactly 0 columns

  Scenario: The narrowest real column is one cell wide and still never overflows
    Given the name "deepseek/deepseek-v3.1-terminus" in a 1-column cell
    Then every offset from 0 to 60 renders exactly 1 columns
    And frame 0 is byte-identical to the static padded cell

  Scenario: A two-column cell still never overflows
    Given the name "deepseek/deepseek-v3.1-terminus" in a 2-column cell
    Then every offset from 0 to 60 renders exactly 2 columns

  # ── never cut mid-character ──────────────────────────────────────────────────

  Scenario: A multi-byte name is never split mid-rune
    Given the name "モデル・ディープシーク・ターミナス" in a 12-column cell
    Then no offset from 0 to 60 splits a rune

  Scenario: An emoji name is never split mid-grapheme-cluster
    Given the name "rocket-🚀-model-👩‍🚀-astronaut-🛰-relay" in a 16-column cell
    Then no offset from 0 to 60 splits a rune
    And no offset from 0 to 60 splits a grapheme cluster

  Scenario: A combining-mark name is never split mid-cluster
    Given the name "café-model-naïve-weights-édition" in a 14-column cell
    Then no offset from 0 to 60 splits a grapheme cluster

  # ── no ANSI, and the ASCII fold ──────────────────────────────────────────────

  Scenario: No frame carries an ANSI escape
    Given the name "meta-llama/llama-3.1-70b-instruct-turbo" in a 20-column cell
    Then no offset from 0 to 60 carries an ANSI escape

  Scenario: Under NO_COLOR the marquee still moves and still carries no ANSI
    Given NO_COLOR is set
    And the name "meta-llama/llama-3.1-70b-instruct-turbo" in a 20-column cell
    Then offset 1 differs from frame 0
    And no offset from 0 to 60 carries an ANSI escape

  Scenario: Under the ASCII fold frame zero still matches the static cell
    Given ROGERAI_ASCII is set
    And the name "meta-llama/llama-3.1-70b-instruct-turbo" in a 20-column cell
    Then frame 0 is byte-identical to the static padded cell
    And every offset from 0 to 60 renders exactly 20 columns

  # ── the hard-cut flavour: a quanted band, where the quant stays pinned ───────

  Scenario: The hard-cut flavour's frame zero is exactly what truncVisible renders
    Given the name "deepseek/deepseek-v3.1-terminus" in a 13-column hard-cut cell
    Then frame 0 is byte-identical to the static hard-cut cell
    And frame 0 does not end in an ellipsis

  Scenario: The hard-cut flavour never shows an ellipsis at any offset
    Given the name "deepseek/deepseek-v3.1-terminus" in a 13-column hard-cut cell
    Then no offset from 0 to 60 ends in an ellipsis
    And every offset from 0 to 60 renders exactly 13 columns

  # ── the rhythm: hold, scroll, hold, wrap ─────────────────────────────────────

  Scenario: A cell with nowhere to go never leaves column zero
    Given a marquee with 0 columns to travel
    Then the offset is 0 at every elapsed frame from 0 to 200

  Scenario: It holds at the start so the eye can catch the beginning
    Given a marquee with 10 columns to travel
    Then the offset is 0 for the whole start hold

  Scenario: It advances one column per step after the start hold
    Given a marquee with 10 columns to travel
    Then the offset is 1 on the first frame after the start hold
    And the offset advances by exactly one column per step through the scroll

  Scenario: It holds at the end on the last column
    Given a marquee with 10 columns to travel
    Then the offset is 10 for the whole end hold

  Scenario: It returns to the start and repeats
    Given a marquee with 10 columns to travel
    Then the offset is 0 again one frame after the cycle ends
    And the offset at every elapsed frame equals the offset one cycle later

  Scenario: A negative elapsed frame count is frame zero, not a crash
    Given a marquee with 10 columns to travel
    Then the offset is 0 at elapsed frame -5

  # ── the model: only the selected cell, on the screen you are looking at ──────

  Scenario: The selected band's long name scrolls
    Given a band list with a long-named band selected
    Then the marquee is running
    And the selected band row changes as the frame advances

  Scenario: The unselected rows never move
    Given a band list with a long-named band selected
    Then the unselected band rows are byte-identical as the frame advances

  Scenario: A selected band whose name fits does not scroll
    Given a band list with a short-named band selected
    Then the marquee is not running
    And the whole band view is byte-identical as the frame advances

  Scenario: An empty band list has nothing to scroll
    Given a band list with no bands
    Then the marquee is not running

  Scenario: A screen that is not the band table does not scroll it
    Given a band list with a long-named band selected
    And the operator is on the CHANNEL screen
    Then the marquee is not running

  Scenario: The band list does not scroll while the filter has focus
    Given a band list with a long-named band selected
    And the operator is typing a filter
    Then the marquee is not running

  Scenario: Compact windowshade freezes the marquee at frame zero
    Given a band list with a long-named band selected
    And the windowshade is down
    Then the marquee offset is 0

  Scenario: Moving the selection resets the marquee to frame zero
    Given a band list with a long-named band selected
    And the marquee has scrolled off frame zero
    When the operator moves the selection down one row
    Then the marquee offset is 0

  Scenario: Staying on the same row keeps the marquee scrolling
    Given a band list with a long-named band selected
    And the marquee has scrolled off frame zero
    When the frame advances without moving the selection
    Then the marquee offset is not 0

  Scenario: A running marquee keeps the carrier beat alive
    Given a band list with a long-named band selected
    Then the frame clock is animating

  Scenario: A fitting selected cell lets the carrier beat freeze
    Given a band list with a short-named band selected
    Then the frame clock is not animating

  Scenario: No band row overflows the terminal at any frame
    Given a band list with a long-named band selected
    Then no rendered line exceeds the terminal width at any offset

  Scenario: The band table stays width-safe on a narrow terminal
    Given a band list with a long-named band selected
    And the terminal is 40 columns wide
    Then no rendered line exceeds the terminal width at any offset

  # ── the SHARE table gets the same treatment ──────────────────────────────────

  Scenario: The selected SHARE row's long model name scrolls
    Given a SHARE table with a long-named model selected
    Then the marquee is running
    And the selected SHARE row changes as the frame advances

  Scenario: A short SHARE model name does not scroll
    Given a SHARE table with a short-named model selected
    Then the marquee is not running

  Scenario: The SHARE table does not scroll while the station rename has focus
    Given a SHARE table with a long-named model selected
    And the operator is renaming the station
    Then the marquee is not running

  Scenario: No SHARE row overflows the terminal at any frame
    Given a SHARE table with a long-named model selected
    Then no rendered line exceeds the terminal width at any offset
